package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type testResponse struct {
	Recorder *httptest.ResponseRecorder `json:"-"`
	Data     json.RawMessage            `json:"data"`
	Error    *struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
		Replayed  bool   `json:"replayed"`
	} `json:"meta"`
}

func TestExpiredDesktopBootstrapIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/desktop/bootstrap?nonce=secret", nil)
	api := &API{
		localUserID: "local-user", localBootstrap: "secret",
		localBootstrapUntil: time.Now().Add(-time.Second),
	}
	api.desktopBootstrap(context)
	if context.Writer.Status() != http.StatusNotFound {
		t.Fatalf("expired bootstrap status = %d, want 404", context.Writer.Status())
	}
}

func TestControlPlaneCoreFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testControlPlaneCoreFlow(t, newTestRepository(t))
}

func testControlPlaneCoreFlow(t *testing.T, repository *testRepository) {
	t.Helper()
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})

	auth := testLoginHeaders(t, repository, router)
	req := func(method, path string, body any, headers map[string]string, expectedStatus int) testResponse {
		t.Helper()
		merged := map[string]string{}
		for key, value := range auth {
			merged[key] = value
		}
		for key, value := range headers {
			merged[key] = value
		}
		return request(t, router, method, path, body, merged, expectedStatus)
	}

	health := req(http.MethodGet, "/v1/health", nil, nil, http.StatusOK)
	if health.Meta.RequestID == "" {
		t.Fatal("health response must contain request_id")
	}
	req(http.MethodGet, "/livez", nil, nil, http.StatusOK)
	req(http.MethodGet, "/readyz", nil, nil, http.StatusOK)

	computer := createTestComputer(t, repository, auth, "开发 Mac")

	projectResponse := req(http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID,
		"name":        "Teamva",
	}, nil, http.StatusCreated)
	project := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, projectResponse)
	req(http.MethodGet, "/v1/projects?computer_id="+computer.ID, nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/projects/"+project.ID, nil, nil, http.StatusOK)
	removedTheme := req(http.MethodPatch, "/v1/projects/"+project.ID, map[string]any{
		"theme": "hallmark", "expected_version": project.Version,
	}, nil, http.StatusBadRequest)
	if removedTheme.Error == nil || removedTheme.Error.Code != "invalid_argument" {
		t.Fatalf("removed project theme error=%+v", removedTheme.Error)
	}

	conflict := req(http.MethodPatch, "/v1/projects/"+project.ID, map[string]any{
		"name":             "冲突名称",
		"expected_version": project.Version + 1,
	}, nil, http.StatusConflict)
	if conflict.Error == nil || conflict.Error.Code != "version_conflict" {
		t.Fatalf("expected version_conflict, got %+v", conflict.Error)
	}

	req(http.MethodPost, "/v1/projects/"+project.ID+"/resources", map[string]any{
		"kind": "code_directory", "name": "相对目录", "path": "src",
	}, nil, http.StatusBadRequest)
	resourceResponse := req(http.MethodPost, "/v1/projects/"+project.ID+"/resources", map[string]any{
		"kind": "code_directory",
		"name": "主代码目录",
		"path": "/tmp/teamva-test-source",
	}, nil, http.StatusCreated)
	resource := decodeData[struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}](t, resourceResponse)
	if resource.Status != "pending_validation" {
		t.Fatalf("unexpected resource status %q", resource.Status)
	}
	req(http.MethodGet, "/v1/projects/"+project.ID+"/resources", nil, nil, http.StatusOK)
	resource = decodeData[struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPatch,
		"/v1/projects/"+project.ID+"/resources/"+resource.ID,
		map[string]any{"name": "主代码目录（更新）", "expected_version": resource.Version},
		nil, http.StatusOK))
	validationStatus := http.StatusAccepted
	validation := req(http.MethodPost,
		"/v1/projects/"+project.ID+"/resources/"+resource.ID+"/validate",
		map[string]any{"expected_version": resource.Version}, nil, validationStatus)
	resource = decodeData[struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}](t, validation)
	validated, err := repository.UpdateResource(t.Context(), testLocalUserID(repository, t), resource.ID, resource.Version, func(item *domain.ProjectResource) {
		item.Status = "ready"
		item.GitDetected = true
	})
	if err != nil {
		t.Fatal(err)
	}
	resource.Version = validated.Version
	{
		// Provider probe result fixture; execution still uses the real SQL outbox.
		if _, err := repository.UpsertAgentService(t.Context(), testLocalUserID(repository, t), domain.AgentService{
			ComputerID:        computer.ID,
			Provider:          "opencode",
			ProviderVersion:   "test",
			LoginStatus:       "authenticated",
			Status:            "ready",
			Capabilities:      map[string]any{"models": []string{"opencode/model-x"}},
			CapabilityVersion: 1,
		}); err != nil {
			t.Fatalf("provision agent service: %v", err)
		}
	}

	// Agent 配置（引用 fixture 提供的 agent_service）。
	servicesPage := decodeData[struct {
		Items []domain.AgentService `json:"items"`
	}](t, req(http.MethodGet, "/v1/computers/"+computer.ID+"/agent-services", nil, nil, http.StatusOK))
	if len(servicesPage.Items) == 0 {
		t.Fatal("expected at least one agent service from fixture")
	}
	serviceID := servicesPage.Items[0].ID
	agent := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/computers/"+computer.ID+"/agents", map[string]any{
		"agent_service_id": serviceID,
		"name":             "前端专家",
		"system_prompt":    "你是前端专家",
	}, nil, http.StatusCreated))
	req(http.MethodGet, "/v1/computers/"+computer.ID+"/agents", nil, nil, http.StatusOK)
	agent = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPatch, "/v1/agents/"+agent.ID, map[string]any{
		"note": "负责前端", "expected_version": agent.Version,
	}, nil, http.StatusOK))

	// Issue 生命周期。
	issueResponse := req(http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "实现控制面", "description": "描述", "worktree_hint": true,
	}, nil, http.StatusCreated)
	issue := decodeData[struct {
		ID           string `json:"id"`
		Version      int64  `json:"version"`
		WorktreeHint bool   `json:"worktree_hint"`
	}](t, issueResponse)
	if !issue.WorktreeHint {
		t.Fatal("worktree_hint must be persisted")
	}
	req(http.MethodGet, "/v1/projects/"+project.ID+"/issues", nil, nil, http.StatusOK)
	issue = decodeData[struct {
		ID           string `json:"id"`
		Version      int64  `json:"version"`
		WorktreeHint bool   `json:"worktree_hint"`
	}](t, req(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"status": "open", "expected_version": issue.Version,
	}, nil, http.StatusOK))

	// 人类评论 + @Agent。
	comment := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "@前端专家 请实现", "mentioned_agent_ids": []string{agent.ID},
	}, nil, http.StatusCreated))
	_ = comment

	// Agent 激活 run（@ 触发的执行）。
	instance, _, err := repository.EnsureIssueAgentInstance(t.Context(), testLocalUserID(repository, t), issue.ID, agent.ID)
	if err != nil {
		t.Fatalf("create run session: %v", err)
	}
	runBody := map[string]any{
		"input":                   "实现 API",
		"issue_agent_instance_id": instance.ID,
		"expected_issue_version":  issue.Version,
	}
	runResponse := req(http.MethodPost, "/v1/issues/"+issue.ID+"/runs", runBody, map[string]string{
		"Idempotency-Key": "test-run-1",
	}, http.StatusAccepted)
	run := decodeData[struct {
		ID                   string `json:"id"`
		Status               string `json:"status"`
		IssueAgentInstanceID string `json:"issue_agent_instance_id"`
	}](t, runResponse)
	if run.Status != "queued" {
		t.Fatalf("unexpected run status %q", run.Status)
	}
	assertRunSSE(t, router, run.ID, auth)
	req(http.MethodGet, "/v1/runs/"+run.ID, nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/issues/"+issue.ID+"/agent-instances", nil, nil, http.StatusOK)

	replayed := req(http.MethodPost, "/v1/issues/"+issue.ID+"/runs", runBody, map[string]string{
		"Idempotency-Key": "test-run-1",
	}, http.StatusOK)
	replayedRun := decodeData[struct {
		ID string `json:"id"`
	}](t, replayed)
	if !replayed.Meta.Replayed || replayedRun.ID != run.ID {
		t.Fatalf("idempotent retry must return the same run")
	}

	// 同一 Agent 实例并发第二个 run → target_busy（实例内串行）。
	busy := req(http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"input":                   "第二个运行",
		"issue_agent_instance_id": run.IssueAgentInstanceID,
		"expected_issue_version":  issue.Version,
	}, map[string]string{
		"Idempotency-Key": "test-run-2",
	}, http.StatusConflict)
	if busy.Error == nil || busy.Error.Code != "target_busy" {
		t.Fatalf("expected target_busy, got %+v", busy.Error)
	}

	events := req(http.MethodGet, "/v1/runs/"+run.ID+"/events", nil, nil, http.StatusOK)
	eventPage := decodeData[struct {
		Items []struct {
			Type string `json:"event_type"`
		} `json:"items"`
	}](t, events)
	if len(eventPage.Items) != 1 || eventPage.Items[0].Type != "run.queued" {
		t.Fatalf("expected a queued event, got %+v", eventPage.Items)
	}

	stopped := req(http.MethodPost, "/v1/runs/"+run.ID+"/stop", map[string]any{
		"mode": "graceful",
	}, nil, http.StatusAccepted)
	stoppedRun := decodeData[struct {
		Status string `json:"status"`
	}](t, stopped)
	if stoppedRun.Status != "cancelling" {
		t.Fatalf("unexpected stopped run status %q", stoppedRun.Status)
	}
	firstEventPage := decodeData[domain.RunEventPage](t, req(
		http.MethodGet, "/v1/runs/"+run.ID+"/events?after=0&limit=1", nil, nil, http.StatusOK,
	))
	if len(firstEventPage.Items) != 1 || firstEventPage.NextCursor != "1" || !firstEventPage.Truncated {
		t.Fatalf("first bounded event page=%+v", firstEventPage)
	}
	secondEventPage := decodeData[domain.RunEventPage](t, req(
		http.MethodGet, "/v1/runs/"+run.ID+"/events?after="+firstEventPage.NextCursor+"&limit=1",
		nil, nil, http.StatusOK,
	))
	if len(secondEventPage.Items) != 1 || secondEventPage.Items[0].Sequence <= firstEventPage.Items[0].Sequence {
		t.Fatalf("second bounded event page=%+v", secondEventPage)
	}

	disposableResource := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/projects/"+project.ID+"/resources", map[string]any{
		"kind": "url", "name": "临时文档", "url": "https://example.com",
	}, nil, http.StatusCreated))
	req(http.MethodDelete,
		"/v1/projects/"+project.ID+"/resources/"+disposableResource.ID+"?expected_version="+itoa(disposableResource.Version),
		nil, nil, http.StatusNoContent)

	disposableIssue := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "临时任务",
	}, nil, http.StatusCreated))
	disposableIssue = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPatch,
		"/v1/issues/"+disposableIssue.ID,
		map[string]any{"title": "临时任务（更新）", "expected_version": disposableIssue.Version},
		nil, http.StatusOK))
	req(http.MethodDelete,
		"/v1/issues/"+disposableIssue.ID+"?expected_version="+itoa(disposableIssue.Version),
		nil, nil, http.StatusNoContent)

	disposableProject := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID, "name": "临时项目",
	}, nil, http.StatusCreated))
	disposableProject = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPatch, "/v1/projects/"+disposableProject.ID, map[string]any{
		"name": "临时项目（更新）", "expected_version": disposableProject.Version,
	}, nil, http.StatusOK))
	req(http.MethodDelete,
		"/v1/projects/"+disposableProject.ID+"?expected_version="+itoa(disposableProject.Version),
		nil, nil, http.StatusNoContent)
}

func assertRunSSE(t *testing.T, handler http.Handler, runID string, headers map[string]string) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet, server.URL+"/v1/runs/"+runID+"/events/stream?after_seq=0", nil,
	)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	for name, value := range headers {
		httpRequest.Header.Set(name, value)
	}
	response, err := server.Client().Do(httpRequest)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected SSE response: status=%d content_type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	var received strings.Builder
	for scanner.Scan() {
		received.WriteString(scanner.Text())
		received.WriteByte('\n')
		if strings.Contains(received.String(), `"seq":1`) {
			cancel()
			break
		}
	}
	if !strings.Contains(received.String(), "event:run.queued") ||
		!strings.Contains(received.String(), `"seq":1`) {
		t.Fatalf("missing queued SSE event: %q", received.String())
	}
}

func TestRunRequiresIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newRouter(newTestRepository(t), nil, "test", false)

	response := request(t, router, http.MethodPost, "/v1/issues/missing/runs", map[string]any{
		"input":                  []map[string]any{{"type": "text", "text": "test"}},
		"expected_issue_version": 1,
	}, nil, http.StatusBadRequest)
	if response.Error == nil || response.Error.Code != "invalid_argument" {
		t.Fatalf("expected invalid_argument, got %+v", response.Error)
	}
}

func request(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	headers map[string]string,
	expectedStatus int,
) testResponse {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	httpRequest := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		httpRequest.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != expectedStatus {
		t.Fatalf("%s %s expected %d, got %d: %s", method, path, expectedStatus, recorder.Code, recorder.Body.String())
	}
	if expectedStatus == http.StatusNoContent {
		return testResponse{}
	}
	var response testResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response for %s %s: %v (body=%s)", method, path, err, recorder.Body.String())
	}
	response.Recorder = recorder
	return response
}

func decodeData[T any](t *testing.T, response testResponse) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return result
}
