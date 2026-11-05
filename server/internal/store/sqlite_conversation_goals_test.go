package store

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func conversationGoalFixture(t *testing.T, budget *int64) (sqliteExecutionFixture, domain.ConversationGraphDetail, domain.ConversationGoal) {
	t.Helper()
	f, graph := createConversationFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	goal, replayed, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID,
		"goal-1", "完成节点目标并报告验证结果", budget)
	if err != nil || replayed {
		t.Fatalf("create goal: %+v %v", goal, err)
	}
	return f, graph, goal
}

func completeConversationGoalRun(t *testing.T, f sqliteExecutionFixture, goal domain.ConversationGoal, text string, tokens int64) {
	t.Helper()
	run, err := f.repository.GetRun(t.Context(), f.user.ID, goal.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": tokens}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": text}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
}

func advanceConversationGoalTest(t *testing.T, f sqliteExecutionFixture, nodeID string) domain.ConversationGoal {
	t.Helper()
	if _, err := f.repository.AdvanceConversationGoals(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	goal, err := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, nodeID)
	if err != nil || goal == nil {
		t.Fatalf("get goal: %v", err)
	}
	return *goal
}

func TestConversationGoalWorkAuditContinuationLifecycle(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	nodeID := graph.Nodes[0].ID
	replay, hit, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, nodeID, "goal-1", goal.Objective, nil)
	if err != nil || !hit || replay.ID != goal.ID {
		t.Fatalf("replay: %v %v", hit, err)
	}
	if _, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, nodeID, "goal-1", "different", nil); err == nil {
		t.Fatal("changed replay accepted")
	}
	if _, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, nodeID, "ordinary", "hello"); err == nil {
		t.Fatal("ordinary send bypassed goal")
	}
	if _, err := f.repository.GetCurrentConversationGoal(t.Context(), "another-user", nodeID); err == nil {
		t.Fatal("cross-owner goal read")
	}

	completeConversationGoalRun(t, f, goal, "第一步完成，仍需验证第二步。", 100)
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, nodeID, messages[len(messages)-1].ID,
		domain.ConversationNode{AgentID: f.agent.ID}); err == nil {
		t.Fatal("fork bypassed unfinished goal")
	}
	// Goal reserves the node in the terminal -> audit gap, not merely while busy.
	if _, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, nodeID, "gap", "hello"); err == nil {
		t.Fatal("goal slot released between runs")
	}
	audit := advanceConversationGoalTest(t, f, nodeID)
	if audit.Phase != domain.GoalPhaseEvaluating || audit.TokensUsed != 100 {
		t.Fatalf("audit: %+v", audit)
	}
	var raw []byte
	if err := f.repository.db.QueryRow(`SELECT payload FROM command_outbox WHERE command_name='run.start'
	 AND json_extract(payload,'$.run_id')=?`, audit.CurrentRunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ReadOnly || !payload.DisableTools || payload.IssueID != "" || payload.IssueAgentInstanceID != "" || payload.ConversationNodeID != nodeID || payload.SystemPrompt != goalAuditSystemPrompt {
		t.Fatal("audit lost isolation/read-only/no-tools contract")
	}
	completeConversationGoalRun(t, f, audit, `{"verdict":"continue","note":"还需第二步证据"}`, 20)
	continued := advanceConversationGoalTest(t, f, nodeID)
	if continued.WorkRuns != 2 || continued.Phase != domain.GoalPhaseRunning || continued.TokensUsed != 120 {
		t.Fatalf("continued: %+v", continued)
	}
	// A restart/tick replay cannot enqueue a duplicate continuation.
	if _, err := f.repository.AdvanceConversationGoals(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	again, _ := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, nodeID)
	if again.CurrentRunID != continued.CurrentRunID || again.RunCount != 3 {
		t.Fatal("duplicate continuation")
	}
	completeConversationGoalRun(t, f, continued, "第二步与全部目标已验证，结果如下。", 200)
	audit = advanceConversationGoalTest(t, f, nodeID)
	completeConversationGoalRun(t, f, audit, `{"verdict":"complete","note":"两步都有证据"}`, 30)
	finished := advanceConversationGoalTest(t, f, nodeID)
	if finished.Status != domain.GoalCompleted || finished.TokensUsed != 350 || finished.WorkRuns != 2 {
		t.Fatalf("finished: %+v", finished)
	}
	view, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Goals) != 1 || len(view.Messages) != 3 {
		t.Fatalf("public snapshot: goals=%d messages=%d", len(view.Goals), len(view.Messages))
	}
	for _, message := range view.Messages {
		if strings.Contains(message.Body, "verdict") {
			t.Fatal("audit JSON leaked into node messages")
		}
	}
	var issueRuns int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs r JOIN conversation_goal_runs gr ON gr.run_id=r.id
	 WHERE gr.goal_id=? AND (r.issue_id IS NOT NULL OR r.goal_id IS NOT NULL)`, goal.ID).Scan(&issueRuns); err != nil {
		t.Fatal(err)
	}
	if issueRuns != 0 {
		t.Fatal("graph goal borrowed Issue scope")
	}
}

func TestConversationGoalPauseResumeCancelAndLateResults(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	nodeID := graph.Nodes[0].ID
	paused, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, nodeID, goal.ID, goal.Version, "pause")
	if err != nil || paused.Status != domain.GoalPaused {
		t.Fatalf("pause: %v", err)
	}
	if _, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, nodeID, goal.ID, paused.Version, "resume"); err == nil {
		t.Fatal("resumed while stopping")
	}
	if _, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, nodeID, goal.ID, goal.Version, "cancel"); err == nil {
		t.Fatal("stale version accepted")
	}
	run, _ := f.repository.GetRun(t.Context(), f.user.ID, goal.CurrentRunID)
	applySQLiteGoalEvents(t, f.repository, run, nodeprotocol.RunEvent{Type: "run.finished", Payload: map[string]any{"status": "cancelled"}})
	paused = advanceConversationGoalTest(t, f, nodeID)
	if paused.RunCount != 1 {
		t.Fatal("paused goal continued")
	}
	if err := f.repository.DeleteConversationGraph(t.Context(), f.user.ID, graph.Graph.ID, graph.Graph.Version); err == nil {
		t.Fatal("deleted graph with unfinished goal")
	}
	resumed, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, nodeID, goal.ID, paused.Version, "resume")
	if err != nil {
		t.Fatal(err)
	}
	resumed = advanceConversationGoalTest(t, f, nodeID)
	if resumed.Status != domain.GoalActive || resumed.WorkRuns != 2 || resumed.CurrentRunID == goal.CurrentRunID {
		t.Fatalf("resume failed: %+v", resumed)
	}
	cancelled, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, nodeID, goal.ID, resumed.Version, "cancel")
	if err != nil || cancelled.Status != domain.GoalCancelled {
		t.Fatal(err)
	}
	// A success already in flight cannot revive a cancelled goal or spawn audits.
	completeConversationGoalRun(t, f, resumed, "late answer", 10)
	final := advanceConversationGoalTest(t, f, nodeID)
	if final.Status != domain.GoalCancelled || final.RunCount != 2 || final.TokensUsed != 10 {
		t.Fatalf("cancelled goal revived or lost usage: %+v", final)
	}
}

func TestConversationGoalBudgetAndAuditFailure(t *testing.T) {
	for _, auditText := range []string{"budget", "not JSON", `{"verdict":"blocked","note":"需要用户输入"}`} {
		t.Run(auditText, func(t *testing.T) {
			budget := int64(1000)
			f, graph, goal := conversationGoalFixture(t, &budget)
			tokens := int64(100)
			if auditText == "budget" {
				tokens = 1001
			}
			completeConversationGoalRun(t, f, goal, "progress", tokens)
			next := advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
			if auditText == "budget" {
				if next.Status != domain.GoalBudgetReached || next.RunCount != 1 {
					t.Fatalf("budget failed: %+v", next)
				}
				return
			}
			completeConversationGoalRun(t, f, next, auditText, 20)
			next = advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
			if next.Status != domain.GoalBlocked || next.RunCount != 2 {
				t.Fatalf("invalid/blocked audit continued: %+v", next)
			}
		})
	}
}

func TestConversationGoalConcurrentCreationAndRollback(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID, "same-key", "objective", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM conversation_goals`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("goals=%d %v", count, err)
	}

	other, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID,
		domain.ConversationGraph{ProjectID: f.project.ID, Title: "other"}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.db.Exec(`UPDATE agent_services SET status='unavailable'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, other.Nodes[0].ID, "failed-key", "objective", nil); err == nil {
		t.Fatal("unavailable provider accepted")
	}
	goal, err := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, other.Nodes[0].ID)
	if err != nil || goal != nil {
		t.Fatalf("failed creation left goal: %+v %v", goal, err)
	}
}

func TestConversationGoalResumesDurablyAfterRepositoryReopen(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	completeConversationGoalRun(t, f, goal, "持久化的工作结果", 100)
	var seq int
	var name, path string
	if err := f.repository.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.repository = reopened
	goal = advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
	if goal.Phase != domain.GoalPhaseEvaluating || goal.RunCount != 2 {
		t.Fatalf("not recovered: %+v", goal)
	}
}

func TestConversationGoalStopsRunningWorkAtReportedBudget(t *testing.T) {
	budget := int64(1000)
	f, graph, goal := conversationGoalFixture(t, &budget)
	run, err := f.repository.GetRun(t.Context(), f.user.ID, goal.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(1100)}},
	)
	goal = advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
	if goal.Status != domain.GoalBudgetReached {
		t.Fatalf("budget not enforced: %+v", goal)
	}
	run, err = f.repository.GetRun(t.Context(), f.user.ID, run.ID)
	if err != nil || run.Status != domain.RunCancelling {
		t.Fatalf("run was not stopped: %s %v", run.Status, err)
	}
}

func TestConversationGoalsAreIndependentAndRespectRoundLimit(t *testing.T) {
	f, graph, first := conversationGoalFixture(t, nil)
	other, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID,
		domain.ConversationGraph{ProjectID: f.project.ID, Title: "parallel"}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, other.Nodes[0].ID, "same-key", "independent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, other.Nodes[0].ID, first.ID, first.Version, "cancel"); err == nil {
		t.Fatal("cross-node cancellation allowed")
	}
	paused, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, first.NodeID, first.ID, first.Version, "pause")
	if err != nil || paused.Status != domain.GoalPaused {
		t.Fatal(err)
	}
	if _, err := f.repository.db.Exec(`UPDATE conversation_goals SET max_work_runs=1 WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	completeConversationGoalRun(t, f, second, "progress", 1)
	second = advanceConversationGoalTest(t, f, other.Nodes[0].ID)
	completeConversationGoalRun(t, f, second, `{"verdict":"continue","note":"unfinished"}`, 1)
	second = advanceConversationGoalTest(t, f, other.Nodes[0].ID)
	if second.Status != domain.GoalBlocked || second.StatusReason != "work_limit_reached" {
		t.Fatalf("limit: %+v", second)
	}
	unchanged, err := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil || unchanged.Status != domain.GoalPaused || unchanged.CurrentRunID != first.CurrentRunID {
		t.Fatalf("independent goal changed: %+v %v", unchanged, err)
	}
}

func TestConversationGoalOutboxFailureRollsBackEverything(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	if _, err := f.repository.db.Exec(`CREATE TRIGGER reject_goal_outbox BEFORE INSERT ON command_outbox
	 WHEN NEW.command_name='run.start' BEGIN SELECT RAISE(ABORT,'test outbox unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID, "outbox-fail", "objective", nil); err == nil {
		t.Fatal("outbox failure ignored")
	}
	var count int
	for _, query := range []string{
		`SELECT count(*) FROM conversation_goals`, `SELECT count(*) FROM conversation_goal_runs`,
		`SELECT count(*) FROM conversation_messages`, `SELECT count(*) FROM runs WHERE conversation_node_id IS NOT NULL`,
	} {
		if err := f.repository.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial state: %s => %d %v", query, count, err)
		}
	}
	node, err := f.repository.GetConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil || node.Status != domain.ConversationNodeIdle {
		t.Fatal("failed creation left node running")
	}
}

func TestConversationGoalConcurrentAdvanceEnqueuesOneAudit(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	completeConversationGoalRun(t, f, goal, "progress", 1)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repository.AdvanceConversationGoals(t.Context(), time.Now().UTC())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil || current.RunCount != 2 || current.Phase != domain.GoalPhaseEvaluating {
		t.Fatalf("duplicate audit: %+v %v", current, err)
	}
}

func TestConversationGoalOpenCodeAuditUsesOnlyTheFinalTextPart(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	completeConversationGoalRun(t, f, goal, "目标已完成，结果经过验证。", 100)
	audit := advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
	run, err := f.repository.GetRun(t.Context(), f.user.ID, audit.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "part": map[string]any{"id": "reason", "type": "reasoning", "text": ""}}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "reason", "text": "Need to review evidence before deciding."}},
		nodeprotocol.RunEvent{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "part": map[string]any{"id": "answer", "type": "text", "text": ""}}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "answer", "text": `{"verdict":"complete","note":"目标已验证"}`}},
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	goal = advanceConversationGoalTest(t, f, graph.Nodes[0].ID)
	if goal.Status != domain.GoalCompleted {
		t.Fatalf("reasoning corrupted audit JSON: %+v", goal)
	}
}
