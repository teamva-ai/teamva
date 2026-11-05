package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const workPlanFixture = `{"work_items":[{"key":"one","agent_id":"a","title":"实现并验证接口","instruction":"","after":[]}]}`

func workPlanRequest() nodeprotocol.WorkPlanRequest {
	return nodeprotocol.WorkPlanRequest{ExecutionID: "draft", Provider: "fake", Model: "model", Input: "private-request", Agents: []nodeprotocol.WorkPlanAgent{{ID: "a", Name: "甲"}}}
}

func newWorkPlanRuntime(t *testing.T) *Runtime {
	t.Helper()
	engine := newIPCBridgeRuntime(t)
	engine.providerMetadata["fake"] = adapter.Metadata{ID: "fake", Features: []string{"no_tools"}}
	engine.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		t.Error("draft tried resolving MCP")
		return nil, errors.New("forbidden")
	})
	engine.SetToolCaller(toolCallerFunc(func(context.Context, nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		t.Error("draft reached a business tool")
		return nodeprotocol.NodeRequestResult{}, errors.New("forbidden")
	}))
	return engine
}

func assertWorkPlanNoDurableEffects(t *testing.T, engine *Runtime) {
	t.Helper()
	if len(engine.managers) != 0 || len(engine.runStarts) != 0 {
		t.Fatal("draft used a business manager/Run registration")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(filepath.Dir(engine.workspaceRoot), "runtime.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"inbox_commands", "event_log", "event_sequence_cursors", "native_sessions", "conversation_sessions", "run_git_snapshots"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("draft changed %s: count=%d err=%v", table, count, err)
		}
	}
	var count, bytes int
	if err := db.QueryRow("SELECT unacked_count,unacked_bytes FROM event_outbox_state").Scan(&count, &bytes); err != nil || count != 0 || bytes != 0 {
		t.Fatal("draft changed event outbox")
	}
	entries, err := os.ReadDir(engine.titleDirectories.path)
	if err != nil || len(entries) != 0 {
		t.Fatal("draft retained private attempt files")
	}
}

func TestWorkPlanAlwaysUsesFreshStrictNoToolsAndNoBusinessChannels(t *testing.T) {
	engine := newWorkPlanRuntime(t)
	project := filepath.Join(engine.workspaceRoot, "existing-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(project, "keep.txt")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	var previousRoot, previousCWD, previousGeneration string
	created := 0
	engine.titleManagerFactory = func(provider, executable, root string) *Manager {
		created++
		if root == previousRoot {
			t.Fatal("draft reused a private Manager home")
		}
		previousRoot = root
		manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
		manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
			if !request.ReadOnly || !request.DisableTools || request.ToolRequestDir != "" || request.NativeSessionID != "" || request.IssueID != "" || request.ConversationID != "" || len(request.Attachments) != 0 {
				t.Fatal("draft acquired tools, IPC, business scope or a native session")
			}
			if request.Model != "model" || strings.Contains(request.Prompt, "issue_comment_create") || strings.Contains(request.Prompt, "system_prompt") {
				t.Fatal("unexpected draft prompt/configuration")
			}
			var input struct {
				Input  string                       `json:"input"`
				Agents []nodeprotocol.WorkPlanAgent `json:"agents"`
			}
			if err := json.Unmarshal([]byte(request.Prompt), &input); err != nil || input.Input != "private-request" || len(input.Agents) != 1 || input.Agents[0].ID != "a" {
				t.Fatal("draft did not preserve explicit input and allowlist")
			}
			entries, err := os.ReadDir(request.CWD)
			if err != nil || len(entries) != 0 || request.CWD == previousCWD || request.CWD == project {
				t.Fatal("workspace was not fresh and empty")
			}
			previousCWD = request.CWD
			wantDigest, _ := instanceConfigDigest(InstanceConfig{Model: "model", SystemPrompt: workPlanSystemPrompt, ReadOnly: true, DisableTools: true})
			manager.mu.Lock()
			for key, instance := range manager.instances {
				if key.Generation == previousGeneration || key.ProjectID != "" || key.GitScopeHash != "" || instance.session != "" || instance.configDigest != wantDigest || manager.store != nil || manager.brokers != nil {
					t.Error("draft instance acquired non-draft configuration or persistence")
				}
				previousGeneration = key.Generation
			}
			manager.mu.Unlock()
			return emitTitle(ctx, sink, workPlanFixture)
		}})
		return manager
	}
	for range 2 {
		result, err := engine.GenerateWorkPlan(nil, workPlanRequest())
		if err != nil || len(result.WorkItems) != 1 || result.WorkItems[0].AgentID != "a" || result.WorkItems[0].Instruction != "" {
			t.Fatalf("draft failed: %v", err)
		}
		if _, err := os.Stat(previousRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("private native home survived completion")
		}
		assertWorkPlanNoDurableEffects(t, engine)
	}
	if created != 2 {
		t.Fatal("draft did not create independent attempts")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unchanged" {
		t.Fatal("draft touched business files")
	}
	entries, err := os.ReadDir(project)
	if err != nil || len(entries) != 1 {
		t.Fatal("draft prepared a business workspace or IPC")
	}
}

func TestWorkPlanRejectsUnsupportedProvidersBeforeAnyFactory(t *testing.T) {
	for _, provider := range []string{"codex", "unknown", "fake"} {
		t.Run(provider, func(t *testing.T) {
			engine := newWorkPlanRuntime(t)
			engine.providerMetadata["fake"] = adapter.Metadata{ID: "fake", Features: []string{"read_only"}}
			engine.titleManagerFactory = func(string, string, string) *Manager {
				t.Fatal("unsupported Provider reached config/native factory")
				return nil
			}
			request := workPlanRequest()
			request.Provider = provider
			result, err := engine.GenerateWorkPlan(t.Context(), request)
			if !errors.Is(err, errNoToolsUnsupported) || len(result.WorkItems) != 0 {
				t.Fatal("unsupported draft was accepted or downgraded")
			}
			assertWorkPlanNoDurableEffects(t, engine)
		})
	}
}

func TestInvalidWorkPlanRequestNeverCreatesProvider(t *testing.T) {
	engine := newWorkPlanRuntime(t)
	engine.titleManagerFactory = func(string, string, string) *Manager { t.Fatal("invalid request created a Provider"); return nil }
	for _, change := range []func(*nodeprotocol.WorkPlanRequest){
		func(r *nodeprotocol.WorkPlanRequest) { r.ExecutionID = " " },
		func(r *nodeprotocol.WorkPlanRequest) { r.Provider = "../invalid" },
		func(r *nodeprotocol.WorkPlanRequest) { r.Input = "" },
		func(r *nodeprotocol.WorkPlanRequest) {
			r.Input = strings.Repeat("x", nodeprotocol.MaxWorkPlanInputBytes+1)
		},
		func(r *nodeprotocol.WorkPlanRequest) { r.Agents = nil },
		func(r *nodeprotocol.WorkPlanRequest) { r.Agents = append(r.Agents, r.Agents[0]) },
		func(r *nodeprotocol.WorkPlanRequest) { r.Model = string([]byte{0xff}) },
	} {
		request := workPlanRequest()
		change(&request)
		result, err := engine.GenerateWorkPlan(t.Context(), request)
		if !errors.Is(err, errTitleGeneration) || len(result.WorkItems) != 0 {
			t.Fatal("invalid request accepted or leaked diagnostics")
		}
	}
	assertWorkPlanNoDurableEffects(t, engine)
}

func TestWorkPlanOutputFailuresStayGenericAndPrivate(t *testing.T) {
	for name, run := range map[string]func(context.Context, adapter.Sink) (adapter.NativeSession, error){
		"invalid-output": func(ctx context.Context, sink adapter.Sink) (adapter.NativeSession, error) {
			return emitTitle(ctx, sink, "private-model-output "+workPlanFixture)
		},
		"provider-error": func(context.Context, adapter.Sink) (adapter.NativeSession, error) {
			return adapter.NativeSession{}, errors.New("private-model-output private-request")
		},
		"no-terminal": func(ctx context.Context, sink adapter.Sink) (adapter.NativeSession, error) {
			return adapter.NativeSession{}, sink(ctx, adapter.Event{Type: "output.snapshot", Payload: map[string]any{"text": workPlanFixture, "final": true}})
		},
		"failed-terminal": func(ctx context.Context, sink adapter.Sink) (adapter.NativeSession, error) {
			_, _ = emitTitle(ctx, sink, workPlanFixture)
			return adapter.NativeSession{}, sink(ctx, adapter.Event{Type: "run.finished", Payload: map[string]any{"status": "failed", "reason": "private-request"}})
		},
		"oversized": func(ctx context.Context, sink adapter.Sink) (adapter.NativeSession, error) {
			return emitTitle(ctx, sink, strings.Repeat("x", nodeprotocol.MaxWorkPlanOutputBytes+1))
		},
		"unknown-agent": func(ctx context.Context, sink adapter.Sink) (adapter.NativeSession, error) {
			return emitTitle(ctx, sink, strings.ReplaceAll(workPlanFixture, `"agent_id":"a"`, `"agent_id":"foreign"`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newWorkPlanRuntime(t)
			engine.titleManagerFactory = func(provider, executable, root string) *Manager {
				manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
					return run(ctx, sink)
				}})
				return manager
			}
			result, err := engine.GenerateWorkPlan(t.Context(), workPlanRequest())
			if !errors.Is(err, errTitleGeneration) || err.Error() != "generation_failed" || len(result.WorkItems) != 0 {
				t.Fatalf("failure leaked or succeeded: %v", err)
			}
			assertWorkPlanNoDurableEffects(t, engine)
		})
	}
}

func TestWorkPlanUnexpectedToolEventsCancelAndCannotBecomeSuccess(t *testing.T) {
	for _, event := range []adapter.Event{
		{Type: "provider.tool", Payload: map[string]any{"tool": "issue_comment_create", "status": "running"}},
		{Type: "provider.event", Payload: map[string]any{"kind": "tool", "status": "completed"}},
		{Type: "session.tool.success"}, {Type: "files.changed"}, {Type: "workspace.diff"},
	} {
		t.Run(event.Type, func(t *testing.T) {
			engine := newWorkPlanRuntime(t)
			engine.titleManagerFactory = func(provider, executable, root string) *Manager {
				manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
					if err := sink(ctx, event); err == nil {
						t.Error("unexpected tool event was ignored")
					}
					<-ctx.Done()
					// Even a broken adapter ignoring sink errors cannot restore success.
					return emitTitle(ctx, sink, workPlanFixture)
				}})
				return manager
			}
			result, err := engine.GenerateWorkPlan(t.Context(), workPlanRequest())
			if !errors.Is(err, errTitleGeneration) || len(result.WorkItems) != 0 {
				t.Fatal("tool contract violation became a successful draft")
			}
			assertWorkPlanNoDurableEffects(t, engine)
		})
	}
}

func TestWorkPlanAllowlistIsFrozenAndOutputCanExceedTitleLimit(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "larger-than-title", true: "frozen-allowlist"}[mutate], func(t *testing.T) {
			engine := newWorkPlanRuntime(t)
			request := workPlanRequest()
			engine.titleManagerFactory = func(provider, executable, root string) *Manager {
				manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
					raw := strings.Replace(workPlanFixture, `"instruction":""`, `"instruction":"`+strings.Repeat("x", 5000)+`"`, 1)
					if mutate {
						request.Agents[0].ID = "foreign"
						raw = strings.Replace(raw, `"agent_id":"a"`, `"agent_id":"foreign"`, 1)
					}
					return emitTitle(ctx, sink, raw)
				}})
				return manager
			}
			result, err := engine.GenerateWorkPlan(t.Context(), request)
			if mutate {
				if !errors.Is(err, errTitleGeneration) || len(result.WorkItems) != 0 {
					t.Fatal("request mutation broadened immutable allowlist")
				}
			} else if err != nil || len(result.WorkItems) != 1 || len(result.WorkItems[0].Instruction) != 5000 {
				t.Fatalf("draft incorrectly used title output bound: %v", err)
			}
		})
	}
}

func TestWorkPlanCancellationAndRuntimeCloseJoinCleanup(t *testing.T) {
	for _, mode := range []string{"caller", "deadline", "runtime"} {
		t.Run(mode, func(t *testing.T) {
			engine := newWorkPlanRuntime(t)
			started, closing, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			engine.titleManagerFactory = func(provider, executable, root string) *Manager {
				manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(context.Context, adapter.RunRequest, adapter.Sink) (adapter.NativeSession, error) {
					close(started)
					<-closing
					return adapter.NativeSession{}, errors.New("private-request")
				}, closeTitle: func() error { once.Do(func() { close(closing) }); <-release; return nil }})
				return manager
			}
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := engine.GenerateWorkPlan(ctx, workPlanRequest()); result <- err }()
			select {
			case <-started:
			case <-closing:
				close(release)
				<-result
				t.Fatal("Provider preparation exhausted the test deadline")
			}
			closed := make(chan error, 1)
			if mode == "runtime" {
				go func() { closed <- engine.Close() }()
			} else if mode == "caller" {
				cancel()
			}
			<-closing
			select {
			case <-result:
				t.Error("draft returned before Provider cleanup")
			case <-closed:
				t.Error("Runtime.Close returned before draft cleanup")
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			want := context.Canceled
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if err := <-result; !errors.Is(err, want) {
				t.Fatalf("safe cancellation lost: %v", err)
			}
			if mode == "runtime" {
				if err := <-closed; err != nil {
					t.Fatal(err)
				}
				if _, err := engine.GenerateWorkPlan(t.Context(), workPlanRequest()); !errors.Is(err, context.Canceled) {
					t.Fatal("closed Runtime accepted new draft")
				}
			} else {
				assertWorkPlanNoDurableEffects(t, engine)
			}
		})
	}
}
