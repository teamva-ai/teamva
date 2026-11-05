package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestProjectsRejectAndNeverExposeColors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computerID := computerID(t, repository)
	for _, color := range []any{"hallmark", "chamber", "flexoki", "default", "", nil} {
		response := request(t, router, http.MethodPost, "/v1/projects", map[string]any{
			"computer_id": computerID, "name": "rejected", "theme": color,
		}, auth, http.StatusBadRequest)
		if response.Error == nil || response.Error.Code != "invalid_argument" {
			t.Fatalf("removed theme create error=%+v", response.Error)
		}
	}
	createdResponse := request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computerID, "name": "global colors only", "description": "notes",
	}, auth, http.StatusCreated)
	created := decodeData[domain.Project](t, createdResponse)
	for _, id := range []string{created.ID, domain.DefaultProjectID(computerID)} {
		path := "/v1/projects/" + id
		beforeResponse := request(t, router, http.MethodGet, path, nil, auth, http.StatusOK)
		before := decodeData[domain.Project](t, beforeResponse)
		for _, color := range []any{"hallmark", "chamber", "flexoki", "default", "", nil} {
			response := request(t, router, http.MethodPatch, path, map[string]any{
				"expected_version": before.Version, "theme": color, "description": "must not save",
			}, auth, http.StatusBadRequest)
			if response.Error == nil || response.Error.Code != "invalid_argument" {
				t.Fatalf("removed theme update error=%+v", response.Error)
			}
		}
		after := decodeData[domain.Project](t, request(t, router, http.MethodGet, path, nil, auth, http.StatusOK))
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("removed color request changed project: before=%+v after=%+v", before, after)
		}
		updated := request(t, router, http.MethodPatch, path, map[string]any{
			"expected_version": before.Version, "description": "updated",
		}, auth, http.StatusOK)
		for _, response := range []testResponse{createdResponse, beforeResponse, updated} {
			fields := decodeData[map[string]any](t, response)
			if _, exists := fields["theme"]; exists {
				t.Fatalf("project HTTP response exposes theme: %+v", fields)
			}
		}
	}
	page := decodeData[struct {
		Items []map[string]any `json:"items"`
	}](t, request(t, router, http.MethodGet, "/v1/projects?computer_id="+computerID, nil, auth, http.StatusOK))
	if len(page.Items) != 2 {
		t.Fatalf("rejected requests created projects: %+v", page.Items)
	}
	for _, item := range page.Items {
		if _, exists := item["theme"]; exists {
			t.Fatalf("project list exposes theme: %+v", item)
		}
	}
}
