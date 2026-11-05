package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestDefaultProjectAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computerID := computerID(t, repository)
	page := decodeData[domain.Page[domain.Project]](t, request(t, router, http.MethodGet, "/v1/projects?computer_id="+computerID, nil, auth, http.StatusOK))
	if len(page.Items) != 1 || !page.Items[0].IsDefault || page.Items[0].Name != "默认项目" || page.Items[0].ID != domain.DefaultProjectID(computerID) {
		t.Fatalf("default project list: %+v", page.Items)
	}
	project := page.Items[0]
	path := "/v1/projects/" + project.ID
	// Changing the UI locale cannot change the persisted built-in name.
	preferences := decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{"expected_version": preferences.Version, "locale": "en-US"}, auth, http.StatusOK)
	got := decodeData[domain.Project](t, request(t, router, http.MethodGet, path, nil, auth, http.StatusOK))
	if !got.IsDefault || got.Name != "默认项目" {
		t.Fatalf("default project GET/localization: %+v", got)
	}
	for _, response := range []testResponse{
		request(t, router, http.MethodDelete, path+"?expected_version=1", nil, auth, http.StatusForbidden),
		request(t, router, http.MethodPatch, path, map[string]any{"expected_version": 1, "name": "renamed"}, auth, http.StatusForbidden),
	} {
		if response.Error == nil || response.Error.Code != "protected_project" {
			t.Fatalf("expected protected_project, got %+v", response.Error)
		}
	}
	updated := decodeData[domain.Project](t, request(t, router, http.MethodPatch, path, map[string]any{
		"expected_version": 1, "description": "normal update", "name": "默认项目",
	}, auth, http.StatusOK))
	if !updated.IsDefault || updated.Description != "normal update" || updated.Name != "默认项目" || updated.Version != 2 {
		t.Fatalf("default PATCH: %+v", updated)
	}
	request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computerID, "name": "forged", "is_default": true,
	}, auth, http.StatusBadRequest)
	request(t, router, http.MethodPatch, path, map[string]any{
		"expected_version": updated.Version, "is_default": false,
	}, auth, http.StatusBadRequest)
	ordinary := decodeData[domain.Project](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computerID, "name": "默认项目",
	}, auth, http.StatusCreated))
	if ordinary.IsDefault || ordinary.ID == project.ID {
		t.Fatalf("ordinary name treated as default: %+v", ordinary)
	}
	request(t, router, http.MethodDelete, fmt.Sprintf("/v1/projects/%s?expected_version=%d", ordinary.ID, ordinary.Version), nil, auth, http.StatusNoContent)
	other := createTestIdentity(t, repository, "default-project-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	for _, response := range []testResponse{
		request(t, router, http.MethodGet, path, nil, otherAuth, http.StatusNotFound),
		request(t, router, http.MethodGet, "/v1/projects?computer_id="+computerID, nil, otherAuth, http.StatusNotFound),
		request(t, router, http.MethodPatch, path, map[string]any{"expected_version": 2, "name": "intruder"}, otherAuth, http.StatusNotFound),
		request(t, router, http.MethodDelete, path+"?expected_version=2", nil, otherAuth, http.StatusNotFound),
	} {
		if response.Error == nil || response.Error.Code != "not_found" {
			t.Fatalf("ownership protection leaked built-in status: %+v", response.Error)
		}
	}
	issue := decodeData[domain.Issue](t, request(t, router, http.MethodPost, path+"/issues", map[string]any{"title": "Default issue"}, auth, http.StatusCreated))
	resource := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, path+"/resources", map[string]any{"kind": "url", "name": "Default URL", "url": "https://example.invalid"}, auth, http.StatusCreated))
	memory := decodeData[domain.ProjectMemoryEntry](t, request(t, router, http.MethodPost, path+"/memories", map[string]any{"key": "fact", "kind": "fact", "title": "Default memory", "content": "retained", "source_type": "user"}, auth, http.StatusCreated))
	if issue.ProjectID != project.ID || resource.ProjectID != project.ID || memory.ProjectID != project.ID {
		t.Fatal("default project business data lost its real project association")
	}
}
