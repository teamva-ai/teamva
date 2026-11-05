package claude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRealNativeResultFixturesPreserveFailureAndUsefulMessage(t *testing.T) {
	data, err := os.ReadFile("testdata/native-results.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []map[string]any
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 21 {
		t.Fatal("native matrix must retain all 21 cases")
	}
	for _, native := range cases {
		t.Run(stringValue(native["case"]), func(t *testing.T) {
			wantStatus := "failed"
			if native["is_error"] == false {
				wantStatus = "succeeded"
			}
			if resultStatus(native) != wantStatus {
				t.Fatalf("subtype success overrode error bit: %v", native)
			}
			events := []map[string]any{}
			kinds := []string{}
			p := newStreamProjection(func(kind string, payload map[string]any) error {
				kinds = append(kinds, kind)
				events = append(events, payload)
				return nil
			})
			if native["is_error"] == true {
				assistant := map[string]any{"type": "assistant", "error": "unknown", "api_error_status": native["api_error_status"], "message": map[string]any{"id": "fixture-error", "content": []any{map[string]any{"type": "text", "text": native["result"]}}}}
				if err := p.message(assistant); err != nil {
					t.Fatal(err)
				}
				if len(kinds) != 1 || kinds[0] != "provider.event" || events[0]["kind"] != "error" || events[0]["message"] != native["result"] || events[0]["status_code"] != native["api_error_status"] {
					t.Fatalf("native error information changed: %v", events)
				}
				if errorMessage(native) != native["result"] {
					t.Fatal("upstream/CLI useful message lost")
				}
			}
			if err := p.result(native, wantStatus); err != nil {
				t.Fatal(err)
			}
			if wantStatus == "failed" {
				for _, kind := range kinds {
					if kind == "output.snapshot" || kind == "output.delta" {
						t.Fatal("error became public answer")
					}
				}
			}
		})
	}
}

func TestCompactionNativeLifecycleNeverTreatsNullAsCompletion(t *testing.T) {
	for _, mode := range []string{"completed", "failed", "interrupted", "local-command-failed"} {
		t.Run(mode, func(t *testing.T) {
			states := []string{}
			reasons := []string{}
			outputs := 0
			p := newStreamProjection(func(kind string, payload map[string]any) error {
				if kind == "context.compaction" {
					states = append(states, stringValue(payload["status"]))
					reasons = append(reasons, stringValue(payload["reason"]))
				}
				if strings.HasPrefix(kind, "output.") {
					outputs++
				}
				return nil
			})
			_ = p.message(map[string]any{"type": "system", "subtype": "status", "status": "compacting"})
			id := p.compactionID
			_ = p.message(map[string]any{"type": "system", "subtype": "status", "status": nil})
			if p.compactionID != id || len(states) != 1 {
				t.Fatal("status null completed a compaction")
			}
			switch mode {
			case "completed":
				_ = p.message(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "manual", "pre_tokens": 140010.0, "post_tokens": 259.0, "duration_ms": 26.0}})
				if strings.Join(states, ",") != "running,completed" {
					t.Fatal(states)
				}
			case "failed", "local-command-failed":
				_ = p.message(map[string]any{"type": "system", "subtype": "status", "status": nil, "compact_result": "failed", "compact_error": "Error during compaction: API Error: 500 gateway_compact_summary_failed"})
				if strings.Join(states, ",") != "running,failed" || !strings.Contains(reasons[1], "gateway_compact_summary_failed") {
					t.Fatalf("lost failed state/message: %v %v", states, reasons)
				}
				if mode == "local-command-failed" {
					native := map[string]any{"type": "result", "subtype": "success", "is_error": false, "local_command": "compact", "result": "Error during compaction"}
					if resultStatus(native) != "succeeded" {
						t.Fatal("changed native local-command outcome")
					}
					_ = p.result(native, "succeeded")
					if outputs != 0 {
						t.Fatal("compaction error became public answer")
					}
				}
			case "interrupted":
				if p.compactionID == "" || len(states) != 1 {
					t.Fatal("unfinished compaction falsely finalized")
				}
			}
		})
	}
}

func TestStreamingBlockSnapshotsReasoningRetryAndOutputIdentity(t *testing.T) {
	events := []map[string]any{}
	kinds := []string{}
	p := newStreamProjection(func(kind string, payload map[string]any) error {
		events = append(events, payload)
		kinds = append(kinds, kind)
		return nil
	})
	_ = p.message(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "m1"}}})
	for index, text := range []string{"first ", "second"} {
		_ = p.message(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text"}}})
		_ = p.message(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": text}}})
		_ = p.message(map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	if events[len(events)-1]["text"] != "first second" {
		t.Fatalf("same-id per-block assistant snapshots lost text: %v", events)
	}
	_ = p.message(map[string]any{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 1, "retry_delay_ms": 534, "error_status": 429, "error": "rate_limit"})
	if kinds[len(kinds)-1] != "provider.event" || events[len(events)-1]["kind"] != "error" || events[len(events)-1]["willRetry"] != true || events[len(events)-1]["status_code"] != 429 || events[len(events)-1]["message"] != "rate_limit" || events[len(events)-1]["error_status"] != 429 {
		t.Fatal("native retry status lost")
	}
	if update, handled := (outputProjector{}).ProjectOutput("context.compaction", map[string]any{"text": "hidden summary"}); handled || update.Text != "" {
		t.Fatal("compaction projected into answer")
	}
}

func TestPublicProviderEventContractForErrorAndRetry(t *testing.T) {
	p := newStreamProjection(func(kind string, payload map[string]any) error {
		if !publicClaudeEventTypes[kind] {
			t.Errorf("event type %q is not in the public Provider/SSE event contract", kind)
		}
		return nil
	})
	_ = p.message(map[string]any{"type": "assistant", "error": "authentication_failed", "message": map[string]any{"id": "error", "content": []any{map[string]any{"type": "text", "text": "public-safe-error"}}}})
	_ = p.message(map[string]any{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 2, "error_status": 429, "error": "rate_limit", "retry_delay_ms": 100})
}
