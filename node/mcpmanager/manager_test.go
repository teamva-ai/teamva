package mcpmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func saveJSON(t *testing.T, m *Manager, scope, project, name string, c any) View {
	t.Helper()
	raw, _ := json.Marshal(c)
	v, err := m.Save(context.Background(), Input{Name: name, Scope: scope, ProjectID: project, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func localConfig(command ...string) map[string]any {
	return map[string]any{"type": "local", "command": command}
}

func TestStoreSecretsScopeAndVersion(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	c := map[string]any{"type": "remote", "url": "https://example.invalid/mcp?region=test", "headers": map[string]string{"Authorization": "Bearer fixture-secret"}, "environment": map[string]string{"API_KEY": "fixture-env"}, "oauth": map[string]string{"client_id": "public-client", "client_secret": "fixture-client-secret"}}
	user := saveJSON(t, m, "user", "", "shared", c)
	if strings.Contains(string(user.Config), "fixture-") || !strings.Contains(string(user.Config), KeepSecret) {
		t.Fatal("view exposed secrets")
	}
	project := saveJSON(t, m, "project", "p1", "shared", map[string]any{"type": "remote", "url": "https://project.invalid", "disabled": true})
	saveJSON(t, m, "project", "p2", "other", localConfig("fixture"))
	list, err := m.List(ctx, "p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	for _, v := range list {
		if (v.Scope == "user") != v.Shadowed {
			t.Fatal("same-name disabled project did not mark user as shadowed")
		}
	}
	global, _ := m.List(ctx, "")
	for _, v := range global {
		if v.Shadowed {
			t.Fatal("global scope marked shadowed")
		}
	}
	eff, err := m.Effective(ctx, "p1")
	if err != nil || len(eff) != 1 || !strings.Contains(string(eff["shared"]), "disabled") {
		t.Fatal("project override")
	}
	eff, _ = m.Effective(ctx, "")
	if !strings.Contains(string(eff["shared"]), "fixture-secret") {
		t.Fatal("private effective lost secret")
	}
	// Saving the masked full entry retains only existing keys.
	updated, err := m.Save(ctx, user.Input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == user.Version {
		t.Fatal("version unchanged")
	}
	if _, err = m.Save(ctx, user.Input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale save accepted")
	}
	if err = m.Delete(ctx, "project", "p1", "shared", project.Version+2); !errors.Is(err, ErrConflict) {
		t.Fatal("stale delete accepted")
	}
	c2 := map[string]any{"type": "remote", "url": "https://example.invalid/mcp", "headers": map[string]string{}}
	raw, _ := json.Marshal(c2)
	updated.Config = raw
	updated, err = m.Save(ctx, updated.Input)
	if err != nil {
		t.Fatal(err)
	}
	eff, _ = m.Effective(ctx, "")
	if strings.Contains(string(eff["shared"]), "fixture-secret") {
		t.Fatal("deleted key retained")
	}
	dir := m.dir
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("file permissions")
	}
	info, _ = os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
	if _, err = New(Config{DataDir: filepath.Dir(dir)}); !errors.Is(err, ErrConflict) {
		t.Fatal("second writer allowed")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: filepath.Dir(dir)})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	list, _ = reopened.List(ctx, "p1")
	if len(list) != 2 || list[0].Status == "connected" {
		t.Fatal("load executed or changed entries")
	}
}

func TestVersionMonotonicRecreateAndOverflow(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	v := saveJSON(t, m, "user", "", "fixture", localConfig("fixture"))
	initial := v.Input
	for range 100 {
		old := v.Version
		var err error
		v, err = m.Save(ctx, v.Input)
		if err != nil || v.Version != old+1 || v.Version > maxVersion {
			t.Fatal("update version not monotonic", err)
		}
	}
	if _, err := m.Save(ctx, initial); !errors.Is(err, ErrConflict) {
		t.Fatal("stale snapshot accepted")
	}
	last := v.Input
	if err := m.Delete(ctx, "user", "", "fixture", v.Version); err != nil {
		t.Fatal(err)
	}
	v = saveJSON(t, m, "user", "", "fixture", localConfig("fixture"))
	if v.Version == last.Version || v.Version == initial.Version {
		t.Fatal("delete/recreate reused creation token")
	}
	if _, err := m.Save(ctx, last); !errors.Is(err, ErrConflict) {
		t.Fatal("ABA update accepted")
	}
	m.mu.Lock()
	m.entries[key("user", "", "fixture")].Version = maxVersion
	m.mu.Unlock()
	v.Version = maxVersion
	before, _ := os.ReadFile(filepath.Join(m.dir, "config.json"))
	if _, err := m.Save(ctx, v.Input); !errors.Is(err, ErrConflict) {
		t.Fatal("overflow accepted")
	}
	after, _ := os.ReadFile(filepath.Join(m.dir, "config.json"))
	if string(before) != string(after) {
		t.Fatal("overflow mutated persisted data")
	}
	list, _ := m.List(ctx, "")
	if list[0].Version != maxVersion {
		t.Fatal("overflow mutated entry")
	}
}

func TestValidationRejectsSecretPublicFields(t *testing.T) {
	tests := []string{
		`{"type":"remote","url":"https://user:pass@example.invalid"}`,
		`{"type":"remote","url":"https://example.invalid?api_key=secret"}`,
		`{"type":"remote","url":"https://example.invalid#secret"}`,
		`{"type":"remote","url":"file:///private/secret"}`,
		`{"type":"remote","url":"https://example.invalid","headers":{"Authorization":"__TEAMVA_KEEP_SECRET__"}}`,
		`{"type":"remote","url":"https://example.invalid","headers":{"bad\r\nname":"secret"}}`,
		`{"type":"remote","url":"https://example.invalid","headers":{"X-Test":"x\r\nAuthorization: secret"}}`,
		`{"type":"local","command":["fixture","--api-key=secret"]}`,
		`{"type":"local","command":["fixture","-H","Authorization: Bearer secret"]}`,
		`{"type":"local","command":["fixture","https://user:pass@example.invalid"]}`,
		`{"type":"local","command":["fixture"],"cwd":"relative"}`,
		`{"type":"local","command":["fixture"],"environment":{"BAD=ENV":"x"}}`,
		`{"type":"local","command":["fixture"],"oauth":false}`,
		`{"type":"remote","url":"https://example.invalid","oauth":{"clientSecret":"secret"}}`,
		`{"type":"remote","url":"https://example.invalid","timeout":{"catalog":0}}`,
		`{"type":"remote","url":"https://example.invalid","unknown":"secret"}`,
	}
	m := newTestManager(t)
	for _, raw := range tests {
		_, err := m.Save(context.Background(), Input{Name: "fixture", Scope: "user", Config: json.RawMessage(raw)})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid config accepted: %s: %v", raw, err)
		}
	}
}

func TestConcurrentVersionSave(t *testing.T) {
	m := newTestManager(t)
	v := saveJSON(t, m, "user", "", "fixture", localConfig("fixture"))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.Save(context.Background(), v.Input); results <- err }()
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("lost update")
	}
}

func TestSymlinkAndCorruptStoreRejected(t *testing.T) {
	base := t.TempDir()
	dest := t.TempDir()
	if os.Symlink(dest, filepath.Join(base, "mcp")) != nil {
		t.Fatal("symlink")
	}
	if _, err := New(Config{DataDir: base}); !errors.Is(err, ErrStorage) {
		t.Fatal("symlink directory accepted")
	}
	base = t.TempDir()
	if os.Mkdir(filepath.Join(base, "mcp"), 0700) != nil {
		t.Fatal("mkdir")
	}
	target := filepath.Join(base, "secret")
	os.WriteFile(target, []byte("never overwrite"), 0600)
	os.Symlink(target, filepath.Join(base, "mcp", "config.json"))
	if _, err := New(Config{DataDir: base}); !errors.Is(err, ErrStorage) {
		t.Fatal("symlink file accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "never overwrite" {
		t.Fatal("symlink target overwritten")
	}
	base = t.TempDir()
	os.Mkdir(filepath.Join(base, "mcp"), 0700)
	file := filepath.Join(base, "mcp", "config.json")
	os.WriteFile(file, []byte(`{"format":42}`), 0600)
	if _, err := New(Config{DataDir: base}); !errors.Is(err, ErrStorage) {
		t.Fatal("corrupt store accepted")
	}
	b, _ = os.ReadFile(file)
	if string(b) != `{"format":42}` {
		t.Fatal("corrupt data replaced")
	}
}

func fixtureServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	s.AddTool(&mcp.Tool{Name: "echo", Description: "fixture-only", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	s.AddResource(&mcp.Resource{Name: "Fixture", URI: "fixture://resource", MIMEType: "text/plain"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	s.AddResourceTemplate(&mcp.ResourceTemplate{Name: "Template", URITemplate: "fixture://resource/{id}"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	s.AddPrompt(&mcp.Prompt{Name: "fixture-prompt", Description: "fixture-only"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	return s
}
func TestMCPFixtureProcess(t *testing.T) {
	if os.Getenv("MCPMANAGER_FIXTURE") != "1" {
		return
	}
	_ = fixtureServer().Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}
func TestLocalConnectAndCatalog(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t)
	cfg := localConfig(binary, "-test.run=^TestMCPFixtureProcess$")
	cfg["environment"] = map[string]string{"MCPMANAGER_FIXTURE": "1"}
	cfg["protocol"] = "auto"
	v := saveJSON(t, m, "user", "", "fixture", cfg)
	if v.Status != "disconnected" {
		t.Fatal("save executed command")
	}
	v, err = m.Connect(context.Background(), "user", "", "fixture")
	if err != nil {
		t.Fatal(err, v.Error)
	}
	assertCatalog(t, v)
	v, err = m.Disconnect(context.Background(), "user", "", "fixture")
	if err != nil || v.Status != "disconnected" || len(v.Tools) != 0 {
		t.Fatal("disconnect")
	}
}
func assertCatalog(t *testing.T, v View) {
	t.Helper()
	if v.Status != "connected" || len(v.Tools) != 1 || len(v.Resources) != 1 || len(v.ResourceTemplates) != 1 || len(v.Prompts) != 1 {
		t.Fatalf("catalog: status=%s tools=%d resources=%d templates=%d prompts=%d error=%s", v.Status, len(v.Tools), len(v.Resources), len(v.ResourceTemplates), len(v.Prompts), v.Error)
	}
}
func TestRemoteConnect2026AndLegacy(t *testing.T) {
	for _, protocol := range []string{"legacy", "auto", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			s := fixtureServer()
			ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true}))
			defer ts.Close()
			m := newTestManager(t)
			saveJSON(t, m, "user", "", "fixture", map[string]any{"type": "remote", "url": ts.URL, "protocol": protocol, "oauth": false})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v, err := m.Connect(ctx, "user", "", "fixture")
			if err != nil {
				t.Fatal(err, v.Error)
			}
			assertCatalog(t, v)
			if protocol == "2026-07-28" && v.ProtocolVersion != "2026-07-28" {
				t.Fatal("false protocol support")
			}
		})
	}
}
func TestErrorsNeverEchoTransportSecret(t *testing.T) {
	m := newTestManager(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture-sensitive-transport-error", 500) }))
	defer ts.Close()
	saveJSON(t, m, "user", "", "fixture", map[string]any{"type": "remote", "url": ts.URL, "headers": map[string]string{"Authorization": "fixture-secret"}, "oauth": false})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, err := m.Connect(ctx, "user", "", "fixture")
	b, _ := json.Marshal(v)
	if err == nil || strings.Contains(err.Error(), "fixture-") || strings.Contains(string(b), "fixture-sensitive-") {
		t.Fatal("transport error leaked")
	}
}
