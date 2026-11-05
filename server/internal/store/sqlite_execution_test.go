package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

type sqliteExecutionFixture struct {
	path       string
	repository *SQLite
	user       domain.User
	computer   domain.Computer
	service    domain.AgentService
	project    domain.Project
	agent      domain.Agent
	issue      domain.Issue
	instance   domain.IssueAgentInstance
}

func newSQLiteExecutionFixture(t *testing.T) sqliteExecutionFixture {
	t.Helper()
	ctx := context.Background()
	base := newSQLiteAgentTestFixture(t)
	project, err := base.repository.CreateProject(ctx, base.user.ID, domain.Project{ComputerID: base.computer.ID, Name: "execution"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := base.repository.CreateAgent(ctx, base.user.ID, domain.Agent{
		ComputerID: base.computer.ID, AgentServiceID: base.service.ID,
		Name: "runner", Status: domain.AgentActive, Model: "test/model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.repository.CreateAgentRevision(ctx, base.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: base.service.ID, Model: "test/model", SystemPrompt: "system",
	}, nil); err != nil {
		t.Fatal(err)
	}
	issue, err := base.repository.CreateIssue(ctx, base.user.ID, domain.Issue{ProjectID: project.ID, Title: "execute"})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := base.repository.EnsureIssueAgentInstance(ctx, base.user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sqliteExecutionFixture{path: base.path, repository: base.repository, user: base.user, computer: base.computer,
		service: base.service, project: project, agent: agent, issue: issue, instance: instance}
}

func (f sqliteExecutionFixture) createRun(t *testing.T, key string) domain.Run {
	t.Helper()
	run, replayed, err := f.repository.CreateRun(context.Background(), f.user.ID, f.issue.ID,
		f.issue.Version, key, domain.Run{IssueAgentInstanceID: f.instance.ID,
			AgentID: f.agent.ID, Input: []domain.ContentBlock{{Type: "text", Text: "work"}}})
	if err != nil || replayed {
		t.Fatalf("create run: replayed=%t err=%v", replayed, err)
	}
	return run
}

func TestSQLiteComputerUseFlagComesThroughRunStartPayload(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	if _, err := f.repository.db.ExecContext(ctx, `
		UPDATE computers SET computer_use_enabled = 1 WHERE id = ?
	`, f.computer.ID); err != nil {
		t.Fatal(err)
	}
	run, _, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, f.issue.Version,
		"computer-use-flag", domain.Run{
			AgentID: f.agent.ID,
			Input:   []domain.ContentBlock{{Type: "text", Text: "work"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT payload FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ComputerUse {
		t.Fatalf("run.start payload did not carry ComputerUse: %+v", payload)
	}
	// 关闭开关后新 Run 不得携带（使用新 Issue，避免同会话幂等复用首个 Run）。
	second, err := f.repository.CreateIssue(ctx, f.user.ID, domain.Issue{
		ProjectID: f.project.ID, Title: "Computer Use Off", Description: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	var disabled int
	if _, err := f.repository.db.ExecContext(ctx, `
		UPDATE computers SET computer_use_enabled = 0 WHERE id = ?
	`, f.computer.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT computer_use_enabled FROM computers WHERE id = ?
	`, f.computer.ID).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 0 {
		t.Fatalf("post-disable state=%d", disabled)
	}
	run, replayed, err := f.repository.CreateRun(ctx, f.user.ID, second.ID, second.Version,
		"computer-use-flag-off", domain.Run{
			AgentID: f.agent.ID,
			Input:   []domain.ContentBlock{{Type: "text", Text: "work"}},
		})
	if err != nil || replayed {
		t.Fatalf("second create run: replayed=%t err=%v", replayed, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT payload FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	payload = nodeprotocol.StartRunPayload{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ComputerUse {
		t.Fatalf("run.start payload carried ComputerUse after disable: %+v", payload)
	}
}

func TestSQLiteCreateRunAtomicAndOutboxStableRetry(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "atomic")
	var generations, events, commands int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM issue_agent_generations WHERE issue_agent_instance_id = ?`, f.instance.ID).Scan(&generations); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM run_events WHERE run_id = ?`, run.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE json_extract(payload, '$.run_id') = ?`, run.ID).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if generations != 1 || events != 1 || commands != 1 {
		t.Fatalf("partial execution envelope: generations=%d events=%d commands=%d", generations, events, commands)
	}
	replayed, wasReplayed, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, 999, "atomic", domain.Run{})
	if err != nil || !wasReplayed || replayed.ID != run.ID {
		t.Fatalf("idempotent replay = %+v replayed=%t err=%v", replayed, wasReplayed, err)
	}
	first, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || first.Name != "run.start" {
		t.Fatalf("first claim = %+v ok=%t err=%v", first, ok, err)
	}
	if err := f.repository.RetryLocalCommand(ctx, first.ID, first.ClaimToken, "transient", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	second, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC().Add(time.Millisecond), time.Minute)
	if err != nil || !ok || second.ID != first.ID || second.Attempts != 2 {
		t.Fatalf("retry claim = %+v ok=%t err=%v", second, ok, err)
	}
}

func TestSQLiteRunDoesNotSelectSingleResourcePath(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()

	for _, resource := range []domain.ProjectResource{
		{ProjectID: f.project.ID, Kind: "code_directory", Name: "legacy", Path: "legacy-resource-id"},
		{ProjectID: f.project.ID, Kind: "code_directory", Name: "source", Path: "/tmp/teamva-source"},
	} {
		created, err := f.repository.CreateResource(ctx, f.user.ID, resource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.repository.UpdateResource(ctx, f.user.ID, created.ID, created.Version, func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = true
		}); err != nil {
			t.Fatal(err)
		}
	}

	run := f.createRun(t, "absolute-resource")
	var selectedPath any
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT json_extract(payload, '$.path')
		FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&selectedPath); err != nil {
		t.Fatal(err)
	}
	if selectedPath != nil {
		t.Fatalf("run.start unexpectedly selected one resource path: %v", selectedPath)
	}
}

func TestSQLiteRunSendsAllProjectWorkspaceResources(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	resources := make([]domain.ProjectResource, 0, 2)
	for _, input := range []domain.ProjectResource{
		{ProjectID: f.project.ID, Kind: "code_directory", Name: "First", Path: "/tmp/teamva-first"},
		{ProjectID: f.project.ID, Kind: "code_directory", Name: "Second", Path: "/tmp/teamva-second"},
	} {
		created, err := f.repository.CreateResource(ctx, f.user.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		created, err = f.repository.UpdateResource(ctx, f.user.ID, created.ID, created.Version, func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = true
		})
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, created)
	}
	run, replayed, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, f.issue.Version,
		"repository-payload", domain.Run{
			IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
			Input: []domain.ContentBlock{
				{Type: "text", Text: "work"},
				{Type: "image", MimeType: "image/png", Filename: "capture.png", DataURL: tinyPNGDataURL},
			},
		})
	if err != nil || replayed {
		t.Fatalf("run=%+v replayed=%t err=%v", run, replayed, err)
	}
	var raw string
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT payload FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	var payloadObject map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payloadObject); err != nil {
		t.Fatal(err)
	}
	_, hasCWD := payloadObject["cwd"]
	_, hasPath := payloadObject["path"]
	if hasCWD || hasPath || len(payload.GitResources) != 2 {
		t.Fatalf("run.start payload=%+v", payload)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].DataURL != tinyPNGDataURL {
		t.Fatalf("run.start attachments=%+v", payload.Attachments)
	}
	for index, resource := range resources {
		if payload.GitResources[index].ResourceID != resource.ID ||
			payload.GitResources[index].Name != resource.Name ||
			payload.GitResources[index].Path != resource.Path {
			t.Fatalf("git resources=%+v", payload.GitResources)
		}
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restoredIssue, err := reopened.GetIssue(ctx, f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredRun, err := reopened.GetRun(ctx, f.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredIssue.ID != f.issue.ID || restoredRun.ProjectID != f.project.ID {
		t.Fatalf("restored issue=%+v run=%+v", restoredIssue, restoredRun)
	}
	if len(restoredRun.Input) != 2 || restoredRun.Input[1].Filename != "capture.png" || restoredRun.Input[1].DataURL != "" {
		t.Fatalf("restored run leaked or lost attachment metadata: %+v", restoredRun.Input)
	}
}

func TestSQLiteScheduleSendsAllProjectWorkspaceResources(t *testing.T) {
	for _, test := range []struct {
		name  string
		paths []string
	}{
		{
			name: "two resources", paths: []string{"/tmp/schedule-first", "/tmp/schedule-second"},
		},
		{
			name: "one resource with nested repositories", paths: []string{"/tmp/schedule-monorepo"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			resources := make([]domain.ProjectResource, 0, len(test.paths))
			for index, resourcePath := range test.paths {
				created, err := f.repository.CreateResource(ctx, f.user.ID, domain.ProjectResource{
					ProjectID: f.project.ID, Kind: "code_directory",
					Name: fmt.Sprintf("Resource %d", index+1), Path: resourcePath,
				})
				if err != nil {
					t.Fatal(err)
				}
				created, err = f.repository.UpdateResource(ctx, f.user.ID, created.ID, created.Version,
					func(item *domain.ProjectResource) {
						item.Status = "ready"
						item.GitDetected = true
					})
				if err != nil {
					t.Fatal(err)
				}
				resources = append(resources, created)
			}
			task, err := f.repository.CreateScheduledTask(ctx, f.user.ID, domain.ScheduledTask{
				ProjectID: f.project.ID, Name: test.name,
				ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "UTC",
				Prompt: []domain.ContentBlock{{Type: "text", Text: "scheduled work"}}, AgentID: f.agent.ID,
				ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
			})
			if err != nil {
				t.Fatal(err)
			}
			trigger, replayed, err := f.repository.TriggerScheduledTask(ctx, task.ID,
				"manual:"+test.name, time.Now().UTC())
			if err != nil || replayed {
				t.Fatalf("trigger=%+v replayed=%t err=%v", trigger, replayed, err)
			}
			run, err := f.repository.GetRun(ctx, f.user.ID, trigger.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := f.repository.db.QueryRowContext(ctx, `
				SELECT payload FROM command_outbox
				WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
			`, run.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload nodeprotocol.StartRunPayload
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			var payloadObject map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &payloadObject); err != nil {
				t.Fatal(err)
			}
			_, hasCWD := payloadObject["cwd"]
			_, hasPath := payloadObject["path"]
			if task.ProjectID != f.project.ID || run.ProjectID != f.project.ID || hasCWD || hasPath ||
				len(payload.GitResources) != len(resources) {
				t.Fatalf("task=%+v run=%+v payload=%+v", task, run, payload)
			}
			for index, resource := range resources {
				if payload.GitResources[index].ResourceID != resource.ID ||
					payload.GitResources[index].Path != resource.Path {
					t.Fatalf("git resources=%+v", payload.GitResources)
				}
			}
		})
	}
}

func TestSQLiteOutboxClaimTokenFencesExpiredWorker(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	f.createRun(t, "claim-fence")
	oldClaim, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), 20*time.Millisecond)
	if err != nil || !ok || oldClaim.ClaimToken == "" {
		t.Fatalf("old claim=%+v ok=%t err=%v", oldClaim, ok, err)
	}
	time.Sleep(40 * time.Millisecond)
	newClaim, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || newClaim.ID != oldClaim.ID || newClaim.ClaimToken == oldClaim.ClaimToken {
		t.Fatalf("new claim=%+v ok=%t err=%v", newClaim, ok, err)
	}
	if err := f.repository.RenewLocalCommandLease(ctx, oldClaim.ID, oldClaim.ClaimToken, time.Now().UTC(), time.Minute); err == nil {
		t.Fatal("expired worker renewed replacement claim")
	}
	if err := f.repository.CompleteLocalCommand(ctx, oldClaim.ID, oldClaim.ClaimToken, json.RawMessage(`{"worker":"old"}`)); err == nil {
		t.Fatal("expired worker completed replacement claim")
	}
	if err := f.repository.RetryLocalCommand(ctx, oldClaim.ID, oldClaim.ClaimToken, "old retry", time.Now()); err == nil {
		t.Fatal("expired worker retried replacement claim")
	}
	if err := f.repository.MarkLocalCommandUncertain(ctx, oldClaim.ID, oldClaim.ClaimToken, "old uncertain"); err == nil {
		t.Fatal("expired worker marked replacement claim uncertain")
	}
	if err := f.repository.FailLocalCommand(ctx, oldClaim.ID, oldClaim.ClaimToken, "old failure", nil); err == nil {
		t.Fatal("expired worker failed replacement claim")
	}
	if err := f.repository.CompleteLocalCommand(ctx, newClaim.ID, newClaim.ClaimToken, json.RawMessage(`{"worker":"new"}`)); err != nil {
		t.Fatal(err)
	}
	stored, err := f.repository.GetLocalCommand(ctx, newClaim.ID)
	if err != nil || stored.Status != LocalCommandCompleted || string(stored.Result) != `{"worker":"new"}` {
		t.Fatalf("stored command=%+v err=%v", stored, err)
	}
}

func TestSQLiteOutboxDoesNotReclaimInFlightCommandAfterSleep(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	f.createRun(t, "sleep-in-flight")
	claimedAt := time.Now().UTC()
	claim, ok, err := f.repository.ClaimLocalCommand(ctx, claimedAt, 20*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("initial claim=%+v ok=%t err=%v", claim, ok, err)
	}
	afterSleep := claimedAt.Add(time.Minute)
	if next, ok, err := f.repository.ClaimLocalCommand(ctx, afterSleep, time.Minute, claim.ID); err != nil || ok {
		t.Fatalf("in-flight command was reclaimed after sleep: %+v ok=%t err=%v", next, ok, err)
	}
	stored, err := f.repository.GetLocalCommand(ctx, claim.ID)
	if err != nil || stored.Status != LocalCommandProcessing || stored.ClaimToken != claim.ClaimToken {
		t.Fatalf("in-flight claim changed: %+v err=%v", stored, err)
	}
	newClaim, ok, err := f.repository.ClaimLocalCommand(ctx, afterSleep, time.Minute)
	if err != nil || !ok || newClaim.ID != claim.ID || newClaim.ClaimToken == claim.ClaimToken {
		t.Fatalf("command was not reclaimable after owner left: %+v ok=%t err=%v", newClaim, ok, err)
	}
}

func TestSQLiteCreateRunRollsBackOnOutboxFailure(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	if _, err := f.repository.db.ExecContext(ctx, `
		CREATE TRIGGER reject_execution_outbox BEFORE INSERT ON command_outbox
		WHEN NEW.command_name = 'run.start' BEGIN SELECT RAISE(ABORT, 'reject outbox'); END
	`); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, f.issue.Version, "rollback",
		domain.Run{IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
			Input: []domain.ContentBlock{{Type: "text", Text: "must rollback"}}})
	if err == nil {
		t.Fatal("run unexpectedly survived rejected outbox insert")
	}
	var runs, generations int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE idempotency_key = 'rollback'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM issue_agent_generations WHERE issue_agent_instance_id = ?`, f.instance.ID).Scan(&generations); err != nil {
		t.Fatal(err)
	}
	instance, err := f.repository.GetIssueAgentInstance(ctx, f.user.ID, f.issue.ID, f.agent.ID)
	if err != nil || runs != 0 || generations != 0 || instance.Status != domain.AgentInstanceIdle {
		t.Fatalf("rollback leaked state: runs=%d generations=%d instance=%+v err=%v", runs, generations, instance, err)
	}
}

func TestSQLiteApplyLocalEventsDuplicateAndGap(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "events")
	now := time.Now().UTC()
	gap := nodeprotocol.RunEvent{ID: "evt_gap", RunID: run.ID, LeaseEpoch: 1, Sequence: 2,
		Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: now}
	cursors, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{gap})
	if err != nil || len(cursors) != 0 {
		t.Fatalf("gap advanced cursor: %+v err=%v", cursors, err)
	}
	started := nodeprotocol.RunEvent{ID: "evt_started", RunID: run.ID, LeaseEpoch: 1,
		Sequence: 1, Type: "run.started", OccurredAt: now}
	cursors, err = f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{started})
	if err != nil || len(cursors) != 1 || cursors[0].Sequence != 1 {
		t.Fatalf("start cursor = %+v err=%v", cursors, err)
	}
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{started}); err != nil {
		t.Fatal(err)
	}
	cursors, err = f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{gap})
	if err != nil || len(cursors) != 1 || cursors[0].Sequence != 2 {
		t.Fatalf("completed cursor = %+v err=%v", cursors, err)
	}
	finished, err := f.repository.GetRun(ctx, f.user.ID, run.ID)
	events, listErr := f.repository.ListRunEvents(ctx, f.user.ID, run.ID)
	if err != nil || listErr != nil || finished.Status != domain.RunSucceeded || len(events) != 3 {
		t.Fatalf("run=%+v events=%+v err=%v listErr=%v", finished, events, err, listErr)
	}
}

func TestSQLiteApplyLocalEventsAckStopsAtBatchGapInsteadOfStoredCursor(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "batch-ack-gap")
	now := time.Now().UTC()
	first := nodeprotocol.RunEvent{
		ID: "batch-ack-1", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "run.started", Payload: map[string]any{"source": "runtime"}, OccurredAt: now,
	}
	second := nodeprotocol.RunEvent{
		ID: "batch-ack-2", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 2,
		Type: "output.delta", Payload: map[string]any{"text": "stored"}, OccurredAt: now.Add(time.Millisecond),
	}
	if cursors, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{first, second}); err != nil || len(cursors) != 1 || cursors[0].Sequence != 2 {
		t.Fatalf("initial cursors=%+v err=%v", cursors, err)
	}

	future := nodeprotocol.RunEvent{
		ID: "batch-ack-4", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 4,
		Type: "output.delta", Payload: map[string]any{"text": "future"}, OccurredAt: now.Add(4 * time.Millisecond),
	}
	if cursors, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{future}); err != nil || len(cursors) != 0 {
		t.Fatalf("future-only gap reused stored cursor: cursors=%+v err=%v", cursors, err)
	}
	cursors, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{first, future})
	if err != nil || len(cursors) != 1 || cursors[0].Sequence != 1 {
		t.Fatalf("gap batch cursors=%+v err=%v, want only exact-validated sequence 1", cursors, err)
	}
	var futureRows int
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT count(*) FROM run_events
		WHERE run_id = ? AND lease_epoch = ? AND runtime_sequence = 4
	`, run.ID, run.LeaseEpoch).Scan(&futureRows); err != nil {
		t.Fatal(err)
	}
	if futureRows != 0 {
		t.Fatalf("future gap persisted %d rows", futureRows)
	}
}

func TestSQLiteCurrentProviderTerminalStatusProjection(t *testing.T) {
	tests := []struct {
		name          string
		currentStatus string
		payload       map[string]any
		wantStatus    string
		wantReason    string
	}{
		{
			name:       "failed top-level status",
			payload:    map[string]any{"status": "failed"},
			wantStatus: domain.RunFailed, wantReason: "provider_failed",
		},
		{
			name: "interrupted nested status",
			payload: map[string]any{
				"provider_id": "codex",
				"turn":        map[string]any{"status": "interrupted"},
			},
			wantStatus: domain.RunCancelled, wantReason: "interrupted",
		},
		{
			name:          "cancelling wins over successful terminal",
			currentStatus: domain.RunCancelling,
			payload:       map[string]any{"status": "succeeded"},
			wantStatus:    domain.RunCancelled, wantReason: "interrupted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSQLiteExecutionFixture(t)
			defer func() { _ = f.repository.Close() }()
			run := f.createRun(t, "legacy-terminal-"+strings.ReplaceAll(test.name, " ", "-"))
			if test.currentStatus != "" {
				if _, err := f.repository.db.ExecContext(ctx, `UPDATE runs SET status = ? WHERE id = ?`, test.currentStatus, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			cursors, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
				ID: "legacy-terminal-event", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
				Type: "run.completed", Payload: test.payload, OccurredAt: time.Now().UTC(),
			}})
			if err != nil || len(cursors) != 1 || cursors[0].Sequence != 1 {
				t.Fatalf("cursors=%+v err=%v", cursors, err)
			}
			stored, err := f.repository.GetRun(ctx, f.user.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.wantStatus || stored.TerminalReason != test.wantReason {
				t.Fatalf("run status=%q reason=%q, want status=%q reason=%q",
					stored.Status, stored.TerminalReason, test.wantStatus, test.wantReason)
			}
		})
	}
}

func TestSQLiteApplyLocalEventsRejectsMissingTimeAndOversizedBatchBeforeWriting(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "event-ingress-bounds")
	ctx := t.Context()

	assertRejectedWithoutRuntimeRows := func(t *testing.T, events []nodeprotocol.RunEvent, wantCode string) {
		t.Helper()
		_, err := f.repository.ApplyLocalEvents(ctx, events)
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != wantCode || domainErr.Retryable {
			t.Fatalf("ApplyLocalEvents error=%v, want non-retryable %s", err, wantCode)
		}
		var count int
		if err := f.repository.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM run_events
			WHERE run_id = ? AND runtime_sequence IS NOT NULL
		`, run.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rejected batch persisted %d runtime events", count)
		}
	}

	assertRejectedWithoutRuntimeRows(t, []nodeprotocol.RunEvent{{
		ID: "missing-time", RunID: run.ID, LeaseEpoch: run.LeaseEpoch,
		Sequence: 1, Type: "run.started",
	}}, "invalid_run_event")

	now := time.Now().UTC()
	tooMany := make([]nodeprotocol.RunEvent, nodeprotocol.MaxRunEventsPerBatch+1)
	for index := range tooMany {
		tooMany[index] = nodeprotocol.RunEvent{
			ID: fmt.Sprintf("count-%d", index+1), RunID: run.ID, LeaseEpoch: run.LeaseEpoch,
			Sequence: int64(index + 1), Type: "output.delta", OccurredAt: now,
		}
	}
	assertRejectedWithoutRuntimeRows(t, tooMany, "run_event_batch_too_large")

	large := make([]nodeprotocol.RunEvent, 3)
	for index := range large {
		large[index] = nodeprotocol.RunEvent{
			ID: fmt.Sprintf("bytes-%d", index+1), RunID: run.ID, LeaseEpoch: run.LeaseEpoch,
			Sequence: int64(index + 1), Type: "output.delta",
			Payload: map[string]any{"text": strings.Repeat("x", 6<<20)}, OccurredAt: now,
		}
		if err := nodeprotocol.ValidateRunEventSize(large[index]); err != nil {
			t.Fatalf("individual fixture event exceeds its own limit: %v", err)
		}
	}
	assertRejectedWithoutRuntimeRows(t, large, "run_event_batch_too_large")
}

func TestSQLiteApplyLocalEventsRedactsProviderEchoedInlineImageData(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := t.Context()
	run, _, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, f.issue.Version, "event-image-redaction", domain.Run{
		IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
		Input: []domain.ContentBlock{{Type: "text", Text: "work"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := nodeprotocol.RunEvent{
		ID: "runtime-image-echo", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "provider.event", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"part": map[string]any{"type": "file", "url": "data:image/png;base64,secret"},
			"text": "provider echoed data:image/png;base64,secret",
		},
	}
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{event}); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT payload_wire FROM run_events WHERE id = ?`, event.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "data:image") {
		t.Fatalf("persisted event retained inline image data: %s", payload)
	}
}

func TestSQLiteRunEventPageStopsAtPayloadBudgetWithoutLoadingNextPayload(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	run := f.createRun(t, "event-page-budget")
	now := time.Now().UTC()
	for sequence := int64(2); sequence <= 4; sequence++ {
		event := nodeprotocol.RunEvent{
			ID: fmt.Sprintf("event-budget-%d", sequence), RunID: run.ID,
			LeaseEpoch: 1, Sequence: sequence, Type: "output.delta",
			Payload: map[string]any{"text": strings.Repeat("x", 600)}, OccurredAt: now,
		}
		wire, err := EncodeRunEventWire(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.repository.db.ExecContext(t.Context(), `
			INSERT INTO run_events (
				id, run_id, lease_epoch, sequence, event_type, payload,
				payload_wire, event_wire_prefix, event_wire_suffix,
				payload_wire_bytes, event_wire_bytes, occurred_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, event.ID, run.ID, event.LeaseEpoch, sequence, event.Type,
			string(wire.Payload), string(wire.Payload), string(wire.Prefix),
			string(wire.Suffix), wire.PayloadBytes, wire.EventBytes,
			sqliteTimeValue(now)); err != nil {
			t.Fatal(err)
		}
	}
	planRows, err := f.repository.db.QueryContext(t.Context(),
		"EXPLAIN QUERY PLAN "+sqliteRunEventPageQuery,
		run.ID, 1, 11, 10, 1000)
	if err != nil {
		t.Fatal(err)
	}
	usesBudgetIndex := false
	for planRows.Next() {
		var nodeID, parentID, unused int
		var detail string
		if err := planRows.Scan(&nodeID, &parentID, &unused, &detail); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		if strings.Contains(detail, "run_events_page_budget") {
			usesBudgetIndex = true
		}
	}
	if err := planRows.Close(); err != nil {
		t.Fatal(err)
	}
	if !usesBudgetIndex {
		t.Fatal("SQLite event page plan did not use run_events_page_budget")
	}
	page, err := f.repository.ListRunEventPage(t.Context(), f.user.ID, run.ID, 1, 10, 1000)
	if err != nil || !page.Truncated || len(page.Items) != 1 || page.NextCursor != "2" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = f.repository.ListRunEventPage(t.Context(), f.user.ID, run.ID, 2, 10, 1000)
	if err != nil || !page.Truncated || len(page.Items) != 1 || page.NextCursor != "3" {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	page, err = f.repository.ListRunEventPage(t.Context(), f.user.ID, run.ID, 3, 10, 1000)
	if err != nil || page.Truncated || len(page.Items) != 1 || page.Items[0].Sequence != 4 {
		t.Fatalf("last page=%+v err=%v", page, err)
	}
}

func TestSQLiteRunEventWireGuardAcceptsExactFramingAndRejectsTampering(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	run := f.createRun(t, "wire-guard")
	event := nodeprotocol.RunEvent{
		ID: `runtime-<>&-"payload":`, RunID: run.ID, LeaseEpoch: run.LeaseEpoch,
		Sequence: 1, Type: "output.delta",
		Payload: map[string]any{"text": "<compact>&"}, OccurredAt: time.Now().UTC(),
	}
	if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{event}); err != nil {
		t.Fatalf("valid escaped wire rejected: %v", err)
	}
	var payload, prefix, suffix string
	var payloadBytes, eventBytes int
	if err := f.repository.db.QueryRowContext(t.Context(), `
		SELECT payload_wire, event_wire_prefix, event_wire_suffix,
		       payload_wire_bytes, event_wire_bytes
		FROM run_events WHERE id = ?
	`, event.ID).Scan(
		&payload, &prefix, &suffix, &payloadBytes, &eventBytes,
	); err != nil {
		t.Fatal(err)
	}
	if payloadBytes != len(payload) || eventBytes != len(prefix)+len(payload)+len(suffix) {
		t.Fatalf("wire payload=%d/%d event=%d/%d",
			payloadBytes, len(payload), eventBytes, len(prefix)+len(payload)+len(suffix))
	}
	_, err := f.repository.db.ExecContext(t.Context(), `
		UPDATE run_events SET event_wire_bytes = event_wire_bytes - 1 WHERE id = ?
	`, event.ID)
	if err == nil || !strings.Contains(err.Error(), "run_event_wire_size_invalid") {
		t.Fatalf("tampered event size error=%v", err)
	}
	var invalidWire *domain.Error
	if mapped := MapRunEventWriteError(err); !errors.As(mapped, &invalidWire) ||
		invalidWire.Code != "invalid_run_event" || invalidWire.Retryable {
		t.Fatalf("tampered event mapped error=%v", mapped)
	}

	oversized := nodeprotocol.RunEvent{
		ID: "runtime-oversized", RunID: run.ID, LeaseEpoch: run.LeaseEpoch,
		Sequence: 2, Type: "output.delta",
		Payload:    map[string]any{"text": strings.Repeat("x", nodeprotocol.MaxRunEventPayloadBytes)},
		OccurredAt: time.Now().UTC(),
	}
	_, err = f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{oversized})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "run_event_too_large" || domainErr.Retryable {
		t.Fatalf("oversized SQLite event error=%v", err)
	}
}

func TestSQLiteApplyLocalEventsCleanupCursorsAndDuplicateValidation(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "cleanup-cursors")
	now := time.Now().UTC()

	orphan, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{
		{ID: "orphan-7", RunID: "run_missing", LeaseEpoch: 3, Sequence: 7, Type: "output.delta", OccurredAt: now},
		{ID: "orphan-8", RunID: "run_missing", LeaseEpoch: 3, Sequence: 8, Type: "run.completed", OccurredAt: now},
	})
	if err != nil || len(orphan) != 1 || orphan[0].LeaseEpoch != 3 || orphan[0].Sequence != 8 {
		t.Fatalf("orphan cleanup cursor=%+v err=%v", orphan, err)
	}
	stale, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{
		{ID: "stale-4", RunID: run.ID, LeaseEpoch: 9, Sequence: 4, Type: "output.delta", OccurredAt: now},
		{ID: "stale-5", RunID: run.ID, LeaseEpoch: 9, Sequence: 5, Type: "run.completed", OccurredAt: now},
	})
	if err != nil || len(stale) != 1 || stale[0].LeaseEpoch != 9 || stale[0].Sequence != 5 {
		t.Fatalf("stale cleanup cursor=%+v err=%v", stale, err)
	}
	first := nodeprotocol.RunEvent{ID: "runtime-sequence-1", RunID: run.ID, LeaseEpoch: 1,
		Sequence: 1, Type: "run.started", Payload: map[string]any{"state": "original"}, OccurredAt: now}
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{first}); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.ID = "different-event-at-sequence-1"
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{conflict}); err == nil {
		t.Fatal("different event at an imported lease/runtime sequence was accepted")
	}
	for name, mutate := range map[string]func(*nodeprotocol.RunEvent){
		"type": func(event *nodeprotocol.RunEvent) { event.Type = "provider.event" },
		"payload": func(event *nodeprotocol.RunEvent) {
			event.Payload = map[string]any{"state": "changed"}
		},
		"occurred_at": func(event *nodeprotocol.RunEvent) { event.OccurredAt = now.Add(time.Second) },
	} {
		t.Run("same id changed "+name, func(t *testing.T) {
			changed := first
			mutate(&changed)
			if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{changed}); err == nil {
				t.Fatalf("same-ID event with changed %s was accepted", name)
			}
		})
	}
}

func TestSQLiteTerminalRelationsPreservePausedAndNewerActiveRun(t *testing.T) {
	t.Run("paused instance", func(t *testing.T) {
		ctx := context.Background()
		f := newSQLiteExecutionFixture(t)
		defer func() { _ = f.repository.Close() }()
		run := f.createRun(t, "terminal-paused")
		if _, err := f.repository.db.ExecContext(ctx, `UPDATE issue_agent_instances SET status = 'paused' WHERE id = ?`, f.instance.ID); err != nil {
			t.Fatal(err)
		}
		_, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
			ID: "paused-complete", RunID: run.ID, LeaseEpoch: 1, Sequence: 1,
			Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: time.Now().UTC(),
		}})
		if err != nil {
			t.Fatal(err)
		}
		instance, err := f.repository.GetIssueAgentInstance(ctx, f.user.ID, f.issue.ID, f.agent.ID)
		if err != nil || instance.Status != domain.AgentInstancePaused {
			t.Fatalf("instance=%+v err=%v", instance, err)
		}
	})

	t.Run("new active run", func(t *testing.T) {
		ctx := context.Background()
		f := newSQLiteExecutionFixture(t)
		defer func() { _ = f.repository.Close() }()
		oldRun := f.createRun(t, "terminal-old")
		now := sqliteTimeValue(time.Now().UTC())
		if _, err := f.repository.db.ExecContext(ctx, `
			INSERT INTO runs (
				id, issue_id, issue_agent_instance_id, issue_agent_generation_id, agent_id,
				project_id, computer_id, status, agent_service_id, provider, model, read_only,
				lease_epoch, input, idempotency_key, version, created_at, updated_at
			)
			SELECT 'run_newer_active', issue_id, issue_agent_instance_id, issue_agent_generation_id,
			       agent_id, project_id, computer_id, 'running', agent_service_id, provider, model,
			       read_only, lease_epoch, input, 'terminal-newer', 1, ?, ?
			FROM runs WHERE id = ?
		`, now, now, oldRun.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
			ID: "old-complete", RunID: oldRun.ID, LeaseEpoch: 1, Sequence: 1,
			Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: time.Now().UTC(),
		}}); err != nil {
			t.Fatal(err)
		}
		instance, err := f.repository.GetIssueAgentInstance(ctx, f.user.ID, f.issue.ID, f.agent.ID)
		if err != nil || instance.Status != domain.AgentInstanceRunning {
			t.Fatalf("terminal old run idled newer active run: instance=%+v err=%v", instance, err)
		}
		// A later event on the already-terminal old run must not rerun terminal
		// relation cleanup either.
		if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
			ID: "old-late-output", RunID: oldRun.ID, LeaseEpoch: 1, Sequence: 2,
			Type: "output.delta", OccurredAt: time.Now().UTC(),
		}}); err != nil {
			t.Fatal(err)
		}
		instance, err = f.repository.GetIssueAgentInstance(ctx, f.user.ID, f.issue.ID, f.agent.ID)
		if err != nil || instance.Status != domain.AgentInstanceRunning {
			t.Fatalf("late old event idled newer active run: instance=%+v err=%v", instance, err)
		}
	})
}

func TestSQLiteMentionConsumeAndRunAreAtomic(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	resource, err := f.repository.CreateResource(ctx, f.user.ID, domain.ProjectResource{
		ProjectID: f.project.ID, Kind: "code_directory", Name: "Git",
		Path: "/tmp/teamva-mention-atomic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateResource(ctx, f.user.ID, resource.ID, resource.Version,
		func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = true
		}); err != nil {
		t.Fatal(err)
	}
	comment, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "@runner"}, []string{f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repository.ListIssueCommentMentions(ctx, f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 1 || mentions[0].CommentID != comment.ID {
		t.Fatalf("mentions=%+v err=%v", mentions, err)
	}
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE agent_services SET status = 'unavailable' WHERE id = ?`, f.service.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.CreateRunForMention(ctx, f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+mentions[0].ID)
	if err == nil {
		t.Fatal("unavailable runtime unexpectedly created mention run")
	}
	var consumed any
	if err := f.repository.db.QueryRowContext(ctx, `SELECT consumed_at FROM issue_comment_mentions WHERE id = ?`, mentions[0].ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatalf("failed run permanently consumed mention: %v", consumed)
	}
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE agent_services SET status = 'ready' WHERE id = ?`, f.service.ID); err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.CreateRunForMention(ctx, f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+mentions[0].ID)
	if err != nil || run.ID == "" || run.ProjectID != f.project.ID {
		t.Fatalf("mention run=%+v err=%v", run, err)
	}
	if run.IssueAgentInstanceID == f.instance.ID {
		t.Fatal("structured @ reused the pre-existing Agent conversation")
	}
	comments, err := f.repository.ListIssueComments(ctx, f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 2 {
		t.Fatalf("working reply comments=%+v err=%v", comments, err)
	}
	working := comments[1]
	if working.Status != domain.IssueCommentWorking || working.RunID != run.ID ||
		working.AuthorAgentID != f.agent.ID || len(run.Input) == 0 || !strings.Contains(run.Input[0].Text, working.ID) {
		t.Fatalf("working reply was not linked into run prompt: comment=%+v input=%+v", working, run.Input)
	}
	replayed, err := f.repository.CreateRunForMention(ctx, f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+mentions[0].ID)
	if err != nil || replayed.ID != run.ID || replayed.IssueAgentInstanceID != run.IssueAgentInstanceID {
		t.Fatalf("mention replay created another conversation: run=%+v err=%v", replayed, err)
	}
}

func TestSQLiteMentionUsesProjectWorkspaceWithoutRepositorySelection(t *testing.T) {
	ctx := t.Context()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	resource, err := f.repository.CreateResource(ctx, f.user.ID, domain.ProjectResource{
		ProjectID: f.project.ID, Kind: "code_directory", Name: "Git", Path: "/tmp/mention-git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateResource(ctx, f.user.ID, resource.ID, resource.Version,
		func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = true
		}); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "@runner",
	}, []string{f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repository.ListIssueCommentMentions(ctx, f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mentions=%+v err=%v", mentions, err)
	}
	run, err := f.repository.CreateRunForMention(ctx, f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+mentions[0].ID)
	if err != nil || run.ID == "" || run.ProjectID != f.project.ID {
		t.Fatalf("workspace mention run=%+v err=%v", run, err)
	}
	var consumed any
	if err := f.repository.db.QueryRowContext(ctx,
		`SELECT consumed_at FROM issue_comment_mentions WHERE id = ?`, mentions[0].ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed == nil {
		t.Fatal("successful workspace mention was not consumed")
	}
}

func TestSQLiteReadyNonGitDirectoryDoesNotRequireRepositorySelection(t *testing.T) {
	ctx := t.Context()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	resource, err := f.repository.CreateResource(ctx, f.user.ID, domain.ProjectResource{
		ProjectID: f.project.ID, Kind: "code_directory", Name: "Plain directory",
		Path: "/tmp/plain-code-directory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateResource(ctx, f.user.ID, resource.ID, resource.Version,
		func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = false
		}); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "@runner",
	}, []string{f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repository.ListIssueCommentMentions(ctx, f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mentions=%+v err=%v", mentions, err)
	}
	run, err := f.repository.CreateRunForMention(ctx, f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "work"}}, "non-git-mention")
	if err != nil || run.ID == "" || run.ProjectID != f.project.ID {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	var gitResourceCount int
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT COALESCE(json_array_length(json_extract(payload, '$.git_resources')), 0)
		FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&gitResourceCount); err != nil {
		t.Fatal(err)
	}
	if gitResourceCount != 0 {
		t.Fatalf("non-Git directory leaked into run.start resources: %d", gitResourceCount)
	}
}

func TestSQLiteEachMentionCreatesIndependentConversation(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	// An active run in an older conversation must not block a new @.
	f.createRun(t, "mention-busy-run")
	mentionIDs := make(map[string]bool)
	for _, body := range []string{"first", "second"} {
		comment, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{
			IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: body,
		}, []string{f.agent.ID})
		if err != nil {
			t.Fatal(err)
		}
		mentions, err := f.repository.ListIssueCommentMentions(ctx, f.user.ID, f.issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, mention := range mentions {
			if mention.CommentID == comment.ID {
				mentionIDs[mention.ID] = true
				break
			}
		}
	}
	if len(mentionIDs) != 2 {
		t.Fatalf("got mention IDs %v", mentionIDs)
	}
	conversationIDs := make(map[string]bool)
	for range 2 {
		claim, ok, err := f.repository.ClaimNextIssueMention(ctx, time.Now().UTC(), time.Minute)
		if err != nil || !ok || !mentionIDs[claim.MentionID] {
			t.Fatalf("claim=%+v ok=%t err=%v", claim, ok, err)
		}
		run, err := f.repository.CreateRunForClaimedMention(ctx, claim,
			[]domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID)
		if err != nil {
			t.Fatalf("new @ was blocked by another conversation: %v", err)
		}
		if run.IssueAgentInstanceID == f.instance.ID || conversationIDs[run.IssueAgentInstanceID] {
			t.Fatalf("@ mention did not create a fresh conversation: %+v", run)
		}
		conversationIDs[run.IssueAgentInstanceID] = true
		delete(mentionIDs, claim.MentionID)
	}
	instances, err := f.repository.ListIssueAgentInstances(ctx, f.user.ID, f.issue.ID)
	if err != nil || len(instances) != 3 {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
}

func TestSQLiteProviderApprovalAlwaysWaitsForNativeDecision(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()

	run := f.createRun(t, "provider-approval")
	now := time.Now().UTC()
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{
		{ID: "started", RunID: run.ID, LeaseEpoch: 1, Sequence: 1, Type: "run.started", OccurredAt: now},
		{
			ID: "approval", RunID: run.ID, LeaseEpoch: 1, Sequence: 2,
			Type: "approval.requested", OccurredAt: now.Add(time.Millisecond),
			Payload: map[string]any{
				"request_id":        "provider-request",
				"action_kind":       "shell",
				"normalized_action": map[string]any{"command": "pwd"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var approvalStatus, runStatus string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT status FROM approvals WHERE run_id = ?`, run.ID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, run.ID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	var decisionCommands int
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT count(*) FROM command_outbox
		WHERE command_name = 'approval.decide' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&decisionCommands); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "pending" || runStatus != domain.RunWaiting || decisionCommands != 0 {
		t.Fatalf("approval=%s run=%s decision_commands=%d", approvalStatus, runStatus, decisionCommands)
	}
}

func TestSQLiteScheduleTriggerIdempotencyForbidAndDueAdvance(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	due := time.Now().UTC().Add(-2 * time.Minute)
	task, err := f.repository.CreateScheduledTask(ctx, f.user.ID, domain.ScheduledTask{
		ProjectID: f.project.ID, Name: "schedule", AgentID: f.agent.ID,
		ScheduleKind: "cron", ScheduleExpression: "* * * * *", Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "scheduled"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once", NextRunAt: &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, replayed, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:one", time.Now().UTC())
	if err != nil || replayed || first.RunID == "" {
		t.Fatalf("first trigger=%+v replayed=%t err=%v", first, replayed, err)
	}
	second, replayed, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:one", time.Now().UTC())
	if err != nil || !replayed || second.RunID != first.RunID {
		t.Fatalf("replay trigger=%+v replayed=%t err=%v", second, replayed, err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:two", time.Now().UTC()); err == nil {
		t.Fatal("forbid schedule accepted a concurrent trigger")
	}
	run, err := f.repository.GetRun(ctx, f.user.ID, first.RunID)
	if err != nil || run.ReadOnly {
		t.Fatalf("scheduled run must be writable: %+v err=%v", run, err)
	}
	handled, err := f.repository.TriggerDueLocalSchedules(ctx, time.Now().UTC())
	if err != nil || handled != 1 {
		t.Fatalf("due handled=%d err=%v", handled, err)
	}
	runs, err := f.repository.ListScheduledTaskRuns(ctx, f.user.ID, task.ID)
	updated, getErr := f.repository.GetScheduledTask(ctx, f.user.ID, task.ID)
	hasSkippedConcurrency := false
	for _, scheduledRun := range runs {
		hasSkippedConcurrency = hasSkippedConcurrency || scheduledRun.Status == "skipped_concurrency"
	}
	if err != nil || getErr != nil || len(runs) != 2 || !hasSkippedConcurrency ||
		updated.NextRunAt == nil || !updated.NextRunAt.After(time.Now().UTC().Add(-time.Second)) {
		t.Fatalf("task runs=%+v updated=%+v err=%v getErr=%v", runs, updated, err, getErr)
	}
}

func TestSQLiteDueSchedulesIsolateBadTask(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	badDue := time.Now().UTC().Add(-2 * time.Minute)
	goodDue := time.Now().UTC().Add(-time.Minute)
	bad, err := f.repository.CreateScheduledTask(ctx, f.user.ID, domain.ScheduledTask{
		ProjectID: f.project.ID, Name: "bad", AgentID: f.agent.ID,
		ScheduleKind: "cron", ScheduleExpression: "* * * * *", Timezone: "UTC",
		Prompt: []domain.ContentBlock{{Type: "text", Text: "bad"}}, ConcurrencyPolicy: "allow",
		MisfirePolicy: "run_once", NextRunAt: &badDue,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid durable rows must be isolated too; API/store creation now rejects
	// this calendar, so inject it explicitly to exercise the advance boundary.
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE scheduled_tasks SET timezone = 'Invalid/Timezone' WHERE id = ?`, bad.ID); err != nil {
		t.Fatal(err)
	}
	good, err := f.repository.CreateScheduledTask(ctx, f.user.ID, domain.ScheduledTask{
		ProjectID: f.project.ID, Name: "good", AgentID: f.agent.ID,
		ScheduleKind: "cron", ScheduleExpression: "* * * * *", Timezone: "UTC",
		Prompt: []domain.ContentBlock{{Type: "text", Text: "good"}}, ConcurrencyPolicy: "allow",
		MisfirePolicy: "run_once", NextRunAt: &goodDue,
	})
	if err != nil {
		t.Fatal(err)
	}
	handled, err := f.repository.TriggerDueLocalSchedules(ctx, time.Now().UTC())
	if err != nil || handled != 2 {
		t.Fatalf("handled=%d err=%v", handled, err)
	}
	badStored, err := f.repository.GetScheduledTask(ctx, f.user.ID, bad.ID)
	if err != nil || badStored.Enabled {
		t.Fatalf("bad task=%+v err=%v", badStored, err)
	}
	var badAttempts int
	var badError string
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT dispatch_attempts, COALESCE(last_dispatch_error, '') FROM scheduled_tasks WHERE id = ?
	`, bad.ID).Scan(&badAttempts, &badError); err != nil || badAttempts != 1 || badError == "" {
		t.Fatalf("bad attempts=%d error=%q queryErr=%v", badAttempts, badError, err)
	}
	goodRuns, err := f.repository.ListScheduledTaskRuns(ctx, f.user.ID, good.ID)
	if err != nil || len(goodRuns) != 1 || goodRuns[0].RunID == "" {
		t.Fatalf("good runs=%+v err=%v", goodRuns, err)
	}
}

func TestSQLiteStopApprovalOutboxAndRecovery(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "decisions")
	start, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("start claim=%+v ok=%t err=%v", start, ok, err)
	}
	approval, err := f.repository.CreateApproval(ctx, domain.Approval{
		ProjectID: f.project.ID, IssueID: f.issue.ID, RunID: run.ID,
		ProviderApprovalRef: "provider-request", ActionKind: "shell",
		ActionDigest: "sha256:test", NormalizedAction: map[string]any{"command": "pwd"},
		Risk: "high", ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version,
		approval.ActionDigest, "approved", "once"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.StopRun(ctx, f.user.ID, run.ID, "force"); err != nil {
		t.Fatal(err)
	}
	var decisions, stops int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE command_name = 'approval.decide'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE command_name = 'run.stop'`).Scan(&stops); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || stops != 1 {
		t.Fatalf("decision commands=%d stop commands=%d", decisions, stops)
	}
	if err := f.repository.MarkLocalCommandUncertain(ctx, start.ID, start.ClaimToken, "crash"); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenSQLite(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repository = restarted
	if err := f.repository.RecoverLocalExecution(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.repository.GetRun(ctx, f.user.ID, run.ID)
	command, commandErr := f.repository.GetLocalCommand(ctx, start.ID)
	if err != nil || commandErr != nil || recovered.Status != domain.RunCancelled ||
		recovered.TerminalReason != "interrupted" || recovered.FinishedAt == nil || command.Status != LocalCommandFailed ||
		command.LastError != "run_interrupted" || len(command.Result) != 0 || command.CompletedAt == nil {
		payload, _ := json.Marshal(command)
		t.Fatalf("recovered=%+v command=%s err=%v commandErr=%v", recovered, payload, err, commandErr)
	}
	// The accepted stop survives the crash; this does not invent a successful
	// native execution or approval delivery. Unknown commands remain failed.
	assertSQLiteRecoveredCommandFences(t, f.repository, command, start.ClaimToken)
	for _, name := range []string{"run.stop", "approval.decide"} {
		var id string
		if err := f.repository.db.QueryRowContext(ctx, `SELECT id FROM command_outbox WHERE command_name=? AND json_extract(payload,'$.run_id')=?`, name, run.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		obsolete, err := f.repository.GetLocalCommand(ctx, id)
		if err != nil || obsolete.Status != LocalCommandFailed || obsolete.LastError != "run_terminal" || obsolete.CompletedAt == nil || len(obsolete.Result) != 0 {
			t.Fatalf("recovery fabricated control delivery: %+v err=%v", obsolete, err)
		}
		assertSQLiteRecoveredCommandFences(t, f.repository, obsolete, "pre-restart-control-claim")
	}
	var approvalStatus, scope string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT status,decision_scope FROM approvals WHERE id=?`, approval.ID).Scan(&approvalStatus, &scope); err != nil || approvalStatus != "approved" || scope != "once" {
		t.Fatalf("recovery rewrote the user decision: status=%s scope=%s err=%v", approvalStatus, scope, err)
	}
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "once"); err == nil {
		t.Fatal("recovery made an obsolete approval executable again")
	}
	var runnable int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE json_extract(payload,'$.run_id')=? AND status IN ('pending','processing','uncertain')`, run.ID).Scan(&runnable); err != nil || runnable != 0 {
		t.Fatalf("recovered cancellation left executable commands=%d err=%v", runnable, err)
	}
	assertSQLiteRecoverySnapshotStable(t, f.repository, f.user.ID, run.ID, start.ID)
}

func TestSQLiteSteerRunQueuesOneCommandAndPersistsUserMessage(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "steer")
	start, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || start.Name != "run.start" {
		t.Fatalf("start claim=%+v ok=%t err=%v", start, ok, err)
	}
	input := []domain.ContentBlock{
		{Type: "text", Text: "改为只检查测试"},
		{Type: "image", MimeType: "image/png", Filename: "steer.png", DataURL: tinyPNGDataURL},
	}
	steered, err := f.repository.SteerRun(ctx, f.user.ID, run.ID, "steer-once", input, false)
	if err != nil || steered.Status != domain.RunStarting {
		t.Fatalf("steer=%+v err=%v", steered, err)
	}
	if _, err := f.repository.SteerRun(ctx, f.user.ID, run.ID, "steer-once", input, false); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.SteerRun(ctx, f.user.ID, run.ID, "steer-once", []domain.ContentBlock{{Type: "text", Text: "不同内容必须冲突"}}, false)
	assertDomainCode(t, err, "idempotency_conflict")
	var commands, messages int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE command_name = 'run.steer'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM run_events WHERE run_id = ? AND event_type = 'user.steer'`, run.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if commands != 1 || messages != 1 {
		t.Fatalf("steer commands=%d messages=%d", commands, messages)
	}
	var eventPayload []byte
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT payload FROM run_events WHERE run_id = ? AND event_type = 'user.steer'
	`, run.ID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	var userMessage map[string]any
	if err := json.Unmarshal(eventPayload, &userMessage); err != nil || userMessage["internal"] != false || strings.Contains(string(eventPayload), "data:image") {
		t.Fatalf("user steer payload=%s err=%v", eventPayload, err)
	}
	command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute, start.ID)
	if err != nil || !ok || command.Name != "run.steer" {
		t.Fatalf("steer claim=%+v ok=%t err=%v", command, ok, err)
	}
	var payload nodeprotocol.SteerRunPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil || payload.RunID != run.ID || payload.Prompt != "改为只检查测试" ||
		len(payload.Attachments) != 1 || payload.Attachments[0].DataURL != tinyPNGDataURL {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, command.ID, command.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	completedCommand, err := f.repository.GetLocalCommand(ctx, command.ID)
	if err != nil || strings.Contains(string(completedCommand.Payload), "data:image") || strings.Contains(string(completedCommand.Payload), "attachments") {
		t.Fatalf("completed command retained inline attachment: payload=%s err=%v", completedCommand.Payload, err)
	}
	now := time.Now().UTC()
	if _, err := f.repository.db.ExecContext(ctx, `
		INSERT INTO issue_comments (
			id, issue_id, author_kind, author_agent_id, body, run_id, status,
			version, created_at, updated_at
		) VALUES ('cmt_steer', ?, 'agent', ?, '[teamva:working]', ?, 'working', 1, ?, ?)
	`, f.issue.ID, f.agent.ID, run.ID, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		t.Fatal(err)
	}
	events := []nodeprotocol.RunEvent{
		{ID: "evt_steer_started", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.started", Payload: map[string]any{}, OccurredAt: now},
		{ID: "evt_steer_item_start", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 2, Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": ""}}, OccurredAt: now},
		{ID: "evt_steer_delta_1", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 3, Type: "output.delta", Payload: map[string]any{"text": "干预"}, OccurredAt: now},
		{ID: "evt_steer_delta_2", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 4, Type: "output.delta", Payload: map[string]any{"text": "完成"}, OccurredAt: now},
		{ID: "evt_steer_item_done", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 5, Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "干预完成"}}, OccurredAt: now},
		{ID: "evt_steer_done", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 6, Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: now},
	}
	if _, err := f.repository.ApplyLocalEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	var commentStatus, body string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT status, body FROM issue_comments WHERE id = 'cmt_steer'`).Scan(&commentStatus, &body); err != nil {
		t.Fatal(err)
	}
	if commentStatus != domain.IssueCommentFailed || body != "[teamva:working]" {
		t.Fatalf("model text must not impersonate a public tool comment: status=%q body=%q", commentStatus, body)
	}
}

func TestSQLiteTerminalRunCancelsApprovalAndApprovalScopeComesFromRun(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	run := f.createRun(t, "terminal-approval")
	approval, err := f.repository.CreateApproval(ctx, domain.Approval{
		ProjectID: "proj_untrusted", IssueID: "iss_untrusted", RunID: run.ID,
		ProviderApprovalRef: "provider-terminal", ActionKind: "shell",
		ActionDigest: "sha256:terminal", NormalizedAction: map[string]any{"command": "pwd"},
		Risk: "high", ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if approval.ProjectID != run.ProjectID || approval.IssueID != run.IssueID {
		t.Fatalf("approval scope was not derived from run: approval=%+v run=%+v", approval, run)
	}
	if err := f.repository.FailLocalRun(ctx, run.ID, "test_terminal"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT status FROM approvals WHERE id = ?`, approval.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("terminal run left approval %q, want cancelled", status)
	}
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version,
		approval.ActionDigest, "approved", "once"); err == nil {
		t.Fatal("terminal run approval was decidable")
	}
	var decisions int
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT count(*) FROM command_outbox
		WHERE command_name = 'approval.decide' AND json_extract(payload, '$.run_id') = ?
	`, run.ID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("terminal approval dispatched %d decisions", decisions)
	}
	if _, err := f.repository.db.ExecContext(ctx, `
		INSERT INTO approvals (
			id, project_id, issue_id, run_id, status, action_kind, action_digest,
			normalized_action, risk, expires_at, version, created_at, updated_at
		) VALUES ('apr_bad_scope', 'proj_bad', NULL, ?, 'pending', 'shell',
		          'sha256:bad', '{}', 'high', ?, 1, ?, ?)
	`, run.ID, sqliteTimeValue(time.Now().Add(time.Minute)), sqliteTimeValue(time.Now()), sqliteTimeValue(time.Now())); err == nil {
		t.Fatal("baseline allowed approval scope inconsistent with its run")
	}
}

func TestSQLiteRecoverLocalExecutionStateMatrix(t *testing.T) {
	tests := []struct {
		name          string
		runStatus     string
		commandName   string
		commandStatus string
		wantRun       string
		wantCommand   string
		wantLastError string
	}{
		{name: "terminal start processing", runStatus: domain.RunSucceeded, commandName: "run.start", commandStatus: LocalCommandProcessing, wantRun: domain.RunSucceeded, wantCommand: LocalCommandCompleted},
		{name: "terminal stop pending", runStatus: domain.RunFailed, commandName: "run.stop", commandStatus: LocalCommandPending, wantRun: domain.RunFailed, wantCommand: LocalCommandFailed, wantLastError: "run_terminal"},
		{name: "terminal approval uncertain", runStatus: domain.RunCancelled, commandName: "approval.decide", commandStatus: LocalCommandUncertain, wantRun: domain.RunCancelled, wantCommand: LocalCommandFailed, wantLastError: "run_terminal"},
		{name: "starting processing", runStatus: "starting", commandName: "run.start", commandStatus: LocalCommandProcessing, wantRun: domain.RunFailed, wantCommand: LocalCommandFailed, wantLastError: "run_interrupted"},
		{name: "running stop pending", runStatus: domain.RunRunning, commandName: "run.stop", commandStatus: LocalCommandPending, wantRun: domain.RunFailed, wantCommand: LocalCommandFailed, wantLastError: "run_terminal"},
		{name: "waiting approval uncertain", runStatus: domain.RunWaiting, commandName: "approval.decide", commandStatus: LocalCommandUncertain, wantRun: domain.RunFailed, wantCommand: LocalCommandFailed, wantLastError: "run_terminal"},
		{name: "cancelling stop processing", runStatus: "cancelling", commandName: "run.stop", commandStatus: LocalCommandProcessing, wantRun: domain.RunCancelled, wantCommand: LocalCommandFailed, wantLastError: "run_terminal"},
		{name: "queued start processing safely replayed", runStatus: domain.RunQueued, commandName: "run.start", commandStatus: LocalCommandProcessing, wantRun: domain.RunQueued, wantCommand: LocalCommandPending, wantLastError: "dispatcher_restarted"},
		{name: "queued start uncertain safely replayed", runStatus: domain.RunQueued, commandName: "run.start", commandStatus: LocalCommandUncertain, wantRun: domain.RunQueued, wantCommand: LocalCommandPending, wantLastError: "dispatcher_restarted"},
		{name: "queued stop processing not replayed", runStatus: domain.RunQueued, commandName: "run.stop", commandStatus: LocalCommandProcessing, wantRun: domain.RunQueued, wantCommand: LocalCommandFailed, wantLastError: "recovery_not_replayable"},
		{name: "queued approval uncertain not replayed", runStatus: domain.RunQueued, commandName: "approval.decide", commandStatus: LocalCommandUncertain, wantRun: domain.RunQueued, wantCommand: LocalCommandFailed, wantLastError: "recovery_not_replayable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSQLiteExecutionFixture(t)
			defer func() { _ = f.repository.Close() }()
			run := f.createRun(t, "recover-matrix")
			if _, err := f.repository.db.ExecContext(ctx, `UPDATE runs SET status = ? WHERE id = ?`, test.runStatus, run.ID); err != nil {
				t.Fatal(err)
			}
			var commandID string
			if test.commandName == "run.start" {
				if err := f.repository.db.QueryRowContext(ctx, `
					SELECT id FROM command_outbox
					WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
				`, run.ID).Scan(&commandID); err != nil {
					t.Fatal(err)
				}
			} else {
				commandID = newID("cmd")
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]any{"run_id": run.ID})
				if _, err := f.repository.db.ExecContext(ctx, `
					INSERT INTO command_outbox (
						id, computer_id, command_name, idempotency_key, payload,
						status, available_at, expires_at, created_at
					) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)
				`, commandID, f.computer.ID, test.commandName, test.commandName+":"+commandID,
					string(payload), sqliteTimeValue(now), sqliteTimeValue(now.Add(time.Hour)), sqliteTimeValue(now)); err != nil {
					t.Fatal(err)
				}
			}
			var claimedAt, leaseUntil, claimToken any
			if test.commandStatus == LocalCommandProcessing {
				claimedAt = sqliteTimeValue(time.Now().UTC())
				leaseUntil = sqliteTimeValue(time.Now().UTC().Add(time.Minute))
				claimToken = newID("claim")
			}
			if _, err := f.repository.db.ExecContext(ctx, `
				UPDATE command_outbox SET status = ?, claimed_at = ?, lease_until = ?, claim_token = ? WHERE id = ?
			`, test.commandStatus, claimedAt, leaseUntil, claimToken, commandID); err != nil {
				t.Fatal(err)
			}
			if err := f.repository.RecoverLocalExecution(ctx); err != nil {
				t.Fatal(err)
			}
			recovered, err := f.repository.GetRun(ctx, f.user.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			command, err := f.repository.GetLocalCommand(ctx, commandID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != test.wantRun || command.Status != test.wantCommand || command.LastError != test.wantLastError {
				t.Fatalf("recovered run=%s command=%s, want run=%s command=%s", recovered.Status, command.Status, test.wantRun, test.wantCommand)
			}
			if command.Status == LocalCommandCompleted {
				if string(command.Result) != "{}" {
					t.Fatal("recovered terminal command result differs from its acknowledged projection")
				}
			} else if len(command.Result) != 0 {
				t.Fatal("recovery fabricated a native command result")
			}
			staleToken, _ := claimToken.(string)
			if staleToken == "" {
				staleToken = "pre-restart-unowned-claim"
			}
			assertSQLiteRecoveredCommandFences(t, f.repository, command, staleToken)
			events, err := f.repository.ListRunEvents(ctx, f.user.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			terminalCount := 0
			for index, event := range events {
				if event.Sequence != int64(index+1) {
					t.Fatal("recovery introduced a public event sequence gap")
				}
				if event.Type == "run.finished" {
					terminalCount++
					if event.Payload["status"] != test.wantRun || event.Payload["reason"] != "interrupted" {
						t.Fatalf("recovery terminal contradicts accepted Run state: %+v", event)
					}
				}
			}
			activeBefore := test.runStatus == domain.RunStarting || test.runStatus == domain.RunRunning || test.runStatus == domain.RunWaiting || test.runStatus == domain.RunCancelling
			if activeBefore {
				if terminalCount != 1 || recovered.FinishedAt == nil || recovered.TerminalReason != "interrupted" {
					t.Fatal("recovered active Run lacks one explicit interrupted terminal")
				}
			} else if terminalCount != 0 {
				t.Fatal("recovery fabricated another terminal for a queued/already-terminal Run")
			}
			assertSQLiteRecoverySnapshotStable(t, f.repository, f.user.ID, run.ID, commandID)
			if command.Status == LocalCommandPending {
				reclaimed, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
				if err != nil || !ok || reclaimed.ID != commandID || reclaimed.ClaimToken == staleToken || reclaimed.ClaimToken == "" {
					t.Fatalf("safe queued replay lost stable identity or fencing: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func assertSQLiteRecoveredCommandFences(t *testing.T, repository *SQLite, command LocalCommand, staleToken string) {
	t.Helper()
	if command.ClaimedAt != nil || command.LeaseUntil != nil || command.ClaimToken != "" {
		t.Fatalf("recovered command retained ownership: %+v", command)
	}
	if (command.Status == LocalCommandPending) != (command.CompletedAt == nil) {
		t.Fatalf("recovered command completion timestamp contradicts status: %+v", command)
	}
	for name, mutation := range map[string]func() error{
		"renew": func() error {
			return repository.RenewLocalCommandLease(t.Context(), command.ID, staleToken, time.Now().UTC(), time.Minute)
		},
		"complete": func() error {
			return repository.CompleteLocalCommand(t.Context(), command.ID, staleToken, json.RawMessage(`{}`))
		},
		"retry": func() error {
			return repository.RetryLocalCommand(t.Context(), command.ID, staleToken, "stale", time.Now().UTC())
		},
		"uncertain": func() error {
			return repository.MarkLocalCommandUncertain(t.Context(), command.ID, staleToken, "stale")
		},
		"fail": func() error { return repository.FailLocalCommand(t.Context(), command.ID, staleToken, "stale", nil) },
	} {
		if err := mutation(); err == nil {
			t.Fatalf("pre-restart token could %s recovered command", name)
		}
	}
}

func assertSQLiteRecoverySnapshotStable(t *testing.T, repository *SQLite, userID, runID, commandID string) {
	t.Helper()
	snapshot := func() string {
		run, err := repository.GetRun(t.Context(), userID, runID)
		if err != nil {
			t.Fatal(err)
		}
		command, err := repository.GetLocalCommand(t.Context(), commandID)
		if err != nil {
			t.Fatal(err)
		}
		events, err := repository.ListRunEvents(t.Context(), userID, runID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(struct {
			Run     domain.Run
			Command LocalCommand
			Events  []domain.RunEvent
		}{run, command, events})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	before := snapshot()
	if err := repository.RecoverLocalExecution(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("repeated recovery changed Run, command result or terminal history")
	}
}
