package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// External access is opt-in at the listener, which authenticates remote peers
// with the access token. This test fixes the HTTP-layer contract that remains:
// a remote peer may establish a session and mutate only from its own origin,
// while loopback behavior is unchanged.
func TestExternalAccessRequiresExactOriginForSessionAndMutations(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouterWithOptions(repository, nil, "test", true, Options{
		AllowedOrigins: []string{"http://127.0.0.1:1234"},
		WebOrigin:      "http://127.0.0.1:1234",
		ExternalAccess: true,
	})

	const (
		externalHost   = "192.168.50.10:1234"
		externalOrigin = "http://192.168.50.10:1234"
		remoteAddr     = "192.168.50.20:5000"
	)
	do := func(method, path, origin, body, cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == "" {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader([]byte(body))
		}
		request := httptest.NewRequest(method, path, reader)
		request.Host = externalHost
		request.RemoteAddr = remoteAddr
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	foreign := do(http.MethodPost, "/v1/auth/local-session", "http://attacker.test", "{}", "", "")
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin session = %d, want 403", foreign.Code)
	}
	created := do(http.MethodPost, "/v1/auth/local-session", externalOrigin, "{}", "", "")
	if created.Code != http.StatusOK {
		t.Fatalf("same-origin session = %d: %s", created.Code, created.Body.String())
	}
	var bootstrap struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &bootstrap); err != nil || bootstrap.Data.CSRFToken == "" {
		t.Fatalf("session bootstrap=%s err=%v", created.Body.String(), err)
	}
	var sessionCookie string
	for _, cookie := range created.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie.Value
		}
	}
	if sessionCookie == "" {
		t.Fatal("external session cookie was not set")
	}

	preferences := do(http.MethodGet, "/v1/me/preferences", externalOrigin, "", sessionCookie, "")
	if preferences.Code != http.StatusOK {
		t.Fatalf("external read = %d: %s", preferences.Code, preferences.Body.String())
	}
	mutation := `{"expected_version":1,"theme":"dark"}`
	allowed := do(http.MethodPatch, "/v1/me/preferences", externalOrigin, mutation, sessionCookie, bootstrap.Data.CSRFToken)
	if allowed.Code != http.StatusOK {
		t.Fatalf("external same-origin mutation = %d: %s", allowed.Code, allowed.Body.String())
	}
	evil := do(http.MethodPatch, "/v1/me/preferences", "http://attacker.test", mutation, sessionCookie, bootstrap.Data.CSRFToken)
	if evil.Code != http.StatusForbidden {
		t.Fatalf("external foreign-origin mutation = %d, want 403", evil.Code)
	}
}
