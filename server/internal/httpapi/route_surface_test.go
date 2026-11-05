package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Route enumeration is deliberately independent of manually curated fixtures:
// every newly registered protected operation must pass the same admission gates.
// Positive/stateful handler coverage is measured separately, not inferred here.
func TestEveryProtectedRouteEnforcesCookieAndMutationCSRFOverHTTP(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "surface-contract", true, []string{testBrowserOrigin})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	auth := testLoginHeaders(t, repository, router)
	public := map[string]bool{
		"GET /livez": true, "GET /readyz": true, "GET /v1/health": true,
		"GET /desktop/bootstrap": true, "POST /v1/auth/local-session": true,
	}
	client := server.Client()
	for _, route := range router.Routes() {
		if public[route.Method+" "+route.Path] {
			continue
		}
		segments := strings.Split(route.Path, "/")
		for index, segment := range segments {
			if strings.HasPrefix(segment, ":") {
				segments[index] = "missing-surface-fixture"
			}
		}
		path := strings.Join(segments, "/")
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			send := func(withCookie bool, want int) {
				t.Helper()
				request, err := http.NewRequest(route.Method, server.URL+path, strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Origin", testBrowserOrigin)
				request.Header.Set("Content-Type", "application/json")
				if withCookie {
					request.Header.Set("Cookie", auth["Cookie"])
					request.Header.Set("X-CSRF-Token", "invalid-surface-token")
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				payload, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("admission status=%d want=%d", response.StatusCode, want)
				}
				if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Request-ID") == "" {
					t.Fatal("admission error lacks no-store/request identity")
				}
				var envelope struct {
					Error struct {
						Code      string          `json:"code"`
						Message   string          `json:"message"`
						Retryable *bool           `json:"retryable"`
						Details   json.RawMessage `json:"details"`
					} `json:"error"`
					Meta struct {
						RequestID string `json:"request_id"`
					} `json:"meta"`
				}
				if json.Unmarshal(payload, &envelope) != nil || envelope.Error.Code == "" || envelope.Error.Message == "" || envelope.Error.Retryable == nil || envelope.Meta.RequestID != response.Header.Get("X-Request-ID") {
					t.Fatal("admission response violates ErrorEnvelope")
				}
				if raw := envelope.Error.Details; len(raw) > 0 {
					var object map[string]any
					if json.Unmarshal(raw, &object) != nil || object == nil {
						t.Fatal("optional error.details must be an object, not null")
					}
				}
			}
			send(false, http.StatusUnauthorized)
			if route.Method != http.MethodGet && route.Method != http.MethodHead && route.Method != http.MethodOptions {
				send(true, http.StatusForbidden)
			}
		})
	}
}

func TestProjectListRequiresComputerID(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "surface-contract", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	for _, path := range []string{"/v1/projects", "/v1/projects?computer_id=", "/v1/projects?computer_id=%20%09%E3%80%80"} {
		request(t, router, http.MethodGet, path, nil, auth, http.StatusBadRequest)
	}
	request(t, router, http.MethodGet, "/v1/projects?computer_id="+computerID(t, repository), nil, auth, http.StatusOK)
	request(t, router, http.MethodGet, "/v1/projects?computer_id=missing-surface-computer", nil, auth, http.StatusNotFound)
}
