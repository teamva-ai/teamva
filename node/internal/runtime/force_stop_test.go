package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type forceStopAdapter struct {
	fakeAdapter
	mode                                string
	ready, releaseRun, releaseInterrupt chan struct{}
	releaseOnce                         sync.Once
	runSink                             adapter.Sink
}

func (a *forceStopAdapter) release() {
	a.releaseOnce.Do(func() { close(a.releaseRun); close(a.releaseInterrupt) })
}
func (a *forceStopAdapter) Start(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	a.runSink = sink
	for _, event := range []adapter.Event{
		{Type: "run.started"},
		{Type: "provider.event", Payload: map[string]any{"kind": "error", "willRetry": true, "message": "Gateway 429 retry fixture; API_KEY=fixture-only-retry"}},
		{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "running"}},
	} {
		if err := sink(ctx, event); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	if a.mode == "failed-compaction" {
		_ = sink(ctx, adapter.Event{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "failed", "message": "Actual compaction gateway failure fixture"}})
	}
	close(a.ready)
	if a.mode == "native-during-grace" {
		<-ctx.Done()
		<-time.After(30 * time.Millisecond)
		_ = sink(context.Background(), adapter.Event{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "failed", "message": "Actual compaction gateway failure during stop fixture"}})
		_ = sink(context.Background(), adapter.Event{Type: "run.finished", Payload: map[string]any{"status": "cancelled", "reason": "interrupted", "message": "native cancellation during grace fixture"}})
		return adapter.NativeSession{ID: "session"}, context.Canceled
	}
	// Deliberately non-cooperative native execution: cancellation alone does not
	// make Start return. The public stop must still durably fence this sink.
	<-a.releaseRun
	_ = sink(context.Background(), adapter.Event{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "completed"}})
	_ = sink(context.Background(), adapter.Event{Type: "run.finished", Payload: map[string]any{"status": "cancelled", "reason": "interrupted", "message": "context canceled (late native receipt)"}})
	return adapter.NativeSession{ID: "session"}, context.Canceled
}

func (a *forceStopAdapter) Interrupt(_ context.Context, _ string) error {
	if a.mode == "native-wins" {
		_ = a.runSink(context.Background(), adapter.Event{Type: "context.compaction", Payload: map[string]any{"id": "compact", "status": "cancelled", "message": "native compaction interruption fixture"}})
		return a.runSink(context.Background(), adapter.Event{Type: "run.finished", Payload: map[string]any{"status": "cancelled", "reason": "interrupted", "message": "native cancellation diagnosis fixture"}})
	}
	if a.mode == "blocked-interrupt" {
		<-a.releaseInterrupt
	}
	return nil
}

func TestForceStopDurablySettlesBeforeAcknowledgementAndFencesLateCallbacks(t *testing.T) {
	for _, mode := range []string{"delayed-native", "blocked-interrupt", "failed-compaction", "native-wins", "native-during-grace", "graceful-escalation"} {
		t.Run(mode, func(t *testing.T) {
			store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			engine := New("cmp", store, nil, t.TempDir(), nil)
			defer engine.Close()
			a := &forceStopAdapter{mode: mode, ready: make(chan struct{}), releaseRun: make(chan struct{}), releaseInterrupt: make(chan struct{})}
			defer a.release()
			engine.RegisterTestAdapter("fake", a)
			command := func(id, name string, payload any) nodeprotocol.CommandEnvelope {
				raw, _ := json.Marshal(payload)
				now := time.Now().UTC()
				return nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: id, Name: name, ComputerID: "cmp", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: raw}
			}
			startDone := make(chan error, 1)
			go func() {
				_, _, err := engine.Handle(context.Background(), command("start", "run.start", nodeprotocol.StartRunPayload{RunID: "run", ProjectID: "project", IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation", Provider: "fake", LeaseEpoch: 1, Prompt: "work", DisableTools: true}))
				startDone <- err
			}()
			select {
			case <-a.ready:
			case <-time.After(5 * time.Second):
				t.Fatal("native fixture did not start")
			}
			if strings.HasPrefix(mode, "graceful-") {
				graceful := command("graceful-stop", "run.stop", map[string]any{"run_id": "run", "lease_epoch": 1, "provider": "fake", "mode": "graceful"})
				record, _, err := engine.Handle(t.Context(), graceful)
				var receipt map[string]any
				if err != nil || json.Unmarshal(record.Result, &receipt) != nil || receipt["interrupted"] != true || receipt["terminal_pending"] != true {
					t.Fatalf("graceful interrupt acknowledgement must not claim Run completion: result=%s err=%v", record.Result, err)
				}
				before, err := store.UnackedEvents(t.Context(), 20)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range before {
					if _, terminal := normalizeTerminalOutcome(event); terminal {
						t.Fatal("graceful request acknowledgement fabricated a Runtime terminal")
					}
				}
			}
			type stopped struct {
				record localstore.CommandRecord
				err    error
			}
			stopDone := make(chan stopped, 1)
			started := time.Now()
			stopCommand := command("stop", "run.stop", map[string]any{"run_id": "run", "lease_epoch": 1, "provider": "fake", "mode": "force"})
			go func() {
				record, _, err := engine.Handle(t.Context(), stopCommand)
				stopDone <- stopped{record, err}
			}()
			select {
			case result := <-stopDone:
				if result.err != nil || result.record.Status != "completed" {
					t.Fatalf("force stop result=%+v err=%v", result.record, result.err)
				}
				var receipt map[string]any
				if json.Unmarshal(result.record.Result, &receipt) != nil || receipt["terminal_pending"] == true {
					t.Fatal("force acknowledged a pending terminal instead of its durable barrier")
				}
			case <-time.After(3500 * time.Millisecond):
				t.Fatal("force stop waited indefinitely for non-cooperative native interrupt")
			}
			events, err := store.UnackedEvents(t.Context(), 20)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(events)
			t.Logf("force acknowledgement after %s; durable stream before native exit: %s", time.Since(started), encoded)
			terminalCount := 0
			compactStatus := ""
			compactIndex, terminalIndex := -1, -1
			for index, event := range events {
				if event.Sequence != int64(index+1) {
					t.Fatal("runtime sequence gap")
				}
				if event.Type == "context.compaction" {
					compactStatus = event.Payload["status"].(string)
					compactIndex = index
				}
				if _, terminal := normalizeTerminalOutcome(event); terminal {
					terminalCount++
					terminalIndex = index
					if event.Payload["status"] != "cancelled" {
						t.Fatal("force stop changed cancellation into another terminal outcome")
					}
				}
			}
			wantCompact := "cancelled"
			if mode == "failed-compaction" || mode == "native-during-grace" {
				wantCompact = "failed"
			}
			if terminalCount != 1 || terminalIndex != len(events)-1 || compactIndex >= terminalIndex || compactStatus != wantCompact {
				t.Fatalf("stop ACK preceded authoritative terminal/compaction settlement: terminals=%d compact=%s", terminalCount, compactStatus)
			}
			if strings.Contains(string(encoded), "fixture-only-") || !strings.Contains(string(encoded), "Gateway 429 retry fixture") {
				t.Fatal("accepted diagnostics were lost or unsafe")
			}
			if mode == "native-wins" && !strings.Contains(string(encoded), "native cancellation diagnosis fixture") {
				t.Fatal("force barrier overwrote an already accepted native diagnosis")
			}
			if mode == "native-during-grace" && (!strings.Contains(string(encoded), "Actual compaction gateway failure during stop fixture") || !strings.Contains(string(encoded), "native cancellation during grace fixture")) {
				t.Fatal("bounded stop grace discarded useful native compaction/cancellation diagnosis")
			}
			a.release()
			select {
			case err := <-startDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("start error=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("fixture failed to release")
			}
			after, err := store.UnackedEvents(t.Context(), 20)
			if err != nil || len(after) != len(events) {
				t.Fatalf("late native receipt changed public history after stop ACK: before=%d after=%d err=%v", len(events), len(after), err)
			}
			if record, replayed, err := engine.Handle(t.Context(), stopCommand); err != nil || !replayed || record.Status != "completed" {
				t.Fatalf("durable stop replay changed execution: replayed=%v status=%s err=%v", replayed, record.Status, err)
			}
		})
	}
}

func TestForceStopBeforeSinkInstallationCannotCreateLateTerminal(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", &fakeAdapter{})
	ctx, unregister, err := engine.registerRunStart(t.Context(), "preparing")
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	raw, _ := json.Marshal(nodeprotocol.StopRunPayload{RunID: "preparing", LeaseEpoch: 1, Provider: "fake", Mode: "force"})
	now := time.Now().UTC()
	_, _, err = engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "early-stop", Name: "run.stop", ComputerID: "cmp", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: raw})
	if err != nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("early force did not cancel preparation: %v", err)
	}
	called := false
	if !engine.installForceStopFinalizer("preparing", make(chan struct{}), func(string) error { called = true; return nil }) {
		t.Fatal("late sink was not told to fence its callbacks")
	}
	if err := engine.finishForceStoppedRun("preparing", "late stop replay"); err != nil || called {
		t.Fatal("late finalizer created a terminal after early stop acknowledgement")
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("pre-provider force invented late runtime activity: %+v err=%v", events, err)
	}
}
