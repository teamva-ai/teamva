//go:build darwin

package opencode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestMacOSSandboxScopeChangesAndQuotedRoots(t *testing.T) {
	isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	root := permissionFixtureDirectory(t, base, `repo"(quoted)`)
	config := permissionFixtureDirectory(t, base, "config")
	data := permissionFixtureDirectory(t, base, "data")
	oldIPC := permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "old")
	newIPC := permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "new")
	client := testClient(t)
	client.WithConfigDir(config).WithDataDir(data)
	client.writableRoots = []string{root}
	run := func(request provider.RunRequest, script string, args ...string) {
		t.Helper()
		if err := client.prepareRunSandbox(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		cmd, err := client.writeSandbox.command("/bin/sh", append([]string{"-euc", script, "fixture"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Dir = root
		cmd.Env = client.writeSandbox.environment([]string{"PATH=" + os.Getenv("PATH")})
		if err := cmd.Run(); err != nil {
			t.Fatal("OS sandbox did not enforce a changed/quoted exact scope")
		}
	}
	request := provider.RunRequest{CWD: root, ToolRequestDir: oldIPC}
	run(request, `touch "$1"; if touch "$2" 2>/dev/null; then exit 11; fi`, filepath.Join(oldIPC, "allowed"), filepath.Join(base, "denied"))
	request.ToolRequestDir = newIPC
	run(request, `touch "$1"; if touch "$2" 2>/dev/null; then exit 12; fi`, filepath.Join(newIPC, "allowed"), filepath.Join(oldIPC, "revoked"))
	request.ReadOnly = true
	run(request, `touch "$1"; if touch "$2" 2>/dev/null; then exit 13; fi`, filepath.Join(newIPC, "readonly-transport"), filepath.Join(root, "readonly-denied"))
	for _, path := range []string{filepath.Join(base, "denied"), filepath.Join(oldIPC, "revoked"), filepath.Join(root, "readonly-denied")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("a revoked or quoted-root scope created an unauthorized marker")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.prepareRunSandbox(canceled, request); err != context.Canceled {
		t.Fatal("canceled sandbox preparation did not stop promptly")
	}
}

func TestMacOSWriteSandboxActualShellGitAndNode(t *testing.T) {
	if err := platformSandboxAvailable(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("Node is required for the real macOS write sandbox fixture")
	}
	home := isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	root := permissionFixtureDirectory(t, base, "repo")
	second := permissionFixtureDirectory(t, base, "second")
	sibling := permissionFixtureDirectory(t, base, "repo-extra")
	reference := permissionFixtureDirectory(t, base, "reference")
	skill := permissionFixtureDirectory(t, root, "bound-skill")
	config := permissionFixtureDirectory(t, base, "config")
	data := permissionFixtureDirectory(t, base, "data")
	ipc := permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "run-exact")
	permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "run-other")
	aliases := permissionFixtureDirectory(t, root, "aliases")
	credential := filepath.Join(base, "auth.fixture")
	writeFile(t, credential, "fixture-only-read-auth")
	writeFile(t, filepath.Join(sibling, "original"), "unchanged-fixture")
	writeFile(t, filepath.Join(skill, "SKILL.md"), "unchanged-skill")
	writeFile(t, filepath.Join(config, "opencode.json"), "unchanged-config")
	if err := os.Symlink(credential, filepath.Join(data, "auth-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(root, "sibling-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(sibling, "original"), filepath.Join(aliases, "hard")); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	// Assertions intentionally do not print auth contents, environment or raw
	// command output. Every path/credential here is this test's own fixture.
	script := `import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
const root = ` + quote(root) + `;
const deny = operation => assert.throws(operation, error => error.code === 'EPERM' || error.code === 'EACCES');
test('authorized root, IPC and private runtime caches are writable', () => {
  fs.writeFileSync(path.join(root, 'node-allowed'), 'allowed');
  fs.writeFileSync(` + quote(filepath.Join(second, "node-allowed")) + `, 'allowed');
  fs.writeFileSync(` + quote(filepath.Join(ipc, "request.json")) + `, '{}');
  fs.writeFileSync(path.join(process.env.TMPDIR, 'private-temp'), 'allowed');
  fs.writeFileSync(path.join(process.env.XDG_CACHE_HOME, 'private-cache'), 'allowed');
  const child = spawnSync('/bin/sh', ['-c', 'printf child > child-allowed'], { cwd: root });
  assert.equal(child.status, 0);
});
test('parent, sibling, reference, other IPC, config and Skills are OS denied', () => {
  for (const target of ` + quoteJSON([]string{filepath.Join(base, "parent-denied"), filepath.Join(sibling, "denied"), filepath.Join(reference, "denied"), filepath.Join(root, ".teamva", "tool-requests", "run-other", "request.json"), filepath.Join(config, "opencode.json"), filepath.Join(skill, "SKILL.md")}) + `) {
    deny(() => fs.writeFileSync(target, 'must-not-write'));
  }
});
test('symlink, preexisting hardlink, link creation and ancestor rename cannot escape', () => {
  deny(() => fs.writeFileSync(path.join(root, 'sibling-link', 'denied'), 'must-not-write'));
  deny(() => fs.writeFileSync(path.join(root, 'aliases', 'hard'), 'must-not-write'));
  deny(() => fs.renameSync(path.join(root, 'aliases'), path.join(root, 'renamed-aliases')));
  deny(() => fs.linkSync(` + quote(filepath.Join(sibling, "original")) + `, path.join(root, 'new-hard')));
});
test('CLI can read its original auth link, but cannot modify its target', () => {
  assert.equal(fs.readFileSync(` + quote(filepath.Join(data, "auth-link")) + `, 'utf8'), 'fixture-only-read-auth');
  deny(() => fs.writeFileSync(` + quote(filepath.Join(data, "auth-link")) + `, 'must-not-write'));
});
test('ordinary shell redirects also inherit the kernel policy', () => {
  const child = spawnSync('/bin/sh', ['-c', 'printf denied > "$1"', 'fixture', ` + quote(filepath.Join(sibling, "shell-denied")) + `]);
  assert.notEqual(child.status, 0);
});
test('a nested sandbox-exec cannot remove the parent write boundary', () => {
  const child = spawnSync('/usr/bin/sandbox-exec', ['-p', '(version 1)(allow default)', '/bin/sh', '-c', 'printf denied > "$1"', 'fixture', ` + quote(filepath.Join(sibling, "nested-sandbox-denied")) + `]);
  assert.notEqual(child.status, 0);
});
`
	writeFile(t, filepath.Join(root, "sandbox.test.mjs"), script)
	client := testClient(t)
	client.WithConfigDir(config).WithDataDir(data).WithReadOnlySkillDirs(skill)
	client.writableRoots = []string{root, second}
	request := provider.RunRequest{CWD: root, ToolRequestDir: ipc}
	if err := client.prepareRunSandbox(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	cmd, err := client.writeSandbox.command("/bin/sh", "-euc", `touch shell-allowed
git init --quiet
git add shell-allowed
git -c user.name=Fixture -c user.email=fixture@example.invalid -c commit.gpgsign=false commit --quiet -m fixture
git worktree add --quiet --detach "$1" HEAD
git -C "$1" status --porcelain
node --test sandbox.test.mjs`, "fixture", second)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = root
	cmd.Env = client.writeSandbox.environment([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"})
	output, err := cmd.CombinedOutput()
	if err != nil {
		// All output belongs to this closed fixture; still avoid echoing paths,
		// auth fixture data, or environment values in routine test failures.
		t.Fatalf("real sandbox shell/git/node fixture failed; output contained TAP=%v (raw output suppressed)", strings.Contains(string(output), "TAP version"))
	}
	for _, path := range []string{filepath.Join(base, "parent-denied"), filepath.Join(sibling, "denied"), filepath.Join(sibling, "shell-denied"), filepath.Join(sibling, "nested-sandbox-denied"), filepath.Join(reference, "denied")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("OS sandbox created an out-of-root marker")
		}
	}
	for path, expected := range map[string]string{credential: "fixture-only-read-auth", filepath.Join(sibling, "original"): "unchanged-fixture", filepath.Join(skill, "SKILL.md"): "unchanged-skill", filepath.Join(config, "opencode.json"): "unchanged-config"} {
		if payload, err := os.ReadFile(path); err != nil || string(payload) != expected {
			t.Error("OS sandbox changed a protected fixture")
		}
	}
	t.Log("real sandbox-exec + shell touch + git commit/worktree + node --test passed; unauthorized OS writes, aliases, hardlinks and inherited redirects denied")
}
