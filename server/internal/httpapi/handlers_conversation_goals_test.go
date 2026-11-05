package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestConversationGoalHTTPContract(t *testing.T) {
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{"models": []string{"test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: service.ID, Name: "goal agent", Model: "test", Status: "active",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "goal project"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := repo.CreateConversationGraph(t.Context(), user.ID,
		domain.ConversationGraph{ProjectID: project.ID, Title: "goal graph"}, domain.ConversationNode{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/conversation-nodes/" + graph.Nodes[0].ID
	request(t, router, http.MethodGet, base+"/goal", nil, auth, http.StatusOK)
	request(t, router, http.MethodPost, base+"/goals", map[string]any{"objective": "goal"}, auth, http.StatusBadRequest)
	auth["Idempotency-Key"] = "goal-http"
	request(t, router, http.MethodPost, base+"/goals", map[string]any{"objective": "goal", "token_budget": 1}, auth, http.StatusBadRequest)
	goal := decodeData[domain.ConversationGoal](t, request(t, router, http.MethodPost, base+"/goals",
		map[string]any{"objective": "goal", "token_budget": 10000}, auth, http.StatusAccepted))
	request(t, router, http.MethodPost, base+"/goals", map[string]any{"objective": "goal", "token_budget": 10000}, auth, http.StatusOK)
	request(t, router, http.MethodPost, base+"/goals", map[string]any{"objective": "different"}, auth, http.StatusConflict)
	path := base + "/goals/" + goal.ID
	request(t, router, http.MethodPost, path+"/invalid", map[string]any{"expected_version": goal.Version}, auth, http.StatusBadRequest)
	paused := decodeData[domain.ConversationGoal](t, request(t, router, http.MethodPost, path+"/pause",
		map[string]any{"expected_version": goal.Version}, auth, http.StatusOK))
	request(t, router, http.MethodPost, path+"/cancel", map[string]any{"expected_version": goal.Version}, auth, http.StatusConflict)
	request(t, router, http.MethodPost, path+"/cancel", map[string]any{"expected_version": paused.Version}, auth, http.StatusOK)
	view := decodeData[domain.ConversationGraphDetail](t, request(t, router, http.MethodGet,
		"/v1/conversation-graphs/"+graph.Graph.ID, nil, auth, http.StatusOK))
	if len(view.Goals) != 1 || view.Goals[0].Status != domain.GoalCancelled {
		t.Fatal("graph snapshot missing goal state")
	}
}
