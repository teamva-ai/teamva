package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	providerprocess "github.com/teamva-ai/teamva/node/provider/process"
)

// This contract never starts a thread/turn or logs raw CLI output. In particular,
// `mcp list/get --json` can include env/header values: keep them in memory only.
func TestNativeCodexMCPConfigWithoutModel(t *testing.T) {
	if os.Getenv("TEAMVA_CODEX_TEST") != "1" {
		t.Skip("set TEAMVA_CODEX_TEST=1 for isolated native MCP config verification (no model)")
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
	home, configDir := t.TempDir(), t.TempDir()
	// Allow-list instead of copying os.Environ: no account/provider credentials,
	// ambient CODEX_* overrides, proxies, or user configuration reach this CLI.
	env := []string{"HOME=" + home, "CODEX_HOME=" + configDir, "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_STATE_HOME=" + filepath.Join(home, "state")}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.Command(binary, args...)
		cmd.Dir, cmd.Env = home, env
		return providerprocess.CombinedOutput(ctx, cmd, 1<<20)
	}
	version, err := run("--version")
	if err != nil || !regexp.MustCompile(`^codex-cli [0-9]+\.[0-9]+\.[0-9]+\s*$`).Match(version) {
		t.Fatal("could not identify native Codex version (raw output suppressed)")
	}
	t.Logf("native MCP config contract: %s; no model/thread/turn requests", strings.TrimSpace(string(version)))
	// Native schemas must be checked against this version, not only rolling docs:
	// https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/config.schema.json
	local := managedMCPEntry{Type: "local", Command: []string{"node", "fixture with spaces.mjs", "quoted \"argument\"\n"},
		Environment: map[string]string{"FIXTURE_PRIVATE": "test-only-private-value"}, CWD: home, Disabled: true}
	local.Timeout.Startup, local.Timeout.Execution = 1500, 2500
	remote := managedMCPEntry{Type: "remote", URL: "http://127.0.0.1:1/offline-mcp", Headers: map[string]string{"X-Fixture-Private": "test-only-private-value"}, Disabled: true,
		OAuth: json.RawMessage(`{"client_id":"fixture-client","client_secret":"test-only-private-value","scope":"read write","callback_port":19876,"redirect_uri":"http://127.0.0.1:19876/callback"}`)}
	localRaw, _ := json.Marshal(local)
	remoteRaw, _ := json.Marshal(remote)
	if err := WriteConfig(configDir, provider.InstanceConfig{MCPServers: map[string]json.RawMessage{"fixture-local": localRaw, "fixture-remote": remoteRaw}}); err != nil {
		t.Fatal("write native fixture config failed")
	}
	path := filepath.Join(configDir, "config.toml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Force file-only MCP auth even for a CLI that consults its credential store.
	if err := os.WriteFile(path, append([]byte("mcp_oauth_credentials_store = \"file\"\ncli_auth_credentials_store = \"ephemeral\"\n"), original...), 0o600); err != nil {
		t.Fatal(err)
	}
	original, _ = os.ReadFile(path)
	output, err := run("mcp", "list", "--json")
	if err != nil {
		t.Fatal("native MCP list rejected generated config (raw output suppressed)")
	}
	type nativeEntry struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type        string            `json:"type"`
			Command     string            `json:"command"`
			Args        []string          `json:"args"`
			Env         map[string]string `json:"env"`
			CWD         string            `json:"cwd"`
			URL         string            `json:"url"`
			HTTPHeaders map[string]string `json:"http_headers"`
		} `json:"transport"`
		StartupTimeout float64 `json:"startup_timeout_sec"`
		ToolTimeout    float64 `json:"tool_timeout_sec"`
	}
	var entries []nativeEntry
	if json.Unmarshal(output, &entries) != nil || len(entries) != 2 {
		t.Fatal("native MCP list did not return both generated servers")
	}
	for _, entry := range entries {
		if entry.Enabled {
			t.Fatal("native CLI lost disabled state")
		}
		switch entry.Name {
		case "fixture-local":
			if entry.Transport.Command != local.Command[0] || !reflect.DeepEqual(entry.Transport.Args, local.Command[1:]) ||
				!reflect.DeepEqual(entry.Transport.Env, local.Environment) || entry.Transport.CWD != local.CWD ||
				entry.StartupTimeout != 1.5 || entry.ToolTimeout != 2.5 {
				t.Fatal("native CLI did not preserve argv/env/cwd/timeouts (values suppressed)")
			}
		case "fixture-remote":
			if entry.Transport.URL != remote.URL || !reflect.DeepEqual(entry.Transport.HTTPHeaders, remote.Headers) {
				t.Fatal("native CLI did not preserve remote URL/headers (values suppressed)")
			}
		default:
			t.Fatal("native CLI inherited an uncontrolled server")
		}
	}
	// list/get intentionally do not expose OAuth settings. Verify the parser
	// recognizes every native OAuth field rather than silently ignoring it by
	// feeding a wrong type. No OAuth login, callback or remote connection occurs.
	for _, field := range []struct{ valid, invalid string }{
		{`client_id = "fixture-client"`, `client_id = true`},
		{`client_secret = "test-only-private-value"`, `client_secret = true`},
		{`callback_url = "http://127.0.0.1:19876/callback"`, `callback_url = true`},
		{`callback_port = 19876`, `callback_port = "invalid-type"`},
	} {
		changed := strings.Replace(string(original), field.valid, field.invalid, 1)
		if changed == string(original) {
			t.Fatal("expected native OAuth field missing from generated config")
		}
		if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := run("mcp", "list", "--json"); err == nil {
			t.Fatal("native OAuth parser ignored an invalid field type")
		}
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("mcp", "get", "fixture-remote", "--json"); err != nil {
		t.Fatal("native MCP get rejected valid OAuth fixture (raw output suppressed)")
	}
	t.Log("native list/get accepted local/remote transport, disabled, argv/env/cwd/headers/timeouts; OAuth field types enforced")
}
