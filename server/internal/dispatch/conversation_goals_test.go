package dispatch

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

// No model or browser: drive the real dispatcher + SQLite/outbox with a
// deterministic runtime and verify a complete work/audit/continue/audit loop.
func TestDispatcherCompletesConversationGoalWithoutIssue(t *testing.T) {
	ctx := t.Context()
	repo, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repo), computer: computer}
	runtime := &fakeRuntime{caller: caller}
	local, err := newLocalWithController(ctx, repo, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()); _ = repo.Close() })
	services, err := waitForAgentServices(t, repo, user.ID, computer.ID)
	if err != nil || len(services) == 0 {
		t.Fatal("missing fake service", err)
	}
	project, err := repo.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "graph goals"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repo.CreateConfiguredAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: services[0].ID, Name: "graph worker", Model: "deepseek/test", Status: "active",
	}, "report progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := repo.CreateConversationGraph(ctx, user.ID,
		domain.ConversationGraph{ProjectID: project.ID, Title: "goal loop"}, domain.ConversationNode{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	var audits atomic.Int32
	runtime.eventsForStart = func(runCtx context.Context, payload nodeprotocol.StartRunPayload) ([]nodeprotocol.RunEvent, error) {
		if payload.IssueID != "" || payload.IssueAgentInstanceID != "" || payload.ConversationNodeID != graph.Nodes[0].ID || !payload.DisableTools || !payload.ReadOnly {
			return nil, fmt.Errorf("conversation goal lost node-only permissions")
		}
		run, err := repo.GetRun(runCtx, user.ID, payload.RunID)
		if err != nil {
			return nil, err
		}
		text := "工作进展与验证结果"
		if run.Purpose == domain.RunPurposeGoalAudit {
			text = `{"verdict":"continue","note":"还需要验证"}`
			if audits.Add(1) > 1 {
				text = `{"verdict":"complete","note":"验证完成"}`
			}
		}
		return []nodeprotocol.RunEvent{
			{Type: "run.started"},
			{Type: "usage.updated", Payload: map[string]any{"total_tokens": int64(100)}},
			{Type: "output.delta", Payload: map[string]any{"text": text}},
			{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
		}, nil
	}
	_, _, err = repo.CreateConversationGoal(ctx, user.ID, graph.Nodes[0].ID, "dispatcher-goal", "完成验证", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("conversation goal did not complete")
		case <-ticker.C:
			goal, err := repo.GetCurrentConversationGoal(ctx, user.ID, graph.Nodes[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if goal.Status == domain.GoalCompleted {
				if goal.WorkRuns != 2 || goal.RunCount != 4 || goal.TokensUsed != 400 {
					t.Fatalf("wrong counters: %+v", goal)
				}
				issues, err := repo.ListIssues(ctx, user.ID, project.ID)
				if err != nil || len(issues) != 0 {
					t.Fatal("goal created hidden Issue")
				}
				return
			}
			if goal.Status != domain.GoalActive {
				t.Fatalf("goal blocked: %+v", goal)
			}
		}
	}
}
