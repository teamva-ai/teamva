package store

import (
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// A successful native run is not a successful audit: a model can emit its tool
// dialect as plain assistant text even though NoTools exposes no executable
// tool. Keep that output observable and never invent a verdict or a tool result.
func TestSQLiteGoalAuditPlainDSMLRemainsObservableFormatFailure(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	const dsml = `<｜｜DSML｜｜ calls>
<｜｜DSML｜｜ invoke name="Bash">
<｜｜DSML｜｜ parameter name="command" string="true">cat stage-one.txt</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
</｜｜DSML｜｜ calls>`
	if _, _, ok := parseGoalAudit(dsml); ok {
		t.Fatal("plain DSML was accepted as an independent audit verdict")
	}

	for round := 0; round < 2; round++ {
		// Even a work result reporting real artifact verification cannot replace
		// the independent verdict and explicit final-summary round.
		applySQLiteGoalEvents(t, f.repository, work,
			nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": "阶段产物验证通过，退出码 0。"}},
			nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
		)
		advanceSQLiteIsolationGoals(t, f, false)
		audit := sqliteIsolationCurrentRun(t, f, goal.ID)
		if audit.Purpose != domain.RunPurposeGoalAudit || audit.IssueAgentInstanceID != "" || !audit.ReadOnly {
			t.Fatalf("audit lost its separate read-only scope: %+v", audit)
		}
		applySQLiteGoalEvents(t, f.repository, audit,
			nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": dsml[:40]}},
			nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": dsml[40:]}},
			nodeprotocol.RunEvent{Type: "provider.event", Payload: map[string]any{
				"kind": "text", "text": dsml,
			}},
			nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
		)
		advanceSQLiteIsolationGoals(t, f, false)
		current := sqliteIsolationGoal(t, f, goal.ID)
		if current.AuditFailureStreak != round+1 || current.FinalizeAttempts != 0 || current.Status == domain.GoalCompleted {
			t.Fatalf("malformed audit became verified completion: %+v", current)
		}
		persistedAudit, err := f.repository.GetRun(t.Context(), f.user.ID, audit.ID)
		if err != nil || persistedAudit.Status != domain.RunSucceeded {
			t.Fatalf("native success was rewritten instead of preserving audit format failure: run=%+v err=%v", persistedAudit, err)
		}
		var raw string
		if err := f.repository.db.QueryRowContext(t.Context(), `
			SELECT json_extract(payload, '$.text') FROM run_events
			WHERE run_id = ? AND event_type = 'provider.event'
			  AND json_extract(payload, '$.kind') = 'text'
		`, audit.ID).Scan(&raw); err != nil || raw != dsml {
			t.Fatalf("original malformed public output was lost: text=%q err=%v", raw, err)
		}
		if round == 0 {
			if current.Status != domain.GoalActive || current.Phase != domain.GoalPhaseRunning || current.ContinuationsUsed != 1 || !strings.Contains(current.Note, "审核格式无效") {
				t.Fatalf("first format failure did not remain visible during bounded retry: %+v", current)
			}
			work = sqliteIsolationCurrentRun(t, f, goal.ID)
			if work.Purpose != domain.RunPurposeGoalContinuation {
				t.Fatalf("first malformed audit did not queue normal work continuation: %+v", work)
			}
		} else if current.Status != domain.GoalBlocked || current.StatusReason != "progress audit returned invalid output" || current.ContinuationsUsed != 1 {
			t.Fatalf("repeated malformed audit did not fail closed: %+v", current)
		}
	}
	var finalizeRuns int
	if err := f.repository.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM runs WHERE goal_id = ? AND purpose = 'goal_finalize'
	`, goal.ID).Scan(&finalizeRuns); err != nil || finalizeRuns != 0 {
		t.Fatalf("invalid audit created a final-summary run: count=%d err=%v", finalizeRuns, err)
	}
}
