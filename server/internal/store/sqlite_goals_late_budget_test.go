package store

import (
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteGoalLateWorkUsageStopsPostAuditEnqueue(t *testing.T) {
	for _, verdict := range []string{"continue", "complete"} {
		t.Run(verdict, func(t *testing.T) {
			budget := int64(1000)
			f, work, goal := createSQLiteGoalFixture(t, &budget)
			t.Cleanup(func() { _ = f.repository.Close() })
			applySQLiteGoalEvents(t, f.repository, work,
				nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(100)}},
				nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": "work done"}},
				nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
			)
			advanceSQLiteIsolationGoals(t, f, false)
			audit := sqliteIsolationCurrentRun(t, f, goal.ID)
			late := nodeprotocol.RunEvent{ID: work.ID + "-late-usage", RunID: work.ID, LeaseEpoch: work.LeaseEpoch,
				Sequence: 4, Type: "usage.updated", OccurredAt: time.Now().UTC(),
				Payload: map[string]any{"total_tokens": int64(900), "provider_id": "codex"}}
			if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{late}); err != nil {
				t.Fatal(err)
			}
			applySQLiteGoalEvents(t, f.repository, audit,
				nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": `{"verdict":"` + verdict + `","note":"verified"}`}},
				nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
			)
			advanceSQLiteIsolationGoals(t, f, false)
			current := sqliteIsolationGoal(t, f, goal.ID)
			if current.Status != domain.GoalBudgetReached || current.CurrentRunID != audit.ID ||
				current.TokensUsed != budget || current.ContinuationsUsed != 0 || current.FinalizeAttempts != 0 {
				t.Fatalf("late usage bypassed budget: %+v", current)
			}
		})
	}
}

func TestSQLiteGoalFinalizeRetryHonorsBudget(t *testing.T) {
	budget := int64(1000)
	f, work, goal := createSQLiteGoalFixture(t, &budget)
	t.Cleanup(func() { _ = f.repository.Close() })
	finishSQLiteIsolationRun(t, f, work, "work done")
	advanceSQLiteIsolationGoals(t, f, false)
	finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, goal.ID), `{"verdict":"complete","note":"verified"}`)
	advanceSQLiteIsolationGoals(t, f, false)
	finalize := sqliteIsolationCurrentRun(t, f, goal.ID)
	applySQLiteGoalEvents(t, f.repository, finalize,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": budget}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanceSQLiteIsolationGoals(t, f, false)
	current := sqliteIsolationGoal(t, f, goal.ID)
	if current.Status != domain.GoalBudgetReached || current.CurrentRunID != finalize.ID || current.FinalizeAttempts != 1 {
		t.Fatalf("finalize retry bypassed budget: %+v", current)
	}
}
