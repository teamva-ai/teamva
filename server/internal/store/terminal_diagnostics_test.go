package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestFailedLocalCommandPreservesSafeFallbackWithoutDuplicatingNativeTerminal(t *testing.T) {
	for _, nativeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback-only", true: "native-first"}[nativeFirst], func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			run := f.createRun(t, "terminal-diagnostics")
			claim, ok, err := f.repository.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim ok=%v err=%v", ok, err)
			}
			if nativeFirst {
				event := nodeprotocol.RunEvent{ID: "native-terminal", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.finished", Payload: map[string]any{"status": "failed", "message": "native diagnostic is authoritative"}, OccurredAt: time.Now().UTC()}
				for range 2 {
					if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{event}); err != nil {
						t.Fatal(err)
					}
				}
			}
			failure := json.RawMessage(`{"error":{"data":{"message":"codex cannot enforce DisableTools fallback fixture","response":{"body":"API_KEY=fixture-only-key; request=req-terminal-fixture"}}},"headers":{"Authorization":"Bearer fixture-only-header"}}`)
			if err := f.repository.FailLocalCommand(t.Context(), claim.ID, claim.ClaimToken, "provider_error", failure); err != nil {
				t.Fatal(err)
			}
			events, err := f.repository.ListRunEvents(t.Context(), f.user.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[1].Sequence != 2 || events[1].Type != "run.finished" {
				t.Fatalf("fallback duplicated terminal or introduced sequence gap: %+v", events)
			}
			if nativeFirst {
				if events[1].ID != "native-terminal" || events[1].Payload["message"] != "native diagnostic is authoritative" {
					t.Fatal("command result replaced the native terminal")
				}
			} else {
				message, _ := events[1].Payload["message"].(string)
				if !strings.Contains(message, "codex cannot enforce DisableTools fallback fixture") || !strings.Contains(message, "req-terminal-fixture") {
					t.Fatal("fallback terminal discarded actionable diagnosis")
				}
			}
			// Inspect actual write columns, not only an HTTP/read projection.
			var eventJSON, resultJSON string
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT payload_wire FROM run_events WHERE run_id=? AND sequence=2`, run.ID).Scan(&eventJSON); err != nil {
				t.Fatal(err)
			}
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT result FROM command_outbox WHERE id=?`, claim.ID).Scan(&resultJSON); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(eventJSON+resultJSON, "fixture-only-") {
				t.Fatal("raw diagnostic secret entered event/command storage")
			}
			t.Logf("persisted terminal=%s; persisted command result=%s", eventJSON, resultJSON)
		})
	}
}
