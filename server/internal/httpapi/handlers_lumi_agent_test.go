package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestLumiAndOrdinaryAgentSharePATCHAndLegacyEditorIsRemoved(t *testing.T) {
	ginSetTestMode(t)
	repo := newTestRepository(t)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identity := createTestIdentity(t, repo, user.Email)
	headers := map[string]string{"Cookie": sessionCookieName + "=" + identity.token, "X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	lumi, err := repo.EnsureGeneralAgent(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "ordinary"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	for _, agent := range []domain.Agent{lumi, ordinary} {
		body := map[string]any{"name": "renamed-" + agent.ID, "note": "same form", "agent_service_id": service.ID, "model": "custom/model", "system_prompt": "custom prompt", "skill_version_ids": []string{}, "expected_version": agent.Version}
		updated := decodeData[domain.Agent](t, localRequest(t, router, http.MethodPatch, "/v1/agents/"+agent.ID, body, headers, 200))
		if updated.Name != body["name"] || updated.AgentServiceID != service.ID || updated.CurrentRevisionID == "" {
			t.Fatal("shared Agent fields not saved")
		}
		localRequest(t, router, http.MethodPatch, "/v1/agents/"+agent.ID, body, headers, 409)
	}
	current, err := repo.GetAgent(t.Context(), user.ID, lumi.ID)
	if err != nil {
		t.Fatal(err)
	}
	localRequest(t, router, http.MethodDelete, "/v1/agents/"+lumi.ID+"?expected_version="+strconv.FormatInt(current.Version, 10), nil, headers, 403)
	legacy := httptest.NewRequest(http.MethodPut, "/v1/work-assistant", strings.NewReader(`{"agent_service_id":"unused","expected_version":1}`))
	for key, value := range headers {
		legacy.Header.Set(key, value)
	}
	legacy.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, legacy)
	if recorder.Code != 404 && recorder.Code != 405 {
		t.Fatalf("legacy configuration endpoint remains active: %d", recorder.Code)
	}
}
