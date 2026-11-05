package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestConversationSendIsAtomicAndIdempotent(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	ctx := context.Background()
	nodeID := graph.Nodes[0].ID
	first, replayed, err := f.repository.SendConversationMessage(ctx, f.user.ID, nodeID, "one-turn", "hello")
	if err != nil || replayed || first.RunID == "" {
		t.Fatalf("send: replay=%v err=%v", replayed, err)
	}
	second, replayed, err := f.repository.SendConversationMessage(ctx, f.user.ID, nodeID, "one-turn", "hello")
	if err != nil || !replayed || second.ID != first.ID {
		t.Fatalf("replay: replay=%v err=%v", replayed, err)
	}
	_, _, err = f.repository.SendConversationMessage(ctx, f.user.ID, nodeID, "one-turn", "different")
	assertConversationError(t, err, "idempotency_conflict")
	_, _, err = f.repository.SendConversationMessage(ctx, f.user.ID, nodeID, "second-turn", "busy input")
	assertConversationError(t, err, "target_busy")
	messages, err := f.repository.ListConversationMessages(ctx, f.user.ID, nodeID)
	if err != nil || len(messages) != 2 || messages[0].ID != first.ID || messages[1].Status != "working" {
		t.Fatalf("partial/duplicate messages: count=%d err=%v", len(messages), err)
	}
	var starts int
	if err := f.repository.db.QueryRow(`SELECT COUNT(*) FROM command_outbox WHERE command_name='run.start'`).Scan(&starts); err != nil || starts != 1 {
		t.Fatalf("outbox starts=%d err=%v", starts, err)
	}
}

func TestConversationUnavailableProviderLeavesNoMessages(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	if _, err := f.repository.db.Exec(`UPDATE agent_services SET status='unavailable' WHERE id=?`, f.service.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, "unavailable", "hello")
	assertConversationError(t, err, "runtime_unavailable")
	updated, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil || len(updated.Messages) != 0 || updated.Nodes[0].Status != "idle" {
		t.Fatalf("failed admission left partial state: err=%v", err)
	}
}

func TestConversationConcurrentSendAdmitsOneTurn(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, key, "hello")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			assertConversationError(t, err, "target_busy")
		}
	}
	if success != 1 {
		t.Fatalf("admitted turns=%d", success)
	}
}

func TestConversationForkPinsContextAndRevision(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	appendConversationMessage(t, f, root.ID, "user", "original question")
	boundary := appendConversationMessage(t, f, root.ID, "agent", "original answer")
	appendConversationMessage(t, f, root.ID, "user", "must not leak after fork")
	child, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{Title: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationMessage(t, f, child.ParentID, "agent", "must not leak from later ancestor edits")
	if _, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: f.agent.ID, AgentServiceID: f.service.ID, Model: "different-model", SystemPrompt: "changed",
	}, nil); err != nil {
		t.Fatal(err)
	}
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, child.ID, "branch-send", "follow-up")
	if err != nil {
		t.Fatal(err)
	}
	var payloadJSON string
	if err := f.repository.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+message.RunID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"original question", "original answer", "follow-up"} {
		if strings.Count(payload.Prompt, text) != 1 {
			t.Errorf("fork context must include %q exactly once", text)
		}
	}
	if strings.Contains(payload.Prompt, "must not leak") || payload.Model == "different-model" || !payload.ReadOnly || payload.ComputerUse {
		t.Fatal("fork leaked mutable context/configuration or write capability")
	}
}

func TestConversationBusyDeletionForkAndCompletion(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	boundary := appendConversationMessage(t, f, root.ID, "agent", "fork boundary")
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "busy", "question")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	assertConversationError(t, f.repository.DeleteConversationNode(t.Context(), f.user.ID, root.ID, current.Version), "target_busy")
	assertConversationError(t, f.repository.DeleteConversationGraph(t.Context(), f.user.ID, graph.Graph.ID, graph.Graph.Version), "target_busy")
	_, err = f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{})
	assertConversationError(t, err, "target_busy")
	_, err = f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "conversation-failed", RunID: message.RunID, LeaseEpoch: 1, Sequence: 1,
		Type: "run.finished", Payload: map[string]any{"status": "failed"}, OccurredAt: time.Now(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil || updated.Nodes[0].Status != "failed" || updated.Messages[len(updated.Messages)-1].Status != "failed" {
		t.Fatalf("failed run left working state: err=%v", err)
	}
}

func TestDeletedProjectHidesConversationNodesAndMessages(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	if err := f.repository.DeleteProject(t.Context(), f.user.ID, f.project.ID, f.project.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.GetConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID); err == nil {
		t.Fatal("deleted project's node remains visible")
	}
	if _, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, "deleted", "hello"); err == nil {
		t.Fatal("deleted project's node accepted a run")
	}
}

func assertConversationError(t *testing.T, err error, code string) {
	t.Helper()
	var typed *domain.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}
