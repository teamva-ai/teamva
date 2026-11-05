package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestCompactionStartedIsRunningAndFailureKeepsGatewayMessage(t *testing.T) {
	c := testClient(t)
	state := &runState{request: provider.RunRequest{RunID: "run"}, session: "ses"}
	events, sink := captureSink()
	for _, event := range []v2Event{
		{ID: "start", Type: "session.compaction.started", Data: map[string]any{"assistantMessageID": "compact"}},
		{ID: "failed", Type: "session.compaction.failed", Data: map[string]any{"assistantMessageID": "compact", "error": map[string]any{"data": map[string]any{"message": "Gateway 503 overloaded; API_KEY=fixture-only-secret"}}}},
		{ID: "execution", Type: "session.execution.failed", Data: map[string]any{"error": map[string]any{"data": map[string]any{"message": "Gateway 429 quota exhausted; Authorization: Bearer fixture-only-auth"}}}},
	} {
		if err := c.handleEvent(t.Context(), state, sink, event); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 3 || (*events)[0].Payload["status"] != "running" {
		t.Fatalf("compaction start was not running: %+v", *events)
	}
	for index, want := range map[int]string{1: "Gateway 503 overloaded", 2: "Gateway 429 quota exhausted"} {
		message, _ := (*events)[index].Payload["message"].(string)
		if !strings.Contains(message, want) || strings.Contains(message, "fixture-only-") {
			t.Fatalf("diagnostic message not safely preserved: %+v", (*events)[index])
		}
	}
}

func TestNativeEventStreamFaultMatrix(t *testing.T) {
	for _, mode := range []string{"eof", "failure-frame", "failed", "interrupted", "retry-success"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(kind string, data map[string]any) {
					data["sessionID"] = "ses"
					raw, _ := json.Marshal(v2Event{ID: kind, Type: kind, Data: data})
					fmt.Fprintf(w, "data: %s\n\n", raw)
					w.(http.Flusher).Flush()
				}
				write("session.execution.started", map[string]any{})
				write("session.compaction.started", map[string]any{"reason": "manual"})
				switch mode {
				case "failure-frame":
					fmt.Fprint(w, "event: effect/http-api/stream/failure\ndata: [{\"_tag\":\"Fail\",\"error\":{\"message\":\"Gateway native stream failure; API_KEY=fixture-only-stream\"}}]\n\n")
				case "failed":
					write("session.execution.failed", map[string]any{"error": map[string]any{"type": "provider.internal", "message": "Gateway failure fixture", "status": 503}})
				case "interrupted":
					write("session.execution.interrupted", map[string]any{})
				case "retry-success":
					write("session.retry.scheduled", map[string]any{"attempt": 1, "error": map[string]any{"message": "Gateway reconnect fixture"}})
					write("session.compaction.ended", map[string]any{"reason": "manual", "text": "summary"})
					write("session.execution.succeeded", map[string]any{})
				}
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			events, sink := captureSink()
			ready, done := make(chan error, 1), make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			go client.streamEvents(ctx, &runState{request: provider.RunRequest{RunID: "run"}, session: "ses"}, sink, ready, done)
			if err := <-ready; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if (err != nil) != (mode == "eof" || mode == "failure-frame") {
					t.Fatalf("stream mode %s err=%v", mode, err)
				}
				if mode == "failure-frame" && (!strings.Contains(err.Error(), "Gateway native stream failure") || strings.Contains(err.Error(), "fixture-only-stream")) {
					t.Fatal("native stream failure detail lost or unsafe")
				}
			case <-ctx.Done():
				t.Fatal("native stream failed to settle")
			}
			if mode == "retry-success" && (len(*events) != 5 || (*events)[2].Payload["willRetry"] != true || (*events)[4].Type != "run.completed") {
				t.Fatalf("retry ended stream before success: %+v", *events)
			}
		})
	}
}

func TestV2NativeCompactionIdentityAndRetryScheduled(t *testing.T) {
	c := testClient(t)
	state := &runState{request: provider.RunRequest{RunID: "run"}, session: "ses"}
	events, sink := captureSink()
	for _, event := range []v2Event{
		{ID: "start-event", Type: "session.compaction.started", Data: map[string]any{"sessionID": "ses", "reason": "manual"}},
		{ID: "end-event", Type: "session.compaction.ended", Data: map[string]any{"sessionID": "ses", "reason": "manual", "text": "private-summary-must-not-be-activity"}},
		{ID: "retry-event", Type: "session.retry.scheduled", Data: map[string]any{"sessionID": "ses", "assistantMessageID": "msg", "attempt": 2, "at": 100, "error": map[string]any{"type": "provider.internal", "message": "Gateway rejected request", "status": 429, "response": map[string]any{"body": `{"error":{"message":"fixture-quota-exhausted"},"Authorization":"Bearer fixture-only-response"}`}}}},
	} {
		if err := c.handleEvent(t.Context(), state, sink, event); err != nil {
			t.Fatal(err)
		}
	}
	if len(*events) != 3 {
		t.Fatalf("native retry disappeared: %+v", *events)
	}
	if (*events)[0].Payload["id"] != (*events)[1].Payload["id"] {
		t.Fatalf("native V2 compaction lifecycle split into unrelated rows: %+v", *events)
	}
	encoded, _ := json.Marshal(*events)
	if strings.Contains(string(encoded), "private-summary") || strings.Contains(string(encoded), "fixture-only-response") {
		t.Fatal("native compaction summary or response credential leaked")
	}
	retry := (*events)[2]
	if retry.Type != "provider.event" || retry.Payload["willRetry"] != true || !strings.Contains(fmt.Sprint(retry.Payload["message"]), "fixture-quota-exhausted") {
		t.Fatalf("retry detail missing or terminal: %+v", retry)
	}
}
