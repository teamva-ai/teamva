package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// This is an HTTP/store integration fixture, not an Agent execution fixture.
// There is no dispatcher, scheduler, Provider, model process, or background timer.
// Only the public UpsertAgentService boundary synthesizes a ready CLI catalog;
// Agents, projects, Issues, configuration, and triggers are written over TCP.
type scheduleSurfaceFixture struct {
	t        *testing.T
	repo     *testRepository
	server   *httptest.Server
	client   *http.Client
	auth     map[string]string
	user     domain.User
	computer domain.Computer
	service  domain.AgentService
	agent    domain.Agent
	project  domain.Project
}

type scheduleSurfaceReply struct {
	Status int
	Header http.Header
	Raw    []byte
	Data   json.RawMessage `json:"data"`
	Error  *struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta struct {
		Replayed        bool  `json:"replayed"`
		ResourceVersion int64 `json:"resource_version"`
	} `json:"meta"`
}

func newScheduleSurfaceFixture(t *testing.T) *scheduleSurfaceFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &scheduleSurfaceFixture{t: t, repo: newTestRepository(t), auth: map[string]string{}}
	f.server = httptest.NewServer(newRouterWithOrigins(f.repo, nil, "schedule-surface", true, []string{testBrowserOrigin}))
	t.Cleanup(f.server.Close)
	f.client = f.server.Client()
	f.client.Timeout = 15 * time.Second
	login := f.call(http.MethodPost, "/v1/auth/local-session", map[string]any{}, "", http.StatusOK,
		"local TCP bootstrap returns an authenticated session and CSRF token")
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Data, &session); err != nil || session.CSRF == "" {
		t.Fatal("missing bootstrap CSRF token")
	}
	for _, cookie := range (&http.Response{Header: login.Header}).Cookies() {
		if cookie.Name == sessionCookieName {
			if !cookie.HttpOnly {
				t.Fatal("local session cookie is not HttpOnly")
			}
			f.auth["Cookie"] = cookie.Name + "=" + cookie.Value
		}
	}
	if f.auth["Cookie"] == "" {
		t.Fatal("missing bootstrap session cookie")
	}
	f.auth["X-CSRF-Token"] = session.CSRF
	var err error
	f.user, f.computer, err = f.repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = f.repo.UpsertAgentService(t.Context(), f.user.ID, domain.AgentService{
		ComputerID: f.computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{"models": []string{"schedule-fixture-model"}}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = scheduleSurfaceData[domain.Agent](t, f.call(http.MethodPost, "/v1/computers/"+f.computer.ID+"/agents",
		map[string]any{"name": "schedule worker", "agent_service_id": f.service.ID, "model": "schedule-fixture-model", "system_prompt": "HTTP fixture; never execute a model"},
		"", http.StatusCreated, "ready catalog permits creating a configured Agent via the public API"))
	if f.agent.CurrentRevisionID == "" || f.agent.Status != domain.AgentActive {
		t.Fatal("HTTP-created schedule Agent is not configured and active")
	}
	f.project = f.createProject("schedule surface")
	return f
}

func (f *scheduleSurfaceFixture) withT(t *testing.T) *scheduleSurfaceFixture {
	t.Helper()
	copy := *f
	copy.t = t
	return &copy
}

// do is safe for concurrent requests: all fixture authentication is read-only
// after construction and assertions remain on the parent testing goroutine.
func (f *scheduleSurfaceFixture) do(method, path string, body any, key string) (scheduleSurfaceReply, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return scheduleSurfaceReply{}, err
		}
	}
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		return scheduleSurfaceReply{}, err
	}
	// TCP connects to the ephemeral loopback listener. Browser host/origin is a
	// stable fixture value, never an actual production listener configuration.
	req.Host = "example.com"
	req.Header.Set("Origin", testBrowserOrigin)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range f.auth {
		req.Header.Set(name, value)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := f.client.Do(req)
	if err != nil {
		return scheduleSurfaceReply{}, err
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(response.Body)
	if err != nil {
		return scheduleSurfaceReply{}, err
	}
	var reply scheduleSurfaceReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return reply, fmt.Errorf("%s %s: invalid JSON response: %w", method, path, err)
	}
	reply.Status, reply.Raw, reply.Header = response.StatusCode, raw, response.Header
	return reply, nil
}

func (f *scheduleSurfaceFixture) call(method, path string, body any, key string, status int, property string) scheduleSurfaceReply {
	f.t.Helper()
	if property == "" {
		f.t.Fatal("each request must declare its expected status and property before sending")
	}
	f.t.Logf("EXPECT %d %s %s — %s", status, method, path, property)
	reply, err := f.do(method, path, body, key)
	if err != nil {
		f.t.Fatal(err)
	}
	if reply.Status != status {
		f.t.Fatalf("%s %s: status=%d want=%d property=%s body=%s", method, path, reply.Status, status, property, reply.Raw)
	}
	if reply.Header.Get("Cache-Control") != "no-store" || reply.Header.Get("X-Request-ID") == "" {
		f.t.Fatalf("response lacks cache/request identity protections: %v", reply.Header)
	}
	return reply
}

func scheduleSurfaceData[T any](t *testing.T, reply scheduleSurfaceReply) T {
	t.Helper()
	var data T
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		t.Fatalf("decode response: %v body=%s", err, reply.Raw)
	}
	return data
}

func scheduleSurfaceError(t *testing.T, reply scheduleSurfaceReply, code string) {
	t.Helper()
	if reply.Error == nil || reply.Error.Code != code {
		t.Fatalf("error code must be %s: %s", code, reply.Raw)
	}
}

func (f *scheduleSurfaceFixture) createProject(name string) domain.Project {
	f.t.Helper()
	return scheduleSurfaceData[domain.Project](f.t, f.call(http.MethodPost, "/v1/projects",
		map[string]any{"computer_id": f.computer.ID, "name": name}, "", http.StatusCreated, "create an owned project via HTTP"))
}

func (f *scheduleSurfaceFixture) createIssue(projectID, title string) domain.Issue {
	f.t.Helper()
	return scheduleSurfaceData[domain.Issue](f.t, f.call(http.MethodPost, "/v1/projects/"+projectID+"/issues",
		map[string]any{"title": title, "description": "preserved target context"}, "", http.StatusCreated, "create an open existing target via HTTP"))
}

func (f *scheduleSurfaceFixture) input(name string) map[string]any {
	// Absolute metadata dates stay safely in 2040. Recurring calendars have no
	// year field; the fixture never starts a scheduler or executes due work.
	return map[string]any{"name": name, "schedule_kind": "once", "schedule_expression": "2040-06-01T12:34:56+08:00",
		"timezone": "Asia/Shanghai", "agent_id": f.agent.ID, "prompt": []domain.ContentBlock{{Type: "text", Text: "first instruction"}, {Type: "text", Text: "second instruction"}}}
}

func (f *scheduleSurfaceFixture) createTask(input map[string]any) domain.ScheduledTask {
	f.t.Helper()
	reply := f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", input, "", http.StatusCreated,
		"valid calendar and configured Agent persist exactly one enabled task")
	task := scheduleSurfaceData[domain.ScheduledTask](f.t, reply)
	if reply.Meta.ResourceVersion != task.Version || task.Version != 1 || !task.Enabled || task.NextRunAt == nil {
		f.t.Fatalf("invalid created task/version metadata: %+v meta=%+v", task, reply.Meta)
	}
	return task
}

func (f *scheduleSurfaceFixture) task(id string) domain.ScheduledTask {
	f.t.Helper()
	reply := f.call(http.MethodGet, "/v1/scheduled-tasks/"+id, nil, "", http.StatusOK, "GET returns the committed task and matching resource version")
	task := scheduleSurfaceData[domain.ScheduledTask](f.t, reply)
	if reply.Meta.ResourceVersion != task.Version {
		f.t.Fatal("task GET version metadata disagrees with resource")
	}
	return task
}

func (f *scheduleSurfaceFixture) tasks(projectID string) []domain.ScheduledTask {
	f.t.Helper()
	page := scheduleSurfaceData[domain.Page[domain.ScheduledTask]](f.t, f.call(http.MethodGet,
		"/v1/projects/"+projectID+"/scheduled-tasks", nil, "", http.StatusOK, "owned project list contains only fully committed tasks; empty items is an array"))
	if page.Items == nil {
		f.t.Fatal("scheduled task page must return [] rather than null")
	}
	return page.Items
}

func (f *scheduleSurfaceFixture) history(id string) []domain.ScheduledTaskRun {
	f.t.Helper()
	page := scheduleSurfaceData[domain.Page[domain.ScheduledTaskRun]](f.t, f.call(http.MethodGet,
		"/v1/scheduled-tasks/"+id+"/runs", nil, "", http.StatusOK, "GET history exposes exactly the committed trigger records"))
	if page.Items == nil {
		f.t.Fatal("history page must return [] rather than null")
	}
	return page.Items
}

func (f *scheduleSurfaceFixture) issues(projectID string) []domain.Issue {
	f.t.Helper()
	return scheduleSurfaceData[domain.Page[domain.Issue]](f.t, f.call(http.MethodGet, "/v1/projects/"+projectID+"/issues",
		nil, "", http.StatusOK, "GET project Issues detects unintended or half-written target creation")).Items
}

func (f *scheduleSurfaceFixture) comments(issueID string) []domain.IssueComment {
	f.t.Helper()
	return scheduleSurfaceData[domain.Page[domain.IssueComment]](f.t, f.call(http.MethodGet, "/v1/issues/"+issueID+"/comments",
		nil, "", http.StatusOK, "GET comments verifies the genuine user instruction and linked working reply")).Items
}

func (f *scheduleSurfaceFixture) instances(issueID string) []domain.IssueAgentInstance {
	f.t.Helper()
	return scheduleSurfaceData[domain.Page[domain.IssueAgentInstance]](f.t, f.call(http.MethodGet, "/v1/issues/"+issueID+"/agent-instances",
		nil, "", http.StatusOK, "GET sessions verifies one durable session per consumed scheduled mention")).Items
}

func (f *scheduleSurfaceFixture) assertTaskUnchanged(before domain.ScheduledTask) {
	f.t.Helper()
	if after := f.task(before.ID); !reflect.DeepEqual(before, after) {
		f.t.Fatalf("rejected operation partially changed task: before=%+v after=%+v", before, after)
	}
}

func scheduleSurfaceTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
