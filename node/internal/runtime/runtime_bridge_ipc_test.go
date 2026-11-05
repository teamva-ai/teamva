package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Called from the fake adapter's actual Start/Resume callback, not while
// inspecting a command payload. Never write on a missing or wider IPC grant.
func assertExactRunIPC(t *testing.T, request adapter.RunRequest) bool {
	t.Helper()
	want, err := filepath.EvalSymlinks(filepath.Join(request.CWD, ".teamva", "tool-requests", request.RunID))
	if err != nil || request.ToolRequestDir == "" || request.ToolRequestDir != want ||
		request.ToolRequestDir == request.CWD || request.ToolRequestDir == filepath.Dir(want) {
		t.Error("Provider Start/Resume did not receive only its exact host-created Run IPC")
		return false
	}
	return true
}

func newIPCBridgeRuntime(t *testing.T) *Runtime {
	t.Helper()
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	base := t.TempDir()
	store, err := localstore.Open(t.Context(), filepath.Join(base, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine := New("computer-bridge", store, nil, filepath.Join(base, "workspaces"), nil)
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return engine
}

func runIPCBridgeCommand(t *testing.T, engine *Runtime, payload nodeprotocol.StartRunPayload) {
	t.Helper()
	command := runtimeBridgeCommand(t, "run.start", "command-"+payload.RunID, payload)
	record, replayed, err := engine.Handle(t.Context(), command)
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("bridge Run failed: status=%s replayed=%t err=%v", record.Status, replayed, err)
	}
}

func assertIPCArchive(t *testing.T, request adapter.RunRequest) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(request.ToolRequestDir, "provider-archive"))
	if err != nil || string(content) != request.RunID {
		t.Fatal("ordinary Issue Run IPC archive was removed or attributed to another Run")
	}
}

func recordExactIPC(t *testing.T, observations chan<- adapter.RunRequest) func(adapter.RunRequest) {
	return func(request adapter.RunRequest) {
		if assertExactRunIPC(t, request) {
			if err := os.WriteFile(filepath.Join(request.ToolRequestDir, "provider-archive"), []byte(request.RunID), 0o600); err != nil {
				t.Error("Provider could not use its supplied exact IPC directory")
			}
		}
		observations <- request
	}
}

func TestIssueFileBridgeExactIPCOnStartResumeAndFreshScope(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	observations := make(chan adapter.RunRequest, 3)
	provider := &fakeAdapter{onStart: recordExactIPC(t, observations)}
	engine.RegisterTestAdapter("fake", provider)
	resources := make([][]nodeprotocol.GitResourceRequest, 2)
	for index, id := range []string{"resource-a", "resource-b"} {
		root := t.TempDir()
		initRuntimeGitRepository(t, root)
		resources[index] = []nodeprotocol.GitResourceRequest{{ResourceID: id, Name: id, Path: root}}
		approveRuntimeGitResources(t, engine, resources[index])
	}
	payload := nodeprotocol.StartRunPayload{
		RunID: "issue-start", IssueID: "issue", ProjectID: "project", AgentID: "agent",
		IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation",
		Provider: "fake", LeaseEpoch: 1, Prompt: "publish a summary", GitResources: resources[0],
	}
	runIPCBridgeCommand(t, engine, payload)
	first := <-observations
	if first.NativeSessionID != "" {
		t.Fatal("initial Issue Run was not a fresh Provider Start")
	}
	payload.RunID, payload.NativeSessionID = "issue-resume", "native_1"
	runIPCBridgeCommand(t, engine, payload)
	resumed := <-observations
	if resumed.NativeSessionID != "native_1" || resumed.CWD != first.CWD || resumed.ToolRequestDir == first.ToolRequestDir {
		t.Fatal("same-scope Resume did not replace its IPC with this Run's exact directory")
	}
	payload.RunID, payload.GitResources = "issue-fresh-scope", resources[1]
	runIPCBridgeCommand(t, engine, payload)
	fresh := <-observations
	if fresh.NativeSessionID != "" || fresh.CWD == first.CWD || fresh.ToolRequestDir == resumed.ToolRequestDir {
		t.Fatal("fresh authorization scope reused the old native session, CWD or IPC grant")
	}
	provider.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.mu.Unlock()
	if starts != 2 || resumes != 1 {
		t.Fatalf("actual Provider calls: starts=%d resumes=%d", starts, resumes)
	}
	for _, request := range []adapter.RunRequest{first, resumed, fresh} {
		assertIPCArchive(t, request)
	}
}

func TestParallelIssueFileBridgesHaveExactDisjointIPCAndStopIndependently(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	observations := make(chan adapter.RunRequest, 2)
	provider := &fakeAdapter{onStart: recordExactIPC(t, observations), waitForCancel: true}
	engine.RegisterTestAdapter("fake", provider)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := func(runID, instanceID string) <-chan error {
		command := runtimeBridgeCommand(t, "run.start", "command-"+runID, nodeprotocol.StartRunPayload{
			RunID: runID, IssueID: "issue", ProjectID: "project", AgentID: "agent",
			IssueAgentInstanceID: instanceID, IssueAgentGenerationID: "generation",
			Provider: "fake", LeaseEpoch: 1, Prompt: "work",
		})
		done := make(chan error, 1)
		go func() {
			_, _, err := engine.Handle(ctx, command)
			done <- err
		}()
		return done
	}
	awaitRequest := func() adapter.RunRequest {
		t.Helper()
		select {
		case request := <-observations:
			return request
		case <-time.After(5 * time.Second):
			t.Fatal("parallel Provider did not reach Start")
			return adapter.RunRequest{}
		}
	}
	firstDone := start("parallel-a", "instance-a")
	first := awaitRequest()
	secondDone := start("parallel-b", "instance-b")
	second := awaitRequest()
	if first.CWD != second.CWD || first.ToolRequestDir == second.ToolRequestDir ||
		first.ToolRequestDir == filepath.Dir(second.ToolRequestDir) || second.ToolRequestDir == filepath.Dir(first.ToolRequestDir) {
		t.Fatal("parallel instances shared IPC authority or received a parent directory")
	}
	stop := func(request adapter.RunRequest, done <-chan error) {
		t.Helper()
		command := runtimeBridgeCommand(t, "run.stop", "stop-"+request.RunID, nodeprotocol.StopRunPayload{RunID: request.RunID, Provider: "fake"})
		if _, _, err := engine.Handle(t.Context(), command); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("stopped Provider error=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stopped Provider did not return")
		}
		assertIPCArchive(t, request)
	}
	stop(first, firstDone)
	select {
	case err := <-secondDone:
		t.Fatalf("stopping one instance also terminated the other: %v", err)
	default:
	}
	assertIPCArchive(t, second)
	stop(second, secondDone)
}

func TestDisabledIssueToolsAndScheduleHaveNoProviderIPCGrant(t *testing.T) {
	for _, issue := range []bool{true, false} {
		t.Run(map[bool]string{true: "issue_disable_tools", false: "no_issue_no_bridge"}[issue], func(t *testing.T) {
			engine := newIPCBridgeRuntime(t)
			var observed adapter.RunRequest
			provider := &fakeAdapter{onStart: func(request adapter.RunRequest) {
				observed = request
				if request.ToolRequestDir != "" {
					t.Error("Provider Start received IPC without an enabled FileBridge")
				}
			}}
			engine.RegisterTestAdapter("fake", provider)
			payload := nodeprotocol.StartRunPayload{RunID: "without-bridge", ProjectID: "project", Provider: "fake", LeaseEpoch: 1, Prompt: "work"}
			if issue {
				payload.IssueID, payload.IssueAgentInstanceID, payload.IssueAgentGenerationID = "issue", "instance", "generation"
				payload.DisableTools = true
			}
			runIPCBridgeCommand(t, engine, payload)
			if observed.ToolRequestDir != "" || strings.Contains(observed.Prompt, "issue_comment_create") {
				t.Fatal("a disabled/non-Issue bridge exposed transport or business tool instructions")
			}
			if _, err := os.Stat(filepath.Join(observed.CWD, ".teamva", "tool-requests", payload.RunID)); !os.IsNotExist(err) {
				t.Fatal("a disabled/non-Issue bridge created an IPC directory")
			}
		})
	}
}

func TestReadOnlyAssistantExactIPCOnlyCleansItsOwnTransientRun(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	observations := make(chan adapter.RunRequest, 2)
	provider := &fakeAdapter{onStart: recordExactIPC(t, observations)}
	engine.RegisterTestAdapter("fake", provider)
	runIPCBridgeCommand(t, engine, nodeprotocol.StartRunPayload{
		RunID: "ordinary-archive", IssueID: "issue", ProjectID: "project", AgentID: "agent",
		IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation",
		Provider: "fake", LeaseEpoch: 1, Prompt: "public summary",
	})
	ordinary := <-observations
	sessionID := "assistant-session"
	home := filepath.Join(engine.workspaceRoot, "home", keyDirectorySegment(sessionID))
	sibling := filepath.Join(home, ".teamva", "tool-requests", "other-run", "keep")
	writeRetentionFile(t, sibling, "other assistant Run")
	keep := filepath.Join(home, "keep-home")
	writeRetentionFile(t, keep, "assistant home")
	runIPCBridgeCommand(t, engine, nodeprotocol.StartRunPayload{
		RunID: "assistant-transient", AssistantSessionID: sessionID, AgentID: "lumi",
		Provider: "fake", LeaseEpoch: 1, Prompt: "read saved history", ReadOnly: true,
	})
	assistant := <-observations
	if !assistant.ReadOnly || assistant.IssueID != "" || assistant.CWD != home ||
		assistant.ToolRequestDir == home || assistant.ToolRequestDir == filepath.Dir(sibling) {
		t.Fatal("read-only assistant received a project/home/other-Run write grant")
	}
	if _, err := os.Stat(assistant.ToolRequestDir); !os.IsNotExist(err) {
		t.Fatal("assistant transient history IPC was retained after completion")
	}
	for path, want := range map[string]string{sibling: "other assistant Run", keep: "assistant home"} {
		if content, err := os.ReadFile(path); err != nil || string(content) != want {
			t.Fatal("assistant rooted cleanup removed its home or another Run's data")
		}
	}
	assertIPCArchive(t, ordinary)
}
