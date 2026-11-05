package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// These are synthetic fixtures, never real credentials. The manager boundary
// returns a redacted View; only Save receives the private input directly.
const mcpHTTPFixtureSecret = "mcp-http-private-test-fixture"

type mcpHTTPCall struct {
	action, scope, project, name string
	version                      int64
}

type stubMCP struct {
	calls   []mcpHTTPCall
	input   mcpmanager.Input
	failure error
	nilList bool
}

var _ MCPManager = (*stubMCP)(nil)

func mcpHTTPView() mcpmanager.View {
	return mcpmanager.View{
		Input: mcpmanager.Input{
			Name: "fixture", Scope: "user", Version: 7,
			Config: json.RawMessage(`{"type":"remote","url":"https://example.test/mcp","headers":{"Authorization":"__TEAMVA_KEEP_SECRET__"},"oauth":{"client_secret":"__TEAMVA_KEEP_SECRET__"}}`),
		},
		Status: "disconnected",
	}
}

func (s *stubMCP) List(_ context.Context, project string) ([]mcpmanager.View, error) {
	s.calls = append(s.calls, mcpHTTPCall{action: "list", project: project})
	if s.nilList {
		return nil, s.failure
	}
	return []mcpmanager.View{mcpHTTPView()}, s.failure
}

func (s *stubMCP) Save(_ context.Context, input mcpmanager.Input) (mcpmanager.View, error) {
	s.input = input
	s.calls = append(s.calls, mcpHTTPCall{action: "save", scope: input.Scope, project: input.ProjectID, name: input.Name, version: input.Version})
	if input.Version < 0 {
		return mcpmanager.View{}, mcpmanager.ErrInvalid
	}
	return mcpHTTPView(), s.failure
}

func (s *stubMCP) Delete(_ context.Context, scope, project, name string, version int64) error {
	s.calls = append(s.calls, mcpHTTPCall{action: "delete", scope: scope, project: project, name: name, version: version})
	return s.failure
}

func (s *stubMCP) view(action, scope, project, name string) (mcpmanager.View, error) {
	s.calls = append(s.calls, mcpHTTPCall{action: action, scope: scope, project: project, name: name})
	return mcpHTTPView(), s.failure
}

func (s *stubMCP) Connect(_ context.Context, scope, project, name string) (mcpmanager.View, error) {
	return s.view("connect", scope, project, name)
}

func (s *stubMCP) Disconnect(_ context.Context, scope, project, name string) (mcpmanager.View, error) {
	return s.view("disconnect", scope, project, name)
}

func (s *stubMCP) StartAuth(_ context.Context, scope, project, name string) (mcpmanager.AuthResult, error) {
	s.calls = append(s.calls, mcpHTTPCall{action: "auth", scope: scope, project: project, name: name})
	var result mcpmanager.AuthResult
	// Keep this stub independent of optional internal AuthResult fields.
	_ = json.Unmarshal([]byte(`{"authorization_url":"https://example.test/authorize","status":"pending"}`), &result)
	return result, s.failure
}

func (s *stubMCP) RevokeAuth(_ context.Context, scope, project, name string) (mcpmanager.View, error) {
	return s.view("revoke", scope, project, name)
}

func mcpHTTPSetup(t *testing.T) (*testRepository, *gin.Engine, *stubMCP, map[string]string, domain.Project) {
	t.Helper()
	ginSetTestMode(t)
	repo := newTestRepository(t)
	manager := &stubMCP{}
	router := newRouterWithOptions(repo, nil, "test", true, Options{MCP: manager, ExternalAccess: true})
	headers := testLoginHeaders(t, repo, router)
	project, err := repo.CreateProject(t.Context(), testLocalUserID(repo, t), domain.Project{
		ComputerID: computerID(t, repo), Name: "MCP HTTP ownership fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo, router, manager, headers, project
}

func mcpHTTPInput(version int64) map[string]any {
	return map[string]any{
		"name": "fixture", "scope": "user", "version": version,
		"config": map[string]any{
			"type": "remote", "url": "https://example.test/mcp",
			"headers": map[string]string{"Authorization": mcpHTTPFixtureSecret},
			"oauth":   map[string]string{"client_secret": mcpHTTPFixtureSecret},
		},
	}
}

func assertMCPSafeResponse(t *testing.T, response testResponse) {
	t.Helper()
	if response.Recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("MCP response must not be cached")
	}
	if strings.Contains(response.Recorder.Body.String(), mcpHTTPFixtureSecret) {
		t.Fatal("MCP response reflected a private fixture value")
	}
}

func TestMCPHTTPAllRoutesRequireLocalOwnerSessionAndCSRF(t *testing.T) {
	repo, router, manager, auth, _ := mcpHTTPSetup(t)
	other := createTestIdentity(t, repo, "mcp-non-owner@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	for _, operation := range []struct {
		method, path, action string
		body                 any
	}{
		{http.MethodGet, "/v1/mcp/servers", "list", nil},
		{http.MethodPost, "/v1/mcp/servers", "save", mcpHTTPInput(0)},
		{http.MethodDelete, "/v1/mcp/servers/fixture?scope=user&version=7", "delete", nil},
		{http.MethodPost, "/v1/mcp/servers/fixture/connect", "connect", map[string]string{"scope": "user"}},
		{http.MethodPost, "/v1/mcp/servers/fixture/disconnect", "disconnect", map[string]string{"scope": "user"}},
		{http.MethodPost, "/v1/mcp/servers/fixture/auth", "auth", map[string]string{"scope": "user"}},
		{http.MethodPost, "/v1/mcp/servers/fixture/revoke", "revoke", map[string]string{"scope": "user"}},
	} {
		t.Run(operation.action, func(t *testing.T) {
			before := len(manager.calls)
			localRequest(t, router, operation.method, operation.path, operation.body, nil, http.StatusUnauthorized)
			localRequest(t, router, operation.method, operation.path, operation.body, otherAuth, http.StatusForbidden)
			if operation.method != http.MethodGet {
				localRequest(t, router, operation.method, operation.path, operation.body, map[string]string{"Cookie": auth["Cookie"], "Origin": testBrowserOrigin}, http.StatusForbidden)
				payload, _ := json.Marshal(operation.body)
				foreign := httptest.NewRequest(operation.method, operation.path, bytes.NewReader(payload))
				foreign.RemoteAddr = "127.0.0.1:45678"
				for key, value := range auth {
					foreign.Header.Set(key, value)
				}
				foreign.Header.Set("Content-Type", "application/json")
				foreign.Header.Set("Origin", "https://attacker.test")
				denied := httptest.NewRecorder()
				router.ServeHTTP(denied, foreign)
				// CORS can reject before the JSON error middleware runs.
				if denied.Code != http.StatusForbidden {
					t.Fatal("foreign origin was not rejected")
				}
			}
			for _, marked := range []bool{false, true} {
				payload, _ := json.Marshal(operation.body)
				req := httptest.NewRequest(operation.method, operation.path, bytes.NewReader(payload))
				req.RemoteAddr = "192.168.50.20:45678"
				if marked {
					req.RemoteAddr = "127.0.0.1:45678"
					req = MarkRemoteAccess(req)
				}
				// Neither an apparent localhost Host nor spoofed proxy headers
				// can turn a LAN socket into the privileged local entry point.
				req.Host = "127.0.0.1"
				for key, value := range auth {
					req.Header.Set(key, value)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Forwarded-For", "127.0.0.1")
				req.Header.Set("X-Real-IP", "127.0.0.1")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("non-local MCP operation returned %d", recorder.Code)
				}
			}
			if len(manager.calls) != before {
				t.Fatal("rejected request reached MCP manager")
			}
			response := localRequest(t, router, operation.method, operation.path, operation.body, auth, http.StatusOK)
			assertMCPSafeResponse(t, response)
			if len(manager.calls) != before+1 || manager.calls[before].action != operation.action {
				t.Fatal("registered route did not invoke its exact MCP operation")
			}
			if operation.action == "delete" && !decodeData[struct {
				Deleted bool `json:"deleted"`
			}](t, response).Deleted {
				t.Fatal("delete envelope did not confirm deletion")
			}
			if operation.action == "auth" {
				result := decodeData[struct {
					URL    string `json:"authorization_url"`
					Status string `json:"status"`
				}](t, response)
				if result.URL != "https://example.test/authorize" || result.Status != "pending" {
					t.Fatal("auth route did not return authorization URL and status")
				}
			} else if operation.action != "delete" && !strings.Contains(response.Recorder.Body.String(), mcpmanager.KeepSecret) {
				t.Fatal("MCP View did not preserve redacted config sentinel")
			}
		})
	}
	if !strings.Contains(string(manager.input.Config), mcpHTTPFixtureSecret) {
		t.Fatal("private config was not relayed directly to manager")
	}
}

func TestMCPHTTPProjectOwnershipScopeAndVersionValidation(t *testing.T) {
	repo, router, manager, auth, project := mcpHTTPSetup(t)
	other := createTestIdentity(t, repo, "mcp-project-owner@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token}
	computer := createTestComputer(t, repo, otherAuth, "MCP foreign computer")
	foreign, err := repo.CreateProject(t.Context(), other.userID, domain.Project{ComputerID: computer.ID, Name: "MCP foreign project"})
	if err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{foreign.ID, "proj_missing"} {
		before := len(manager.calls)
		localRequest(t, router, http.MethodGet, "/v1/mcp/servers?project_id="+projectID, nil, auth, http.StatusNotFound)
		input := mcpHTTPInput(0)
		input["scope"], input["project_id"] = "project", projectID
		localRequest(t, router, http.MethodPost, "/v1/mcp/servers", input, auth, http.StatusNotFound)
		localRequest(t, router, http.MethodDelete, "/v1/mcp/servers/fixture?scope=project&version=7&project_id="+projectID, nil, auth, http.StatusNotFound)
		for _, action := range []string{"connect", "disconnect", "auth", "revoke"} {
			localRequest(t, router, http.MethodPost, "/v1/mcp/servers/fixture/"+action, map[string]string{"scope": "project", "project_id": projectID}, auth, http.StatusNotFound)
		}
		if len(manager.calls) != before {
			t.Fatal("missing or foreign project reached MCP manager")
		}
	}
	for _, target := range []map[string]string{
		{}, {"scope": "global"}, {"scope": "project"}, {"scope": "user", "project_id": project.ID},
	} {
		before := len(manager.calls)
		input := mcpHTTPInput(0)
		delete(input, "scope")
		for key, value := range target {
			input[key] = value
		}
		localRequest(t, router, http.MethodPost, "/v1/mcp/servers", input, auth, http.StatusBadRequest)
		localRequest(t, router, http.MethodDelete, "/v1/mcp/servers/fixture?version=7&scope="+target["scope"]+"&project_id="+target["project_id"], nil, auth, http.StatusBadRequest)
		for _, action := range []string{"connect", "disconnect", "auth", "revoke"} {
			localRequest(t, router, http.MethodPost, "/v1/mcp/servers/fixture/"+action, target, auth, http.StatusBadRequest)
		}
		if len(manager.calls) != before {
			t.Fatal("invalid scope reached MCP manager")
		}
	}
	for _, version := range []string{"", "0", "-1", "abc", "1.5", "9223372036854775808"} {
		before := len(manager.calls)
		localRequest(t, router, http.MethodDelete, "/v1/mcp/servers/fixture?scope=user&version="+version, nil, auth, http.StatusBadRequest)
		if len(manager.calls) != before {
			t.Fatal("invalid deletion version reached MCP manager")
		}
	}
	localRequest(t, router, http.MethodPost, "/v1/mcp/servers", mcpHTTPInput(-1), auth, http.StatusBadRequest)
	localRequest(t, router, http.MethodGet, "/v1/mcp/servers?project_id="+project.ID, nil, auth, http.StatusOK)
	if manager.calls[len(manager.calls)-1].project != project.ID {
		t.Fatal("list discarded authorized project scope")
	}
	input := mcpHTTPInput(7)
	input["scope"], input["project_id"] = "project", project.ID
	localRequest(t, router, http.MethodPost, "/v1/mcp/servers", input, auth, http.StatusOK)
	if manager.input.ProjectID != project.ID || manager.input.Scope != "project" || manager.input.Version != 7 {
		t.Fatal("save discarded scope or optimistic version")
	}
	for _, action := range []string{"connect", "disconnect", "auth", "revoke"} {
		localRequest(t, router, http.MethodPost, "/v1/mcp/servers/fixture/"+action, map[string]string{"scope": "project", "project_id": project.ID}, auth, http.StatusOK)
		call := manager.calls[len(manager.calls)-1]
		if call.scope != "project" || call.project != project.ID || call.name != "fixture" {
			t.Fatal("action discarded target identity")
		}
	}
}

func TestMCPHTTPErrorsAreStableAndNeverReflectManagerOutput(t *testing.T) {
	_, router, manager, auth, _ := mcpHTTPSetup(t)
	for _, failure := range []struct {
		err    error
		status int
		code   string
	}{
		{mcpmanager.ErrNotFound, 404, "not_found"},
		{mcpmanager.ErrConflict, 409, "version_conflict"},
		{mcpmanager.ErrInvalid, 400, "mcp_invalid"},
		{errors.New("subprocess output"), 503, "mcp_unavailable"},
	} {
		manager.failure = fmt.Errorf("%w: %s", failure.err, mcpHTTPFixtureSecret)
		for _, operation := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/v1/mcp/servers", nil},
			{http.MethodPost, "/v1/mcp/servers", mcpHTTPInput(0)},
			{http.MethodPost, "/v1/mcp/servers", mcpHTTPInput(7)},
			{http.MethodDelete, "/v1/mcp/servers/fixture?scope=user&version=7", nil},
			{http.MethodPost, "/v1/mcp/servers/fixture/connect", map[string]string{"scope": "user"}},
			{http.MethodPost, "/v1/mcp/servers/fixture/disconnect", map[string]string{"scope": "user"}},
			{http.MethodPost, "/v1/mcp/servers/fixture/auth", map[string]string{"scope": "user"}},
			{http.MethodPost, "/v1/mcp/servers/fixture/revoke", map[string]string{"scope": "user"}},
		} {
			response := localRequest(t, router, operation.method, operation.path, operation.body, auth, failure.status)
			assertMCPSafeResponse(t, response)
			if response.Error == nil || response.Error.Code != failure.code {
				t.Fatal("MCP error mapping changed")
			}
		}
	}
	manager.failure, manager.nilList = nil, true
	response := localRequest(t, router, http.MethodGet, "/v1/mcp/servers", nil, auth, http.StatusOK)
	items := decodeData[struct {
		Items []json.RawMessage `json:"items"`
	}](t, response)
	if items.Items == nil || len(items.Items) != 0 {
		t.Fatal("empty MCP list must serialize as [] rather than null")
	}
}
