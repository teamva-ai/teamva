package nodeprotocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStartRunPayloadCarriesProjectRepositoryResources(t *testing.T) {
	input := StartRunPayload{
		RunID: "run-1", ProjectID: "project-1", LeaseEpoch: 1, Provider: "codex",
		GitResources: []GitResourceRequest{{ResourceID: "resource-1", Name: "Code", Path: "/safe/code"}},
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var restored StartRunPayload
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.GitResources) != 1 ||
		restored.GitResources[0] != input.GitResources[0] {
		t.Fatalf("restored payload=%+v", restored)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	if _, exists := object["cwd"]; exists {
		t.Fatalf("run.start retained legacy cwd selector: %s", payload)
	}
	if _, exists := object["path"]; exists {
		t.Fatalf("run.start retained legacy path selector: %s", payload)
	}
}

func TestStartRunPayloadRejectsLegacyWorkspaceSelectors(t *testing.T) {
	for _, field := range []string{"cwd", "path"} {
		payload := []byte(`{"run_id":"run-1","project_id":"project-1","lease_epoch":1,"provider":"codex","` +
			field + `":"/legacy"}`)
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		var restored StartRunPayload
		if err := decoder.Decode(&restored); err == nil {
			t.Fatalf("legacy %s selector was accepted", field)
		}
	}
}

func TestCommandEnvelopeHasNoWorkspaceBindingAlias(t *testing.T) {
	encoded, err := json.Marshal(CommandEnvelope{})
	if err != nil || strings.Contains(string(encoded), "workspace_binding_version") {
		t.Fatalf("obsolete binding field: %s %v", encoded, err)
	}
	decoder := json.NewDecoder(strings.NewReader(`{"workspace_binding_version":7}`))
	decoder.DisallowUnknownFields()
	var command CommandEnvelope
	if err := decoder.Decode(&command); err == nil {
		t.Fatal("obsolete binding field accepted")
	}
}

func TestRunEventEncodingHasIndependentEightMiBBoundary(t *testing.T) {
	if MaxRunEventBytes >= MaxRunEventBatchBytes || MaxRunEventPayloadBytes >= MaxRunEventBytes {
		t.Fatalf("event=%d payload=%d batch=%d", MaxRunEventBytes, MaxRunEventPayloadBytes, MaxRunEventBatchBytes)
	}
	base := RunEvent{
		ID: "event-1", RunID: "run-1", LeaseEpoch: 1, Sequence: 1,
		Type: "output.delta", OccurredAt: time.Now().UTC(),
	}
	base.Payload = map[string]any{"text": strings.Repeat("x", MaxRunEventPayloadBytes)}
	if _, _, err := EncodeRunEvent(base); !errors.Is(err, ErrRunEventTooLarge) {
		t.Fatalf("oversized payload error=%v", err)
	}
	base.Payload = map[string]any{"text": "bounded"}
	encoded, payload, err := EncodeRunEvent(base)
	if err != nil || len(encoded) == 0 || len(payload) == 0 {
		t.Fatalf("encoded=%d payload=%d err=%v", len(encoded), len(payload), err)
	}
}

func TestRunEventEncodingRedactsInlineImageDataURLs(t *testing.T) {
	event := RunEvent{
		ID: "event-redacted", RunID: "run-redacted", LeaseEpoch: 1, Sequence: 1,
		Type: "provider.event", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"part": map[string]any{"type": "file", "url": "data:image/png;base64,secret"},
			"text": "provider echoed data:image/png;base64,secret",
			"raw":  json.RawMessage(`{"url":"data:image/png;base64,secret"}`),
		},
	}
	encoded, payload, err := EncodeRunEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "data:image") || strings.Contains(string(payload), "data:image") {
		t.Fatalf("encoded event retained inline image data: %s", encoded)
	}
}

func TestEmbeddedProtocolRejectsOversizedCommandResult(t *testing.T) {
	if Version != 6 {
		t.Fatalf("protocol version=%d want 6", Version)
	}
	if _, err := EncodeCommandResult(CommandResult{
		CommandID: "message-1", Status: "completed", Result: json.RawMessage(`"` + strings.Repeat("x", MaxCommandResultBytes) + `"`),
	}); err == nil {
		t.Fatal("oversized command result was accepted")
	}
}

func TestRunPayloadsCarryImageAttachments(t *testing.T) {
	attachment := Attachment{
		MimeType: "image/png", Filename: "capture.png",
		DataURL: "data:image/png;base64,iVBORw0KGgo=",
	}
	if err := ValidateAttachments([]Attachment{attachment}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(StartRunPayload{Attachments: []Attachment{attachment}})
	if err != nil {
		t.Fatal(err)
	}
	var restored StartRunPayload
	if err := json.Unmarshal(encoded, &restored); err != nil || len(restored.Attachments) != 1 || restored.Attachments[0] != attachment {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestValidateAttachmentsRejectsMismatchedImageBytes(t *testing.T) {
	if err := ValidateAttachments([]Attachment{{
		MimeType: "image/png", Filename: "fake.png", DataURL: "data:image/png;base64,SGVsbG8=",
	}}); err == nil {
		t.Fatal("mismatched image bytes were accepted")
	}
}
