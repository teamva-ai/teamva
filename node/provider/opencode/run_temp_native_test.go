//go:build darwin

package opencode

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
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Real V2 shell tool, actual zsh/Python/mktemp/Node, local synthetic model only.
// Every invocation succeeds/fails on the first attempt; there is no model retry
// or workaround and no real authentication data is read.
func TestBundledRunTempHeredocOffline(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for offline native heredoc/temp verification")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	for _, name := range []string{"python3", "node", "mktemp"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("%s required for offline fixture", name)
		}
	}
	isolateNativeFixtureEnvironment(t)
	t.Setenv("SHELL", "/bin/zsh")
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	sibling := permissionFixtureDirectory(t, base, "sibling-temp")
	other := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "other", ".tmp")
	configDir, dataDir := permissionFixtureDirectory(t, base, "config"), permissionFixtureDirectory(t, base, "data")
	type stage struct {
		name, ipc string
		readonly  bool
	}
	stages := []stage{}
	for _, name := range []string{"cold", "same-temp-saved-env", "warm", "recreated", "readonly", "parallel-a", "parallel-b"} {
		stages = append(stages, stage{name, permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", name), name == "readonly" || name == "parallel-b"})
	}
	stages[1].ipc = stages[0].ipc
	for _, st := range stages {
		permissionFixtureDirectory(t, st.ipc, ".tmp")
	}
	var mu sync.Mutex
	calls := map[string]int{}
	ready, release := make(chan string, 4), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	script := func(st stage) string {
		denied := []string{filepath.Join(other, "must-not-write"), filepath.Join(sibling, "must-not-write")}
		for _, prior := range stages {
			if prior.ipc != st.ipc {
				denied = append(denied, filepath.Join(prior.ipc, ".tmp", "must-not-write"))
			}
		}
		if st.readonly {
			denied = append(denied, filepath.Join(cwd, "readonly-business-denied"))
		}
		return `set -e
test -n "$ZSH_VERSION"
python3 - <<'PY'
import os, pathlib, tempfile
expected = ` + quoteJSON(filepath.Join(st.ipc, ".tmp")) + `
for key in ["TMPDIR", "TMP", "TEMP"]:
    assert os.environ[key] == expected, key
assert os.environ["TMPPREFIX"] == expected + "/zsh"
with tempfile.NamedTemporaryFile(prefix="python-") as f:
    assert pathlib.Path(f.name).parent == pathlib.Path(expected)
    f.write(b"temporary-fixture")
for target in ` + quoteJSON(denied) + `:
    try:
        pathlib.Path(target).write_text("must-not-write")
    except PermissionError:
        pass
    else:
        raise AssertionError("unauthorized temporary write")
pathlib.Path(` + quoteJSON(filepath.Join(st.ipc, "request.json")) + `).write_text("heredoc-ipc-fixture")
PY
temporary="$(mktemp "$TMPDIR/fixture.XXXXXXXX")"
case "$temporary" in "$TMPDIR"/*) ;; *) exit 41 ;; esac
printf 'mktemp-fixture' > "$temporary"
rm "$temporary"
node --input-type=module - <<'JS'
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
const expected = ` + quoteJSON(filepath.Join(st.ipc, ".tmp")) + `;
if (os.tmpdir() !== expected) throw new Error('Node temp scope mismatch');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'node-'));
fs.writeFileSync(path.join(tmp, 'fixture'), 'node-temp-fixture');
fs.rmSync(tmp, { recursive: true });
JS
`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		lastUser := ""
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				lastUser = string(msg.Content)
			}
		}
		var st stage
		for _, candidate := range stages {
			if strings.Contains(lastUser, "TEMP_STAGE:"+candidate.name) {
				st = candidate
			}
		}
		if st.name == "" {
			t.Error("unexpected synthetic model request")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		calls[st.name]++
		number := calls[st.name]
		mu.Unlock()
		if number == 1 && strings.HasPrefix(st.name, "parallel-") {
			ready <- st.name
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		delta := map[string]any{"role": "assistant", "content": "offline temp fixture complete"}
		finish := "stop"
		if number == 1 {
			toolCalls := []map[string]any{}
			for index, command := range []string{script(st), `node -e 'require("fs").writeFileSync(` + quoteJSON(filepath.Join(other, "real-failure")) + `, "must-not-write")'`} {
				arguments, _ := json.Marshal(map[string]any{"command": command, "workdir": cwd, "timeout": 20000})
				toolCalls = append(toolCalls, map[string]any{"index": index, "id": []string{"temp-success", "real-failure"}[index], "type": "function", "function": map[string]any{"name": "shell", "arguments": string(arguments)}})
			}
			delta, finish = map[string]any{"role": "assistant", "tool_calls": toolCalls}, "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			raw, _ := json.Marshal(map[string]any{"id": "temp-fixture", "object": "chat.completion.chunk", "model": "run-temp-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		chunk(delta, nil)
		chunk(map[string]any{}, finish)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "run-temp-fixture", APIKey: "offline-fixture-only"})
	if err != nil {
		t.Fatal("save fake localhost model failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	newClient := func(configDir, dataDir string) *Client {
		t.Helper()
		item, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: configDir, DataDir: dataDir, Config: provider.InstanceConfig{SystemPrompt: "Offline temp fixture", WritableRepositories: []string{cwd}}})
		if err != nil {
			t.Fatal(err)
		}
		client := item.(*Client)
		t.Cleanup(func() { _ = client.Close() })
		cfg := readConfig(t, configDir)
		cfg["agents"].(map[string]any)["title"] = map[string]any{"disabled": true}
		raw, _ := json.Marshal(cfg)
		writeFile(t, filepath.Join(configDir, "opencode", "opencode.json"), string(raw))
		return client
	}
	check := func(st stage, events []capturedEvent) error {
		var good, bad bool
		diagnostic := []string{}
		for _, event := range events {
			if event.Type == "provider.event" && event.Payload["kind"] == "tool" && (event.Payload["phase"] == "success" || event.Payload["phase"] == "failed") {
				content, _ := json.Marshal(event.Payload["content"])
				if len(content) > 1600 {
					content = content[:1600]
				}
				diagnostic = append(diagnostic, fmt.Sprintf("id=%v phase=%v metadata=%v content=%s error=%v", event.Payload["call_id"], event.Payload["phase"], event.Payload["metadata"], content, event.Payload["error"]))
			}
			if event.Type != "provider.event" || event.Payload["kind"] != "tool" || event.Payload["phase"] != "success" {
				continue
			}
			metadata, _ := event.Payload["metadata"].(map[string]any)
			if event.Payload["call_id"] == "temp-success" {
				good = metadata["exit"] != nil && intValue(metadata["exit"]) == 0
			}
			if event.Payload["call_id"] == "real-failure" {
				bad = metadata["exit"] != nil && intValue(metadata["exit"]) != 0
			}
		}
		if !good || !bad {
			return fmt.Errorf("%s: native shell did not report exact success=0/failure!=0; synthetic-only diagnostics: %s", st.name, strings.Join(diagnostic, "; "))
		}
		if raw, err := os.ReadFile(filepath.Join(st.ipc, "request.json")); err != nil || string(raw) != "heredoc-ipc-fixture" {
			return fmt.Errorf("%s: heredoc did not publish exact IPC marker on first attempt", st.name)
		}
		mu.Lock()
		count := calls[st.name]
		mu.Unlock()
		if count != 2 {
			return fmt.Errorf("%s: fixture unexpectedly retried model/tool calls (%d)", st.name, count)
		}
		return nil
	}
	client := newClient(configDir, dataDir)
	native := ""
	for index, st := range stages[:5] {
		if index == 1 {
			path := client.writeSandbox.env["PATH"]
			if path == "" {
				path = os.Getenv("PATH")
			}
			if err := client.requestJSON(ctx, http.MethodPut, "/api/session/"+native+"/environment", map[string]any{"variables": map[string]string{"PATH": path, "HOME": os.Getenv("HOME"), "TMPDIR": sibling, "TMP": sibling, "TEMP": sibling, "TMPPREFIX": filepath.Join(sibling, "zsh")}}, nil); err != nil {
				t.Fatal("native session environment fixture rejected")
			}
		}
		if index == 3 {
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client = newClient(configDir, dataDir)
			if found, err := client.VerifySession(ctx, native); err != nil || !found {
				t.Fatal("cold VerifySession failed")
			}
		}
		previous := client.process
		events, sink := captureSink()
		request := provider.RunRequest{RunID: st.name, CWD: cwd, Prompt: "TEMP_STAGE:" + st.name, Model: view.ModelRef, NativeSessionID: native, ReadOnly: st.readonly, ToolRequestDir: st.ipc}
		var session provider.NativeSession
		if native == "" {
			session, err = client.Start(ctx, request, sink)
		} else {
			session, err = client.Resume(ctx, request, sink)
		}
		if err != nil {
			t.Fatalf("%s native temp fixture failed: %v", st.name, err)
		}
		if index == 1 && previous != client.process {
			t.Fatal("same-scope environment override was not exercised on a warm native process")
		}
		if index != 1 && previous != nil && previous == client.process {
			t.Fatal("changed Run temp scope reused old native process/environment")
		}
		native = session.ID
		if err := check(st, *events); err != nil {
			t.Fatal(err)
		}
	}
	// Two truly overlapping native services in the same CWD get disjoint temps;
	// the read-only one still cannot write the writable one's IPC or business.
	results := make(chan error, 2)
	for _, st := range stages[5:] {
		parallel := newClient(permissionFixtureDirectory(t, t.TempDir()), permissionFixtureDirectory(t, t.TempDir()))
		go func(st stage) {
			events, sink := captureSink()
			_, err := parallel.Start(ctx, provider.RunRequest{RunID: st.name, CWD: cwd, Prompt: "TEMP_STAGE:" + st.name, Model: view.ModelRef, ReadOnly: st.readonly, ToolRequestDir: st.ipc}, sink)
			if err == nil {
				err = check(st, *events)
			}
			results <- err
		}(st)
	}
	for range 2 {
		select {
		case <-ready:
		case err := <-results:
			t.Fatalf("parallel native service ended before request overlap: %v", err)
		case <-ctx.Done():
			t.Fatal("parallel temp services did not overlap")
		}
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(other, "must-not-write"), filepath.Join(other, "real-failure"), filepath.Join(sibling, "must-not-write"), filepath.Join(cwd, "readonly-business-denied")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("native shell created forbidden temp/business marker")
		}
	}
	t.Log("real V2 zsh heredoc + Python tempfile + mktemp + Node temp succeeded first attempt; exact IPC, cold/warm Resume, readonly business denial, parallel disjoint temp and truthful exit codes verified; fake localhost only")
}
