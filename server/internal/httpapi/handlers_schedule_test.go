package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestScheduleAPIRejectsImpossibleCalendarForCreateUpdateAndPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, &resourceOpenTestRelay{}, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "schedule calendar")
	userID := testLocalUserID(repository, t)
	project, err := repository.CreateProject(t.Context(), userID, domain.Project{ComputerID: computer.ID, Name: "calendar"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := repository.UpsertAgentService(t.Context(), userID, domain.AgentService{
		ComputerID: computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(t.Context(), userID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "calendar worker", Status: domain.AgentActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAgentRevision(t.Context(), userID, domain.AgentRevision{AgentID: agent.ID, AgentServiceID: service.ID}, nil); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"name": "calendar", "schedule_kind": "cron", "schedule_expression": "0 0 31 2 *", "timezone": "UTC",
		"agent_id": agent.ID, "prompt": []map[string]any{{"type": "text", "text": "work"}}, "misfire_policy": "run_once",
	}
	for _, path := range []string{"/v1/scheduled-tasks/preview", "/v1/projects/" + project.ID + "/scheduled-tasks"} {
		body := input
		if path == "/v1/scheduled-tasks/preview" {
			body = map[string]any{"schedule_kind": input["schedule_kind"], "schedule_expression": input["schedule_expression"], "timezone": input["timezone"]}
		}
		response := request(t, router, http.MethodPost, path, body, auth, http.StatusBadRequest)
		if response.Error == nil || response.Error.Code != "schedule_no_next_run" {
			t.Fatalf("no-next error response=%s", response.Recorder.Body.String())
		}
	}
	input["schedule_expression"] = "0 0 * * *"
	created := decodeData[domain.ScheduledTask](t, request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/scheduled-tasks", input, auth, http.StatusCreated))
	input["schedule_expression"] = "0 0 31 2 *"
	input["expected_version"] = created.Version
	response := request(t, router, http.MethodPatch, "/v1/scheduled-tasks/"+created.ID, input, auth, http.StatusBadRequest)
	if response.Error == nil || response.Error.Code != "schedule_no_next_run" {
		t.Fatalf("update no-next response=%+v", response)
	}
	stored, err := repository.GetScheduledTask(t.Context(), userID, created.ID)
	if err != nil || stored.Version != created.Version || stored.ScheduleExpression != created.ScheduleExpression {
		t.Fatalf("invalid API update changed task=%+v err=%v", stored, err)
	}
}
