//go:build darwin || linux

package codex

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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

type nativeIsolationJob struct {
	command    string
	patch      string
	escalate   bool
	served     bool
	lastOutput string
	started    chan<- struct{}
	release    <-chan struct{}
}

// This is a local deterministic Responses fixture, not a real model. The real
// Codex app-server still parses the actual model tool call and launches its own
// native sandbox. No auth file or ambient provider credential is inherited.
func TestNativeCodexExactIPCIsolationWithoutExternalModel(t *testing.T) {
	if os.Getenv("TEAMVA_CODEX_TEST") != "1" {
		t.Skip("set TEAMVA_CODEX_TEST=1 for isolated real Codex sandbox verification")
	}
	binary := os.Getenv("TEAMVA_CODEX_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("codex")
		if err != nil {
			t.Fatal("native Codex executable missing")
		}
	}
	binary, _ = filepath.Abs(binary)
	base := t.TempDir()
	if parent := os.Getenv("TEAMVA_CODEX_FIXTURE_ROOT"); parent != "" {
		var err error
		base, err = os.MkdirTemp(parent, "native-provider-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("retained isolated native fixture: %s", base)
	}
	mkdir := func(name string) string {
		t.Helper()
		path := filepath.Join(base, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return canonical
	}
	home, cwd := mkdir("home"), mkdir("workspace")
	t.Setenv("HOME", home) // factory LinkAuth never looks at the real home.
	first, second := mkdir("business-a"), mkdir("business-b")
	ipcA := mkdir("workspace/.teamva/tool-requests/run-a")
	ipcB := mkdir("workspace/.teamva/tool-requests/run-b")
	ipcRO := mkdir("workspace/.teamva/tool-requests/run-readonly")
	ipcScope := mkdir("workspace/.teamva/tool-requests/run-new-scope")
	ipcConcurrentA := mkdir("workspace/.teamva/tool-requests/concurrent-a")
	ipcConcurrentB := mkdir("workspace/.teamva/tool-requests/concurrent-b")
	writeProviderFile(t, filepath.Join(cwd, "AGENTS.md"), "Host-owned workspace manifest\n")
	writeProviderFile(t, filepath.Join(first, "source.txt"), "fixture\n")
	writeProviderFile(t, filepath.Join(first, "test.sh"), "#!/bin/sh\nset -eu\ntest \"$(cat source.txt)\" = fixture\nmkdir -p test-output\nprintf TEST_OK > test-output/result\n")
	if err := os.Chmod(filepath.Join(first, "test.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	var jobsMu sync.Mutex
	jobs := map[string]*nativeIsolationJob{}
	var responses atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "fixture body failed", 400)
			return
		}
		jobsMu.Lock()
		var selected *nativeIsolationJob
		var token string
		position := -1
		for name, job := range jobs {
			if at := strings.LastIndex(string(body), name); at > position || (at >= 0 && at == position && len(name) > len(token)) {
				position, token, selected = at, name, job
			}
		}
		if selected == nil {
			jobsMu.Unlock()
			http.Error(w, "no fixture job", 400)
			return
		}
		tool := !selected.served
		selected.served = true
		selected.lastOutput = nativeLatestToolOutput(body)
		jobsMu.Unlock()
		if tool && selected.started != nil {
			selected.started <- struct{}{}
			select {
			case <-selected.release:
			case <-r.Context().Done():
				return
			}
		}
		n := responses.Add(1)
		var item map[string]any
		if tool && selected.patch != "" {
			item = map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": token, "input": selected.patch}
		} else if tool {
			arguments := map[string]any{"cmd": selected.command, "max_output_tokens": 3000, "yield_time_ms": 10000}
			if selected.escalate {
				arguments["sandbox_permissions"] = "require_escalated"
				arguments["justification"] = "fixture attempt"
			}
			encoded, _ := json.Marshal(arguments)
			item = map[string]any{"type": "function_call", "name": "exec_command", "call_id": token, "arguments": string(encoded)}
		} else if match := nativeRunningProcess(body); match != 0 {
			encoded, _ := json.Marshal(map[string]any{"session_id": match, "chars": "", "yield_time_ms": 1000, "max_output_tokens": 3000})
			item = map[string]any{"type": "function_call", "name": "write_stdin", "call_id": fmt.Sprintf("%s-poll-%d", token, n), "arguments": string(encoded)}
		} else {
			item = map[string]any{"type": "message", "id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "FIXTURE_DONE"}}}
		}
		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("response-%d", n)}},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", n), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			encoded, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
		}
	}))
	defer server.Close()
	wrapper := filepath.Join(base, "isolated-codex")
	// An allow-listed environment, not os.Environ. No real user config, API key,
	// proxy, login, keychain credential or model endpoint reaches this process.
	writeProviderFile(t, wrapper, "#!/bin/sh\ncd "+quote(cwd)+" || exit 1\nexec /usr/bin/env -i HOME="+quote(home)+" CODEX_HOME=\"$CODEX_HOME\" PATH="+quote(os.Getenv("PATH"))+" XDG_CONFIG_HOME="+quote(filepath.Join(home, "config"))+" XDG_DATA_HOME="+quote(filepath.Join(home, "data"))+" XDG_CACHE_HOME="+quote(filepath.Join(home, "cache"))+" "+quote(binary)+" \"$@\"\n")
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	newClient := func(name string, roots []string) (*Client, string, string) {
		t.Helper()
		configDir, dataDir := mkdir(name+"/config"), mkdir(name+"/data")
		item, err := newInstance(adapter.InstanceOptions{Executable: wrapper, ConfigDir: configDir, DataDir: dataDir, Config: adapter.InstanceConfig{WritableRepositories: roots}})
		if err != nil {
			t.Fatal(err)
		}
		client := item.(*Client)
		t.Cleanup(func() {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		})
		payload, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		// Keep this offline fixture focused on explicit native/MCP capabilities;
		// otherwise each synthetic CODEX_HOME may auto-fetch a marketplace clone.
		prefix := "model_provider = \"fixture\"\nmodel = \"gpt-6.1-sol\"\ncli_auth_credentials_store = \"ephemeral\"\nfeatures.unified_exec = true\nfeatures.plugins = false\nfeatures.apps = false\n"
		footer := "\n[model_providers.fixture]\nname = \"local deterministic fixture\"\nwire_api = \"responses\"\nrequires_openai_auth = false\nbase_url = " + quote(server.URL+"/v1") + "\n"
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(prefix+string(payload)+footer), 0o600); err != nil {
			t.Fatal(err)
		}
		writeProviderFile(t, filepath.Join(configDir, "skills/bound/SKILL.md"), "fixture skill\n")
		return client, configDir, dataDir
	}
	client, configDir, dataDir := newClient("instance-a", []string{first, second})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := client.ensureStarted(ctx); err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
	}
	legacyMarker := filepath.Join(ipcB, "legacy-was-writable")
	err := client.call(ctx, "command/exec", map[string]any{"command": []string{"/bin/sh", "-c", "printf legacy > " + quote(legacyMarker)}, "cwd": cwd,
		"sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []string{ipcA}, "networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true}}, &baseline)
	if err != nil || baseline.ExitCode != 0 {
		t.Fatalf("could not reproduce native legacy implicit-CWD grant: %v", err)
	}
	if _, err := os.Stat(legacyMarker); err != nil {
		t.Fatal("legacy baseline did not write other Run IPC")
	}
	t.Log("confirmed real legacy workspaceWrite(exact own IPC) writes other Run IPC")
	paths := map[string]string{"first": filepath.Join(first, "probe"), "second": filepath.Join(second, "probe"), "cwd": filepath.Join(cwd, "probe"),
		"own": filepath.Join(ipcA, "probe"), "other": filepath.Join(ipcB, "probe"), "sibling": filepath.Join(filepath.Dir(ipcA), "probe"),
		"config": filepath.Join(configDir, "probe"), "data": filepath.Join(dataDir, "probe"), "skill": filepath.Join(dataDir, "skills/bound/SKILL.md"),
		"manifest": filepath.Join(cwd, "AGENTS.md"), "outside": filepath.Join(base, "probe")}
	// An ordinary writable file symlink must not confer its target's grant.
	for name, target := range map[string]string{"alias-other": ipcB, "alias-config": configDir, "alias-skill": filepath.Join(dataDir, "skills")} {
		alias := filepath.Join(first, name)
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(alias, "alias-probe")
	}
	probe := func(extra string) string {
		var command strings.Builder
		for name, path := range paths {
			fmt.Fprintf(&command, "if (printf modified > %s) 2>/dev/null; then echo %s=WRITE; else echo %s=DENIED; fi; ", quote(path), name, name)
		}
		command.WriteString(extra)
		return command.String()
	}
	run := func(c *Client, request adapter.RunRequest, command string, escalate bool, started chan<- struct{}, release <-chan struct{}, patches ...string) (adapter.NativeSession, string, error) {
		jobsMu.Lock()
		jobs[request.Prompt] = &nativeIsolationJob{command: command, escalate: escalate, started: started, release: release}
		if len(patches) > 0 {
			jobs[request.Prompt].patch = patches[0]
		}
		jobsMu.Unlock()
		var outputMu sync.Mutex
		var output strings.Builder
		sink := func(_ context.Context, event adapter.Event) error {
			if event.ProviderEventType == "item/completed" {
				item, _ := event.Payload["item"].(map[string]any)
				if item["type"] == "commandExecution" {
					text, _ := item["aggregatedOutput"].(string)
					outputMu.Lock()
					output.WriteString(text)
					outputMu.Unlock()
				}
			}
			return nil
		}
		var session adapter.NativeSession
		var err error
		if request.NativeSessionID == "" {
			session, err = c.Start(ctx, request, sink)
		} else {
			session, err = c.Resume(ctx, request, sink)
		}
		outputMu.Lock()
		text := output.String()
		outputMu.Unlock()
		if text == "" {
			jobsMu.Lock()
			text = jobs[request.Prompt].lastOutput
			jobsMu.Unlock()
		}
		return session, text, err
	}
	check := func(output string, writable ...string) {
		t.Helper()
		allowed := map[string]bool{}
		for _, name := range writable {
			allowed[name] = true
		}
		for name := range paths {
			want := name + "=DENIED\n"
			if allowed[name] {
				want = name + "=WRITE\n"
			}
			if !strings.Contains(output, want) {
				t.Fatalf("native sandbox outcome missing %q; output=%s", want, output)
			}
		}
	}
	request := adapter.RunRequest{RunID: "ordinary", Prompt: "am-fixture-ordinary", CWD: cwd, ToolRequestDir: ipcA}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		// /usr/bin/git is an Xcode locator shim, not git. Its xcrun cache is
		// intentionally not granted /tmp. Test the real installed git binary.
		if actual, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output(); err == nil {
			git = strings.TrimSpace(string(actual))
		}
	}
	legal := "cd " + quote(first) + " && ./test.sh && " + quote(git) + " init -q && " + quote(git) + " add source.txt && " + quote(git) + " -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture && echo LEGAL_SHELL_GIT_TEST_OK"
	session, output, err := run(client, request, probe(legal), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	check(output, "first", "second", "own")
	if !strings.Contains(output, "LEGAL_SHELL_GIT_TEST_OK") {
		t.Fatalf("legal Shell/git/test blocked: %s", output)
	}
	t.Log("ordinary: business A+B and own IPC write; CWD/other IPC/sibling/config/data/Skills/manifest/outside denied; real Shell/git/test passed")
	_, output, err = run(client, adapter.RunRequest{RunID: "default-git", Prompt: "am-fixture-default-git", CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: session.ID}, "git -C "+quote(first)+" status --short && echo DEFAULT_GIT_OK", false, nil, nil)
	if err != nil || !strings.Contains(output, "DEFAULT_GIT_OK") {
		t.Fatalf("default PATH git is not usable: %v %s", err, output)
	}
	t.Log("default PATH git status also works without a /tmp or TMPDIR write grant")
	_, output, err = run(client, adapter.RunRequest{RunID: "heredoc", Prompt: "am-fixture-heredoc", CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: session.ID}, nativeHeredocCommand(ipcA), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"CAT_HEREDOC_OK", "PYTHON_HEREDOC_TEMP_OK", "MKTEMP_OK", "NODE_TEMP_OK"} {
		if !strings.Contains(output, marker) {
			t.Fatalf("native default temp/heredoc failed (%s): %s", marker, output)
		}
	}
	t.Log("native cat/python heredocs, Python tempfile, shell mktemp and Node temp writes passed")
	worktree := filepath.Join(first, ".qa-worktrees", "git-proof")
	worktreeAdd := exec.CommandContext(ctx, git, "-C", first, "worktree", "add", "-b", "qa-native-worktree", worktree)
	worktreeAdd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if output, err := worktreeAdd.CombinedOutput(); err != nil {
		t.Fatalf("prepare isolated linked worktree: %v %s", err, output)
	}
	registeredWorktree, _, _ := newClient("registered-worktree", []string{first, second, worktree})
	worktreeCommand := "set -e\nprintf registered > " + quote(filepath.Join(worktree, "registered.txt")) + "\n" + quote(git) + " -C " + quote(worktree) + " add registered.txt\n" + quote(git) + " -C " + quote(worktree) + " -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm registered\necho REGISTERED_WORKTREE_COMMIT_OK"
	_, output, err = run(registeredWorktree, adapter.RunRequest{RunID: "registered-worktree", Prompt: "am-fixture-registered-worktree", CWD: cwd, ToolRequestDir: ipcA}, worktreeCommand, false, nil, nil)
	if err != nil || !strings.Contains(output, "REGISTERED_WORKTREE_COMMIT_OK") {
		t.Fatalf("authorized registered linked worktree cannot commit: %v %s", err, output)
	}
	t.Log("registered primary+secondary+linked worktree: native git add/commit succeeds with only already-authorized metadata writes")
	for _, tc := range []struct {
		name, root string
		readonly   bool
	}{
		{"worktree-without-primary", worktree, false}, {"pure-subdir", filepath.Join(first, "code-subdir"), false}, {"readonly-worktree", worktree, true},
	} {
		if err := os.MkdirAll(tc.root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots := []string{tc.root}
		if tc.readonly {
			roots = []string{first, second, worktree}
		}
		restricted, _, _ := newClient(tc.name, roots)
		command := quote(git) + " -C " + quote(tc.root) + " add . >/dev/null 2>&1; if test $? -eq 0; then echo OUTSIDE_METADATA_WRITE; else echo METADATA_WRITE_DENIED; fi"
		if !tc.readonly {
			command = "printf scoped > " + quote(filepath.Join(tc.root, "scoped.txt")) + " && " + command
		}
		_, output, err = run(restricted, adapter.RunRequest{RunID: tc.name, Prompt: "am-fixture-" + tc.name, CWD: cwd, ToolRequestDir: ipcA, ReadOnly: tc.readonly}, command, false, nil, nil)
		if err != nil || !strings.Contains(output, "METADATA_WRITE_DENIED") || strings.Contains(output, "OUTSIDE_METADATA_WRITE") {
			t.Fatalf("metadata boundary widened (%s): %v %s", tc.name, err, output)
		}
		if !tc.readonly {
			if data, err := os.ReadFile(filepath.Join(tc.root, "scoped.txt")); err != nil || string(data) != "scoped" {
				t.Fatal("restricted legitimate worktree/subdir file write failed")
			}
		}
	}
	t.Log("worktree without authorized primary, pure subdir, readonly: external/parent .git metadata still denied")
	// Factory LinkAuth must not make its source writable when a broad business
	// root contains a nested user home. Only a self-created fake auth is used.
	authBusiness := mkdir("auth-business")
	authHome := mkdir("auth-business/nested-home")
	authSource := filepath.Join(authHome, ".codex", "auth.json")
	fakeAuth := `{"OPENAI_API_KEY":"fake-native-fixture-not-a-real-key"}` + "\n"
	writeProviderFile(t, authSource, fakeAuth)
	t.Setenv("HOME", authHome)
	authClient, _, authData := newClient("auth-instance", []string{authBusiness})
	t.Setenv("HOME", home)
	authAlias := filepath.Join(authData, "auth.json")
	if target, err := os.Readlink(authAlias); err != nil || target != authSource {
		t.Fatal("fake auth was not linked by the factory")
	}
	var authSession adapter.NativeSession
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for index, attack := range []string{"direct-write", "direct-atomic", "direct-unlink", "direct-rename", "alias-write", "alias-atomic", "alias-unlink", "alias-rename", "rename-auth-dir", "rename-home-dir", "hardlink-write"} {
		code := `import os,sys,pathlib
s=pathlib.Path(sys.argv[1]);a=pathlib.Path(sys.argv[2]);h=pathlib.Path(sys.argv[3]);op=sys.argv[4]
t=a if op.startswith('alias') else s
try:
 if op=='hardlink-write':
  link=h.parent/'hardlink-auth';os.link(s,link);link.write_bytes(b'ATTACKED')
 elif op.endswith('-write'):t.write_bytes(b'ATTACKED')
 elif op.endswith('-atomic'):
  tmp=t.with_name('auth-attack.new');tmp.write_bytes(b'ATTACKED');os.replace(tmp,t)
 elif op.endswith('-unlink'):t.unlink()
 elif op.endswith('-rename'):t.rename(t.with_name('auth-moved.json'))
 elif op=='rename-auth-dir':s.parent.rename(s.parent.with_name('.codex-moved'))
 elif op=='rename-home-dir':h.rename(h.with_name('home-moved'))
 print('AUTH_ATTACK_ALLOWED')
except OSError:print('AUTH_ATTACK_DENIED')`
		command := quote(python) + " -c " + quote(code) + " " + quote(authSource) + " " + quote(authAlias) + " " + quote(authHome) + " " + quote(attack)
		request := adapter.RunRequest{RunID: fmt.Sprintf("auth-%d", index), Prompt: fmt.Sprintf("am-fixture-auth-%d", index), CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: authSession.ID}
		authSession, output, err = run(authClient, request, command, false, nil, nil)
		if err != nil || !strings.Contains(output, "AUTH_ATTACK_DENIED") || strings.Contains(output, "AUTH_ATTACK_ALLOWED") {
			t.Fatalf("fake auth write protection failed (%s): %v %s", attack, err, output)
		}
		if data, err := os.ReadFile(authSource); err != nil || string(data) != fakeAuth {
			t.Fatalf("original fake auth changed (%s)", attack)
		}
		if target, err := os.Readlink(authAlias); err != nil || target != authSource {
			t.Fatalf("fake auth alias changed (%s)", attack)
		}
	}
	_, output, err = run(authClient, adapter.RunRequest{RunID: "auth-normal", Prompt: "am-fixture-auth-normal", CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: authSession.ID}, "printf legal > "+quote(filepath.Join(authBusiness, "normal"))+" && "+quote(git)+" -C "+quote(authBusiness)+" init -q && "+quote(git)+" -C "+quote(authBusiness)+" add normal && "+quote(git)+" -C "+quote(authBusiness)+" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm legal && echo AUTH_SCOPE_NORMAL_GIT_OK\n"+nativeHeredocCommand(ipcA), false, nil, nil)
	if err != nil || !strings.Contains(output, "AUTH_SCOPE_NORMAL_GIT_OK") || !strings.Contains(output, "NODE_TEMP_OK") {
		t.Fatalf("auth protection blocked normal repository/git/node: %v %s", err, output)
	}
	t.Log("factory fake auth inside a business root: source+alias overwrite/atomic replace/unlink/rename and ancestor moves denied; ordinary business/git/node still work")
	for index, tc := range []struct {
		root              string
		readOnly, allowed bool
	}{
		{first, false, true}, {ipcA, false, true}, {ipcB, false, false}, {configDir, false, false},
		{filepath.Join(dataDir, "skills"), false, false}, {cwd, false, false}, {first, true, false}, {ipcA, true, true},
	} {
		path := filepath.Join(tc.root, fmt.Sprintf("patch-probe-%d", index))
		patch := "*** Begin Patch\n*** Add File: " + path + "\n+native-patch\n*** End Patch"
		_, _, err = run(client, adapter.RunRequest{RunID: fmt.Sprintf("patch-%d", index), Prompt: fmt.Sprintf("am-fixture-patch-%d", index), CWD: cwd, ToolRequestDir: ipcA, ReadOnly: tc.readOnly, NativeSessionID: session.ID}, "", false, nil, nil, patch)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := os.ReadFile(path)
		if tc.allowed {
			if readErr != nil || string(body) != "native-patch\n" {
				t.Fatalf("legitimate native apply_patch denied: %d", index)
			}
		} else if !os.IsNotExist(readErr) {
			t.Fatalf("native apply_patch escaped exact roots: %d", index)
		}
	}
	t.Log("real native apply_patch: ordinary business+own IPC allowed, other IPC/config/Skills/CWD denied; readonly only own IPC")
	if err := os.Rename(filepath.Join(first, ".git"), filepath.Join(first, ".fixture-git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ipcB, filepath.Join(first, ".git")); err != nil {
		t.Fatal(err)
	}
	paths["git-alias"] = filepath.Join(first, ".git", "alias-probe")
	_, output, err = run(client, adapter.RunRequest{RunID: "git-alias", Prompt: "am-fixture-git-alias", CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: session.ID}, probe(":"), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The native integrity guard refuses to spawn a command at all when an
	// explicit .git write root contains a symlink. This is a valid fail-closed
	// outcome, not a reason to retry outside the sandbox.
	jobsMu.Lock()
	aliasOutcome := jobs["am-fixture-git-alias"].lastOutput
	jobsMu.Unlock()
	if !strings.Contains(aliasOutcome, "symlinked writable roots are not supported") {
		t.Fatal("native git alias guard did not reject the writable-root symlink")
	}
	if _, err := os.Stat(filepath.Join(ipcB, "alias-probe")); !os.IsNotExist(err) {
		t.Fatal("native .git alias escaped to other Run IPC")
	}
	if err := os.Remove(filepath.Join(first, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(first, ".fixture-git"), filepath.Join(first, ".git")); err != nil {
		t.Fatal(err)
	}
	delete(paths, "git-alias")
	t.Log("preexisting .git symlink to other Run IPC denied despite legal in-root git metadata writes")
	request = adapter.RunRequest{RunID: "readonly", Prompt: "am-fixture-readonly", CWD: cwd, ToolRequestDir: ipcRO, ReadOnly: true, NativeSessionID: session.ID}
	paths["old-ipc"] = filepath.Join(ipcA, "old-ipc-probe")
	paths["old-tmp"] = filepath.Join(ipcA, codexRunTempName, "old-tmp-probe")
	paths["own"] = filepath.Join(ipcRO, "probe")
	_, output, err = run(client, request, probe(":"), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	check(output, "own")
	_, output, err = run(client, adapter.RunRequest{RunID: "readonly-heredoc", Prompt: "am-fixture-readonly-heredoc", CWD: cwd, ReadOnly: true, ToolRequestDir: ipcRO, NativeSessionID: session.ID}, nativeHeredocCommand(ipcRO), false, nil, nil)
	if err != nil || !strings.Contains(output, "CAT_HEREDOC_OK") || !strings.Contains(output, "PYTHON_HEREDOC_TEMP_OK") || !strings.Contains(output, "NODE_TEMP_OK") {
		t.Fatalf("readonly IPC temp/heredoc failed after Resume: %v %s", err, output)
	}
	t.Log("Resume to readonly: only new exact IPC+its private temp writable; heredoc/Python/Node work with the new Run environment")
	noBridgeTemp := `node -e 'const fs=require("node:fs"),os=require("node:os"),path=require("node:path");try{const d=fs.mkdtempSync(path.join(os.tmpdir(),"am-no-bridge-"));fs.rmSync(d,{recursive:true});console.log("NO_BRIDGE_TEMP_LEAK")}catch(e){if(e.code!=="EPERM"&&e.code!=="EACCES")throw e;console.log("NO_BRIDGE_TEMP_DENIED")}'`
	_, output, err = run(client, adapter.RunRequest{RunID: "readonly-no-ipc", Prompt: "am-fixture-no-ipc", CWD: cwd, ReadOnly: true, NativeSessionID: session.ID}, probe(noBridgeTemp), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	check(output)
	if !strings.Contains(output, "NO_BRIDGE_TEMP_DENIED") || strings.Contains(output, "NO_BRIDGE_TEMP_LEAK") {
		t.Fatal("no-bridge Run gained temp writes")
	}
	t.Log("Resume readonly without IPC: explicit empty roots revoke prior IPC/temp writes; Node default temp still denied")
	escalatedMarker := filepath.Join(ipcB, "escalated")
	_, _, err = run(client, adapter.RunRequest{RunID: "escalation", Prompt: "am-fixture-escalation", CWD: cwd, ToolRequestDir: ipcA, NativeSessionID: session.ID}, "printf escaped > "+quote(escalatedMarker), true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(escalatedMarker); !os.IsNotExist(err) {
		t.Fatal("native require_escalated bypassed the exact boundary")
	}
	t.Log("explicit native require_escalated tool attempt denied; no full-access fallback")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	// Same native durable home/thread, new factory authorization scope.
	scoped, _, _ := newClient("instance-a", []string{second})
	paths["own"] = filepath.Join(ipcScope, "probe")
	request = adapter.RunRequest{RunID: "scope", Prompt: "am-fixture-scope", CWD: cwd, ToolRequestDir: ipcScope, NativeSessionID: session.ID}
	resumed, output, err := run(scoped, request, probe(":"), false, nil, nil)
	if err != nil || resumed.ID != session.ID {
		t.Fatalf("durable scope Resume failed: %v", err)
	}
	check(output, "second", "own")
	t.Log("durable Resume with new factory scope: only business B + new IPC, no stale A/previous IPC")
	parallelA, _, _ := newClient("parallel-a", []string{first})
	parallelB, _, _ := newClient("parallel-b", []string{second})
	started, release := make(chan struct{}, 2), make(chan struct{})
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	for index, c := range []*Client{parallelA, parallelB} {
		own, other, business := ipcConcurrentA, ipcConcurrentB, first
		if index == 1 {
			own, other, business = ipcConcurrentB, ipcConcurrentA, second
		}
		command := "printf own > " + quote(filepath.Join(own, "probe")) + " && printf business > " + quote(filepath.Join(business, "parallel")) + " && if (printf cross > " + quote(filepath.Join(other, "cross-probe")) + ") 2>/dev/null; then echo CROSS_WRITE; else echo OWN_AND_BUSINESS_OK_OTHER_DENIED; fi"
		request := adapter.RunRequest{RunID: fmt.Sprintf("concurrent-%d", index), Prompt: fmt.Sprintf("am-fixture-concurrent-%d", index), CWD: cwd, ToolRequestDir: own}
		go func() { _, out, err := run(c, request, command, false, started, release); results <- result{out, err} }()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			close(release)
			t.Fatal("concurrent native turns never both became active")
		}
	}
	close(release)
	for range 2 {
		r := <-results
		if r.err != nil || !strings.Contains(r.output, "OWN_AND_BUSINESS_OK_OTHER_DENIED") {
			t.Fatalf("concurrent isolation failed: %v %s", r.err, r.output)
		}
	}
	t.Log("two real native instances concurrently share CWD: each legal business+own IPC works, peer IPC denied")
	if body, err := os.ReadFile(filepath.Join(configDir, "skills/bound/SKILL.md")); err != nil || string(body) != "fixture skill\n" {
		t.Fatal("host Skill modified")
	}
	if body, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md")); err != nil || string(body) != "Host-owned workspace manifest\n" {
		t.Fatal("host manifest modified")
	}
	if responses.Load() < 12 {
		t.Fatal("fixture did not exercise real native tool execution")
	}
}

func nativeHeredocCommand(ipc string) string {
	encoded, _ := json.Marshal(ipc)
	temp, _ := json.Marshal(filepath.Join(ipc, codexRunTempName))
	return fmt.Sprintf(`test "$TMPDIR" = %s && test "$TMP" = "$TMPDIR" && test "$TEMP" = "$TMPDIR" && echo EXACT_TEMP_ENV_OK
cat <<'AM_CAT_EOF' > %s/cat-heredoc.json
{"fixture":"cat"}
AM_CAT_EOF
test "$(cat %s/cat-heredoc.json)" = '{"fixture":"cat"}' && echo CAT_HEREDOC_OK
python3 - <<'AM_PY_EOF'
import json, pathlib, tempfile
ipc = pathlib.Path(%s)
(ipc / 'python-heredoc.json').write_text(json.dumps({'fixture':'python'}))
with tempfile.NamedTemporaryFile() as f:
    assert pathlib.Path(f.name).parent == ipc / '.tmp', f.name
    f.write(b'fixture'); f.flush()
print('PYTHON_HEREDOC_TEMP_OK')
AM_PY_EOF
am_tmp=$(mktemp) && test "$(dirname "$am_tmp")" = "$TMPDIR" && printf fixture > "$am_tmp" && rm "$am_tmp" && echo MKTEMP_OK
node -e 'const fs=require("node:fs"),os=require("node:os"),path=require("node:path"),assert=require("node:assert");assert.strictEqual(os.tmpdir(),%s);const d=fs.mkdtempSync(path.join(os.tmpdir(),"am-native-"));fs.writeFileSync(path.join(d,"fixture"),"fixture");fs.rmSync(d,{recursive:true});console.log("NODE_TEMP_OK")'
`, shellFixtureQuote(filepath.Join(ipc, codexRunTempName)), shellFixtureQuote(ipc), shellFixtureQuote(ipc), string(encoded), string(temp))
}

func shellFixtureQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func nativeLatestToolOutput(body []byte) string {
	var request struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil {
		return ""
	}
	var output string
	for _, item := range request.Input {
		if item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output" {
			output, _ = item["output"].(string)
		}
	}
	return output
}

func nativeRunningProcess(body []byte) int {
	output := nativeLatestToolOutput(body)
	match := regexp.MustCompile(`Process running with session ID ([0-9]+)`).FindStringSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	id, _ := strconv.Atoi(match[1])
	return id
}
