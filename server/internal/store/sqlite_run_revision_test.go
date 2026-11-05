package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func saveSQLiteFollowupConfiguration(t *testing.T, f sqliteExecutionFixture, skills []string) domain.Agent {
	t.Helper()
	agent, err := f.repository.GetAgent(t.Context(), f.user.ID, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, prompt := "deepseek/deepseek-flash", "new saved system prompt"
	agent, err = f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version,
		AgentConfigurationPatch{Model: &model, SystemPrompt: &prompt, SkillVersionIDs: skills})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func sqliteRevisionRunEnvelope(t *testing.T, f sqliteExecutionFixture, run domain.Run) (string, string, nodeprotocol.StartRunPayload) {
	t.Helper()
	var snapshot, raw string
	if err := f.repository.db.QueryRowContext(t.Context(), `
		SELECT run.snapshot, command.payload FROM runs run JOIN command_outbox command
		  ON command.command_name = 'run.start' AND json_extract(command.payload, '$.run_id') = run.id
		WHERE run.id = ?`, run.ID).Scan(&snapshot, &raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return snapshot, raw, payload
}

func TestSQLiteOrdinaryFollowupUsesCurrentRevisionOnSameInstance(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	old := f.createRun(t, "old-configuration")
	finishSQLiteIsolationRun(t, f, old, "old history")
	old, err := f.repository.GetRun(t.Context(), f.user.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot, oldRaw, oldPayload := sqliteRevisionRunEnvelope(t, f, old)
	installed := installSkillUninstallTestPackage(t, sqliteAgentTestFixture{
		repository: f.repository, user: f.user, computer: f.computer,
	})
	current := saveSQLiteFollowupConfiguration(t, f, []string{installed.SkillPackageVersionID})

	// A newer @ conversation must not hijack the selected historical instance.
	if _, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "new activation",
	}, []string{f.agent.ID}); err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repository.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mentions=%+v err=%v", mentions, err)
	}
	newMention, err := f.repository.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.agent.ID,
		mentions[0].ID, []domain.ContentBlock{{Type: "text", Text: "new mention"}}, "new-mention")
	if err != nil {
		t.Fatal(err)
	}
	newerBefore, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, newMention.IssueAgentInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	next := f.createRun(t, "current-configuration")
	if next.IssueAgentInstanceID != old.IssueAgentInstanceID || next.IssueAgentInstanceID == newMention.IssueAgentInstanceID ||
		next.IssueAgentGenerationID == old.IssueAgentGenerationID || next.Model != current.Model {
		t.Fatalf("follow-up changed instance or reused stale generation/config: %+v", next)
	}
	snapshot, _, payload := sqliteRevisionRunEnvelope(t, f, next)
	if !strings.Contains(snapshot, current.CurrentRevisionID) || payload.Model != current.Model ||
		payload.SystemPrompt != "new saved system prompt" || len(payload.Skills) != 1 ||
		payload.Skills[0].Slug != "uninstall-test" || payload.NativeSessionID != "" ||
		payload.IssueAgentGenerationID != next.IssueAgentGenerationID {
		t.Fatalf("new run snapshot/payload not current: snapshot=%s payload=%+v", snapshot, payload)
	}
	instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil || instance.AgentRevisionID != current.CurrentRevisionID || instance.Status != domain.AgentInstanceRunning {
		t.Fatalf("selected instance not atomically advanced: %+v err=%v", instance, err)
	}
	newerAfter, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, newerBefore.ID)
	if err != nil || !reflect.DeepEqual(newerBefore, newerAfter) {
		t.Fatalf("newer conversation was modified: %+v err=%v", newerAfter, err)
	}
	for _, pair := range []struct{ generation, revision string }{
		{old.IssueAgentGenerationID, f.instance.AgentRevisionID}, {next.IssueAgentGenerationID, current.CurrentRevisionID},
	} {
		var revision string
		if err := f.repository.db.QueryRowContext(t.Context(), `SELECT agent_revision_id FROM issue_agent_generations WHERE id = ?`,
			pair.generation).Scan(&revision); err != nil || revision != pair.revision {
			t.Fatalf("generation revision=%s want=%s err=%v", revision, pair.revision, err)
		}
	}
	// Even after another save, a replay bypasses version/busy checks and returns
	// exactly the original Run without moving either instance or frozen payload.
	third := saveSQLiteFollowupConfiguration(t, f, []string{})
	if third.CurrentRevisionID == current.CurrentRevisionID {
		t.Fatal("fixture failed to publish another revision")
	}
	replayed, wasReplayed, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, 999,
		"old-configuration", domain.Run{})
	if err != nil || !wasReplayed || !reflect.DeepEqual(replayed, old) {
		t.Fatalf("old replay changed: %+v replayed=%t err=%v", replayed, wasReplayed, err)
	}
	after, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil || !reflect.DeepEqual(instance, after) {
		t.Fatalf("replay refreshed instance: %+v err=%v", after, err)
	}
	afterSnapshot, afterRaw, afterPayload := sqliteRevisionRunEnvelope(t, f, old)
	if afterSnapshot != oldSnapshot || afterRaw != oldRaw || !reflect.DeepEqual(afterPayload, oldPayload) {
		t.Fatal("historical Run snapshot or start payload changed")
	}
}

func TestSQLiteOrdinaryFollowupRevisionRefreshPreservesFencesAndAtomicity(t *testing.T) {
	for _, mode := range []string{"queued", "starting", "running", "waiting_approval", "cancelling", "paused",
		"version_conflict", "closed", "goal_active", "goal_paused", "goal_blocked", "goal_budget_reached",
		"old_generation", "outbox_failure", "instance_failure", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			t.Cleanup(func() { _ = f.repository.Close() })
			input := domain.Run{IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
				Input: []domain.ContentBlock{{Type: "text", Text: "continue"}}}
			expected, code := f.issue.Version, "target_busy"
			switch mode {
			case "queued", "starting", "running", "waiting_approval", "cancelling", "old_generation":
				old := f.createRun(t, "existing")
				if mode == "old_generation" {
					finishSQLiteIsolationRun(t, f, old, "history")
					input.IssueAgentGenerationID = old.IssueAgentGenerationID
					code = "not_found"
				} else if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE runs SET status = ? WHERE id = ?`, mode, old.ID); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, f.instance.ID, f.instance.Version,
					func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
					t.Fatal(err)
				}
				code = "agent_instance_paused"
			case "version_conflict":
				expected++
				code = "version_conflict"
			case "closed":
				issue, err := f.repository.UpdateIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version,
					func(i *domain.Issue) { i.Status = "closed" })
				if err != nil {
					t.Fatal(err)
				}
				expected, code = issue.Version, "issue_not_open"
			case "goal_active", "goal_paused", "goal_blocked", "goal_budget_reached":
				work, goal, _, err := f.repository.CreateIssueAgentGoalRun(t.Context(), f.user.ID, f.issue.ID,
					f.instance.ID, f.issue.Version, "goal", "goal objective", nil, input)
				if err != nil {
					t.Fatal(err)
				}
				finishSQLiteIsolationRun(t, f, work, "goal progress")
				if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE issue_agent_goals SET status = ? WHERE id = ?`,
					strings.TrimPrefix(mode, "goal_"), goal.ID); err != nil {
					t.Fatal(err)
				}
				code = "goal_already_exists"
			case "outbox_failure", "instance_failure":
				trigger := `CREATE TRIGGER reject_revision_refresh BEFORE INSERT ON command_outbox
					WHEN NEW.command_name = 'run.start' BEGIN SELECT RAISE(ABORT, 'reject outbox'); END`
				if mode == "instance_failure" {
					trigger = `CREATE TRIGGER reject_revision_refresh BEFORE UPDATE ON issue_agent_instances
						BEGIN SELECT RAISE(ABORT, 'reject instance'); END`
				}
				if _, err := f.repository.db.ExecContext(t.Context(), trigger); err != nil {
					t.Fatal(err)
				}
				code = ""
			case "unavailable":
				code = "runtime_unavailable"
			}
			current := saveSQLiteFollowupConfiguration(t, f, nil)
			if mode == "unavailable" {
				if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE agent_services SET status = 'unavailable' WHERE id = ?`, current.AgentServiceID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			counts := func() [4]int {
				var result [4]int
				for i, table := range []string{"runs", "issue_agent_generations", "command_outbox", "run_events"} {
					if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&result[i]); err != nil {
						t.Fatal(err)
					}
				}
				return result
			}
			beforeCounts := counts()
			_, replayed, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, expected, "followup", input)
			if err == nil || replayed {
				t.Fatalf("fence bypassed: replayed=%t err=%v", replayed, err)
			}
			if code != "" {
				assertDomainCode(t, err, code)
			}
			after, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
			if err != nil || !reflect.DeepEqual(before, after) || counts() != beforeCounts {
				t.Fatalf("failed run changed instance/envelope: before=%+v after=%+v counts=%v/%v err=%v",
					before, after, beforeCounts, counts(), err)
			}
		})
	}
}

func TestSQLiteGoalKeepsFrozenRevisionAfterAgentConfigurationSave(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	oldSnapshot, _, oldPayload := sqliteRevisionRunEnvelope(t, f, work)
	current := saveSQLiteFollowupConfiguration(t, f, nil)
	if current.CurrentRevisionID == goal.AgentRevisionID {
		t.Fatal("fixture did not save a new configuration")
	}
	finishSQLiteIsolationRun(t, f, work, "progress")
	for _, stage := range []string{"audit", "continuation", "audit", "finalize"} {
		advanceSQLiteIsolationGoals(t, f, false)
		run := sqliteIsolationCurrentRun(t, f, goal.ID)
		snapshot, _, payload := sqliteRevisionRunEnvelope(t, f, run)
		if snapshot != oldSnapshot || payload.Model != oldPayload.Model || payload.Provider != oldPayload.Provider ||
			!reflect.DeepEqual(payload.Skills, oldPayload.Skills) {
			t.Fatalf("%s silently changed frozen config: snapshot=%s payload=%+v", stage, snapshot, payload)
		}
		if stage == "audit" {
			if run.Purpose != domain.RunPurposeGoalAudit || payload.SystemPrompt == oldPayload.SystemPrompt {
				t.Fatalf("audit lost its independent system prompt: %+v", run)
			}
			verdict := "continue"
			if sqliteIsolationGoal(t, f, goal.ID).ContinuationsUsed > 0 {
				verdict = "complete"
			}
			finishSQLiteIsolationRun(t, f, run, `{"verdict":"`+verdict+`","note":"verified"}`)
		} else {
			if run.IssueAgentGenerationID != work.IssueAgentGenerationID || payload.SystemPrompt != oldPayload.SystemPrompt {
				t.Fatalf("%s changed frozen generation/prompt: %+v", stage, run)
			}
			finishSQLiteIsolationRun(t, f, run, "progress")
		}
	}
	instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil || instance.AgentRevisionID != goal.AgentRevisionID {
		t.Fatalf("Goal refreshed instance config: %+v err=%v", instance, err)
	}
}

func TestSQLiteOrdinaryFollowupConcurrentRevisionRefreshIsAtomic(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		name := "different_keys"
		if sameKey {
			name = "idempotent_replay"
		}
		t.Run(name, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			t.Cleanup(func() { _ = f.repository.Close() })
			current := saveSQLiteFollowupConfiguration(t, f, nil)
			type result struct {
				run      domain.Run
				replayed bool
				err      error
			}
			results, start := make(chan result, 2), make(chan struct{})
			for _, key := range []string{"first", "second"} {
				if sameKey {
					key = "shared"
				}
				go func(key string) {
					<-start
					run, replayed, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, f.issue.Version,
						key, domain.Run{AgentID: f.agent.ID, IssueAgentInstanceID: f.instance.ID,
							Input: []domain.ContentBlock{{Type: "text", Text: "concurrent follow-up"}}})
					results <- result{run, replayed, err}
				}(key)
			}
			close(start)
			first, second := <-results, <-results
			if first.err != nil {
				first, second = second, first
			}
			if first.err != nil || first.run.Model != current.Model {
				t.Fatalf("no current-config run created: %+v", first)
			}
			if sameKey {
				if second.err != nil || first.run.ID != second.run.ID || first.replayed == second.replayed {
					t.Fatalf("concurrent replay created a second Run: first=%+v second=%+v", first, second)
				}
			} else {
				assertDomainCode(t, second.err, "target_busy")
			}
			instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
			if err != nil || instance.AgentRevisionID != current.CurrentRevisionID || instance.Version != f.instance.Version+1 {
				t.Fatalf("configuration was refreshed more than once: %+v err=%v", instance, err)
			}
			for _, table := range []string{"runs", "issue_agent_generations", "command_outbox", "run_events"} {
				var count int
				if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("concurrent creation duplicated %s: count=%d err=%v", table, count, err)
				}
			}
		})
	}
}
