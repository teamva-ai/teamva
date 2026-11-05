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

type titleTestAdapter struct {
	*fakeAdapter
	startTitle func(context.Context, adapter.RunRequest, adapter.Sink) (adapter.NativeSession, error)
	closeTitle func() error
}

func (f *titleTestAdapter) Start(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
	return f.startTitle(ctx, request, sink)
}

func (f *titleTestAdapter) Close() error {
	if f.closeTitle != nil {
		return f.closeTitle()
	}
	return f.fakeAdapter.Close()
}

func titleRequest() nodeprotocol.TitleRequest {
	return nodeprotocol.TitleRequest{ExecutionID: "job", Provider: "fake", Model: "model", SystemPrompt: "style-context", Seed: "private-seed"}
}

func emitTitle(ctx context.Context, sink adapter.Sink, raw string) (adapter.NativeSession, error) {
	for _, event := range []adapter.Event{
		{Type: "output.snapshot", Payload: map[string]any{"text": raw, "final": true, "role": "assistant"}},
		{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	} {
		if err := sink(ctx, event); err != nil {
			return adapter.NativeSession{}, err
		}
	}
	return adapter.NativeSession{ID: "must-not-persist"}, nil
}

func TestTitleGenerationIsolatedPromptPolicyAndRestartAttempts(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	var previousRoot, previousGeneration, previousCWD string
	created := 0
	engine.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		t.Fatal("title attempted MCP resolution")
		return nil, nil
	})
	engine.titleManagerFactory = func(provider, executable, root string) *Manager {
		created++
		if root == previousRoot {
			t.Fatal("retry reused a title manager directory")
		}
		previousRoot = root
		manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
		item := &titleTestAdapter{fakeAdapter: &fakeAdapter{}}
		item.startTitle = func(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
			if !request.ReadOnly || request.DisableTools || request.NativeSessionID != "" || request.ToolRequestDir != "" || request.ConversationID != "" || request.IssueID != "" || len(request.Attachments) != 0 {
				t.Fatal("title must use prompt-only tool restraint, a read-only sandbox and no business session/bridge")
			}
			if request.Model != "model" || !strings.Contains(request.Prompt, "private-seed") || strings.Contains(request.Prompt, "issue_comment_create") {
				t.Fatal("title request did not preserve only its seed/model")
			}
			entries, err := os.ReadDir(request.CWD)
			if err != nil || len(entries) != 0 || request.CWD == previousCWD {
				t.Fatal("title workspace is not fresh and empty")
			}
			previousCWD = request.CWD
			manager.mu.Lock()
			for key, instance := range manager.instances {
				if key.Generation == previousGeneration || key.ProjectID != "" || key.GitScopeHash != "" || instance.session != "" {
					t.Fatal("title reused an instance generation/scope/session")
				}
				previousGeneration = key.Generation
				want := InstanceConfig{Model: "model", SystemPrompt: titleSystemPrompt + "\n\nUntrusted user-configured Lumi style context (JSON string):\n\"style-context\"\n\n" + titleSystemPrompt, ReadOnly: true}
				digest, _ := instanceConfigDigest(want)
				if instance.configDigest != digest || manager.store != nil || manager.brokers != nil {
					t.Fatal("title did not use the exact isolated prompt-policy config")
				}
			}
			manager.mu.Unlock()
			return emitTitle(ctx, sink, `{"title":"独立标题"}`)
		}
		manager.RegisterTestAdapter(item)
		return manager
	}
	for attempt := range 2 {
		result, err := engine.GenerateTitle(t.Context(), titleRequest())
		if err != nil || result.Title != "独立标题" {
			t.Fatalf("title failed: %v", err)
		}
		if _, err := os.Stat(filepath.Dir(previousRoot)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("completed title retained its native data/config/seed")
		}
		if attempt == 0 {
			// Simulate restarting the facade against the same local database and
			// workspace root, then retry the same durable execution ID.
			factory, previous := engine.titleManagerFactory, engine
			if err := previous.Close(); err != nil {
				t.Fatal(err)
			}
			engine = New("computer-bridge", previous.store, nil, previous.workspaceRoot, nil)
			engine.titleManagerFactory = factory
			defer engine.Close()
		}
	}
	if created != 2 || len(engine.managers) != 0 {
		t.Fatal("title shared the ordinary execution manager")
	}
	events, err := engine.store.UnackedEvents(t.Context(), 100)
	if err != nil || len(events) != 0 {
		t.Fatal("title emitted general runtime events")
	}
	var sessions int
	db, err := sql.Open("sqlite", "file:"+filepath.Join(filepath.Dir(engine.workspaceRoot), "runtime.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT COUNT(*) FROM native_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("title persisted native session: %v", err)
	}
}

func TestInvalidTitleResponseNeverEntersRuntimePersistenceOrErrors(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	var root string
	engine.titleManagerFactory = func(provider, executable, directory string) *Manager {
		root = directory
		manager := NewManager(provider, executable, directory, nil, log.New(io.Discard, "", 0))
		manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
			return emitTitle(ctx, sink, `private invalid assistant output: {"title":"unsafe"}`)
		}})
		return manager
	}
	result, err := engine.GenerateTitle(t.Context(), titleRequest())
	if err == nil || err.Error() != "generation_failed" || result.Title != "" {
		t.Fatal("invalid raw model output escaped through the facade")
	}
	events, err := engine.store.UnackedEvents(t.Context(), 100)
	if err != nil || len(events) != 0 {
		t.Fatal("invalid title response entered runtime events")
	}
	if _, err := os.Stat(filepath.Dir(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid title retained its private attempt data")
	}
}

func TestDecodeTitleStrictJSON(t *testing.T) {
	valid := []string{`{"title":"简短标题"}`, " {\"title\":\"" + strings.Repeat("题", 60) + "\"}\n"}
	for _, raw := range valid {
		if _, err := decodeTitle(raw); err != nil {
			t.Fatal("valid title rejected")
		}
	}
	invalid := []string{
		`{"title":""}`, `{"title":" "}`, `{"title":null}`, `{"title":1}`, `{}`, `[]`, `"title"`,
		`{"title":"a","title":"b"}`, `{"title":"a","extra":true}`, `{"extra":true,"title":"a"}`,
		`{"title":"a"} {"title":"b"}`, `{"title":"line\nbreak"}`, `{"title":"line\rbreak"}`,
		`{"title":"control\u0000"}`, `{"title":"bidi\u202e"}`, `{"title":"bidi\u2066"}`, `{"title":"line\u2028"}`,
		`{"title":"<b>HTML</b>"}`, `{"title":"&lt;b&gt;HTML&lt;/b&gt;"}`, "```json\n{\"title\":\"a\"}\n```", "{\"title\":\"```a```\"}",
		`{"title":"~~~code~~~"}`, `{"title":"\ud800"}`, "{\"title\":\"" + strings.Repeat("题", 61) + "\"}",
		strings.Repeat(" ", maxTitleResponseBytes) + `{"title":"a"}`, "{\"title\":\"\xff\"}",
	}
	for index, raw := range invalid {
		if title, err := decodeTitle(raw); err == nil || title != "" {
			t.Fatalf("invalid title accepted at case %d", index)
		}
	}
}

func TestTitleOutputAcceptsOnlyCompletedAssistantFinalText(t *testing.T) {
	for _, mode := range []string{"reasoning", "tool", "partial", "failed", "interrupted", "missing-terminal", "oversized", "multipart-noise", "user", "empty-final"} {
		t.Run(mode, func(t *testing.T) {
			output := newTitleOutput("opencode")
			payload := map[string]any{"message_id": "assistant", "part_id": "p", "kind": "text", "text": `{"title":"valid"}`}
			event := adapter.Event{Type: "provider.event", Payload: payload}
			switch mode {
			case "reasoning", "tool":
				payload["kind"] = mode
			case "partial":
				event.Type = "output.delta"
			case "oversized":
				payload["text"] = strings.Repeat(" ", maxTitleResponseBytes+1)
			case "multipart-noise":
				_ = output.accept(t.Context(), adapter.Event{Type: "provider.event", Payload: map[string]any{"kind": "text", "message_id": "assistant", "part_id": "explanation", "text": "explanation"}})
			case "empty-final":
				_ = output.accept(t.Context(), event)
				payload["message_id"], payload["text"] = "new-empty-assistant", ""
			case "user":
				output = newTitleOutput("fake")
				event.Type, payload["role"], payload["final"] = "output.snapshot", "user", true
			}
			_ = output.accept(t.Context(), event)
			if mode != "missing-terminal" {
				status := "completed"
				if mode == "failed" || mode == "interrupted" {
					status = mode
				}
				_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": status}})
			}
			raw, err := output.finalText()
			if err == nil {
				_, err = decodeTitle(raw)
			}
			if err == nil {
				t.Fatal("non-final assistant text was accepted")
			}
		})
	}
	output := newTitleOutput("opencode")
	for _, event := range []adapter.Event{
		{Type: "provider.event", Payload: map[string]any{"kind": "reasoning", "text": "private reasoning"}},
		{Type: "output.delta", Payload: map[string]any{"message_id": "answer", "part_id": "text", "text": `{"title":"part`}},
		{Type: "provider.event", Payload: map[string]any{"kind": "text", "message_id": "answer", "part_id": "text", "text": `{"title":"最终标题"}`}},
		{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	} {
		if err := output.accept(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := output.finalText()
	if err != nil || raw != `{"title":"最终标题"}` {
		t.Fatal("authoritative assistant text snapshot not selected")
	}
	for _, phase := range []string{"commentary", "final_answer"} {
		output := newTitleOutput("codex")
		_ = output.accept(t.Context(), adapter.Event{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": phase, "text": `{"title":"final"}`}}})
		_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
		_, err := output.finalText()
		if (err == nil) != (phase == "final_answer") {
			t.Fatal("provider commentary was mistaken for an explicit final answer")
		}
	}
}

func TestTitleCancellationAndRuntimeCloseJoinProviderCleanup(t *testing.T) {
	for _, runtimeClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller-cancel", true: "runtime-close"}[runtimeClose], func(t *testing.T) {
			engine := newIPCBridgeRuntime(t)
			started, closing, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			engine.titleManagerFactory = func(provider, executable, root string) *Manager {
				manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{},
					startTitle: func(context.Context, adapter.RunRequest, adapter.Sink) (adapter.NativeSession, error) {
						close(started)
						// A deliberately misbehaving Start only returns after Close.
						<-closing
						return adapter.NativeSession{}, errors.New("private-seed must not leak")
					},
					closeTitle: func() error { once.Do(func() { close(closing) }); <-release; return nil },
				})
				return manager
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := engine.GenerateTitle(ctx, titleRequest()); result <- err }()
			<-started
			closed := make(chan error, 1)
			if runtimeClose {
				go func() { closed <- engine.Close() }()
			} else {
				cancel()
			}
			<-closing
			select {
			case <-result:
				t.Fatal("GenerateTitle returned before provider cleanup")
			case <-closed:
				t.Fatal("Runtime.Close returned before title cleanup")
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal("title cancellation did not return a safe cancellation error")
			}
			if runtimeClose {
				if err := <-closed; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTitleCodexPromptPolicyReachesNativeTurnWithoutFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	directory := t.TempDir()
	methodsPath := filepath.Join(directory, "native-methods")
	executable := filepath.Join(directory, "fake-codex-title")
	// Exercise the real Codex adapter/configuration and native JSON-RPC path,
	// not an adapter that would silently accept DisableTools. This local shell
	// fixture never contacts a Provider or account and logs method names only.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=${line#*\"id\":}
  id=${id%%,*}
  case "$line" in
    *'"method":"initialize"'*)
      printf 'initialize\n' >> "$TEAMVA_TITLE_METHODS"
      printf '{"id":%s,"result":{"userAgent":"teamva/0.163.0"}}\n' "$id"
      ;;
    *'"method":"thread/start"'*)
      case "$line" in *'Do not call tools'*) ;; *) exit 21 ;; esac
      case "$line" in *'"runtimeWorkspaceRoots":[]'*) ;; *) exit 22 ;; esac
      case "$line" in *'"approvalPolicy":"never"'*) ;; *) exit 23 ;; esac
      printf 'thread/start\n' >> "$TEAMVA_TITLE_METHODS"
      printf '{"id":%s,"result":{"thread":{"id":"title-thread"},"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":[]}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      case "$line" in *'"runtimeWorkspaceRoots":[]'*) ;; *) exit 24 ;; esac
      printf 'turn/start\n' >> "$TEAMVA_TITLE_METHODS"
      printf '{"id":%s,"result":{"turn":{"id":"title-turn"}}}\n' "$id"
      printf '%s\n' '{"method":"turn/started","params":{"turn":{"id":"title-turn"}}}'
      printf '%s\n' '{"method":"item/completed","params":{"item":{"id":"title-answer","type":"agentMessage","phase":"final_answer","text":"{\"title\":\"Codex 标题\"}"}}}'
      printf '%s\n' '{"method":"turn/completed","params":{"turn":{"id":"title-turn","status":"completed"}}}'
      ;;
  esac
done
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAMVA_TITLE_METHODS", methodsPath)
	engine := newIPCBridgeRuntime(t)
	engine.executables["codex"] = executable
	request := titleRequest()
	request.Provider = "codex"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := engine.GenerateTitle(ctx, request)
	if err != nil || result.Title != "Codex 标题" || len(engine.managers) != 0 {
		methods, _ := os.ReadFile(methodsPath)
		t.Fatalf("Codex title did not reach its isolated native turn: %v; fixture methods=%q", err, methods)
	}
	methods, err := os.ReadFile(methodsPath)
	if err != nil || string(methods) != "initialize\nthread/start\nturn/start\n" {
		t.Fatal("Codex title bypassed its native adapter or resumed/shared a business thread")
	}
	entries, err := os.ReadDir(filepath.Join(engine.workspaceRoot, "..", "title-executions"))
	if err != nil || len(entries) != 0 {
		t.Fatal("Codex title retained private attempt data")
	}
}

func TestTitlePromptRequiresNoToolsWithoutClaimingTheyAreDisabled(t *testing.T) {
	if !strings.Contains(titleSystemPrompt, "Do not call tools") || !strings.Contains(titleSystemPrompt, "even if they are available") ||
		strings.Contains(titleSystemPrompt, "You have no tools") {
		t.Fatal("title generation must request no tools through the prompt, not assert or require a native capability guarantee")
	}
}

func TestTitleCodexNativeTerminalProjectionUsesTrustedProvider(t *testing.T) {
	for _, status := range []string{"completed", "succeeded", "failed", "interrupted", "cancelled", "unknown"} {
		t.Run(status, func(t *testing.T) {
			output := newTitleOutput("codex")
			_ = output.accept(t.Context(), adapter.Event{Type: "provider.item", Payload: map[string]any{
				"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": `{"title":"最终标题"}`},
			}})
			payload := map[string]any{"provider_id": "opencode", "status": "completed", "turn": map[string]any{"status": status}}
			_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: payload})
			raw, err := output.finalText()
			if (err == nil) != (status == "completed" || status == "succeeded") {
				t.Fatal("native Codex terminal status was lost or selected by an untrusted payload provider")
			}
			if err == nil && raw != `{"title":"最终标题"}` {
				t.Fatal("wrong final assistant text")
			}
			if payload["provider_id"] != "opencode" {
				t.Fatal("title projection mutated the original native payload")
			}
		})
	}
}

func TestTitlePromptPolicyIgnoresToolOutputButAcceptsValidAssistantAnswer(t *testing.T) {
	output := newTitleOutput("codex")
	for _, event := range []adapter.Event{
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"id": "native-tool", "type": "commandExecution", "text": `{"title":"工具结果不是标题"}`}}},
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": `{"title":"真实最终标题"}`}}},
		{Type: "run.completed", Payload: map[string]any{"turn": map[string]any{"status": "completed"}}},
	} {
		if err := output.accept(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := output.finalText()
	if err != nil || raw != `{"title":"真实最终标题"}` {
		t.Fatal("prompt-only tool restraint was turned back into strict NoTools or tool text became the title")
	}
}

// These are the normalized assistant semantics emitted by the OpenCode adapter,
// not raw native events or user/tool payloads containing text-shaped fields.
func titleSemanticEvent(kind, messageID, partID, field, text string) adapter.Event {
	return adapter.Event{Type: "provider.event", Payload: map[string]any{
		"kind": kind, "message_id": messageID, "part_id": partID, field: text,
	}}
}

func titleTextEnded(messageID, partID, text string) adapter.Event {
	return titleSemanticEvent("text", messageID, partID, "text", text)
}

func TestTitleOutputEmptyPartsAndLatestAssistantOwnership(t *testing.T) {
	const answer = `{"title":"最新标题"}`
	const earlier = `{"title":"较早标题"}`
	for _, test := range []struct {
		name   string
		events []adapter.Event
		want   string
	}{
		{name: "empty first part then valid answer", events: []adapter.Event{
			titleTextEnded("a", "p0", ""), titleTextEnded("a", "p1", answer),
		}, want: answer},
		{name: "empty last part does not erase other parts", events: []adapter.Event{
			titleTextEnded("a", "p0", answer), titleTextEnded("a", "p1", ""),
		}, want: answer},
		{name: "authoritative empty clears stale delta before another part", events: []adapter.Event{
			{Type: "output.delta", Payload: map[string]any{"message_id": "a", "part_id": "p0", "text": earlier}},
			titleTextEnded("a", "p0", ""), titleTextEnded("a", "p1", answer),
		}, want: answer},
		{name: "authoritative empty clears earlier completed part", events: []adapter.Event{
			titleTextEnded("a", "p0", earlier), titleTextEnded("a", "p0", ""), titleTextEnded("a", "p1", answer),
		}, want: answer},
		{name: "empty can be replaced by a later authoritative nonempty snapshot", events: []adapter.Event{
			titleTextEnded("a", "p0", ""), titleTextEnded("a", "p0", answer),
		}, want: answer},
		{name: "empty replacement leaves no final answer", events: []adapter.Event{
			titleTextEnded("a", "p0", answer), titleTextEnded("a", "p0", ""),
		}},
		{name: "latest empty message invalidates old JSON", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleTextEnded("new", "p0", ""),
		}},
		{name: "latest message can fill another part after an empty block", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleTextEnded("new", "p0", ""), titleTextEnded("new", "p1", answer),
		}, want: answer},
		{name: "latest reasoning delta invalidates old JSON", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleSemanticEvent("reasoning", "new", "r0", "delta", "private reasoning"),
		}},
		{name: "latest reasoning ended invalidates old JSON", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleSemanticEvent("reasoning", "new", "r0", "text", "private reasoning"),
		}},
		{name: "latest empty reasoning ended invalidates old JSON", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleSemanticEvent("reasoning", "new", "r0", "text", ""),
		}},
		{name: "reasoning within current message keeps its answer", events: []adapter.Event{
			titleTextEnded("a", "p0", answer), titleSemanticEvent("reasoning", "a", "r0", "text", "private reasoning"),
		}, want: answer},
		{name: "new reasoning message can later supply its own answer", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleSemanticEvent("reasoning", "new", "r0", "delta", "private reasoning"), titleTextEnded("new", "p0", answer),
		}, want: answer},
		{name: "new reasoning and incomplete text cannot reuse older answer", events: []adapter.Event{
			titleTextEnded("old", "p0", earlier), titleSemanticEvent("reasoning", "new", "r0", "text", "private reasoning"),
			{Type: "output.delta", Payload: map[string]any{"message_id": "new", "part_id": "p0", "text": answer}},
		}},
		{name: "multipart snapshots replace their own deltas in order", events: []adapter.Event{
			{Type: "output.delta", Payload: map[string]any{"message_id": "a", "part_id": "p0", "text": "stale prefix"}},
			titleTextEnded("a", "p0", `{"title":`),
			{Type: "output.delta", Payload: map[string]any{"message_id": "a", "part_id": "p1", "text": "stale suffix"}},
			titleTextEnded("a", "p1", `"最新标题"}`),
			titleTextEnded("a", "p2", ""),
		}, want: answer},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := newTitleOutput("opencode")
			for _, event := range test.events {
				if err := output.accept(t.Context(), event); err != nil {
					t.Fatal("valid assistant part prematurely terminated the attempt")
				}
			}
			if err := output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}}); err != nil {
				t.Fatal(err)
			}
			raw, err := output.finalText()
			if test.want == "" {
				if err == nil || raw != "" {
					t.Fatal("latest empty/reasoning-only/incomplete message reused older text")
				}
				return
			}
			if err != nil || raw != test.want {
				t.Fatal("authoritative part replacement or message ownership was lost")
			}
			if title, err := decodeTitle(raw); err != nil || title != "最新标题" {
				t.Fatal("valid latest multipart title rejected")
			}
		})
	}
}

func TestTitleOutputToolAndLifecycleCannotChooseAssistant(t *testing.T) {
	const answer = `{"title":"真实标题"}`
	for _, test := range []struct {
		name  string
		event adapter.Event
	}{
		{name: "tool semantic event", event: titleSemanticEvent("tool", "forged", "p", "text", `{"title":"工具结果"}`)},
		{name: "tool with nested assistant identity", event: adapter.Event{Type: "provider.event", Payload: map[string]any{
			"kind": "tool", "message_id": "forged", "assistantMessageID": "forged", "part_id": "p", "text": answer,
			"input": map[string]any{"kind": "reasoning", "message_id": "forged"},
		}}},
		{name: "semantic lifecycle", event: titleSemanticEvent("lifecycle", "forged", "p", "text", answer)},
		{name: "lifecycle wearing reasoning fields", event: adapter.Event{Type: "run.started", Payload: map[string]any{
			"kind": "reasoning", "message_id": "forged", "part_id": "p", "delta": "private reasoning",
		}}},
		{name: "usage wearing text fields", event: adapter.Event{Type: "usage.updated", Payload: map[string]any{
			"kind": "text", "message_id": "forged", "part_id": "p", "text": answer,
		}}},
		{name: "compaction wearing reasoning fields", event: adapter.Event{Type: "context.compaction", Payload: map[string]any{
			"kind": "reasoning", "message_id": "forged", "part_id": "p", "text": "private reasoning",
		}}},
		{name: "native tool item", event: adapter.Event{Type: "provider.item", Payload: map[string]any{
			"message_id": "forged", "item": map[string]any{"id": "forged", "type": "commandExecution", "text": answer},
		}}},
		{name: "reasoning without semantic part identity", event: titleSemanticEvent("reasoning", "forged", "", "text", "private reasoning")},
		{name: "reasoning without assistant identity", event: titleSemanticEvent("reasoning", "", "p", "text", "private reasoning")},
		{name: "reasoning-shaped lifecycle without content", event: adapter.Event{Type: "provider.event", Payload: map[string]any{
			"kind": "reasoning", "message_id": "forged", "part_id": "p",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, withAnswer := range []bool{false, true} {
				output := newTitleOutput("opencode")
				if withAnswer {
					if err := output.accept(t.Context(), titleTextEnded("assistant", "p", answer)); err != nil {
						t.Fatal(err)
					}
				}
				owner := output.messageID
				if err := output.accept(t.Context(), test.event); err != nil {
					t.Fatal("tool/lifecycle payload terminated an unrelated assistant answer")
				}
				if output.messageID != owner {
					t.Fatal("tool/lifecycle fields changed assistant ownership")
				}
				_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
				raw, err := output.finalText()
				if withAnswer && (err != nil || raw != answer) {
					t.Fatal("tool/lifecycle payload replaced a genuine assistant answer")
				}
				if !withAnswer && err == nil {
					t.Fatal("tool/lifecycle text was accepted as an assistant answer")
				}
			}
		})
	}
}

func TestTitleOutputSemanticOwnershipRequiresOwningProjector(t *testing.T) {
	const answer = `{"title":"原助手答案"}`
	for _, provider := range []string{"codex", "fake"} {
		t.Run(provider, func(t *testing.T) {
			output := newTitleOutput(provider)
			event := adapter.Event{Type: "output.snapshot", Payload: map[string]any{"message_id": "assistant", "text": answer, "final": true, "role": "assistant"}}
			if provider == "codex" {
				event = adapter.Event{Type: "provider.item", Payload: map[string]any{
					"item": map[string]any{"id": "assistant", "type": "agentMessage", "phase": "final_answer", "text": answer},
				}}
			}
			if err := output.accept(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			for _, event := range []adapter.Event{
				titleSemanticEvent("reasoning", "forged", "r0", "text", "not this provider's assistant event"),
				titleTextEnded("forged", "p0", ""),
			} {
				if err := output.accept(t.Context(), event); err != nil {
					t.Fatal("foreign semantic payload terminated the owning provider's answer")
				}
				if output.messageID != "assistant" {
					t.Fatal("foreign semantic payload changed assistant ownership")
				}
			}
			_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed", "turn": map[string]any{"status": "completed"}}})
			if raw, err := output.finalText(); err != nil || raw != answer {
				t.Fatal("foreign semantic payload replaced the owning provider's answer")
			}
		})
	}
}

func TestTitleOutputEmptyPartsRetainResponseBounds(t *testing.T) {
	const answer = `{"title":"bounded"}`
	for _, test := range []struct {
		name       string
		emptyParts int
		bytes      int
		wantValid  bool
	}{
		{name: "32 parts including empty snapshots", emptyParts: 31, bytes: len(answer), wantValid: true},
		{name: "33 parts including empty snapshots", emptyParts: 32, bytes: len(answer)},
		{name: "4096 byte response after empty part", emptyParts: 1, bytes: maxTitleResponseBytes, wantValid: true},
		{name: "4097 byte response after empty part", emptyParts: 1, bytes: maxTitleResponseBytes + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := newTitleOutput("opencode")
			for part := 0; part < test.emptyParts; part++ {
				if err := output.accept(t.Context(), titleTextEnded("a", strings.Repeat("p", part+1), "")); err != nil {
					t.Fatal("bounded empty part must remain eligible for later answer")
				}
			}
			raw := answer + strings.Repeat(" ", test.bytes-len(answer))
			acceptErr := output.accept(t.Context(), titleTextEnded("a", "answer", raw))
			_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
			final, finalErr := output.finalText()
			if test.wantValid {
				if acceptErr != nil || finalErr != nil || final != raw {
					t.Fatal("in-bounds final response rejected")
				}
			} else if acceptErr == nil || finalErr == nil || final != "" {
				t.Fatal("empty snapshots bypassed the part/byte limits")
			}
		})
	}
	for _, split := range []bool{false, true} {
		output := newTitleOutput("opencode")
		_ = output.accept(t.Context(), titleTextEnded("a", "empty", ""))
		if split {
			_ = output.accept(t.Context(), titleTextEnded("a", "p0", strings.Repeat(" ", maxTitleResponseBytes)))
			_ = output.accept(t.Context(), titleTextEnded("a", "p1", answer))
		} else {
			_ = output.accept(t.Context(), adapter.Event{Type: "output.delta", Payload: map[string]any{"message_id": "a", "part_id": "p", "text": strings.Repeat(" ", maxTitleResponseBytes)}})
			if err := output.accept(t.Context(), adapter.Event{Type: "output.delta", Payload: map[string]any{"message_id": "a", "part_id": "p", "text": answer}}); err == nil {
				t.Fatal("oversized delta accumulation was not rejected")
			}
			// A subsequent authoritative empty block cannot revive a limit violation.
			_ = output.accept(t.Context(), titleTextEnded("a", "p", ""))
			_ = output.accept(t.Context(), titleTextEnded("a", "p1", answer))
		}
		_ = output.accept(t.Context(), adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
		if _, err := output.finalText(); err == nil {
			t.Fatal("aggregate/delta response size limit was bypassed")
		}
	}
}

func TestTitleSemanticMessagesStillCleanPrivateAttempts(t *testing.T) {
	for _, reasoningOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty part followed by answer", true: "new reasoning-only message"}[reasoningOnly], func(t *testing.T) {
			engine := newIPCBridgeRuntime(t)
			var root string
			engine.titleManagerFactory = func(provider, executable, directory string) *Manager {
				root = directory
				manager := NewManager(provider, executable, directory, nil, log.New(io.Discard, "", 0))
				manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, request adapter.RunRequest, sink adapter.Sink) (adapter.NativeSession, error) {
					events := []adapter.Event{titleTextEnded("a", "p0", ""), titleTextEnded("a", "p1", `{"title":"隔离标题"}`)}
					if reasoningOnly {
						events = []adapter.Event{titleTextEnded("old", "p0", `{"title":"不得沿用"}`), titleSemanticEvent("reasoning", "new", "r0", "delta", "private reasoning")}
					}
					events = append(events, adapter.Event{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
					for _, event := range events {
						if err := sink(ctx, event); err != nil {
							return adapter.NativeSession{}, err
						}
					}
					return adapter.NativeSession{ID: "must-not-persist"}, nil
				}})
				return manager
			}
			request := titleRequest()
			request.Provider = "opencode"
			result, err := engine.GenerateTitle(t.Context(), request)
			if reasoningOnly {
				if !errors.Is(err, errTitleGeneration) || result.Title != "" {
					t.Fatal("new reasoning-only message reused old JSON through GenerateTitle")
				}
			} else if err != nil || result.Title != "隔离标题" {
				t.Fatal("valid answer after empty part failed through GenerateTitle")
			}
			if _, err := os.Stat(filepath.Dir(root)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("semantic output retained private title attempt data")
			}
			if len(engine.managers) != 0 {
				t.Fatal("semantic title output escaped into the business runtime")
			}
		})
	}
}
