package httpapi

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestScheduleAPIProfilesPreviewAndIssueHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, &resourceOpenTestRelay{}, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "schedule profile")
	userID := testLocalUserID(repository, t)
	project, err := repository.CreateProject(t.Context(), userID, domain.Project{ComputerID: computer.ID, Name: "profiles"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := repository.UpsertAgentService(t.Context(), userID, domain.AgentService{ComputerID: computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated", Capabilities: map[string]any{}, CapabilityVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(t.Context(), userID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "worker", Status: domain.AgentActive})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"name": "daily", "schedule_kind": "daily", "times": []string{"17:45", "08:15", "08:15"}, "timezone": "Asia/Shanghai", "agent_id": agent.ID, "prompt": []domain.ContentBlock{{Type: "text", Text: "真实指令"}}}
	path := "/v1/projects/" + project.ID + "/scheduled-tasks"
	// An enabled but not configured Agent is not a business writer.
	request(t, router, http.MethodPost, path, input, auth, http.StatusBadRequest)
	if _, err := repository.CreateAgentRevision(t.Context(), userID, domain.AgentRevision{AgentID: agent.ID, AgentServiceID: service.ID, Model: "test/model"}, nil); err != nil {
		t.Fatal(err)
	}
	task := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost, path, input, auth, http.StatusCreated))
	if task.IssueMode != "new" || !reflect.DeepEqual(task.Times, []string{"08:15", "17:45"}) || task.ScheduleExpression != "" || task.NextRunAt == nil {
		t.Fatal(task)
	}
	preview := decodeData[struct {
		Times []time.Time `json:"times"`
	}](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/preview", map[string]any{"schedule_kind": "daily", "times": input["times"], "timezone": "Asia/Shanghai", "count": 3}, auth, http.StatusOK))
	if len(preview.Times) != 3 || !preview.Times[0].Equal(*task.NextRunAt) {
		t.Fatalf("preview=%+v task=%+v", preview, task)
	}
	paused := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/pause", map[string]any{"expected_version": task.Version}, auth, http.StatusOK))
	resumed := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/resume", map[string]any{"expected_version": paused.Version}, auth, http.StatusOK))
	if resumed.IssueMode != "new" || !reflect.DeepEqual(resumed.Times, task.Times) {
		t.Fatal(resumed)
	}
	headers := map[string]string{}
	for key, value := range auth {
		headers[key] = value
	}
	headers["Idempotency-Key"] = "profile-one"
	trigger := decodeData[domain.ScheduledTaskRun](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, headers, http.StatusAccepted))
	replay := decodeData[domain.ScheduledTaskRun](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, headers, http.StatusOK))
	if trigger.IssueID == "" || trigger.RunID == "" || replay.ID != trigger.ID || replay.IssueID != trigger.IssueID {
		t.Fatalf("trigger=%+v replay=%+v", trigger, replay)
	}
	history := decodeData[domain.Page[domain.ScheduledTaskRun]](t, request(t, router, http.MethodGet, "/v1/scheduled-tasks/"+task.ID+"/runs", nil, auth, http.StatusOK))
	if len(history.Items) != 1 || history.Items[0].IssueID != trigger.IssueID {
		t.Fatal(history)
	}
	input["issue_mode"] = "existing"
	input["issue_id"] = trigger.IssueID
	input["expected_version"] = resumed.Version
	updated := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, input, auth, http.StatusOK))
	if updated.IssueMode != "existing" || updated.IssueID != trigger.IssueID {
		t.Fatal(updated)
	}
	delete(input, "issue_mode")
	delete(input, "issue_id")
	input["expected_version"] = updated.Version
	updated = decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, input, auth, http.StatusOK))
	if updated.IssueMode != "existing" || updated.IssueID != trigger.IssueID {
		t.Fatal(updated)
	}
	// Foreign project ownership is checked before triggering or revealing history.
	foreign := createTestIdentity(t, repository, "schedule-foreign@example.test")
	foreignAuth := map[string]string{"Cookie": sessionCookieName + "=" + foreign.token, "X-CSRF-Token": foreign.csrf, "Origin": testBrowserOrigin, "Idempotency-Key": "foreign"}
	for _, suffix := range []string{"/run-now", "/runs"} {
		method := http.MethodGet
		if suffix == "/run-now" {
			method = http.MethodPost
		}
		request(t, router, method, "/v1/scheduled-tasks/"+task.ID+suffix, nil, foreignAuth, http.StatusNotFound)
	}
	// Built-in identity protection does not make Lumi read-only. Once configured
	// it is a normal writable Agent and can be used by scheduled Issue work.
	lumi, err := repository.EnsureGeneralAgent(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAgentRevision(t.Context(), userID, domain.AgentRevision{AgentID: lumi.ID, AgentServiceID: service.ID, Model: "test/model"}, nil); err != nil {
		t.Fatal(err)
	}
	input["agent_id"] = lumi.ID
	delete(input, "expected_version")
	lumiTask := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost, path, input, auth, http.StatusCreated))
	if lumiTask.AgentID != lumi.ID {
		t.Fatalf("scheduled task lost the Lumi Agent: %+v", lumiTask)
	}
	lumiHeaders := map[string]string{}
	for key, value := range headers {
		lumiHeaders[key] = value
	}
	lumiHeaders["Idempotency-Key"] = "profile-lumi"
	lumiTrigger := decodeData[domain.ScheduledTaskRun](t, request(t, router, http.MethodPost, "/v1/scheduled-tasks/"+lumiTask.ID+"/run-now", nil, lumiHeaders, http.StatusAccepted))
	if lumiTrigger.RunID == "" || lumiTrigger.IssueID == "" {
		t.Fatalf("Lumi scheduled run was not created: %+v", lumiTrigger)
	}
	run, err := repository.GetRun(t.Context(), userID, lumiTrigger.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ReadOnly {
		t.Fatal("Lumi scheduled Issue run must use the normal writable Agent scope")
	}
	input["agent_id"] = agent.ID
	input["name"] = "  "
	unnamed := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost, path, input, auth, http.StatusCreated))
	if unnamed.Name != "新定时任务" {
		t.Fatalf("blank creation name did not initialize the automatic placeholder: %+v", unnamed)
	}
}
