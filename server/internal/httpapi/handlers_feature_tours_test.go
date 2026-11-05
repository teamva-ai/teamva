package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestFeatureToursAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	get := func(headers map[string]string) domain.FeatureTours {
		return decodeData[domain.FeatureTours](t, request(t, router, http.MethodGet, "/v1/me/feature-tours", nil, headers, http.StatusOK))
	}
	if view := get(auth); view.Seen == nil || len(view.Seen) != 0 {
		t.Fatalf("fresh user=%+v", view)
	}
	for i, id := range domain.FeatureTourIDs() {
		for range 2 {
			response := request(t, router, http.MethodPost, "/v1/me/feature-tours/"+id+"/seen", map[string]any{}, auth, http.StatusOK)
			view := decodeData[domain.FeatureTours](t, response)
			if !slices.Equal(view.Seen, domain.FeatureTourIDs()[:i+1]) || !slices.Equal(get(auth).Seen, view.Seen) {
				t.Fatalf("mark %s=%+v", id, view)
			}
			var envelope struct {
				Meta map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(response.Recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if _, ok := envelope.Meta["resource_version"]; ok {
				t.Fatal("tour marks must not expose a preference version lock")
			}
		}
	}
	// A new browser session for the same user inherits the persisted set.
	freshBrowserAuth := testLoginHeaders(t, repository, router)
	if !slices.Equal(get(freshBrowserAuth).Seen, domain.FeatureTourIDs()) {
		t.Fatal("new browser session reset tours")
	}
	other := createTestIdentity(t, repository, "tour-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	if view := get(otherAuth); len(view.Seen) != 0 {
		t.Fatalf("other user inherited marks: %+v", view)
	}
	request(t, router, http.MethodPost, "/v1/me/feature-tours/issues/seen?user_id="+testLocalUserID(repository, t), map[string]any{}, otherAuth, http.StatusOK)
	if !slices.Equal(get(otherAuth).Seen, []string{"issues"}) || !slices.Equal(get(auth).Seen, domain.FeatureTourIDs()) {
		t.Fatal("tour state crossed user boundary")
	}
}

func TestFeatureToursAPIValidationAndSecurity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	path := "/v1/me/feature-tours/overview/seen"
	request(t, router, http.MethodGet, "/v1/me/feature-tours", nil, nil, http.StatusUnauthorized)
	request(t, router, http.MethodPost, path, map[string]any{}, nil, http.StatusUnauthorized)
	auth := testLoginHeaders(t, repository, router)
	request(t, router, http.MethodPost, path, map[string]any{}, map[string]string{"Cookie": auth["Cookie"], "Origin": testBrowserOrigin}, http.StatusForbidden)
	request(t, router, http.MethodPost, path, map[string]any{}, map[string]string{"Cookie": auth["Cookie"], "X-CSRF-Token": auth["X-CSRF-Token"]}, http.StatusForbidden)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for name, value := range auth {
		req.Header.Set(name, value)
	}
	req.Header.Set("Origin", "http://evil.example.test")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mark=%d", recorder.Code)
	}
	for _, id := range []string{"unknown", "Overview", "overview.issues"} {
		response := request(t, router, http.MethodPost, "/v1/me/feature-tours/"+id+"/seen", map[string]any{}, auth, http.StatusBadRequest)
		if response.Error == nil || response.Error.Code != "invalid_argument" {
			t.Fatalf("unknown id=%+v", response.Error)
		}
	}
	for _, body := range []any{nil, json.RawMessage(`null`), []any{}, "overview", map[string]any{"seen": true}, map[string]any{"user_id": "other"}, map[string]any{"expected_version": 1}} {
		request(t, router, http.MethodPost, path, body, auth, http.StatusBadRequest)
	}
	view := decodeData[domain.FeatureTours](t, request(t, router, http.MethodGet, "/v1/me/feature-tours", nil, auth, http.StatusOK))
	if len(view.Seen) != 0 {
		t.Fatalf("rejected requests changed tours: %+v", view)
	}
	userID := testLocalUserID(repository, t)
	var count int
	if err := repository.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key=?`, "feature_tours:"+userID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected requests backfilled state: count=%d err=%v", count, err)
	}
	if _, err := repository.db.Exec(`UPDATE users SET deleted_at='2026-10-28T00:00:00Z' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodGet, "/v1/me/feature-tours", nil, auth, http.StatusUnauthorized)
	request(t, router, http.MethodPost, path, map[string]any{}, auth, http.StatusUnauthorized)
}

func TestFeatureToursAPICorruptMetadataFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	key := "feature_tours:" + testLocalUserID(repository, t)
	const corrupt = `{"overview":true,"issues":false}`
	if _, err := repository.db.Exec(`INSERT INTO app_metadata(key, value, updated_at) VALUES (?, ?, 'unchanged')`, key, corrupt); err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodGet, "/v1/me/feature-tours", nil, auth, http.StatusInternalServerError)
	request(t, router, http.MethodPost, "/v1/me/feature-tours/issues/seen", map[string]any{}, auth, http.StatusInternalServerError)
	var value, timestamp string
	if err := repository.db.QueryRow(`SELECT value, updated_at FROM app_metadata WHERE key=?`, key).Scan(&value, &timestamp); err != nil || value != corrupt || timestamp != "unchanged" {
		t.Fatalf("failed mark overwrote metadata: value=%s timestamp=%s err=%v", value, timestamp, err)
	}
}
