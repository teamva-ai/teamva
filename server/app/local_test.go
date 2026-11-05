package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gofrs/flock"
)

type testEnvelope[T any] struct {
	Data T `json:"data"`
}

type testBootstrap struct {
	CSRFToken string `json:"csrf_token"`
	User      struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

func TestSPAHandlerServesAssetsAndFallback(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html":    {Data: []byte("desktop-index")},
		"assets/app.js": {Data: []byte("application")},
	}
	api := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})
	handler := newSPAHandler(frontend, api)

	fallback := httptest.NewRecorder()
	handler.ServeHTTP(fallback, httptest.NewRequest(http.MethodGet, "/issues/one", nil))
	if fallback.Code != http.StatusOK || fallback.Body.String() != "desktop-index" {
		t.Fatalf("fallback = %d %q", fallback.Code, fallback.Body.String())
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if asset.Code != http.StatusOK || asset.Header().Get("Cache-Control") == "" {
		t.Fatalf("asset = %d cache=%q", asset.Code, asset.Header().Get("Cache-Control"))
	}
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if apiResponse.Code != http.StatusTeapot {
		t.Fatalf("API status = %d", apiResponse.Code)
	}
	for _, path := range []string{"/assets/missing.js", "/assets/missing", "/downloads/desktop/missing.zip", "/favicon.ico"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("missing file %s = %d, want 404", path, response.Code)
		}
	}
}

func TestLocalBrowserSessionRequiresSameOriginAndSurvivesRefresh(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "desktop-independent")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	origin := local.Endpoint().Origin
	for _, test := range []struct {
		name, source, contentType, fetchSite, host string
	}{
		{name: "missing origin", contentType: "application/json"},
		{name: "foreign origin", source: "https://attacker.test", contentType: "application/json"},
		{name: "null origin", source: "null", contentType: "application/json"},
		{name: "form post", source: origin, contentType: "application/x-www-form-urlencoded"},
		{name: "cross-site metadata", source: origin, contentType: "application/json", fetchSite: "cross-site"},
		{name: "DNS rebinding", source: origin, contentType: "application/json", host: "attacker.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPost, origin+"/v1/auth/local-session", strings.NewReader("{}"))
			request.Header.Set("Origin", test.source)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			if test.host != "" {
				request.Host = test.host
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden || len(response.Cookies()) != 0 {
				t.Fatalf("unsafe session admission: status=%d cookies=%d", response.StatusCode, len(response.Cookies()))
			}
		})
	}
	client := newTestClient(t)
	first := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	second := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	if first.User.ID == "" || first.CSRFToken == "" || first.CSRFToken != second.CSRFToken {
		t.Fatal("browser refresh must restore the same local session")
	}
	_ = getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	// Browser admission does not consume the desktop bridge's one-time nonce.
	bootstrapDesktopSession(t, newTestClient(t), local.Endpoint(), "desktop-independent")
	// An expired or replaced cookie can be renewed without a desktop restart.
	parsed, _ := url.Parse(origin)
	client.Jar.SetCookies(parsed, []*http.Cookie{{Name: "am_session", Value: "expired", Path: "/"}})
	renewed := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	if renewed.User.ID != first.User.ID || renewed.CSRFToken == "" {
		t.Fatal("invalid local session was not renewed")
	}
	_ = getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
}

func TestLocalHostGuardProtectsPagesAndReadAPIs(t *testing.T) {
	handler := secureLocalHTTP("http://127.0.0.1:43119", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/", "/workbench", "/v1/projects", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, "http://attacker.test"+path, nil)
		request.RemoteAddr = "127.0.0.1:52345"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("rebound host accepted at %s", path)
		}
		if response.Header().Get("X-Frame-Options") != "DENY" {
			t.Error("local pages must not be frameable")
		}
	}
	// The local entry point always refuses remote peers, even with a valid Host.
	remote := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "http://192.168.50.10:43119/readyz", nil)
		request.RemoteAddr = "192.168.50.20:52345"
		request.Host = "192.168.50.10:43119"
		return request
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, remote())
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("remote peer without external access = %d, want 401", response.Code)
	}
}

func TestLocalExternalAccessRequiresOptInAndStrongToken(t *testing.T) {
	start := func(t *testing.T, allowExternal bool, token string) (*Local, error) {
		t.Helper()
		return StartLocal(t.Context(), LocalConfig{
			Address: "0.0.0.0:0", DataDir: t.TempDir(), Version: "test", DisableRuntime: true,
			AllowExternalAccess: allowExternal, AccessToken: token,
		})
	}
	if local, err := start(t, false, ""); err == nil {
		_ = local.Close(context.Background())
		t.Fatal("non-loopback bind without opt-in must be refused")
	}
	if local, err := start(t, true, "short"); err == nil {
		_ = local.Close(context.Background())
		t.Fatal("external access without a strong token must be refused")
	}
	if local, err := start(t, true, strings.Repeat("a", 7)); err == nil {
		_ = local.Close(context.Background())
		t.Fatal("token below the minimum length must be refused")
	}
}

func TestLocalhostAliasAndForwardedHeaders(t *testing.T) {
	const origin = "http://127.0.0.1:41720"
	handler := secureLocalHTTP(origin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, test := range []struct {
		name, peer, host string
		status           int
	}{
		{"local", "127.0.0.1:50000", "127.0.0.1:41720", http.StatusOK},
		{"localhost navigation", "127.0.0.1:50000", "localhost:41720", http.StatusTemporaryRedirect},
		{"remote forged local host", "192.0.2.10:50000", "127.0.0.1:41720", http.StatusUnauthorized},
		{"remote forged localhost", "192.0.2.10:50000", "localhost:41720", http.StatusUnauthorized},
		{"rebound local peer", "127.0.0.1:50000", "attacker.test:41720", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+test.host+"/settings?tab=network", nil)
			r.RemoteAddr = test.peer
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Forwarded-Host", "localhost:41720")
			r.Header.Set("Forwarded", "for=127.0.0.1;host=localhost:41720")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if test.status == http.StatusTemporaryRedirect && w.Header().Get("Location") != origin+"/settings?tab=network" {
				t.Fatal("localhost must navigate to the canonical same-port origin")
			}
		})
	}
}

func TestLocalEmptyProjectGitStreamIsNotA404(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "empty-git-stream")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	client := newTestClient(t)
	origin := local.Endpoint().Origin
	session := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	project := mutateJSON[domain.Project](t, client, origin, http.MethodPost, "/v1/projects", session.CSRFToken, map[string]any{
		"computer_id": computers.Items[0].ID, "name": "empty-git",
	})
	response, err := client.Get(origin + "/v1/projects/" + project.ID + "/git/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("empty project stream status=%d type=%s", response.StatusCode, response.Header.Get("Content-Type"))
	}
}

func TestLocalHTTPBootstrapsAndPersistsProject(t *testing.T) {
	dataDir := t.TempDir()
	first := startTestLocal(t, dataDir, "first-nonce")
	runtimeState, err := os.ReadFile(first.Endpoint().StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runtimeState), "first-nonce") || strings.Contains(string(runtimeState), "bootstrap_url") {
		t.Fatalf("runtime state persisted desktop bootstrap credentials: %s", runtimeState)
	}
	client := newTestClient(t)
	bootstrapDesktopSession(t, client, first.Endpoint(), "first-nonce")
	replayedBootstrap, err := client.Get(first.Endpoint().Origin + "/desktop/bootstrap?nonce=first-nonce")
	if err != nil {
		t.Fatal(err)
	}
	_ = replayedBootstrap.Body.Close()
	if replayedBootstrap.StatusCode != http.StatusNotFound {
		t.Fatalf("replayed bootstrap status = %d, want 404", replayedBootstrap.StatusCode)
	}
	bootstrap := getJSON[testBootstrap](t, client, first.Endpoint().Origin, "/v1/auth/bootstrap")
	if bootstrap.CSRFToken == "" || bootstrap.User.ID == "" {
		t.Fatalf("invalid auth bootstrap: %+v", bootstrap)
	}
	if bootstrap.User.Email != "local@teamva.invalid" {
		t.Fatalf("desktop used unexpected local identity: %q", bootstrap.User.Email)
	}
	capabilities := getJSON[map[string]any](t, client, first.Endpoint().Origin, "/v1/system/capabilities")
	if capabilities["mode"] != "local" {
		t.Fatalf("mode = %v, want local", capabilities["mode"])
	}
	computers := getJSON[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, client, first.Endpoint().Origin, "/v1/computers")
	if len(computers.Items) != 1 {
		t.Fatalf("computers = %d, want 1", len(computers.Items))
	}
	initialProjects := getJSON[domain.Page[domain.Project]](t, client, first.Endpoint().Origin,
		"/v1/projects?computer_id="+url.QueryEscape(computers.Items[0].ID))
	if len(initialProjects.Items) != 1 || !initialProjects.Items[0].IsDefault || initialProjects.Items[0].ID != domain.DefaultProjectID(computers.Items[0].ID) || initialProjects.Items[0].Name != "默认项目" {
		t.Fatalf("empty app default project = %+v", initialProjects.Items)
	}
	project := mutateJSON[struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}](t, client, first.Endpoint().Origin, http.MethodPost, "/v1/projects", bootstrap.CSRFToken, map[string]any{
		"computer_id": computers.Items[0].ID,
		"name":        "离线项目",
		"description": "SQLite persistence",
	})
	if project.ID == "" || project.Name != "离线项目" {
		t.Fatalf("invalid project: %+v", project)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := first.Close(shutdownContext); err != nil {
		t.Fatal(err)
	}

	second := startTestLocal(t, dataDir, "second-nonce")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = second.Close(ctx)
	})
	secondClient := newTestClient(t)
	bootstrapDesktopSession(t, secondClient, second.Endpoint(), "second-nonce")
	projects := getJSON[domain.Page[domain.Project]](t, secondClient, second.Endpoint().Origin,
		"/v1/projects?computer_id="+url.QueryEscape(computers.Items[0].ID))
	if len(projects.Items) != 2 || !projects.Items[0].IsDefault || projects.Items[0].ID != initialProjects.Items[0].ID || projects.Items[0].Name != "默认项目" || projects.Items[1].ID != project.ID || projects.Items[1].IsDefault {
		t.Fatalf("persisted projects = %+v, want %s", projects.Items, project.ID)
	}
}

func TestLocalHTTPCreatesAndControlsPersistentGoal(t *testing.T) {
	ctx := context.Background()
	local := startTestLocal(t, t.TempDir(), "goal-nonce")
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = local.Close(closeContext)
	})
	user, computer, err := local.repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := local.repository.UpsertAgentService(ctx, user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := local.repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "goal-http-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := local.repository.CreateAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: service.ID, Model: "test/model",
		Name: "goal-agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: service.ID, Model: "test/model", SystemPrompt: "test",
	}, nil); err != nil {
		t.Fatal(err)
	}
	issue, err := local.repository.CreateIssue(ctx, user.ID, domain.Issue{
		ProjectID: project.ID, Title: "persistent goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := local.repository.EnsureIssueAgentInstance(ctx, user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	client := newTestClient(t)
	bootstrapDesktopSession(t, client, local.Endpoint(), "goal-nonce")
	bootstrap := getJSON[testBootstrap](t, client, local.Endpoint().Origin, "/v1/auth/bootstrap")
	run := mutateJSONWithHeaders[domain.Run](
		t, client, local.Endpoint().Origin, http.MethodPost, "/v1/issues/"+issue.ID+"/runs",
		bootstrap.CSRFToken, map[string]string{"Idempotency-Key": "goal-http-create"}, map[string]any{
			"issue_agent_instance_id": instance.ID,
			"input":                   "完成目标并验证",
			"goal":                    true,
			"goal_token_budget":       2000,
			"expected_issue_version":  issue.Version,
		},
	)
	current := getJSON[struct {
		Goal *domain.IssueAgentGoal `json:"goal"`
	}](t, client, local.Endpoint().Origin, "/v1/agent-instances/"+instance.ID+"/goal")
	if current.Goal == nil || run.GoalID != current.Goal.ID || current.Goal.CurrentRunID != run.ID ||
		current.Goal.TokenBudget == nil || *current.Goal.TokenBudget != 2000 {
		t.Fatalf("created goal = %+v run=%+v", current.Goal, run)
	}

	paused := mutateJSON[domain.IssueAgentGoal](
		t, client, local.Endpoint().Origin, http.MethodPost, "/v1/goals/"+current.Goal.ID+"/pause",
		bootstrap.CSRFToken, map[string]any{"expected_version": current.Goal.Version},
	)
	if paused.Status != domain.GoalPaused {
		t.Fatalf("paused goal status = %q", paused.Status)
	}
	if _, err := local.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
		ID: "goal-http-finished", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "run.finished", Payload: map[string]any{"status": "cancelled"}, OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	resumed := mutateJSON[domain.IssueAgentGoal](
		t, client, local.Endpoint().Origin, http.MethodPost, "/v1/goals/"+paused.ID+"/resume",
		bootstrap.CSRFToken, map[string]any{"expected_version": paused.Version},
	)
	if resumed.Status != domain.GoalActive || resumed.Phase != domain.GoalPhaseDispatching {
		t.Fatalf("resumed goal = %+v", resumed)
	}
	cancelled := mutateJSON[domain.IssueAgentGoal](
		t, client, local.Endpoint().Origin, http.MethodPost, "/v1/goals/"+resumed.ID+"/cancel",
		bootstrap.CSRFToken, map[string]any{"expected_version": resumed.Version},
	)
	if cancelled.Status != domain.GoalCancelled {
		t.Fatalf("cancelled goal status = %q", cancelled.Status)
	}
	current = getJSON[struct {
		Goal *domain.IssueAgentGoal `json:"goal"`
	}](t, client, local.Endpoint().Origin, "/v1/agent-instances/"+instance.ID+"/goal")
	if current.Goal != nil {
		t.Fatalf("cancelled goal remained current: %+v", current.Goal)
	}
}

func TestLocalRequestLogOmitsBootstrapQuery(t *testing.T) {
	var output bytes.Buffer
	nonce := "sensitive-bootstrap-nonce"
	local, err := StartLocal(t.Context(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: t.TempDir(), BootstrapNonce: nonce,
		Version: "test", DisableRuntime: true,
		FrontendFS: fstest.MapFS{"index.html": {Data: []byte("index")}},
		Logger:     log.New(&output, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	client := newTestClient(t)
	bootstrapDesktopSession(t, client, local.Endpoint(), nonce)
	logged := output.String()
	if !strings.Contains(logged, "path=/desktop/bootstrap") || strings.Contains(logged, nonce) {
		t.Fatalf("unsafe local request log: %q", logged)
	}
}

func TestLocalDataDirectoryLockRejectsSecondProcessAndReleasesOnClose(t *testing.T) {
	dataDir := t.TempDir()
	first := startTestLocal(t, dataDir, "lock-first")

	second, err := StartLocal(t.Context(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: dataDir,
		BootstrapNonce: "lock-second", Version: "test", DisableRuntime: true,
	})
	if second != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = second.Close(ctx)
		cancel()
		t.Fatal("second local instance unexpectedly started")
	}
	if !errors.Is(err, ErrLocalAlreadyRunning) {
		t.Fatalf("second local instance error = %v, want ErrLocalAlreadyRunning", err)
	}
	response, err := http.Get(first.Endpoint().ReadyURL)
	if err != nil {
		t.Fatalf("first instance stopped responding: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first instance readiness = %d", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := first.Close(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()

	third := startTestLocal(t, dataDir, "lock-third")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = third.Close(ctx)
	})
}

func TestLocalCorruptControlDatabaseStopsBeforeSeedAndDispatcher(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "app.db")
	original := []byte("corrupt control database - preserve")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	knownTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, knownTime, knownTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := StartLocal(t.Context(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: dataDir,
		BootstrapNonce: "corrupt-startup", Version: "test",
		// Keep runtime enabled: reaching dispatch.NewLocal would create the
		// runtime directory and probe Providers. Integrity failure must happen
		// before that branch is entered.
		DisableRuntime: false,
	})
	if local != nil {
		_ = local.Close(context.Background())
		t.Fatal("corrupt local control database unexpectedly started")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "integrity") {
		t.Fatalf("startup error=%v", err)
	}
	afterPayload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPayload, original) || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("startup modified corrupt database: size=%d/%d mtime=%s/%s",
			after.Size(), before.Size(), after.ModTime(), before.ModTime())
	}
	for _, forbidden := range []string{"runtime", "runtime.json"} {
		if _, statErr := os.Lstat(filepath.Join(dataDir, forbidden)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("seed/dispatcher startup artifact %s exists: %v", forbidden, statErr)
		}
	}
}

func TestLocalCloseTimeoutKeepsDataLockUntilFinalizerCompletes(t *testing.T) {
	dataDir := t.TempDir()
	dataLock := flock.New(filepath.Join(dataDir, ".teamva.lock"))
	locked, err := dataLock.TryLock()
	if err != nil || !locked {
		t.Fatalf("acquire initial data lock: locked=%t err=%v", locked, err)
	}
	repository, err := store.OpenSQLite(t.Context(), filepath.Join(dataDir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		writer.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	requestDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()
	<-requestStarted

	local := &Local{
		repository: repository, server: server, listener: listener, dataLock: dataLock,
		endpoint: LocalEndpoint{StateFile: filepath.Join(dataDir, "runtime.json")},
		serveErr: make(chan error, 1), closeDone: make(chan struct{}),
	}
	shortContext, shortCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = local.Close(shortContext)
	shortCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short close error = %v, want deadline exceeded", err)
	}
	competitor := flock.New(filepath.Join(dataDir, ".teamva.lock"))
	competitorLocked, err := competitor.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if competitorLocked {
		_ = competitor.Close()
		t.Fatal("data lock was released while close finalizer was still active")
	}
	_ = competitor.Close()

	close(releaseRequest)
	longContext, longCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := local.Close(longContext); err != nil {
		longCancel()
		t.Fatal(err)
	}
	longCancel()
	<-requestDone
	reacquire := flock.New(filepath.Join(dataDir, ".teamva.lock"))
	reacquired, err := reacquire.TryLock()
	if err != nil || !reacquired {
		t.Fatalf("reacquire finalized lock: locked=%t err=%v", reacquired, err)
	}
	_ = reacquire.Close()
}

func startTestLocal(t *testing.T, dataDir, nonce string) *Local {
	t.Helper()
	local, err := StartLocal(context.Background(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: dataDir,
		BootstrapNonce: nonce, Version: "test", DisableRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func newTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func bootstrapDesktopSession(t *testing.T, client *http.Client, endpoint LocalEndpoint, nonce string) {
	t.Helper()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Get(endpoint.Origin + "/desktop/bootstrap?nonce=" + url.QueryEscape(nonce))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("bootstrap status = %d: %s", response.StatusCode, body)
	}
	client.CheckRedirect = nil
}

func getJSON[T any](t *testing.T, client *http.Client, origin, path string) T {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, origin+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s status = %d: %s", path, response.StatusCode, body)
	}
	var envelope testEnvelope[T]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func mutateJSON[T any](
	t *testing.T, client *http.Client, origin, method, path, csrf string, body any,
) T {
	t.Helper()
	return mutateJSONWithHeaders[T](t, client, origin, method, path, csrf, nil, body)
}

func mutateJSONWithHeaders[T any](
	t *testing.T, client *http.Client, origin, method, path, csrf string, headers map[string]string, body any,
) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, origin+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status = %d: %s", method, path, response.StatusCode, responseBody)
	}
	var envelope testEnvelope[T]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}
