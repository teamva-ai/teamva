package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	"github.com/teamva-ai/teamva/node/internal/runtime"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestEmbeddedUnackedEventsUsesBoundedDurablePrefix(t *testing.T) {
	root := t.TempDir()
	controller, err := New(t.Context(), Config{
		ComputerID: "computer_bounded_events", DataDir: filepath.Join(root, "data"),
		WorkspaceDir: filepath.Join(root, "workspaces"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	payload := strings.Repeat("x", 128<<10)
	for index := 0; index < nodeprotocol.MaxRunEventsPerBatch; index++ {
		if _, err := controller.store.AppendEvent(t.Context(), "run-embedded-prefix", 1, adapter.Event{
			Type: "provider.event", Payload: map[string]any{"text": payload},
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := controller.UnackedEvents(t.Context(), nodeprotocol.MaxRunEventsPerBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || len(events) >= nodeprotocol.MaxRunEventsPerBatch {
		t.Fatalf("embedded durable prefix events=%d", len(events))
	}
	encoded, err := json.Marshal(nodeprotocol.RunEventBatch{Events: events})
	if err != nil || len(encoded) > 8<<20 {
		t.Fatalf("embedded durable prefix bytes=%d err=%v", len(encoded), err)
	}
	last := events[len(events)-1]
	if err := controller.AckEvents(t.Context(), []nodeprotocol.RunEventCursor{{
		RunID: last.RunID, LeaseEpoch: last.LeaseEpoch, Sequence: last.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	remaining, err := controller.UnackedEvents(t.Context(), nodeprotocol.MaxRunEventsPerBatch)
	if err != nil || len(remaining) == 0 || remaining[0].Sequence <= last.Sequence {
		t.Fatalf("embedded durable prefix did not make progress: remaining=%d err=%v", len(remaining), err)
	}
}

func TestCustomExecutablesAreUsedByProbeAndManagedRuns(t *testing.T) {
	root := t.TempDir()
	// The fake CLIs must never inspect or link the developer's account/config.
	for name, dir := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "user-config", "XDG_DATA_HOME": "user-data",
	} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
	customOpenCodeLog := filepath.Join(root, "custom-opencode.log")
	customCodexLog := filepath.Join(root, "custom-codex.log")
	customOpenCode := writeRecordingExecutable(t, root, "custom-opencode", customOpenCodeLog, "opencode test")
	customCodex := writeRecordingExecutable(t, root, "custom-codex", customCodexLog, "codex-cli test")

	pathDir := filepath.Join(root, "path")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fallbackLog := filepath.Join(root, "fallback.log")
	writeRecordingExecutable(t, pathDir, "opencode", fallbackLog, "fallback opencode")
	writeRecordingExecutable(t, pathDir, "codex", fallbackLog, "fallback codex")
	t.Setenv("PATH", pathDir)
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "")

	controller, err := New(t.Context(), Config{
		ComputerID: "computer_1", DataDir: filepath.Join(root, "data"),
		WorkspaceDir:  filepath.Join(root, "workspaces"),
		ProviderPaths: map[string]string{"opencode": customOpenCode, "codex": customCodex},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })

	probeContext, cancelProbe := context.WithTimeout(t.Context(), 2*time.Second)
	manifests, err := controller.Probe(probeContext)
	cancelProbe()
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{"opencode": "opencode test", "codex": "test"}
	seenCustom := map[string]bool{}
	for _, manifest := range manifests {
		want, custom := versions[manifest.Provider]
		if !custom {
			continue
		} // Other auto-discovered providers are not this fixture's executables.
		seenCustom[manifest.Provider] = true
		if manifest.ProviderVersion != want || manifest.Available {
			t.Fatalf("probe must read the custom version before the intentional fake startup failure: %+v", manifest)
		}
	}
	if len(seenCustom) != len(versions) {
		t.Fatalf("custom providers missing from probe manifests: %+v", manifests)
	}
	// No manual --version preflight: every recorded invocation belongs to Probe
	// or Handle, so a fixture sanity check cannot satisfy the product assertion.
	assertInvocationCount(t, customOpenCodeLog, "--version", 1)
	assertInvocationCount(t, customOpenCodeLog, "serve", 1)
	assertInvocationCount(t, customCodexLog, "--version", 1)
	assertInvocationCount(t, customCodexLog, "app-server", 1)

	for _, provider := range []string{"opencode", "codex"} {
		payload, marshalErr := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: "run_" + provider, IssueID: "issue_1", ProjectID: "project_1",
			IssueAgentInstanceID:   "instance_" + provider,
			IssueAgentGenerationID: "generation_1",
			LeaseEpoch:             1, Provider: provider, Prompt: "test", ReadOnly: true,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		runContext, cancelRun := context.WithTimeout(t.Context(), 2*time.Second)
		result, runErr := controller.Handle(runContext, nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "command_" + provider,
			Name: "run.start", ComputerID: "computer_1", SentAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(time.Minute), Payload: payload,
		})
		contextErr := runContext.Err()
		cancelRun()
		// These recorders deliberately exit 1 instead of implementing a native
		// server. Require that failure, not a deadline or earlier setup rejection.
		if contextErr != nil || runErr == nil || result.Status != "failed" || result.ErrorCode != "provider_error" {
			t.Fatalf("%s fake startup: result=%+v err=%v context=%v", provider, result, runErr, contextErr)
		}
		// Managed OpenCode cannot write the root-level Probe log: the OS sandbox
		// only permits its private native data (and exact Run IPC). Collect there
		// rather than weakening the two-independent-executions assertion.
		logs, globErr := filepath.Glob(filepath.Join(root, "agents", "provider-data", "*", "custom-"+provider+".log"))
		if globErr != nil || len(logs) != 1 {
			t.Fatalf("%s managed invocation logs = %v, err=%v", provider, logs, globErr)
		}
		argument := "serve"
		if provider == "codex" {
			argument = "app-server"
		}
		assertInvocationCount(t, logs[0], argument, 1)
		assertInvocationCount(t, logs[0], "--version", 0)
		t.Logf("%s: separate Probe and managed custom executable invocations verified (%s)", provider, argument)
	}

	// Probe records are unchanged; neither a cached probe nor a managed PATH
	// fallback may stand in for the separate custom managed invocation above.
	assertInvocationCount(t, customOpenCodeLog, "serve", 1)
	assertInvocationCount(t, customCodexLog, "app-server", 1)
	if logs, err := filepath.Glob(filepath.Join(root, "agents", "provider-data", "*", "fallback.log")); err != nil || len(logs) != 0 {
		t.Fatalf("managed runs fell back to PATH executables: logs=%v err=%v", logs, err)
	}
	if payload, readErr := os.ReadFile(fallbackLog); readErr == nil && len(payload) > 0 {
		t.Fatalf("managed runs fell back to PATH executables:\n%s", payload)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
}

func writeRecordingExecutable(t *testing.T, dir, name, logPath, version string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := fmt.Sprintf(`#!/bin/sh
log=%q
case "$1" in
  serve)
    # Probe now uses the same isolated config flags. Only an execution's
    # provider-data home redirects its receipt; keep the two invocations distinct.
    case "$XDG_DATA_HOME" in %q*) log="$XDG_DATA_HOME/"%q ;; esac
    ;;
  app-server)
    if [ -n "$CODEX_HOME" ]; then log="$CODEX_HOME/"%q; fi
    ;;
esac
printf '%%s\n' "$*" >> "$log" || exit 2
if [ "$1" = "--version" ]; then
  printf '%%s\n' %q
  exit 0
fi
exit 1
`, logPath, filepath.Join(dir, "agents", "provider-data")+string(os.PathSeparator), filepath.Base(logPath), filepath.Base(logPath), version)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertInvocationCount(t *testing.T, logPath, argument string, want int) {
	t.Helper()
	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for line := range strings.SplitSeq(string(payload), "\n") {
		if args := strings.Fields(line); len(args) > 0 && args[0] == argument {
			count++
		}
	}
	if count != want {
		t.Fatalf("%s was invoked with %q %d times, want exactly %d; log:\n%s",
			logPath, argument, count, want, payload)
	}
}

type stuckProbeAdapter struct {
	adapter.Adapter
	started chan struct{}
	release chan struct{}
	exited  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (a *stuckProbeAdapter) Name() string { return "opencode" }
func (a *stuckProbeAdapter) Probe(context.Context) (adapter.CapabilityManifest, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	close(a.started)
	// Deliberately ignore context: Controller must still return to its caller,
	// retain exclusive discovery ownership, and join us after adapter.Close.
	<-a.release
	close(a.exited)
	return adapter.CapabilityManifest{Provider: a.Name()}, nil
}
func (a *stuckProbeAdapter) Close() error {
	a.once.Do(func() { close(a.release) })
	return nil
}

func TestControllerProbeCancellationKeepsGateUntilWorkerFinishes(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store, err := localstore.Open(ctx, filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	item := &stuckProbeAdapter{started: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	probeCtx, probeCancel := context.WithCancel(ctx)
	controller := &Controller{
		store: store, runtime: runtime.New("computer_probe", store, []adapter.Adapter{item}, root, nil),
		discovery: make(chan struct{}, 1), probeCtx: probeCtx, probeCancel: probeCancel,
	}
	defer controller.Close()
	callerCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, err := controller.Probe(callerCtx); result <- err }()
	select {
	case <-item.started:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Probe error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Probe did not cancel while waiting for results")
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer waitCancel()
	_, err = controller.Handle(waitCtx, nodeprotocol.CommandEnvelope{Name: "agent.probe"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("manual probe gate did not honor context: %v", err)
	}
	item.mu.Lock()
	calls := item.calls
	item.mu.Unlock()
	if calls != 1 {
		t.Fatalf("cancelled caller released discovery gate while worker remained: calls=%d", calls)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-item.exited:
	default:
		t.Fatal("Close did not join outstanding probe worker")
	}
}

func TestControllerCloseReleasesStuckManualProbeHandle(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store, err := localstore.Open(ctx, filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	item := &stuckProbeAdapter{started: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	probeCtx, probeCancel := context.WithCancel(ctx)
	controller := &Controller{
		store: store, runtime: runtime.New("computer_probe", store, []adapter.Adapter{item}, root, nil),
		discovery: make(chan struct{}, 1), probeCtx: probeCtx, probeCancel: probeCancel,
	}
	defer controller.Close()
	defer item.Close()
	callerCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := controller.Handle(callerCtx, nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "manual_probe", Name: "agent.probe",
			ComputerID: "computer_probe", SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
			IdempotencyKey: "manual_probe", Payload: json.RawMessage(`{"provider":"opencode"}`),
		})
		result <- err
	}()
	select {
	case <-item.started:
	case err := <-result:
		t.Fatalf("manual probe never reached Provider: %v", err)
	case <-time.After(time.Second):
		t.Fatal("manual probe never started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("manual probe did not honor cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual probe Handle caller remained stuck")
	}
	closed := make(chan error, 1)
	go func() { closed <- controller.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Runtime Handle join deadlocked before discovery adapter shutdown")
	}
}
