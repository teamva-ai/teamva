package httpapi

// These tests cross a real loopback HTTP connection and disposable SQLite.
// Provider, GUI and installation capabilities are explicit test relays; only
// local Git and the isolated MCP SDK fixtures execute real protocol work.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/embedded"
	"github.com/teamva-ai/teamva/node/mcpmanager"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/computeruse"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const systemSurfaceSecret = "system-surface-synthetic-private-value"

type systemSurfaceHTTP struct {
	server *httptest.Server
	client *http.Client
	auth   map[string]string
}

func newSystemSurfaceHTTP(t *testing.T, repo *testRepository, runtime Runtime, manager MCPManager) *systemSurfaceHTTP {
	t.Helper()
	return newSystemSurfaceHTTPRepository(t, repo, repo, runtime, manager)
}

func newSystemSurfaceHTTPRepository(t *testing.T, repo *testRepository, apiRepo store.CoreLocalRepository, runtime Runtime, manager MCPManager) *systemSurfaceHTTP {
	t.Helper()
	ginSetTestMode(t)
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	server.Config.Handler = newRouterWithOptions(apiRepo, runtime, "system-surface-test", true,
		Options{WebOrigin: origin, AllowedOrigins: []string{origin}, MCP: manager, LocalUserID: testLocalUserID(repo, t)})
	server.Start()
	t.Cleanup(server.Close)
	user, _, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repo.CreateLocalSession(t.Context(), user.ID, time.Hour, domain.SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 10 * time.Second
	return &systemSurfaceHTTP{server: server, client: client, auth: map[string]string{
		"Cookie": sessionCookieName + "=" + session.Token, "X-CSRF-Token": session.CSRF, "Origin": origin,
	}}
}

func (s *systemSurfaceHTTP) exchange(ctx context.Context, method, path string, input any, headers map[string]string) (testResponse, int, error) {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return testResponse{}, 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.server.URL+path, bytes.NewReader(payload))
	if err != nil {
		return testResponse{}, 0, err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return testResponse{}, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return testResponse{}, resp.StatusCode, err
	}
	result := testResponse{Recorder: httptest.NewRecorder()}
	result.Recorder.Header().Add("Content-Type", resp.Header.Get("Content-Type"))
	result.Recorder.Header().Add("Cache-Control", resp.Header.Get("Cache-Control"))
	result.Recorder.WriteHeader(resp.StatusCode)
	_, _ = result.Recorder.Write(raw)
	if resp.StatusCode != http.StatusNoContent && len(raw) != 0 {
		err = json.Unmarshal(raw, &result)
	}
	return result, resp.StatusCode, err
}

func (s *systemSurfaceHTTP) call(t *testing.T, method, path string, input any, expected int) testResponse {
	t.Helper()
	return s.callHeaders(t, method, path, input, s.auth, expected)
}

func (s *systemSurfaceHTTP) callHeaders(t *testing.T, method, path string, input any, headers map[string]string, expected int) testResponse {
	t.Helper()
	result, status, err := s.exchange(t.Context(), method, path, input, headers)
	if err != nil || status != expected {
		t.Fatalf("%s %s: status=%d want=%d err=%v", method, path, status, expected, err)
	}
	if expected != http.StatusNoContent && result.Meta.RequestID == "" {
		t.Fatal("missing request ID")
	}
	if strings.Contains(result.Recorder.Body.String(), systemSurfaceSecret) {
		t.Fatal("HTTP response exposed a synthetic credential")
	}
	return result
}

func systemSurfaceProject(t *testing.T, repo *testRepository, name string) domain.Project {
	t.Helper()
	item, err := repo.CreateProject(t.Context(), testLocalUserID(repo, t), domain.Project{ComputerID: computerID(t, repo), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func systemSurfaceAssertNoSQLiteSecret(t *testing.T, repo *testRepository) {
	t.Helper()
	rows, err := repo.db.QueryContext(t.Context(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, table := range tables {
		rows, err := repo.db.QueryContext(t.Context(), `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				if strings.Contains(fmt.Sprint(value), systemSurfaceSecret) {
					t.Fatalf("private fixture entered SQLite table %s", table)
				}
				if b, ok := value.([]byte); ok && bytes.Contains(b, []byte(systemSurfaceSecret)) {
					t.Fatalf("private fixture entered SQLite blob in %s", table)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
}

type systemSurfaceRelay struct {
	programmingToolRuntimeRelay
	modelSetupRelay
	mu            sync.Mutex
	commands      []string
	inputs        []json.RawMessage
	notifications atomic.Int64
	execute       func(string, any) (json.RawMessage, error)
	pathFailure   error
	providerCalls []string
	pathHook      func(context.Context, string, string) error
}

func (r *systemSurfaceRelay) NotifyIssueActivation(string) { r.notifications.Add(1) }
func (r *systemSurfaceRelay) SetProviderExecutable(ctx context.Context, provider, path string) (nodeprotocol.ProviderExecutable, error) {
	if r.pathHook != nil {
		if err := r.pathHook(ctx, provider, path); err != nil {
			return nodeprotocol.ProviderExecutable{}, err
		}
	}
	if r.pathFailure != nil {
		return nodeprotocol.ProviderExecutable{}, r.pathFailure
	}
	return r.programmingToolRuntimeRelay.SetProviderExecutable(ctx, provider, path)
}
func (r *systemSurfaceRelay) ExecuteLocalCommand(_ context.Context, name string, input any) (json.RawMessage, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.commands = append(r.commands, name)
	r.inputs = append(r.inputs, raw)
	r.mu.Unlock()
	return r.execute(name, input)
}

func (r *systemSurfaceRelay) DiagnoseProvider(_ context.Context, provider string) (nodeprotocol.ProviderDiagnostic, error) {
	r.providerCalls = append(r.providerCalls, "diagnose:"+provider)
	return nodeprotocol.ProviderDiagnostic{Provider: provider, Status: "fixture", Available: false}, nil
}

func (r *systemSurfaceRelay) RecycleIdleProvider(_ context.Context, provider string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	r.providerCalls = append(r.providerCalls, "recycle:"+provider)
	return nodeprotocol.ProviderRuntimeRecycleResult{Provider: provider, RecycledInstances: 2}, nil
}

func TestSystemSurfaceProgrammingToolsTCPAndFailureAtomicity(t *testing.T) {
	repo := newTestRepository(t)
	relay := &systemSurfaceRelay{programmingToolRuntimeRelay: programmingToolRuntimeRelay{
		items: []nodeprotocol.ProviderExecutable{{Provider: "opencode", Path: "fixture-auto", Source: "bundled"}},
	}}
	s := newSystemSurfaceHTTP(t, repo, relay, nil)
	path := "/v1/system/programming-tools"
	page := decodeData[domain.Page[ProgrammingToolExecutable]](t, s.call(t, "GET", path, nil, 200))
	if len(page.Items) != 1 || page.Items[0].Resolved != "fixture-auto" {
		t.Fatal("runtime executable projection was lost")
	}
	binary := filepath.Join(t.TempDir(), "never-execute")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	updated := decodeData[ProgrammingToolExecutable](t, s.call(t, "PUT", path+"/opencode", map[string]string{"path": "  " + binary + "  "}, 200))
	if updated.Path != binary || relay.setExecutable != binary {
		t.Fatal("normalized executable not passed to relay")
	}
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]string{"path": nonExecutable}, map[string]string{"path": "~"}, map[string]any{"path": binary, "unknown": true}, map[string]any{"path": 123}} {
		s.call(t, "PUT", path+"/opencode", body, 400)
	}
	if relay.setCalls != 1 {
		t.Fatal("invalid executable reached runtime")
	}
	relay.pathFailure = domain.Conflict("provider_busy", "fixture busy", nil)
	s.call(t, "PUT", path+"/opencode", map[string]string{"path": ""}, 409)
	stored, err := repo.ProviderExecutablePath(t.Context(), "opencode")
	if err != nil || stored != binary {
		t.Fatal("failed runtime update mutated durable setting")
	}
	relay.pathFailure = nil
	s.call(t, "PUT", path+"/opencode", map[string]string{"path": ""}, 200)
	if stored, err := repo.ProviderExecutablePath(t.Context(), "opencode"); err != nil || stored != "" {
		t.Fatal("clear was not durable")
	}
	modelPath := path + "/opencode/model-providers"
	body := map[string]string{"preset_id": "custom", "base_url": "http://127.0.0.1:1/v1", "model": "fixture-model", "api_key": systemSurfaceSecret}
	for _, op := range []struct{ method, path string }{{"GET", modelPath}, {"PUT", modelPath}, {"POST", modelPath + "/test"}} {
		var input any
		if op.method != "GET" {
			input = body
		}
		result := s.call(t, op.method, op.path, input, 200)
		if result.Recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("model settings cacheable")
		}
	}
	if relay.saved != 1 || relay.tested != 1 || relay.input.APIKey != systemSurfaceSecret {
		t.Fatal("save/test relay did not receive exact write-only input")
	}
	relay.failure = errors.New("fixture upstream failure")
	for _, op := range []struct{ method, path string }{{"GET", modelPath}, {"PUT", modelPath}, {"POST", modelPath + "/test"}} {
		var input any
		if op.method != "GET" {
			input = body
		}
		s.call(t, op.method, op.path, input, 503)
	}
	for _, action := range []string{"diagnose", "recycle-idle"} {
		path := "/v1/computers/" + computerID(t, repo) + "/programming-tools/opencode/" + action
		s.call(t, "POST", path, nil, 200)
		s.call(t, "POST", strings.Replace(path, "opencode", "unknown", 1), nil, 400)
		s.call(t, "POST", strings.Replace(path, computerID(t, repo), "cmp_missing", 1), nil, 404)
	}
	if len(relay.providerCalls) != 2 || relay.providerCalls[0] != "diagnose:opencode" || relay.providerCalls[1] != "recycle:opencode" {
		t.Fatal("provider operation did not use exact capability or rejected input reached relay")
	}
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

type systemSurfaceFailExecutableStore struct {
	store.CoreLocalRepository
	fail  bool
	write func(context.Context, string, string) error
}

func (s *systemSurfaceFailExecutableStore) SetProviderExecutablePath(ctx context.Context, provider, path string) error {
	if s.write != nil {
		return s.write(ctx, provider, path)
	}
	if s.fail {
		return errors.New("fixture durable executable setting failure")
	}
	return s.CoreLocalRepository.SetProviderExecutablePath(ctx, provider, path)
}

func TestSystemSurfaceExecutableStoreFailureRestoresPreviousRuntimePath(t *testing.T) {
	repo := newTestRepository(t)
	previous := filepath.Join(t.TempDir(), "previous-executable")
	if err := os.WriteFile(previous, []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetProviderExecutablePath(t.Context(), "opencode", previous); err != nil {
		t.Fatal(err)
	}
	failing := &systemSurfaceFailExecutableStore{CoreLocalRepository: repo, fail: true}
	relay := &systemSurfaceRelay{}
	// Replace only the repository capability. Session persistence and all other
	// operations still use this test's real SQLite, never a fake success store.
	s := newSystemSurfaceHTTPRepository(t, repo, failing, relay, nil)
	s.call(t, "PUT", "/v1/system/programming-tools/opencode", map[string]string{"path": ""}, 500)
	stored, err := repo.ProviderExecutablePath(t.Context(), "opencode")
	if err != nil || stored != previous || relay.setExecutable != previous || relay.setCalls != 2 {
		t.Fatal("durable failure did not restore the previous runtime executable")
	}
	relay.pathHook = func(ctx context.Context, _, path string) error {
		if path == previous {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("rollback was not bounded by an independent timeout")
			}
			return errors.New(systemSurfaceSecret)
		}
		return nil
	}
	incomplete := s.call(t, "PUT", "/v1/system/programming-tools/opencode", map[string]string{"path": ""}, 503)
	if incomplete.Error == nil || incomplete.Error.Code != "programming_tool_update_incomplete" ||
		!strings.Contains(incomplete.Recorder.Body.String(), `"runtime_state":"unknown"`) {
		t.Fatal("unconfirmed rollback was reported as atomic success")
	}
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

func TestSystemSurfaceExecutableFailureCannotRollbackConcurrentSuccessfulSave(t *testing.T) {
	repo := newTestRepository(t)
	previous := filepath.Join(t.TempDir(), "previous")
	next := filepath.Join(t.TempDir(), "next")
	for _, path := range []string{previous, next} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetProviderExecutablePath(t.Context(), "opencode", previous); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	failing := &systemSurfaceFailExecutableStore{CoreLocalRepository: repo}
	failing.write = func(ctx context.Context, provider, path string) error {
		if path == "" {
			close(entered)
			select {
			case <-release:
				return errors.New("fixture first save failure")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return repo.SetProviderExecutablePath(ctx, provider, path)
	}
	relay := &systemSurfaceRelay{}
	s := newSystemSurfaceHTTPRepository(t, repo, failing, relay, nil)
	type result struct {
		status int
		err    error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() {
		_, status, err := s.exchange(t.Context(), "PUT", "/v1/system/programming-tools/opencode", map[string]string{"path": ""}, s.auth)
		first <- result{status, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first save did not reach injected durable failure")
	}
	go func() {
		_, status, err := s.exchange(t.Context(), "PUT", "/v1/system/programming-tools/opencode", map[string]string{"path": next}, s.auth)
		second <- result{status, err}
	}()
	select {
	case <-second:
		t.Fatal("second setting escaped the first update's compensation fence")
	case <-time.After(30 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for _, expected := range []struct {
		ch     <-chan result
		status int
	}{{first, 500}, {second, 200}} {
		select {
		case got := <-expected.ch:
			if got.err != nil || got.status != expected.status {
				t.Fatalf("concurrent executable status=%d want=%d err=%v", got.status, expected.status, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent executable save stalled")
		}
	}
	stored, err := repo.ProviderExecutablePath(t.Context(), "opencode")
	if err != nil || stored != next || relay.setExecutable != next || relay.setCalls != 3 {
		t.Fatal("failed save compensation overwrote a newer successful setting")
	}
}

func systemSurfaceMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "system-surface-local-fixture", Version: "1"},
		&mcp.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	server.AddTool(&mcp.Tool{Name: "fixture_echo", Description: systemSurfaceSecret, InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	server.AddResource(&mcp.Resource{Name: "fixture", URI: "fixture://resource"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	server.AddResourceTemplate(&mcp.ResourceTemplate{Name: "fixture-template", URITemplate: "fixture://resource/{id}"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	server.AddPrompt(&mcp.Prompt{Name: "fixture-prompt"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	return server
}

func TestSystemSurfaceMCPFixtureProcess(t *testing.T) {
	if os.Getenv("TEAMVA_SYSTEM_SURFACE_MCP_FIXTURE") != "1" {
		return
	}
	if err := systemSurfaceMCPServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSystemSurfaceMCPRealManagerStdioScopeCASAndPersistence(t *testing.T) {
	repo := newTestRepository(t)
	dir := t.TempDir()
	manager, err := mcpmanager.New(mcpmanager.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	s := newSystemSurfaceHTTP(t, repo, nil, manager)
	project := systemSurfaceProject(t, repo, "MCP project")
	other := systemSurfaceProject(t, repo, "MCP other project")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"type": "local", "command": []string{binary, "-test.run=^TestSystemSurfaceMCPFixtureProcess$"},
		"environment": map[string]string{"TEAMVA_SYSTEM_SURFACE_MCP_FIXTURE": "1", "API_KEY": systemSurfaceSecret}}
	raw, _ := json.Marshal(config)
	user := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers", mcpmanager.Input{Name: "shared", Scope: "user", Config: raw}, 200))
	if user.Status != "disconnected" || !strings.Contains(string(user.Config), mcpmanager.KeepSecret) {
		t.Fatal("save started executable or did not redact environment")
	}
	connected := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers/shared/connect", map[string]string{"scope": "user"}, 200))
	if connected.Status != "connected" || len(connected.Tools) != 1 || len(connected.Resources) != 1 || len(connected.ResourceTemplates) != 1 || len(connected.Prompts) != 1 || connected.ProtocolVersion == "" {
		t.Fatal("stdio MCP handshake/catalog not completed")
	}
	disconnected := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers/shared/disconnect", map[string]string{"scope": "user"}, 200))
	if disconnected.Status != "disconnected" || len(disconnected.Tools) != 0 {
		t.Fatal("disconnect retained live catalog")
	}
	shadowConfig := json.RawMessage(`{"type":"local","command":["never-executed"],"disabled":true}`)
	projectEntry := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers", mcpmanager.Input{Name: "shared", Scope: "project", ProjectID: project.ID, Config: shadowConfig}, 200))
	s.call(t, "POST", "/v1/mcp/servers", mcpmanager.Input{Name: "other-only", Scope: "project", ProjectID: other.ID, Config: shadowConfig}, 200)
	list := decodeData[domain.Page[mcpmanager.View]](t, s.call(t, "GET", "/v1/mcp/servers?project_id="+project.ID, nil, 200))
	if len(list.Items) != 2 {
		t.Fatal("project list mixed unrelated project entries")
	}
	for _, item := range list.Items {
		if item.Name != "shared" || item.Shadowed != (item.Scope == "user") {
			t.Fatal("disabled project override did not shadow user entry")
		}
	}
	effective, err := manager.Effective(t.Context(), project.ID)
	if err != nil || len(effective) != 1 || !strings.Contains(string(effective["shared"]), `"disabled":true`) {
		t.Fatal("effective precedence did not follow scoped HTTP save")
	}
	// Redacted full-object round trips retain the original secret in private
	// memory, never in the control-plane DB or the HTTP projection.
	results := make(chan int, 2)
	errorsOut := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, status, err := s.exchange(t.Context(), "POST", "/v1/mcp/servers", user.Input, s.auth)
			results <- status
			errorsOut <- err
		}()
	}
	close(start)
	statuses := map[int]int{}
	for range 2 {
		statuses[<-results]++
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatalf("CAS results=%v", statuses)
	}
	list = decodeData[domain.Page[mcpmanager.View]](t, s.call(t, "GET", "/v1/mcp/servers", nil, 200))
	if len(list.Items) != 1 || list.Items[0].Version != user.Version+1 {
		t.Fatal("concurrent update lost monotonic version")
	}
	user = list.Items[0]
	effective, err = manager.Effective(t.Context(), "")
	if err != nil || !strings.Contains(string(effective["shared"]), systemSurfaceSecret) {
		t.Fatal("masked update erased private secret")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"type":"local","command":["fixture","--api-key=secret"]}`),
		json.RawMessage(`{"type":"remote","url":"https://user:pass@example.invalid"}`),
		json.RawMessage(`{"type":"local","command":["fixture"],"unknown":true}`),
	} {
		input := user.Input
		input.Config = invalid
		s.call(t, "POST", "/v1/mcp/servers", input, 400)
	}
	file := filepath.Join(dir, "mcp", "config.json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private MCP store permissions")
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	backup := file + ".fixture-backup"
	if err := os.Rename(file, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	s.call(t, "POST", "/v1/mcp/servers", user.Input, 503)
	s.call(t, "DELETE", "/v1/mcp/servers/shared?scope=user&version="+fmt.Sprint(user.Version), nil, 503)
	list = decodeData[domain.Page[mcpmanager.View]](t, s.call(t, "GET", "/v1/mcp/servers", nil, 200))
	if len(list.Items) != 1 || list.Items[0].Version != user.Version {
		t.Fatal("failed persistence changed manager memory")
	}
	after, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed persistence damaged previous private store")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, file); err != nil {
		t.Fatal(err)
	}
	s.call(t, "DELETE", "/v1/mcp/servers/shared?scope=project&project_id="+project.ID+"&version="+fmt.Sprint(projectEntry.Version), nil, 200)
	effective, err = manager.Effective(t.Context(), project.ID)
	if err != nil || !strings.Contains(string(effective["shared"]), systemSurfaceSecret) {
		t.Fatal("deleting override did not reveal user configuration")
	}
	s.call(t, "DELETE", "/v1/mcp/servers/shared?scope=user&version="+fmt.Sprint(user.Version), nil, 200)
	s.call(t, "POST", "/v1/mcp/servers/shared/connect", map[string]string{"scope": "user"}, 404)
	if err := repo.DeleteProject(t.Context(), testLocalUserID(repo, t), other.ID, other.Version); err != nil {
		t.Fatal(err)
	}
	s.call(t, "GET", "/v1/mcp/servers?project_id="+other.ID, nil, 404)
	s.call(t, "POST", "/v1/mcp/servers/other-only/connect", map[string]string{"scope": "project", "project_id": other.ID}, 404)
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

func TestSystemSurfaceMCPOAuthLocalHTTPCallbackAndRevoke(t *testing.T) {
	// Plain HTTP is intentional here: every endpoint, including the ephemeral
	// callback, is an isolated 127.0.0.1 fixture, never a public OAuth service.
	var base string
	var challenge string
	var mu sync.Mutex
	var exchanges atomic.Int64
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	sdk := systemSurfaceMCPServer()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{Stateless: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_secret") != "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_challenge") == "" {
			t.Error("invalid PKCE authorization request")
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		challenge = q.Get("code_challenge")
		mu.Unlock()
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || callback.Hostname() != "127.0.0.1" || callback.Port() == "" {
			t.Error("callback must be ephemeral loopback")
			http.Error(w, "invalid", 400)
			return
		}
		params := callback.Query()
		params.Set("code", "fixture-code")
		params.Set("state", q.Get("state"))
		callback.RawQuery = params.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		expected := challenge
		mu.Unlock()
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("client_secret") != systemSurfaceSecret || r.Form.Get("code") != "fixture-code" ||
			r.Form.Get("grant_type") != "authorization_code" || base64.RawURLEncoding.EncodeToString(hash[:]) != expected {
			t.Error("token exchange failed client/PKCE verification")
			http.Error(w, "invalid", 400)
			return
		}
		exchanges.Add(1)
		write(w, map[string]any{"access_token": systemSurfaceSecret + "-access", "token_type": "Bearer", "refresh_token": systemSurfaceSecret + "-refresh", "expires_in": 3600})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+systemSurfaceSecret+"-access" {
			w.WriteHeader(401)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	oauthServer := httptest.NewServer(mux)
	t.Cleanup(oauthServer.Close)
	base = oauthServer.URL
	repo := newTestRepository(t)
	manager, err := mcpmanager.New(mcpmanager.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	s := newSystemSurfaceHTTP(t, repo, nil, manager)
	config, _ := json.Marshal(map[string]any{"type": "remote", "url": base + "/mcp", "oauth": map[string]string{
		"client_id": "fixture-client", "client_secret": systemSurfaceSecret, "auth_server_metadata_url": base + "/metadata",
	}})
	s.call(t, "POST", "/v1/mcp/servers", mcpmanager.Input{Name: "oauth", Scope: "user", Config: config}, 200)
	auth := decodeData[mcpmanager.AuthResult](t, s.call(t, "POST", "/v1/mcp/servers/oauth/auth", map[string]string{"scope": "user"}, 200))
	if auth.Status != "auth_pending" {
		t.Fatal("OAuth did not enter pending state")
	}
	u, err := url.Parse(auth.AuthorizationURL)
	if err != nil || u.Host != strings.TrimPrefix(base, "http://") {
		t.Fatal("unexpected authorization origin")
	}
	callback := u.Query().Get("redirect_uri")
	resp, err := s.client.Get(callback + "?state=forged&code=forged")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 || exchanges.Load() != 0 {
		t.Fatal("forged state reached token exchange")
	}
	resp, err = s.client.Get(auth.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("callback failed: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := decodeData[domain.Page[mcpmanager.View]](t, s.call(t, "GET", "/v1/mcp/servers", nil, 200))
		if len(list.Items) == 1 && list.Items[0].Status == "connected" && list.Items[0].Authenticated {
			if len(list.Items[0].Tools) != 1 || exchanges.Load() != 1 {
				t.Fatal("OAuth succeeded without authenticated catalog")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("OAuth callback did not complete connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	revoked := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers/oauth/revoke", map[string]string{"scope": "user"}, 200))
	if revoked.Authenticated || revoked.Status != "disconnected" || len(revoked.Tools) != 0 {
		t.Fatal("revoke retained token/catalog")
	}
	effective, err := manager.Effective(t.Context(), "")
	if err != nil || strings.Contains(string(effective["oauth"]), systemSurfaceSecret+"-access") || strings.Contains(string(effective["oauth"]), systemSurfaceSecret+"-refresh") {
		t.Fatal("revoke retained OAuth token in effective snapshot")
	}
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(callback); err == nil {
		response.Body.Close()
		t.Fatal("OAuth callback listener was not closed")
	}
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

func TestSystemSurfaceSkillsInstallOpenAndInvalidArtifactAtomicity(t *testing.T) {
	repo := newTestRepository(t)
	pkg, err := repo.UpsertSkillPackage(t.Context(), domain.SkillPackage{Slug: "system-surface-skill", Name: "Isolated fixture", SourceURL: "https://example.invalid/never-fetched"})
	if err != nil {
		t.Fatal(err)
	}
	artifact := nodeprotocol.InstallSkillResult{Slug: pkg.Slug, CommitSHA: strings.Repeat("a", 40), ContentDigest: "sha256:fixture", FileCount: 1, SizeBytes: 12,
		Manifest: map[string]any{"files": []any{"SKILL.md"}}}
	relay := &systemSurfaceRelay{}
	mode := "ok"
	relay.execute = func(name string, input any) (json.RawMessage, error) {
		switch name {
		case "skill.install":
			payload, ok := input.(nodeprotocol.InstallSkillPayload)
			if !ok || payload.Slug != pkg.Slug || payload.SourceURL != pkg.SourceURL {
				return nil, errors.New("incorrect skill installation relay input")
			}
			switch mode {
			case "error":
				return nil, domain.Conflict("fixture_install_blocked", "fixture install blocked", nil)
			case "malformed":
				return json.RawMessage(`not-json`), nil
			case "wrong-slug":
				bad := artifact
				bad.Slug = "other"
				return json.Marshal(bad)
			case "missing-digest":
				bad := artifact
				bad.ContentDigest = ""
				return json.Marshal(bad)
			case "empty-files":
				bad := artifact
				bad.FileCount = 0
				return json.Marshal(bad)
			}
			return json.Marshal(artifact)
		case "skill.open":
			return json.RawMessage(`{"opened":true,"fixture":true}`), nil
		case "skill.uninstall":
			return json.RawMessage(`{"uninstalled":true}`), nil
		default:
			return nil, errors.New("unexpected capability")
		}
	}
	s := newSystemSurfaceHTTP(t, repo, relay, nil)
	path := "/v1/computers/" + computerID(t, repo) + "/skill-packages"
	packages := decodeData[domain.Page[domain.SkillPackage]](t, s.call(t, "GET", "/v1/skill-packages", nil, 200))
	if len(packages.Items) != 1 || packages.Items[0].ID != pkg.ID {
		t.Fatal("skill package catalog missing fixture")
	}
	for _, scenario := range []struct {
		mode   string
		status int
	}{{"error", 409}, {"malformed", 500}, {"wrong-slug", 400}, {"missing-digest", 400}, {"empty-files", 400}} {
		mode = scenario.mode
		s.call(t, "POST", path, map[string]string{"skill_package_id": pkg.ID}, scenario.status)
		installed := decodeData[domain.Page[domain.ComputerSkillPackage]](t, s.call(t, "GET", path, nil, 200))
		if len(installed.Items) != 0 {
			t.Fatal("failed install published installation")
		}
		var versions int
		if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM skill_package_versions WHERE skill_package_id = ?`, pkg.ID).Scan(&versions); err != nil || versions != 0 {
			t.Fatal("failed install published immutable version")
		}
	}
	mode = "ok"
	installed := decodeData[domain.ComputerSkillPackage](t, s.call(t, "POST", path, map[string]string{"skill_package_id": pkg.ID}, 201))
	opened := decodeData[struct {
		Opened bool `json:"opened"`
	}](t, s.call(t, "POST", path+"/"+installed.ID+"/open", nil, 200))
	if !opened.Opened || relay.commands[len(relay.commands)-1] != "skill.open" || !bytes.Contains(relay.inputs[len(relay.inputs)-1], []byte(pkg.Slug)) {
		t.Fatal("skill open did not use registered slug")
	}
	reinstalled := decodeData[domain.ComputerSkillPackage](t, s.call(t, "POST", path, map[string]string{"skill_package_id": pkg.ID}, 201))
	var payload nodeprotocol.InstallSkillPayload
	if err := json.Unmarshal(relay.inputs[len(relay.inputs)-1], &payload); err != nil || payload.Revision != artifact.CommitSHA || reinstalled.Version != installed.Version {
		t.Fatal("active reinstall was not pinned/idempotent")
	}
	before := len(relay.commands)
	s.call(t, "GET", strings.Replace(path, computerID(t, repo), "cmp_missing", 1), nil, 404)
	s.call(t, "POST", strings.Replace(path, computerID(t, repo), "cmp_missing", 1), map[string]string{"skill_package_id": pkg.ID}, 404)
	s.call(t, "POST", path, map[string]any{"skill_package_id": pkg.ID, "path": "/untrusted"}, 400)
	foreign := createTestIdentity(t, repo, "system-surface-skill-foreign@example.test")
	foreignAuth := map[string]string{"Cookie": sessionCookieName + "=" + foreign.token, "X-CSRF-Token": foreign.csrf, "Origin": s.server.URL}
	for _, operation := range []struct {
		method, path string
		body         any
	}{
		{"GET", path, nil}, {"POST", path, map[string]string{"skill_package_id": pkg.ID}},
		{"POST", path + "/" + installed.ID + "/open", nil},
		{"DELETE", path + "/" + installed.ID + "?expected_version=" + fmt.Sprint(installed.Version), nil},
	} {
		s.callHeaders(t, operation.method, operation.path, operation.body, foreignAuth, 404)
	}
	if len(relay.commands) != before {
		t.Fatal("invalid skill target reached capability relay")
	}
	s.call(t, "DELETE", path+"/"+installed.ID+"?expected_version="+fmt.Sprint(installed.Version+1), nil, 409)
	if len(relay.commands) != before {
		t.Fatal("stale uninstall reached capability relay")
	}
	s.call(t, "DELETE", path+"/"+installed.ID+"?expected_version="+fmt.Sprint(installed.Version), nil, 204)
	s.call(t, "POST", path+"/"+installed.ID+"/open", nil, 404)
}

func TestSystemSurfaceComputerUseControlledNPMAndConcurrentVersion(t *testing.T) {
	// No npm from PATH, personal NVM directory or GUI installation is reachable.
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	oldBins, oldNpm := computeruse.CommonBinDirs, computeruse.CommonNpmBins
	computeruse.CommonBinDirs, computeruse.CommonNpmBins = []string{bin}, []string{filepath.Join(bin, "npm")}
	t.Cleanup(func() { computeruse.CommonBinDirs, computeruse.CommonNpmBins = oldBins, oldNpm })
	logFile := filepath.Join(t.TempDir(), "npm-args")
	t.Setenv("TEAMVA_SYSTEM_SURFACE_NPM_ARGS", logFile)
	writeFakeExecutable(t, bin, computeruse.ServerName, "#!/bin/sh\nprintf 'fixture-version\\n'\n")
	writeFakeExecutable(t, bin, "npm", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TEAMVA_SYSTEM_SURFACE_NPM_ARGS\"\nprintf 'fixture-install-only\\n'\n")
	repo := newTestRepository(t)
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	path := "/v1/computers/" + computerID(t, repo) + "/computer-use"
	status := decodeData[computeruse.Status](t, s.call(t, "GET", path, nil, 200))
	if !status.Binary.Installed || status.Binary.Version != "fixture-version" || status.Binary.Path != filepath.Join(bin, computeruse.ServerName) {
		t.Fatal("detection escaped isolated binary fixture")
	}
	s.call(t, "POST", path, nil, 200)
	args, err := os.ReadFile(logFile)
	if err != nil || string(args) != "install\n-g\nopen-computer-use\n" {
		t.Fatal("installer did not pass exact expected npm arguments")
	}
	writeFakeExecutable(t, bin, "npm", "#!/bin/sh\nprintf 'fixture-install-failed' >&2\nexit 7\n")
	failed := s.call(t, "POST", path, nil, 422)
	if failed.Error == nil || failed.Error.Code != "computer_use_install_failed" {
		t.Fatal("npm failure did not map to actionable install error")
	}
	computer, err := repo.GetComputer(t.Context(), testLocalUserID(repo, t), computerID(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{map[string]any{"expected_version": computer.Version}, map[string]any{"enabled": true, "expected_version": 0}, map[string]any{"enabled": "false", "expected_version": computer.Version}} {
		s.call(t, "PATCH", path, bad, 400)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	errorsOut := make(chan error, 2)
	for _, value := range []bool{true, false} {
		go func(enabled bool) {
			<-start
			_, status, err := s.exchange(t.Context(), "PATCH", path, map[string]any{"enabled": enabled, "expected_version": computer.Version}, s.auth)
			results <- status
			errorsOut <- err
		}(value)
	}
	close(start)
	statuses := map[int]int{}
	for range 2 {
		statuses[<-results]++
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatalf("computer-use CAS results=%v", statuses)
	}
	after, err := repo.GetComputer(t.Context(), testLocalUserID(repo, t), computer.ID)
	if err != nil || after.Version != computer.Version+1 {
		t.Fatal("concurrent flags lost CAS version")
	}
	for _, method := range []string{"GET", "POST", "PATCH"} {
		var input any
		if method == "PATCH" {
			input = map[string]any{"enabled": true, "expected_version": after.Version}
		}
		s.call(t, method, strings.Replace(path, computer.ID, "cmp_missing", 1), input, 404)
	}
	foreign := createTestIdentity(t, repo, "system-surface-computer-use-foreign@example.test")
	foreignAuth := map[string]string{"Cookie": sessionCookieName + "=" + foreign.token, "X-CSRF-Token": foreign.csrf, "Origin": s.server.URL}
	for _, method := range []string{"GET", "POST", "PATCH"} {
		var input any
		if method == "PATCH" {
			input = map[string]any{"enabled": true, "expected_version": after.Version}
		}
		s.callHeaders(t, method, path, input, foreignAuth, 404)
	}
}

type systemSurfaceGitRuntime struct {
	*embedded.Controller
	*systemSurfaceRelay
}

func (r *systemSurfaceGitRuntime) NotifyIssueActivation(id string) {
	r.systemSurfaceRelay.NotifyIssueActivation(id)
}

func systemSurfaceController(t *testing.T, repo *testRepository) *embedded.Controller {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin := filepath.Join(t.TempDir(), "provider-never-called")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	controller, err := embedded.New(t.Context(), embedded.Config{ComputerID: computerID(t, repo), DataDir: t.TempDir(),
		ProviderPaths: map[string]string{"codex": bin, "opencode": bin}, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	return controller
}

func TestSystemSurfaceDirectoryCommandsTCPRealLocalExecutionAndPolling(t *testing.T) {
	repo := newTestRepository(t)
	controller := systemSurfaceController(t, repo)
	relay := &systemSurfaceRelay{execute: func(string, any) (json.RawMessage, error) {
		return nil, errors.New("HTTP admission must not execute filesystem commands")
	}}
	s := newSystemSurfaceHTTP(t, repo, relay, nil)
	root := filepath.Join(os.Getenv("HOME"), "directory-fixture")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	computer := computerID(t, repo)
	base := "/v1/computers/" + computer
	for _, scenario := range []struct {
		endpoint, command string
		body              any
		fail              bool
	}{
		{"directory-list", "filesystem.list", map[string]any{"path": "  " + root + "  ", "query": "", "show_hidden": true}, false},
		{"resource-register", "resource.register", map[string]string{"path": root}, false},
		{"resource-register", "resource.register", map[string]string{"path": filepath.Join(root, "missing")}, true},
	} {
		created := decodeData[struct {
			ID string `json:"command_id"`
		}](t, s.call(t, "POST", base+"/"+scenario.endpoint, scenario.body, 202))
		poll := base + "/directory-list/" + created.ID
		pending := decodeData[struct {
			Status string `json:"status"`
			Result any    `json:"result"`
		}](t, s.call(t, "GET", poll, nil, 200))
		if pending.Status != "pending" || pending.Result != nil {
			t.Fatal("new command did not persist pending before execution")
		}
		command, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
		if err != nil || !ok || command.ID != created.ID || command.Name != scenario.command {
			t.Fatalf("claim mismatch: ok=%t err=%v", ok, err)
		}
		result, executeErr := controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: command.ID, Name: command.Name, ComputerID: computer,
			SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), IdempotencyKey: command.IdempotencyKey, Payload: command.Payload,
		})
		if scenario.fail {
			if executeErr == nil && result.Status != "failed" {
				t.Fatal("missing directory registration succeeded")
			}
			if err := repo.FailLocalCommand(t.Context(), command.ID, command.ClaimToken, result.ErrorCode, result.Result); err != nil {
				t.Fatal(err)
			}
		} else {
			if executeErr != nil || result.Status != "completed" {
				t.Fatalf("local command failed: status=%s err=%v", result.Status, executeErr)
			}
			if err := repo.CompleteLocalCommand(t.Context(), command.ID, command.ClaimToken, result.Result); err != nil {
				t.Fatal(err)
			}
		}
		completed := decodeData[struct {
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		}](t, s.call(t, "GET", poll, nil, 200))
		want := "completed"
		if scenario.fail {
			want = "failed"
		}
		if completed.Status != want {
			t.Fatal("polling did not expose durable completion")
		}
		if scenario.command == "filesystem.list" && (!bytes.Contains(completed.Result, []byte("visible")) || !bytes.Contains(completed.Result, []byte(".hidden"))) {
			t.Fatal("real listing lost visible/hidden directories")
		}
		if scenario.command == "resource.register" && !scenario.fail && !bytes.Contains(completed.Result, []byte(filepath.Base(root))) {
			t.Fatal("real registration did not return canonical directory")
		}
		s.call(t, "GET", strings.Replace(poll, computer, "cmp_missing", 1), nil, 404)
	}
	before := relay.notifications.Load()
	s.call(t, "POST", base+"/resource-register", map[string]string{}, 400)
	s.call(t, "POST", base+"/directory-list", map[string]any{"show_hidden": "yes"}, 400)
	s.call(t, "POST", "/v1/computers/cmp_missing/directory-list", map[string]string{"path": root}, 404)
	if len(relay.commands) != 0 || before != 3 || relay.notifications.Load() != before {
		t.Fatal("directory admission executed synchronously or invalid input notified runtime")
	}
}

func TestSystemSurfaceGitTCPRealRepositoryDiffRefreshEventsAndOpenBoundary(t *testing.T) {
	repo := newTestRepository(t)
	controller := systemSurfaceController(t, repo)
	relay := &systemSurfaceRelay{execute: func(name string, input any) (json.RawMessage, error) {
		switch name {
		case "resource.openers":
			return json.RawMessage(`{"platform":"fixture","items":[{"id":"fixture-finder","available":true}]}`), nil
		case "resource.open":
			return json.RawMessage(`{"opened":true,"fixture":true}`), nil
		default:
			return nil, errors.New("unexpected GUI capability")
		}
	}}
	s := newSystemSurfaceHTTP(t, repo, &systemSurfaceGitRuntime{controller, relay}, nil)
	root := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("local git executable unavailable")
	}
	runGit := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), git, append([]string{"-C", root}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("local git fixture: %v: %s", err, out)
		}
	}
	runGit("init", "-q")
	file := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "fixture.txt")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	if err := os.WriteFile(file, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := systemSurfaceProject(t, repo, "Git local fixture")
	resource, err := repo.CreateResource(t.Context(), testLocalUserID(repo, t), domain.ProjectResource{ProjectID: project.ID, Name: "Fixture repository", Kind: "code_directory", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = repo.UpdateResource(t.Context(), testLocalUserID(repo, t), resource.ID, resource.Version, func(item *domain.ProjectResource) { item.Status, item.GitDetected = "ready", true })
	if err != nil {
		t.Fatal(err)
	}
	validation, err := json.Marshal(nodeprotocol.ValidateResourcePayload{ResourceID: resource.ID, Path: root, ValidationVersion: resource.Version})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "system-surface-explicit-validation", Name: "resource.validate", ComputerID: computerID(t, repo),
		SentAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), Payload: validation,
	})
	if err != nil || validated.Status != "completed" {
		t.Fatalf("explicit fixture resource validation failed: %v", err)
	}
	base := "/v1/projects/" + project.ID + "/git"
	listed := s.call(t, "GET", base+"/repositories?refresh=true", nil, 200)
	repositories := decodeData[nodeprotocol.GitRepositoryList](t, listed)
	if len(repositories.Repositories) != 1 || repositories.Repositories[0].Counts.Unstaged != 1 || strings.Contains(listed.Recorder.Body.String(), root) {
		t.Fatal("real Git status missing diff or exposing absolute path")
	}
	repositoryPath := base + "/repositories/" + repositories.Repositories[0].ID
	changes := decodeData[nodeprotocol.GitChangesPage](t, s.call(t, "GET", repositoryPath+"/changes?limit=1&area=unstaged", nil, 200))
	if len(changes.Items) != 1 || changes.Items[0].DisplayPath != "fixture.txt" || !changes.Items[0].DiffAvailable {
		t.Fatal("real Git change not returned")
	}
	diffPath := repositoryPath + "/changes/" + changes.Items[0].ID + "/diff"
	diff := decodeData[nodeprotocol.GitDiffResult](t, s.call(t, "GET", diffPath+"?context=0", nil, 200))
	if !strings.Contains(diff.Patch, "-before") || !strings.Contains(diff.Patch, "+after") {
		t.Fatal("real Git patch missing expected content")
	}
	for _, path := range []string{base + "/repositories?refresh=wrong", repositoryPath + "/changes?limit=0", repositoryPath + "/changes?limit=501", repositoryPath + "/changes?expand_untracked=wrong", diffPath + "?context=21", diffPath + "?allow_large=wrong"} {
		s.call(t, "GET", path, nil, 400)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", s.server.URL+base+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range s.auth {
		req.Header.Set(key, value)
	}
	stream, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 || !strings.Contains(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("Git event subscription failed")
	}
	if err := os.WriteFile(file, []byte("event-change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refreshed := decodeData[nodeprotocol.GitRefreshResult](t, s.call(t, "POST", base+"/refresh", nil, 200))
	if !refreshed.Refreshed {
		t.Fatal("Git refresh did not succeed")
	}
	reader := bufio.NewReader(stream.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Git event did not arrive: %v", err)
		}
		if strings.HasPrefix(line, "event:git.changed") || strings.HasPrefix(line, "event: git.changed") {
			break
		}
	}
	cancel()
	stream.Body.Close()
	openers := decodeData[struct {
		Items []json.RawMessage `json:"items"`
	}](t, s.call(t, "GET", "/v1/computers/"+computerID(t, repo)+"/resource-openers", nil, 200))
	if len(openers.Items) != 1 {
		t.Fatal("resource openers capability relay not used")
	}
	openPath := "/v1/projects/" + project.ID + "/resources/" + resource.ID + "/open"
	s.call(t, "POST", openPath, map[string]string{"opener_id": "fixture-finder"}, 200)
	var openInput map[string]string
	if err := json.Unmarshal(relay.inputs[len(relay.inputs)-1], &openInput); err != nil || openInput["path"] != root || openInput["opener_id"] != "fixture-finder" {
		t.Fatal("open relay did not use authoritative registered path")
	}
	s.call(t, "POST", openPath, map[string]string{"opener_id": "fixture-finder", "path": "/attacker"}, 400)
	otherProject := systemSurfaceProject(t, repo, "Other Git project")
	s.call(t, "POST", strings.Replace(openPath, project.ID, otherProject.ID, 1), map[string]string{"opener_id": "fixture-finder"}, 404)
	if err := repo.DeleteResource(t.Context(), testLocalUserID(repo, t), resource.ID, resource.Version); err != nil {
		t.Fatal(err)
	}
	empty := decodeData[nodeprotocol.GitRepositoryList](t, s.call(t, "GET", base+"/repositories", nil, 200))
	if len(empty.Repositories) != 0 {
		t.Fatal("deleted resource retained Git authority")
	}
	s.call(t, "GET", diffPath, nil, 404)
	s.call(t, "POST", openPath, map[string]string{"opener_id": "fixture-finder"}, 404)
}

func TestSystemSurfaceApprovalsNativeEventDecisionDeliveryAndAtomicFailure(t *testing.T) {
	repo, user, _, _, run := issueHistoryFixture(t)
	start, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
	if err != nil || !ok || start.Name != "run.start" {
		t.Fatalf("start command missing: ok=%t err=%v", ok, err)
	}
	if err := repo.CompleteLocalCommand(t.Context(), start.ID, start.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{
		{ID: "system-surface-start", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.started", OccurredAt: now},
		{ID: "system-surface-approval", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 2, Type: "approval.requested", OccurredAt: now,
			Payload: map[string]any{"request_id": "fixture-native-request", "action_kind": "shell", "normalized_action": map[string]any{"command": "fixture-only"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	page := decodeData[domain.Page[domain.Approval]](t, s.call(t, "GET", "/v1/approvals?status=pending", nil, 200))
	if len(page.Items) != 1 || page.Items[0].RunID != run.ID || page.Items[0].ProviderApprovalRef != "fixture-native-request" {
		t.Fatal("native event did not create a scoped pending approval")
	}
	approval := page.Items[0]
	path := "/v1/approvals/" + approval.ID
	got := decodeData[domain.Approval](t, s.call(t, "GET", path, nil, 200))
	if got.ActionDigest != approval.ActionDigest || got.Version != approval.Version {
		t.Fatal("approval detail lost digest or version")
	}
	decision := func(version int64, digest, value, scope string) map[string]any {
		return map[string]any{"expected_version": version, "action_digest": digest, "decision": value, "scope": scope}
	}
	for _, scenario := range []struct {
		body   any
		status int
	}{
		{decision(approval.Version, approval.ActionDigest, "maybe", "once"), 400},
		{decision(approval.Version, approval.ActionDigest, "approved", "forever"), 400},
		{decision(approval.Version+1, approval.ActionDigest, "approved", "once"), 409},
		{decision(approval.Version, "different-digest", "approved", "once"), 409},
		{map[string]any{"expected_version": approval.Version, "decision": "approved"}, 400},
	} {
		s.call(t, "POST", path+"/decisions", scenario.body, scenario.status)
	}
	foreign := createTestIdentity(t, repo, "system-surface-approval-foreign@example.test")
	foreignAuth := map[string]string{"Cookie": sessionCookieName + "=" + foreign.token, "X-CSRF-Token": foreign.csrf, "Origin": s.server.URL}
	s.callHeaders(t, "GET", path, nil, foreignAuth, 404)
	s.callHeaders(t, "POST", path+"/decisions", decision(approval.Version, approval.ActionDigest, "approved", "once"), foreignAuth, 404)
	// A transaction failure after the approval UPDATE must not publish a decision
	// without its matching durable Provider command.
	if _, err := repo.db.ExecContext(t.Context(), `CREATE TRIGGER system_surface_decision_fail BEFORE INSERT ON command_outbox
		WHEN NEW.command_name = 'approval.decide' BEGIN SELECT RAISE(ABORT, 'fixture decision failure'); END`); err != nil {
		t.Fatal(err)
	}
	s.call(t, "POST", path+"/decisions", decision(approval.Version, approval.ActionDigest, "approved", "once"), 500)
	current := decodeData[domain.Approval](t, s.call(t, "GET", path, nil, 200))
	if current.Status != "pending" || current.Version != approval.Version || current.DecidedAt != nil {
		t.Fatal("outbox failure committed partial approval decision")
	}
	if _, err := repo.db.ExecContext(t.Context(), `DROP TRIGGER system_surface_decision_fail`); err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	errorsOut := make(chan error, 2)
	startRace := make(chan struct{})
	for _, value := range []string{"approved", "denied"} {
		go func(value string) {
			<-startRace
			_, status, err := s.exchange(t.Context(), "POST", path+"/decisions", decision(approval.Version, approval.ActionDigest, value, "session"), s.auth)
			results <- status
			errorsOut <- err
		}(value)
	}
	close(startRace)
	statuses := map[int]int{}
	for range 2 {
		statuses[<-results]++
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatalf("approval CAS results=%v", statuses)
	}
	current = decodeData[domain.Approval](t, s.call(t, "GET", path, nil, 200))
	if current.Version != approval.Version+1 || current.DecisionScope != "session" || current.DecidedAt == nil {
		t.Fatal("winning approval decision missing version/scope/time")
	}
	command, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
	if err != nil || !ok || command.Name != "approval.decide" {
		t.Fatalf("native decision command missing: ok=%t err=%v", ok, err)
	}
	var payload nodeprotocol.ApprovalDecisionPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil || payload.RunID != run.ID || payload.RequestID != approval.ProviderApprovalRef || payload.Decision != current.Status || payload.Scope != "session" {
		t.Fatal("native decision payload lost authoritative request binding")
	}
	beforeDelivery, err := repo.GetRun(t.Context(), user.ID, run.ID)
	if err != nil || beforeDelivery.Status != domain.RunWaiting {
		t.Fatal("run resumed before confirmed decision delivery")
	}
	if err := repo.CompleteLocalCommand(t.Context(), command.ID, command.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	afterDelivery, err := repo.GetRun(t.Context(), user.ID, run.ID)
	if err != nil || afterDelivery.Status != domain.RunRunning {
		t.Fatal("confirmed decision did not resume run")
	}
	var commands int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox WHERE command_name = 'approval.decide'`).Scan(&commands); err != nil || commands != 1 {
		t.Fatal("rejected/duplicate decision published extra outbox command")
	}
	// Deleting a parent project must fence detail and mutation consistently with
	// the collection route, including historical terminal-run approvals.
	if err := repo.FailLocalRun(t.Context(), run.ID, "fixture-finished"); err != nil {
		t.Fatal(err)
	}
	project, err := repo.GetProject(t.Context(), user.ID, run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteProject(t.Context(), user.ID, project.ID, project.Version); err != nil {
		t.Fatal(err)
	}
	empty := decodeData[domain.Page[domain.Approval]](t, s.call(t, "GET", "/v1/approvals", nil, 200))
	if len(empty.Items) != 0 {
		t.Fatal("deleted parent retained approval collection entry")
	}
	s.call(t, "GET", path, nil, 404)
	s.call(t, "POST", path+"/decisions", decision(current.Version, current.ActionDigest, "approved", "once"), 404)
}

func TestSystemSurfaceApprovalDefaultDenialAndExpiryUseDurableCommands(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint("expired=", expired), func(t *testing.T) {
			repo, user, _, _, run := issueHistoryFixture(t)
			start, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim start: ok=%t err=%v", ok, err)
			}
			if err := repo.CompleteLocalCommand(t.Context(), start.ID, start.ClaimToken, nil); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			requested := now
			if expired {
				requested = now.Add(-11 * time.Minute)
			}
			if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{
				{ID: "fixture-start", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.started", OccurredAt: requested},
				{ID: "fixture-request", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 2, Type: "approval.requested", OccurredAt: requested,
					Payload: map[string]any{"request_id": "fixture-denial", "normalized_action": map[string]any{"command": "fixture"}}},
			}); err != nil {
				t.Fatal(err)
			}
			s := newSystemSurfaceHTTP(t, repo, nil, nil)
			all := decodeData[domain.Page[domain.Approval]](t, s.call(t, "GET", "/v1/approvals", nil, 200))
			if len(all.Items) != 1 {
				t.Fatal("approval fixture missing")
			}
			approval := all.Items[0]
			status := 200
			if expired {
				status = 409
				if approval.Status != "expired" {
					t.Fatal("listing did not durably expire native request")
				}
			}
			// Omitted scope is the public default once; no arbitrary tool or
			// Provider is ever started by this HTTP decision.
			s.call(t, "POST", "/v1/approvals/"+approval.ID+"/decisions", map[string]any{
				"expected_version": approval.Version, "action_digest": approval.ActionDigest, "decision": "denied",
			}, status)
			pending := decodeData[domain.Page[domain.Approval]](t, s.call(t, "GET", "/v1/approvals?status=pending", nil, 200))
			if len(pending.Items) != 0 {
				t.Fatal("decision/expiration left approval pending")
			}
			command, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
			if err != nil || !ok || command.Name != "approval.decide" {
				t.Fatalf("native denial command missing: ok=%t err=%v", ok, err)
			}
			var payload nodeprotocol.ApprovalDecisionPayload
			if err := json.Unmarshal(command.Payload, &payload); err != nil || payload.Decision != "denied" || payload.Scope != "once" || payload.RequestID != "fixture-denial" || payload.RunID != run.ID {
				t.Fatal("denial did not retain safe once/native request binding")
			}
			current, err := repo.GetRun(t.Context(), user.ID, run.ID)
			if err != nil || current.Status != domain.RunWaiting {
				t.Fatal("denial resumed run without delivery acknowledgement")
			}
		})
	}
}

func TestSystemSurfaceUnavailableCapabilitiesAndInvalidGUIResults(t *testing.T) {
	repo := newTestRepository(t)
	pkg, err := repo.UpsertSkillPackage(t.Context(), domain.SkillPackage{Slug: "unavailable-fixture", Name: "Unavailable fixture"})
	if err != nil {
		t.Fatal(err)
	}
	computer := computerID(t, repo)
	installation, err := repo.InstallComputerSkillPackageVersion(t.Context(), testLocalUserID(repo, t), computer, pkg.ID, domain.SkillPackageVersion{
		CommitSHA: strings.Repeat("b", 40), ContentDigest: "sha256:fixture", Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	project := systemSurfaceProject(t, repo, "Capability fixture")
	resource, err := repo.CreateResource(t.Context(), testLocalUserID(repo, t), domain.ProjectResource{
		ProjectID: project.ID, Name: "Capability root", Kind: "code_directory", Path: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = repo.UpdateResource(t.Context(), testLocalUserID(repo, t), resource.ID, resource.Version, func(item *domain.ProjectResource) { item.Status, item.GitDetected = "ready", true })
	if err != nil {
		t.Fatal(err)
	}
	computerPath := "/v1/computers/" + computer
	skillPath := computerPath + "/skill-packages"
	openPath := "/v1/projects/" + project.ID + "/resources/" + resource.ID + "/open"
	gitPath := "/v1/projects/" + project.ID + "/git"
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/system/programming-tools", nil},
		{"PUT", "/v1/system/programming-tools/opencode", map[string]string{"path": ""}},
		{"GET", "/v1/system/programming-tools/opencode/model-providers", nil},
		{"PUT", "/v1/system/programming-tools/opencode/model-providers", map[string]string{"preset_id": "deepseek", "base_url": "", "model": "fixture-model"}},
		{"POST", "/v1/system/programming-tools/opencode/model-providers/test", map[string]string{"preset_id": "deepseek", "base_url": "", "model": "fixture-model"}},
		{"GET", "/v1/mcp/servers", nil},
		{"POST", computerPath + "/programming-tools/opencode/diagnose", nil},
		{"POST", computerPath + "/programming-tools/opencode/recycle-idle", nil},
		{"GET", computerPath + "/resource-openers", nil},
		{"POST", skillPath, map[string]string{"skill_package_id": pkg.ID}},
		{"POST", skillPath + "/" + installation.ID + "/open", nil},
		{"POST", openPath, map[string]string{"opener_id": "fixture"}},
		{"GET", gitPath + "/repositories", nil},
		{"GET", gitPath + "/repositories/fixture/changes", nil},
		{"GET", gitPath + "/repositories/fixture/changes/fixture/diff", nil},
		{"POST", gitPath + "/refresh", nil},
		{"GET", gitPath + "/events", nil},
	} {
		response := s.call(t, op.method, op.path, op.body, 501)
		if response.Error == nil || response.Error.Code != "capability_unsupported" {
			t.Fatal("unsupported capability did not fail closed")
		}
	}
	mode := "malformed"
	relay := &systemSurfaceRelay{execute: func(string, any) (json.RawMessage, error) {
		if mode == "malformed" {
			return json.RawMessage(`not-json`), nil
		}
		return nil, errors.New("fixture GUI capability failure")
	}}
	s = newSystemSurfaceHTTP(t, repo, relay, nil)
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"GET", computerPath + "/resource-openers", nil},
		{"POST", skillPath + "/" + installation.ID + "/open", nil},
		{"POST", openPath, map[string]string{"opener_id": "fixture"}},
	} {
		s.call(t, op.method, op.path, op.body, 500)
	}
	mode = "failure"
	failed := s.call(t, "POST", openPath, map[string]string{"opener_id": "fixture"}, 422)
	if failed.Error == nil || failed.Error.Code != "resource_open_failed" {
		t.Fatal("GUI capability error did not map to resource-open failure")
	}
}
