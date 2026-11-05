package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func finishConversationForkTurn(t *testing.T, f sqliteExecutionFixture, nodeID, key, question, answer, status string) domain.Run {
	t.Helper()
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, nodeID, key, question)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.GetRun(t.Context(), f.user.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "run.started"},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"text": answer}},
		nodeprotocol.RunEvent{Type: "run.finished", Payload: map[string]any{"status": status}},
	)
	run, err = f.repository.GetRun(t.Context(), f.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func conversationForkStartPayload(t *testing.T, f sqliteExecutionFixture, runID string) nodeprotocol.StartRunPayload {
	t.Helper()
	var raw string
	if err := f.repository.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertConversationForkPrompt(t *testing.T, payload nodeprotocol.StartRunPayload, included, excluded []string) {
	t.Helper()
	for _, text := range included {
		if strings.Count(payload.Prompt, text) != 1 {
			t.Errorf("prompt must contain %q exactly once", text)
		}
	}
	for _, text := range excluded {
		if strings.Contains(payload.Prompt, text) {
			t.Errorf("prompt unexpectedly contains %q", text)
		}
	}
	if !payload.ReadOnly || !payload.DisableTools || payload.ComputerUse {
		t.Fatal("fork changed run isolation")
	}
}

func TestConversationForkPreservesFullContinuationAndHistoricalRuns(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	moveConversationForGeometry(t, f, graph.Graph.ID, root.ID, -17.5, 32.25, 713, 927)
	runs := []domain.Run{
		finishConversationForkTurn(t, f, root.ID, "prefix-turn", "prefix-question", "prefix-answer", "completed"),
		finishConversationForkTurn(t, f, root.ID, "tail-turn", "tail-question", "tail-answer", "completed"),
		finishConversationForkTurn(t, f, root.ID, "failed-turn", "failed-question", "failed-answer", "failed"),
	}
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	oldMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	if err != nil || len(oldMessages) != 6 || oldMessages[5].Status != "failed" {
		t.Fatalf("historical turns: %+v %v", oldMessages, err)
	}
	child := forkConversationForGeometry(t, f, root.ID, oldMessages[1].ID)
	after := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	assertConversationSubtreeTranslation(t, before, after, root.ID, child.ID)
	actual, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	if err != nil || !reflect.DeepEqual(oldMessages, actual) {
		t.Fatal("fork changed full continuation message IDs, versions, status, timestamps or run links")
	}
	for _, previous := range runs {
		current, err := f.repository.GetRun(t.Context(), f.user.ID, previous.ID)
		if err != nil || !reflect.DeepEqual(previous, current) {
			t.Fatalf("historical run changed: %s %v", previous.ID, err)
		}
	}
	parentMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, child.ParentID)
	if err != nil || len(parentMessages) != 2 {
		t.Fatalf("frozen prefix: %+v %v", parentMessages, err)
	}
	for i, copy := range parentMessages {
		original := oldMessages[i]
		if copy.ID == original.ID || copy.RunID != original.RunID || copy.Version != original.Version ||
			copy.Status != original.Status || copy.Body != original.Body || !copy.CreatedAt.Equal(original.CreatedAt) || !copy.UpdatedAt.Equal(original.UpdatedAt) {
			t.Fatal("frozen prefix rewrote historical message metadata")
		}
	}
	if _, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: f.agent.ID, AgentServiceID: f.service.ID, Model: "replacement-model", SystemPrompt: "replacement",
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{root.ID, child.ID} {
		message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, nodeID, "continue-"+nodeID, "next-question")
		if err != nil {
			t.Fatal(err)
		}
		payload := conversationForkStartPayload(t, f, message.RunID)
		included, excluded := []string{"prefix-question", "prefix-answer", "next-question"}, []string{"failed-answer"}
		if nodeID == root.ID {
			included = append(included, "tail-question", "tail-answer", "failed-question")
		} else {
			excluded = append(excluded, "tail-question", "tail-answer", "failed-question")
		}
		assertConversationForkPrompt(t, payload, included, excluded)
		node, err := f.repository.GetConversationNode(t.Context(), f.user.ID, nodeID)
		if err != nil || payload.Model != "test/model" || node.AgentRevisionID != root.AgentRevisionID {
			t.Fatal("continuation lost pinned agent revision")
		}
	}
	if _, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, child.ParentID, "frozen", "no-write"); err == nil {
		t.Fatal("frozen parent admitted a turn")
	}
	var deleted int
	if err := f.repository.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE deleted_at IS NOT NULL`).Scan(&deleted); err != nil || deleted != 0 {
		t.Fatalf("fork soft-deleted historical messages: %d %v", deleted, err)
	}
}

func TestConversationNestedForkContextDoesNotReplayAncestorsTwice(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	finishConversationForkTurn(t, f, root.ID, "root", "root-question", "root-answer", "completed")
	messages, _ := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	first := forkConversationForGeometry(t, f, root.ID, messages[1].ID)
	finishConversationForkTurn(t, f, first.ID, "nested", "nested-question", "nested-answer", "completed")
	finishConversationForkTurn(t, f, first.ID, "nested-tail", "nested-tail-question", "nested-tail-answer", "completed")
	messages, _ = f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ID)
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	second := forkConversationForGeometry(t, f, first.ID, messages[1].ID)
	assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), first.ID, second.ID)
	if len(second.InheritedMessages) != 4 {
		t.Fatalf("nested prefix was duplicated: %+v", second.InheritedMessages)
	}
	for _, nodeID := range []string{first.ID, second.ID} {
		message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, nodeID, "nested-next-"+nodeID, "nested-next-question")
		if err != nil {
			t.Fatal(err)
		}
		included := []string{"root-question", "root-answer", "nested-question", "nested-answer", "nested-next-question"}
		var excluded []string
		if nodeID == first.ID {
			included = append(included, "nested-tail-question", "nested-tail-answer")
		} else {
			excluded = []string{"nested-tail-question", "nested-tail-answer"}
		}
		assertConversationForkPrompt(t, conversationForkStartPayload(t, f, message.RunID), included, excluded)
	}
	// Re-fork a frozen ancestor with active descendants. Only source itself
	// becomes writable; descendants retain their locks, runs and parent chains.
	parentMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, second.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	before = conversationGeometrySnapshot(t, f, graph.Graph.ID)
	third := forkConversationForGeometry(t, f, second.ParentID, parentMessages[1].ID)
	assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), second.ParentID, third.ID)
	if _, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, second.ParentID, "thawed-source", "thawed-question"); err != nil {
		t.Fatalf("formerly frozen source must be writable: %v", err)
	}
}

func TestConversationForkRejectsInvalidBoundaryWithoutMutation(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	userMessage := appendConversationMessage(t, f, root.ID, "user", "not agent")
	failed, err := f.repository.CreateConversationMessage(t.Context(), f.user.ID, root.ID,
		domain.ConversationMessage{AuthorKind: "agent", Body: "not published", Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	published := appendConversationMessage(t, f, root.ID, "agent", "boundary")
	other, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID,
		domain.ConversationGraph{ProjectID: f.project.ID, Title: "other"}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	foreignMessage := appendConversationMessage(t, f, other.Nodes[0].ID, "agent", "other node")
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	for _, boundary := range []string{userMessage.ID, failed.ID, foreignMessage.ID, "missing"} {
		_, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary, domain.ConversationNode{})
		assertConversationError(t, err, "invalid_argument")
	}
	if _, err := f.repository.ForkConversationNode(t.Context(), "another-user", root.ID, published.ID, domain.ConversationNode{}); err == nil {
		t.Fatal("cross-user fork accepted")
	}
	if !reflect.DeepEqual(before, conversationGeometrySnapshot(t, f, graph.Graph.ID)) {
		t.Fatal("rejected boundary changed graph")
	}
}

func TestConversationConcurrentForkAndSendPreservesAdmittedTurn(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	boundary := appendConversationMessage(t, f, root.ID, "agent", "boundary")
	tail := appendConversationMessage(t, f, root.ID, "agent", "tail")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var forkErr, sendErr error
	var child domain.ConversationNode
	var message domain.ConversationMessage
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		child, forkErr = f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{})
	}()
	go func() {
		defer wg.Done()
		<-start
		message, _, sendErr = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "racing-send", "admitted-question")
	}()
	close(start)
	wg.Wait()
	if sendErr != nil {
		t.Fatalf("full continuation must admit send before or after fork: %v", sendErr)
	}
	if forkErr != nil {
		assertConversationError(t, forkErr, "target_busy")
	}
	detail := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	wantNodes := 1
	if forkErr == nil {
		wantNodes = 3
		if len(child.InheritedMessages) != 1 {
			t.Fatal("racing send leaked into prefix snapshot")
		}
	}
	if len(detail.Nodes) != wantNodes {
		t.Fatal("concurrent fork left partial nodes")
	}
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	if err != nil || len(messages) != 4 || messages[1].ID != tail.ID || messages[2].ID != message.ID {
		t.Fatalf("admitted send/history lost: %+v %v", messages, err)
	}
	assertConversationForkPrompt(t, conversationForkStartPayload(t, f, message.RunID),
		[]string{"boundary", "tail", "admitted-question"}, nil)
}

func TestConversationConcurrentForkAndDeleteIsAtomic(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	root := graph.Nodes[0]
	boundary := appendConversationMessage(t, f, root.ID, "agent", "boundary")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var forkErr, deleteErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, forkErr = f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{})
	}()
	go func() {
		defer wg.Done()
		<-start
		deleteErr = f.repository.DeleteConversationNode(t.Context(), f.user.ID, root.ID, root.Version)
	}()
	close(start)
	wg.Wait()
	if deleteErr == nil {
		assertConversationError(t, forkErr, "not_found")
		var live int
		if err := f.repository.db.QueryRow(`SELECT COUNT(*) FROM conversation_nodes WHERE deleted_at IS NULL`).Scan(&live); err != nil || live != 0 {
			t.Fatalf("delete left fork nodes: %d %v", live, err)
		}
	} else {
		assertConversationError(t, deleteErr, "version_conflict")
		if forkErr != nil {
			t.Fatal(forkErr)
		}
		detail := conversationGeometrySnapshot(t, f, graph.Graph.ID)
		if len(detail.Nodes) != 3 {
			t.Fatal("fork was partly deleted")
		}
		assertConversationBorderBoxesSeparate(t, detail)
	}
}

func TestConversationForkRejectsEveryUnfinishedGoalBetweenRuns(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	completeConversationGoalRun(t, f, goal, "published boundary", 10)
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"active", "paused", "blocked", "budget_reached"} {
		t.Run(status, func(t *testing.T) {
			if _, err := f.repository.db.Exec(`UPDATE conversation_goals SET status=? WHERE id=?`, status, goal.ID); err != nil {
				t.Fatal(err)
			}
			before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
			_, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID, messages[len(messages)-1].ID, domain.ConversationNode{})
			assertConversationError(t, err, "goal_already_exists")
			if !reflect.DeepEqual(before, conversationGeometrySnapshot(t, f, graph.Graph.ID)) {
				t.Fatal("rejected goal fork left partial changes")
			}
		})
	}
}
