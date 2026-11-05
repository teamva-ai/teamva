package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestConversationEmptyGraphRootAgentAndTitleHTTPContract(t *testing.T) {
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
	var agents []domain.Agent
	for _, name := range []string{"first", "second"} {
		agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: name, Model: "test", Status: domain.AgentActive}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, agent)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "empty graph"})
	if err != nil {
		t.Fatal(err)
	}
	graph := decodeData[domain.ConversationGraphDetail](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/conversation-graphs", map[string]any{}, auth, http.StatusCreated))
	if graph.Graph.Title != "新对话" || graph.Nodes == nil || len(graph.Nodes) != 0 || graph.Messages == nil || graph.Goals == nil {
		t.Fatalf("empty graph=%+v", graph)
	}
	base := "/v1/conversation-graphs/" + graph.Graph.ID
	request(t, router, http.MethodPost, base+"/root", map[string]any{"agent_id": agents[0].ID}, auth, http.StatusBadRequest)
	root := decodeData[domain.ConversationNode](t, request(t, router, http.MethodPost, base+"/root", map[string]any{"agent_id": agents[0].ID, "expected_version": graph.Graph.Version}, auth, http.StatusOK))
	replayed := decodeData[domain.ConversationNode](t, request(t, router, http.MethodPost, base+"/root", map[string]any{"agent_id": agents[0].ID, "expected_version": graph.Graph.Version}, auth, http.StatusOK))
	rooted := decodeData[domain.ConversationGraphDetail](t, request(t, router, http.MethodGet, base, nil, auth, http.StatusOK))
	if len(rooted.Nodes) != 1 || root.Title != "新节点" || replayed.ID != root.ID || replayed.Version != root.Version {
		t.Fatal("root was not idempotent")
	}
	request(t, router, http.MethodPost, base+"/root", map[string]any{"agent_id": agents[1].ID, "expected_version": rooted.Graph.Version}, auth, http.StatusConflict)
	updatedGraph := decodeData[domain.ConversationGraph](t, request(t, router, http.MethodPatch, base, map[string]any{"title": "图的新名字", "expected_version": rooted.Graph.Version}, auth, http.StatusOK))
	if updatedGraph.Title != "图的新名字" || updatedGraph.Version != rooted.Graph.Version+1 {
		t.Fatal("graph title patch failed")
	}
	request(t, router, http.MethodPatch, base, map[string]any{"title": "stale", "expected_version": rooted.Graph.Version}, auth, http.StatusConflict)
	node := rooted.Nodes[0]
	nodeBase := "/v1/conversation-nodes/" + node.ID
	node = decodeData[domain.ConversationNode](t, request(t, router, http.MethodPatch, nodeBase, map[string]any{"agent_id": agents[1].ID, "expected_version": node.Version}, auth, http.StatusOK))
	if node.AgentID != agents[1].ID || node.AgentRevisionID != agents[1].CurrentRevisionID {
		t.Fatal("node binding was not updated atomically")
	}
	noop := decodeData[domain.ConversationNode](t, request(t, router, http.MethodPatch, nodeBase, map[string]any{"agent_id": agents[1].ID, "expected_version": node.Version}, auth, http.StatusOK))
	if noop.Version != node.Version {
		t.Fatal("same-agent PATCH was not no-op")
	}
	auth["Idempotency-Key"] = "http-bound"
	request(t, router, http.MethodPost, nodeBase+"/messages", map[string]any{"body": "stale", "expected_agent_id": agents[0].ID, "expected_agent_revision_id": rooted.Nodes[0].AgentRevisionID}, auth, http.StatusConflict)
	request(t, router, http.MethodPost, nodeBase+"/goals", map[string]any{"objective": "stale", "expected_agent_id": agents[0].ID, "expected_agent_revision_id": rooted.Nodes[0].AgentRevisionID}, auth, http.StatusConflict)
	message := decodeData[domain.ConversationMessage](t, request(t, router, http.MethodPost, nodeBase+"/messages", map[string]any{"body": "bound request", "expected_agent_id": node.AgentID, "expected_agent_revision_id": node.AgentRevisionID}, auth, http.StatusCreated))
	if message.RunID == "" {
		t.Fatal("missing Run")
	}
	request(t, router, http.MethodPost, nodeBase+"/messages", map[string]any{"body": "bound request", "expected_agent_id": node.AgentID, "expected_agent_revision_id": node.AgentRevisionID}, auth, http.StatusCreated)
	request(t, router, http.MethodPost, nodeBase+"/messages", map[string]any{"body": "bound request"}, auth, http.StatusConflict)
	current, err := repo.GetConversationNode(t.Context(), user.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, nodeBase, map[string]any{"agent_id": agents[0].ID, "title": "not committed", "expected_version": current.Version}, auth, http.StatusConflict)
	current = decodeData[domain.ConversationNode](t, request(t, router, http.MethodPatch, nodeBase, map[string]any{"title": "运行中可重命名", "expected_version": current.Version}, auth, http.StatusOK))
	if current.Title != "运行中可重命名" || current.AgentID != agents[1].ID {
		t.Fatal("title/binding contract failed")
	}
}
