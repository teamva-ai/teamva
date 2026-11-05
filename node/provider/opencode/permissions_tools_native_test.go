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
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// A deterministic localhost HTTP fixture emits synthetic tool calls. The real
// bundled V2 file tools execute; no real model, account, or vendor is involved.
// This complements the permission API test and does not replace Ego/live QA.
func TestBundledPermissionFileToolsOffline(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for offline native file tools (no model)")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	project := permissionFixtureDirectory(t, base, "project")
	cwd := permissionFixtureDirectory(t, project, "repo")
	sibling := permissionFixtureDirectory(t, project, "repo-extra")
	second := permissionFixtureDirectory(t, project, "second")
	reference := permissionFixtureDirectory(t, base, "reference")
	if err := exec.Command("git", "init", "--quiet", project).Run(); err != nil {
		t.Fatal(err)
	}
	configDir := permissionFixtureDirectory(t, base, "config")
	skill := permissionFixtureDirectory(t, configDir, "skills", "bound")
	ipc := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "run-exact")
	for _, target := range []string{filepath.Join(sibling, "marker.txt"), filepath.Join(project, "marker.txt"), filepath.Join(skill, "SKILL.md")} {
		writeFile(t, target, "unchanged-fixture")
	}
	writeFile(t, filepath.Join(cwd, "allowed-edit.txt"), "edit-before")
	writeFile(t, filepath.Join(second, "shell-boundary.test.mjs"), `import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
const deny = operation => assert.throws(operation, error => error.code === 'EPERM' || error.code === 'EACCES');
test('native OpenCode shell has a kernel-enforced write boundary', () => {
  fs.writeFileSync('shell-node-allowed', 'shell-allowed');
  for (const target of `+quoteJSON([]string{filepath.Join(project, "marker.txt"), filepath.Join(sibling, "marker.txt"), filepath.Join(reference, "shell-denied"), filepath.Join(skill, "SKILL.md"), filepath.Join(cwd, "linked-dir", "marker.txt"), filepath.Join(cwd, "hardlinked-file"), filepath.Join(cwd, ".account-fixture", "opencode", "auth.json")})+`) {
    deny(() => fs.writeFileSync(target, 'must-not-write'));
  }
});
`)
	if err := os.Symlink(sibling, filepath.Join(cwd, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(sibling, "marker.txt"), filepath.Join(cwd, "hardlinked-file")); err != nil {
		t.Fatal(err)
	}
	type invocation struct {
		id, name string
		input    map[string]any
		phase    string
	}
	write := func(id, target, phase string) invocation {
		return invocation{id, "write", map[string]any{"path": target, "content": "fixture-allowed"}, phase}
	}
	batches := [][]invocation{
		{
			write("allow-root", "ok.txt", "success"),
			write("allow-second", filepath.Join(second, "ok.txt"), "success"),
			{"allow-edit", "edit", map[string]any{"path": "allowed-edit.txt", "oldString": "edit-before", "newString": "fixture-allowed"}, "success"},
			{"allow-shell", "shell", map[string]any{"workdir": second, "command": "touch shell-ok && git init --quiet && git add shell-ok && git -c user.name=Fixture -c user.email=fixture@example.invalid -c commit.gpgsign=false commit --quiet -m fixture && node --test shell-boundary.test.mjs", "timeout": 30000}, "success"},
			write("deny-sibling", "../repo-extra/marker.txt", "failed"),
			write("deny-parent", "../marker.txt", "failed"),
			write("deny-reference", filepath.Join(base, "reference", "marker.txt"), "failed"),
			write("deny-skill", filepath.Join(skill, "SKILL.md"), "failed"),
			write("deny-symlink", "linked-dir/marker.txt", "failed"),
			write("deny-hardlink", "hardlinked-file", "failed"),
			write("deny-other-ipc", ".teamva/tool-requests/run-other/request.json", "failed"),
			{"deny-edit", "edit", map[string]any{"path": "../repo-extra/marker.txt", "oldString": "unchanged-fixture", "newString": "must-not-change"}, "failed"},
		},
		{
			write("readonly-deny-root", "readonly.txt", "failed"),
			write("readonly-allow-ipc", filepath.Join(ipc, "request.json"), "success"),
			write("readonly-deny-ipc", ".teamva/tool-requests/run-other/request.json", "failed"),
		},
		{
			{"allow-patch", "patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: allowed-patch.txt\n+fixture-allowed\n*** End Patch"}, "success"},
			{"deny-patch", "patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: mixed-patch.txt\n+must-not-appear\n*** Update File: ../repo-extra/marker.txt\n@@\n-unchanged-fixture\n+must-not-change\n*** End Patch"}, "failed"},
			{"deny-patch-alias", "patch", map[string]any{"patchText": "*** Begin Patch\n*** Update File: linked-dir/marker.txt\n@@\n-unchanged-fixture\n+must-not-change\n*** End Patch"}, "failed"},
			{"deny-patch-move", "patch", map[string]any{"patchText": "*** Begin Patch\n*** Update File: allowed-edit.txt\n*** Move to: ../repo-extra/moved.txt\n@@\n-fixture-allowed\n+must-not-change\n*** End Patch"}, "failed"},
		},
	}
	var batch atomic.Int32
	var calls [3]atomic.Int32
	var modelRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		modelRequests.Add(1)
		var body struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		index := int(batch.Load())
		delta := map[string]any{"role": "assistant", "content": "fixture done"}
		finish := "stop"
		if len(body.Tools) > 0 && calls[index].Add(1) == 1 {
			toolCalls := []map[string]any{}
			for n, call := range batches[index] {
				arguments, _ := json.Marshal(call.input)
				toolCalls = append(toolCalls, map[string]any{"index": n, "id": call.id, "type": "function", "function": map[string]any{"name": call.name, "arguments": string(arguments)}})
			}
			delta = map[string]any{"role": "assistant", "tool_calls": toolCalls}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			value, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "permission-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", value)
		}
		chunk(delta, nil)
		chunk(map[string]any{}, finish)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "permission-fixture", APIKey: "offline-fixture-only"})
	if err != nil {
		t.Fatal("save isolated synthetic provider failed")
	}
	// V2 advertises patch instead of write/edit only for GPT-style model IDs.
	// Both IDs resolve to the same deterministic local HTTP fixture, not a model.
	patchView, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "gpt-permission-fixture", APIKey: "offline-fixture-only"})
	if err != nil {
		t.Fatal("save isolated synthetic patch provider failed")
	}
	// A real native cold verification/run must still work when its original
	// fake account source is INSIDE a writable business root. No real credentials
	// are consulted: this is the same synthetic key used only by localhost.
	accountHome := permissionFixtureDirectory(t, cwd, ".account-fixture")
	accountSource := filepath.Join(accountHome, "opencode", "auth.json")
	providerID, _, _ := strings.Cut(view.ModelRef, "/")
	accountPayload := quoteJSON(map[string]any{providerID: map[string]string{"type": "api", "key": "offline-fixture-only"}})
	writeFile(t, accountSource, accountPayload)
	t.Setenv("XDG_DATA_HOME", accountHome)
	options := provider.InstanceOptions{Executable: binary, ConfigDir: configDir, DataDir: permissionFixtureDirectory(t, base, "data"), Config: provider.InstanceConfig{WritableRepositories: []string{cwd, second}}}
	adapter, err := newInstance(options)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client := adapter.(*Client)
	client.WithReadOnlySkillDirs(skill)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if exists, err := client.VerifySession(ctx, "ses_missing"); err != nil || exists {
		t.Fatal("real V2 cold VerifySession before Start failed")
	}
	if !client.sandboxReady || modelRequests.Load() != 0 {
		t.Fatal("verification before Start was unbounded or sent a synthetic model request")
	}
	var sessionID string
	previousProcess := client.process
	for index := range batches {
		if index == 1 {
			// Preserve the real native DB but discard the whole Client/process,
			// reproducing the Runtime's cold verify-before-Resume sequence.
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			recreated, err := newInstance(options)
			if err != nil {
				t.Fatal(err)
			}
			defer recreated.Close()
			client = recreated.(*Client)
			client.WithReadOnlySkillDirs(skill)
			before := modelRequests.Load()
			if exists, err := client.VerifySession(ctx, sessionID); err != nil || !exists {
				t.Fatal("real V2 could not cold-verify its persisted session before Resume")
			}
			if !client.sandboxReady || before != modelRequests.Load() {
				t.Fatal("cold stored-session verification was unbounded or invoked the synthetic model")
			}
			previousProcess = client.process
		}
		batch.Store(int32(index))
		events, sink := captureSink()
		request := provider.RunRequest{RunID: fmt.Sprintf("offline-tools-%d", index), CWD: cwd, Prompt: "Execute the deterministic fixture tools", Model: view.ModelRef, ReadOnly: index == 1, ToolRequestDir: ipc, NativeSessionID: sessionID}
		if index == 2 {
			request.Model = patchView.ModelRef
		}
		var session provider.NativeSession
		if index != 1 {
			session, err = client.Start(ctx, request, sink)
		} else {
			session, err = client.Resume(ctx, request, sink)
		}
		if err != nil {
			t.Fatalf("offline native file tools failed in batch %d (details suppressed): %v", index, err)
		}
		client.mu.Lock()
		if index == 1 && previousProcess == client.process {
			t.Error("read-only Resume reused the previous writable OS sandbox process")
		}
		previousProcess = client.process
		client.mu.Unlock()
		sessionID = session.ID
		phases := map[string]string{}
		for _, event := range *events {
			if event.Type == "provider.event" && event.Payload["kind"] == "tool" {
				phases[stringValue(event.Payload["call_id"])] = stringValue(event.Payload["phase"])
				if event.Payload["call_id"] == "allow-shell" && event.Payload["phase"] == "success" {
					metadata, _ := event.Payload["metadata"].(map[string]any)
					if metadata["exit"] == nil || intValue(metadata["exit"]) != 0 {
						t.Error("native shell/git/node fixture did not exit successfully")
					}
				}
			}
		}
		for _, call := range batches[index] {
			if phases[call.id] != call.phase {
				t.Errorf("native tool %s phase=%s want=%s", call.id, phases[call.id], call.phase)
			}
		}
	}
	for _, target := range []string{filepath.Join(sibling, "marker.txt"), filepath.Join(project, "marker.txt"), filepath.Join(skill, "SKILL.md")} {
		if payload, err := os.ReadFile(target); err != nil || string(payload) != "unchanged-fixture" {
			t.Error("native file tool changed a forbidden marker")
		}
	}
	for _, target := range []string{filepath.Join(cwd, "readonly.txt"), filepath.Join(cwd, "mixed-patch.txt"), filepath.Join(base, "reference", "marker.txt"), filepath.Join(sibling, "moved.txt"), filepath.Join(cwd, ".teamva", "tool-requests", "run-other", "request.json")} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Error("native file tool created a forbidden file")
		}
	}
	for _, target := range []string{filepath.Join(cwd, "ok.txt"), filepath.Join(cwd, "allowed-edit.txt"), filepath.Join(second, "ok.txt"), filepath.Join(ipc, "request.json")} {
		if payload, err := os.ReadFile(target); err != nil || string(payload) != "fixture-allowed" {
			t.Error("native file tool did not create an authorized fixture marker")
		}
	}
	if payload, err := os.ReadFile(filepath.Join(cwd, "allowed-patch.txt")); err != nil || string(payload) != "fixture-allowed\n" {
		t.Error("native patch did not create an authorized fixture marker")
	}
	if payload, err := os.ReadFile(filepath.Join(second, "shell-node-allowed")); err != nil || string(payload) != "shell-allowed" {
		t.Error("native shell/node did not write its authorized marker")
	}
	if _, err := os.Stat(filepath.Join(reference, "shell-denied")); !os.IsNotExist(err) {
		t.Error("native shell/node created an unauthorized marker")
	}
	if payload, err := os.ReadFile(accountSource); err != nil || string(payload) != accountPayload {
		t.Error("native verification/run changed its original fake account source inside a business root")
	}
	t.Log("real V2 shell/git/node test + write/edit/patch, OS/alias boundaries and read-only Resume exercised with synthetic localhost responses; no real model/account")
}
