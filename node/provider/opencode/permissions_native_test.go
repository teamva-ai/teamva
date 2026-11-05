package opencode

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	providerprocess "github.com/teamva-ai/teamva/node/provider/process"
)

// Starts the REAL bundled V2 service with no account files, no inherited API
// credentials, and no inference requests. Only the V2 permission evaluator and
// our deny-only alias hook run; this is not model or tool-execution evidence.
func TestBundledPermissionBoundaryWithoutModel(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for native offline permission verification (no model)")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	binary, _ = filepath.Abs(binary)
	home := isolateUserHomes(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	project := permissionFixtureDirectory(t, base, "project")
	cwd := permissionFixtureDirectory(t, project, "repo")
	second := permissionFixtureDirectory(t, project, "second")
	sibling := permissionFixtureDirectory(t, project, "repo-extra")
	// A genuine outer project ensures ../sibling is an INTERNAL normalized
	// resource, so external_directory alone cannot accidentally make this pass.
	git := exec.Command("git", "init", "--quiet", project)
	if err := git.Run(); err != nil {
		t.Fatal("prepare native parent-project fixture failed")
	}
	ipc := permissionFixtureDirectory(t, cwd, ".teamva", "tool-requests", "run-exact")
	skill := permissionFixtureDirectory(t, cwd, "bound-skill")
	configDir := permissionFixtureDirectory(t, base, "config")
	client := testClient(t)
	client.executable = binary
	client.WithConfigDir(configDir).WithDataDir(permissionFixtureDirectory(t, base, "data"))
	client.WithReadOnlySkillDirs(skill)
	client.writableRoots = []string{cwd, second}
	if err := WriteConfig(configDir, provider.InstanceConfig{}); err != nil {
		t.Fatal("write native permission config failed")
	}
	// All marker/link targets are disposable fixture paths; never user data.
	marker := filepath.Join(sibling, "marker.txt")
	if err := os.WriteFile(marker, []byte("unchanged-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(cwd, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(marker, filepath.Join(cwd, "linked-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(marker, filepath.Join(cwd, "hardlinked-file")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Dir = cwd
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + configDir, "XDG_DATA_HOME=" + client.dataDir,
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"OPENCODE_PASSWORD=" + client.password, "OPENCODE_SERVER_PASSWORD=" + client.password,
		"OPENCODE_CONFIG_PROJECT_DISABLE=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_EXTERNAL_SKILLS=1", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	tree, err := providerprocess.Start(cmd)
	if err != nil {
		t.Fatal("start native permission server failed")
	}
	defer func() {
		_ = stdout.Close()
		if err := tree.Close(); err != nil {
			t.Error("native permission server cleanup failed")
		}
	}()
	ready := make(chan serverAddressResult, 1)
	go func() { address, err := readServerAddress(stdout); ready <- serverAddressResult{address, err} }()
	select {
	case address := <-ready:
		if address.err != nil {
			t.Fatal("native permission server readiness failed")
		}
		client.baseURL = address.baseURL
	case <-ctx.Done():
		t.Fatal("native permission server readiness timed out")
	}
	if err := client.verifyPermissionGuard(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := client.getJSON(ctx, "/api/info", &info); err != nil || !strings.Contains(info.Version, "2.0.18") {
		t.Fatal("native permission test requires the pinned V2.0.18 contract")
	}
	t.Logf("native permission evaluator: %s; isolated fixture, no inference/account", info.Version)
	request := provider.RunRequest{CWD: cwd, RunID: "offline-permission", ToolRequestDir: ipc}
	var created struct {
		Data struct {
			ID          string              `json:"id"`
			Permissions []map[string]string `json:"permissions"`
		} `json:"data"`
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session", client.sessionCreateBody(request), &created); err != nil || created.Data.ID == "" {
		t.Fatal("native V2 session permissions contract rejected")
	}
	if len(created.Data.Permissions) == 0 {
		t.Fatal("native V2 session did not retain host permissions")
	}
	// A previous project-scoped "always allow edit *" approval must never
	// override the current host's configured deny. This writes only fixture
	// permission metadata; no file tool or real user approval is involved.
	var broad struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session", map[string]any{
		"title": "offline saved approval fixture", "location": map[string]string{"directory": cwd},
		"permissions": []map[string]string{{"action": "edit", "resource": "*", "effect": "ask"}},
	}, &broad); err != nil {
		t.Fatal("prepare native saved-approval fixture failed")
	}
	var pending struct {
		Data struct {
			ID     string `json:"id"`
			Effect string `json:"effect"`
		} `json:"data"`
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session/"+broad.Data.ID+"/permission", map[string]any{
		"action": "edit", "resources": []string{filepath.Join(sibling, "approval-only.txt")}, "save": []string{"*"},
	}, &pending); err != nil || pending.Data.Effect != "ask" {
		t.Fatal("prepare native pending approval failed")
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session/"+broad.Data.ID+"/permission/"+pending.Data.ID+"/reply",
		map[string]string{"decision": "always"}, nil); err != nil {
		t.Fatal("save native fixture approval failed")
	}
	evaluate := func(action string, resources []string, want string) {
		t.Helper()
		var result struct {
			Data struct {
				Effect string `json:"effect"`
			} `json:"data"`
		}
		if err := client.requestJSON(ctx, http.MethodPost, "/api/session/"+created.Data.ID+"/permission",
			map[string]any{"action": action, "resources": resources}, &result); err != nil {
			t.Fatal("native V2 permission evaluation failed (response suppressed)")
		}
		if result.Data.Effect != want {
			t.Fatalf("native action=%s resources=%v got=%s want=%s", action, resources, result.Data.Effect, want)
		}
	}
	for _, resource := range []string{"ok.txt", "nested/new.txt", "../second/ok.txt", filepath.Join(second, "ok.txt")} {
		evaluate("edit", []string{resource}, "allow")
	}
	for _, resource := range []string{"../marker.txt", "../repo-extra/marker.txt", "../../reference/new.txt", filepath.Join(sibling, "marker.txt"), "bound-skill/SKILL.md", "linked-dir/marker.txt", "linked-dir/new/nested.txt", "linked-file", "hardlinked-file"} {
		evaluate("edit", []string{resource}, "deny")
	}
	evaluate("edit", []string{"ok.txt", "../repo-extra/marker.txt"}, "deny")
	evaluate("edit", []string{".teamva/tool-requests/run-other/request.json"}, "deny")
	evaluate("edit", []string{".teamva/tool-requests/run-exact/request.json"}, "allow")
	evaluate("external_directory", []string{sibling + "/*"}, "deny")
	evaluate("external_directory", []string{second + "/*"}, "allow")
	for _, command := range []string{"echo safe", "sleep 1 > ../marker", "python -c 'open()'", "node script.js"} {
		evaluate("shell", []string{command}, "deny")
	}
	// The actual V2 PATCH API must replace, not append, all old rules on Resume.
	request.ReadOnly = true
	if err := client.requestJSON(ctx, http.MethodPatch, "/api/session/"+created.Data.ID,
		map[string]any{"permissions": client.permissionRules(request)}, nil); err != nil {
		t.Fatal("native V2 session permission replacement failed")
	}
	evaluate("edit", []string{"ok.txt"}, "deny")
	evaluate("edit", []string{"../second/ok.txt"}, "deny")
	evaluate("edit", []string{".teamva/tool-requests/run-exact/request.json"}, "allow")
	evaluate("edit", []string{".teamva/tool-requests/run-other/request.json"}, "deny")
	evaluate("read", []string{filepath.Join(skill, "SKILL.md")}, "allow")
	evaluate("fixture_write_business_data", []string{"*"}, "deny")
	request.ToolRequestDir = ""
	if err := client.requestJSON(ctx, http.MethodPatch, "/api/session/"+created.Data.ID,
		map[string]any{"permissions": client.permissionRules(request)}, nil); err != nil {
		t.Fatal("native V2 IPC permission removal failed")
	}
	evaluate("edit", []string{".teamva/tool-requests/run-exact/request.json"}, "deny")
	// API evaluation is side-effect-free, so all real fixture markers remain.
	if payload, err := os.ReadFile(marker); err != nil || string(payload) != "unchanged-fixture" {
		t.Fatal("offline permission test mutated its deny marker")
	}
}
