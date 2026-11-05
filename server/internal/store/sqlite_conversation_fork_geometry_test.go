package store

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func conversationGeometrySnapshot(t *testing.T, f sqliteExecutionFixture, graphID string) domain.ConversationGraphDetail {
	t.Helper()
	detail, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graphID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func conversationNodesByID(detail domain.ConversationGraphDetail) map[string]domain.ConversationNode {
	result := make(map[string]domain.ConversationNode, len(detail.Nodes))
	for _, node := range detail.Nodes {
		result[node.ID] = node
	}
	return result
}

func forkConversationForGeometry(t *testing.T, f sqliteExecutionFixture, sourceID, messageID string) domain.ConversationNode {
	t.Helper()
	child, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, sourceID, messageID, domain.ConversationNode{Title: "几何分支"})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func moveConversationForGeometry(t *testing.T, f sqliteExecutionFixture, graphID, nodeID string, x, y, width, height float64) {
	t.Helper()
	node := conversationNodesByID(conversationGeometrySnapshot(t, f, graphID))[nodeID]
	if _, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, nodeID, node.Version, func(n *domain.ConversationNode) {
		n.PositionX, n.PositionY, n.Width, n.Height = x, y, width, height
	}); err != nil {
		t.Fatal(err)
	}
}

// Test actual persisted border boxes, not just node centers. In the browser a
// card covers its entire width/height, including the writable branch's textarea.
func assertConversationBorderBoxesSeparate(t *testing.T, detail domain.ConversationGraphDetail) {
	t.Helper()
	for i, a := range detail.Nodes {
		for _, b := range detail.Nodes[i+1:] {
			if a.PositionX < b.PositionX+b.Width && a.PositionX+a.Width > b.PositionX &&
				a.PositionY < b.PositionY+b.Height && a.PositionY+a.Height > b.PositionY {
				t.Fatalf("overlapping border boxes: %s (%g,%g %gx%g), %s (%g,%g %gx%g)",
					a.ID, a.PositionX, a.PositionY, a.Width, a.Height, b.ID, b.PositionX, b.PositionY, b.Width, b.Height)
			}
		}
	}
}

// A fork adds exactly two nodes. Every old descendant retains its relative
// geometry, message snapshot, status and parent chain; only source is reparented
// and made writable. No unrelated node (including its version) may change.
func assertConversationSubtreeTranslation(t *testing.T, before, after domain.ConversationGraphDetail, sourceID, childID string) {
	t.Helper()
	old, current := conversationNodesByID(before), conversationNodesByID(after)
	if len(after.Nodes) != len(before.Nodes)+2 || after.Graph.Version != before.Graph.Version+1 {
		t.Fatalf("fork must add two nodes and one graph revision: nodes %d -> %d, version %d -> %d",
			len(before.Nodes), len(after.Nodes), before.Graph.Version, after.Graph.Version)
	}
	source, moved := old[sourceID], current[sourceID]
	dx, dy := moved.PositionX-source.PositionX, moved.PositionY-source.PositionY
	if dx <= 0 || dy >= 0 {
		t.Fatalf("expected upper-right translation, got (%g,%g)", dx, dy)
	}
	child, parent := current[childID], current[moved.ParentID]
	if _, existed := old[parent.ID]; existed || parent.ParentID != source.ParentID ||
		parent.PositionX != source.PositionX || parent.PositionY != source.PositionY ||
		parent.Width != source.Width || parent.Height != source.Height || !parent.Locked || moved.Locked || child.Locked ||
		child.ParentID != parent.ID || child.MessageCount != 0 || child.Width != source.Width || child.Height != source.Height {
		t.Fatalf("one-to-three fork metadata changed: source=%+v parent=%+v child=%+v", moved, parent, child)
	}
	if child.PositionX != moved.PositionX || math.Abs((child.PositionY+moved.PositionY)/2-source.PositionY) > 0.000001 {
		t.Fatal("source and new branch must remain symmetric around the history copy")
	}
	for id, previous := range old {
		inSubtree := id == sourceID
		for ancestor := previous.ParentID; ancestor != "" && !inSubtree; ancestor = old[ancestor].ParentID {
			inSubtree = ancestor == sourceID
		}
		actual := current[id]
		if !inSubtree {
			if !reflect.DeepEqual(previous, actual) {
				t.Fatalf("unrelated node changed: before=%+v after=%+v", previous, actual)
			}
			continue
		}
		if math.Abs(actual.PositionX-previous.PositionX-dx) > 0.000001 ||
			math.Abs(actual.PositionY-previous.PositionY-dy) > 0.000001 || actual.Version != previous.Version+1 {
			t.Fatalf("subtree did not translate rigidly once: before=%+v after=%+v delta=(%g,%g)", previous, actual, dx, dy)
		}
		// Normalize the fields intentionally changed by the fork, then compare
		// everything else (including inherited context and message previews).
		actual.PositionX, actual.PositionY = previous.PositionX, previous.PositionY
		actual.Version, actual.UpdatedAt = previous.Version, previous.UpdatedAt
		if id == sourceID {
			actual.ParentID, actual.ForkMessageID, actual.Locked = previous.ParentID, previous.ForkMessageID, previous.Locked
		}
		if !reflect.DeepEqual(actual, previous) {
			t.Fatalf("fork changed subtree data beyond layout: before=%+v after=%+v", previous, actual)
		}
	}
	for _, message := range before.Messages {
		found := false
		for _, actual := range after.Messages {
			if actual.ID == message.ID {
				found = reflect.DeepEqual(actual, message)
				break
			}
		}
		if !found {
			t.Fatalf("fork changed an existing message: %+v", message)
		}
	}
	assertConversationBorderBoxesSeparate(t, after)
}

func TestSQLiteConversationForkRepeatedAndNestedSubtreeGeometry(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "root boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	writable := first
	// Fork writable branches twice, then branch repeatedly from their locked
	// ancestors. This produces several generations and unaffected sibling trees.
	for i := 0; i < 2; i++ {
		message := appendConversationMessage(t, f, writable.ID, "agent", "nested boundary")
		before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
		writable = forkConversationForGeometry(t, f, writable.ID, message.ID)
		assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), message.NodeID, writable.ID)
	}
	for _, sourceID := range []string{first.ParentID, first.ParentID, writable.ParentID, first.ParentID} {
		messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, sourceID)
		if err != nil || len(messages) == 0 {
			t.Fatalf("source boundary: %v", err)
		}
		before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
		child := forkConversationForGeometry(t, f, sourceID, messages[len(messages)-1].ID)
		assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), sourceID, child.ID)
	}
}

func TestSQLiteConversationForkPreservesManualSubtreeAndAvoidsFixedCards(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "root boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	nestedMessage := appendConversationMessage(t, f, first.ID, "agent", "nested boundary")
	nested := forkConversationForGeometry(t, f, first.ID, nestedMessage.ID)
	// nested.ParentID has two descendants. Keep one to its left, with unequal
	// dimensions and fractional/negative coordinates; leave a stationary sibling
	// directly across the subtree's initial translated position.
	moveConversationForGeometry(t, f, graph.Graph.ID, first.ParentID, -2000, -1800, 380, 560)
	moveConversationForGeometry(t, f, graph.Graph.ID, graph.Nodes[0].ID, 583.5, -1781.25, 640, 800)
	moveConversationForGeometry(t, f, graph.Graph.ID, nested.ParentID, 123.5, -211.25, 300, 260)
	moveConversationForGeometry(t, f, graph.Graph.ID, first.ID, -526.5, 398.75, 1000, 1600)
	moveConversationForGeometry(t, f, graph.Graph.ID, nested.ID, 123.5, 2200.75, 540, 800)
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	assertConversationBorderBoxesSeparate(t, before)
	parentMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, nested.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	child := forkConversationForGeometry(t, f, nested.ParentID, parentMessages[0].ID)
	after := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	assertConversationSubtreeTranslation(t, before, after, nested.ParentID, child.ID)
	// This obstacle intersects the first valid subtree translation; an extra
	// rightward displacement is required instead of moving the fixed sibling.
	moved := conversationNodesByID(after)[nested.ParentID]
	if moved.PositionX-123.5 <= 1030 {
		t.Fatalf("fixed obstacle was not avoided: source x=%g", moved.PositionX)
	}
}

func TestSQLiteConversationForkIgnoresDeletedAndOtherGraphGeometry(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	deleted, err := f.repository.GetConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.DeleteConversationNode(t.Context(), f.user.ID, deleted.ID, deleted.Version); err != nil {
		t.Fatal(err)
	}
	var deletedVersion int64
	var deletedX, deletedY string
	if err := f.repository.db.QueryRow(`SELECT version, position_x, position_y FROM conversation_nodes WHERE id=?`, deleted.ID).
		Scan(&deletedVersion, &deletedX, &deletedY); err != nil {
		t.Fatal(err)
	}
	other, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID,
		domain.ConversationGraph{ProjectID: f.project.ID, Title: "unrelated graph"}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	moveConversationForGeometry(t, f, other.Graph.ID, other.Nodes[0].ID, 460, -320, 1000, 1600)
	otherBefore := conversationGeometrySnapshot(t, f, other.Graph.ID)
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	child := forkConversationForGeometry(t, f, first.ParentID, messages[0].ID)
	after := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	assertConversationSubtreeTranslation(t, before, after, first.ParentID, child.ID)
	if current := conversationNodesByID(after)[first.ParentID]; current.PositionX != 460 || current.PositionY != -320 {
		t.Fatalf("deleted/other graph rectangles affected placement: %+v", current)
	}
	if !reflect.DeepEqual(otherBefore, conversationGeometrySnapshot(t, f, other.Graph.ID)) {
		t.Fatal("other graph was changed")
	}
	var version int64
	var x, y string
	if err := f.repository.db.QueryRow(`SELECT version, position_x, position_y FROM conversation_nodes WHERE id=?`, deleted.ID).Scan(&version, &x, &y); err != nil {
		t.Fatal(err)
	}
	if version != deletedVersion || x != deletedX || y != deletedY {
		t.Fatal("soft-deleted descendant was moved")
	}
}

func TestSQLiteConversationForkGeometryRollback(t *testing.T) {
	for _, failure := range []string{"duplicate_child", "coordinate_bound"} {
		t.Run(failure, func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
			first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
			messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
			if err != nil {
				t.Fatal(err)
			}
			// History insertion, subtree translation and source unlock/reparent
			// must all roll back if the final child INSERT fails.
			appendConversationMessage(t, f, first.ParentID, "user", "must survive rollback")
			child := domain.ConversationNode{Versioned: domain.Versioned{ID: first.ID}}
			if failure == "coordinate_bound" {
				child.ID = ""
				moveConversationForGeometry(t, f, graph.Graph.ID, first.ParentID, 999999, 0, 380, 560)
			}
			before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
			var nodeCount, messageCount, recordCount int
			if err := f.repository.db.QueryRow(`SELECT (SELECT COUNT(*) FROM conversation_nodes),
				(SELECT COUNT(*) FROM conversation_messages), (SELECT COUNT(*) FROM work_records)`).Scan(&nodeCount, &messageCount, &recordCount); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, first.ParentID, messages[0].ID, child); err == nil {
				t.Fatal("expected failed fork")
			}
			if !reflect.DeepEqual(before, conversationGeometrySnapshot(t, f, graph.Graph.ID)) {
				t.Fatal("failed fork left visible partial state")
			}
			var nodes, storedMessages, records int
			if err := f.repository.db.QueryRow(`SELECT (SELECT COUNT(*) FROM conversation_nodes),
				(SELECT COUNT(*) FROM conversation_messages), (SELECT COUNT(*) FROM work_records)`).Scan(&nodes, &storedMessages, &records); err != nil {
				t.Fatal(err)
			}
			if nodes != nodeCount || storedMessages != messageCount || records != recordCount {
				t.Fatal("failed fork retained inserted nodes/messages/work records")
			}
		})
	}
}

func TestSQLiteConversationConcurrentForkGeometry(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, first.ParentID, messages[0].ID, domain.ConversationNode{})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	after := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	if len(after.Nodes) != len(before.Nodes)+4 || after.Graph.Version != before.Graph.Version+2 {
		t.Fatal("concurrent forks lost a committed split")
	}
	assertConversationBorderBoxesSeparate(t, after)
	current := conversationNodesByID(after)
	for _, old := range before.Nodes {
		if current[old.ID].Version != old.Version+2 {
			t.Fatalf("old subtree was not translated once per fork: %s", old.ID)
		}
	}
	// A stale drag/resize cannot overwrite coordinates written by the fork.
	_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, first.ID, first.Version,
		func(node *domain.ConversationNode) { node.PositionX = -200 })
	assertConversationError(t, err, "version_conflict")
}

func TestSQLiteConversationForkResizedBorderBoxes(t *testing.T) {
	for _, size := range [][2]float64{{300, 260}, {1000, 260}, {300, 1600}, {1000, 1600}} {
		t.Run(fmt.Sprintf("%gx%g", size[0], size[1]), func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			root := graph.Nodes[0]
			moveConversationForGeometry(t, f, graph.Graph.ID, root.ID, -319.75, 117.5, size[0], size[1])
			boundary := appendConversationMessage(t, f, root.ID, "agent", "resized boundary")
			before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
			child := forkConversationForGeometry(t, f, root.ID, boundary.ID)
			assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), root.ID, child.ID)
		})
	}
}

func TestSQLiteConversationForkResizedSourceWithManualDescendants(t *testing.T) {
	for _, size := range [][2]float64{{300, 260}, {1000, 260}, {300, 1600}, {1000, 1600}, {847.25, 1139.5}} {
		t.Run(fmt.Sprintf("%gx%g", size[0], size[1]), func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			root := graph.Nodes[0]
			boundary := appendConversationMessage(t, f, root.ID, "agent", "boundary")
			first := forkConversationForGeometry(t, f, root.ID, boundary.ID)
			// For a wide source the right descendant crosses the new child's
			// actual column even though it misses the old default 380px column.
			moveConversationForGeometry(t, f, graph.Graph.ID, first.ParentID, 0, 0, size[0], size[1])
			moveConversationForGeometry(t, f, graph.Graph.ID, root.ID, -1300, -1600, 1000, 1600)
			moveConversationForGeometry(t, f, graph.Graph.ID, first.ID, 600, 2000, 300, 260)
			messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
			if err != nil {
				t.Fatal(err)
			}
			before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
			assertConversationBorderBoxesSeparate(t, before)
			child := forkConversationForGeometry(t, f, first.ParentID, messages[0].ID)
			assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), first.ParentID, child.ID)
		})
	}
}

func TestSQLiteConversationForkMovesRunningDescendantWithoutChangingRun(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, first.ID, "running-descendant", "still running")
	if err != nil {
		t.Fatal(err)
	}
	var beforeRun string
	if err := f.repository.db.QueryRow(`SELECT status FROM runs WHERE id=?`, message.RunID).Scan(&beforeRun); err != nil {
		t.Fatal(err)
	}
	parentMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	child := forkConversationForGeometry(t, f, first.ParentID, parentMessages[0].ID)
	assertConversationSubtreeTranslation(t, before, conversationGeometrySnapshot(t, f, graph.Graph.ID), first.ParentID, child.ID)
	var afterRun string
	if err := f.repository.db.QueryRow(`SELECT status FROM runs WHERE id=?`, message.RunID).Scan(&afterRun); err != nil {
		t.Fatal(err)
	}
	if beforeRun != afterRun {
		t.Fatal("layout changed descendant execution")
	}
}

func TestSQLiteConversationConcurrentForkAndManualMove(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
	first := forkConversationForGeometry(t, f, graph.Nodes[0].ID, boundary.ID)
	parentMessages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, first.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	before := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	start := make(chan struct{})
	forkResult, moveResult := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, first.ParentID, parentMessages[0].ID, domain.ConversationNode{})
		forkResult <- err
	}()
	go func() {
		<-start
		_, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, first.ID, first.Version, func(n *domain.ConversationNode) {
			n.PositionX, n.PositionY = 123.25, 789.5
		})
		moveResult <- err
	}()
	close(start)
	if err := <-forkResult; err != nil {
		t.Fatal(err)
	}
	moveErr := <-moveResult
	if moveErr != nil {
		assertConversationError(t, moveErr, "version_conflict")
	}
	after := conversationGeometrySnapshot(t, f, graph.Graph.ID)
	current, old := conversationNodesByID(after), conversationNodesByID(before)
	dx := current[first.ParentID].PositionX - old[first.ParentID].PositionX
	dy := current[first.ParentID].PositionY - old[first.ParentID].PositionY
	expectedX, expectedY, expectedVersion := first.PositionX+dx, first.PositionY+dy, first.Version+1
	if moveErr == nil {
		expectedX, expectedY, expectedVersion = 123.25+dx, 789.5+dy, first.Version+2
	}
	moved := current[first.ID]
	if moved.PositionX != expectedX || moved.PositionY != expectedY || moved.Version != expectedVersion {
		t.Fatalf("concurrent move was lost or overwrote fork geometry: %+v; want (%g,%g) v%d", moved, expectedX, expectedY, expectedVersion)
	}
	assertConversationBorderBoxesSeparate(t, after)
}
