package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestRemoteLoginBoundaryExpiryAndCapacity(t *testing.T) {
	network := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	probe := func(method, path, body, contentType, requestOrigin, site string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Accept", "text/html")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		network.serveRemote(network.current, w, r)
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("native browser login header emitted")
		}
		return w
	}
	for _, path := range []string{"/remote/login", "/assets/app.js", "/favicon.svg"} {
		if w := probe("GET", path, "", "", "", "", nil); w.Code != 200 {
			t.Fatal("login assets must be public")
		}
	}
	w := probe("GET", "/settings?tab=network", "", "", "", "", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/remote/login?next=%2Fsettings%3Ftab%3Dnetwork" {
		t.Fatal("page navigation must redirect to our login page with its return path")
	}
	for _, path := range []string{"/v1/computers", "/v1/auth/local-session", "/v1/auth/remote-session"} {
		w := probe("GET", path, "", "", "", "", nil)
		if w.Code != 401 || w.Header().Get("Location") != "" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("APIs must return JSON 401, not login HTML")
		}
	}
	body := `{"password":"` + testRemotePassword + `"}`
	for _, requestOrigin := range []string{"", "http://attacker.test", strings.Replace(origin, "http:", "https:", 1)} {
		if w := probe("POST", "/v1/auth/remote-session", body, "application/json", requestOrigin, "", nil); w.Code != 403 {
			t.Fatal("login accepted an invalid origin")
		}
	}
	if w := probe("POST", "/v1/auth/remote-session", body, "application/json", origin, "cross-site", nil); w.Code != 403 {
		t.Fatal("cross-site login accepted")
	}
	if w := probe("POST", "/v1/auth/remote-session", body, "application/x-www-form-urlencoded", origin, "", nil); w.Code != 415 {
		t.Fatal("form login accepted")
	}
	for _, body := range []string{`{"username":"admin","password":"unused"}`, `{} {}`, strings.Repeat("x", 1025)} {
		if w := probe("POST", "/v1/auth/remote-session", body, "application/json", origin, "", nil); w.Code != 400 {
			t.Fatal("invalid or oversized login body accepted")
		}
	}
	w = probe("POST", "/v1/auth/remote-session", body, "application/json", origin, "", nil)
	if w.Code != 204 {
		t.Fatal("password-only login failed")
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != fmt.Sprintf("am_remote_%d", view.Port) || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int(remoteSessionTTL.Seconds()) {
		t.Fatal("remote cookie must be port-scoped, HttpOnly, Strict and time-bounded")
	}
	if w := probe("GET", "/v1/auth/remote-session", "", "", "", "", cookie); w.Code != 204 {
		t.Fatal("valid session rejected")
	}
	// Explicitly replaying a token under a different IP Host must also fail.
	r := httptest.NewRequest("GET", fmt.Sprintf("http://127.0.0.2:%d/readyz", view.Port), nil)
	r.AddCookie(cookie)
	if _, ok := network.authenticateRemote(network.current, r); ok {
		t.Fatal("session not bound to its origin host")
	}

	// Re-login rotates the token rather than fixing a client-provided session.
	w = probe("POST", "/v1/auth/remote-session", body, "application/json", origin, "", cookie)
	if w.Code != 204 {
		t.Fatal("re-login failed")
	}
	fresh := w.Result().Cookies()[0]
	if fresh.Value == cookie.Value {
		t.Fatal("login did not rotate session")
	}
	if w := probe("GET", "/readyz", "", "", "", "", cookie); w.Code != 303 {
		t.Fatal("rotated session remained valid")
	}
	network.mu.Lock()
	digest := sha256.Sum256([]byte(fresh.Value))
	expired := network.sessions[digest]
	expired.expires = time.Now().Add(-time.Second)
	network.sessions[digest] = expired
	network.mu.Unlock()
	if w := probe("GET", "/v1/auth/remote-session", "", "", "", "", fresh); w.Code != 401 {
		t.Fatal("expired session remained valid")
	}

	network.mu.Lock()
	for i := range maxRemoteSessions {
		var key [32]byte
		key[0] = byte(i)
		network.sessions[key] = remoteSession{host: r.Host, expires: time.Now().Add(time.Hour)}
	}
	network.mu.Unlock()
	if w := probe("POST", "/v1/auth/remote-session", body, "application/json", origin, "", nil); w.Code != 429 {
		t.Fatal("session cache capacity must be bounded")
	}
}
