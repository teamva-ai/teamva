package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func isolateUserHomes(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, entry := range []struct{ name, directory string }{
		{"XDG_CONFIG_HOME", "config"}, {"XDG_DATA_HOME", "data"},
		{"XDG_STATE_HOME", "state"}, {"XDG_CACHE_HOME", "cache"},
	} {
		t.Setenv(entry.name, filepath.Join(home, entry.directory))
	}
	return home
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read instance config: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("parse instance config: %v", err)
	}
	return parsed
}

func TestWriteConfigMergesUserProvidersAndWritesInstanceLayout(t *testing.T) {
	isolateUserHomes(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeFile(t, filepath.Join(configHome, "opencode", "opencode.json"), `{
		"provider": {"enmooy": {"name": "enmooy", "options": {"baseURL": "http://127.0.0.1:1/v1"}}},
		"permissions": {"edit": "deny"}
	}`)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, provider.InstanceConfig{SystemPrompt: "instance prompt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("config must live under opencode/ layout, found legacy path: %v", err)
	}
	cfg := readConfig(t, dir)
	if cfg["provider"] == nil {
		t.Fatal("user providers must survive the isolated instance config")
	}
	agents, _ := cfg["agents"].(map[string]any)
	build, _ := agents["build"].(map[string]any)
	if build["system"] != "instance prompt" || cfg["instructions"] != nil {
		t.Fatal("instance prompt must use the native agent.system override, not additional instructions")
	}
	paths, _ := cfg["skills"].([]any)
	if len(paths) != 0 {
		t.Fatalf("zero binding must not scan a stale skills directory, got %#v", cfg["skills"])
	}
	if _, present := cfg["permissions"]; present {
		t.Fatalf("user permissions must not leak into the instance config: %#v", cfg["permissions"])
	}
}

func TestWriteConfigIncludesWorkspaceInstructionsAndComputerUse(t *testing.T) {
	isolateUserHomes(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "AGENTS.md")
	writeFile(t, workspace, "host workspace instructions")
	if err := WriteConfig(dir, provider.InstanceConfig{
		WorkspaceInstructions: workspace, ComputerUse: true,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig(t, dir)
	plugins := cfg["plugins"].([]any)
	options := plugins[0].(map[string]any)["options"].(map[string]any)
	instructions := options["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != "host workspace instructions" {
		t.Fatal("workspace instructions missing from the native context hook")
	}
	mcp, _ := cfg["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	if _, ok := servers["open-computer-use"]; !ok {
		t.Fatalf("computer use MCP server missing: %#v", cfg["mcp"])
	}
}

func TestWriteConfigInjectsAuthAPIKeys(t *testing.T) {
	isolateUserHomes(t)
	configHome := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	writeFile(t, filepath.Join(dataHome, "opencode", "auth.json"), `{
		"deepseek": {"type": "api", "key": "sk-deepseek-test"},
		"openai": {"type": "oauth", "access": "token"}
	}`)
	writeFile(t, filepath.Join(configHome, "opencode", "opencode.json"), `{
		"provider": {"enmooy": {"name": "enmooy", "options": {"apiKey": "sk-enmooy-keep"}}}
	}`)

	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig(t, dir)
	providers, _ := cfg["providers"].(map[string]any)
	deepseek, _ := providers["deepseek"].(map[string]any)
	deepseekOptions, _ := deepseek["settings"].(map[string]any)
	if deepseekOptions["apiKey"] != "sk-deepseek-test" {
		t.Fatalf("auth api key must be injected, got %#v", providers["deepseek"])
	}
	legacy, _ := cfg["provider"].(map[string]any)
	enmooy, _ := legacy["enmooy"].(map[string]any)
	enmooyOptions, _ := enmooy["options"].(map[string]any)
	if enmooyOptions["apiKey"] != "sk-enmooy-keep" {
		t.Fatalf("existing provider key must be kept, got %#v", providers["enmooy"])
	}
	if _, present := providers["openai"]; present {
		t.Fatalf("oauth credentials must not be injected: %#v", providers["openai"])
	}
}

func TestAuthSourcePrefersDataHome(t *testing.T) {
	isolateUserHomes(t)
	configHome := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	if got := authSource(); got != "" {
		t.Fatalf("expected no auth source, got %q", got)
	}
	dataAuth := filepath.Join(dataHome, "opencode", "auth.json")
	writeFile(t, dataAuth, `{"deepseek":{"type":"api"}}`)
	if got := authSource(); got != dataAuth {
		t.Fatalf("expected data-home credentials, got %q", got)
	}
	configAuth := filepath.Join(configHome, "opencode", "auth.json")
	writeFile(t, configAuth, `{"openai":{"type":"oauth"}}`)
	if got := authSource(); got != dataAuth {
		t.Fatalf("data-home credentials must win, got %q", got)
	}
}

func TestWriteConfigAcceptsGlobalJSONCWithIsolatedHomeFallback(t *testing.T) {
	home := isolateUserHomes(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{
		// V2's native provider settings must survive unchanged.
		"providers": {"local": {
			"settings": {"baseURL": "https://example.test/a//b?q=/*literal*/",},
			"models": {"example": {"modelID": "example",},},
		},}, /* trailing block comment */
	}`)
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	providers := readConfig(t, dir)["providers"].(map[string]any)
	settings := providers["local"].(map[string]any)["settings"].(map[string]any)
	if settings["baseURL"] != "https://example.test/a//b?q=/*literal*/" {
		t.Fatal("JSONC parsing changed URL string contents")
	}
}

func TestUserConfigJSONCOverlaysJSON(t *testing.T) {
	isolateUserHomes(t)
	directory := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode")
	writeFile(t, filepath.Join(directory, "opencode.json"), `{
		"providers": {"first": {"name": "retained"}, "shared": {"name": "before", "settings": {"timeout": 10}}}
	}`)
	writeFile(t, filepath.Join(directory, "opencode.jsonc"), `{
		"providers": {"shared": {"name": "after", "settings": {"baseURL": "https://example.test"}},},
	}`)
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	providers := readConfig(t, dir)["providers"].(map[string]any)
	shared := providers["shared"].(map[string]any)
	settings := shared["settings"].(map[string]any)
	if providers["first"] == nil || shared["name"] != "after" || settings["timeout"] != float64(10) || settings["baseURL"] != "https://example.test" {
		t.Fatal("global JSON/JSONC provider overlay was lost")
	}
}

func TestWriteConfigRejectsInvalidGlobalJSONCWithoutDisclosingContents(t *testing.T) {
	isolateUserHomes(t)
	directory := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode")
	const sensitive = "private-config-value-do-not-include-in-errors"
	writeFile(t, filepath.Join(directory, "opencode.jsonc"), `{"providers":{"`+sensitive+`": {"settings":{"apiKey":"`+sensitive+`"}}}, "broken": [,]}`)
	dir := t.TempDir()
	err := WriteConfig(dir, provider.InstanceConfig{SystemPrompt: "must not write on invalid config"})
	if err == nil || !strings.Contains(err.Error(), "opencode.jsonc") || !strings.Contains(err.Error(), "JSONC") {
		t.Fatalf("expected visible JSONC configuration error, got %v", err)
	}
	if strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), directory) {
		t.Fatal("configuration diagnostic disclosed private contents or path")
	}
	for _, path := range []string{"instructions.md", filepath.Join("opencode", "opencode.json")} {
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Fatalf("configuration failure wrote partial instance file %s", path)
		}
	}
}

func TestUserConfigDoesNotSilentlyFallBackPastInvalidOrUnreadableFile(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "unreadable"}[unreadable], func(t *testing.T) {
			isolateUserHomes(t)
			directory := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode")
			writeFile(t, filepath.Join(directory, "opencode.jsonc"), `{"providers":{}}`)
			if unreadable {
				if err := os.Mkdir(filepath.Join(directory, "opencode.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, filepath.Join(directory, "opencode.json"), `{"broken":}`)
			}
			if _, err := userConfig(); err == nil {
				t.Fatal("invalid global config was silently replaced by another file")
			}
		})
	}
}

func TestFactorySeparatesNativeDataFromConfigAndKeepsDirectFallback(t *testing.T) {
	isolateUserHomes(t)
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "managed"}[external], func(t *testing.T) {
			configDir := t.TempDir()
			dataDir := filepath.Join(configDir, "data")
			options := provider.InstanceOptions{ConfigDir: configDir, Config: provider.InstanceConfig{SystemPrompt: "isolated prompt"}}
			if external {
				dataDir = t.TempDir()
				options.DataDir = dataDir
			}
			writeFile(t, filepath.Join(dataDir, "opencode", "auth.json"), `{"fixture":"native"}`)
			writeFile(t, filepath.Join(dataDir, "opencode", "opencode.db"), "native database fixture")
			item, err := newInstance(options)
			if err != nil {
				t.Fatal(err)
			}
			if client := item.(*Client); client.dataDir != dataDir || client.configDir != configDir {
				t.Fatal("OpenCode native data/config environment uses the wrong instance home")
			}
			if err := item.Close(); err != nil {
				t.Fatal(err)
			}
			if !external {
				return
			}
			if _, err := os.Stat(filepath.Join(configDir, "data")); !os.IsNotExist(err) {
				t.Fatal("managed factory still stores native data inside prunable config")
			}
			if err := os.RemoveAll(configDir); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			options.Config.SystemPrompt = "recreated prompt"
			recreated, err := newInstance(options)
			if err != nil {
				t.Fatal(err)
			}
			defer recreated.Close()
			for name, expected := range map[string]string{
				"auth.json": `{"fixture":"native"}`, "opencode.db": "native database fixture",
			} {
				payload, err := os.ReadFile(filepath.Join(dataDir, "opencode", name))
				if err != nil || string(payload) != expected {
					t.Fatalf("native OpenCode entry %s was overwritten or removed", name)
				}
			}
			agents := readConfig(t, configDir)["agents"].(map[string]any)
			if agents["build"].(map[string]any)["system"] != "recreated prompt" {
				t.Fatal("recreated OpenCode lost its instance prompt configuration")
			}
		})
	}
}

func TestFactoryDoesNotFollowSymlinkedNativeDataHome(t *testing.T) {
	isolateUserHomes(t)
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "native-data")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := newInstance(provider.InstanceOptions{ConfigDir: t.TempDir(), DataDir: linked}); err == nil {
		t.Fatal("factory accepted a symlinked native data home")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("factory wrote outside its private native data home")
	}
}
