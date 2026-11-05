package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// This fixture never implements command execution or starts a Provider. Only
// durable HTTP/store admission and synthetic event projection are under test.
type runtimeSurfaceNotifier struct {
	notifications atomic.Int64
	subscribed    atomic.Int64
	unsubscribed  atomic.Int64
	wake          chan struct{}
}

func (r *runtimeSurfaceNotifier) NotifyIssueActivation(string) { r.notifications.Add(1) }
func (r *runtimeSurfaceNotifier) SubscribeRunEvents(string) (<-chan struct{}, func()) {
	r.subscribed.Add(1)
	return r.wake, func() { r.unsubscribed.Add(1) }
}
func (r *runtimeSurfaceNotifier) SubscribeIssue(string) (<-chan struct{}, func()) {
	return r.SubscribeRunEvents("")
}

type runtimeSurfaceFixture struct {
	t        *testing.T
	repo     *testRepository
	server   *httptest.Server
	client   *http.Client
	auth     map[string]string
	runtime  *runtimeSurfaceNotifier
	user     domain.User
	computer domain.Computer
	service  domain.AgentService
	agent    domain.Agent
	project  domain.Project
	issue    domain.Issue
	instance domain.IssueAgentInstance
}

type runtimeSurfaceResponse struct {
	Status int
	Header http.Header
	Raw    []byte
	Data   json.RawMessage `json:"data"`
	Error  *struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta struct {
		Replayed bool `json:"replayed"`
	} `json:"meta"`
}

func newRuntimeSurfaceFixture(t *testing.T) *runtimeSurfaceFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &runtimeSurfaceFixture{t: t, repo: newTestRepository(t), auth: map[string]string{},
		runtime: &runtimeSurfaceNotifier{wake: make(chan struct{}, 8)}}
	f.server = httptest.NewServer(newRouterWithOrigins(f.repo, f.runtime, "runtime-surface", true, []string{testBrowserOrigin}))
	t.Cleanup(f.server.Close)
	f.client = f.server.Client()
	f.client.Timeout = 5 * time.Second
	login := f.request(http.MethodPost, "/v1/auth/local-session", map[string]any{}, "", http.StatusOK)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Data, &session); err != nil || session.CSRF == "" {
		t.Fatal("TCP bootstrap did not return CSRF token")
	}
	for _, cookie := range (&http.Response{Header: login.Header}).Cookies() {
		if cookie.Name == sessionCookieName {
			if !cookie.HttpOnly {
				t.Fatal("session must be HttpOnly")
			}
			f.auth["Cookie"] = cookie.Name + "=" + cookie.Value
		}
	}
	if f.auth["Cookie"] == "" {
		t.Fatal("TCP bootstrap did not set session cookie")
	}
	f.auth["X-CSRF-Token"] = session.CSRF
	var err error
	f.user, f.computer, err = f.repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic authenticated catalog result; no native CLI has been probed.
	f.service, err = f.repo.UpsertAgentService(t.Context(), f.user.ID, domain.AgentService{
		ComputerID: f.computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{"models": []string{"surface-test"}}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = f.createAgent("surface A")
	f.project, err = f.repo.CreateProject(t.Context(), f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "runtime surface"})
	if err != nil {
		t.Fatal(err)
	}
	f.issue, err = f.repo.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: "runtime surface"})
	if err != nil {
		t.Fatal(err)
	}
	f.instance, _, err = f.repo.EnsureIssueAgentInstance(t.Context(), f.user.ID, f.issue.ID, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *runtimeSurfaceFixture) createAgent(name string) domain.Agent {
	f.t.Helper()
	agent, err := f.repo.CreateConfiguredAgent(f.t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: name,
		Model: "surface-test", Status: domain.AgentActive,
	}, "surface fixture system prompt", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return agent
}

func (f *runtimeSurfaceFixture) newRequest(ctx context.Context, method, path string, body any, key string) (*http.Request, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	// The actual transport is loopback TCP; the configured browser origin is
	// stable so no production listener/origin configuration is touched.
	req.Host = "example.com"
	req.Header.Set("Origin", testBrowserOrigin)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range f.auth {
		req.Header.Set(name, value)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req, nil
}

func (f *runtimeSurfaceFixture) do(method, path string, body any, key string) (runtimeSurfaceResponse, error) {
	req, err := f.newRequest(f.t.Context(), method, path, body, key)
	if err != nil {
		return runtimeSurfaceResponse{}, err
	}
	res, err := f.client.Do(req)
	if err != nil {
		return runtimeSurfaceResponse{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return runtimeSurfaceResponse{}, err
	}
	var reply runtimeSurfaceResponse
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &reply); err != nil {
			return reply, fmt.Errorf("%s %s: non-JSON response: %w", method, path, err)
		}
	}
	reply.Status, reply.Raw, reply.Header = res.StatusCode, raw, res.Header
	return reply, nil
}

func (f *runtimeSurfaceFixture) request(method, path string, body any, key string, status int) runtimeSurfaceResponse {
	f.t.Helper()
	reply, err := f.do(method, path, body, key)
	if err != nil {
		f.t.Fatal(err)
	}
	if reply.Status != status {
		f.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, reply.Status, status, reply.Raw)
	}
	return reply
}

func runtimeSurfaceData[T any](t *testing.T, response runtimeSurfaceResponse) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Data, &value); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return value
}

func (f *runtimeSurfaceFixture) issueRunBody(input string) map[string]any {
	return map[string]any{"issue_agent_instance_id": f.instance.ID, "input": input, "expected_issue_version": f.issue.Version}
}

func (f *runtimeSurfaceFixture) createRun(key string) domain.Run {
	f.t.Helper()
	return runtimeSurfaceData[domain.Run](f.t, f.request(http.MethodPost, "/v1/issues/"+f.issue.ID+"/runs",
		f.issueRunBody("surface question"), key, http.StatusAccepted))
}

// Synthetic Provider events enter through the same public repository boundary
// used by the embedded controller, never SQL mutation or external processes.
func (f *runtimeSurfaceFixture) events(run domain.Run, events ...nodeprotocol.RunEvent) {
	f.t.Helper()
	for i := range events {
		if events[i].Payload == nil {
			events[i].Payload = map[string]any{}
		}
		events[i].Payload["provider_id"] = "codex"
		events[i].ID = fmt.Sprintf("surface-%s-%d", run.ID, events[i].Sequence)
		events[i].RunID, events[i].LeaseEpoch = run.ID, run.LeaseEpoch
		events[i].OccurredAt = time.Now().UTC()
	}
	if _, err := f.repo.ApplyLocalEvents(f.t.Context(), events); err != nil {
		f.t.Fatal(err)
	}
}

func (f *runtimeSurfaceFixture) finish(run domain.Run, sequence int64) {
	f.events(run, nodeprotocol.RunEvent{Sequence: sequence, Type: "run.finished", Payload: map[string]any{"status": "cancelled"}})
}

func (f *runtimeSurfaceFixture) graph() domain.ConversationGraphDetail {
	f.t.Helper()
	return runtimeSurfaceData[domain.ConversationGraphDetail](f.t, f.request(http.MethodPost,
		"/v1/projects/"+f.project.ID+"/conversation-graphs", map[string]any{"title": "surface graph", "agent_id": f.agent.ID}, "", http.StatusCreated))
}
