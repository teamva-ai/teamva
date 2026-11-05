package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Snapshot every table a Goal resume/dispatch can change, including complete
// row contents rather than just counts. Refused resumes must be read-only.
func snapshotSQLiteGoalResumeState(t *testing.T, repository *SQLite) string {
	t.Helper()
	snapshot := make(map[string][][]any)
	for _, table := range []string{
		"issue_agent_goals", "issue_agent_instances", "issue_agent_generations",
		"issue_comments", "runs", "run_events", "command_outbox",
	} {
		rows, err := repository.db.QueryContext(t.Context(), `SELECT * FROM `+table+` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if bytes, ok := value.([]byte); ok {
					values[i] = string(bytes)
				}
			}
			snapshot[table] = append(snapshot[table], values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func sqliteGoalBudgetError(t *testing.T, err error, budget, used int64) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "goal_budget_reached" || de.HTTPStatus != 409 || de.Retryable {
		t.Fatalf("resume error=%v, want non-retryable goal_budget_reached/409", err)
	}
	if len(de.Details) != 2 || de.Details["token_budget"] != budget || de.Details["tokens_used"] != used {
		t.Fatalf("budget error details=%v, want only numeric budget=%d used=%d", de.Details, budget, used)
	}
	if strings.Contains(err.Error(), "完成实现并验证测试") {
		t.Fatal("budget error contains the objective")
	}
}

func TestSQLiteGoalBudgetReachedResumeCannotCreateAnotherRun(t *testing.T) {
	for _, tc := range []struct {
		budget int64
		used   int64
	}{{1_000, 1_000}, {60_000, 139_950}} {
		t.Run(fmt.Sprintf("budget_%d_used_%d", tc.budget, tc.used), func(t *testing.T) {
			f, work, goal := createSQLiteGoalFixture(t, &tc.budget)
			t.Cleanup(func() { _ = f.repository.Close() })
			applySQLiteGoalEvents(t, f.repository, work,
				nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": tc.used}},
				nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
			)
			advanceSQLiteIsolationGoals(t, f, false)
			settled := sqliteIsolationGoal(t, f, goal.ID)
			if settled.Status != domain.GoalBudgetReached || settled.TokensUsed != tc.used {
				t.Fatalf("budget boundary did not settle: %+v", settled)
			}
			before := snapshotSQLiteGoalResumeState(t, f.repository)
			for range 2 {
				_, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, settled.Version)
				if err == nil {
					// This is the old bug: accepted resume queues another billable
					// work round on the very next tick, despite the same budget.
					advanceSQLiteIsolationGoals(t, f, false)
					var runs, starts int
					_ = f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM runs WHERE goal_id = ?`, goal.ID).Scan(&runs)
					_ = f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'`).Scan(&starts)
					t.Fatalf("exhausted resume was accepted and created extra work: runs=%d run.start=%d (both must stay 1)", runs, starts)
				}
				sqliteGoalBudgetError(t, err, tc.budget, tc.used)
				if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
					t.Fatal("refused exhausted resume changed database rows")
				}
			}
			if changed := advanceSQLiteIsolationGoals(t, f, false); changed != 0 {
				t.Fatalf("refused exhausted Goal advanced: %d", changed)
			}
			_, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, settled.ID, settled.Version-1)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != "version_conflict" {
				t.Fatalf("budget guard bypassed optimistic version check: %v", err)
			}
			if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
				t.Fatal("stale-version resume changed database rows")
			}
			cancelled, err := f.repository.CancelIssueAgentGoal(t.Context(), f.user.ID, settled.ID, settled.Version)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, cancelled.ID, cancelled.Version)
			if !errors.As(err, &de) || de.Code != "goal_not_resumable" {
				t.Fatalf("budget guard revived a cancelled Goal: %v", err)
			}
		})
	}
}

func TestSQLiteGoalResumeSeesLateDeduplicatedUsageInPausedAndBlockedGoals(t *testing.T) {
	for _, status := range []string{domain.GoalPaused, domain.GoalBlocked} {
		t.Run(status, func(t *testing.T) {
			budget := int64(1_000)
			f, work, goal := createSQLiteGoalFixture(t, &budget)
			t.Cleanup(func() { _ = f.repository.Close() })
			if status == domain.GoalPaused {
				if _, err := f.repository.PauseIssueAgentGoal(t.Context(), f.user.ID, goal.ID, goal.Version); err != nil {
					t.Fatal(err)
				}
			}
			terminal := nodeprotocol.RunEvent{
				ID: work.ID + "-terminal", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 1,
				Type: "run.finished", OccurredAt: time.Now().UTC(),
				Payload: map[string]any{"status": "failed", "provider_id": "codex"},
			}
			if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{terminal}); err != nil {
				t.Fatal(err)
			}
			advanceSQLiteIsolationGoals(t, f, false)
			stopped := sqliteIsolationGoal(t, f, goal.ID)
			if stopped.Status != status {
				t.Fatalf("stopped Goal=%+v, want %s", stopped, status)
			}
			usage := []nodeprotocol.RunEvent{{
				ID: work.ID + "-late-usage-1", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 2,
				Type: "usage.updated", OccurredAt: time.Now().UTC(),
				Payload: map[string]any{"total_tokens": int64(500), "accounting_mode": "delta", "provider_id": "codex"},
			}, {
				ID: work.ID + "-late-usage-2", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 3,
				Type: "usage.updated", OccurredAt: time.Now().UTC(),
				Payload: map[string]any{"total_tokens": int64(500), "accounting_mode": "delta", "provider_id": "codex"},
			}}
			for range 2 {
				if _, err := f.repository.ApplyLocalEvents(t.Context(), usage); err != nil {
					t.Fatal(err)
				}
			}
			var cached int64
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT tokens_used FROM issue_agent_goals WHERE id = ?`, goal.ID).Scan(&cached); err != nil || cached != 0 {
				t.Fatalf("fixture must retain stale cached usage: cached=%d err=%v", cached, err)
			}
			before := snapshotSQLiteGoalResumeState(t, f.repository)
			_, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, stopped.Version)
			sqliteGoalBudgetError(t, err, budget, 1_000)
			current, found, err := f.repository.GetCurrentIssueAgentGoal(t.Context(), f.user.ID, f.instance.ID)
			if err != nil || !found || current.TokensUsed != 1_000 || current.Version != stopped.Version || current.Status != status {
				t.Fatalf("late-usage read projection=%+v found=%t err=%v", current, found, err)
			}
			byID := sqliteIsolationGoal(t, f, goal.ID)
			if byID.TokensUsed != current.TokensUsed || byID.Version != stopped.Version {
				t.Fatalf("Goal read projections differ: byID=%+v current=%+v", byID, current)
			}
			if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
				t.Fatal("late-usage rejection/read projections wrote database rows")
			}
		})
	}
}

func TestSQLiteGoalWithinBudgetResumeDeduplicatesUsageAndPreservesVersion(t *testing.T) {
	budget := int64(1_000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	paused, err := f.repository.PauseIssueAgentGoal(t.Context(), f.user.ID, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	events := []nodeprotocol.RunEvent{
		{ID: work.ID + "-u1", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 1, Type: "usage.updated", OccurredAt: time.Now().UTC(), Payload: map[string]any{"total_tokens": int64(300)}},
		{ID: work.ID + "-u2", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 2, Type: "usage.updated", OccurredAt: time.Now().UTC(), Payload: map[string]any{"total_tokens": int64(300)}},
		{ID: work.ID + "-done", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 3, Type: "run.finished", OccurredAt: time.Now().UTC(), Payload: map[string]any{"status": "cancelled"}},
	}
	for range 2 {
		if _, err := f.repository.ApplyLocalEvents(t.Context(), events); err != nil {
			t.Fatal(err)
		}
	}
	resumed, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, paused.Version)
	if err != nil || resumed.Status != domain.GoalActive || resumed.TokensUsed != 600 || resumed.TokenBudget == nil || *resumed.TokenBudget != budget {
		t.Fatalf("valid within-budget resume=%+v err=%v", resumed, err)
	}
	_, err = f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, paused.Version)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "version_conflict" {
		t.Fatalf("stale resume version error=%v", err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	if changed := advanceSQLiteIsolationGoals(t, f, false); changed != 0 {
		t.Fatalf("duplicate dispatch advanced %d Goals", changed)
	}
	continuation := sqliteIsolationCurrentRun(t, f, goal.ID)
	if continuation.Purpose != domain.RunPurposeGoalContinuation || continuation.IssueAgentInstanceID != work.IssueAgentInstanceID ||
		continuation.IssueAgentGenerationID != work.IssueAgentGenerationID {
		t.Fatalf("within-budget resume changed conversation identity: %+v", continuation)
	}
}

func TestSQLiteGoalLateUsageAfterResumeCannotBypassBudgetAtDispatch(t *testing.T) {
	budget := int64(1_000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	paused, err := f.repository.PauseIssueAgentGoal(t.Context(), f.user.ID, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, work, nodeprotocol.RunEvent{Type: "run.finished", Payload: map[string]any{"status": "cancelled"}})
	if _, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: work.ID + "-late-after-resume", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 2,
		Type: "usage.updated", OccurredAt: time.Now().UTC(), Payload: map[string]any{"total_tokens": budget},
	}}); err != nil {
		t.Fatal(err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	settled := sqliteIsolationGoal(t, f, goal.ID)
	var runs, starts int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM runs WHERE goal_id = ?`, goal.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'`).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if settled.Status != domain.GoalBudgetReached || settled.TokensUsed != budget || settled.ContinuationsUsed != 0 || runs != 1 || starts != 1 {
		t.Fatalf("late usage bypassed pending resume boundary: goal=%+v runs=%d starts=%d", settled, runs, starts)
	}
}

func TestSQLiteGoalResumeBudgetGuardContinuesToExcludeAuditUsage(t *testing.T) {
	budget := int64(1_000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	applySQLiteGoalEvents(t, f.repository, work,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(100)}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, goal.ID)
	applySQLiteGoalEvents(t, f.repository, audit,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(50_000)}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": `{"verdict":"continue","note":"remaining"}`}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	current := sqliteIsolationGoal(t, f, goal.ID)
	paused, err := f.repository.PauseIssueAgentGoal(t.Context(), f.user.ID, current.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, paused.Version)
	if err != nil || resumed.TokensUsed != 100 {
		t.Fatalf("resume guard changed Issue audit accounting: %+v err=%v", resumed, err)
	}
	advanceSQLiteIsolationGoals(t, f, false)
	if run := sqliteIsolationCurrentRun(t, f, goal.ID); run.Purpose != domain.RunPurposeGoalContinuation {
		t.Fatalf("audit-excluded resume failed to queue work: %+v", run)
	}
}

func TestSQLiteGoalConcurrentExhaustedResumeIsReadOnly(t *testing.T) {
	budget := int64(1_000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	applySQLiteGoalEvents(t, f.repository, work,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": budget}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanceSQLiteIsolationGoals(t, f, false)
	settled := sqliteIsolationGoal(t, f, goal.ID)
	before := snapshotSQLiteGoalResumeState(t, f.repository)
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, settled.Version)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		sqliteGoalBudgetError(t, err, budget, budget)
	}
	if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
		t.Fatal("concurrent rejected resumes changed database rows")
	}
}
