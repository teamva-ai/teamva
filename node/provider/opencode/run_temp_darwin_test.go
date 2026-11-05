//go:build darwin

package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestMacOSRunTempEnvironmentScopeAndNoBridgeBoundary(t *testing.T) {
	isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	first := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "first")
	second := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "second")
	client := testClient(t)
	client.WithConfigDir(permissionFixtureDirectory(t, base, "config")).WithDataDir(permissionFixtureDirectory(t, base, "data"))
	client.writableRoots = []string{cwd}
	a, err := buildWriteSandbox(t.Context(), client, provider.RunRequest{CWD: cwd, ToolRequestDir: first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildWriteSandbox(t.Context(), client, provider.RunRequest{CWD: cwd, ToolRequestDir: second, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		sandbox *writeSandbox
		ipc     string
	}{{a, first}, {b, second}} {
		want := filepath.Join(item.ipc, ".tmp")
		for _, key := range []string{"TMPDIR", "TMP", "TEMP", "OPENCODE_TEAMVA_RUN_TMP"} {
			if item.sandbox.env[key] != want {
				t.Fatalf("%s did not use exact Run IPC temp", key)
			}
		}
		if item.sandbox.env["TMPPREFIX"] != filepath.Join(want, "zsh") {
			t.Fatal("zsh here-doc prefix missing")
		}
	}
	if a.fingerprint == b.fingerprint {
		t.Fatal("different per-Run temp scopes reused the same fingerprint")
	}
	client.sandboxReady = true
	for _, request := range []provider.RunRequest{{CWD: cwd, ReadOnly: true}, {CWD: cwd, ReadOnly: true, ToolRequestDir: first}} {
		want := "deny"
		if request.ToolRequestDir != "" {
			want = "allow"
		}
		if permissionEffect(client.permissionRules(request), "shell", "python3 - <<'PY'") != want {
			t.Fatal("read-only Shell capability did not follow exact sandboxed host IPC")
		}
	}
	for _, noTools := range []bool{false, true} {
		unused := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "unused")
		request := provider.RunRequest{CWD: cwd, ReadOnly: true, DisableTools: noTools}
		if noTools {
			request.ToolRequestDir = unused
		} // A hostile direct caller cannot force temp creation.
		c, err := buildWriteSandbox(t.Context(), client, request)
		if err != nil {
			t.Fatal(err)
		}
		if c.runTemp != nil || c.env["OPENCODE_TEAMVA_RUN_TMP"] != "" || c.env["TMPDIR"] == filepath.Join(unused, ".tmp") {
			t.Fatal("no bridge/NoTools acquired Run temporary capabilities")
		}
		if _, err := os.Stat(filepath.Join(unused, ".tmp")); !os.IsNotExist(err) {
			t.Fatal("NoTools created a Run temporary directory")
		}
	}
}

func TestMacOSWarmRunTempReplacementFailsBeforeRecreation(t *testing.T) {
	isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	ipc := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "run")
	client := testClient(t)
	client.WithConfigDir(permissionFixtureDirectory(t, base, "config")).WithDataDir(permissionFixtureDirectory(t, base, "data"))
	request := provider.RunRequest{CWD: cwd, ToolRequestDir: ipc}
	if err := client.prepareRunSandbox(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(ipc, ".tmp"), filepath.Join(ipc, "previous-tmp")); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareRunSandbox(t.Context(), request); err == nil {
		t.Fatal("warm Run silently replaced its pinned temp directory")
	}
	if _, err := os.Stat(filepath.Join(ipc, ".tmp")); !os.IsNotExist(err) {
		t.Fatal("unsafe warm scope created a replacement temp before rejecting it")
	}
}
