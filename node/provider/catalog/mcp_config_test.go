package catalog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/codex"
	"github.com/teamva-ai/teamva/node/provider/opencode"
)

func mcpFixture() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		`local."quoted`:     json.RawMessage(`{"type":"local","command":["server","a\u0007\u000b\n\"\\b"],"environment":{"PRIVATE":"test-only-secret"},"cwd":"/private/work","disabled":true,"protocol":"auto","codemode":false,"timeout":{"startup":1500,"execution":2500,"catalog":3000}}`),
		"remote":            json.RawMessage(`{"type":"remote","url":"https://mcp.example.test/mcp","headers":{"Authorization":"Bearer test-only-secret"},"oauth":{"client_id":"test-client","client_secret":"test-only-secret","scope":"read write","callback_port":19876,"redirect_uri":"http://127.0.0.1:19876/callback","auth_server_metadata_url":"https://auth.example.test/metadata"}}`),
		"open-computer-use": json.RawMessage(`{"type":"local","command":["untrusted-override"]}`),
	}
}

func TestManagedOpenCodeMCPUsesV2AndNeverInheritsUserServers(t *testing.T) {
	global := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", global)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(global, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "opencode", "opencode.json"), []byte(`{"mcp":{"servers":{"ambient":{"type":"local","command":["ambient"]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, readOnly := range []bool{false, true} {
		config := provider.InstanceConfig{MCPServers: mcpFixture(), ComputerUse: true, ReadOnly: readOnly}
		if err := opencode.WriteConfig(dir, config); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			MCP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "ambient") || strings.Contains(string(raw), "untrusted-override") {
			t.Fatal("uncontrolled/reserved MCP configuration leaked")
		}
		want := 3
		if readOnly {
			want = 1 // Preserve only explicit ComputerUse's pre-existing behavior.
		}
		if len(parsed.MCP.Servers) != want {
			t.Fatalf("readOnly=%v got %d MCP servers", readOnly, len(parsed.MCP.Servers))
		}
		if !readOnly {
			for name, expected := range config.MCPServers {
				if name == "open-computer-use" {
					continue
				}
				var got, want any
				_ = json.Unmarshal(parsed.MCP.Servers[name], &got)
				_ = json.Unmarshal(expected, &want)
				if !reflect.DeepEqual(got, want) {
					t.Fatal("V2 managed MCP entry was changed")
				}
			}
		}
	}
	// Full rewrite removes servers deleted from effective configuration.
	if err := opencode.WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
	if strings.Contains(string(raw), `"mcp"`) {
		t.Fatal("deleted managed MCP persisted")
	}
}

func TestManagedCodexMCPTranslatesOnlyNativeFieldsAndRespectsReadOnly(t *testing.T) {
	dir := t.TempDir()
	config := provider.InstanceConfig{MCPServers: mcpFixture(), ComputerUse: true, SystemPrompt: "keep instructions"}
	if err := codex.WriteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	text := string(raw)
	for _, expected := range []string{
		`[mcp_servers."local.\"quoted"]`, `command = "server"`, `args = ["a\u0007\u000b\n\"\\b"]`,
		`cwd = "/private/work"`, `enabled = false`, `startup_timeout_sec = 1.5`, `tool_timeout_sec = 2.5`,
		`"PRIVATE" = "test-only-secret"`, `[mcp_servers."remote".http_headers]`,
		`url = "https://mcp.example.test/mcp"`, `"Authorization" = "Bearer test-only-secret"`,
		`[mcp_servers."remote".oauth]`, `client_id = "test-client"`, `client_secret = "test-only-secret"`,
		`scopes = ["read", "write"]`, `callback_port = 19876`, `callback_url = "http://127.0.0.1:19876/callback"`,
		`[mcp_servers."open-computer-use"]`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("native Codex field missing: %s", expected)
		}
	}
	for _, unsupported := range []string{"protocol", "codemode", "catalog", "auth_server_metadata_url", "untrusted-override"} {
		if strings.Contains(text, unsupported) {
			t.Fatalf("unsupported/reserved field emitted: %s", unsupported)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "config.toml"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("MCP configuration is not private")
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(prompt), config.SystemPrompt) {
		t.Fatal("prompt lost while configuring MCP")
	}
	config.ReadOnly = true
	if err := codex.WriteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(raw), "test-only-secret") || strings.Contains(string(raw), `mcp_servers."remote"`) {
		t.Fatal("read-only Codex received custom MCP")
	}
	config.ComputerUse = false
	if err := codex.WriteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(raw), "mcp_servers") {
		t.Fatal("read-only Codex received MCP without explicit ComputerUse")
	}
}

func TestInstanceConfigMCPSecretsNeverSerialize(t *testing.T) {
	raw, err := json.Marshal(provider.InstanceConfig{MCPServers: mcpFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "MCPServers") {
		t.Fatal("MCP private configuration serialized")
	}
}
