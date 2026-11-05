package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func quoteJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func isolateNativeFixtureEnvironment(t *testing.T) {
	t.Helper()
	// Keep executable lookup and the caller-approved temp root, but never
	// forward ambient real Provider keys or NODE_OPTIONS into a native fixture.
	pathEnv, tempEnv := os.Getenv("PATH"), os.Getenv("TMPDIR")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		t.Setenv(name, "")
	}
	t.Setenv("PATH", pathEnv)
	t.Setenv("TMPDIR", tempEnv)
	isolateUserHomes(t)
}

// Mirrors V2 whole-value wildcard matching, deliberately not filepath.Match:
// '*' in native permissions also matches slashes. Real-evaluator tests below
// are the authority for the session/API integration.
func permissionEffect(rules []map[string]string, action, resource string) string {
	match := func(value, pattern string) bool {
		pattern = regexp.QuoteMeta(filepath.ToSlash(pattern))
		pattern = strings.ReplaceAll(strings.ReplaceAll(pattern, `\*`, ".*"), `\?`, ".")
		if strings.HasSuffix(pattern, " .*") {
			pattern = strings.TrimSuffix(pattern, " .*") + "( .*)?"
		}
		return regexp.MustCompile("(?s)^" + pattern + "$").MatchString(filepath.ToSlash(value))
	}
	effect := "ask"
	for _, rule := range rules {
		if match(action, rule["action"]) && match(resource, rule["resource"]) {
			effect = rule["effect"]
		}
	}
	return effect
}

func permissionFixtureDirectory(t *testing.T, root string, parts ...string) string {
	t.Helper()
	value := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(value, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestPermissionRulesExactWritableRepositories(t *testing.T) {
	base := permissionFixtureDirectory(t, t.TempDir())
	first := permissionFixtureDirectory(t, base, "project", "first")
	second := permissionFixtureDirectory(t, base, "project", "second")
	sibling := permissionFixtureDirectory(t, base, "project", "first-extra")
	client := testClient(t)
	client.writableRoots = []string{first, second}
	for _, cwd := range []string{first, filepath.Dir(first), permissionFixtureDirectory(t, first, "nested", "leaf")} {
		rules := client.permissionRules(provider.RunRequest{CWD: cwd})
		for _, test := range []struct {
			path string
			want string
		}{
			{filepath.Join(first, "ok.txt"), "allow"},
			{filepath.Join(first, "deep", "ok.txt"), "allow"},
			{filepath.Join(second, "ok.txt"), "allow"},
			{filepath.Join(sibling, "blocked.txt"), "deny"},
			{filepath.Join(base, "project", "blocked.txt"), "deny"},
			{filepath.Join(base, "reference", "blocked.txt"), "deny"},
			{filepath.Join(base, "blocked.txt"), "deny"},
		} {
			rel, _ := filepath.Rel(cwd, test.path)
			for _, resource := range []string{test.path, rel} {
				if effect := permissionEffect(rules, "edit", resource); effect != test.want {
					t.Errorf("cwd=%s edit=%s got=%s want=%s", cwd, resource, effect, test.want)
				}
			}
		}
		for _, command := range []string{"echo safe", "sleep 1", "sleep 1 > ../marker", "python -c 'write()'", "node script.js", "git status", "sh -c 'echo marker > ../marker'"} {
			if permissionEffect(rules, "shell", command) != "deny" {
				t.Fatal("a command acquired unsandboxed host authority")
			}
		}
	}
}

func TestReadOnlyAndUnconfiguredRunsHaveOnlyHostSuppliedIPCException(t *testing.T) {
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	ipc := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "run-exact")
	skill := permissionFixtureDirectory(t, cwd, "bound-skill")
	client := testClient(t)
	client.writableRoots = []string{cwd}
	client.WithReadOnlySkillDirs(skill)
	for _, instanceReadOnly := range []bool{false, true} {
		client.readOnly = instanceReadOnly
		rules := client.permissionRules(provider.RunRequest{CWD: cwd, ReadOnly: !instanceReadOnly, ToolRequestDir: ipc})
		for resource, want := range map[string]string{
			"normal.txt": "deny", ".teamva/tool-requests/run-other/request.json": "deny",
			".teamva/tool-requests/run-exact/request.json": "allow", "bound-skill/SKILL.md": "deny",
		} {
			if effect := permissionEffect(rules, "edit", resource); effect != want {
				t.Fatalf("resource=%s effect=%s want=%s", resource, effect, want)
			}
		}
		if permissionEffect(rules, "read", filepath.Join(skill, "SKILL.md")) != "allow" ||
			permissionEffect(rules, "external_directory", skill+"/*") != "allow" {
			t.Fatal("bound Skills must stay readable")
		}
		if permissionEffect(rules, "fixture_write_business_data", "*") != "deny" {
			t.Fatal("read-only Run retained a previously loaded custom MCP capability")
		}
	}
	client.readOnly = false
	client.writableRoots = nil
	rules := client.permissionRules(provider.RunRequest{CWD: cwd})
	if permissionEffect(rules, "edit", "normal.txt") != "deny" ||
		permissionEffect(rules, "edit", ".teamva/tool-requests/guessed/request.json") != "deny" {
		t.Fatal("CWD or guessed IPC path granted implicit write access")
	}
}

func TestFactoryCanonicalizesCopiesAndValidatesRoots(t *testing.T) {
	isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	root := permissionFixtureDirectory(t, base, "repo")
	link := filepath.Join(base, "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	roots := []string{root, link, root}
	adapter, err := newInstance(provider.InstanceOptions{ConfigDir: t.TempDir(), Config: provider.InstanceConfig{WritableRepositories: roots, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	roots[0] = base
	client := adapter.(*Client)
	if !client.readOnly || !slices.Equal(client.writableRoots, []string{root}) {
		t.Fatal("factory did not retain an immutable canonical authorization snapshot")
	}
	for _, bad := range []string{"relative/repo", string(filepath.Separator), filepath.Join(base, "missing"), permissionFixtureDirectory(t, base, "literal*glob"), permissionFixtureDirectory(t, base, "literal?glob")} {
		if _, err := writablePermissionRoots([]string{bad}); err == nil {
			t.Fatal("unrepresentable or unsafe permission root was accepted")
		}
	}
}

func TestComputerUseDoesNotBroadenNativeFilePermissions(t *testing.T) {
	isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	root := permissionFixtureDirectory(t, base, "repo")
	for _, enabled := range []bool{false, true} {
		adapter, err := newInstance(provider.InstanceOptions{ConfigDir: t.TempDir(), Config: provider.InstanceConfig{
			WritableRepositories: []string{root}, ComputerUse: enabled,
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer adapter.Close()
		rules := adapter.(*Client).permissionRules(provider.RunRequest{CWD: root})
		for _, resource := range []string{"../unrelated.txt", filepath.Join(base, "unrelated.txt")} {
			if permissionEffect(rules, "edit", resource) != "deny" {
				t.Fatal("Computer Use widened unrelated native file writes")
			}
		}
		if permissionEffect(rules, "edit", "ok.txt") != "allow" || permissionEffect(rules, "shell", "echo safe") != "deny" {
			t.Fatal("Computer Use altered the native authorization snapshot")
		}
	}
}

func TestPermissionGuardVerificationFailsClosed(t *testing.T) {
	for _, state := range []string{"active", "failed", "missing"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/plugin" || r.URL.Query().Get("location[directory]") != "/fixture" {
					t.Error("guard verification used the wrong V2 location contract")
				}
				id := permissionGuardID
				if state == "missing" {
					id = "unrelated-plugin"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": id, "state": map[string]string{"status": state}}}})
			}))
			defer server.Close()
			client := testClient(t)
			client.configDir, client.baseURL = "/managed-fixture", server.URL
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := client.verifyPermissionGuard(ctx, "/fixture"); (err == nil) != (state == "active") {
				t.Fatal("a missing/failed native guard must prevent model dispatch")
			}
		})
	}
}
