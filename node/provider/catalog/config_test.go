package catalog_test

import (
	"encoding/json"
	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/codex"
	"github.com/teamva-ai/teamva/node/provider/opencode"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type InstanceConfig = provider.InstanceConfig

var writeOpenCodeConfig = opencode.WriteConfig
var writeCodexConfig = codex.WriteConfig

const computerUseServerName = "open-computer-use"
const computerUseCommand = "open-computer-use"
const computerUseMCPArgs = "mcp"

func TestProviderConfigsAuthorizeExactWritableRoots(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	instructions := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(instructions, []byte("host workspace fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := InstanceConfig{
		WritableRepositories:  []string{second, first, first},
		WorkspaceInstructions: instructions,
	}
	if err := writeOpenCodeConfig(configDir, config); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(configDir, "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var openCode map[string]any
	if err := json.Unmarshal(payload, &openCode); err != nil {
		t.Fatal(err)
	}
	// V2 rules belong to each session (also replaced on Resume), not to a
	// reusable configuration. The factory must carry the immutable roots into
	// those rules; Location alone is NOT a write boundary. Native tests in the
	// provider verify the real permission evaluator without calling a model.
	for _, key := range []string{"permission", "permissions"} {
		if _, present := openCode[key]; present {
			t.Fatal("OpenCode must not persist session permission rules in global config")
		}
	}
	if plugins, ok := openCode["plugins"].([]any); !ok || len(plugins) != 1 {
		t.Fatal("OpenCode deny-only alias guard is missing")
	}
	if openCode["update"] != "disable" {
		t.Fatal("OpenCode managed service must not update its external binary")
	}
	plugin := openCode["plugins"].([]any)[0].(map[string]any)
	instructionList := plugin["options"].(map[string]any)["instructions"].([]any)
	if len(instructionList) != 1 || instructionList[0] != "host workspace fixture" {
		t.Fatal("OpenCode workspace instructions did not reach the native context hook")
	}

	if err := writeCodexConfig(configDir, config); err != nil {
		t.Fatal(err)
	}
	codexConfig, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(codexConfig)
	if !strings.Contains(text, `default_permissions = ":read-only"`) ||
		!strings.Contains(text, `[permissions.teamva.filesystem]`) ||
		!strings.Contains(text, `[permissions.teamva.filesystem.":workspace_roots"]`) ||
		!strings.Contains(text, `"." = "write"`) ||
		!strings.Contains(text, `".git" = "write"`) ||
		!strings.Contains(text, "enabled = false") || strings.Contains(text, "sandbox_workspace_write") {
		t.Fatalf("Codex sandbox config=%s", text)
	}
	for _, root := range []string{first, second} {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(filepath.ToSlash(canonical))
		if err != nil {
			t.Fatal(err)
		}
		// Factory freezes the authorization snapshot in memory and sends the
		// complete business+Run IPC list on every native thread and turn.
		// A reusable config must not retain roots from a previous Run/scope.
		if strings.Contains(text, string(encoded)) {
			t.Fatalf("Codex reusable config retained a Run root: %s", encoded)
		}
	}
}

func TestOpenCodeAuthLinkUsesIsolatedDataTarget(t *testing.T) {
	globalConfig := filepath.Join(t.TempDir(), "global-config")
	source := filepath.Join(globalConfig, "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"token":"test-only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", globalConfig)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "global-data"))

	instanceConfig := filepath.Join(t.TempDir(), "instance-xdg")
	if err := os.MkdirAll(instanceConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(instanceConfig, "data", "opencode", "auth.json")
	item, err := opencode.Definition(provider.Metadata{}).NewInstance(provider.InstanceOptions{
		Executable: "unused-opencode", ConfigDir: instanceConfig, Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = item.Close() })
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("OpenCode auth target was not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("OpenCode auth target is not a symlink: mode=%v", info.Mode())
	}
	linked, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if linked != source {
		t.Fatalf("OpenCode auth link points to %q, want %q", linked, source)
	}
	if _, err := os.Lstat(filepath.Join(instanceConfig, "opencode", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected auth link in instance config: %v", err)
	}
}

func TestComputerUseInjectsOfficialMCPServerIntoRunConfigs(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	config := InstanceConfig{ComputerUse: true, ReadOnly: true}
	if err := writeOpenCodeConfig(configDir, config); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(configDir, "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var openCode map[string]any
	if err := json.Unmarshal(payload, &openCode); err != nil {
		t.Fatal(err)
	}
	mcp, ok := openCode["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("OpenCode mcp missing: %s", payload)
	}
	servers, ok := mcp["servers"].(map[string]any)
	if !ok {
		t.Fatalf("OpenCode V2 mcp.servers missing")
	}
	entry, ok := servers[computerUseServerName].(map[string]any)
	if !ok || entry["type"] != "local" {
		t.Fatalf("OpenCode computer use entry=%#v", servers[computerUseServerName])
	}
	command, ok := entry["command"].([]any)
	if !ok || len(command) != 2 || command[0] != computerUseCommand || command[1] != computerUseMCPArgs {
		t.Fatalf("OpenCode computer use command=%#v", entry["command"])
	}

	if err := writeCodexConfig(configDir, config); err != nil {
		t.Fatal(err)
	}
	codexConfig, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(codexConfig)
	if !strings.Contains(text, `[mcp_servers."open-computer-use"]`) ||
		!strings.Contains(text, `command = "open-computer-use"`) ||
		!strings.Contains(text, `args = ["mcp"]`) {
		t.Fatalf("Codex computer use section missing:\n%s", text)
	}

	// 未启用时不得注入。
	configDir = t.TempDir()
	if err := writeOpenCodeConfig(configDir, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	payload, err = os.ReadFile(filepath.Join(configDir, "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "open-computer-use") {
		t.Fatalf("OpenCode injected disabled computer use: %s", payload)
	}
	if err := writeCodexConfig(configDir, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	codexConfig, err = os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(codexConfig), "open-computer-use") {
		t.Fatalf("Codex injected disabled computer use: %s", codexConfig)
	}
}
