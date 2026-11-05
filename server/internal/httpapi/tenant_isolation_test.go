package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// TestTenantIsolationAcrossFullDomain exercises the IDOR contract: a second
// independent user must receive the same uniform not-found error for every
// resource owned by the first user, and must never see the first user's rows
// in any List response.
func TestTenantIsolationAcrossFullDomain(t *testing.T) {
	runTenantIsolation(t, newTestRepository(t))
}

func runTenantIsolation(t *testing.T, repository *testRepository) {
	t.Helper()
	ginSetTestMode(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})

	alice := createTestIdentity(t, repository, "alice@example.test")
	bob := createTestIdentity(t, repository, "bob@example.test")
	bearer := func(id testIdentity) map[string]string {
		return map[string]string{
			"Cookie": sessionCookieName + "=" + id.token, "X-CSRF-Token": id.csrf,
			"Origin": testBrowserOrigin,
		}
	}

	// Alice registers a computer and creates a project under it.
	aliceComputer := createTestComputer(t, repository, bearer(alice), "Alice Mac")
	aliceService, err := repository.UpsertAgentService(context.Background(), alice.userID, domain.AgentService{
		ComputerID: aliceComputer.ID, Provider: "opencode", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatalf("create Alice agent service: %v", err)
	}
	aliceAgent, err := repository.CreateAgent(context.Background(), alice.userID, domain.Agent{
		ComputerID: aliceComputer.ID, AgentServiceID: aliceService.ID,
		Model: "test/model", Name: "Alice Agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatalf("create Alice agent: %v", err)
	}
	if _, err := repository.CreateAgentRevision(context.Background(), alice.userID, domain.AgentRevision{
		AgentID: aliceAgent.ID, AgentServiceID: aliceService.ID, Model: aliceAgent.Model,
	}, nil); err != nil {
		t.Fatalf("create Alice agent revision: %v", err)
	}

	project := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": aliceComputer.ID, "name": "Alice 项目",
	}, bearer(alice), http.StatusCreated))

	resource := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/resources", map[string]any{
		"kind": "code_directory", "name": "代码", "path": "/tmp/alice-source",
	}, bearer(alice), http.StatusCreated))

	issue := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "Alice 任务",
	}, bearer(alice), http.StatusCreated))

	comment := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "Alice 评论",
	}, bearer(alice), http.StatusCreated))

	memory := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/memories", map[string]any{
		"key": "alice-memory", "kind": "fact", "title": "记忆", "content": "正文",
	}, bearer(alice), http.StatusCreated))

	policy := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/policy-rules", map[string]any{
		"action_kind": "command.run", "effect": "allow", "conditions": map[string]any{},
	}, bearer(alice), http.StatusCreated))

	scheduled := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/scheduled-tasks", map[string]any{
		"name": "Alice 计划", "schedule_kind": "cron",
		"schedule_expression": "0 10 * * *", "timezone": "Asia/Shanghai",
		"prompt":   []map[string]string{{"type": "text", "text": "任务"}},
		"agent_id": aliceAgent.ID,
	}, bearer(alice), http.StatusCreated))

	// Every IDOR probe from Bob on Alice's IDs must return 404 (uniform,
	// non-informative) and every List must be empty for Bob.
	probes := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/projects?computer_id=" + aliceComputer.ID, nil},
		{http.MethodPost, "/v1/projects", map[string]any{"computer_id": aliceComputer.ID, "name": "Bob 篡改"}},
		{http.MethodGet, "/v1/computers/" + aliceComputer.ID + "/agents", nil},
		{http.MethodGet, "/v1/agents/" + aliceAgent.ID, nil},
		{http.MethodPatch, "/v1/agents/" + aliceAgent.ID, map[string]any{"name": "篡改", "expected_version": aliceAgent.Version + 1}},
		{http.MethodDelete, "/v1/agents/" + aliceAgent.ID + "?expected_version=" + fmt.Sprint(aliceAgent.Version+1), nil},
		{http.MethodGet, "/v1/projects/" + project.ID, nil},
		{http.MethodPatch, "/v1/projects/" + project.ID, map[string]any{"name": "篡改", "expected_version": 1}},
		{http.MethodDelete, "/v1/projects/" + project.ID + "?expected_version=1", nil},
		{http.MethodGet, "/v1/projects/" + project.ID + "/resources", nil},
		{http.MethodPatch, "/v1/projects/" + project.ID + "/resources/" + resource.ID, map[string]any{"name": "篡改", "expected_version": 1}},
		{http.MethodDelete, "/v1/projects/" + project.ID + "/resources/" + resource.ID + "?expected_version=1", nil},
		{http.MethodPost, "/v1/projects/" + project.ID + "/resources/" + resource.ID + "/validate", map[string]any{"expected_version": 1}},
		{http.MethodGet, "/v1/projects/" + project.ID + "/issues", nil},
		{http.MethodGet, "/v1/issues/" + issue.ID, nil},
		{http.MethodPatch, "/v1/issues/" + issue.ID, map[string]any{"title": "篡改", "expected_version": 1}},
		{http.MethodDelete, "/v1/issues/" + issue.ID + "?expected_version=1", nil},
		{http.MethodGet, "/v1/issues/" + issue.ID + "/comments", nil},
		{http.MethodGet, "/v1/issues/" + issue.ID + "/agent-instances", nil},
		{http.MethodPatch, "/v1/comments/" + comment.ID, map[string]any{"body": "篡改", "expected_version": 1}},
		{http.MethodDelete, "/v1/comments/" + comment.ID + "?expected_version=1", nil},
		{http.MethodGet, "/v1/projects/" + project.ID + "/memories", nil},
		{http.MethodGet, "/v1/projects/" + project.ID + "/memories/" + memory.ID, nil},
		{http.MethodPatch, "/v1/projects/" + project.ID + "/memories/" + memory.ID, map[string]any{"title": "篡改", "content": "篡改", "expected_version": 1}},
		{http.MethodPost, "/v1/projects/" + project.ID + "/memories/" + memory.ID + "/archive", map[string]any{"expected_version": 1}},
		{http.MethodPost, "/v1/projects/" + project.ID + "/memories/" + memory.ID + "/restore", map[string]any{"expected_version": 1}},
		{http.MethodGet, "/v1/projects/" + project.ID + "/memories/" + memory.ID + "/versions", nil},
		{http.MethodGet, "/v1/projects/" + project.ID + "/policy-rules", nil},
		{http.MethodPatch, "/v1/policy-rules/" + policy.ID, map[string]any{"effect": "deny", "expected_version": 1}},
		{http.MethodDelete, "/v1/policy-rules/" + policy.ID + "?expected_version=1", nil},
		{http.MethodGet, "/v1/projects/" + project.ID + "/scheduled-tasks", nil},
		{http.MethodGet, "/v1/scheduled-tasks/" + scheduled.ID, nil},
		{http.MethodPatch, "/v1/scheduled-tasks/" + scheduled.ID, map[string]any{
			"name": "篡改", "schedule_kind": "cron", "schedule_expression": "0 10 * * *",
			"timezone": "Asia/Shanghai", "prompt": []map[string]string{{"type": "text", "text": "x"}},
			"expected_version": 1,
		}},
		{http.MethodPost, "/v1/scheduled-tasks/" + scheduled.ID + "/pause", map[string]any{"expected_version": 1}},
		{http.MethodPost, "/v1/scheduled-tasks/" + scheduled.ID + "/run-now", nil},
		{http.MethodGet, "/v1/scheduled-tasks/" + scheduled.ID + "/runs", nil},
	}
	for _, probe := range probes {
		response := request(t, router, probe.method, probe.path, probe.body, bearer(bob), http.StatusNotFound)
		if response.Error == nil || response.Error.Code != "not_found" {
			t.Fatalf("Bob %s %s expected not_found, got %+v", probe.method, probe.path, response.Error)
		}
	}

	// Bob must not enumerate Alice's rows. Bob queries with his own computer
	// id (Alice's id is invisible to him: the probe above returns 404), and
	// his list must be empty.
	bobComputer := createTestComputer(t, repository, bearer(bob), "Bob Mac")
	for _, list := range []struct{ method, path string }{
		{http.MethodGet, "/v1/projects?computer_id=" + bobComputer.ID},
		{http.MethodGet, "/v1/approvals?status=pending"},
	} {
		items := decodeData[struct {
			Items []json.RawMessage `json:"items"`
		}](t, request(t, router, list.method, list.path, nil, bearer(bob), http.StatusOK))
		if len(items.Items) != 0 {
			t.Fatalf("Bob %s %s leaked %d rows", list.method, list.path, len(items.Items))
		}
	}

	// Alice can still reach her own resources (sanity check).
	request(t, router, http.MethodGet, "/v1/projects/"+project.ID, nil, bearer(alice), http.StatusOK)
	request(t, router, http.MethodGet, "/v1/issues/"+issue.ID, nil, bearer(alice), http.StatusOK)
	request(t, router, http.MethodGet, "/v1/projects?computer_id="+aliceComputer.ID, nil, bearer(alice), http.StatusOK)
	items := decodeData[struct {
		Items []json.RawMessage `json:"items"`
	}](t, request(t, router, http.MethodGet, "/v1/projects?computer_id="+aliceComputer.ID, nil, bearer(alice), http.StatusOK))
	if len(items.Items) != 1 {
		t.Fatalf("Alice project list expected 1 row, got %d", len(items.Items))
	}
}

func ginSetTestMode(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
}
