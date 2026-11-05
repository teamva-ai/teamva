package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestControlledWorkspaceToolsFailClosed(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{})
	config, _ := c.config()
	scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.close()
	if _, err = scope.call(t.Context(), "write_file", map[string]any{"path": "file.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	if value, err := scope.call(t.Context(), "read_file", map[string]any{"path": "file.txt"}); err != nil || value != "hello" {
		t.Fatalf("read %q %v", value, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	_ = os.Symlink(outside, filepath.Join(cwd, "outside-link"))
	_ = os.Link(outside, filepath.Join(cwd, "outside-hardlink"))
	_ = os.MkdirAll(filepath.Join(cwd, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(cwd, ".git", "config"), []byte("git"), 0o600)
	_ = os.Symlink(filepath.Join(cwd, ".git"), filepath.Join(cwd, "alias-git"))
	for _, path := range []string{"../escape", outside, "outside-link", "outside-hardlink", ".git/config", "alias-git/config", ".env", ".teamva/other/file", filepath.Join(c.configDir, "instance.json"), filepath.Join(c.dataDir, "claude", "credentials.json")} {
		if _, err := scope.call(t.Context(), "read_file", map[string]any{"path": path}); err == nil {
			t.Errorf("read permitted %s", path)
		}
		if path != "outside-hardlink" {
			if _, err := scope.call(t.Context(), "write_file", map[string]any{"path": path, "content": "bad"}); err == nil {
				t.Errorf("write permitted %s", path)
			}
		}
	}
	// Atomic replacement of a hardlink cannot write the outside inode.
	if _, err := scope.call(t.Context(), "write_file", map[string]any{"path": "outside-hardlink", "content": "replacement"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "secret" {
		t.Fatal("outside hardlink inode changed")
	}
	if _, err := scope.call(t.Context(), "Bash", map[string]any{"command": "env"}); err == nil {
		t.Fatal("shell tool permitted")
	}
	config.WritableRepositories = nil
	noWrite, _ := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	defer noWrite.close()
	if _, err := noWrite.call(t.Context(), "write_file", map[string]any{"path": "file.txt", "content": "bad"}); err == nil {
		t.Fatal("CWD acquired implicit write authority")
	}
}

func TestReadOnlyAndDisableToolsRemoveAuthority(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{})
	config, _ := c.config()
	ipc := t.TempDir()
	for _, name := range []string{"issue_comment_create", "issue_get", "not_a_host_tool"} {
		_ = os.Mkdir(filepath.Join(ipc, name), 0o700)
	}
	for _, flags := range []struct{ readonly, disabled bool }{{true, false}, {false, true}, {true, true}} {
		scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd, ReadOnly: flags.readonly, DisableTools: flags.disabled, ToolRequestDir: ipc})
		if err != nil {
			t.Fatal(err)
		}
		if scope.permits("write_file") || scope.permits("issue_comment_create") || scope.permits("not_a_host_tool") {
			t.Fatal("write/unknown tool survived restricted scope")
		}
		if flags.disabled && (scope.permits("read_file") || scope.permits("issue_get") || len(scope.tools()) != 0 || scope.ipcRoot != nil) {
			t.Fatal("disabled tools retained authority")
		}
		if !flags.disabled && !scope.permits("issue_get") {
			t.Fatal("read-only host tool missing")
		}
		scope.close()
	}
}

func TestCredentialProjectionAndConfigIsolation(t *testing.T) {
	c, _ := fakeClient(t, provider.InstanceConfig{})
	t.Setenv("NODE_OPTIONS", "--require /never.js")
	t.Setenv("ANTHROPIC_API_KEY", "inherited-secret-must-not-win")
	t.Setenv("CLAUDE_CODE_CUSTOM", "unknown")
	t.Setenv("XDG_CONFIG_HOME", "/unknown")
	account, err := accountEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(c.environment(account), "\n")
	for _, forbidden := range []string{"NODE_OPTIONS=", "EVIL=", "inherited-secret-must-not-win", "CLAUDE_CODE_CUSTOM=", "XDG_CONFIG_HOME=", "ANTHROPIC_AUTH_TOKEN="} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("forbidden env projected: %s", forbidden)
		}
	}
	if !strings.Contains(env, "ANTHROPIC_API_KEY="+fakeSecret) {
		t.Fatal("bare API credential mapping missing")
	}
	data, err := os.ReadFile(filepath.Join(c.configDir, "instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), fakeSecret) || strings.Contains(string(data), "apiKeyHelper") {
		t.Fatal("account configuration leaked into instance config")
	}
	for _, config := range []provider.InstanceConfig{{ComputerUse: true}, {MCPServers: map[string]json.RawMessage{"unknown": json.RawMessage(`{"command":"env"}`)}}} {
		if err := WriteConfig(t.TempDir(), config); err == nil {
			t.Fatal("unsupported execution capability accepted")
		}
	}
	redact := newRedactor(account)
	encoded, _ := json.Marshal(redact.value(map[string]any{"message": "useful 401 " + fakeSecret + " api_key=sk-fake-secret-canary", "authorization": "arbitrary-secret"}))
	if strings.Contains(string(encoded), fakeSecret) || strings.Contains(string(encoded), "sk-fake-secret-canary") || strings.Contains(string(encoded), "arbitrary-secret") || !strings.Contains(string(encoded), "useful 401") {
		t.Fatalf("redaction failed: %s", encoded)
	}
	for _, text := range []string{"authorization=Bearer unknown-canary-token", `{"authorization":"Bearer unknown-canary-token"}`, "Bearer unknown-canary-token"} {
		if strings.Contains(redact.text(text), "unknown-canary-token") {
			t.Fatal("unknown Bearer/header credential survived redaction")
		}
	}
}

func TestAutoCompactWindowIsAnExplicitBoundedTokenCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Inherited options, including otherwise known names, never bypass the
	// private settings whitelist or become a test-only product env escape hatch.
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "100000")
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "99")
	t.Setenv("API_TIMEOUT_MS", "1")
	c := &Client{dataDir: t.TempDir()}
	for _, test := range []struct {
		value string
		valid bool
	}{
		{"100000", true}, {"786432", true}, {"1000000", true},
		{"", false}, {"99999", false}, {"1000001", false}, {"0", false}, {"-100000", false},
		{"+100000", false}, {"0100000", false}, {" 100000", false}, {"100000 ", false},
		{"100000.0", false}, {"100k", false}, {"1M", false}, {"1e6", false},
		{"NaN", false}, {"Infinity", false}, {"18446744073709551616", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]any{"env": map[string]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW": test.value, "CLAUDE_CODE_MAX_RETRIES": "0", "API_TIMEOUT_MS": "1", "EVIL": "ignored"}})
			if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			account, err := accountEnvironment()
			if !test.valid {
				if err == nil {
					t.Fatalf("accepted invalid compact token count %q", test.value)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if account["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != test.value {
				t.Fatal("known compact window was not projected")
			}
			env := strings.Join(c.environment(account), "\n")
			if !strings.Contains(env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW="+test.value) {
				t.Fatal("validated compact window absent from child")
			}
			for _, name := range []string{"CLAUDE_CODE_MAX_RETRIES=", "API_TIMEOUT_MS=", "EVIL="} {
				if strings.Contains(env, name) {
					t.Fatalf("unknown test/user env projected: %s", name)
				}
			}
		})
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"CLAUDE_CODE_AUTO_COMPACT_WINDOW":100000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := accountEnvironment(); err == nil {
		t.Fatal("non-string numeric env value accepted")
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	account, err := accountEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(c.environment(account), "\n"), "CLAUDE_CODE_AUTO_COMPACT_WINDOW=") {
		t.Fatal("inherited compact value bypassed explicit settings")
	}
}
