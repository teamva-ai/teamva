package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type pipelineAdapter struct {
	fakeAdapter
	emit    []adapter.Event
	failure error
}

func (a *pipelineAdapter) Start(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	a.sink = sink
	for _, e := range a.emit {
		if err := sink(ctx, e); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	return adapter.NativeSession{ID: "session"}, a.failure
}

func TestEventPipelineSessionPersistenceExitSettlesAndRejectsLateOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := localstore.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installRuntimeSessionFailureTrigger(t, path, "native_sessions")
	a := &pipelineAdapter{emit: []adapter.Event{{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "running"}}}}
	engine := New("cmp", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", a)
	payload, _ := json.Marshal(nodeprotocol.StartRunPayload{RunID: "run", ProjectID: "project", IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation", Provider: "fake", LeaseEpoch: 1, Prompt: "work", DisableTools: true})
	now := time.Now().UTC()
	_, _, err = engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command", Name: "run.start", ComputerID: "cmp", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
	if err == nil {
		t.Fatal("expected native session persistence failure")
	}
	if err := a.sink(t.Context(), adapter.Event{Type: "output.delta", Payload: map[string]any{"text": "must not survive terminal"}}); err != nil {
		t.Fatal(err)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 3 || events[1].Type != "context.compaction" || events[1].Payload["status"] != "failed" || events[2].Type != "run.finished" {
		t.Fatalf("post-provider failure left a dangling activity or late output: %+v err=%v", events, err)
	}
}

func TestEventPipelineRetainsSafeFailureAndSettlesCompactionBeforeTerminal(t *testing.T) {
	for _, terminal := range []string{"failed", "cancelled", "completed", "transport", "compaction-failed-run-completed"} {
		t.Run(terminal, func(t *testing.T) {
			store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			a := &pipelineAdapter{emit: []adapter.Event{
				{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "running"}},
				{Type: "run.error", Payload: map[string]any{"willRetry": true, "error": map[string]any{"message": "Gateway 503 retrying; API_KEY=fixture-only-retry"}}},
				{Type: "output.delta", Payload: map[string]any{"text": "after retry"}},
			}}
			if terminal == "transport" {
				a.failure = errors.New("Gateway stream disconnected; Authorization: Bearer fixture-only-transport")
			} else {
				status := terminal
				if terminal == "compaction-failed-run-completed" {
					a.emit = append(a.emit, adapter.Event{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "failed", "message": "Compaction gateway rejected summary"}})
					status = "completed"
				}
				a.emit = append(a.emit, adapter.Event{Type: "run.finished", Payload: map[string]any{"status": status, "error": map[string]any{"data": map[string]any{"message": "Gateway detail preserved; password=fixture-only-password"}}}})
			}
			engine := New("cmp-pipeline", store, nil, t.TempDir(), nil)
			defer engine.Close()
			engine.RegisterTestAdapter("fake", a)
			payload, _ := json.Marshal(nodeprotocol.StartRunPayload{RunID: "run", ProjectID: "project", IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation", Provider: "fake", LeaseEpoch: 1, Prompt: "work", DisableTools: true})
			now := time.Now().UTC()
			record, _, _ := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command", Name: "run.start", ComputerID: "cmp-pipeline", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
			events, err := store.UnackedEvents(t.Context(), 20)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded)+string(record.Result), "fixture-only-") {
				t.Fatal("raw diagnostic credential entered durable storage")
			}
			t.Logf("pre-persistence: diagnostic credential canaries injected (not logged); durable events: %s; command result: %s", encoded, record.Result)
			if len(events) != 5 || events[1].Type != "provider.event" || events[2].Type != "output.delta" {
				t.Fatalf("retry ended the run or terminal cleanup missing: %+v", events)
			}
			want := "failed"
			if terminal == "cancelled" || terminal == "completed" {
				want = "cancelled"
			}
			if events[3].Type != "context.compaction" || events[3].Payload["status"] != want {
				t.Fatalf("unfinished compaction not settled before terminal: %+v", events)
			}
			message, _ := events[4].Payload["message"].(string)
			if !strings.Contains(message, "Gateway") {
				t.Fatalf("terminal lost provider/transport failure detail: %+v", events[4])
			}
		})
	}
}
