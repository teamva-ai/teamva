package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

type fakeRuntime struct {
	mu             sync.Mutex
	events         []nodeprotocol.RunEvent
	caller         *localToolCaller
	onStart        func(context.Context, nodeprotocol.StartRunPayload) error
	eventsForStart func(context.Context, nodeprotocol.StartRunPayload) ([]nodeprotocol.RunEvent, error)
	beforeAck      func([]nodeprotocol.RunEventCursor) error
}

func (f *fakeRuntime) Probe(context.Context) ([]nodeprotocol.AgentManifest, error) {
	return []nodeprotocol.AgentManifest{{
		Provider: "opencode", ProviderVersion: "test", Available: true, Authenticated: true,
		Capabilities: map[string]any{"models": []string{"deepseek/test"}}, CapabilityVersion: 1,
	}}, nil
}

func (f *fakeRuntime) Handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	if command.Name == "agent.diagnose" {
		result, err := json.Marshal(nodeprotocol.ProviderDiagnostic{
			Provider: "opencode", ProviderVersion: "9.9.9", Available: true, Authenticated: false,
			Status: "needs_auth", CapabilityVersion: 1,
			Capabilities: map[string]any{"models": []string{}},
			StartedAt:    time.Now().UTC(), FinishedAt: time.Now().UTC(),
			Steps: []nodeprotocol.DiagnosticStep{{
				ID: "authentication", Label: "检查连接", Status: "warning", Message: "尚未连接 Provider",
			}},
		})
		if err != nil {
			return nodeprotocol.CommandResult{}, err
		}
		return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed", Result: result}, nil
	}
	if command.Name == "run.start" {
		var payload nodeprotocol.StartRunPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return nodeprotocol.CommandResult{}, err
		}
		if payload.IssueID != "" {
			if f.onStart != nil {
				if err := f.onStart(ctx, payload); err != nil {
					return nodeprotocol.CommandResult{}, err
				}
			} else {
				_, err := f.caller.SendToolRequest(ctx, nodeprotocol.NodeRequest{
					RequestID: "tool-test", Tool: "issue_comment_create", RunID: payload.RunID,
					IssueID: payload.IssueID, AgentID: payload.AgentID,
					InstanceID: payload.IssueAgentInstanceID,
					Payload:    map[string]any{"body": "本地执行完成", "mentioned_agent_ids": []any{}},
				})
				if err != nil {
					return nodeprotocol.CommandResult{}, err
				}
			}
		}
		events := []nodeprotocol.RunEvent{
			{ID: "runtime-start-" + payload.RunID, RunID: payload.RunID, LeaseEpoch: 1, Sequence: 1, Type: "run.started", OccurredAt: time.Now().UTC()},
			{ID: "runtime-complete-" + payload.RunID, RunID: payload.RunID, LeaseEpoch: 1, Sequence: 2, Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: time.Now().UTC()},
		}
		if f.eventsForStart != nil {
			var err error
			events, err = f.eventsForStart(ctx, payload)
			if err != nil {
				return nodeprotocol.CommandResult{}, err
			}
		}
		now := time.Now().UTC()
		for index := range events {
			if events[index].Payload == nil {
				events[index].Payload = map[string]any{}
			}
			events[index].Payload["provider_id"] = payload.Provider
			events[index].RunID = payload.RunID
			events[index].LeaseEpoch = payload.LeaseEpoch
			if events[index].ID == "" {
				events[index].ID = fmt.Sprintf("runtime-%s-%d", payload.RunID, index+1)
			}
			if events[index].Sequence == 0 {
				events[index].Sequence = int64(index + 1)
			}
			if events[index].OccurredAt.IsZero() {
				events[index].OccurredAt = now.Add(time.Duration(index) * time.Millisecond)
			}
		}
		f.mu.Lock()
		f.events = append(f.events, events...)
		f.mu.Unlock()
	}
	return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed"}, nil
}

func (f *fakeRuntime) UnackedEvents(context.Context, int) ([]nodeprotocol.RunEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeprotocol.RunEvent(nil), f.events...), nil
}

func (f *fakeRuntime) AckEvents(_ context.Context, cursors []nodeprotocol.RunEventCursor) error {
	if f.beforeAck != nil {
		if err := f.beforeAck(cursors); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cursor := range cursors {
		remaining := f.events[:0]
		for _, event := range f.events {
			if event.RunID != cursor.RunID || event.Sequence > cursor.Sequence {
				remaining = append(remaining, event)
			}
		}
		f.events = remaining
	}
	return nil
}

func (f *fakeRuntime) Close() error { return nil }

func waitForAgentServices(t *testing.T, repository *store.SQLite, userID, computerID string) ([]domain.AgentService, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, err := repository.ListAgentServices(t.Context(), userID, computerID)
		if err != nil || len(items) > 0 {
			return items, err
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("initial asynchronous Provider probe did not finish")
	return nil, nil
}

type observingGitRuntime struct {
	*fakeRuntime
	mu           sync.Mutex
	handleCalls  int
	gitCalls     int
	repositories []nodeprotocol.GitRepositorySummary
	truncated    bool
}

func (f *observingGitRuntime) Handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	f.mu.Lock()
	f.handleCalls++
	f.mu.Unlock()
	return f.fakeRuntime.Handle(ctx, command)
}

func (f *observingGitRuntime) ListGitRepositories(_ context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	f.mu.Lock()
	f.gitCalls++
	configured := append([]nodeprotocol.GitRepositorySummary(nil), f.repositories...)
	truncated := f.truncated
	f.mu.Unlock()
	items := configured
	if items == nil {
		items = []nodeprotocol.GitRepositorySummary{{
			ID: "repo_ephemeral", CommonID: "common_ephemeral", Label: request.ProjectID,
			ResourceIDs: []string{}, Scope: "full", State: "clean", Generation: 1,
			Warnings: []nodeprotocol.GitRepositoryWarning{},
		}}
	}
	return nodeprotocol.GitRepositoryList{Repositories: items, Truncated: truncated}, nil
}

func (f *observingGitRuntime) ListGitChanges(context.Context, nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	return nodeprotocol.GitChangesPage{}, nil
}

func (f *observingGitRuntime) ReadGitDiff(context.Context, nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	return nodeprotocol.GitDiffResult{}, nil
}

func (f *observingGitRuntime) RefreshGit(context.Context, nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	return nodeprotocol.GitRefreshResult{Refreshed: true}, nil
}

func (f *observingGitRuntime) SubscribeGitEvents(context.Context, nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	events := make(chan nodeprotocol.GitEvent)
	return events, func() { close(events) }, nil
}

func (f *observingGitRuntime) GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	return []nodeprotocol.GitRunWorkspaceSnapshot{}, nil
}

func TestLocalGitReadBypassesRuntimeHandle(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &observingGitRuntime{fakeRuntime: &fakeRuntime{caller: caller}}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	result, err := local.ListGitRepositories(ctx, nodeprotocol.GitRepositoriesRequest{ProjectID: "project_ephemeral"})
	if err != nil || len(result.Repositories) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.gitCalls != 1 || runtime.handleCalls != 0 {
		t.Fatalf("gitCalls=%d handleCalls=%d", runtime.gitCalls, runtime.handleCalls)
	}
}

func TestLocalDiagnoseProviderUpdatesServiceState(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})

	if _, err := waitForAgentServices(t, repository, user.ID, computer.ID); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := local.DiagnoseProvider(ctx, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.Status != "needs_auth" || len(diagnostic.Steps) != 1 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services = %+v, err = %v", services, err)
	}
	if services[0].ProviderVersion != "9.9.9" || services[0].Status != "unavailable" || services[0].LoginStatus != "required" {
		t.Fatalf("service = %+v", services[0])
	}
}

type blockingRuntime struct {
	mu      sync.Mutex
	events  []nodeprotocol.RunEvent
	started chan string
	done    chan struct{}
	once    sync.Once
	starts  int
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{started: make(chan string, 1), done: make(chan struct{})}
}

func (f *blockingRuntime) Probe(context.Context) ([]nodeprotocol.AgentManifest, error) {
	return []nodeprotocol.AgentManifest{{
		Provider: "opencode", Available: true, Authenticated: true,
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	}}, nil
}

func (f *blockingRuntime) Handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	switch command.Name {
	case "run.start":
		var payload nodeprotocol.StartRunPayload
		_ = json.Unmarshal(command.Payload, &payload)
		f.mu.Lock()
		f.starts++
		f.events = append(f.events, nodeprotocol.RunEvent{
			ID: "blocking-start", RunID: payload.RunID, LeaseEpoch: 1,
			Sequence: 1, Type: "run.started", OccurredAt: time.Now().UTC(),
		})
		f.mu.Unlock()
		f.started <- payload.RunID
		select {
		case <-ctx.Done():
			return nodeprotocol.CommandResult{}, ctx.Err()
		case <-f.done:
			return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed"}, nil
		}
	case "run.stop":
		f.once.Do(func() { close(f.done) })
		return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed", Result: json.RawMessage(`{"interrupted":true}`)}, nil
	default:
		return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed"}, nil
	}
}

func (f *blockingRuntime) UnackedEvents(context.Context, int) ([]nodeprotocol.RunEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeprotocol.RunEvent(nil), f.events...), nil
}

func (f *blockingRuntime) AckEvents(_ context.Context, _ []nodeprotocol.RunEventCursor) error {
	f.mu.Lock()
	f.events = nil
	f.mu.Unlock()
	return nil
}

func (f *blockingRuntime) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

func TestLocalDispatcherExecutesMentionAndToolComment(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	runtime.beforeAck = func(cursors []nodeprotocol.RunEventCursor) error {
		for _, cursor := range cursors {
			run, err := repository.GetRun(ctx, user.ID, cursor.RunID)
			if err != nil {
				return err
			}
			if cursor.Sequence >= 2 && run.Status != domain.RunSucceeded {
				return fmt.Errorf("runtime ACK observed uncommitted run state %s", run.Status)
			}
		}
		return nil
	}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 || services[0].Status != "ready" {
		t.Fatalf("services = %+v, err = %v", services, err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "本地项目",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: services[0].ID,
		Model: "deepseek/test", Name: "开发助手", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: services[0].ID,
		Model: "deepseek/test", SystemPrompt: "认真完成任务",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{
		ProjectID: project.ID, Title: "离线任务", Description: "验证本地执行",
		Status: domain.IssueOpen, CreatedByUserID: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateIssueComment(ctx, user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: user.ID, Body: "@开发助手 开始工作",
	}, []string{agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	local.NotifyIssueActivation(issue.ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		instances, _ := repository.ListIssueAgentInstances(ctx, user.ID, issue.ID)
		if len(instances) == 1 {
			runs, _ := repository.ListRunsByInstance(ctx, user.ID, instances[0].ID, 10)
			if len(runs) == 1 && runs[0].Status == domain.RunSucceeded {
				comments, _ := repository.ListIssueComments(ctx, user.ID, issue.ID)
				if len(comments) != 2 || comments[1].AuthorKind != domain.IssueCommentAuthorAgent || comments[1].Body != "本地执行完成" {
					t.Fatalf("comments = %+v", comments)
				}
				events, _ := repository.ListRunEvents(ctx, user.ID, runs[0].ID)
				if len(events) < 3 || events[len(events)-1].Type != "run.completed" {
					t.Fatalf("events = %+v", events)
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("local run did not complete")
}

func TestLocalDispatcherCompletesPersistentGoalLoop(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "持续目标项目",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: services[0].ID,
		Model: "deepseek/test", Name: "目标助手", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: services[0].ID,
		Model: "deepseek/test", SystemPrompt: "完成持续目标",
	}, nil); err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{
		ProjectID: project.ID, Title: "自动续跑并完成", Status: domain.IssueOpen,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := repository.EnsureIssueAgentInstance(ctx, user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	completedEvents := func(output string, usage int64) []nodeprotocol.RunEvent {
		events := []nodeprotocol.RunEvent{{Type: "run.started"}}
		if usage > 0 {
			events = append(events, nodeprotocol.RunEvent{
				Type: "usage.updated", Payload: map[string]any{"total_tokens": usage},
			})
		}
		if output != "" {
			events = append(events, nodeprotocol.RunEvent{
				Type: "output.delta", Payload: map[string]any{"text": output},
			})
		}
		return append(events, nodeprotocol.RunEvent{
			Type: "run.completed", Payload: map[string]any{"status": "completed"},
		})
	}
	var goalMu sync.Mutex
	auditCount := 0
	runtime.eventsForStart = func(runContext context.Context, payload nodeprotocol.StartRunPayload) ([]nodeprotocol.RunEvent, error) {
		run, getErr := repository.GetRun(runContext, user.ID, payload.RunID)
		if getErr != nil {
			return nil, getErr
		}
		if run.Purpose == domain.RunPurposeGoalAudit {
			if !payload.ReadOnly || !payload.DisableTools {
				return nil, fmt.Errorf("goal audit was not isolated: %+v", payload)
			}
		} else if payload.DisableTools {
			return nil, fmt.Errorf("tools disabled for non-audit purpose %q", run.Purpose)
		}
		switch run.Purpose {
		case domain.RunPurposeWork:
			return completedEvents("第一轮实现已完成", 100), nil
		case domain.RunPurposeGoalContinuation:
			return completedEvents("补充验证已完成", 250), nil
		case domain.RunPurposeGoalAudit:
			goalMu.Lock()
			auditCount++
			currentAudit := auditCount
			goalMu.Unlock()
			if currentAudit == 1 {
				return completedEvents(`{"verdict":"continue","note":"还需补充验证"}`, 900), nil
			}
			return completedEvents(`Analysis draft: {"verdict":"continue","note":"尚未作最终判断"}
Final answer: {"verdict":"complete","note":"目标已完整验证"}`, 900), nil
		case domain.RunPurposeGoalFinalize:
			return completedEvents("最终总结已发布", 0), nil
		default:
			return nil, fmt.Errorf("unexpected goal run purpose %q", run.Purpose)
		}
	}
	runtime.onStart = func(runContext context.Context, payload nodeprotocol.StartRunPayload) error {
		run, getErr := repository.GetRun(runContext, user.ID, payload.RunID)
		if getErr != nil || run.Purpose != domain.RunPurposeGoalFinalize {
			return getErr
		}
		result, toolErr := caller.SendToolRequest(runContext, nodeprotocol.NodeRequest{
			RequestID: "goal-final-" + payload.RunID,
			RunID:     payload.RunID, IssueID: payload.IssueID, AgentID: payload.AgentID,
			InstanceID: payload.IssueAgentInstanceID, Tool: "issue_comment_create",
			Payload: map[string]any{"body": "持续目标最终总结", "mentioned_agent_ids": []any{}},
		})
		if toolErr != nil {
			return toolErr
		}
		if !result.OK {
			return fmt.Errorf("final summary tool failed: %s", result.ErrorCode)
		}
		return nil
	}

	budget := int64(10_000)
	initialRun, createdGoal, replayed, err := repository.CreateIssueAgentGoalRun(
		ctx, user.ID, issue.ID, instance.ID, issue.Version, "goal-dispatch-test",
		"完成实现并运行验证", &budget, domain.Run{
			IssueAgentInstanceID: instance.ID, AgentID: agent.ID,
			Input: []domain.ContentBlock{{Type: "text", Text: "开始持续目标"}},
		},
	)
	if err != nil || replayed {
		t.Fatalf("create goal run=%+v goal=%+v replayed=%t err=%v", initialRun, createdGoal, replayed, err)
	}
	local.NotifyIssueActivation(issue.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		goal, found, getErr := repository.GetCurrentIssueAgentGoal(ctx, user.ID, instance.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if found && goal.Status == domain.GoalCompleted {
			if goal.TokensUsed != 350 || goal.ContinuationsUsed != 1 {
				t.Fatalf("completed goal counters = %+v", goal)
			}
			runs, listErr := repository.ListRunsByIssue(ctx, user.ID, issue.ID, 20)
			if listErr != nil {
				t.Fatal(listErr)
			}
			purposeCounts := map[string]int{}
			for _, run := range runs {
				purposeCounts[run.Purpose]++
				if run.Status != domain.RunSucceeded {
					t.Fatalf("goal run did not succeed: %+v", run)
				}
			}
			if len(runs) != 5 || purposeCounts[domain.RunPurposeWork] != 1 ||
				purposeCounts[domain.RunPurposeGoalContinuation] != 1 ||
				purposeCounts[domain.RunPurposeGoalAudit] != 2 ||
				purposeCounts[domain.RunPurposeGoalFinalize] != 1 {
				t.Fatalf("goal runs=%+v purposeCounts=%v", runs, purposeCounts)
			}
			comments, listErr := repository.ListIssueComments(ctx, user.ID, issue.ID)
			if listErr != nil || len(comments) != 1 || comments[0].Status != domain.IssueCommentPublished ||
				comments[0].Body != "持续目标最终总结" {
				t.Fatalf("goal comments=%+v err=%v", comments, listErr)
			}
			goalMu.Lock()
			completedAudits := auditCount
			goalMu.Unlock()
			if completedAudits != 2 {
				t.Fatalf("goal audits=%d, want 2", completedAudits)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("persistent goal loop did not complete")
}

func TestLocalDispatcherExecutesMultipleAgentMentions(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "多 Agent 项目",
	})
	if err != nil {
		t.Fatal(err)
	}
	agentIDs := make([]string, 0, 3)
	for index := 1; index <= 3; index++ {
		agent, createErr := repository.CreateAgent(ctx, user.ID, domain.Agent{
			ComputerID: computer.ID, AgentServiceID: services[0].ID,
			Model: "deepseek/test", Name: fmt.Sprintf("并发助手 %d", index), Status: domain.AgentActive,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
			AgentID: agent.ID, AgentServiceID: services[0].ID, Model: agent.Model,
		}, nil); createErr != nil {
			t.Fatal(createErr)
		}
		agentIDs = append(agentIDs, agent.ID)
	}
	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{
		ProjectID: project.ID, Title: "并发协作", Status: domain.IssueOpen,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateIssueComment(ctx, user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: user.ID, Body: "请三位助手分别完成任务",
	}, agentIDs); err != nil {
		t.Fatal(err)
	}
	local.NotifyIssueActivation(issue.ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		instances, listErr := repository.ListIssueAgentInstances(ctx, user.ID, issue.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(instances) == len(agentIDs) {
			allSucceeded := true
			for _, instance := range instances {
				runs, runErr := repository.ListRunsByInstance(ctx, user.ID, instance.ID, 10)
				if runErr != nil {
					t.Fatal(runErr)
				}
				if len(runs) != 1 || runs[0].Status != domain.RunSucceeded {
					allSucceeded = false
					break
				}
			}
			if allSucceeded {
				comments, commentErr := repository.ListIssueComments(ctx, user.ID, issue.ID)
				if commentErr != nil {
					t.Fatal(commentErr)
				}
				if len(comments) != 4 {
					t.Fatalf("comments=%+v", comments)
				}
				seenAgents := make(map[string]struct{}, len(agentIDs))
				for _, comment := range comments {
					if comment.AuthorKind == domain.IssueCommentAuthorAgent {
						seenAgents[comment.AuthorAgentID] = struct{}{}
					}
				}
				if len(seenAgents) != len(agentIDs) {
					t.Fatalf("agent comments came from %v", seenAgents)
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("multi-Agent mentions did not all complete")
}

func TestLocalDispatcherAllowsIndirectAgentDelegationCycle(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "循环委派项目",
	})
	if err != nil {
		t.Fatal(err)
	}
	createAgent := func(name string) domain.Agent {
		t.Helper()
		agent, createErr := repository.CreateAgent(ctx, user.ID, domain.Agent{
			ComputerID: computer.ID, AgentServiceID: services[0].ID,
			Model: "deepseek/test", Name: name, Status: domain.AgentActive,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
			AgentID: agent.ID, AgentServiceID: services[0].ID, Model: agent.Model,
		}, nil); createErr != nil {
			t.Fatal(createErr)
		}
		return agent
	}
	agentA := createAgent("循环助手 A")
	agentB := createAgent("循环助手 B")

	var cycleMu sync.Mutex
	starts := map[string]int{}
	runtime.onStart = func(runContext context.Context, payload nodeprotocol.StartRunPayload) error {
		cycleMu.Lock()
		starts[payload.AgentID]++
		count := starts[payload.AgentID]
		cycleMu.Unlock()
		body := "A 完成循环"
		mentioned := []any{}
		switch {
		case payload.AgentID == agentA.ID && count == 1:
			body = "A 委派给 B"
			mentioned = []any{agentB.ID}
		case payload.AgentID == agentB.ID && count == 1:
			body = "B 回委派给 A"
			mentioned = []any{agentA.ID}
		case payload.AgentID != agentA.ID || count != 2:
			return fmt.Errorf("unexpected cycle activation agent=%s count=%d", payload.AgentID, count)
		}
		_, toolErr := caller.SendToolRequest(runContext, nodeprotocol.NodeRequest{
			RequestID: "cycle-tool-" + payload.RunID, Tool: "issue_comment_create",
			RunID: payload.RunID, IssueID: payload.IssueID, AgentID: payload.AgentID,
			InstanceID: payload.IssueAgentInstanceID,
			Payload:    map[string]any{"body": body, "mentioned_agent_ids": mentioned},
		})
		return toolErr
	}

	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{
		ProjectID: project.ID, Title: "A 到 B 再回到 A", Status: domain.IssueOpen,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateIssueComment(ctx, user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: user.ID, Body: "@A 启动循环委派",
	}, []string{agentA.ID}); err != nil {
		t.Fatal(err)
	}
	local.NotifyIssueActivation(issue.ID)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		instances, listErr := repository.ListIssueAgentInstances(ctx, user.ID, issue.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		// A -> B -> A is three @ activations and therefore three independent
		// conversations, including two separate conversations owned by Agent A.
		if len(instances) == 3 {
			totalRuns := 0
			allSucceeded := true
			for _, instance := range instances {
				runs, runErr := repository.ListRunsByInstance(ctx, user.ID, instance.ID, 10)
				if runErr != nil {
					t.Fatal(runErr)
				}
				totalRuns += len(runs)
				for _, run := range runs {
					if run.Status != domain.RunSucceeded {
						allSucceeded = false
					}
				}
			}
			if totalRuns == 3 && allSucceeded {
				comments, commentErr := repository.ListIssueComments(ctx, user.ID, issue.ID)
				if commentErr != nil {
					t.Fatal(commentErr)
				}
				if len(comments) != 4 || comments[1].Body != "A 委派给 B" ||
					comments[2].Body != "B 回委派给 A" || comments[3].Body != "A 完成循环" {
					t.Fatalf("cycle comments=%+v", comments)
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("indirect Agent delegation cycle did not complete")
}

func TestLocalDispatcherExecutesManualSchedule(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, _ := repository.LocalIdentity()
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, _ := waitForAgentServices(t, repository, user.ID, computer.ID)
	project, _ := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "计划项目"})
	agent, _ := repository.CreateAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: services[0].ID,
		Model: "deepseek/test", Name: "计划助手", Status: domain.AgentActive,
	})
	_, err = repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: services[0].ID, Model: "deepseek/test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{
		ProjectID: project.ID, Name: "手动计划", AgentID: agent.ID,
		ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "执行计划"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger, replayed, err := repository.TriggerScheduledTask(ctx, task.ID, "manual:test", time.Now().UTC())
	if err != nil || replayed || trigger.RunID == "" {
		t.Fatalf("trigger = %+v replayed=%t err=%v", trigger, replayed, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, getErr := repository.GetRun(ctx, user.ID, trigger.RunID)
		if getErr == nil && run.Status == domain.RunSucceeded {
			runs, _ := repository.ListScheduledTaskRuns(ctx, user.ID, task.ID)
			if len(runs) != 1 || runs[0].Status != domain.RunSucceeded {
				t.Fatalf("scheduled runs = %+v", runs)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("scheduled run did not complete")
}

func TestLocalDispatcherStopsActiveRun(t *testing.T) {
	ctx := context.Background()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, _ := repository.LocalIdentity()
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := newBlockingRuntime()
	traced := newLocalLeaseTraceStore(repository)
	local, err := newLocalWithController(ctx, traced, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	// Leave real scheduler/SQLite headroom under parallel -race load, and prove
	// renewal by observing committed lease state rather than assuming a 400ms
	// sleep implies all 40ms renewal ticks ran on time. Handle stays blocked
	// until after both the original deadline and a successful later renewal.
	const lease = 2 * time.Second
	local.commandLeaseNanos.Store(int64(lease))
	t.Cleanup(func() {
		_ = local.Close(context.Background())
		_ = repository.Close()
	})
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services: count=%d err=%v", len(services), err)
	}
	run := localLeaseRunFixture(t, repository, user, computer, services[0], "stop-test")
	select {
	case started := <-runtime.started:
		if started != run.ID {
			t.Fatal("runtime started a different Run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not start")
	}
	var original store.LocalCommand
	select {
	case original = <-traced.claims:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime start has no observed durable claim")
	}
	if original.Name != "run.start" || original.LeaseUntil == nil || original.ClaimToken == "" {
		t.Fatal("invalid initial run.start claim")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	renewalsBeforeExpiry := 0
	for {
		select {
		case renewal := <-traced.renewals:
			if renewal.Err != nil {
				t.Fatalf("lease renewal failed: original_expiry=%s renewal_now=%s observed_at=%s err=%v", original.LeaseUntil, renewal.Now, renewal.At, renewal.Err)
			}
			if renewal.Command.ID != original.ID || renewal.Command.ClaimToken != original.ClaimToken ||
				renewal.Command.Attempts != 1 || renewal.Command.LeaseUntil == nil || !renewal.Command.LeaseUntil.After(*original.LeaseUntil) {
				t.Fatal("successful renewal did not extend the original owned claim")
			}
			if renewal.Now.Before(*original.LeaseUntil) {
				renewalsBeforeExpiry++
				continue
			}
			if renewalsBeforeExpiry == 0 || !time.Now().After(*original.LeaseUntil) {
				t.Fatal("did not observe a renewal on both sides of the original lease deadline")
			}
			t.Logf("claim crossed original lease with %d committed earlier renewal(s), then renewed at %s (original expiry %s)", renewalsBeforeExpiry, renewal.Now, original.LeaseUntil)
		case <-deadline.C:
			t.Fatal("no successful renewal after the original lease deadline")
		}
		break
	}
	if command, ok, err := repository.ClaimLocalCommand(ctx, time.Now().UTC(), lease); err != nil || ok {
		t.Fatalf("blocking run.start became claimable after its original lease: command=%+v ok=%t err=%v", command, ok, err)
	}
	runtime.mu.Lock()
	starts := runtime.starts
	runtime.mu.Unlock()
	if starts != 1 {
		t.Fatalf("blocking run.start delivered %d times, want 1", starts)
	}
	if _, err := repository.StopRun(ctx, user.ID, run.ID, "force"); err != nil {
		t.Fatal(err)
	}
	stopContext, cancelStop := context.WithTimeout(ctx, 5*time.Second)
	defer cancelStop()
	if err := local.StopLocalRun(stopContext, run.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := repository.GetRun(ctx, user.ID, run.ID)
	if err != nil || finished.Status != domain.RunCancelled {
		t.Fatalf("run = %+v err=%v", finished, err)
	}
}
