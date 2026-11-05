package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSUsesExplicitPagesOrigin(t *testing.T) {
	router := newRouterWithOrigins(
		newTestRepository(t), nil, "test", false,
		[]string{"https://app.teamva.example"},
	)
	request := httptest.NewRequest(http.MethodOptions, "/v1/projects", nil)
	request.Header.Set("Origin", "https://app.teamva.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://app.teamva.example" {
		t.Fatalf("unexpected allow origin: %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
	if response.Header().Get("Access-Control-Expose-Headers") != "ETag, X-Request-ID" {
		t.Fatalf("unexpected exposed headers: %q", response.Header().Get("Access-Control-Expose-Headers"))
	}

	request = httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted origin response: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestCORSRejectsNonOriginAllowlistValues(t *testing.T) {
	for name, configured := range map[string]string{
		"wildcard": "*",
		"opaque":   "null",
		"path":     "https://app.teamva.example/path",
	} {
		t.Run(name, func(t *testing.T) {
			router := newRouterWithOrigins(
				newTestRepository(t), nil, "test", false,
				[]string{configured},
			)
			request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
			request.Header.Set("Origin", "https://app.teamva.example")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("unsafe allowlist value %q was accepted", configured)
			}
		})
	}
}
