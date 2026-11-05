package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

// Opt-in real installed CLI + loopback Anthropic fixture. This test never reads
// the user's credential file or calls a real model, and does not represent an
// official-upstream model acceptance. It verifies the actual CLI SDK protocol.
func TestRealClaudeSDKMCP(t *testing.T) {
	if os.Getenv("TEAMVA_CLAUDE_TEST") != "1" {
		t.Skip("set TEAMVA_CLAUDE_TEST=1 for real CLI / loopback-only SDK acceptance")
	}
	executable := os.Getenv("TEAMVA_CLAUDE_PATH")
	if executable == "" {
		var err error
		executable, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"normal", "read-only", "no-tools", "issue"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			catalog := []string{}
			lastCatalog := []string{}
			toolResultSeen := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					w.WriteHeader(404)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				calls++
				call := calls
				tools, _ := body["tools"].([]any)
				lastCatalog = nil
				for _, value := range tools {
					tool, _ := value.(map[string]any)
					catalog = append(catalog, stringValue(tool["name"]))
					lastCatalog = append(lastCatalog, stringValue(tool["name"]))
				}
				bodyBytes, _ := json.Marshal(body)
				if strings.Contains(string(bodyBytes), "controlled-read-ok") {
					toolResultSeen = true
				}
				mu.Unlock()
				content := []any{map[string]any{"type": "text", "text": "NATIVE_SDK_OK"}}
				stop := "end_turn"
				if mode != "no-tools" && call == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "tool_read", "name": "mcp__teamva__read_file", "input": map[string]any{"path": "fixture.txt"}}}
					stop = "tool_use"
				}
				if mode == "issue" && call <= 3 {
					name := "read_file"
					input := map[string]any{"path": "fixture.txt"}
					if call == 1 {
						name = "write_file"
						input["content"] = "controlled-read-ok-written"
					}
					if call == 2 {
						name = "issue_comment_create"
						input = map[string]any{"body": "native public summary"}
					}
					content = []any{map[string]any{"type": "tool_use", "id": fmt.Sprintf("tool-%d", call), "name": "mcp__teamva__" + name, "input": input}}
					stop = "tool_use"
				}
				if body["stream"] != true {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg-native", "type": "message", "role": "assistant", "model": "fixture-model", "content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 20, "output_tokens": 10}})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				sse := func(kind string, event map[string]any) {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
				sse("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg-native-%d", call), "type": "message", "role": "assistant", "model": "fixture-model", "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 20, "output_tokens": 0}}})
				block := content[0].(map[string]any)
				if block["type"] == "tool_use" {
					sse("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}})
					input, _ := json.Marshal(block["input"])
					sse("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
				} else {
					sse("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
					sse("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "NATIVE_SDK_OK"}})
				}
				sse("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
				sse("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 10}})
				sse("message_stop", map[string]any{"type": "message_stop"})
			}))
			defer server.Close()
			home := t.TempDir()
			t.Setenv("HOME", home)
			_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
			settings, _ := json.Marshal(map[string]any{"env": map[string]string{"ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": fakeSecret, "ANTHROPIC_MODEL": "fixture-model", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "100000"}})
			if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			// Real SDK MCP must preserve >64 KiB UTF-8 business text, including
			// the tail consumed by the loopback model fixture on its next turn.
			_ = os.WriteFile(filepath.Join(cwd, "fixture.txt"), []byte(strings.Repeat("甲", 24000)+" controlled-read-ok"), 0o600)
			config := provider.InstanceConfig{Model: "fixture-model", WritableRepositories: []string{cwd}, ReadOnly: mode == "read-only", DisableTools: mode == "no-tools"}
			adapter, err := newInstance(provider.InstanceOptions{Executable: executable, ConfigDir: t.TempDir(), DataDir: t.TempDir(), Config: config})
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			probe, err := adapter.Probe(ctx)
			if err != nil || !probe.Available || !probe.Authenticated {
				t.Fatalf("real isolated probe failed: %+v %v", probe, err)
			}
			events, captureSink := capture()
			var cursorMu sync.Mutex
			var durableCursor int64
			sink := func(ctx context.Context, event provider.Event) error {
				cursorMu.Lock()
				defer cursorMu.Unlock()
				if event.Sequence != 0 {
					return fmt.Errorf("native Provider must leave the shared durable sequence to Runtime: %d", event.Sequence)
				}
				durableCursor++
				return captureSink(ctx, event)
			}
			ipc := ""
			bridgeCalled := make(chan string, 1)
			if mode == "issue" {
				ipc = t.TempDir()
				tool := filepath.Join(ipc, "issue_comment_create")
				_ = os.Mkdir(tool, 0o700)
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						entries, _ := os.ReadDir(tool)
						for _, entry := range entries {
							if strings.HasPrefix(entry.Name(), "req-") && strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".response.json") {
								path := filepath.Join(tool, entry.Name())
								data, _ := os.ReadFile(path)
								cursorMu.Lock()
								durableCursor += 2
								cursorMu.Unlock()
								_ = os.WriteFile(path+".response.json", []byte(`{"ok":true,"data":{"id":"native-comment"}}`), 0o600)
								bridgeCalled <- string(data)
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
			}
			session, err := adapter.Start(ctx, provider.RunRequest{RunID: "native-sdk", CWD: cwd, Prompt: "Use the available host tool to read fixture.txt, then reply NATIVE_SDK_OK.", ReadOnly: config.ReadOnly, DisableTools: config.DisableTools, ToolRequestDir: ipc}, sink)
			if err != nil {
				data, _ := json.Marshal(*events)
				t.Fatalf("native SDK failed: %v; safe events=%s", err, data)
			}
			started := 0
			for _, event := range *events {
				if event.Type == "run.started" {
					started++
					if event.Payload["native_session_id"] != session.ID {
						t.Fatal("real CLI run.started lost its native identity")
					}
				}
			}
			if started != 1 {
				t.Fatalf("real CLI run.started=%d want=1", started)
			}
			if mode == "issue" {
				cursorMu.Lock()
				cursor := durableCursor
				cursorMu.Unlock()
				if cursor != int64(len(*events)+2) {
					t.Fatal("real SDK MCP did not tolerate host-inserted events")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if mode != "no-tools" && !toolResultSeen {
				t.Fatalf("real CLI did not return host file result; requests=%d catalog=%v", calls, catalog)
			}
			for _, name := range catalog {
				if !strings.HasPrefix(name, "mcp__teamva__") {
					t.Fatalf("unexpected native/custom tool %s", name)
				}
				if mode == "read-only" && strings.Contains(name, "write_file") {
					t.Fatal("read-only tool catalog has write")
				}
			}
			if mode == "no-tools" && len(catalog) != 0 {
				t.Fatal("disabled tool catalog is not empty")
			}
			if ok, err := adapter.VerifySession(ctx, session.ID); err != nil || !ok {
				t.Fatalf("native transcript verification failed: %v %v", ok, err)
			}
			if mode == "issue" {
				data, _ := os.ReadFile(filepath.Join(cwd, "fixture.txt"))
				if string(data) != "controlled-read-ok-written" {
					t.Fatal("real CLI did not perform the controlled file write")
				}
				select {
				case body := <-bridgeCalled:
					if !strings.Contains(body, "native public summary") {
						t.Fatal("wrong public summary request")
					}
				default:
					t.Fatal("real CLI did not publish a real FileBridge request")
				}
			}
			// Rebuild a native session under stricter current authorization: neither
			// transcript nor an earlier SDK MCP catalog may restore old tools.
			if mode == "normal" {
				mu.Unlock()
				config.DisableTools = true
				if err := WriteConfig(adapter.(*Client).configDir, config); err != nil {
					t.Fatal(err)
				}
				resumed, resumeErr := adapter.Resume(ctx, provider.RunRequest{RunID: "native-resume-no-tools", CWD: cwd, NativeSessionID: session.ID, Prompt: "Reply NATIVE_SDK_OK without tools", DisableTools: true}, sink)
				mu.Lock()
				if resumeErr != nil || resumed.ID != session.ID {
					t.Fatalf("real native resume failed: %v", resumeErr)
				}
				if len(lastCatalog) != 0 {
					t.Fatal("resumed no-tools session restored an old MCP catalog")
				}
				resumeStarted := 0
				for _, event := range *events {
					if event.RunID == "native-resume-no-tools" && event.Type == "run.started" {
						resumeStarted++
						if event.Payload["native_session_id"] != session.ID {
							t.Fatal("resumed run.started changed native identity")
						}
					}
				}
				if resumeStarted != 1 {
					t.Fatalf("real resumed run.started=%d want=1", resumeStarted)
				}
			}
			t.Logf("real CLI mode=%s requests=%d scoped_tools=%d session_verified=true", mode, calls, len(catalog))
		})
	}
}
