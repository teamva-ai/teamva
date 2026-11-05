package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func conversationForkHTTPFixture(t *testing.T) (*testRepository, *gin.Engine, map[string]string, domain.ConversationGraphDetail, domain.User) {
	t.Helper()
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
		ComputerID: computer.ID, AgentServiceID: service.ID, Name: "fork agent", Model: "test", Status: "active",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "fork project"})
	if err != nil {
		t.Fatal(err)
	}
	graph := decodeData[domain.ConversationGraphDetail](t, request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/conversation-graphs", map[string]any{"title": "fork graph", "agent_id": agent.ID}, auth, http.StatusCreated))
	return repo, router, auth, graph, user
}

// Synthetic events exercise persisted HTTP/store contracts without starting a
// dispatcher, service or real Provider.
func finishConversationForkHTTP(t *testing.T, repo *testRepository, router *gin.Engine, auth map[string]string, userID, nodeID, key, question, answer, status string) {
	t.Helper()
	auth["Idempotency-Key"] = key
	message := decodeData[domain.ConversationMessage](t, request(t, router, http.MethodPost,
		"/v1/conversation-nodes/"+nodeID+"/messages", map[string]any{"body": question}, auth, http.StatusCreated))
	run, err := repo.GetRun(t.Context(), userID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	events := []nodeprotocol.RunEvent{
		{Type: "run.started"},
		{Type: "output.delta", Payload: map[string]any{"text": answer}},
		{Type: "run.finished", Payload: map[string]any{"status": status}},
	}
	for i := range events {
		if events[i].Payload == nil {
			events[i].Payload = map[string]any{}
		}
		events[i].Payload["provider_id"] = "codex"
		events[i].ID, events[i].RunID = fmt.Sprintf("%s-%d", run.ID, i), run.ID
		events[i].LeaseEpoch, events[i].Sequence, events[i].OccurredAt = run.LeaseEpoch, int64(i+1), time.Now().UTC()
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
}

func TestConversationForkHTTPKeepsFullWritableContinuationAndPrefixBranch(t *testing.T) {
	repo, router, auth, graph, user := conversationForkHTTPFixture(t)
	root := graph.Nodes[0]
	base := "/v1/conversation-nodes/"
	request(t, router, http.MethodPatch, base+root.ID, map[string]any{
		"expected_version": root.Version, "width": 847, "height": 1139,
	}, auth, http.StatusOK)
	for _, turn := range [][4]string{
		{"prefix", "prefix-question", "prefix-answer", "completed"},
		{"tail", "tail-question", "tail-answer", "completed"},
		{"failed", "failed-question", "failed-answer", "failed"},
	} {
		finishConversationForkHTTP(t, repo, router, auth, user.ID, root.ID, turn[0], turn[1], turn[2], turn[3])
	}
	before := decodeData[domain.Page[domain.ConversationMessage]](t, request(t, router, http.MethodGet, base+root.ID+"/messages", nil, auth, http.StatusOK)).Items
	if len(before) != 6 || before[1].Status != domain.ConversationMessagePublished || before[1].Body != "prefix-answer" {
		t.Fatalf("synthetic HTTP turns did not publish expected prefix: %+v", before)
	}
	child := decodeData[domain.ConversationNode](t, request(t, router, http.MethodPost, base+root.ID+"/fork",
		map[string]any{"message_id": before[1].ID, "title": "prefix branch"}, auth, http.StatusCreated))
	after := decodeData[domain.Page[domain.ConversationMessage]](t, request(t, router, http.MethodGet, base+root.ID+"/messages", nil, auth, http.StatusOK)).Items
	if len(before) != 6 || !reflect.DeepEqual(before, after) {
		t.Fatal("HTTP fork lost/changed tail messages or their Run/version/status metadata")
	}
	detail := decodeData[domain.ConversationGraphDetail](t, request(t, router, http.MethodGet,
		"/v1/conversation-graphs/"+graph.Graph.ID, nil, auth, http.StatusOK))
	if len(detail.Nodes) != 3 {
		t.Fatal("fork must add exactly two nodes")
	}
	for _, node := range detail.Nodes {
		if node.Width != 847 || node.Height != 1139 || node.Locked != (node.ID == child.ParentID) {
			t.Fatalf("fork geometry/write contract: %+v", node)
		}
	}
	if len(child.InheritedMessages) != 2 || child.MessageCount != 0 {
		t.Fatal("new branch must inherit only prefix, without fake local messages")
	}
	auth["Idempotency-Key"] = "frozen-parent"
	request(t, router, http.MethodPost, base+child.ParentID+"/messages", map[string]any{"body": "no-write"}, auth, http.StatusBadRequest)
	for _, nodeID := range []string{root.ID, child.ID} {
		auth["Idempotency-Key"] = "continue-" + nodeID
		message := decodeData[domain.ConversationMessage](t, request(t, router, http.MethodPost, base+nodeID+"/messages",
			map[string]any{"body": "next-question"}, auth, http.StatusCreated))
		var raw string
		if err := repo.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+message.RunID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload nodeprotocol.StartRunPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"prefix-question", "prefix-answer", "next-question"} {
			if strings.Count(payload.Prompt, text) != 1 {
				t.Fatalf("HTTP send did not replay %q exactly once", text)
			}
		}
		for _, text := range []string{"tail-question", "tail-answer", "failed-question"} {
			if strings.Contains(payload.Prompt, text) != (nodeID == root.ID) {
				t.Fatalf("wrong full/prefix context for %s", nodeID)
			}
		}
		if strings.Contains(payload.Prompt, "failed-answer") || !payload.DisableTools || !payload.ReadOnly {
			t.Fatal("HTTP send replayed failed output or lost isolation")
		}
	}
}

func TestConversationForkHTTPRejectsInvalidBusyAndUnfinishedGoalAtomically(t *testing.T) {
	repo, router, auth, graph, user := conversationForkHTTPFixture(t)
	root := graph.Nodes[0]
	base := "/v1/conversation-nodes/" + root.ID
	finishConversationForkHTTP(t, repo, router, auth, user.ID, root.ID, "first", "question", "answer", "completed")
	before, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{before.Messages[0].ID, "missing-message"} {
		request(t, router, http.MethodPost, base+"/fork", map[string]any{"message_id": boundary}, auth, http.StatusBadRequest)
	}
	rejected, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil || !reflect.DeepEqual(before, rejected) {
		t.Fatal("invalid boundary fork changed graph")
	}
	auth["Idempotency-Key"] = "busy"
	request(t, router, http.MethodPost, base+"/messages", map[string]any{"body": "still running"}, auth, http.StatusCreated)
	busyBefore, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPost, base+"/fork", map[string]any{"message_id": before.Messages[1].ID}, auth, http.StatusConflict)
	busyAfter, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil || !reflect.DeepEqual(busyBefore, busyAfter) {
		t.Fatal("busy fork changed execution or graph")
	}
	// A paused goal still reserves its node, even after its active Run ends.
	finishEvents := []nodeprotocol.RunEvent{{ID: "busy-finished", RunID: busyBefore.Messages[len(busyBefore.Messages)-1].RunID,
		LeaseEpoch: 1, Sequence: 1, Type: "run.finished", OccurredAt: time.Now().UTC(), Payload: map[string]any{"status": "failed"}}}
	if _, err := repo.ApplyLocalEvents(t.Context(), finishEvents); err != nil {
		t.Fatal(err)
	}
	auth["Idempotency-Key"] = "goal"
	goal := decodeData[domain.ConversationGoal](t, request(t, router, http.MethodPost, base+"/goals",
		map[string]any{"objective": "reserved goal", "token_budget": 10000}, auth, http.StatusAccepted))
	request(t, router, http.MethodPost, base+"/goals/"+goal.ID+"/pause", map[string]any{"expected_version": goal.Version}, auth, http.StatusOK)
	goalRun, err := repo.GetRun(t.Context(), user.ID, goal.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "paused-goal-finished", RunID: goalRun.ID, LeaseEpoch: goalRun.LeaseEpoch, Sequence: 1,
		Type: "run.finished", OccurredAt: time.Now().UTC(), Payload: map[string]any{"status": "failed"},
	}}); err != nil {
		t.Fatal(err)
	}
	goalBefore, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPost, base+"/fork", map[string]any{"message_id": before.Messages[1].ID}, auth, http.StatusConflict)
	goalAfter, err := repo.GetConversationGraphDetail(t.Context(), user.ID, graph.Graph.ID)
	if err != nil || !reflect.DeepEqual(goalBefore, goalAfter) {
		t.Fatal("unfinished-goal fork changed graph")
	}
}
