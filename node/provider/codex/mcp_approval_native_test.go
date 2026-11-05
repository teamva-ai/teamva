//go:build darwin || linux

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestNativeCodexMCPApprovalWithoutExternalModel(t *testing.T) {
	if os.Getenv("TEAMVA_CODEX_TEST") != "1" {
		t.Skip("set TEAMVA_CODEX_TEST=1 for isolated real native MCP approval verification")
	}
	binary := os.Getenv("TEAMVA_CODEX_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("codex")
		if err != nil {
			t.Fatal(err)
		}
	}
	binary, _ = filepath.Abs(binary)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	fixture := os.Getenv("TEAMVA_CODEX_MCP_FIXTURE")
	if fixture == "" {
		fixture = "testdata/mcp_approval.mjs"
	}
	fixture, err = filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"approved", "denied", "cancelled", "interrupted", "unsupported-form"} {
		t.Run(decision, func(t *testing.T) {
			base := t.TempDir()
			if parent := os.Getenv("TEAMVA_CODEX_FIXTURE_ROOT"); parent != "" {
				var err error
				base, err = os.MkdirTemp(parent, "native-mcp-approval-")
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("isolated native MCP fixture: %s", base)
			}
			home, cwd, configDir, dataDir := filepath.Join(base, "home"), filepath.Join(base, "workspace"), filepath.Join(base, "config"), filepath.Join(base, "data")
			ipc := filepath.Join(cwd, ".teamva", "tool-requests", decision)
			for _, path := range []string{home, cwd, configDir, dataDir, ipc} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			var modelCalls atomic.Int64
			var modelError atomic.Value
			var discoveredTool atomic.Value
			var systemVerified atomic.Bool
			const systemMarker = "CODEX_BASE_INSTRUCTIONS_JSON_FIXTURE"
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
				if err != nil {
					http.Error(w, "fixture body", 400)
					return
				}
				n := modelCalls.Add(1)
				var item map[string]any
				if n == 1 {
					var request map[string]any
					if err := json.Unmarshal(body, &request); err != nil {
						http.Error(w, "fixture JSON", 400)
						return
					}
					if !nativeHigherPriorityMarker(request, systemMarker) {
						modelError.Store("system prompt was not sent as native higher-priority instructions")
						http.Error(w, "missing baseInstructions", 400)
						return
					}
					systemVerified.Store(true)
					name, namespace := nativeEchoTool(request["tools"], "")
					if name == "" {
						name, _ = discoveredTool.Load().(string)
						// Fixed 0.163 contract: core/tests/suite/rmcp_client.rs
						// emits the native tool name inside namespace mcp__<server>.
						namespace = "mcp__amqa_local" // Native sanitizer replaces '-'.
					}
					if name == "" {
						var keys []string
						for key := range request {
							keys = append(keys, key)
						}
						modelError.Store("qa_echo was not advertised; path=" + r.URL.Path + " keys=" + strings.Join(keys, ",") + " tool names=" + strings.Join(nativeToolNames(request["tools"]), ","))
						http.Error(w, "missing qa_echo", 400)
						return
					}
					item = map[string]any{"type": "function_call", "name": name, "call_id": "native-mcp-call", "arguments": `{"text":"CODEX_MCP_APPROVAL_FIXTURE"}`}
					if namespace != "" {
						item["namespace"] = namespace
					}
				} else {
					item = map[string]any{"type": "message", "id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "FIXTURE_DONE"}}}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []map[string]any{
					{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("response-%d", n)}},
					{"type": "response.output_item.done", "output_index": 0, "item": item},
					{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", n), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
				} {
					encoded, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
				}
			}))
			defer model.Close()
			wrapper := filepath.Join(base, "isolated-codex")
			writeProviderFile(t, wrapper, "#!/bin/sh\ncd "+shellFixtureQuote(cwd)+" || exit 1\nexec /usr/bin/env -i HOME="+shellFixtureQuote(home)+" CODEX_HOME=\"$CODEX_HOME\" PATH="+shellFixtureQuote(os.Getenv("PATH"))+" "+shellFixtureQuote(binary)+" \"$@\"\n")
			if err := os.Chmod(wrapper, 0o700); err != nil {
				t.Fatal(err)
			}
			command := []string{node, fixture}
			if decision == "unsupported-form" {
				command = append(command, decision)
			}
			entry, _ := json.Marshal(managedMCPEntry{Type: "local", Command: command})
			item, err := newInstance(adapter.InstanceOptions{Executable: wrapper, ConfigDir: configDir, DataDir: dataDir, Config: adapter.InstanceConfig{SystemPrompt: systemMarker + ": return strict JSON after the explicitly authorized fixture call", MCPServers: map[string]json.RawMessage{"amqa-local": entry}}})
			if err != nil {
				t.Fatal(err)
			}
			client := item.(*Client)
			defer client.Close()
			config, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			prefix := "model_provider=\"fixture\"\nmodel=\"gpt-6.1-sol\"\ncli_auth_credentials_store=\"ephemeral\"\nfeatures.plugins=false\nfeatures.apps=false\n"
			footer := "\n[model_providers.fixture]\nname=\"local deterministic fixture\"\nwire_api=\"responses\"\nrequires_openai_auth=false\nbase_url=" + tomlString(model.URL+"/v1") + "\n"
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(prefix+string(config)+footer), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			if err := client.ensureStarted(ctx); err != nil {
				t.Fatal(err)
			}
			var inventory struct {
				Data []struct {
					Tools map[string]json.RawMessage `json:"tools"`
				} `json:"data"`
			}
			if err := client.call(ctx, "mcpServerStatus/list", map[string]any{"serverName": "amqa-local", "limit": 10}, &inventory); err != nil {
				t.Fatal(err)
			}
			for _, server := range inventory.Data {
				for name := range server.Tools {
					if strings.Contains(name, "qa_echo") {
						discoveredTool.Store(name)
					}
				}
			}
			if discoveredTool.Load() == nil {
				t.Fatal("native MCP inventory did not discover fixture tool")
			}
			approvals := make(chan string, 4)
			var mu sync.Mutex
			var snapshots []map[string]any
			var approvalCount int
			done := make(chan error, 1)
			go func() {
				_, err := client.Start(ctx, adapter.RunRequest{RunID: "native-mcp-" + decision, CWD: cwd, Prompt: "fixture", ToolRequestDir: ipc}, func(_ context.Context, event adapter.Event) error {
					mu.Lock()
					defer mu.Unlock()
					if event.Type == "approval.requested" {
						approvalCount++
						id, _ := event.Payload["request_id"].(string)
						approvals <- id
					}
					if event.ProviderEventType == "item/completed" || event.ProviderEventType == "item/started" {
						native, _ := event.Payload["item"].(map[string]any)
						if native["type"] == "mcpToolCall" {
							snapshots = append(snapshots, native)
						}
					}
					return nil
				})
				done <- err
			}()
			var requestID string
			select {
			case requestID = <-approvals:
			case err := <-done:
				mu.Lock()
				var status, message string
				if len(snapshots) > 0 {
					last := snapshots[len(snapshots)-1]
					status, _ = last["status"].(string)
					nativeError, _ := last["error"].(map[string]any)
					message, _ = nativeError["message"].(string)
				}
				mu.Unlock()
				t.Fatalf("native turn ended without host approval: err=%v status=%s message=%s modelError=%v", err, status, message, modelError.Load())
			case <-ctx.Done():
				t.Fatal("native MCP approval never arrived")
			}
			select {
			case err := <-done:
				t.Fatalf("MCP completed before explicit decision: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if decision == "cancelled" || decision == "interrupted" {
				if decision == "cancelled" {
					cancel()
				} else if err := client.Interrupt(ctx, "native-mcp-"+decision); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if decision == "cancelled" && !errors.Is(err, context.Canceled) || decision == "interrupted" && err != nil {
						t.Fatalf("cancelled approval did not cancel its Run: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("cancelled MCP Run or its process did not stop")
				}
				if err := client.DecideApproval(t.Context(), adapter.ApprovalDecision{RunID: "native-mcp-" + decision, RequestID: requestID, Decision: "approved"}); err == nil {
					t.Fatal("cancelled native MCP approval remained usable")
				}
				client.mu.Lock()
				leaked := decision == "cancelled" && client.process != nil || len(client.approvals) != 0
				client.mu.Unlock()
				mu.Lock()
				defer mu.Unlock()
				for _, snapshot := range snapshots {
					if snapshot["status"] == "completed" || snapshot["result"] != nil {
						t.Fatal("cancelled approval executed the MCP tool")
					}
				}
				if leaked || approvalCount != 1 || modelCalls.Load() != 1 {
					t.Fatal("cancelled approval leaked native process/pending approval or continued model work")
				}
				writeNativeMCPResult(t, base, decision, snapshots, approvalCount)
				t.Logf("real native MCP: %s while awaiting approval; no tool receipt, no continued model call or pending approval; stale decision rejected", decision)
				return
			}
			choice := decision
			if decision == "unsupported-form" {
				choice = "approved" // Only the actual native tool approval, not its subsequent input form.
			}
			if err := client.DecideApproval(ctx, adapter.ApprovalDecision{RunID: "native-mcp-" + decision, RequestID: requestID, Decision: choice, Scope: "once"}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("native turn did not finish after approval")
			}
			mu.Lock()
			defer mu.Unlock()
			if approvalCount != 1 || len(snapshots) < 2 {
				t.Fatal("native MCP approval or start/final snapshots missing")
			}
			if !systemVerified.Load() {
				t.Fatal("native baseInstructions override was not verified")
			}
			last := snapshots[len(snapshots)-1]
			want := "completed"
			if decision != "approved" {
				want = "failed"
			}
			if last["status"] != want || last["server"] != "amqa-local" || last["tool"] != "qa_echo" {
				t.Fatalf("native MCP status/identity lost: %v", last["status"])
			}
			encoded, _ := json.Marshal(last)
			if decision == "approved" && !strings.Contains(string(encoded), "CODEX_MCP_APPROVAL_FIXTURE") {
				t.Fatal("approved native MCP result missing")
			}
			if decision == "denied" {
				nativeError, _ := last["error"].(map[string]any)
				if nativeError["message"] != "user rejected MCP tool call" {
					t.Fatal("explicit native denial lost its error")
				}
			}
			if decision == "unsupported-form" && (!strings.Contains(string(encoded), "UNSUPPORTED_FORM_DECLINED") || strings.Contains(string(encoded), "UNSAFE_FORM_ACCEPTED")) {
				t.Fatalf("unsupported native input form was not safely declined: %s", encoded)
			}
			writeNativeMCPResult(t, base, decision, snapshots, approvalCount)
			t.Logf("real native MCP: one host approval; no execution before decision; %s -> %s; filesystem profile unchanged", decision, want)
		})
	}
}

func writeNativeMCPResult(t *testing.T, base, scenario string, snapshots []map[string]any, approvals int) {
	t.Helper()
	payload, err := json.MarshalIndent(map[string]any{"scenario": scenario, "approvals": approvals, "snapshots": snapshots}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "result.json"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeHigherPriorityMarker(request map[string]any, marker string) bool {
	if instructions, ok := request["instructions"].(string); ok && strings.Contains(instructions, marker) {
		return true
	}
	items, _ := request["input"].([]any)
	for _, item := range items {
		message, _ := item.(map[string]any)
		if message["role"] != "system" && message["role"] != "developer" {
			continue
		}
		encoded, _ := json.Marshal(message["content"])
		if strings.Contains(string(encoded), marker) {
			return true
		}
	}
	return false
}

func nativeEchoTool(value any, namespace string) (string, string) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if name, ns := nativeEchoTool(item, namespace); name != "" {
				return name, ns
			}
		}
	case map[string]any:
		name, _ := v["name"].(string)
		if v["type"] == "namespace" {
			namespace = name
		}
		if v["type"] == "function" && strings.Contains(name, "qa_echo") {
			return name, namespace
		}
		if name, ns := nativeEchoTool(v["tools"], namespace); name != "" {
			return name, ns
		}
	}
	return "", ""
}

func nativeToolNames(value any) []string {
	var names []string
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			names = append(names, nativeToolNames(item)...)
		}
	case map[string]any:
		if name, ok := v["name"].(string); ok {
			kind, _ := v["type"].(string)
			names = append(names, kind+":"+name)
		}
		for _, key := range []string{"tools", "function"} {
			names = append(names, nativeToolNames(v[key])...)
		}
	}
	return names
}
