package httpapi

import (
	"bufio"
	"context"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestRunSSEHeartbeatDoesNotReplayLastPage(t *testing.T) {
	repo := newTestRepository(t)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "SSE test", Status: "active"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "SSE test"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := repo.CreateConversationGraph(t.Context(), user.ID, domain.ConversationGraph{ProjectID: project.ID, Title: "SSE"}, domain.ConversationNode{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := repo.SendConversationMessage(t.Context(), user.ID, graph.Nodes[0].ID, "SSE", "test")
	if err != nil {
		t.Fatal(err)
	}
	a := &API{store: repo}
	router := gin.New()
	router.GET("/:run_id", func(c *gin.Context) {
		c.Set(userKey, user)
		a.streamRunEventsWithHeartbeat(c, 10*time.Millisecond)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/"+message.RunID, nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	data, heartbeat := 0, 0
	wroteTerminal, sawTerminal := false, false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data++
		}
		if line == ": heartbeat" {
			heartbeat++
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, "run.completed") {
			sawTerminal = true
		}
		if heartbeat == 3 && !wroteTerminal {
			if data != 1 {
				t.Fatalf("heartbeat replayed %d data frames", data)
			}
			_, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
				ID: "completion", RunID: message.RunID, LeaseEpoch: 1, Sequence: 1, Type: "run.completed",
				Payload: map[string]any{"status": "completed"}, OccurredAt: time.Now().UTC(),
			}})
			if err != nil {
				t.Fatal(err)
			}
			wroteTerminal = true
		}
	}
	if heartbeat < 3 || data != 2 || !sawTerminal {
		t.Fatalf("heartbeats=%d data frames=%d terminal=%t; want >=3 / 2 / true", heartbeat, data, sawTerminal)
	}
}
