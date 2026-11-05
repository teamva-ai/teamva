package runtime

import (
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestToolBrokerPreservesStructuredWorkItemsAndOrdinaryDelegation(t *testing.T) {
	caller := &recordingToolCaller{}
	broker := NewToolBroker(caller, "run", "issue", "instance", "agent")
	items := []nodeprotocol.WorkPlanItem{
		{Key: "first", AgentID: "other", Title: "first", Instruction: "instruction"},
		{Key: "second", AgentID: "other", Title: "second", After: []string{"first"}},
	}
	if _, err := broker.IssueCommentCreate(t.Context(), "confirmed", nil, false, items); err != nil {
		t.Fatal(err)
	}
	sent, ok := caller.requests[0].Payload["work_items"].([]nodeprotocol.WorkPlanItem)
	if !ok || len(sent) != 2 || sent[0].Instruction != "instruction" || sent[1].After[0] != "first" {
		t.Fatal("work definitions or dependencies lost in ToolBroker")
	}
	if _, err := broker.IssueCommentCreate(t.Context(), "ordinary", []string{"other"}, false); err != nil {
		t.Fatal(err)
	}
	if _, exists := caller.requests[1].Payload["work_items"]; exists {
		t.Fatal("ordinary delegation unexpectedly became a work plan")
	}
}
