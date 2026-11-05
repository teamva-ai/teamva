package dispatch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

func TestLocalDispatcherReleasesWorkDependenciesAfterCommittedPublishedResult(t *testing.T) {
	ctx := t.Context()
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
	runtime.onStart = func(ctx context.Context, payload nodeprotocol.StartRunPayload) error {
		if strings.Contains(payload.Prompt, "instruction-child") && !strings.Contains(payload.Prompt, "actual published parent result") {
			t.Error("child was dispatched without the published upstream result")
		}
		execution := caller.tools.Execute(ctx, computer.ID, nodeprotocol.NodeRequest{
			RunID: payload.RunID, Tool: "issue_comment_create", IdempotencyKey: "result",
			Payload: map[string]any{"body": "actual published parent result"},
		})
		if !execution.Result.OK {
			t.Errorf("tool result=%+v", execution.Result)
		}
		return nil
	}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()); _ = repository.Close() })
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "work dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateConfiguredAgent(ctx, user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: services[0].ID, Name: "worker", Status: domain.AgentActive}, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{ProjectID: project.ID, Title: "dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := repository.CreateIssueWorkComment(ctx, user.ID, domain.IssueComment{IssueID: issue.ID, AuthorKind: "user", Body: "confirmed"}, []nodeprotocol.WorkPlanItem{
		{Key: "parent", AgentID: agent.ID, Title: "parent", Instruction: "instruction-parent"},
		{Key: "child", AgentID: agent.ID, Title: "child", Instruction: "instruction-child", After: []string{"parent"}},
	}, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	local.NotifyIssueActivation(issue.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := repository.GetIssueComment(ctx, user.ID, comment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(view.WorkItems) == 2 && view.WorkItems[1].Status == domain.RunSucceeded {
			if view.WorkItems[0].ResultCommentID == "" || view.WorkItems[1].ResultCommentID == "" || view.WorkItems[0].IssueAgentInstanceID == view.WorkItems[1].IssueAgentInstanceID {
				t.Fatal("missing result binding or independent instance")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("periodic dispatcher did not release dependent work")
}
