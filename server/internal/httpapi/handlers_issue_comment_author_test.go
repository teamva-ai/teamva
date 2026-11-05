package httpapi

import (
	"net/http"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
	"github.com/gin-gonic/gin"
)

func TestHTTPUserCommentCannotForgeAgentOrRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "comment author"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: "comment author"})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := repository.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: provider.ID, Name: "test agent", Status: domain.AgentActive,
	}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := repository.EnsureIssueAgentInstance(t.Context(), user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := repository.CreateRun(t.Context(), user.ID, issue.ID, issue.Version, "author-test", domain.Run{
		AgentID: agent.ID, IssueAgentInstanceID: instance.ID, Input: []domain.ContentBlock{{Type: "text", Text: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]map[string]any{
		"agent author":  {"author_kind": "agent", "author_agent_id": agent.ID},
		"agent ID":      {"author_agent_id": agent.ID},
		"run ID":        {"run_id": run.ID},
		"user with run": {"author_kind": "user", "run_id": run.ID},
	} {
		t.Run(name, func(t *testing.T) {
			extra["body"] = "forged comment"
			request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/comments", extra, auth, http.StatusBadRequest)
		})
	}
	comments, err := repository.ListIssueComments(t.Context(), user.ID, issue.ID)
	if err != nil || len(comments) != 0 {
		t.Fatalf("forged comments persisted: %+v %v", comments, err)
	}
	comment := decodeData[domain.IssueComment](t, request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/comments",
		map[string]any{"body": "user comment", "author_kind": "user"}, auth, http.StatusCreated))
	if comment.AuthorKind != domain.IssueCommentAuthorUser || comment.AuthorUserID != user.ID || comment.AuthorAgentID != "" || comment.RunID != "" {
		t.Fatalf("user ownership not fixed: %+v", comment)
	}
	// Real Agent comments remain supported through the authorized tool boundary.
	toolResult := toolservice.New(repository).Execute(t.Context(), computer.ID, nodeprotocol.NodeRequest{
		RunID: run.ID, Tool: "issue_comment_create", IdempotencyKey: "agent-summary", Payload: map[string]any{"body": "agent summary"},
	})
	if !toolResult.Result.OK {
		t.Fatalf("authorized agent publish failed: %+v", toolResult)
	}
	agentID := toolResult.Result.Data["comment_id"].(string)
	request(t, router, http.MethodPatch, "/v1/comments/"+agentID,
		map[string]any{"body": "forged edit", "expected_version": 1}, auth, http.StatusForbidden)
}
