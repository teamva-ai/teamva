package codex

import (
	"context"
	"encoding/json"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestHandleNotificationNormalizesContextCompaction(t *testing.T) {
	client := New("codex")
	var events []adapter.Event
	client.active = &runState{
		ctx:     t.Context(),
		request: adapter.RunRequest{RunID: "run-compact"},
		done:    make(chan error, 1),
		sink: func(_ context.Context, event adapter.Event) error {
			events = append(events, event)
			return nil
		},
	}

	for _, message := range []rpcMessage{
		{Method: "item/started", Params: json.RawMessage(`{"item":{"id":"compact-1","type":"contextCompaction"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"id":"compact-1","type":"contextCompaction"}}`)},
	} {
		client.handleNotification(message, message.Params)
	}

	if len(events) != 2 {
		t.Fatalf("expected two normalized compaction events, got %+v", events)
	}
	wantStatuses := []string{"running", "completed"}
	for index, event := range events {
		if event.Type != "context.compaction" || event.Payload["provider"] != "codex" ||
			event.Payload["status"] != wantStatuses[index] {
			t.Fatalf("unexpected event %d: %+v", index, event)
		}
	}
}
