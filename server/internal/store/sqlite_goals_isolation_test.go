package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Separate Issues, Agents, revisions and services make failures independent,
// while sharing the same control database/dispatcher queue.
func newSQLiteGoalIsolationPair(t *testing.T) (sqliteExecutionFixture, domain.Run, domain.IssueAgentGoal, domain.Run, domain.IssueAgentGoal) {
	t.Helper()
	f, firstRun, first := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	ctx := t.Context()
	service, err := f.repository.UpsertAgentService(ctx, f.user.ID, domain.AgentService{
		ComputerID: f.computer.ID, Provider: "opencode", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.repository.CreateAgent(ctx, f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: service.ID, Name: "healthy goal agent",
		Status: domain.AgentActive, Model: "test/model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.CreateAgentRevision(ctx, f.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: service.ID, Model: "test/model", SystemPrompt: "system",
	}, nil); err != nil {
		t.Fatal(err)
	}
	issue, err := f.repository.CreateIssue(ctx, f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: "healthy goal"})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := f.repository.EnsureIssueAgentInstance(ctx, f.user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, second, _, err := f.repository.CreateIssueAgentGoalRun(ctx, f.user.ID, issue.ID, instance.ID,
		issue.Version, "independent-goal", "完成独立目标", nil, domain.Run{
			AgentID: agent.ID, IssueAgentInstanceID: instance.ID,
			Input: []domain.ContentBlock{{Type: "text", Text: "independent work"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	return f, firstRun, first, secondRun, second
}

func sqliteIsolationGoal(t *testing.T, f sqliteExecutionFixture, goalID string) domain.IssueAgentGoal {
	t.Helper()
	goal, err := f.repository.GetIssueAgentGoal(t.Context(), f.user.ID, goalID)
	if err != nil {
		t.Fatal(err)
	}
	return goal
}

func sqliteIsolationCurrentRun(t *testing.T, f sqliteExecutionFixture, goalID string) domain.Run {
	t.Helper()
	goal := sqliteIsolationGoal(t, f, goalID)
	run, err := f.repository.GetRun(t.Context(), f.user.ID, goal.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func finishSQLiteIsolationRun(t *testing.T, f sqliteExecutionFixture, run domain.Run, text string) {
	t.Helper()
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": text}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
}

func advanceSQLiteIsolationGoals(t *testing.T, f sqliteExecutionFixture, wantError bool) int {
	t.Helper()
	advanced, err := f.repository.AdvanceLocalGoals(t.Context(), time.Now().UTC())
	if (err != nil) != wantError {
		t.Fatalf("advance: changed=%d err=%v, wantError=%t", advanced, err, wantError)
	}
	return advanced
}

func firstSQLiteIsolationGoalInQueue(t *testing.T, f sqliteExecutionFixture, id string) {
	t.Helper()
	if _, err := f.repository.db.ExecContext(t.Context(), `
		UPDATE issue_agent_goals SET updated_at = '2020-01-01T00:00:00Z' WHERE id = ?
	`, id); err != nil {
		t.Fatal(err)
	}
}

func publishSQLiteIsolationSummary(t *testing.T, f sqliteExecutionFixture, goalID string) {
	t.Helper()
	goal := sqliteIsolationGoal(t, f, goalID)
	if _, err := f.repository.db.ExecContext(t.Context(), `
		UPDATE issue_comments SET status = 'published', body = 'fixture verified summary' WHERE id = ?
	`, goal.SummaryCommentID); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteGoalUnavailableInstanceDoesNotStarveHealthyGoal(t *testing.T) {
	for _, mode := range []string{"public_pause", "deleted_instance", "closed_issue"} {
		t.Run(mode, func(t *testing.T) {
			f, firstRun, first, secondRun, second := newSQLiteGoalIsolationPair(t)
			finishSQLiteIsolationRun(t, f, firstRun, "remaining work")
			finishSQLiteIsolationRun(t, f, secondRun, "remaining work")
			advanceSQLiteIsolationGoals(t, f, false)
			finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, first.ID), `{"verdict":"continue","note":"remaining"}`)
			switch mode {
			case "public_pause":
				instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, first.IssueAgentInstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version,
					func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
					t.Fatal(err)
				}
			case "deleted_instance":
				if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE issue_agent_instances SET deleted_at = ? WHERE id = ?`,
					sqliteTimeValue(time.Now()), first.IssueAgentInstanceID); err != nil {
					t.Fatal(err)
				}
			case "closed_issue":
				if _, err := f.repository.UpdateIssue(t.Context(), f.user.ID, first.IssueID, f.issue.Version,
					func(i *domain.Issue) { i.Status = "closed" }); err != nil {
					t.Fatal(err)
				}
			}
			// Repeated queue rounds must remain fair even when the bad Goal was
			// first in the original queue. Its counters cannot be spent on errors.
			firstSQLiteIsolationGoalInQueue(t, f, first.ID)
			for round := 1; round <= 3; round++ {
				finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, second.ID), `{"verdict":"continue","note":"more evidence"}`)
				advanceSQLiteIsolationGoals(t, f, false)
				healthy := sqliteIsolationGoal(t, f, second.ID)
				if healthy.ContinuationsUsed != round || healthy.Phase != domain.GoalPhaseRunning {
					t.Fatalf("healthy round %d: %+v", round, healthy)
				}
				finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, second.ID), "verified incremental work")
				advanceSQLiteIsolationGoals(t, f, false)
			}
			finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, second.ID), `{"verdict":"complete","note":"verified"}`)
			advanceSQLiteIsolationGoals(t, f, false)
			finalize := sqliteIsolationCurrentRun(t, f, second.ID)
			if finalize.Purpose != domain.RunPurposeGoalFinalize {
				t.Fatalf("healthy final run: %+v", finalize)
			}
			publishSQLiteIsolationSummary(t, f, second.ID)
			finishSQLiteIsolationRun(t, f, finalize, "summary explicitly published")
			advanceSQLiteIsolationGoals(t, f, false)
			bad, healthy := sqliteIsolationGoal(t, f, first.ID), sqliteIsolationGoal(t, f, second.ID)
			wantStatus, wantReason := domain.GoalBlocked, "goal_instance_unavailable"
			switch mode {
			case "public_pause":
				wantStatus, wantReason = domain.GoalPaused, "agent_instance_paused"
			case "closed_issue":
				wantReason = "issue_not_open"
			}
			if bad.Status != wantStatus || bad.StatusReason != wantReason || bad.ContinuationsUsed != 0 ||
				healthy.Status != domain.GoalCompleted {
				t.Fatalf("bad=%+v healthy=%+v", bad, healthy)
			}
			if mode != "public_pause" {
				if _, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, bad.ID, bad.Version); err == nil {
					t.Fatal("Goal resume revived an invalid/closed instance context")
				}
			}
		})
	}
}

func TestSQLiteGoalPublicPauseStopsInstanceFreeAuditAndRequiresExplicitGoalResume(t *testing.T) {
	f, work, created := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	finishSQLiteIsolationRun(t, f, work, "audit this work")
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, created.ID)
	instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version,
		func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
		t.Fatal(err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	paused := sqliteIsolationGoal(t, f, created.ID)
	stopping := sqliteIsolationCurrentRun(t, f, created.ID)
	if paused.Status != domain.GoalPaused || stopping.Status != domain.RunCancelling {
		t.Fatalf("paused=%+v run=%+v", paused, stopping)
	}
	var stopCount int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox
		WHERE command_name = 'run.stop' AND json_extract(payload, '$.run_id') = ?`, audit.ID).Scan(&stopCount); err != nil || stopCount != 1 {
		t.Fatalf("audit stop count=%d err=%v", stopCount, err)
	}
	if _, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, paused.ID, paused.Version); err == nil {
		t.Fatal("Goal resumed before its audit stopped")
	}
	// A late result must not restart a paused Goal. Nor does changing only
	// instance.status back to idle mean the user resumed the Goal itself.
	finishSQLiteIsolationRun(t, f, audit, `{"verdict":"complete","note":"late result"}`)
	instance, err = f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version,
		func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstanceIdle }); err != nil {
		t.Fatal(err)
	}
	if changed := advanceSQLiteIsolationGoals(t, f, false); changed != 0 {
		t.Fatalf("public instance resume implicitly resumed Goal: %d", changed)
	}
	paused = sqliteIsolationGoal(t, f, created.ID)
	resumed, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, paused.ID, paused.Version)
	if err != nil || resumed.Status != domain.GoalActive || resumed.SettledAt != nil {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	continuation := sqliteIsolationCurrentRun(t, f, created.ID)
	if continuation.Purpose != domain.RunPurposeGoalContinuation || continuation.IssueAgentInstanceID != work.IssueAgentInstanceID ||
		continuation.IssueAgentGenerationID != work.IssueAgentGenerationID {
		t.Fatalf("Goal resume replaced its original instance/generation: %+v", continuation)
	}
}

func TestSQLiteGoalCancelCannotUnpauseInstanceOrAllowNewGoal(t *testing.T) {
	f, _, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version,
		func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.CancelIssueAgentGoal(t.Context(), f.user.ID, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	instance, err = f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
	if err != nil || instance.Status != domain.AgentInstancePaused {
		t.Fatalf("cancel unpaused instance: %+v err=%v", instance, err)
	}
	_, _, _, err = f.repository.CreateIssueAgentGoalRun(t.Context(), f.user.ID, f.issue.ID, instance.ID,
		f.issue.Version, "new-goal-on-paused", "不能绕过暂停", nil,
		domain.Run{AgentID: f.agent.ID, Input: []domain.ContentBlock{{Type: "text", Text: "work"}}})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "agent_instance_paused" {
		t.Fatalf("new Goal bypassed pause: %v", err)
	}
	var goals, comments int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_agent_goals`).Scan(&goals); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_comments`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if goals != 1 || comments != 1 {
		t.Fatalf("failed creation left partial rows: goals=%d comments=%d", goals, comments)
	}
}

func TestSQLiteGoalPublicInstancePauseSurvivesTerminalEventBeforeGoalTick(t *testing.T) {
	for _, tc := range []struct {
		name        string
		publicPause bool
		cancelGoal  bool
	}{
		{name: "public_pause", publicPause: true},
		{name: "public_pause_then_cancel", publicPause: true, cancelGoal: true},
		{name: "cancel_without_public_pause", cancelGoal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, work, goal := createSQLiteGoalFixture(t, nil)
			t.Cleanup(func() { _ = f.repository.Close() })
			applySQLiteGoalEvents(t, f.repository, work, nodeprotocol.RunEvent{Type: "run.started"})
			running, err := f.repository.GetRun(t.Context(), f.user.ID, work.ID)
			if err != nil || running.Status != domain.RunRunning {
				t.Fatalf("work did not start: %+v err=%v", running, err)
			}
			instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.publicPause {
				if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version,
					func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
					t.Fatal(err)
				}
			}
			if tc.cancelGoal {
				if _, err := f.repository.CancelIssueAgentGoal(t.Context(), f.user.ID, goal.ID, goal.Version); err != nil {
					t.Fatal(err)
				}
			}
			// This event is deliberately delivered before AdvanceLocalGoals can
			// synchronize Goal.status with the public instance pause.
			if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
				ID: work.ID + "-late-terminal", RunID: work.ID, LeaseEpoch: work.LeaseEpoch,
				Sequence: 2, Type: "run.completed", OccurredAt: time.Now(),
				Payload: map[string]any{"status": "completed", "provider_id": "codex"},
			}}); err != nil {
				t.Fatal(err)
			}
			instance, err = f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, instance.ID)
			wantInstance := domain.AgentInstancePaused
			if !tc.publicPause {
				wantInstance = domain.AgentInstanceIdle
			}
			if err != nil || instance.Status != wantInstance {
				t.Fatalf("terminal event erased public pause: instance=%+v err=%v", instance, err)
			}
			advanceSQLiteIsolationGoals(t, f, false)
			current := sqliteIsolationGoal(t, f, goal.ID)
			want := domain.GoalPaused
			if tc.cancelGoal {
				want = domain.GoalCancelled
			}
			if current.Status != want || current.ContinuationsUsed != 0 {
				t.Fatalf("pause/cancel intent was lost: %+v", current)
			}
		})
	}
}

func TestSQLiteGoalTransientProviderFailureIsObservableAndAutomaticallyRetried(t *testing.T) {
	f, firstWork, first, secondWork, second := newSQLiteGoalIsolationPair(t)
	finishSQLiteIsolationRun(t, f, firstWork, "ready for audit")
	finishSQLiteIsolationRun(t, f, secondWork, "ready for audit")
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE agent_services SET status = 'unavailable' WHERE id = ?`, f.service.ID); err != nil {
		t.Fatal(err)
	}
	firstSQLiteIsolationGoalInQueue(t, f, first.ID)
	for range 3 {
		_, err := f.repository.AdvanceLocalGoals(t.Context(), time.Now().UTC())
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != "runtime_unavailable" || !de.Retryable {
			t.Fatalf("temporary failure was swallowed: %v", err)
		}
		bad := sqliteIsolationGoal(t, f, first.ID)
		if bad.Status != domain.GoalActive || bad.StatusReason != "runtime_unavailable" || bad.CurrentRunID != firstWork.ID || bad.ContinuationsUsed != 0 {
			t.Fatalf("temporary failure became permanent/consumed budget: %+v", bad)
		}
		if healthy := sqliteIsolationGoal(t, f, second.ID); healthy.Phase != domain.GoalPhaseEvaluating {
			t.Fatalf("healthy Goal starved: %+v", healthy)
		}
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE agent_services SET status = 'ready' WHERE id = ?`, f.service.ID); err != nil {
		t.Fatal(err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	recovered := sqliteIsolationGoal(t, f, first.ID)
	if recovered.Status != domain.GoalActive || recovered.Phase != domain.GoalPhaseEvaluating || recovered.StatusReason != "" {
		t.Fatalf("provider recovery required manual unblock: %+v", recovered)
	}
}

func TestSQLiteGoalFailedEnqueueRollsBackPartialRowsAndDoesNotStarveFinalize(t *testing.T) {
	f, firstWork, first, secondWork, second := newSQLiteGoalIsolationPair(t)
	finishSQLiteIsolationRun(t, f, firstWork, "ready for audit")
	finishSQLiteIsolationRun(t, f, secondWork, "ready for audit")
	// Fail after Run + event insertion, at outbox insertion. The trigger text
	// intentionally resembles private input and must not escape in diagnostics.
	const privateFixture = "fixture-private-objective-and-token"
	if _, err := f.repository.db.ExecContext(t.Context(), fmt.Sprintf(`
		CREATE TRIGGER goal_isolation_outbox_failure BEFORE INSERT ON command_outbox
		WHEN NEW.command_name = 'run.start' AND EXISTS (
		 SELECT 1 FROM runs WHERE id = json_extract(NEW.payload, '$.run_id') AND goal_id = '%s'
		) BEGIN SELECT RAISE(ABORT, '%s'); END
	`, first.ID, privateFixture)); err != nil {
		t.Fatal(err)
	}
	checkAdvance := func() {
		t.Helper()
		firstSQLiteIsolationGoalInQueue(t, f, first.ID)
		_, err := f.repository.AdvanceLocalGoals(t.Context(), time.Now().UTC())
		if err == nil || strings.Contains(err.Error(), privateFixture) || !strings.Contains(err.Error(), "advance_failed") {
			t.Fatalf("missing/unsafe aggregate failure: %v", err)
		}
	}
	checkAdvance()
	failed := sqliteIsolationGoal(t, f, first.ID)
	for range 2 {
		checkAdvance()
		if current := sqliteIsolationGoal(t, f, first.ID); current.Version != failed.Version || current.ContinuationsUsed != 0 {
			t.Fatalf("retry changed counters/version despite same failure: %+v -> %+v", failed, current)
		}
	}
	finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, second.ID), `{"verdict":"complete","note":"verified"}`)
	checkAdvance()
	finalize := sqliteIsolationCurrentRun(t, f, second.ID)
	if finalize.Purpose != domain.RunPurposeGoalFinalize {
		t.Fatalf("healthy finalize starved: %+v", finalize)
	}
	publishSQLiteIsolationSummary(t, f, second.ID)
	finishSQLiteIsolationRun(t, f, finalize, "published")
	checkAdvance()
	if healthy := sqliteIsolationGoal(t, f, second.ID); healthy.Status != domain.GoalCompleted {
		t.Fatalf("healthy settlement starved: %+v", healthy)
	}
	var badRuns int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM runs WHERE goal_id = ?`, first.ID).Scan(&badRuns); err != nil || badRuns != 1 {
		t.Fatalf("partial enqueue survived rollback: count=%d err=%v", badRuns, err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER goal_isolation_outbox_failure`); err != nil {
		t.Fatal(err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, first.ID)
	var starts, events int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?`, audit.ID).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM run_events WHERE run_id = ?`, audit.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if audit.Purpose != domain.RunPurposeGoalAudit || starts != 1 || events != 1 ||
		sqliteIsolationGoal(t, f, first.ID).StatusReason != "" {
		t.Fatalf("retry lost atomicity/idempotency: run=%+v starts=%d events=%d", audit, starts, events)
	}
}

func TestSQLiteGoalFailedContinuationEnqueuePreservesCountersAndBudget(t *testing.T) {
	budget := int64(1_000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	applySQLiteGoalEvents(t, f.repository, work,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(200)}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, goal.ID)
	finishSQLiteIsolationRun(t, f, audit, `{"verdict":"blocked","note":"user dependency"}`)
	if _, err := f.repository.db.ExecContext(t.Context(), `
		CREATE TRIGGER goal_continuation_failure BEFORE INSERT ON command_outbox
		WHEN NEW.command_name = 'run.start' AND EXISTS (
		 SELECT 1 FROM runs WHERE id = json_extract(NEW.payload, '$.run_id') AND purpose = 'goal_continuation'
		) BEGIN SELECT RAISE(ABORT, 'fixture enqueue unavailable'); END
	`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		advanceSQLiteIsolationGoals(t, f, true)
		current := sqliteIsolationGoal(t, f, goal.ID)
		if current.Status != domain.GoalActive || current.ContinuationsUsed != 0 || current.BlockedStreak != 0 ||
			current.AuditFailureStreak != 0 || current.CurrentRunID != audit.ID || current.TokensUsed != 200 ||
			current.TokenBudget == nil || *current.TokenBudget != budget {
			t.Fatalf("rolled-back continuation spent counters/budget: %+v", current)
		}
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER goal_continuation_failure`); err != nil {
		t.Fatal(err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	current := sqliteIsolationGoal(t, f, goal.ID)
	continuation := sqliteIsolationCurrentRun(t, f, goal.ID)
	if current.ContinuationsUsed != 1 || current.BlockedStreak != 1 || current.StatusReason != "" || continuation.Purpose != domain.RunPurposeGoalContinuation {
		t.Fatalf("retry was not exactly one continuation: goal=%+v run=%+v", current, continuation)
	}
	applySQLiteGoalEvents(t, f.repository, continuation,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(900)}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanceSQLiteIsolationGoals(t, f, false)
	current = sqliteIsolationGoal(t, f, goal.ID)
	if current.Status != domain.GoalBudgetReached || current.TokensUsed != 1_100 || current.ContinuationsUsed != 1 {
		t.Fatalf("budget accounting changed across failed retries: %+v", current)
	}
}

func TestSQLiteGoalConcurrentAdvancePreservesLeasesAndMultiRoundFairness(t *testing.T) {
	f, firstWork, first, secondWork, second := newSQLiteGoalIsolationPair(t)
	finishSQLiteIsolationRun(t, f, firstWork, "remaining work")
	finishSQLiteIsolationRun(t, f, secondWork, "remaining work")
	advanceConcurrently := func(want int) {
		t.Helper()
		type result struct {
			changed int
			err     error
		}
		results := make(chan result, 12)
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				changed, err := f.repository.AdvanceLocalGoals(t.Context(), time.Now().UTC())
				results <- result{changed, err}
			}()
		}
		wg.Wait()
		close(results)
		total := 0
		for r := range results {
			if r.err != nil {
				t.Fatal(r.err)
			}
			total += r.changed
		}
		if total != want {
			t.Fatalf("concurrent transitions=%d, want %d", total, want)
		}
	}
	advanceConcurrently(2)
	for round := 1; round <= 3; round++ {
		for _, goalID := range []string{first.ID, second.ID} {
			audit := sqliteIsolationCurrentRun(t, f, goalID)
			// A mismatched runtime lease is acknowledged but cannot settle
			// an audit or trigger a continuation in another dispatch tick.
			if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
				ID: fmt.Sprintf("stale-audit-%d-%s", round, goalID), RunID: audit.ID,
				LeaseEpoch: audit.LeaseEpoch + 1, Sequence: 1, Type: "run.completed",
				OccurredAt: time.Now(), Payload: map[string]any{"status": "completed", "provider_id": "codex"},
			}}); err != nil {
				t.Fatal(err)
			}
		}
		advanceConcurrently(0)
		for _, goalID := range []string{first.ID, second.ID} {
			finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, goalID), `{"verdict":"continue","note":"remaining"}`)
		}
		advanceConcurrently(2)
		for _, goalID := range []string{first.ID, second.ID} {
			goal := sqliteIsolationGoal(t, f, goalID)
			run := sqliteIsolationCurrentRun(t, f, goalID)
			if goal.ContinuationsUsed != round || run.Purpose != domain.RunPurposeGoalContinuation || run.LeaseEpoch != 1 {
				t.Fatalf("round %d: goal=%+v run=%+v", round, goal, run)
			}
			finishSQLiteIsolationRun(t, f, run, "verified progress")
		}
		advanceConcurrently(2)
	}
	var runs, uniqueKeys int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*), count(DISTINCT idempotency_key) FROM runs
		WHERE goal_id IN (?, ?)`, first.ID, second.ID).Scan(&runs, &uniqueKeys); err != nil {
		t.Fatal(err)
	}
	if runs != 16 || uniqueKeys != runs {
		t.Fatalf("duplicate/lost transitions: runs=%d keys=%d", runs, uniqueKeys)
	}
}
