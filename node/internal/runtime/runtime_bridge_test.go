package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type bridgeRunAdapter struct {
	*fakeAdapter
	run func(context.Context, adapter.RunRequest, adapter.Sink) (adapter.NativeSession, error)
}

func (provider *bridgeRunAdapter) Start(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	return provider.run(ctx, request, sink)
}

func runtimeBridgeCommand(t *testing.T, name, messageID string, payload any) nodeprotocol.CommandEnvelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: messageID, Name: name,
		ComputerID: "computer-bridge", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: raw,
	}
}

func TestRuntimeStopCancelsBridgeBeforeProviderReturns(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("computer-bridge", store, nil, t.TempDir(), nil)
	defer engine.Close()
	providerRelease := make(chan struct{})
	defer func() {
		select {
		case <-providerRelease:
		default:
			close(providerRelease)
		}
	}()
	provider := &bridgeRunAdapter{fakeAdapter: &fakeAdapter{}, run: func(ctx context.Context, request adapter.RunRequest, _ adapter.Sink) (adapter.NativeSession, error) {
		if !assertExactRunIPC(t, request) {
			return adapter.NativeSession{}, errors.New("provider did not receive its exact Run IPC directory")
		}
		path := filepath.Join(request.ToolRequestDir, "issue_comment_create", "request.json")
		if err := os.WriteFile(path, []byte(`{"body":"summary"}`), 0o600); err != nil {
			return adapter.NativeSession{}, err
		}
		<-ctx.Done()
		// An interrupted Provider may take time to tear down. Its bridge must
		// already be canceled, not wait for this Start call's deferred cleanup.
		<-providerRelease
		return adapter.NativeSession{}, ctx.Err()
	}}
	engine.RegisterTestAdapter("fake", provider)
	toolStarted, toolCanceled := make(chan struct{}), make(chan struct{})
	engine.SetToolCaller(toolCallerFunc(func(ctx context.Context, _ nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		close(toolStarted)
		<-ctx.Done()
		close(toolCanceled)
		return nodeprotocol.NodeRequestResult{}, ctx.Err()
	}))
	start := runtimeBridgeCommand(t, "run.start", "start-bridge-cancel", nodeprotocol.StartRunPayload{
		RunID: "run-bridge-cancel", IssueID: "issue", ProjectID: "project",
		IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation",
		Provider: "fake", LeaseEpoch: 1, Prompt: "work",
	})
	startDone := make(chan error, 1)
	go func() {
		_, _, err := engine.Handle(t.Context(), start)
		startDone <- err
	}()
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not start its request")
	}
	stop := runtimeBridgeCommand(t, "run.stop", "stop-bridge-cancel", nodeprotocol.StopRunPayload{RunID: "run-bridge-cancel", Provider: "fake"})
	if _, _, err := engine.Handle(t.Context(), stop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolCanceled:
	case <-time.After(time.Second):
		t.Fatal("Run stop left an in-flight bridge RPC alive while Provider was stopping")
	}
	select {
	case err := <-startDone:
		t.Fatalf("provider returned before release: %v", err)
	default:
	}
	close(providerRelease)
	select {
	case err := <-startDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped run error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped Runtime did not finish")
	}
}

func TestRuntimeBridgeAllowsFinalPublicSummaryBeforeNormalCompletion(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("computer-bridge", store, nil, t.TempDir(), nil)
	defer engine.Close()
	provider := &bridgeRunAdapter{fakeAdapter: &fakeAdapter{}, run: func(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
		if !assertExactRunIPC(t, request) {
			return adapter.NativeSession{}, errors.New("provider did not receive its exact Run IPC directory")
		}
		path := filepath.Join(request.ToolRequestDir, "issue_comment_create", "summary.json")
		if err := os.WriteFile(path, []byte(`{"body":"final public summary","mentioned_agent_ids":[]}`), 0o600); err != nil {
			return adapter.NativeSession{}, err
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			payload, err := os.ReadFile(path + ".response.json")
			if err == nil {
				var response bridgeTestResponse
				if err := json.Unmarshal(payload, &response); err != nil {
					return adapter.NativeSession{}, err
				}
				if !response.OK || response.Data["comment_id"] != "working-placeholder" {
					return adapter.NativeSession{}, errors.New("final public summary was not committed")
				}
				if err := sink(ctx, adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}}); err != nil {
					return adapter.NativeSession{}, err
				}
				return adapter.NativeSession{ID: "native_1", Provider: "fake"}, nil
			}
			if !os.IsNotExist(err) {
				return adapter.NativeSession{}, err
			}
			select {
			case <-ctx.Done():
				return adapter.NativeSession{}, ctx.Err()
			case <-ticker.C:
			}
		}
	}}
	engine.RegisterTestAdapter("fake", provider)
	calls := 0
	engine.SetToolCaller(toolCallerFunc(func(ctx context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		calls++
		if err := ctx.Err(); err != nil {
			return nodeprotocol.NodeRequestResult{}, err
		}
		if request.Tool != "issue_comment_create" || request.Payload["body"] != "final public summary" || request.Payload["append_new"] != false {
			t.Errorf("final summary request=%+v", request)
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comment_id": "working-placeholder"}}, nil
	}))
	command := runtimeBridgeCommand(t, "run.start", "start-final-summary", nodeprotocol.StartRunPayload{
		RunID: "run-final-summary", IssueID: "issue", ProjectID: "project",
		IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation",
		Provider: "fake", LeaseEpoch: 1, Prompt: "work",
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, _, err := engine.Handle(ctx, command)
	if err != nil || result.Status != "completed" || calls != 1 {
		t.Fatalf("normal completion lost public summary: status=%s calls=%d err=%v", result.Status, calls, err)
	}
}
