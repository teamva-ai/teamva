package store

import (
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestConversationGoalFailedAuditEnqueueLeavesNoOrphanRun(t *testing.T) {
	f, graph, goal := conversationGoalFixture(t, nil)
	completeConversationGoalRun(t, f, goal, "ready for audit", 10)
	// MapRunEventWriteError turns this post-Run insert failure into a domain error.
	if _, err := f.repository.db.Exec(`CREATE TRIGGER reject_conversation_goal_event BEFORE INSERT ON run_events
	 WHEN NEW.event_type='run.queued' BEGIN SELECT RAISE(ABORT,'run_event_wire_size_invalid'); END`); err != nil {
		t.Fatal(err)
	}
	if count, err := f.repository.AdvanceConversationGoals(t.Context(), time.Now().UTC()); err != nil || count != 1 {
		t.Fatalf("advance: count=%d err=%v", count, err)
	}
	current, err := f.repository.GetCurrentConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID)
	if err != nil || current.Status != domain.GoalBlocked || current.StatusReason != "invalid_run_event" ||
		current.CurrentRunID != goal.CurrentRunID || current.RunCount != 1 {
		t.Fatalf("blocker lost goal state: %+v %v", current, err)
	}
	var runs int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs WHERE conversation_node_id=?`, goal.NodeID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("failed enqueue left an unlinked queued Run: count=%d err=%v", runs, err)
	}
	if _, err := f.repository.db.Exec(`DROP TRIGGER reject_conversation_goal_event`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ChangeConversationGoal(t.Context(), f.user.ID, goal.NodeID, goal.ID, current.Version, "resume"); err != nil {
		t.Fatal(err)
	}
	resumed := advanceConversationGoalTest(t, f, goal.NodeID)
	if resumed.Status != domain.GoalActive || resumed.WorkRuns != 2 || resumed.RunCount != 2 {
		t.Fatalf("orphan prevented recovery: %+v", resumed)
	}
}
