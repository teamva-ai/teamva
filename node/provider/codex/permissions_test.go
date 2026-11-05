package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestExactPermissionsRefusesUnverifiedOrOlderNativeServers(t *testing.T) {
	for _, ua := range []string{"", "teamva", "teamva/0.162.9", "teamva/0.163.0-alpha.1", "teamva/not-a-version", "teamva/0.-1.0"} {
		if codexSupportsExactPermissions(ua) {
			t.Fatalf("unsupported native server accepted: %q", ua)
		}
	}
	for _, ua := range []string{"teamva/0.163.0 (Mac OS 27.0.1; arm64)", "teamva/0.164.1", "teamva/1.0.0"} {
		if !codexSupportsExactPermissions(ua) {
			t.Fatalf("supported native server rejected: %q", ua)
		}
	}
}

func TestEveryThreadAndTurnReplacesExactWritableRoots(t *testing.T) {
	repositories := []string{"/business/a", "/business/b"}
	for _, tc := range []struct {
		name     string
		readOnly bool
		ipc      string
		want     []string
	}{
		{"ordinary", false, "/host/cwd/.teamva/tool-requests/one", []string{repositories[0], repositories[1], "/host/cwd/.teamva/tool-requests/one"}},
		{"ordinary-without-ipc", false, "", repositories},
		{"readonly", true, "/host/cwd/.teamva/tool-requests/two", []string{"/host/cwd/.teamva/tool-requests/two"}},
		{"readonly-revokes-all", true, "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := adapter.RunRequest{CWD: "/host/cwd", ReadOnly: tc.readOnly, ToolRequestDir: tc.ipc}
			for name, params := range map[string]map[string]any{
				"thread": threadStartParams(request, repositories...),
				"turn":   turnStartParams(request, "resumed-thread", repositories...),
			} {
				if !reflect.DeepEqual(params["runtimeWorkspaceRoots"], tc.want) || params["permissions"] != codexPermissionProfile {
					t.Fatalf("%s roots=%v want=%v", name, params["runtimeWorkspaceRoots"], tc.want)
				}
				for _, key := range []string{"sandbox", "sandboxPolicy"} {
					if _, legacy := params[key]; legacy {
						t.Fatalf("%s retained an implicit CWD grant", name)
					}
				}
			}
		})
	}
	config := codexPermissionConfig()
	profile := config["permissions"].(map[string]any)[codexPermissionProfile].(map[string]any)
	if !reflect.DeepEqual(profile["filesystem"], map[string]any{":root": "read", ":workspace_roots": map[string]any{".": "write", ".git": "write"}}) ||
		!reflect.DeepEqual(profile["network"], map[string]any{"enabled": false}) {
		t.Fatal("profile gained implicit CWD, temp or network access")
	}
}

func TestFactoryFreezesCanonicalBusinessRootsWithoutInferringCWD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repository-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	roots := []string{root, alias, filepath.Join(root, "missing")}
	item, err := newInstance(adapter.InstanceOptions{ConfigDir: t.TempDir(), Config: adapter.InstanceConfig{WritableRepositories: roots}})
	if err != nil {
		t.Fatal(err)
	}
	defer item.Close()
	roots[0] = "/unauthorized-replacement"
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(item.(*Client).writableRepositories, []string{canonical}) {
		t.Fatal("factory did not keep an immutable canonical authorization snapshot")
	}
}
