package claude

import (
	"bufio"
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

	"github.com/teamva-ai/teamva/node/provider"
)

const fakeSecret = "am-private-credential-canary-never-output"

func fakeClient(t *testing.T, config provider.InstanceConfig) (*Client, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"env": map[string]string{"ANTHROPIC_AUTH_TOKEN": fakeSecret, "ANTHROPIC_MODEL": "fixture", "EVIL": "not-projected"}, "apiKeyHelper": "touch /must-not-execute", "hooks": map[string]any{"SessionStart": "never"}})
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexport TEAMVA_CLAUDE_FAKE=1\nexec '"+strings.ReplaceAll(bin, "'", "'\\''")+"' -test.run=TestFakeClaudeProcess -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if config.WritableRepositories == nil {
		config.WritableRepositories = []string{workspace}
	}
	adapter, err := newInstance(provider.InstanceOptions{Executable: wrapper, ConfigDir: t.TempDir(), DataDir: t.TempDir(), Config: config})
	if err != nil {
		t.Fatal(err)
	}
	c := adapter.(*Client)
	t.Cleanup(func() { _ = c.Close() })
	return c, workspace
}

// Fake the official native transport, not the Provider API. The child validates
// isolation argv/env, initialize handshake, native permission replies and SDK
// MCP traffic. No network, real credential, or global user config is accessed.
func TestFakeClaudeProcess(t *testing.T) {
	if os.Getenv("TEAMVA_CLAUDE_FAKE") != "1" {
		return
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	emit := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	for _, arg := range args {
		if arg == "--version" {
			fmt.Print("2.1.316 (Claude Code)")
			os.Exit(0)
		}
		if arg == "--help" {
			fmt.Print("--bare --strict-mcp-config --setting-sources --input-format --tools")
			os.Exit(0)
		}
	}
	if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
		emit(map[string]any{"loggedIn": os.Getenv("ANTHROPIC_API_KEY") == fakeSecret && os.Getenv("ANTHROPIC_AUTH_TOKEN") == ""})
		os.Exit(0)
	}
	has := func(name string) bool {
		for _, arg := range args {
			if arg == name {
				return true
			}
		}
		return false
	}
	get := func(name string) string {
		for i, arg := range args {
			if arg == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	for _, flag := range []string{"--bare", "--disable-slash-commands", "--strict-mcp-config", "--setting-sources=", "--tools", "--permission-prompt-tool"} {
		if !has(flag) {
			fmt.Fprintln(os.Stderr, "missing safety flag")
			os.Exit(10)
		}
	}
	if get("--tools") != "" || get("--permission-mode") != "default" || os.Getenv("ANTHROPIC_API_KEY") != fakeSecret || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" || os.Getenv("EVIL") != "" || os.Getenv("NODE_OPTIONS") != "" {
		fmt.Fprintln(os.Stderr, "unsafe env or tools")
		os.Exit(11)
	}
	id := ""
	for _, arg := range args {
		for _, prefix := range []string{"--session-id=", "--resume="} {
			if strings.HasPrefix(arg, prefix) {
				id = strings.TrimPrefix(arg, prefix)
			}
		}
	}
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	read := func() map[string]any {
		var m map[string]any
		if decoder.Decode(&m) != nil {
			os.Exit(12)
		}
		return m
	}
	init := read()
	req, _ := init["request"].(map[string]any)
	if req["subtype"] != "initialize" {
		os.Exit(13)
	}
	model := get("--model")
	if model == "fixture-handshake-error" {
		emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": init["request_id"], "error": "native initialize rejected"}})
		os.Exit(1)
	}
	emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": init["request_id"], "response": map[string]any{}}})
	user := read()
	if user["type"] != "user" {
		os.Exit(14)
	}
	if model == "fixture-workspace-instructions" {
		body, err := os.ReadFile(get("--system-prompt-file"))
		expected := stringValue(user["message"].(map[string]any)["content"])
		if err != nil || !strings.Contains(string(body), expected) {
			fmt.Fprintln(os.Stderr, "host workspace instruction body missing")
			os.Exit(20)
		}
	}
	if model == "fixture-startup-error" {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"native startup rejected"}, "session_id": id})
		os.Exit(1)
	}
	project := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "fixture")
	_ = os.MkdirAll(project, 0o700)
	_ = os.WriteFile(filepath.Join(project, id+".jsonl"), []byte("{}\n"), 0o600)
	nativeInit := map[string]any{"type": "system", "subtype": "init", "session_id": id, "apiKeySource": fakeSecret, "tools": []any{}}
	if model == "fixture-missing-init-session" {
		delete(nativeInit, "session_id")
	}
	if model == "fixture-mismatched-init-session" {
		nativeInit["session_id"] = "11111111-1111-4111-a111-111111111111"
	}
	emit(nativeInit)
	if model == "fixture-duplicate-init" {
		emit(nativeInit)
	}
	control := func(subtype string, fields map[string]any) map[string]any {
		fields["subtype"] = subtype
		emit(map[string]any{"type": "control_request", "request_id": "callback", "request": fields})
		answer := read()
		response, _ := answer["response"].(map[string]any)
		result, _ := response["response"].(map[string]any)
		return result
	}
	if model == "fixture-credential-arguments" {
		var fixture map[string]any
		if json.Unmarshal([]byte(stringValue(user["message"].(map[string]any)["content"])), &fixture) != nil {
			os.Exit(23)
		}
		message := map[string]any{"jsonrpc": "2.0", "id": fixture["id"], "method": "tools/call", "params": map[string]any{"name": fixture["name"], "arguments": fixture["arguments"]}}
		frame := map[string]any{"type": "control_request", "request_id": "direct-tools-call", "request": map[string]any{"subtype": "mcp_message", "server_name": "teamva", "message": message}}
		wire, _ := json.Marshal(frame)
		if fixture["unicode"] == true {
			var escaped strings.Builder
			for _, r := range fakeSecret {
				fmt.Fprintf(&escaped, `\u%04x`, r)
			}
			wire = []byte(strings.ReplaceAll(string(wire), fakeSecret, escaped.String()))
		}
		// No can_use_tool round-trip: direct SDK MCP must enforce the boundary.
		fmt.Fprintln(os.Stdout, string(wire))
		answer := read()
		encoded, _ := json.Marshal(answer)
		if strings.Contains(string(encoded), fakeSecret) {
			fmt.Fprintln(os.Stderr, "unsafe tool receipt")
			os.Exit(24)
		}
		outer, _ := answer["response"].(map[string]any)
		reply, _ := outer["response"].(map[string]any)
		mcp, _ := reply["mcp_response"].(map[string]any)
		result, _ := mcp["result"].(map[string]any)
		outcome := "MCP_ARGUMENTS_ALLOWED"
		if result["isError"] == true {
			outcome = "MCP_ARGUMENTS_DENIED"
		}
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": outcome, "session_id": id})
		os.Exit(0)
	}
	if model == "fixture-fifo-read" {
		emit(map[string]any{"type": "control_request", "request_id": "fifo-read", "request": map[string]any{"subtype": "mcp_message", "server_name": "teamva", "message": map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "read_file", "arguments": map[string]any{"path": "pipe"}}}}})
		for {
			message := read()
			request, _ := message["request"].(map[string]any)
			if request["subtype"] == "interrupt" {
				emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "terminal_reason": "aborted_tools", "session_id": id})
				os.Exit(0)
			}
		}
	}
	if model == "fixture-deny" {
		answer := control("can_use_tool", map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "env"}})
		if answer["behavior"] != "deny" {
			os.Exit(15)
		}
		answer = control("can_use_tool", map[string]any{"tool_name": "mcp__unknown__steal", "input": map[string]any{}})
		if answer["behavior"] != "deny" {
			os.Exit(16)
		}
	}
	if model == "fixture-bridge" {
		answer := control("can_use_tool", map[string]any{"tool_name": "mcp__teamva__issue_comment_create", "input": map[string]any{"body": "real summary"}})
		if answer["behavior"] != "allow" {
			os.Exit(17)
		}
		answer = control("mcp_message", map[string]any{"server_name": "teamva", "message": map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "issue_comment_create", "arguments": map[string]any{"body": "real summary"}}}})
		data, _ := json.Marshal(answer)
		if !strings.Contains(string(data), "comment-fixture") {
			os.Exit(18)
		}
	}
	if model == "fixture-large-bridge" {
		answer := control("mcp_message", map[string]any{"server_name": "teamva", "message": map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "issue_comment_create", "arguments": map[string]any{"body": "summary"}}}})
		mcp, _ := answer["mcp_response"].(map[string]any)
		result, _ := mcp["result"].(map[string]any)
		content, _ := result["content"].([]any)
		if len(content) != 1 {
			os.Exit(21)
		}
		block, _ := content[0].(map[string]any)
		var receipt struct {
			OK   bool `json:"ok"`
			Data struct {
				Body string `json:"body"`
				Tail string `json:"tail"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(stringValue(block["text"])), &receipt) != nil || !receipt.OK || receipt.Data.Tail != "receipt-tail-canary" || receipt.Data.Body != strings.Repeat("甲", 24000) {
			fmt.Fprintln(os.Stderr, "large receipt was corrupted")
			os.Exit(22)
		}
	}
	if strings.HasPrefix(model, "fixture-compact") {
		emit(map[string]any{"type": "system", "subtype": "status", "session_id": id, "status": "compacting"})
		if model == "fixture-compact-cancel" {
			_ = read()
			os.Exit(0)
		}
		emit(map[string]any{"type": "system", "subtype": "status", "session_id": id, "status": nil})
		if model == "fixture-compact-failed" {
			emit(map[string]any{"type": "system", "subtype": "status", "status": nil, "compact_result": "failed", "compact_error": "gateway compact failed " + fakeSecret})
		} else {
			emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "auto", "pre_tokens": 1234}})
		}
	}
	if model == "fixture-interrupt" {
		request := read()
		r, _ := request["request"].(map[string]any)
		if r["subtype"] != "interrupt" {
			os.Exit(19)
		}
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "terminal_reason": "aborted_streaming", "session_id": id})
		os.Exit(0)
	}
	if model == "fixture-abrupt" {
		fmt.Fprintln(os.Stderr, "gateway failed credential="+fakeSecret)
		os.Exit(1)
	}
	if model == "fixture-malformed" {
		fmt.Println("not-json")
		os.Exit(0)
	}
	if model == "fixture-error" {
		emit(map[string]any{"type": "assistant", "error": "authentication_failed", "api_error_status": 401, "message": map[string]any{"id": "msg-error", "content": []any{map[string]any{"type": "text", "text": "gateway useful failure " + fakeSecret}}}})
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "api_error_status": 401, "result": "gateway useful failure " + fakeSecret, "session_id": id})
		os.Exit(1)
	}
	if model == "fixture-diagnostic-url" {
		message := "useful-error-marker HTTP 401 upstream https://review-user:review-only-password@example.invalid/r?token=review-only-token dsn=postgres://review:review-dsn-password@example.invalid/db"
		emit(map[string]any{"type": "assistant", "error": "authentication_failed", "api_error_status": 401, "message": map[string]any{"id": "url-error", "content": []any{map[string]any{"type": "text", "text": message}}}})
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "api_error_status": 401, "result": message, "session_id": id})
		os.Exit(1)
	}
	if model == "fixture-large-output" {
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": strings.Repeat("甲", 24000) + " business-tail-canary", "session_id": id})
		os.Exit(0)
	}
	emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg"}}})
	if model == "fixture-split-key" {
		prefix, suffix := fakeSecret[:len(fakeSecret)/2], fakeSecret[len(fakeSecret)/2:]
		emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "thinking", "thinking": ""}}})
		for _, text := range []string{prefix, suffix} {
			emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "thinking_delta", "thinking": text}}})
		}
		emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_stop", "index": 1}})
		for _, text := range []string{prefix, suffix} {
			emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}})
		}
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": fakeSecret, "session_id": id})
		os.Exit(0)
	}
	emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}})
	emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hello "}}})
	emit(map[string]any{"type": "stream_event", "session_id": id, "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "world"}}})
	emit(map[string]any{"type": "assistant", "session_id": id, "message": map[string]any{"id": "msg", "content": []any{map[string]any{"type": "text", "text": "hello world"}}}})
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "hello world", "session_id": id, "usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "cache_read_input_tokens": 4, "cache_creation_input_tokens": 2}})
	// A streaming-input CLI may remain alive for another user turn. The adapter
	// owns cancellation and closes its process group after the first result.
	_ = read()
	os.Exit(0)
}

func capture() (*[]provider.Event, provider.Sink) {
	events := []provider.Event{}
	return &events, func(_ context.Context, event provider.Event) error {
		if !publicClaudeEventTypes[event.Type] {
			return fmt.Errorf("Claude emitted an event outside the public Provider/SSE contract: %s", event.Type)
		}
		events = append(events, event)
		return nil
	}
}

var publicClaudeEventTypes = map[string]bool{"run.started": true, "run.finished": true, "output.delta": true, "output.snapshot": true, "reasoning.snapshot": true, "provider.tool": true, "provider.event": true, "files.changed": true, "workspace.diff": true, "usage.updated": true, "context.compaction": true}

func TestNativeProtocolLifecycleAndResume(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{})
	m, err := c.Probe(t.Context())
	if err != nil || !m.Available || !m.Authenticated || m.ProviderVersion != "2.1.316 (Claude Code)" {
		t.Fatalf("probe: %+v %v", m, err)
	}
	events, sink := capture()
	session, err := c.Start(t.Context(), provider.RunRequest{RunID: "first", CWD: cwd, Prompt: "hello"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionIDPattern.MatchString(session.ID) || session.Provider != "claude" {
		t.Fatalf("session: %+v", session)
	}
	if ok, err := c.VerifySession(t.Context(), session.ID); err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	final := 0
	for _, event := range *events {
		if event.Sequence != 0 {
			t.Fatal("Provider must leave durable event sequencing to Runtime")
		}
		if event.Type == "output.snapshot" && event.Payload["final"] == true {
			final++
			if event.Payload["text"] != "hello world" {
				t.Fatal(event)
			}
		}
	}
	if final != 1 {
		t.Fatalf("final count: %d", final)
	}
	if _, err = c.Resume(t.Context(), provider.RunRequest{RunID: "second", CWD: cwd, NativeSessionID: session.ID, Prompt: "again"}, sink); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Resume(t.Context(), provider.RunRequest{RunID: "wrong", CWD: t.TempDir(), NativeSessionID: session.ID}, sink); err == nil {
		t.Fatal("cross-workspace resume allowed")
	}
	if ok, _ := c.VerifySession(t.Context(), "../../escape"); ok {
		t.Fatal("unsafe session accepted")
	}
}

func TestNativeInitConfirmsRunStartedExactlyOnce(t *testing.T) {
	for _, scenario := range []struct {
		model       string
		wantStarted int
		wantError   bool
	}{
		{"fixture", 1, false}, {"fixture-duplicate-init", 1, false}, {"fixture-error", 1, true},
		{"fixture-startup-error", 0, true}, {"fixture-handshake-error", 0, true},
		{"fixture-missing-init-session", 0, true}, {"fixture-mismatched-init-session", 0, true},
	} {
		t.Run(scenario.model, func(t *testing.T) {
			c, cwd := fakeClient(t, provider.InstanceConfig{Model: scenario.model})
			events, sink := capture()
			session, err := c.Start(t.Context(), provider.RunRequest{RunID: "native-init", CWD: cwd, Prompt: "hello"}, sink)
			if (err != nil) != scenario.wantError {
				t.Fatalf("native outcome: %v", err)
			}
			started, finished := 0, 0
			for _, event := range *events {
				if event.Type == "run.started" {
					started++
					if event.Payload["native_session_id"] != session.ID || event.Payload["status"] != "running" {
						t.Fatalf("unconfirmed start payload: %+v", event.Payload)
					}
				}
				if event.Type == "run.finished" {
					finished++
				}
			}
			if started != scenario.wantStarted {
				t.Fatalf("run.started=%d want=%d", started, scenario.wantStarted)
			}
			if finished != 1 {
				t.Fatalf("run.finished=%d want=1", finished)
			}
			if scenario.wantError {
				return
			}
			if err := WriteConfig(c.configDir, provider.InstanceConfig{Model: "fixture-duplicate-init", WritableRepositories: []string{cwd}}); err != nil {
				t.Fatal(err)
			}
			resumeEvents, resumeSink := capture()
			resumed, err := c.Resume(t.Context(), provider.RunRequest{RunID: "native-resume", CWD: cwd, NativeSessionID: session.ID, Prompt: "again"}, resumeSink)
			if err != nil {
				t.Fatal(err)
			}
			resumeStarted := 0
			for _, event := range *resumeEvents {
				if event.Type == "run.started" {
					resumeStarted++
					if event.Payload["native_session_id"] != resumed.ID || resumed.ID != session.ID {
						t.Fatal("resume start lost native identity")
					}
				}
			}
			if resumeStarted != 1 {
				t.Fatalf("resumed run.started=%d want=1", resumeStarted)
			}
		})
	}
}

func TestHostInsertedEventsOwnTheSharedDurableCursor(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-bridge"})
	ipc := t.TempDir()
	tool := filepath.Join(ipc, "issue_comment_create")
	if err := os.Mkdir(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var cursor int64
	providerEvents, hostEvents := 0, 0
	var cursorErr error
	started, finished := false, false
	sink := func(_ context.Context, event provider.Event) error {
		mu.Lock()
		defer mu.Unlock()
		next := cursor + 1
		if event.Sequence != 0 && event.Sequence != next {
			cursorErr = fmt.Errorf("explicit durable event sequence must equal the next cursor: got %d, next %d", event.Sequence, next)
			return cursorErr
		}
		cursor = next
		providerEvents++
		if event.Type == "run.started" {
			started = true
		}
		if event.Type == "run.finished" && event.Payload["status"] == "succeeded" {
			finished = true
		}
		return nil
	}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			entries, _ := os.ReadDir(tool)
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), "req-") || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".response.json") {
					continue
				}
				// Model the real FileBridge/ToolBroker writer: the host inserts
				// public tool events while the Provider awaits the tool receipt.
				mu.Lock()
				cursor += 2
				hostEvents += 2
				mu.Unlock()
				_ = os.WriteFile(filepath.Join(tool, entry.Name())+".response.json", []byte(`{"ok":true,"data":{"id":"comment-fixture"}}`), 0o600)
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	_, err := c.Start(ctx, provider.RunRequest{RunID: "host-cursor", CWD: cwd, ToolRequestDir: ipc, Prompt: "publish summary"}, sink)
	mu.Lock()
	defer mu.Unlock()
	if cursorErr != nil {
		t.Fatal(cursorErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if hostEvents != 2 || cursor != int64(providerEvents+hostEvents) {
		t.Fatalf("cursor=%d provider=%d host=%d", cursor, providerEvents, hostEvents)
	}
	if !started || !finished {
		t.Fatalf("started=%v finished=%v", started, finished)
	}
}

func TestNativeErrorAndPermissionMatrix(t *testing.T) {
	for _, scenario := range []string{"deny", "error", "abrupt", "malformed", "compact-completed", "compact-failed"} {
		t.Run(scenario, func(t *testing.T) {
			c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-" + scenario})
			events, sink := capture()
			_, err := c.Start(t.Context(), provider.RunRequest{RunID: "run", CWD: cwd, Prompt: "hello"}, sink)
			wantError := scenario == "error" || scenario == "abrupt" || scenario == "malformed"
			if (err != nil) != wantError {
				t.Fatalf("error=%v", err)
			}
			encoded, _ := json.Marshal(*events)
			if strings.Contains(string(encoded), fakeSecret) || (err != nil && strings.Contains(err.Error(), fakeSecret)) {
				t.Fatal("credential escaped redaction")
			}
			if scenario == "error" {
				if !strings.Contains(string(encoded), "gateway useful failure") || !strings.Contains(string(encoded), "401") {
					t.Fatalf("actionable error lost: %s", encoded)
				}
			}
			if strings.HasPrefix(scenario, "compact-") {
				states := []string{}
				for _, e := range *events {
					if e.Type == "context.compaction" {
						states = append(states, stringValue(e.Payload["status"]))
					}
				}
				want := "running,completed"
				if scenario == "compact-failed" {
					want = "running,failed"
				}
				if strings.Join(states, ",") != want {
					t.Fatalf("compaction: %v", states)
				}
			}
		})
	}
}

func TestNativeInterruptAndCompactionCancel(t *testing.T) {
	for _, model := range []string{"fixture-interrupt", "fixture-compact-cancel"} {
		t.Run(model, func(t *testing.T) {
			c, cwd := fakeClient(t, provider.InstanceConfig{Model: model})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ready := make(chan struct{})
			done := make(chan error, 1)
			var once sync.Once
			var mu sync.Mutex
			events := []provider.Event{}
			go func() {
				_, err := c.Start(ctx, provider.RunRequest{RunID: "run", CWD: cwd}, func(_ context.Context, event provider.Event) error {
					mu.Lock()
					events = append(events, event)
					mu.Unlock()
					if event.Type == "context.compaction" {
						once.Do(func() { close(ready) })
					}
					return nil
				})
				done <- err
			}()
			if model == "fixture-interrupt" {
				deadline := time.After(4 * time.Second)
				for {
					c.mu.Lock()
					a := c.active
					readyNow := a != nil && a.stdin != nil && a.tree != nil
					c.mu.Unlock()
					if readyNow {
						time.Sleep(150 * time.Millisecond)
						break
					}
					select {
					case <-deadline:
						t.Fatal("run not started")
					case <-time.After(10 * time.Millisecond):
					}
				}
			} else {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err := c.Interrupt(ctx, "run"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel err: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			mu.Lock()
			defer mu.Unlock()
			terminal := ""
			compaction := ""
			for _, event := range events {
				if event.Type == "run.finished" {
					terminal = stringValue(event.Payload["status"])
				}
				if event.Type == "context.compaction" {
					compaction = stringValue(event.Payload["status"])
				}
			}
			if terminal != "cancelled" {
				t.Fatalf("terminal=%q", terminal)
			}
			if model == "fixture-compact-cancel" && compaction != "cancelled" {
				t.Fatalf("compaction=%q", compaction)
			}
		})
	}
}

func TestNativeSDKMCPCallsRealFileBridge(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-bridge"})
	ipc := t.TempDir()
	tool := filepath.Join(ipc, "issue_comment_create")
	if err := os.Mkdir(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	receipt := make(chan string, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			entries, _ := os.ReadDir(tool)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "req-") && strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".response.json") {
					path := filepath.Join(tool, entry.Name())
					data, _ := os.ReadFile(path)
					_ = os.WriteFile(path+".response.json", []byte(`{"ok":true,"data":{"id":"comment-fixture"}}`), 0o600)
					receipt <- string(data)
					return
				}
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	_, sink := capture()
	if _, err := c.Start(ctx, provider.RunRequest{RunID: "bridge", CWD: cwd, ToolRequestDir: ipc}, sink); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-receipt:
		if !strings.Contains(data, "real summary") {
			t.Fatal(data)
		}
	case <-ctx.Done():
		t.Fatal("no real bridge request")
	}
}
