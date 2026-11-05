package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

const testBrowserOrigin = "http://example.com"

// Bootstrap exercises the real same-origin local admission flow, not a fake login.
func bootstrapViaCookies(t *testing.T, router http.Handler, origin string) (*httptest.ResponseRecorder, string, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/local-session", strings.NewReader("{}"))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Origin", origin)
	router.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	sessionCookie := ""
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie.Value
			if cookie.HttpOnly != true {
				t.Fatal("session cookie must be HttpOnly")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie SameSite=%v", cookie.SameSite)
			}
		}
	}
	if sessionCookie == "" {
		t.Fatal("session cookie not set")
	}
	return recorder, sessionCookie, body.Data.CSRF
}

func cookieRequest(router http.Handler, method, path string, body any, sessionCookie, csrf string, origin string) *httptest.ResponseRecorder {
	return cookieRequestWithSource(router, method, path, body, sessionCookie, csrf, origin, "")
}

func cookieRequestWithSource(
	router http.Handler,
	method, path string,
	body any,
	sessionCookie, csrf, origin, referer string,
) *httptest.ResponseRecorder {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	httpRequest := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionCookie})
	if csrf != "" {
		httpRequest.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		httpRequest.Header.Set("Origin", origin)
	}
	if referer != "" {
		httpRequest.Header.Set("Referer", referer)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httpRequest)
	return recorder
}

func TestSessionCookiesAndCSRFEnforcement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOptions(repository, nil, "test", true, Options{
		AllowedOrigins: []string{testBrowserOrigin},
	})
	_, sessionCookie, csrf := bootstrapViaCookies(t, router, testBrowserOrigin)
	if csrf == "" {
		t.Fatal("login response must include csrf_token")
	}
	// A page reload can restore the CSRF value without rotating it. Multiple
	// tabs therefore remain valid instead of invalidating one another.
	for attempt := 0; attempt < 2; attempt++ {
		restored := cookieRequest(router, http.MethodGet, "/v1/auth/csrf", nil, sessionCookie, "", testBrowserOrigin)
		if restored.Code != http.StatusOK {
			t.Fatalf("restore CSRF failed: %d %s", restored.Code, restored.Body.String())
		}
		var body struct {
			Data struct {
				CSRF string `json:"csrf_token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(restored.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.CSRF != csrf {
			t.Fatal("CSRF restore must return the existing token")
		}
	}

	bootstrap := cookieRequest(router, http.MethodGet, "/v1/auth/bootstrap", nil, sessionCookie, "", testBrowserOrigin)
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("auth bootstrap failed: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	var bootstrapBody struct {
		Data struct {
			CSRF string `json:"csrf_token"`
			User struct {
				Email string `json:"email"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bootstrap.Body.Bytes(), &bootstrapBody); err != nil {
		t.Fatal(err)
	}
	if bootstrapBody.Data.CSRF != csrf || bootstrapBody.Data.User.Email != "local@teamva.invalid" {
		t.Fatalf("auth bootstrap returned incomplete session state: %#v", bootstrapBody.Data)
	}

	prefVersion := func() int64 {
		current := cookieRequest(router, http.MethodGet, "/v1/me/preferences", nil, sessionCookie, "", "")
		if current.Code != http.StatusOK {
			t.Fatalf("read preferences: %d", current.Code)
		}
		var envelope struct {
			Data struct {
				Version int64 `json:"version"`
			} `json:"data"`
		}
		if err := json.Unmarshal(current.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data.Version
	}
	preferences := map[string]any{"locale": "en-US", "theme": "system", "allow_write": true, "expected_version": prefVersion()}
	// state change without CSRF header -> 403
	missing := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", preferences, sessionCookie, "", "")
	if missing.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF, got %d", missing.Code)
	}
	// state change with wrong CSRF -> 403
	wrong := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", preferences, sessionCookie, "wrong-token", "")
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with wrong CSRF, got %d", wrong.Code)
	}
	// cross-origin state change -> 403
	crossOrigin := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", preferences, sessionCookie, csrf, "https://evil.example.com")
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 cross-origin, got %d", crossOrigin.Code)
	}
	// A valid token does not bypass the source check when both Origin and
	// Referer are absent.
	missingSource := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", preferences, sessionCookie, csrf, "")
	if missingSource.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without Origin or Referer, got %d", missingSource.Code)
	}
	// When Origin is unavailable, a trusted Referer origin is accepted while
	// an untrusted Referer is rejected.
	evilReferer := cookieRequestWithSource(
		router, http.MethodPatch, "/v1/me/preferences", preferences,
		sessionCookie, csrf, "", "https://evil.example.com/form",
	)
	if evilReferer.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin Referer, got %d", evilReferer.Code)
	}
	validReferer := cookieRequestWithSource(
		router, http.MethodPatch, "/v1/me/preferences", map[string]any{"locale": "en-US", "theme": "system", "allow_write": true, "expected_version": prefVersion()},
		sessionCookie, csrf, "", testBrowserOrigin+"/app/form?source=test",
	)
	if validReferer.Code != http.StatusOK {
		t.Fatalf("expected 200 with trusted Referer, got %d: %s", validReferer.Code, validReferer.Body.String())
	}
	// same-origin state change with valid CSRF -> 200
	valid := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", map[string]any{"locale": "en-US", "theme": "system", "allow_write": true, "expected_version": prefVersion()}, sessionCookie, csrf, testBrowserOrigin)
	if valid.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid CSRF, got %d: %s", valid.Code, valid.Body.String())
	}
	// GET with cookie but no CSRF is allowed
	read := cookieRequest(router, http.MethodGet, "/v1/me", nil, sessionCookie, "", "")
	if read.Code != http.StatusOK {
		t.Fatalf("expected 200 GET without CSRF, got %d", read.Code)
	}
}

func TestProductionSessionCookieRemainsSameSiteLax(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOptions(repository, nil, "test", true, Options{
		AllowedOrigins: []string{"https://app.teamva.dev"},
		CookieSecure:   true,
	})
	recorder, _, _ := bootstrapViaCookies(t, router, "https://app.teamva.dev")
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("unsafe production cookie: %+v", cookie)
			}
			return
		}
	}
	t.Fatal("session cookie not set")
}

func TestSessionCookieAndIdleWindowSlideForThirtyDays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouter(repository, nil, "test", true)
	login, sessionCookie, _ := bootstrapViaCookies(t, router, testBrowserOrigin)
	assertSessionCookieMaxAge(t, login, int(sessionIdleTTL.Seconds()))
	if bytes.Contains(login.Body.Bytes(), []byte(`"expires_at"`)) {
		t.Fatal("login response must not expose an absolute session expiry")
	}

	activity := cookieRequest(router, http.MethodGet, "/v1/me", nil, sessionCookie, "", "")
	if activity.Code != http.StatusOK {
		t.Fatalf("authenticated activity failed: %d %s", activity.Code, activity.Body.String())
	}
	assertSessionCookieMaxAge(t, activity, int(sessionIdleTTL.Seconds()))
	sessions, err := repository.ListSessions(t.Context(), testLocalUserID(repository, t), "")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %d err=%v", len(sessions), err)
	}
	remaining := time.Until(sessions[0].IdleExpiresAt)
	if remaining < 30*24*time.Hour-time.Minute || remaining > 30*24*time.Hour+time.Minute {
		t.Fatalf("unexpected sliding idle window: %s", remaining)
	}
}

func assertSessionCookieMaxAge(t *testing.T, recorder *httptest.ResponseRecorder, expected int) {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			if cookie.MaxAge != expected {
				t.Fatalf("session cookie MaxAge=%d, want %d", cookie.MaxAge, expected)
			}
			return
		}
	}
	t.Fatal("session cookie not refreshed")
}

func TestBrowserBearerTokenIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouter(repository, nil, "test", true)
	recorder, cookie, _ := bootstrapViaCookies(t, router, testBrowserOrigin)
	if bytes.Contains(recorder.Body.Bytes(), []byte(cookie)) {
		t.Fatal("session secret must never be serialized in the login response")
	}
	request(t, router, http.MethodPost, "/v1/projects", map[string]any{"computer_id": "cmp_x", "name": "b"},
		map[string]string{"Authorization": "Bearer " + cookie}, http.StatusUnauthorized)
}

func TestRevokedCookieCannotReadLocalState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOptions(repository, nil, "test", true, Options{
		AllowedOrigins: []string{"http://example.com"},
	})
	_, sessionCookie, _ := bootstrapViaCookies(t, router, testBrowserOrigin)
	if err := repository.RevokeSession(t.Context(), sessionCookie); err != nil {
		t.Fatal(err)
	}
	me := cookieRequest(router, http.MethodGet, "/v1/me", nil, sessionCookie, "", "")
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session must no longer authenticate: %d", me.Code)
	}
}

func TestRepeatedLocalWritesKeepTheSessionAndCSRFUsable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOptions(repository, nil, "test", true, Options{
		AllowedOrigins: []string{"http://example.com"},
	})
	recorder, _, csrf := bootstrapViaCookies(t, router, testBrowserOrigin)
	firstCookie := ""
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			firstCookie = cookie.Value
		}
	}
	// Verify real SQLite-backed authentication across repeated CSRF writes.
	for i := 0; i < 3; i++ {
		current := cookieRequest(router, http.MethodGet, "/v1/me/preferences", nil, firstCookie, "", "http://example.com")
		if current.Code != http.StatusOK {
			t.Fatalf("read preferences before request %d: %d", i, current.Code)
		}
		var envelope struct {
			Data struct {
				Version int64 `json:"version"`
			} `json:"data"`
		}
		if err := json.Unmarshal(current.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		response := cookieRequest(router, http.MethodPatch, "/v1/me/preferences", map[string]any{"locale": "en-US", "theme": "system", "allow_write": true, "expected_version": envelope.Data.Version}, firstCookie, csrf, "http://example.com")
		if response.Code != http.StatusOK {
			t.Fatalf("session unusable after request %d: %d", i, response.Code)
		}
	}
}

type rotationBoundaryRepository struct {
	store.CoreLocalRepository
	mu              sync.Mutex
	oldToken        string
	currentToken    string
	oldTokenCalls   int
	previousExpired bool
}

func (r *rotationBoundaryRepository) AuthenticateSession(
	_ context.Context, token string, _, _, _ time.Duration,
) (domain.SessionAuthentication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := domain.SessionAuthentication{
		User:    domain.User{Versioned: domain.Versioned{ID: "usr_rotation"}, Status: "active"},
		Session: domain.WebSessionInfo{ID: "ses_rotation"},
	}
	if token == r.currentToken {
		result.TokenMatch = domain.SessionTokenMatchCurrent
		return result, nil
	}
	if token != r.oldToken || r.previousExpired {
		return domain.SessionAuthentication{}, domain.Unauthorized("invalid_session", "登录状态无效或已过期")
	}
	r.oldTokenCalls++
	if r.oldTokenCalls == 1 {
		result.TokenMatch = domain.SessionTokenMatchCurrent
		result.RotatedToken = r.currentToken
	} else {
		result.TokenMatch = domain.SessionTokenMatchPrevious
	}
	return result, nil
}

func (r *rotationBoundaryRepository) expirePrevious() {
	r.mu.Lock()
	r.previousExpired = true
	r.mu.Unlock()
}

func TestConcurrentRotationLatePreviousResponseCannotOverwriteCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &rotationBoundaryRepository{
		oldToken:     "old-session-token",
		currentToken: "rotated-session-token",
	}
	api := &API{store: repository}
	entered := map[string]chan struct{}{
		"rotating": make(chan struct{}, 1),
		"previous": make(chan struct{}, 1),
	}
	release := map[string]chan struct{}{
		"rotating": make(chan struct{}, 1),
		"previous": make(chan struct{}, 1),
	}
	router := gin.New()
	router.GET("/interleaved", authenticate(api), func(c *gin.Context) {
		name := c.GetHeader("X-Test-Request")
		entered[name] <- struct{}{}
		<-release[name]
		c.Status(http.StatusNoContent)
	})
	router.GET("/after-grace", authenticate(api), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	type response struct {
		name     string
		recorder *httptest.ResponseRecorder
	}
	responses := make(chan response, 2)
	sendOld := func(name string) {
		request := httptest.NewRequest(http.MethodGet, "/interleaved", nil)
		request.Header.Set("X-Test-Request", name)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: repository.oldToken})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		responses <- response{name: name, recorder: recorder}
	}
	waitEntered := func(name string) {
		t.Helper()
		select {
		case <-entered[name]:
		case <-time.After(2 * time.Second):
			t.Fatalf("request %s did not enter handler", name)
		}
	}

	go sendOld("rotating")
	waitEntered("rotating") // rotation and its Set-Cookie have happened
	go sendOld("previous")
	waitEntered("previous") // same old token is now explicitly a previous match

	// The rotating response arrives first. The previous-token response arrives
	// last, reproducing the browser ordering that used to restore the old cookie.
	release["rotating"] <- struct{}{}
	first := <-responses
	if first.name != "rotating" {
		t.Fatalf("first response=%s, want rotating", first.name)
	}
	rotatedCookie := sessionCookieValue(first.recorder)
	if rotatedCookie != repository.currentToken {
		t.Fatalf("rotating response cookie=%q, want current token", rotatedCookie)
	}
	release["previous"] <- struct{}{}
	last := <-responses
	if last.name != "previous" {
		t.Fatalf("last response=%s, want previous", last.name)
	}
	if value := sessionCookieValue(last.recorder); value != "" {
		t.Fatalf("late previous-token response wrote stale cookie %q", value)
	}

	// Model the 30-second overlap expiring. Applying responses in arrival order
	// leaves the browser on the rotated token, so it remains authenticated.
	repository.expirePrevious()
	current := cookieRequest(router, http.MethodGet, "/after-grace", nil, rotatedCookie, "", "")
	if current.Code != http.StatusNoContent {
		t.Fatalf("rotated cookie failed after grace: %d %s", current.Code, current.Body.String())
	}
	stale := cookieRequest(router, http.MethodGet, "/after-grace", nil, repository.oldToken, "", "")
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie unexpectedly survived grace: %d", stale.Code)
	}
}

func sessionCookieValue(recorder *httptest.ResponseRecorder) string {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge >= 0 {
			return cookie.Value
		}
	}
	return ""
}
