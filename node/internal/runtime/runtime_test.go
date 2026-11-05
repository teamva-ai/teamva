package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/gitread"
	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/opencode"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type fakeAdapter struct {
	name            string
	mu              sync.Mutex
	start           int
	resume          int
	closed          int
	lastCWD         string
	lastPrompt      string
	lastAttachments []nodeprotocol.Attachment
	onStart         func(adapter.RunRequest)
	onVerify        func()
	startErr        error
	terminalEvent   *adapter.Event
	afterTerminal   error
	waitForCancel   bool
	skipTerminal    bool
	sink            adapter.Sink
	closeErr        error
	nativeSessionID string
}

func (f *fakeAdapter) providerName() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}
func (f *fakeAdapter) Name() string { return f.providerName() }
func (f *fakeAdapter) sessionID() string {
	if f.nativeSessionID != "" {
		return f.nativeSessionID
	}
	return "native_1"
}
func (f *fakeAdapter) Probe(context.Context) (adapter.CapabilityManifest, error) {
	return adapter.CapabilityManifest{Provider: f.providerName(), Available: true}, nil
}
func (f *fakeAdapter) Start(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	f.mu.Lock()
	f.start++
	f.lastCWD = request.CWD
	f.lastPrompt = request.Prompt
	f.lastAttachments = append([]nodeprotocol.Attachment(nil), request.Attachments...)
	onStart := f.onStart
	startErr := f.startErr
	terminalEvent := f.terminalEvent
	afterTerminal := f.afterTerminal
	waitForCancel := f.waitForCancel
	skipTerminal := f.skipTerminal
	f.sink = sink
	f.mu.Unlock()
	if onStart != nil {
		onStart(request)
	}
	if waitForCancel {
		<-ctx.Done()
		return adapter.NativeSession{}, ctx.Err()
	}
	if startErr != nil {
		return adapter.NativeSession{}, startErr
	}
	if err := sink(ctx, adapter.Event{Type: "run.started", Payload: map[string]any{}}); err != nil {
		return adapter.NativeSession{}, err
	}
	if !skipTerminal {
		terminal := adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}}
		if terminalEvent != nil {
			terminal = *terminalEvent
		}
		if err := sink(ctx, terminal); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	if afterTerminal != nil {
		return adapter.NativeSession{ID: f.sessionID(), Provider: "fake"}, afterTerminal
	}
	return adapter.NativeSession{ID: f.sessionID(), Provider: "fake"}, nil
}
func (f *fakeAdapter) Resume(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	f.mu.Lock()
	f.resume++
	f.mu.Unlock()
	if request.NativeSessionID != f.sessionID() {
		return adapter.NativeSession{}, errors.New("unexpected native session")
	}
	f.mu.Lock()
	f.lastCWD = request.CWD
	f.lastPrompt = request.Prompt
	f.lastAttachments = append([]nodeprotocol.Attachment(nil), request.Attachments...)
	onStart := f.onStart
	startErr := f.startErr
	terminalEvent := f.terminalEvent
	afterTerminal := f.afterTerminal
	waitForCancel := f.waitForCancel
	skipTerminal := f.skipTerminal
	f.sink = sink
	f.mu.Unlock()
	if onStart != nil {
		onStart(request)
	}
	if waitForCancel {
		<-ctx.Done()
		return adapter.NativeSession{}, ctx.Err()
	}
	if startErr != nil {
		return adapter.NativeSession{}, startErr
	}
	if err := sink(ctx, adapter.Event{Type: "run.started", Payload: map[string]any{}}); err != nil {
		return adapter.NativeSession{}, err
	}
	if !skipTerminal {
		terminal := adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}}
		if terminalEvent != nil {
			terminal = *terminalEvent
		}
		if err := sink(ctx, terminal); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	if afterTerminal != nil {
		return adapter.NativeSession{ID: f.sessionID(), Provider: "fake"}, afterTerminal
	}
	return adapter.NativeSession{ID: f.sessionID(), Provider: "fake"}, nil
}
func (f *fakeAdapter) Interrupt(context.Context, string) error { return nil }
func (f *fakeAdapter) Steer(_ context.Context, _ string, prompt string, attachments []nodeprotocol.Attachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPrompt = prompt
	f.lastAttachments = append([]nodeprotocol.Attachment(nil), attachments...)
	return nil
}
func (f *fakeAdapter) DecideApproval(context.Context, adapter.ApprovalDecision) error {
	return nil
}

func (f *fakeAdapter) VerifySession(context.Context, string) (bool, error) {
	f.mu.Lock()
	onVerify := f.onVerify
	f.mu.Unlock()
	if onVerify != nil {
		onVerify()
	}
	return true, nil
}
func (f *fakeAdapter) Close() error {
	f.mu.Lock()
	f.closed++
	err := f.closeErr
	f.mu.Unlock()
	return err
}

type blockingPreparationAdapter struct {
	*fakeAdapter
	mu                   sync.Mutex
	verifyContext        context.Context
	verifyStarted        chan struct{}
	verifyRelease        chan struct{}
	interruptCalled      chan struct{}
	interrupts           int
	interruptSawCanceled bool
}

type blockingCloseAdapter struct {
	*fakeAdapter
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingCloseAdapter) Close() error {
	f.once.Do(func() { close(f.started) })
	<-f.release
	return f.fakeAdapter.Close()
}

func (f *blockingPreparationAdapter) VerifySession(ctx context.Context, _ string) (bool, error) {
	f.mu.Lock()
	f.verifyContext = ctx
	f.mu.Unlock()
	close(f.verifyStarted)
	<-ctx.Done()
	<-f.verifyRelease
	// Simulate a preparation implementation that completed successfully at the
	// cancellation edge. Runtime's explicit pre-Provider gate must still win.
	return true, nil
}

func (f *blockingPreparationAdapter) Interrupt(context.Context, string) error {
	f.mu.Lock()
	f.interrupts++
	f.interruptSawCanceled = f.verifyContext != nil && f.verifyContext.Err() != nil
	f.mu.Unlock()
	select {
	case f.interruptCalled <- struct{}{}:
	default:
	}
	return testRunNotActiveError{}
}

type testRunNotActiveError struct{}

func (testRunNotActiveError) Error() string      { return "test run is not active" }
func (testRunNotActiveError) RunNotActive() bool { return true }

func TestCommandIsExecutedOnceAndSessionScopeIsEnforced(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	runtime := New("cmp_1", store, nil, t.TempDir(), nil)
	runtime.RegisterTestAdapter("fake", provider)
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_1", IssueID: "issue_1", ProjectID: "project_1",
		IssueAgentInstanceID: "instance_1", IssueAgentGenerationID: "gen_1",
		LeaseEpoch: 1, Provider: "fake",
		Prompt: "test", ReadOnly: true,
		Attachments: []nodeprotocol.Attachment{{
			MimeType: "image/png", Filename: "capture.png", DataURL: "data:image/png;base64,iVBORw0KGgo=",
		}},
	})
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_1", Name: "run.start",
		ComputerID: "cmp_1",
		SentAt:     time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: payload,
	}
	record, replayed, err := runtime.Handle(t.Context(), command)
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("first command: record=%+v replayed=%v err=%v", record, replayed, err)
	}
	retry := command
	retry.SentAt = retry.SentAt.Add(time.Second)
	retry.TraceContext = map[string]any{"trace_id": "delivery-retry"}
	record, replayed, err = runtime.Handle(t.Context(), retry)
	if err != nil || !replayed || record.Status != "completed" {
		t.Fatalf("replayed command: record=%+v replayed=%v err=%v", record, replayed, err)
	}
	provider.mu.Lock()
	startCount := provider.start
	provider.mu.Unlock()
	if startCount != 1 {
		t.Fatalf("duplicate command executed %d times", startCount)
	}
	provider.mu.Lock()
	startAttachments := append([]nodeprotocol.Attachment(nil), provider.lastAttachments...)
	provider.mu.Unlock()
	if len(startAttachments) != 1 || startAttachments[0].Filename != "capture.png" {
		t.Fatalf("run.start attachments=%+v", startAttachments)
	}
	secondPayload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_2", IssueID: "issue_1", ProjectID: "project_1",
		IssueAgentInstanceID: "instance_1", IssueAgentGenerationID: "gen_1",
		LeaseEpoch: 1, Provider: "fake",
		Prompt: "continue", ReadOnly: true,
	})
	command.MessageID = "cmd_2"
	command.Payload = secondPayload
	if record, replayed, err := runtime.Handle(t.Context(), command); err != nil || replayed || record.Status != "completed" {
		t.Fatalf("resume command: record=%+v replayed=%v err=%v", record, replayed, err)
	}
	provider.mu.Lock()
	resumeCount := provider.resume
	provider.mu.Unlock()
	if resumeCount != 1 {
		t.Fatalf("expected one native session resume, got %d", resumeCount)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 4 {
		t.Fatalf("expected four persisted events across start and resume: %+v err=%v", events, err)
	}

}

func TestSteerCarriesImageAttachment(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &fakeAdapter{onStart: func(adapter.RunRequest) {
		close(started)
		<-release
	}}
	engine := New("cmp-steer", store, nil, t.TempDir(), nil)
	engine.RegisterTestAdapter("fake", provider)
	defer engine.Close()

	now := time.Now().UTC()
	startPayload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-steer", IssueID: "issue-steer", ProjectID: "project-steer",
		IssueAgentInstanceID: "instance-steer", IssueAgentGenerationID: "generation-steer",
		LeaseEpoch: 1, Provider: "fake", Prompt: "start", ReadOnly: true,
	})
	startDone := make(chan error, 1)
	go func() {
		_, _, handleErr := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "command-steer-start", Name: "run.start",
			ComputerID: "cmp-steer", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: startPayload,
		})
		startDone <- handleErr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run.start did not reach the adapter")
	}
	attachment := nodeprotocol.Attachment{
		MimeType: "image/png", Filename: "steer.png", DataURL: "data:image/png;base64,iVBORw0KGgo=",
	}
	steerPayload, _ := json.Marshal(nodeprotocol.SteerRunPayload{
		RunID: "run-steer", LeaseEpoch: 1, Provider: "fake", Attachments: []nodeprotocol.Attachment{attachment},
	})
	record, _, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-steer-image", Name: "run.steer",
		ComputerID: "cmp-steer", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: steerPayload,
	})
	if err != nil || record.Status != "completed" {
		t.Fatalf("steer record=%+v err=%v", record, err)
	}
	provider.mu.Lock()
	got := append([]nodeprotocol.Attachment(nil), provider.lastAttachments...)
	provider.mu.Unlock()
	if len(got) != 1 || got[0] != attachment {
		t.Fatalf("steer attachments=%+v", got)
	}
	close(release)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run.start did not finish")
	}
}

func TestCommandIdentityIgnoresDeliveryMetadataAndConflictsOnSemanticChanges(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	engine := New("cmp-identity", store, []adapter.Adapter{provider}, t.TempDir(), nil)
	defer engine.Close()
	now := time.Now().UTC()
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version,
		MessageID:       "command-identity",
		Name:            "agent.probe",
		ComputerID:      "cmp-identity",
		SentAt:          now,
		ExpiresAt:       now.Add(time.Hour),
		IdempotencyKey:  "identity-key",
		TraceContext:    map[string]any{"trace_id": "first"},
		Payload:         json.RawMessage(`{"provider":"fake"}`),
	}
	record, replayed, err := engine.Handle(t.Context(), command)
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("first command: record=%+v replayed=%t err=%v", record, replayed, err)
	}

	retry := command
	retry.SentAt = now.Add(24 * time.Hour)
	retry.TraceContext = map[string]any{
		"trace_id": "retry",
		"nested":   map[string]any{"attempt": 2},
	}
	// A different location for the same expiration instant is not a semantic
	// change and must result in the same stable identity.
	retry.ExpiresAt = retry.ExpiresAt.In(time.FixedZone("retry-offset", 8*60*60))
	record, replayed, err = engine.Handle(t.Context(), retry)
	if err != nil || !replayed || record.Status != "completed" {
		t.Fatalf("delivery retry: record=%+v replayed=%t err=%v", record, replayed, err)
	}

	tests := []struct {
		name   string
		mutate func(*nodeprotocol.CommandEnvelope)
	}{
		{name: "protocol version", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.ProtocolVersion++
		}},
		{name: "computer", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.ComputerID = "different-computer"
		}},
		{name: "expiration", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.ExpiresAt = now.Add(-time.Second)
		}},
		{name: "idempotency key", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.IdempotencyKey = "different-key"
		}},
		{name: "original payload bytes", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.Payload = json.RawMessage(`{"provider": "fake"}`)
		}},
		{name: "name column", mutate: func(item *nodeprotocol.CommandEnvelope) {
			item.Name = "agent.diagnose"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conflict := command
			test.mutate(&conflict)
			_, _, err := engine.Handle(t.Context(), conflict)
			if !errors.Is(err, localstore.ErrCommandConflict) {
				t.Fatalf("conflict error=%v, want ErrCommandConflict", err)
			}
		})
	}
}

func TestDurableCommandIdentityRedactsInlineAttachmentData(t *testing.T) {
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, ComputerID: "cmp-redact", ExpiresAt: time.Now().UTC(),
		Name: "run.start", Payload: json.RawMessage(`{
			"run_id":"run-redact","project_id":"project-redact","lease_epoch":1,"provider":"fake",
			"attachments":[{"mime_type":"image/png","filename":"capture.png","data_url":"data:image/png;base64,secret"}]
		}`),
	}
	encoded, err := marshalDurableCommandIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	var identity durableCommandIdentity
	if err := json.Unmarshal(encoded, &identity); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(identity.Payload), "data:image") || !strings.Contains(string(identity.Payload), "capture.png") ||
		!strings.Contains(string(identity.Payload), "data_sha256") || identity.PayloadFingerprint == "" {
		t.Fatalf("durable identity was not safely projected: %s", encoded)
	}
}

func TestDurableCommandIdentityRedactsUnexpectedInlineImageStrings(t *testing.T) {
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, ComputerID: "cmp-redact", ExpiresAt: time.Now().UTC(),
		Name: "run.start", Payload: json.RawMessage(`{
			"run_id":"run-redact","project_id":"project-redact","lease_epoch":1,"provider":"fake",
			"unexpected":"provider data:image/png;base64,secret"
		}`),
	}
	encoded, err := marshalDurableCommandIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "data:image") || !strings.Contains(string(encoded), "payload_fingerprint") {
		t.Fatalf("durable identity retained unexpected inline image data: %s", encoded)
	}
}

func TestDurableAttachmentIdentityChangesWithImageBytes(t *testing.T) {
	base := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, ComputerID: "cmp-redact", ExpiresAt: time.Now().UTC(),
		Name: "run.start",
	}
	base.Payload = json.RawMessage(`{"attachments":[{"mime_type":"image/png","filename":"capture.png","data_url":"data:image/png;base64,one"}]}`)
	first, err := marshalDurableCommandIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Payload = json.RawMessage(`{"attachments":[{"mime_type":"image/png","filename":"capture.png","data_url":"data:image/png;base64,two"}]}`)
	second, err := marshalDurableCommandIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || strings.Contains(string(first), "data:image") || strings.Contains(string(second), "data:image") {
		t.Fatalf("attachment identities first=%s second=%s", first, second)
	}
}

func TestRuntimeEventProjectionDropsProviderEchoedInlineImageData(t *testing.T) {
	event := redactInlineImageDataFromEvent(adapter.Event{
		Type: "provider.event",
		Payload: map[string]any{
			"part": map[string]any{
				"type": "file", "url": "data:image/png;base64,secret",
				"nested": []any{map[string]any{"data_url": "data:image/png;base64,secret"}},
			},
			"text": "provider echoed data:image/png;base64,secret",
		},
		ProviderRaw: json.RawMessage(`{"url":"data:image/png;base64,secret"}`),
	})
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "data:image") || event.ProviderRaw != nil {
		t.Fatalf("runtime event retained inline image data: %s", encoded)
	}
	part, _ := event.Payload["part"].(map[string]any)
	if part["type"] != "file" || part["url"] != nil {
		t.Fatalf("event metadata projection=%+v", event.Payload)
	}
}

func TestSuccessfulRunPersistsTerminalBeforeCommandCompletes(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{skipTerminal: true}
	engine := New("cmp-terminal", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)
	payload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-terminal", ProjectID: "project-terminal", IssueAgentInstanceID: "instance-terminal",
		IssueAgentGenerationID: "generation-terminal", LeaseEpoch: 1,
		Provider: "fake", Prompt: "work", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-terminal", Name: "run.start",
		ComputerID: "cmp-terminal", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, err)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 2 || events[1].Type != "run.finished" || events[1].Payload["status"] != "completed" {
		t.Fatalf("terminal events=%+v err=%v", events, err)
	}
}

func TestAdapterErrorAfterPersistedTerminalDoesNotAppendSecondTerminal(t *testing.T) {
	tests := []struct {
		name           string
		providerID     string
		terminal       adapter.Event
		wantStatus     string
		wantOutcome    persistedTerminalOutcome
		wantAdapterErr bool
	}{
		{
			name: "Codex nested completed", providerID: "codex",
			terminal: adapter.Event{
				Type: "run.completed", Payload: map[string]any{
					"turn": map[string]any{"status": "completed"},
				},
			},
			wantStatus: "completed", wantOutcome: terminalOutcomeSucceeded,
		},
		{
			name: "normalized finished succeeded",
			terminal: adapter.Event{
				Type: "run.finished", Payload: map[string]any{"status": "succeeded"},
			},
			wantStatus: "completed", wantOutcome: terminalOutcomeSucceeded,
		},
		{
			name:       "foreign nested turn is not a Codex terminal",
			terminal:   adapter.Event{Type: "run.completed", Payload: map[string]any{"turn": map[string]any{"status": "completed"}}},
			wantStatus: "failed", wantOutcome: terminalOutcomeFailed, wantAdapterErr: true,
		},
		{
			name: "run finished failed",
			terminal: adapter.Event{
				Type: "run.finished", Payload: map[string]any{"status": "failed", "reason": "provider_failed"},
			},
			wantStatus: "failed", wantOutcome: terminalOutcomeFailed, wantAdapterErr: true,
		},
		{
			name:       "fatal run error is already a terminal",
			terminal:   adapter.Event{Type: "run.error", Payload: map[string]any{"message": "provider rejected model"}},
			wantStatus: "failed", wantOutcome: terminalOutcomeFailed, wantAdapterErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			providerErr := errors.New("adapter returned after terminal")
			providerID := test.providerID
			if providerID == "" {
				providerID = "fake"
			}
			provider := &fakeAdapter{terminalEvent: &test.terminal, afterTerminal: providerErr}
			engine := New("cmp-terminal-error", store, nil, t.TempDir(), nil)
			defer engine.Close()
			engine.RegisterTestAdapter(providerID, provider)
			payload, err := json.Marshal(nodeprotocol.StartRunPayload{
				RunID: "run-terminal-error", ProjectID: "project-terminal-error", IssueAgentInstanceID: "instance-terminal-error",
				IssueAgentGenerationID: "generation-terminal-error", LeaseEpoch: 1,
				Provider: providerID, Prompt: "work", DisableTools: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
				ProtocolVersion: nodeprotocol.Version, MessageID: "command-terminal-error", Name: "run.start",
				ComputerID: "cmp-terminal-error", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
			})
			if replayed || record.Status != test.wantStatus {
				t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, err)
			}
			if test.wantAdapterErr != errors.Is(err, providerErr) {
				t.Fatalf("err=%v, want adapter error=%t", err, test.wantAdapterErr)
			}
			stored, getErr := store.GetCommand(t.Context(), "command-terminal-error")
			if getErr != nil || stored.Status != test.wantStatus || stored.ErrorCode != record.ErrorCode {
				t.Fatalf("persisted command=%+v err=%v, returned=%+v", stored, getErr, record)
			}
			events, err := store.UnackedEvents(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			terminalEvents := make([]adapter.Event, 0, 2)
			for _, event := range events {
				if event.Type == "run.completed" || event.Type == "run.finished" || event.Type == "run.error" {
					terminalEvents = append(terminalEvents, event)
				}
			}
			if len(terminalEvents) != 1 || terminalEvents[0].Type != test.terminal.Type {
				t.Fatalf("terminal events=%+v, all events=%+v", terminalEvents, events)
			}
			outcome, terminal := normalizeTerminalOutcome(terminalEvents[0])
			if !terminal || outcome != test.wantOutcome {
				t.Fatalf("terminal outcome=%v terminal=%t event=%+v", outcome, terminal, terminalEvents[0])
			}
		})
	}
}

func TestSinkIgnoresOutputDeltaAfterAckedTerminal(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	engine := New("cmp-late-output", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)
	payload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-late-output", ProjectID: "project-late-output", IssueAgentInstanceID: "instance-late-output",
		IssueAgentGenerationID: "generation-late-output", LeaseEpoch: 1,
		Provider: "fake", Prompt: "work", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-late-output", Name: "run.start",
		ComputerID: "cmp-late-output", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, err)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 2 || events[1].Type != "run.completed" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	terminal := events[1]
	if err := store.AckEventsThrough(t.Context(), []localstore.EventCursor{{
		RunID: terminal.RunID, LeaseEpoch: terminal.LeaseEpoch, Sequence: terminal.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	lateSink := provider.sink
	provider.mu.Unlock()
	if lateSink == nil {
		t.Fatal("adapter sink was not captured")
	}
	if err := lateSink(t.Context(), adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "must be ignored"},
	}); err != nil {
		t.Fatalf("late output returned error: %v", err)
	}
	remaining, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("late output reopened the durable sequence: events=%+v err=%v", remaining, err)
	}
}

func TestSuccessfulTerminalKeepsCommandCompletedWhenSessionPersistenceFails(t *testing.T) {
	tests := []struct {
		name  string
		table string
	}{
		{name: "native session", table: "native_sessions"},
		{name: "generation session", table: "conversation_sessions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "runtime.db")
			store, err := localstore.Open(t.Context(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			installRuntimeSessionFailureTrigger(t, databasePath, test.table)
			provider := &fakeAdapter{}
			engine := New("cmp-session-failure", store, nil, t.TempDir(), nil)
			defer engine.Close()
			engine.RegisterTestAdapter("fake", provider)
			key := InstanceKey{
				InstanceID: "instance-session-failure", Generation: "generation-session-failure",
				ProjectID: "project-session-failure",
			}
			_, _, key.GitScopeHash, err = engine.prepareProjectWorkspace(key.ProjectID, nil)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(nodeprotocol.StartRunPayload{
				RunID: "run-session-failure", ProjectID: key.ProjectID,
				IssueAgentInstanceID: key.InstanceID, IssueAgentGenerationID: key.Generation,
				LeaseEpoch: 1, Provider: "fake", Prompt: "work", DisableTools: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
				ProtocolVersion: nodeprotocol.Version, MessageID: "command-session-failure", Name: "run.start",
				ComputerID: "cmp-session-failure", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
			})
			if err != nil || replayed || record.Status != "completed" || record.ErrorCode != "" {
				t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, err)
			}
			events, err := store.UnackedEvents(t.Context(), 10)
			if err != nil || len(events) != 2 || events[1].Type != "run.completed" {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			outcome, terminal := normalizeTerminalOutcome(events[1])
			if !terminal || outcome != terminalOutcomeSucceeded {
				t.Fatalf("successful event changed after session failure: %+v", events[1])
			}
			engine.managersMu.Lock()
			manager := engine.managers["fake"]
			engine.managersMu.Unlock()
			if manager == nil {
				t.Fatal("test manager is missing")
			}
			manifest, err := readInstanceManifest(manager.instanceDir(key))
			if err != nil || manifest.SessionPersisted {
				t.Fatalf("uncommitted config was not retained: manifest=%+v err=%v", manifest, err)
			}
		})
	}
}

func installRuntimeSessionFailureTrigger(t *testing.T, databasePath, table string) {
	t.Helper()
	var statement string
	switch table {
	case "native_sessions":
		statement = `CREATE TRIGGER fail_native_session BEFORE INSERT ON native_sessions
			BEGIN SELECT RAISE(FAIL, 'injected native session failure'); END`
	case "conversation_sessions":
		statement = `CREATE TRIGGER fail_generation_session BEFORE INSERT ON conversation_sessions
			BEGIN SELECT RAISE(FAIL, 'injected generation session failure'); END`
	default:
		t.Fatalf("unsupported failure table %q", table)
	}
	raw, err := sql.Open("sqlite", "file:"+databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), statement); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeClosePreventsNewHandlesAndManagers(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp_close", store, nil, t.TempDir(), nil)
	provider := &fakeAdapter{}
	engine.RegisterTestAdapter("fake", provider)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_after_close", ProjectID: "project_after_close", IssueAgentInstanceID: "instance_after_close",
		IssueAgentGenerationID: "generation_after_close", LeaseEpoch: 1,
		Provider: "fake", Prompt: "must not start",
	})
	_, _, err = engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_after_close", Name: "run.start",
		ComputerID: "cmp_close", SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: payload,
	})
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code != "runtime_closed" {
		t.Fatalf("Handle after Close error = %v, want runtime_closed", err)
	}
	if _, err := engine.managerFor("new-provider", "new-provider", t.TempDir()); err == nil {
		t.Fatal("managerFor created a provider after Close")
	}
	engine.managersMu.Lock()
	managerCount := len(engine.managers)
	engine.managersMu.Unlock()
	if managerCount != 0 {
		t.Fatalf("managers created after Close: %d", managerCount)
	}
	_, err = engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{ProjectID: "project_after_close"})
	var gitReadError *nodeprotocol.GitReadError
	if !errors.As(err, &gitReadError) || gitReadError.Code != "runtime_closed" {
		t.Fatalf("Git read after Close error = %v, want runtime_closed", err)
	}
}

func TestRunStopCancelsBlockedPreparationBeforeProviderStart(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &blockingPreparationAdapter{
		fakeAdapter:     &fakeAdapter{},
		verifyStarted:   make(chan struct{}),
		verifyRelease:   make(chan struct{}),
		interruptCalled: make(chan struct{}, 1),
	}
	engine := New("cmp-stop-preparation", store, nil, t.TempDir(), nil)
	defer engine.Close()
	var releaseVerifyOnce sync.Once
	releaseVerify := func() {
		releaseVerifyOnce.Do(func() { close(provider.verifyRelease) })
	}
	defer releaseVerify()
	engine.RegisterTestAdapter("fake", provider)
	key := InstanceKey{
		InstanceID: "instance-stop-preparation", Generation: "generation-stop-preparation",
		ProjectID: "project-stop-preparation",
	}
	_, _, key.GitScopeHash, err = engine.prepareProjectWorkspace(key.ProjectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.saveGenerationSession(t.Context(), key, "fake", "native_1"); err != nil {
		t.Fatal(err)
	}
	startPayload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-stop-preparation", ProjectID: key.ProjectID,
		IssueAgentInstanceID: key.InstanceID, IssueAgentGenerationID: key.Generation,
		LeaseEpoch: 1, Provider: "fake", Prompt: "must not start", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	startCommand := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-stop-preparation-start", Name: "run.start",
		ComputerID: "cmp-stop-preparation", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: startPayload,
	}
	type handleResult struct {
		record   localstore.CommandRecord
		replayed bool
		err      error
	}
	startDone := make(chan handleResult, 1)
	go func() {
		record, replayed, handleErr := engine.Handle(context.Background(), startCommand)
		startDone <- handleResult{record: record, replayed: replayed, err: handleErr}
	}()
	select {
	case <-provider.verifyStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("run.start did not reach the blocked preparation stage")
	}

	// The registry enforces one live start per exact run ID without retaining a
	// completed-ID tombstone.
	duplicate := startCommand
	duplicate.MessageID = "command-stop-preparation-duplicate"
	duplicateRecord, duplicateReplayed, duplicateErr := engine.Handle(t.Context(), duplicate)
	if duplicateReplayed || duplicateRecord.Status != "failed" || errorCode(duplicateErr) != "run_already_active" {
		t.Fatalf("duplicate record=%+v replayed=%t err=%v", duplicateRecord, duplicateReplayed, duplicateErr)
	}

	stopPayload, err := json.Marshal(nodeprotocol.StopRunPayload{
		RunID: "run-stop-preparation", LeaseEpoch: 1, Provider: "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	stopCommand := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-stop-preparation-stop", Name: "run.stop",
		ComputerID: "cmp-stop-preparation", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: stopPayload,
	}
	stopRecord, stopReplayed, stopErr := engine.Handle(t.Context(), stopCommand)
	if stopErr != nil || stopReplayed || stopRecord.Status != "completed" {
		t.Fatalf("stop record=%+v replayed=%t err=%v", stopRecord, stopReplayed, stopErr)
	}
	select {
	case <-provider.interruptCalled:
	case <-time.After(time.Second):
		t.Fatal("run.stop did not attempt adapter interrupt after canceling preparation")
	}
	releaseVerify()

	var startResult handleResult
	select {
	case startResult = <-startDone:
	case <-time.After(10 * time.Second):
		t.Fatal("canceled run.start did not return")
	}
	if startResult.replayed || startResult.record.Status != "failed" || !errors.Is(startResult.err, context.Canceled) {
		t.Fatalf("start record=%+v replayed=%t err=%v", startResult.record, startResult.replayed, startResult.err)
	}
	provider.fakeAdapter.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.fakeAdapter.mu.Unlock()
	provider.mu.Lock()
	interrupts, sawCanceled := provider.interrupts, provider.interruptSawCanceled
	provider.mu.Unlock()
	if starts != 0 || resumes != 0 || interrupts != 1 || !sawCanceled {
		t.Fatalf("starts=%d resumes=%d interrupts=%d interruptSawCanceled=%t", starts, resumes, interrupts, sawCanceled)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("pre-provider cancellation produced inconsistent run events: events=%+v err=%v", events, err)
	}
	engine.runStartsMu.Lock()
	registeredStarts := len(engine.runStarts)
	engine.runStartsMu.Unlock()
	if registeredStarts != 0 {
		t.Fatalf("completed start left %d cancel registrations", registeredStarts)
	}

	// Once both live sources are gone, stop is genuinely not found; the prior
	// cancellation is not retained as an unbounded tombstone.
	stopAgain := stopCommand
	stopAgain.MessageID = "command-stop-preparation-stop-again"
	missingRecord, missingReplayed, missingErr := engine.Handle(t.Context(), stopAgain)
	if missingReplayed || missingRecord.Status != "failed" || errorCode(missingErr) != "run_instance_not_found" {
		t.Fatalf("second stop record=%+v replayed=%t err=%v", missingRecord, missingReplayed, missingErr)
	}
}

func TestExpiredAndWrongComputerCommandsNeverEnterInbox(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New("cmp_1", store, nil, t.TempDir(), nil)
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_expired", Name: "agent.probe",
		ComputerID: "cmp_1", SentAt: time.Now().UTC().Add(-time.Minute),
		ExpiresAt: time.Now().UTC().Add(-time.Second), Payload: json.RawMessage(`{"provider":"fake"}`),
	}
	if _, _, err := runtime.Handle(t.Context(), command); errorCode(err) != "command_expired" {
		t.Fatalf("expected command_expired, got %v", err)
	}
	command.ExpiresAt = time.Now().UTC().Add(time.Minute)
	command.ComputerID = "cmp_other"
	if _, _, err := runtime.Handle(t.Context(), command); errorCode(err) != "computer_mismatch" {
		t.Fatalf("expected computer_mismatch, got %v", err)
	}
}

func TestAgentDiagnoseReturnsStructuredResult(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	engine := New("cmp_1", store, []adapter.Adapter{provider}, t.TempDir(), nil)
	payload, _ := json.Marshal(nodeprotocol.DiagnoseProviderPayload{Provider: "fake"})
	now := time.Now().UTC()
	record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_diagnose", Name: "agent.diagnose",
		ComputerID: "cmp_1", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("record=%+v replayed=%v err=%v", record, replayed, err)
	}
	var diagnostic nodeprotocol.ProviderDiagnostic
	if err := json.Unmarshal(record.Result, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Provider != "fake" || diagnostic.Status != "needs_auth" || len(diagnostic.Steps) != 1 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	if diagnostic.Steps[0].ID != "probe" || diagnostic.Steps[0].Status != "success" {
		t.Fatalf("steps = %+v", diagnostic.Steps)
	}
}

// A diagnose run replaces the stored AgentService capabilities. It must keep
// the catalog metadata flags (read_only, file_bridge, ...) that the startup
// probe produced, otherwise provider pickers in the UI lose every entry.
func TestAgentDiagnoseKeepsCatalogCapabilities(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{name: "codex"}
	engine := New("cmp_1", store, []adapter.Adapter{provider}, t.TempDir(), nil)
	payload, _ := json.Marshal(nodeprotocol.DiagnoseProviderPayload{Provider: "codex"})
	now := time.Now().UTC()
	record, _, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_diagnose_caps", Name: "agent.diagnose",
		ComputerID: "cmp_1", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if err != nil || record.Status != "completed" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	var diagnostic nodeprotocol.ProviderDiagnostic
	if err := json.Unmarshal(record.Result, &diagnostic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"streaming", "read_only", "file_bridge", "display_name", "command", "diagnostic_steps", "provider_api_version"} {
		if _, ok := diagnostic.Capabilities[key]; !ok {
			t.Fatalf("diagnose dropped %q: %+v", key, diagnostic.Capabilities)
		}
	}
	if diagnostic.Capabilities["read_only"] != true || diagnostic.Capabilities["file_bridge"] != true ||
		diagnostic.Capabilities["display_name"] != "Codex" {
		t.Fatalf("diagnose capabilities = %+v", diagnostic.Capabilities)
	}
}

func TestRunStartUsesStableProjectWorkspaceWithoutGitResources(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	workspaceRoot := t.TempDir()
	runtime := New("cmp_1", store, nil, workspaceRoot, nil)
	runtime.RegisterTestAdapter("fake", provider)
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_nocwd", IssueID: "issue_1",
		IssueAgentInstanceID: "instance_2", IssueAgentGenerationID: "gen_2",
		ProjectID:  "proj_1",
		LeaseEpoch: 1, Provider: "fake", Prompt: "hello", ReadOnly: true,
	})
	command := nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_nocwd", Name: "run.start",
		ComputerID: "cmp_1",
		SentAt:     time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: payload,
	}
	record, replayed, err := runtime.Handle(t.Context(), command)
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("run without resource: record=%+v replayed=%v err=%v", record, replayed, err)
	}
	provider.mu.Lock()
	cwd := provider.lastCWD
	provider.mu.Unlock()
	want, _, _, workspaceErr := runtime.prepareProjectWorkspace("proj_1", nil)
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	if cwd != want {
		t.Fatalf("expected stable project CWD %q, got %q", want, cwd)
	}
	if info, statErr := os.Stat(want); statErr != nil || !info.IsDir() {
		t.Fatalf("stable project workspace directory was not created: %v", statErr)
	}
}

func TestProjectWorkspaceManifestAtomicRewriteHasNoMissingWindow(t *testing.T) {
	engine := New("cmp-workspace-atomic", nil, nil, t.TempDir(), nil)
	defer engine.cancel()
	repository := t.TempDir()
	scope := gitread.RepositoryExecutionScope{
		Root: repository, CWD: repository, WritableRoots: []string{repository}, Full: true,
		Hash: "scope-atomic",
	}
	initial := []projectWorkspaceRepository{{
		ID: "repository-atomic", Name: "Initial", RootDisplay: "Initial",
		Paths: []string{repository}, Scope: scope,
	}}
	updated := []projectWorkspaceRepository{{
		ID: "repository-atomic", Name: "Updated", RootDisplay: "Updated",
		Paths: []string{repository}, Scope: scope,
	}}
	workspace, _, _, err := engine.prepareProjectWorkspace("project-atomic", initial)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(workspace, "AGENTS.md")
	allowed := map[string]struct{}{
		projectWorkspaceInstructions(initial): {},
		projectWorkspaceInstructions(updated): {},
	}
	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readErr <- nil
				return
			default:
			}
			payload, readManifestErr := os.ReadFile(manifestPath)
			if readManifestErr != nil {
				readErr <- readManifestErr
				return
			}
			if _, ok := allowed[string(payload)]; !ok {
				readErr <- fmt.Errorf("observed partial Workspace manifest")
				return
			}
		}
	}()
	for index := 0; index < 100; index++ {
		repositories := initial
		if index%2 == 1 {
			repositories = updated
		}
		if _, _, _, err := engine.prepareProjectWorkspace("project-atomic", repositories); err != nil {
			close(stop)
			<-readErr
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
}

func TestRunStartRequiresProjectAndRejectsLegacyWorkspaceSelectors(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-strict-workspace", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", &fakeAdapter{})
	for index, raw := range []string{
		`{"run_id":"run-missing-project","lease_epoch":1,"provider":"fake","prompt":"work"}`,
		`{"run_id":"run-legacy-cwd","project_id":"project-strict","lease_epoch":1,"provider":"fake","prompt":"work","cwd":"/legacy"}`,
		`{"run_id":"run-legacy-path","project_id":"project-strict","lease_epoch":1,"provider":"fake","prompt":"work","path":"token"}`,
	} {
		now := time.Now().UTC()
		_, _, handleErr := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: fmt.Sprintf("command-strict-%d", index), Name: "run.start",
			ComputerID: "cmp-strict-workspace", SentAt: now, ExpiresAt: now.Add(time.Minute),
			Payload: json.RawMessage(raw),
		})
		if errorCode(handleErr) != "invalid_command" {
			t.Fatalf("strict payload %d error=%v", index, handleErr)
		}
	}
}

func TestDirectoryListingIsCanonicalAndBounded(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 300; index++ {
		if err := os.Mkdir(filepath.Join(home, fmt.Sprintf("directory-%03d", index)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	engine := &Runtime{}
	items, target, canonicalHome, err := engine.listDirectories(t.Context(), home, "", true)
	wantHome, canonicalErr := filepath.EvalSymlinks(home)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || len(items) > 200 || target != wantHome || canonicalHome != wantHome {
		t.Fatalf("items=%d target=%q home=%q err=%v", len(items), target, canonicalHome, err)
	}
	for _, item := range items {
		if item.Name == "escape" {
			t.Fatalf("symlinked directory escaped listing boundary: %+v", item)
		}
	}
	if _, _, _, err := engine.listDirectories(t.Context(), filepath.Join(home, "escape"), "", true); err == nil {
		t.Fatal("symlinked path outside HOME was accepted")
	}
}

func TestResourceValidateUsesBoundedGitDiscovery(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-resource-validation", store, nil, t.TempDir(), nil)
	defer engine.Close()
	base := t.TempDir()

	plainContainer := filepath.Join(base, "plain-container")
	nestedRepository := filepath.Join(plainContainer, "level-one", "nested-repository")
	if err := os.MkdirAll(nestedRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, nestedRepository)
	deepContainer := filepath.Join(base, "deep-container")
	deepRepository := filepath.Join(deepContainer, "level-one", "level-two", "level-three")
	if err := os.MkdirAll(deepRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, deepRepository)

	ancestorRepository := filepath.Join(base, "ancestor-repository")
	partialScope := filepath.Join(ancestorRepository, "scope", "source")
	if err := os.MkdirAll(partialScope, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, ancestorRepository)

	pureDirectory := filepath.Join(base, "pure-directory")
	if err := os.MkdirAll(filepath.Join(pureDirectory, "one", "two"), 0o700); err != nil {
		t.Fatal(err)
	}

	highFanout := filepath.Join(base, "high-fanout")
	if err := os.MkdirAll(highFanout, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1024; index++ {
		if err := os.Mkdir(filepath.Join(highFanout, fmt.Sprintf("directory-%04d", index)), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	externalRepository := filepath.Join(base, "external-repository")
	symlinkContainer := filepath.Join(base, "symlink-container")
	if err := os.MkdirAll(symlinkContainer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(externalRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, externalRepository)
	symlinkPath := filepath.Join(symlinkContainer, "linked-repository")
	symlinkErr := os.Symlink(externalRepository, symlinkPath)

	validate := func(t *testing.T, id, path string, expected bool) {
		t.Helper()
		payload, marshalErr := json.Marshal(nodeprotocol.ValidateResourcePayload{
			ResourceID:        id,
			Path:              path,
			ValidationVersion: 1,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		now := time.Now().UTC()
		record, replayed, handleErr := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version,
			MessageID:       "validate-" + id,
			Name:            "resource.validate",
			ComputerID:      "cmp-resource-validation",
			SentAt:          now,
			ExpiresAt:       now.Add(time.Minute),
			Payload:         payload,
		})
		if handleErr != nil || replayed || record.Status != "completed" {
			t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, handleErr)
		}
		var result struct {
			ResourceID  string `json:"resource_id"`
			Valid       bool   `json:"valid"`
			GitDetected bool   `json:"git_detected"`
		}
		if unmarshalErr := json.Unmarshal(record.Result, &result); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		if result.ResourceID != id || !result.Valid || result.GitDetected != expected {
			t.Fatalf("validation result=%+v want git_detected=%t", result, expected)
		}
	}

	t.Run("plain container finds depth-two nested repository", func(t *testing.T) {
		validate(t, "nested", plainContainer, true)
	})
	t.Run("resource repository is detected directly", func(t *testing.T) {
		validate(t, "direct", nestedRepository, true)
	})
	t.Run("partial resource finds ancestor repository", func(t *testing.T) {
		validate(t, "ancestor", partialScope, true)
	})
	t.Run("nested scan stops after depth two", func(t *testing.T) {
		validate(t, "deep", deepContainer, false)
	})
	t.Run("pure directory remains non-Git", func(t *testing.T) {
		validate(t, "plain", pureDirectory, false)
	})
	t.Run("nested symlink is not followed", func(t *testing.T) {
		if symlinkErr != nil {
			t.Skipf("symlink fixture unavailable: %v", symlinkErr)
		}
		validate(t, "symlink", symlinkContainer, false)
	})
	t.Run("high fanout remains bounded", func(t *testing.T) {
		validate(t, "fanout", highFanout, false)
	})
}

func TestRunStartUsesStableWorkspaceAndListsAllRepositories(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "root")
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, root)
	initRuntimeGitRepository(t, nested)
	provider := &fakeAdapter{}
	engine := New("cmp_git", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "resource-root", Name: "Code", Path: root}}
	approveRuntimeGitResources(t, engine, resources)
	repositories, err := engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-git", Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	var nestedID string
	for _, repository := range repositories.Repositories {
		if repository.RootDisplay == "Code/nested" {
			nestedID = repository.ID
		}
	}
	if nestedID == "" {
		t.Fatalf("nested repository not discovered: %+v", repositories.Repositories)
	}
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_nested", IssueID: "issue_git", ProjectID: "project-git",
		GitResources:         resources,
		IssueAgentInstanceID: "instance_git", IssueAgentGenerationID: "generation_git",
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", ReadOnly: true,
	})
	record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command_nested", Name: "run.start",
		ComputerID: "cmp_git", SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: payload,
	})
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("nested run record=%+v replayed=%t err=%v", record, replayed, err)
	}
	provider.mu.Lock()
	cwd := provider.lastCWD
	provider.mu.Unlock()
	_, resolved, err := engine.resolveProjectWorkspaceRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-git", Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, _, _, err := engine.prepareProjectWorkspace("project-git", resolved)
	if err != nil {
		t.Fatal(err)
	}
	if cwd != workspace {
		t.Fatalf("project workspace CWD=%q want %q", cwd, workspace)
	}
	manifest, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	rootCanonical, _ := filepath.EvalSymlinks(root)
	nestedCanonical, _ := filepath.EvalSymlinks(nested)
	if !strings.Contains(string(manifest), filepath.ToSlash(rootCanonical)) ||
		!strings.Contains(string(manifest), filepath.ToSlash(nestedCanonical)) {
		t.Fatalf("workspace manifest does not list both repositories: %s", manifest)
	}
}

func TestRunStartAuthorizesExactPartialResourceRoot(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "repository")
	scopeA := filepath.Join(root, "scope-a")
	if err := os.MkdirAll(scopeA, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, root)
	gitRuntimeCommand(t, root, "config", "user.name", "Scope Test")
	gitRuntimeCommand(t, root, "config", "user.email", "scope@example.invalid")
	if err := os.WriteFile(filepath.Join(scopeA, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRuntimeCommand(t, root, "add", "--", ".")
	gitRuntimeCommand(t, root, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(scopeA, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &fakeAdapter{}
	engine := New("cmp-partial-scope", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)

	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "resource-scope", Name: "Scoped", Path: scopeA}}
	approveRuntimeGitResources(t, engine, resources)
	list, listErr := engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-one", Resources: resources,
	})
	if listErr != nil || len(list.Repositories) != 1 || list.Repositories[0].Scope != "resource_prefix" {
		t.Fatalf("repositories=%+v err=%v", list, listErr)
	}
	// Changes remains available for this exact partial scope.
	changes, changesErr := engine.ListGitChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "project-one", RepositoryID: list.Repositories[0].ID, Resources: resources,
		Area: "unstaged", Limit: 10,
	})
	if changesErr != nil || len(changes.Items) != 1 {
		t.Fatalf("partial-scope Changes unexpectedly failed: %v", changesErr)
	}
	if _, diffErr := engine.ReadGitDiff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "project-one", RepositoryID: list.Repositories[0].ID, Resources: resources,
		ChangeID: changes.Items[0].ID, Context: 3,
	}); diffErr != nil {
		t.Fatalf("partial-scope Diff unexpectedly failed: %v", diffErr)
	}
	payload, marshalErr := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-scope-a", ProjectID: "project-one",
		GitResources:         resources,
		IssueAgentInstanceID: "shared-instance", IssueAgentGenerationID: "shared-generation",
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", NativeSessionID: "native_1", DisableTools: true,
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	now := time.Now().UTC()
	record, replayed, handleErr := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-run-scope-a", Name: "run.start",
		ComputerID: "cmp-partial-scope", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if handleErr != nil || replayed || record.Status != "completed" {
		t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, handleErr)
	}
	provider.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.mu.Unlock()
	if starts != 1 || resumes != 0 {
		t.Fatalf("partial workspace provider calls: starts=%d resumes=%d", starts, resumes)
	}
	engine.managersMu.Lock()
	manager := engine.managers["fake"]
	engine.managersMu.Unlock()
	if manager == nil || manager.InstanceCount() != 1 {
		t.Fatalf("partial run did not retain its workspace instance: %v", manager)
	}
	provider.mu.Lock()
	workspace := provider.lastCWD
	provider.mu.Unlock()
	manifest, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	scopeCanonical, _ := filepath.EvalSymlinks(scopeA)
	rootCanonical, _ := filepath.EvalSymlinks(root)
	if !strings.Contains(string(manifest), filepath.ToSlash(scopeCanonical)) ||
		strings.Contains(string(manifest), `"writable_roots": [\n        "`+filepath.ToSlash(rootCanonical)+`"`) {
		t.Fatalf("partial authorization widened beyond resource root: %s", manifest)
	}
}

func TestRuntimeCapturesRunGitSnapshotsBeforeAndAfterSuccessfulProviderTurn(t *testing.T) {
	store, engine, provider, root, _, resources := newRuntimeSnapshotFixture(t, "success")
	defer store.Close()
	defer engine.Close()
	provider.onStart = func(adapter.RunRequest) {
		if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after provider\n"), 0o600); err != nil {
			t.Errorf("provider mutation: %v", err)
		}
	}
	runID := "run-snapshot-success"
	record, replayed, err := engine.Handle(t.Context(), runtimeStartCommand(t, runID, resources))
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("record=%+v replayed=%t err=%v", record, replayed, err)
	}
	assertRuntimeStartEndSnapshots(t, engine, runID, false)
}

func TestRuntimeCapturesEndSnapshotWhenProviderFails(t *testing.T) {
	store, engine, provider, root, _, resources := newRuntimeSnapshotFixture(t, "failure")
	defer store.Close()
	defer engine.Close()
	provider.onStart = func(adapter.RunRequest) {
		if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed before failure\n"), 0o600); err != nil {
			t.Errorf("provider mutation: %v", err)
		}
	}
	provider.startErr = errors.New("provider failed")
	runID := "run-snapshot-failure"
	if _, _, err := engine.Handle(t.Context(), runtimeStartCommand(t, runID, resources)); err == nil {
		t.Fatal("provider failure was not returned")
	}
	assertRuntimeStartEndSnapshots(t, engine, runID, true)
}

func TestRuntimeDoesNotCaptureGitSnapshotWithoutSelectedRepository(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	engine := New("cmp-no-snapshot", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-no-repository", ProjectID: "project-no-repository",
		IssueAgentInstanceID: "instance-no-repository", IssueAgentGenerationID: "generation-no-repository",
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", DisableTools: true,
	})
	now := time.Now().UTC()
	_, _, err = engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-no-repository", Name: "run.start",
		ComputerID: "cmp-no-snapshot", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := engine.GetRunGitSnapshots(t.Context(), "run-no-repository")
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
}

func TestRuntimePersistsMetadataOnlySnapshotWhenCaptureFails(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-capture-failure", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.captureRunGitSnapshots(engine.gitReader, t.Context(), nodeprotocol.StartRunPayload{
		RunID: "run-capture-failure", ProjectID: "project-capture-failure",
	}, []projectWorkspaceRepository{{ID: "repository-missing"}}, "end", 2*time.Second)
	snapshots, err := store.GetRunGitSnapshots(t.Context(), "run-capture-failure")
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if !snapshots[0].Truncated || len(snapshots[0].Warnings) != 1 ||
		snapshots[0].Warnings[0].Code != "snapshot_capture_failed" || snapshots[0].ContentHash == "" {
		t.Fatalf("capture failure was indistinguishable from clean snapshot: %+v", snapshots[0])
	}
}

func TestRuntimePersistsFallbackForEveryRepositoryAfterSnapshotTimeout(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-capture-timeout", store, nil, t.TempDir(), nil)
	defer engine.Close()
	repositories := make([]projectWorkspaceRepository, 0, nodeprotocol.MaxGitRepositoriesPerRun)
	for index := 0; index < nodeprotocol.MaxGitRepositoriesPerRun; index++ {
		repositories = append(repositories, projectWorkspaceRepository{
			ID: fmt.Sprintf("repository-timeout-%03d", index),
		})
	}
	engine.captureRunGitSnapshots(engine.gitReader, t.Context(), nodeprotocol.StartRunPayload{
		RunID: "run-capture-timeout", ProjectID: "project-capture-timeout",
	}, repositories, "end", 0)
	snapshots, err := store.GetRunGitSnapshots(t.Context(), "run-capture-timeout")
	if err != nil || len(snapshots) != nodeprotocol.MaxGitRepositoriesPerRun {
		t.Fatalf("timeout snapshots=%d err=%v", len(snapshots), err)
	}
	for _, snapshot := range snapshots {
		if !snapshot.Truncated || len(snapshot.Warnings) != 1 ||
			snapshot.Warnings[0].Code != nodeprotocol.GitWarningSnapshotCaptureFailed {
			t.Fatalf("timeout fallback=%+v", snapshot)
		}
	}
}

func TestRuntimeCloseCancelsProviderThenPersistsBoundedEndSnapshot(t *testing.T) {
	store, engine, provider, _, _, resources := newRuntimeSnapshotFixture(t, "close")
	defer store.Close()
	started := make(chan struct{})
	provider.mu.Lock()
	provider.waitForCancel = true
	provider.onStart = func(adapter.RunRequest) { close(started) }
	provider.mu.Unlock()
	handleDone := make(chan error, 1)
	go func() {
		_, _, err := engine.Handle(context.Background(), runtimeStartCommand(t,
			"run-snapshot-close", resources))
		handleDone <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not start")
	}
	closedAt := time.Now()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closedAt); elapsed > 8*time.Second {
		t.Fatalf("Runtime.Close took %s", elapsed)
	}
	select {
	case <-handleDone:
	case <-time.After(time.Second):
		t.Fatal("active Handle survived Runtime.Close")
	}
	engine.runStartsMu.Lock()
	registeredStarts := len(engine.runStarts)
	engine.runStartsMu.Unlock()
	if registeredStarts != 0 {
		t.Fatalf("Runtime.Close left %d run.start cancel registrations", registeredStarts)
	}
	snapshots, err := store.GetRunGitSnapshots(t.Context(), "run-snapshot-close")
	if err != nil || len(snapshots) != 2 || snapshots[0].Phase != "start" || snapshots[1].Phase != "end" {
		t.Fatalf("Close lost end snapshot: snapshots=%+v err=%v", snapshots, err)
	}
	events, err := store.UnackedEvents(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range events {
		if event.Type == "run.finished" {
			terminals++
			if event.Payload["status"] != "cancelled" || event.Payload["reason"] != "interrupted" {
				t.Fatalf("cancellation reported as provider failure: %v", event.Payload)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("cancellation terminals=%d", terminals)
	}
}

func TestRuntimeRevalidatesRepositoryImmediatelyBeforeProviderResume(t *testing.T) {
	store, engine, provider, root, _, resources := newRuntimeSnapshotFixture(t, "replace-before-resume")
	defer store.Close()
	defer engine.Close()
	projectID := "project-snapshot-replace-before-resume"
	reader, workspaceRepositories, err := engine.resolveProjectWorkspaceRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: projectID, Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, workspaceScopeHash, err := engine.prepareProjectWorkspace(projectID, workspaceRepositories)
	if err != nil || reader == nil {
		t.Fatalf("prepare workspace: reader=%v err=%v", reader, err)
	}
	key := InstanceKey{
		InstanceID: "instance-replace-before-resume", Generation: "generation-replace-before-resume",
		ProjectID: projectID, GitScopeHash: workspaceScopeHash,
	}
	if err := engine.saveGenerationSession(t.Context(), key, "fake", "native_1"); err != nil {
		t.Fatal(err)
	}
	moved := root + "-moved"
	provider.mu.Lock()
	provider.onVerify = func() {
		if err := os.Rename(root, moved); err != nil {
			t.Errorf("rename repository: %v", err)
			return
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Errorf("recreate repository root: %v", err)
			return
		}
		initRuntimeGitRepository(t, root)
	}
	provider.mu.Unlock()
	payload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-replace-before-resume", ProjectID: projectID,
		GitResources:         resources,
		IssueAgentInstanceID: key.InstanceID, IssueAgentGenerationID: key.Generation,
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, _, err = engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-replace-before-resume", Name: "run.start",
		ComputerID: "cmp-snapshot-replace-before-resume", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	})
	if code := errorCode(err); code != "repository_not_found" && code != "snapshot_expired" && code != "path_out_of_scope" {
		t.Fatalf("repository replacement error=%v", err)
	}
	provider.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.mu.Unlock()
	if starts != 0 || resumes != 0 {
		t.Fatalf("provider crossed final repository validation: starts=%d resumes=%d", starts, resumes)
	}
}

func TestGenerationSessionAndManagerKeyAreProjectWorkspaceScoped(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := t.TempDir()
	repositoryA := filepath.Join(base, "repository-a")
	repositoryB := filepath.Join(base, "repository-b")
	for _, root := range []string{repositoryA, repositoryB} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		initRuntimeGitRepository(t, root)
	}
	provider := &fakeAdapter{}
	engine := New("cmp-repository-session", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", provider)
	resources := []nodeprotocol.GitResourceRequest{
		{ResourceID: "resource-a", Name: "A", Path: repositoryA},
		{ResourceID: "resource-b", Name: "B", Path: repositoryB},
	}
	approveRuntimeGitResources(t, engine, resources)
	list, err := engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-repository-session", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 2 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	for index := 0; index < 2; index++ {
		runID := fmt.Sprintf("run-repository-session-%d", index)
		nativeSessionHint := ""
		if index == 1 {
			nativeSessionHint = "native_1"
		}
		payload, marshalErr := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: runID, ProjectID: "project-repository-session",
			GitResources:         resources,
			IssueAgentInstanceID: "shared-instance", IssueAgentGenerationID: "shared-generation",
			LeaseEpoch: 1, Provider: "fake", Prompt: "work", NativeSessionID: nativeSessionHint, DisableTools: true,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		now := time.Now().UTC()
		record, replayed, handleErr := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "command-" + runID, Name: "run.start",
			ComputerID: "cmp-repository-session", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
		})
		if handleErr != nil || replayed || record.Status != "completed" {
			t.Fatalf("workspace run %d record=%+v replayed=%t err=%v", index, record, replayed, handleErr)
		}
	}
	provider.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.mu.Unlock()
	if starts != 1 || resumes != 1 {
		t.Fatalf("workspace session was not reused: starts=%d resumes=%d", starts, resumes)
	}
	engine.managersMu.Lock()
	manager := engine.managers["fake"]
	engine.managersMu.Unlock()
	if manager == nil || manager.InstanceCount() != 1 {
		t.Fatalf("manager did not reuse workspace key: manager=%v", manager)
	}
}

func newRuntimeSnapshotFixture(t *testing.T, suffix string) (*localstore.Store, *Runtime, *fakeAdapter, string, string, []nodeprotocol.GitResourceRequest) {
	t.Helper()
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, root)
	gitRuntimeCommand(t, root, "config", "user.name", "Runtime Snapshot Test")
	gitRuntimeCommand(t, root, "config", "user.email", "runtime-snapshot@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRuntimeCommand(t, root, "add", "--", "tracked.txt")
	gitRuntimeCommand(t, root, "commit", "-m", "base")
	provider := &fakeAdapter{}
	engine := New("cmp-snapshot-"+suffix, store, nil, t.TempDir(), nil)
	engine.RegisterTestAdapter("fake", provider)
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "resource-" + suffix, Name: "Code", Path: root}}
	approveRuntimeGitResources(t, engine, resources)
	repositories, err := engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-snapshot-" + suffix, Resources: resources,
	})
	if err != nil || len(repositories.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	return store, engine, provider, root, repositories.Repositories[0].ID, resources
}

func runtimeStartCommand(t *testing.T, runID string, resources []nodeprotocol.GitResourceRequest) nodeprotocol.CommandEnvelope {
	t.Helper()
	suffix := strings.TrimPrefix(runID, "run-snapshot-")
	payload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: runID, ProjectID: "project-snapshot-" + suffix,
		GitResources:         resources,
		IssueAgentInstanceID: "instance-" + suffix, IssueAgentGenerationID: "generation-" + suffix,
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-" + suffix, Name: "run.start",
		ComputerID: "cmp-snapshot-" + suffix, SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	}
}

func assertRuntimeStartEndSnapshots(t *testing.T, engine *Runtime, runID string, failed bool) {
	t.Helper()
	snapshots, err := engine.GetRunGitSnapshots(t.Context(), runID)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if snapshots[0].Phase != "start" || snapshots[1].Phase != "end" {
		t.Fatalf("snapshot phases=%+v", snapshots)
	}
	var startPatch, endPatch string
	for _, area := range snapshots[0].Areas {
		startPatch += area.Patch
	}
	for _, area := range snapshots[1].Areas {
		endPatch += area.Patch
	}
	if startPatch != "" || !strings.Contains(endPatch, "tracked.txt") {
		t.Fatalf("failed=%t start patch=%q end patch=%q", failed, startPatch, endPatch)
	}
}

func gitRuntimeCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func initRuntimeGitRepository(t *testing.T, directory string) {
	t.Helper()
	command := exec.Command("git", "-C", directory, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", directory, err, output)
	}
}

func TestScheduleRunDoesNotReceiveIssueToolBridge(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	workspaceRoot := t.TempDir()
	engine := New("cmp_1", store, nil, workspaceRoot, nil)
	engine.RegisterTestAdapter("fake", provider)
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run_schedule", ProjectID: "project_1", LeaseEpoch: 1,
		Provider: "fake", Prompt: "scheduled prompt",
	})
	record, replayed, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_schedule", Name: "run.start",
		ComputerID: "cmp_1", SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: payload,
	})
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("schedule command: record=%+v replayed=%v err=%v", record, replayed, err)
	}
	provider.mu.Lock()
	prompt := provider.lastPrompt
	provider.mu.Unlock()
	if prompt != "scheduled prompt" {
		t.Fatalf("schedule prompt received issue tools: %q", prompt)
	}
	workspace, _, _, workspaceErr := engine.prepareProjectWorkspace("project_1", nil)
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".teamva", "tool-requests", "run_schedule")); !os.IsNotExist(statErr) {
		t.Fatalf("schedule run created an issue bridge: %v", statErr)
	}
}

func TestRuntimeHundredRunLifecycleSoak(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &fakeAdapter{}
	runtime := New("cmp_1", store, nil, t.TempDir(), nil)
	runtime.RegisterTestAdapter("fake", provider)
	manager := runtime.managers["fake"]
	manager.idleAfter = 0

	const runCount = 100
	for index := 0; index < runCount; index++ {
		runID := fmt.Sprintf("run_%d", index)
		payload, marshalErr := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: runID, IssueID: "issue_1",
			IssueAgentInstanceID:   fmt.Sprintf("instance_%d", index),
			IssueAgentGenerationID: "generation_1",
			ProjectID:              "project_1", LeaseEpoch: 1, Provider: "fake",
			Prompt: "test", ReadOnly: true,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		command := nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version,
			MessageID:       fmt.Sprintf("command_%d", index), Name: "run.start",
			ComputerID: "cmp_1", SentAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(time.Minute), Payload: payload,
		}
		record, replayed, handleErr := runtime.Handle(t.Context(), command)
		if handleErr != nil || replayed || record.Status != "completed" {
			t.Fatalf("run %d failed: record=%+v replayed=%v err=%v", index, record, replayed, handleErr)
		}
		if manager.AdapterForRun(runID) != nil {
			t.Fatalf("run %s remained mapped after Handle returned", runID)
		}
		if count := manager.InstanceCount(); count != 0 {
			t.Fatalf("idle instances accumulated after run %d: %d", index, count)
		}
	}
	provider.mu.Lock()
	closed := provider.closed
	provider.mu.Unlock()
	if closed < runCount {
		t.Fatalf("expected each idle managed instance to be closed, got %d closes", closed)
	}
}

func TestRecycleIdleDoesNotCloseActiveInstance(t *testing.T) {
	provider := &fakeAdapter{}
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(provider)
	manager.idleAfter = 0
	key := InstanceKey{InstanceID: "instance_active", Generation: "generation_1"}
	if _, _, err := manager.Get(t.Context(), "run_active", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	manager.RecycleIdle(t.Context())
	if count := manager.InstanceCount(); count != 1 {
		t.Fatalf("active instance was recycled: count=%d", count)
	}
	provider.mu.Lock()
	closedWhileActive := provider.closed
	provider.mu.Unlock()
	if closedWhileActive != 0 {
		t.Fatalf("active provider was closed %d times", closedWhileActive)
	}
	manager.ReleaseRun("run_active")
	manager.RecycleIdle(t.Context())
	if count := manager.InstanceCount(); count != 0 {
		t.Fatalf("released idle instance was not recycled: count=%d", count)
	}
}

func TestRecycleIdleNowSynchronouslyClosesOnlyReleasedInstances(t *testing.T) {
	provider := &fakeAdapter{}
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(provider)
	defer manager.Close()
	key := InstanceKey{InstanceID: "instance-explicit-recycle", Generation: "generation-1"}
	if _, _, err := manager.Get(t.Context(), "run-explicit-recycle", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	recycled, active, remaining, err := manager.RecycleIdleNow(t.Context())
	if err != nil || recycled != 0 || active != 1 || remaining != 1 {
		t.Fatalf("active recycle result=(%d,%d,%d) err=%v", recycled, active, remaining, err)
	}
	provider.mu.Lock()
	closed := provider.closed
	provider.mu.Unlock()
	if closed != 0 {
		t.Fatalf("active provider was closed %d times", closed)
	}

	manager.ReleaseRun("run-explicit-recycle")
	recycled, active, remaining, err = manager.RecycleIdleNow(t.Context())
	if err != nil || recycled != 1 || active != 0 || remaining != 0 {
		t.Fatalf("idle recycle result=(%d,%d,%d) err=%v", recycled, active, remaining, err)
	}
	provider.mu.Lock()
	closed = provider.closed
	provider.mu.Unlock()
	if closed != 1 {
		t.Fatalf("released provider close count=%d, want 1", closed)
	}
}

func TestRecycleIdleNowReportsConcurrentProviderShutdownUntilItCompletes(t *testing.T) {
	provider := &blockingCloseAdapter{
		fakeAdapter: &fakeAdapter{}, started: make(chan struct{}), release: make(chan struct{}),
	}
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(provider)
	key := InstanceKey{InstanceID: "instance-closing", Generation: "generation-1"}
	if _, _, err := manager.Get(t.Context(), "run-closing", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	manager.ReleaseRun("run-closing")
	type recycleResult struct {
		recycled  int
		active    int
		remaining int
		err       error
	}
	firstDone := make(chan recycleResult, 1)
	go func() {
		recycled, active, remaining, err := manager.RecycleIdleNow(context.Background())
		firstDone <- recycleResult{recycled: recycled, active: active, remaining: remaining, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("Provider shutdown did not start")
	}
	recycled, active, remaining, err := manager.RecycleIdleNow(t.Context())
	if err != nil || recycled != 0 || active != 0 || remaining != 1 {
		t.Fatalf("concurrent recycle result=(%d,%d,%d) err=%v", recycled, active, remaining, err)
	}
	close(provider.release)
	select {
	case result := <-firstDone:
		if result.err != nil || result.recycled != 1 || result.active != 0 || result.remaining != 0 {
			t.Fatalf("first recycle result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Provider shutdown did not complete")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerGetWaitsForSameInstanceProviderShutdown(t *testing.T) {
	provider := &blockingCloseAdapter{
		fakeAdapter: &fakeAdapter{}, started: make(chan struct{}), release: make(chan struct{}),
	}
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(provider)
	key := InstanceKey{InstanceID: "instance-close-reopen", Generation: "generation-1"}
	if _, _, err := manager.Get(t.Context(), "run-before-close", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	manager.ReleaseRun("run-before-close")
	recycleDone := make(chan error, 1)
	go func() {
		_, _, _, err := manager.RecycleIdleNow(context.Background())
		recycleDone <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("Provider shutdown did not start")
	}
	getDone := make(chan error, 1)
	go func() {
		_, _, err := manager.Get(context.Background(), "run-after-close", key, InstanceConfig{})
		getDone <- err
	}()
	select {
	case err := <-getDone:
		t.Fatalf("same-key Get returned before old Provider shutdown: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(provider.release)
	select {
	case err := <-recycleDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Provider recycle did not complete")
	}
	select {
	case err := <-getDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("same-key Get did not resume after Provider shutdown")
	}
	manager.ReleaseRun("run-after-close")
	if _, _, _, err := manager.RecycleIdleNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerClosePropagatesProviderShutdownFailure(t *testing.T) {
	shutdownErr := errors.New("provider shutdown failed")
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{closeErr: shutdownErr})
	if _, _, err := manager.Get(t.Context(), "run-close-error",
		InstanceKey{InstanceID: "instance-close-error", Generation: "generation-1"}, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); !errors.Is(err, shutdownErr) {
		t.Fatalf("Manager.Close error=%v, want %v", err, shutdownErr)
	}
}

func TestManagerCopiesAndVerifiesInstalledSkillTree(t *testing.T) {
	dataRoot := t.TempDir()
	agentsRoot := filepath.Join(dataRoot, "agents")
	installedRoot := filepath.Join(dataRoot, "skills", "hallmark")
	if err := os.MkdirAll(filepath.Join(installedRoot, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedRoot, "SKILL.md"), []byte("---\nname: hallmark\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedRoot, "references", "rules.md"), []byte("rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := inspectSkillTree(installedRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager("fake", "fake", agentsRoot, nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	key := InstanceKey{InstanceID: "instance-skill", Generation: "generation-skill"}
	if _, _, err := manager.Get(t.Context(), "run-skill", key, InstanceConfig{Skills: []nodeprotocol.SkillPayload{{
		Slug: "hallmark", ContentDigest: tree.digest,
	}}}); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(manager.instanceDir(key), "skills", "hallmark", "references", "rules.md")
	if content, err := os.ReadFile(copied); err != nil || string(content) != "rules" {
		t.Fatalf("supporting file was not copied: content=%q err=%v", content, err)
	}
	manager.ReleaseRun("run-skill")
	manager.Close()

	mismatch := NewManager("fake", "fake", agentsRoot, nil, log.New(io.Discard, "", 0))
	mismatch.RegisterTestAdapter(&fakeAdapter{})
	_, _, err = mismatch.Get(t.Context(), "run-mismatch", InstanceKey{
		InstanceID: "instance-mismatch", Generation: "generation-mismatch",
	}, InstanceConfig{Skills: []nodeprotocol.SkillPayload{{Slug: "hallmark", ContentDigest: "sha256:wrong"}}})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestWriteOpenCodeConfigUsesOnlyBoundSkillSources(t *testing.T) {
	configDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, slug := range []string{"hallmark", "unbound-stale"} {
		dir := filepath.Join(configDir, "skills", slug)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+slug+"\ndescription: test\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, bound := range []bool{false, true} {
		instance := InstanceConfig{SystemPrompt: "be exact"}
		if bound {
			instance.Skills = []nodeprotocol.SkillPayload{{Slug: "hallmark"}}
		}
		if err := opencode.WriteConfig(configDir, instance); err != nil {
			t.Fatal(err)
		}
		encoded, err := os.ReadFile(filepath.Join(configDir, "opencode", "opencode.json"))
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(encoded, &config); err != nil {
			t.Fatal(err)
		}
		sources, ok := config["skills"].([]any)
		if !ok || (!bound && len(sources) != 0) || (bound && (len(sources) != 1 || sources[0] != filepath.Join(configDir, "skills", "hallmark"))) {
			t.Fatalf("bound=%v: OpenCode skill sources must exclude unbound siblings: %#v", bound, config["skills"])
		}
		// v2 applies permissions per session; an ordinary instance must not bake
		// a global permission block that would also leak into writable runs.
		if permission, present := config["permission"]; present {
			t.Fatalf("OpenCode v2 instance config must not persist permissions: %#v", permission)
		}
	}
}

func TestWriteOpenCodeConfigIsAcceptedByInstalledProvider(t *testing.T) {
	executable, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("OpenCode is not installed")
	}
	isolateRetentionHomes(t)
	for _, name := range []string{"OPENCODE_CONFIG", "OPENCODE_SERVER_PASSWORD", "OPENCODE_PASSWORD"} {
		t.Setenv(name, "")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	skillDir := filepath.Join(configDir, "skills", "hallmark")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: hallmark\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := opencode.WriteConfig(configDir, InstanceConfig{Skills: []nodeprotocol.SkillPayload{{Slug: "hallmark"}}}); err != nil {
		t.Fatal(err)
	}
	// v2 reads the instance configuration from $XDG_CONFIG_HOME/opencode.
	// A private server must not discover, start or reconfigure the user's service.
	command := exec.Command(executable, "debug", "config", "--standalone")
	command.Dir = configDir
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode rejected generated config: %v\n%s", err, output)
	}
}

func TestParseGitHubSkillRepositoryRejectsUnsafeSources(t *testing.T) {
	owner, repository, err := parseGitHubRepository("https://github.com/Nutlope/hallmark.git")
	if err != nil || owner != "Nutlope" || repository != "hallmark" {
		t.Fatalf("valid GitHub source parsed as owner=%q repository=%q err=%v", owner, repository, err)
	}
	for _, source := range []string{
		"http://github.com/Nutlope/hallmark",
		"https://example.com/Nutlope/hallmark",
		"https://github.com/Nutlope/hallmark/tree/main",
		"https://github.com/Nutlope/hallmark?token=secret",
	} {
		if _, _, err := parseGitHubRepository(source); err == nil {
			t.Fatalf("unsafe Skill source accepted: %s", source)
		}
	}
}
