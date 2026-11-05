package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func createSQLiteGoalFixture(
	t *testing.T, budget *int64,
) (sqliteExecutionFixture, domain.Run, domain.IssueAgentGoal) {
	t.Helper()
	f := newSQLiteExecutionFixture(t)
	run, goal, replayed, err := f.repository.CreateIssueAgentGoalRun(
		context.Background(), f.user.ID, f.issue.ID, f.instance.ID, f.issue.Version,
		"goal-create", "完成实现并验证测试", budget,
		domain.Run{
			IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
			Input: []domain.ContentBlock{{Type: "text", Text: "goal work"}},
		},
	)
	if err != nil || replayed {
		t.Fatalf("create goal: replayed=%t err=%v", replayed, err)
	}
	return f, run, goal
}

func applySQLiteGoalEvents(t *testing.T, repository *SQLite, run domain.Run, events ...nodeprotocol.RunEvent) {
	t.Helper()
	now := time.Now().UTC()
	for index := range events {
		if events[index].Payload == nil {
			events[index].Payload = map[string]any{}
		}
		if events[index].Payload["provider_id"] == nil {
			events[index].Payload["provider_id"] = "codex"
		}
		events[index].RunID = run.ID
		events[index].LeaseEpoch = run.LeaseEpoch
		events[index].Sequence = int64(index + 1)
		if events[index].ID == "" {
			events[index].ID = run.ID + "-event-" + string(rune('a'+index))
		}
		if events[index].OccurredAt.IsZero() {
			events[index].OccurredAt = now.Add(time.Duration(index) * time.Millisecond)
		}
	}
	if _, err := repository.ApplyLocalEvents(context.Background(), events); err != nil {
		t.Fatalf("apply goal events for %s: %v", run.ID, err)
	}
}

func TestSQLiteGoalAuditFinalizeLifecycle(t *testing.T) {
	budget := int64(200_000)
	f, workRun, created := createSQLiteGoalFixture(t, &budget)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()

	current, found, err := f.repository.GetCurrentIssueAgentGoal(ctx, f.user.ID, f.instance.ID)
	if err != nil || !found || current.ID != created.ID || workRun.GoalID != created.ID {
		t.Fatalf("current goal = %+v found=%t run=%+v err=%v", current, found, workRun, err)
	}
	replayedRun, replayedGoal, replayed, err := f.repository.CreateIssueAgentGoalRun(
		ctx, f.user.ID, f.issue.ID, f.instance.ID, 999, "goal-create", "ignored", nil, domain.Run{},
	)
	if err != nil || !replayed || replayedRun.ID != workRun.ID || replayedGoal.ID != created.ID {
		t.Fatalf("goal replay = run=%+v goal=%+v replayed=%t err=%v", replayedRun, replayedGoal, replayed, err)
	}

	applySQLiteGoalEvents(t, f.repository, workRun,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(1_250)}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": "实现完成，相关测试已通过。"}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	advanced, err := f.repository.AdvanceLocalGoals(ctx, time.Now().UTC())
	if err != nil || advanced != 1 {
		t.Fatalf("queue audit: advanced=%d err=%v", advanced, err)
	}
	current, _, err = f.repository.GetCurrentIssueAgentGoal(ctx, f.user.ID, f.instance.ID)
	if err != nil || current.Phase != domain.GoalPhaseEvaluating || current.TokensUsed != 1_250 {
		t.Fatalf("evaluating goal = %+v err=%v", current, err)
	}
	auditRun, err := f.repository.GetRun(ctx, f.user.ID, current.CurrentRunID)
	if err != nil || auditRun.Purpose != domain.RunPurposeGoalAudit || auditRun.IssueAgentInstanceID != "" {
		t.Fatalf("audit run = %+v err=%v", auditRun, err)
	}
	var auditPayload []byte
	if err := f.repository.db.QueryRowContext(ctx, `
		SELECT payload FROM command_outbox
		WHERE command_name = 'run.start' AND json_extract(payload, '$.run_id') = ?
	`, auditRun.ID).Scan(&auditPayload); err != nil {
		t.Fatal(err)
	}
	var start nodeprotocol.StartRunPayload
	if err := json.Unmarshal(auditPayload, &start); err != nil || !start.DisableTools || !start.ReadOnly {
		t.Fatalf("audit start payload = %+v err=%v", start, err)
	}

	applySQLiteGoalEvents(t, f.repository, auditRun,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{
			"text": `{"verdict":"complete","note":"实现和测试均已验证"}`,
		}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	if _, err := f.repository.AdvanceLocalGoals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current, _, _ = f.repository.GetCurrentIssueAgentGoal(ctx, f.user.ID, f.instance.ID)
	if current.Phase != domain.GoalPhaseFinalizing {
		t.Fatalf("goal phase = %q, want finalizing", current.Phase)
	}
	finalizeRun, err := f.repository.GetRun(ctx, f.user.ID, current.CurrentRunID)
	if err != nil || finalizeRun.Purpose != domain.RunPurposeGoalFinalize ||
		finalizeRun.IssueAgentGenerationID != workRun.IssueAgentGenerationID {
		t.Fatalf("finalize run = %+v err=%v", finalizeRun, err)
	}
	if _, err := f.repository.db.ExecContext(ctx, `
		UPDATE issue_comments SET body = '最终总结', status = 'published', version = version + 1
		WHERE id = ?
	`, current.SummaryCommentID); err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, finalizeRun,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	if _, err := f.repository.AdvanceLocalGoals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current, _, err = f.repository.GetCurrentIssueAgentGoal(ctx, f.user.ID, f.instance.ID)
	if err != nil || current.Status != domain.GoalCompleted || current.StatusReason != "verified and summarized" {
		t.Fatalf("completed goal = %+v err=%v", current, err)
	}
	instance, err := f.repository.GetIssueAgentInstanceByID(ctx, f.user.ID, f.instance.ID)
	if err != nil || instance.Status != domain.AgentInstanceIdle {
		t.Fatalf("completed instance = %+v err=%v", instance, err)
	}
}

func TestSQLiteGoalBudgetStopsBeforeAudit(t *testing.T) {
	budget := int64(1_000)
	f, workRun, _ := createSQLiteGoalFixture(t, &budget)
	defer func() { _ = f.repository.Close() }()
	applySQLiteGoalEvents(t, f.repository, workRun,
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(1_200)}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	if _, err := f.repository.AdvanceLocalGoals(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	goal, _, err := f.repository.GetCurrentIssueAgentGoal(context.Background(), f.user.ID, f.instance.ID)
	if err != nil || goal.Status != domain.GoalBudgetReached || goal.TokensUsed != 1_200 {
		t.Fatalf("budget goal = %+v err=%v", goal, err)
	}
	var auditRuns int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs WHERE goal_id = ? AND purpose = 'goal_audit'`, goal.ID).Scan(&auditRuns); err != nil {
		t.Fatal(err)
	}
	if auditRuns != 0 {
		t.Fatalf("created %d audit runs after budget was reached", auditRuns)
	}
}

func TestSQLiteGoalPauseStopsAndResumesAfterTerminalRun(t *testing.T) {
	f, workRun, goal := createSQLiteGoalFixture(t, nil)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()
	paused, err := f.repository.PauseIssueAgentGoal(ctx, f.user.ID, goal.ID, goal.Version)
	if err != nil || paused.Status != domain.GoalPaused {
		t.Fatalf("pause goal = %+v err=%v", paused, err)
	}
	stopping, err := f.repository.GetRun(ctx, f.user.ID, workRun.ID)
	if err != nil || stopping.Status != domain.RunCancelling {
		t.Fatalf("stopping run = %+v err=%v", stopping, err)
	}
	if _, err := f.repository.ResumeIssueAgentGoal(ctx, f.user.ID, goal.ID, paused.Version); err == nil {
		t.Fatal("resumed while current run was still stopping")
	}
	applySQLiteGoalEvents(t, f.repository, stopping,
		nodeprotocol.RunEvent{Type: "run.finished", Payload: map[string]any{"status": "cancelled"}},
	)
	paused, err = f.repository.GetIssueAgentGoal(ctx, f.user.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := f.repository.ResumeIssueAgentGoal(ctx, f.user.ID, goal.ID, paused.Version)
	if err != nil || resumed.Status != domain.GoalActive || resumed.Phase != domain.GoalPhaseDispatching {
		t.Fatalf("resume goal = %+v err=%v", resumed, err)
	}
	if _, err := f.repository.AdvanceLocalGoals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resumed, _, _ = f.repository.GetCurrentIssueAgentGoal(ctx, f.user.ID, f.instance.ID)
	continuation, err := f.repository.GetRun(ctx, f.user.ID, resumed.CurrentRunID)
	if err != nil || continuation.Purpose != domain.RunPurposeGoalContinuation || resumed.ContinuationsUsed != 1 {
		t.Fatalf("continuation = %+v goal=%+v err=%v", continuation, resumed, err)
	}
}

func TestSQLiteGoalRejectsParallelOrdinaryRun(t *testing.T) {
	f, _, goal := createSQLiteGoalFixture(t, nil)
	defer func() { _ = f.repository.Close() }()

	_, _, err := f.repository.CreateRun(
		context.Background(), f.user.ID, f.issue.ID, f.issue.Version, "parallel-ordinary-run",
		domain.Run{
			IssueAgentInstanceID: f.instance.ID,
			AgentID:              f.agent.ID,
			Input:                []domain.ContentBlock{{Type: "text", Text: "parallel work"}},
		},
	)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "goal_already_exists" {
		t.Fatalf("parallel ordinary run error = %v, want goal_already_exists", err)
	}
	if domainErr.Details["goal_id"] != goal.ID {
		t.Fatalf("parallel ordinary run goal_id = %v, want %s", domainErr.Details["goal_id"], goal.ID)
	}
}

func TestParseGoalAudit(t *testing.T) {
	verdict, note, ok := parseGoalAudit("```json\n{\"verdict\":\"continue\",\"note\":\"还需验证\"}\n```")
	if !ok || verdict != "continue" || note != "还需验证" {
		t.Fatalf("audit = %q %q %t", verdict, note, ok)
	}
	if _, _, ok := parseGoalAudit(`{"verdict":"maybe","note":"no"}`); ok {
		t.Fatal("invalid verdict accepted")
	}
	verdict, note, ok = parseGoalAudit(`I first considered {"verdict":"continue","note":"draft"}.
The final decision is {"verdict":"complete","note":"全部证据已确认"}`)
	if !ok || verdict != "complete" || note != "全部证据已确认" {
		t.Fatalf("audit with analysis = %q %q %t", verdict, note, ok)
	}
	verdict, note, ok = parseGoalAudit(`Analysis mentioned the schema {"verdict":"continue|complete|blocked"}.
Final answer: {"verdict":"blocked","note":"等待唯一外部依赖"}`)
	if !ok || verdict != "blocked" || note != "等待唯一外部依赖" {
		t.Fatalf("audit after schema prose = %q %q %t", verdict, note, ok)
	}
}
