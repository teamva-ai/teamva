package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestGatewayNotificationsKeepSanitizedDetailAndRetryIsNotTerminal(t *testing.T) {
	for _, retry := range []bool{true, false} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			client := New("unused")
			var events []adapter.Event
			client.active = &runState{ctx: t.Context(), request: adapter.RunRequest{RunID: "run"}, done: make(chan error, 1), sink: func(_ context.Context, e adapter.Event) error { events = append(events, e); return nil }}
			params := map[string]any{"willRetry": retry, "error": map[string]any{"message": "Gateway 503 unavailable", "additionalDetails": "request=req-fixture; API_KEY=fixture-only-api", "headers": map[string]any{"Authorization": "Bearer fixture-only-auth"}}}
			raw, _ := json.Marshal(params)
			client.handleNotification(rpcMessage{Method: "error", Params: raw}, raw)
			if len(events) != 1 {
				t.Fatalf("events=%+v", events)
			}
			want := "run.error"
			if retry {
				want = "provider.event"
			}
			encoded, _ := json.Marshal(events[0].Payload)
			if events[0].Type != want || !strings.Contains(fmt.Sprint(events[0].Payload["message"]), "Gateway 503 unavailable") || !strings.Contains(fmt.Sprint(events[0].Payload["message"]), "req-fixture") || strings.Contains(string(encoded), "fixture-only-") {
				t.Fatalf("unsafe or missing gateway detail: %s", encoded)
			}
			if retry {
				select {
				case <-client.active.done:
					t.Fatal("retry prematurely completed native turn")
				default:
				}
				client.handleNotification(rpcMessage{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"itemId":"answer","delta":"recovered"}`)}, nil)
				client.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"status":"completed"}}`)}, nil)
				if err := <-client.active.done; err != nil || len(events) != 3 || events[1].Type != "output.delta" || events[2].Type != "run.completed" {
					t.Fatalf("retry recovery=%+v err=%v", events, err)
				}
			}
		})
	}
}

func TestFailedTurnWithoutErrorNotificationRetainsGatewayCause(t *testing.T) {
	client := New("unused")
	var event adapter.Event
	client.active = &runState{ctx: t.Context(), request: adapter.RunRequest{RunID: "run"}, done: make(chan error, 1), sink: func(_ context.Context, e adapter.Event) error { event = e; return nil }}
	client.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"status":"failed","error":{"message":"Gateway 401 rejected","additionalDetails":"Cookie: session=fixture-only-cookie"}}}`)}, nil)
	err := <-client.active.done
	encoded, _ := json.Marshal(event.Payload)
	if err == nil || !strings.Contains(err.Error(), "Gateway 401 rejected") || !strings.Contains(fmt.Sprint(event.Payload["message"]), "Gateway 401 rejected") || strings.Contains(string(encoded)+err.Error(), "fixture-only-cookie") {
		t.Fatalf("failed turn lost safe error detail: %s", encoded)
	}
}

func TestRPCFailureRetainsSafeNestedGatewayDetails(t *testing.T) {
	err := &rpcError{Code: -32000, Message: "Model gateway refused request", Data: json.RawMessage(`{"error":{"message":"Gateway 413 context too long","additionalDetails":"API key provided: fixture-only-rpc"},"headers":{"Authorization":"Bearer fixture-only-rpc-auth"}}`)}
	if !strings.Contains(err.Error(), "Gateway 413 context too long") || strings.Contains(err.Error(), "fixture-only-") {
		t.Fatal("RPC envelope hid gateway cause or leaked a credential")
	}
}
