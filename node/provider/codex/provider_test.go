package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func writeProviderFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryUsesDurableCodexHomeAndLinkedInstanceConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeProviderFile(t, filepath.Join(home, ".codex", "auth.json"), `{"fixture":true}`)
	configDir, dataDir, repository := t.TempDir(), t.TempDir(), t.TempDir()
	writeProviderFile(t, filepath.Join(configDir, "skills", "bound", "SKILL.md"), "bound skill")
	options := provider.InstanceOptions{ConfigDir: configDir, DataDir: dataDir, Config: provider.InstanceConfig{
		SystemPrompt: "first prompt", WritableRepositories: []string{repository}, ComputerUse: true,
	}}
	item, err := newInstance(options)
	if err != nil {
		t.Fatal(err)
	}
	defer item.Close()
	if item.(*Client).configDir != dataDir {
		t.Fatal("CODEX_HOME still points at rebuildable configuration")
	}
	for _, name := range []string{"config.toml", "AGENTS.md", "skills"} {
		linked, err := os.Readlink(filepath.Join(dataDir, name))
		if err != nil || linked != filepath.Join(configDir, name) {
			t.Fatalf("native home config %s is not bound to this instance", name)
		}
	}
	config, err := os.ReadFile(filepath.Join(dataDir, "config.toml"))
	canonicalRepository, canonicalErr := filepath.EvalSymlinks(repository)
	if err != nil || canonicalErr != nil || len(item.(*Client).writableRepositories) != 1 || item.(*Client).writableRepositories[0] != canonicalRepository || !strings.Contains(string(config), "open-computer-use") {
		t.Fatal("native config lost repository permissions or MCP configuration")
	}
	if skill, err := os.ReadFile(filepath.Join(dataDir, "skills", "bound", "SKILL.md")); err != nil || string(skill) != "bound skill" {
		t.Fatal("native home cannot read the instance Skill")
	}
	if info, err := os.Lstat(filepath.Join(dataDir, "auth.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("login must be linked into native data home without copying")
	}
	if _, err := os.Lstat(filepath.Join(configDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("credentials must not be linked into prunable config")
	}
	options.Config.SystemPrompt = "updated prompt"
	options.Config.ComputerUse = false
	if err := WriteConfig(configDir, options.Config); err != nil {
		t.Fatal(err)
	}
	if prompt, err := os.ReadFile(filepath.Join(dataDir, "AGENTS.md")); err != nil || !strings.Contains(string(prompt), "updated prompt") || strings.Contains(string(prompt), "first prompt") {
		t.Fatal("native home did not see refreshed instance instructions")
	}
	if err := WriteConfig(configDir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if prompt, err := os.ReadFile(filepath.Join(dataDir, "AGENTS.md")); err != nil || strings.Contains(string(prompt), "updated prompt") {
		t.Fatal("clearing instance instructions retained a stale native prompt")
	}
}

func TestFactoryPreservesNativeFilesAndExistingAuthAcrossConfigRecreation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeProviderFile(t, filepath.Join(home, ".codex", "auth.json"), `{"fixture":"source"}`)
	configDir, dataDir := t.TempDir(), t.TempDir()
	writeProviderFile(t, filepath.Join(dataDir, "auth.json"), `{"fixture":"native"}`)
	writeProviderFile(t, filepath.Join(dataDir, "sessions", "native.jsonl"), "native session")
	writeProviderFile(t, filepath.Join(dataDir, "state_5.sqlite"), "native database fixture")
	options := provider.InstanceOptions{ConfigDir: configDir, DataDir: dataDir, Config: provider.InstanceConfig{SystemPrompt: "first"}}
	first, err := newInstance(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(configDir); err != nil {
		t.Fatal(err)
	}
	options.Config.SystemPrompt = "recreated"
	second, err := newInstance(options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for name, expected := range map[string]string{
		"auth.json": `{"fixture":"native"}`, "sessions/native.jsonl": "native session", "state_5.sqlite": "native database fixture",
	} {
		payload, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(name)))
		if err != nil || string(payload) != expected {
			t.Fatalf("native home entry %s was overwritten or removed", name)
		}
	}
	if prompt, err := os.ReadFile(filepath.Join(dataDir, "AGENTS.md")); err != nil || !strings.Contains(string(prompt), "recreated") {
		t.Fatal("config links did not reconnect after same-key recreation")
	}
}

func TestFactoryWithoutDataDirKeepsExplicitDirectHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir := t.TempDir()
	item, err := newInstance(provider.InstanceOptions{ConfigDir: configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer item.Close()
	if item.(*Client).configDir != configDir {
		t.Fatal("direct factory without DataDir changed its explicit home")
	}
	if info, err := os.Lstat(filepath.Join(configDir, "config.toml")); err != nil || !info.Mode().IsRegular() {
		t.Fatal("direct factory lost regular native config")
	}
}

func TestFactoryRejectsUnmanagedConfigEntriesAndDataHomeSymlinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, entry := range []string{"config.toml", "AGENTS.md", "skills"} {
		t.Run(entry, func(t *testing.T) {
			configDir, dataDir := t.TempDir(), t.TempDir()
			path := filepath.Join(dataDir, entry)
			writeProviderFile(t, path, "unmanaged native entry")
			if _, err := newInstance(provider.InstanceOptions{ConfigDir: configDir, DataDir: dataDir}); err == nil {
				t.Fatal("factory overwrote unmanaged native config")
			}
			if payload, err := os.ReadFile(path); err != nil || string(payload) != "unmanaged native entry" {
				t.Fatal("unmanaged native entry changed on rejection")
			}
		})
	}
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := newInstance(provider.InstanceOptions{ConfigDir: t.TempDir(), DataDir: linked}); err == nil {
		t.Fatal("factory accepted a symlinked native data home")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("factory wrote into a symlinked native data home")
	}
}
