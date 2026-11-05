package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestTurnCompletedNormalizesNonSuccessfulStatuses(t *testing.T) {
	tests := []struct {
		name           string
		providerStatus string
		wantStatus     string
		wantReason     string
		wantError      bool
	}{
		{name: "failed", providerStatus: "failed", wantStatus: "failed", wantReason: "provider_failed", wantError: true},
		{name: "interrupted", providerStatus: "interrupted", wantStatus: "cancelled", wantReason: "interrupted"},
		{name: "cancelled", providerStatus: "cancelled", wantStatus: "cancelled", wantReason: "interrupted"},
		{name: "unknown", providerStatus: "unexpected", wantStatus: "failed", wantReason: "provider_failed", wantError: true},
		{name: "empty", providerStatus: "", wantStatus: "failed", wantReason: "provider_failed", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := map[string]any{
				"turn": map[string]any{
					"id":     "turn-1",
					"status": test.providerStatus,
				},
				"provider_detail": map[string]any{"code": "observable"},
			}
			encodedParams, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{
				"method": "turn/completed",
				"params": params,
			})
			if err != nil {
				t.Fatal(err)
			}

			client := New("codex")
			events := make([]adapter.Event, 0, 1)
			state := &runState{
				ctx:     context.Background(),
				request: adapter.RunRequest{RunID: "run-1"},
				sink: func(_ context.Context, event adapter.Event) error {
					events = append(events, event)
					return nil
				},
				done: make(chan error, 1),
			}
			client.mu.Lock()
			client.active = state
			client.mu.Unlock()

			client.handleNotification(rpcMessage{
				Method: "turn/completed",
				Params: encodedParams,
			}, raw)

			activeErr := <-state.done
			if (activeErr != nil) != test.wantError {
				t.Fatalf("active error=%v, wantError=%t", activeErr, test.wantError)
			}
			if len(events) != 1 {
				t.Fatalf("events=%+v, want one terminal event", events)
			}
			event := events[0]
			if event.Type != "run.finished" || event.Payload["status"] != test.wantStatus ||
				event.Payload["reason"] != test.wantReason {
				t.Fatalf("terminal event=%+v", event)
			}
			turn, _ := event.Payload["turn"].(map[string]any)
			detail, _ := event.Payload["provider_detail"].(map[string]any)
			if turn["status"] != test.providerStatus || detail["code"] != "observable" {
				t.Fatalf("original provider params were not retained: %+v", event.Payload)
			}
			if event.ProviderEventType != "turn/completed" || !bytes.Equal(event.ProviderRaw, raw) {
				t.Fatalf("provider observation type=%q raw=%s, want raw=%s",
					event.ProviderEventType, event.ProviderRaw, raw)
			}
		})
	}
}
