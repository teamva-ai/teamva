package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

// Durable command replay + event outbox fixture, not a native CLI. Handle
// persists its event before returning the failed command, like embedded Runtime.
type terminalOrderRuntime struct {
	*fakeRuntime
	stateMu                 sync.Mutex
	mode                    string
	executions, deliveries  int
	readFailure, ackFailure bool
	result                  nodeprotocol.CommandResult
	commandID               string
}

func (r *terminalOrderRuntime) Handle(_ context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.deliveries++
	if r.commandID != "" {
		if command.MessageID != r.commandID {
			return nodeprotocol.CommandResult{}, errors.New("redelivery changed command identity")
		}
		if r.result.Status != "failed" {
			return r.result, nil
		}
		return r.result, errors.New("codex cannot enforce DisableTools fixture; Cookie: session=fixture-only-returned")
	}
	r.commandID = command.MessageID
	r.executions++
	var p nodeprotocol.StartRunPayload
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return nodeprotocol.CommandResult{}, err
	}
	r.result = nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "failed", ErrorCode: "provider_error", Result: json.RawMessage(`{"error":"codex cannot enforce DisableTools fixture; API_KEY=fixture-only-result"}`)}
	if r.mode == "retry-only" {
		r.result = nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "processing"}
	}
	if r.mode == "no-native-result" {
		r.result.Result = nil
	}
	if r.mode != "no-native-event" && r.mode != "no-native-result" {
		now := time.Now().UTC()
		events := []nodeprotocol.RunEvent{
			{ID: "runtime-retry", RunID: p.RunID, LeaseEpoch: p.LeaseEpoch, Sequence: 1, Type: "run.error", Payload: map[string]any{"willRetry": true, "message": "Gateway retry fixture"}, OccurredAt: now},
			{ID: "runtime-terminal", RunID: p.RunID, LeaseEpoch: p.LeaseEpoch, Sequence: 2, Type: "run.finished", Payload: map[string]any{"status": "failed", "reason": "provider_error", "provider_id": p.Provider, "message": "codex cannot enforce DisableTools fixture; Authorization: Bearer fixture-only-native"}, OccurredAt: now.Add(time.Millisecond)},
		}
		if r.mode == "gap" {
			events = events[1:]
		}
		if r.mode == "retry-only" {
			events = events[:1]
		}
		r.mu.Lock()
		r.events = append(r.events, events...)
		r.mu.Unlock()
	}
	if r.result.Status != "failed" {
		return r.result, nil
	}
	return r.result, errors.New("codex cannot enforce DisableTools fixture; Cookie: session=fixture-only-returned")
}

func (r *terminalOrderRuntime) UnackedEvents(ctx context.Context, limit int) ([]nodeprotocol.RunEvent, error) {
	r.stateMu.Lock()
	if r.readFailure {
		r.readFailure = false
		r.stateMu.Unlock()
		return nil, errors.New("injected event read failure")
	}
	r.stateMu.Unlock()
	return r.fakeRuntime.UnackedEvents(ctx, limit)
}

func (r *terminalOrderRuntime) AckEvents(ctx context.Context, cursors []nodeprotocol.RunEventCursor) error {
	r.stateMu.Lock()
	if r.ackFailure {
		r.ackFailure = false
		r.stateMu.Unlock()
		return errors.New("injected ACK loss after commit")
	}
	r.stateMu.Unlock()
	return r.fakeRuntime.AckEvents(ctx, cursors)
}

func terminalOrderFixture(t *testing.T, mode string) (*Local, *store.SQLite, *terminalOrderRuntime, domain.Run, store.LocalCommand) {
	t.Helper()
	repo, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	run := localLeaseRunFixture(t, repo, user, computer, service, "terminal-order")
	command, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	runtime := &terminalOrderRuntime{fakeRuntime: &fakeRuntime{}, mode: mode, readFailure: mode == "read-failure", ackFailure: mode == "ack-loss"}
	local := &Local{repository: repo, controller: runtime, user: user, computer: computer, wake: make(chan struct{}, 1), inFlight: map[string]struct{}{}, runWaiters: map[string]map[chan struct{}]struct{}{}, issueWaiters: map[string]map[chan struct{}]struct{}{}}
	return local, repo, runtime, run, command
}

func executeTerminalOrderCommand(t *testing.T, local *Local, command store.LocalCommand) {
	t.Helper()
	local.wg.Add(1)
	local.executeOutboxCommand(t.Context(), command)
}

func assertSingleSafeTerminal(t *testing.T, repo *store.SQLite, local *Local, run domain.Run, native bool) {
	t.Helper()
	events, err := repo.ListRunEvents(t.Context(), local.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	t.Logf("persisted failure events: %s", encoded)
	terminals := []domain.RunEvent{}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("non-contiguous public sequence: %+v", events)
		}
		if (event.Type == "run.finished" || event.Type == "run.error" || event.Type == "run.completed") && event.Payload["willRetry"] != true {
			terminals = append(terminals, event)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("command/native race produced %d terminals", len(terminals))
	}
	if native && terminals[0].ID != "runtime-terminal" {
		t.Fatal("generic command terminal won over already-durable native diagnosis")
	}
	if !strings.Contains(string(encoded), "codex cannot enforce DisableTools fixture") || strings.Contains(string(encoded), "fixture-only-") {
		t.Fatal("terminal lost actual policy rejection or retained credential canary")
	}
}

func TestFailedCommandImportsRuntimeTerminalBeforeFallback(t *testing.T) {
	for _, mode := range []string{"native-event", "no-native-event", "no-native-result"} {
		t.Run(mode, func(t *testing.T) {
			local, repo, _, run, command := terminalOrderFixture(t, mode)
			executeTerminalOrderCommand(t, local, command)
			// Check immediate readability, not only a later eventual import.
			assertSingleSafeTerminal(t, repo, local, run, mode == "native-event")
			if err := local.flushEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertSingleSafeTerminal(t, repo, local, run, mode == "native-event")
			stored, err := repo.GetLocalCommand(t.Context(), command.ID)
			if err != nil || strings.Contains(string(stored.Result), "fixture-only-") {
				t.Fatal("unsafe command failure result")
			}
		})
	}
}

func TestFailedCommandEventImportFailureRetainsReplayAndSingleTerminal(t *testing.T) {
	for _, mode := range []string{"read-failure", "ack-loss", "gap"} {
		t.Run(mode, func(t *testing.T) {
			local, repo, runtime, run, command := terminalOrderFixture(t, mode)
			executeTerminalOrderCommand(t, local, command)
			stored, err := repo.GetLocalCommand(t.Context(), command.ID)
			if err != nil || stored.Status != store.LocalCommandUncertain {
				t.Fatalf("event import failure must not seal command/fallback: status=%s err=%v", stored.Status, err)
			}
			current, err := repo.LocalRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "ack-loss" && domain.IsRunTerminal(current.Status) {
				t.Fatal("failed event read/gap fabricated a premature terminal")
			}
			if mode == "gap" {
				runtime.mu.Lock()
				runtime.events = append(runtime.events, nodeprotocol.RunEvent{ID: "runtime-retry", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.error", Payload: map[string]any{"willRetry": true, "message": "Gateway retry fixture"}, OccurredAt: time.Now().UTC()})
				runtime.mu.Unlock()
			}
			claim, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
			if err != nil || !ok || claim.ID != command.ID {
				t.Fatalf("reclaim ok=%v err=%v", ok, err)
			}
			executeTerminalOrderCommand(t, local, claim)
			assertSingleSafeTerminal(t, repo, local, run, true)
			stored, err = repo.GetLocalCommand(t.Context(), command.ID)
			if err != nil || stored.Status != store.LocalCommandFailed || runtime.executions != 1 || runtime.deliveries != 2 {
				t.Fatalf("replay re-executed Provider or failed to seal result: status=%s executions=%d deliveries=%d err=%v", stored.Status, runtime.executions, runtime.deliveries, err)
			}
			if err := local.flushEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertSingleSafeTerminal(t, repo, local, run, true)
		})
	}
}

func TestRetryEventAndProcessingCommandNeverBecomeTerminal(t *testing.T) {
	local, repo, _, run, command := terminalOrderFixture(t, "retry-only")
	executeTerminalOrderCommand(t, local, command)
	current, err := repo.LocalRun(t.Context(), run.ID)
	if err != nil || domain.IsRunTerminal(current.Status) {
		t.Fatalf("retry prematurely finished Run: status=%s err=%v", current.Status, err)
	}
	stored, err := repo.GetLocalCommand(t.Context(), command.ID)
	if err != nil || stored.Status != store.LocalCommandPending {
		t.Fatalf("processing command should remain retryable: status=%s err=%v", stored.Status, err)
	}
	events, err := repo.ListRunEvents(t.Context(), local.user.ID, run.ID)
	if err != nil || len(events) != 2 || events[1].Payload["willRetry"] != true {
		t.Fatalf("retry was dropped or a fallback terminal was added: %+v err=%v", events, err)
	}
}
