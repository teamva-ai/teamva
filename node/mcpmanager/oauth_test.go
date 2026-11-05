package mcpmanager

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func TestOAuthPKCECallbackRefreshEffectiveAndRevoke(t *testing.T) {
	var base string
	var challenge string
	var mu sync.Mutex
	var refreshes atomic.Int32
	sdk := fixtureServer()
	handler := mcpHTTPFixture(sdk)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"read"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_secret") != "" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("resource") != base+"/mcp" {
			t.Error("invalid OAuth authorization request")
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		challenge = q.Get("code_challenge")
		mu.Unlock()
		redirect, _ := url.Parse(q.Get("redirect_uri"))
		params := redirect.Query()
		params.Set("state", q.Get("state"))
		params.Set("code", "fixture-code")
		redirect.RawQuery = params.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "fixture-client-secret" {
			http.Error(w, "invalid", 400)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			mu.Lock()
			expected := challenge
			mu.Unlock()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(hash[:]) != expected || r.Form.Get("code") != "fixture-code" {
				t.Error("PKCE was not verified")
				http.Error(w, "invalid", 400)
				return
			}
			writeJSON(w, map[string]any{"access_token": "fixture-oauth-access", "token_type": "Bearer", "refresh_token": "fixture-oauth-refresh", "expires_in": 3600})
		case "refresh_token":
			if r.Form.Get("resource") != base+"/mcp" {
				t.Error("refresh omitted resource indicator")
				http.Error(w, "invalid", 400)
				return
			}
			if r.Form.Get("refresh_token") != "fixture-oauth-refresh" {
				http.Error(w, "invalid", 400)
				return
			}
			refreshes.Add(1)
			writeJSON(w, map[string]any{"access_token": "fixture-oauth-refreshed", "token_type": "Bearer", "refresh_token": "fixture-rotated-refresh", "expires_in": 3600})
		default:
			http.Error(w, "invalid", 400)
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if authorization != "Bearer fixture-oauth-access" && authorization != "Bearer fixture-oauth-refreshed" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	base = ts.URL
	m := newTestManager(t)
	v := saveJSON(t, m, "user", "", "oauth-fixture", map[string]any{"type": "remote", "url": base + "/mcp", "headers": map[string]string{"Authorization": "Bearer manually-configured"}, "oauth": map[string]string{"client_id": "fixture-client", "client_secret": "fixture-client-secret"}})
	auth, err := m.StartAuth(context.Background(), "user", "", "oauth-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auth.AuthorizationURL, "fixture-client-secret") || auth.Status != "auth_pending" {
		t.Fatal("authorization URL secret")
	}
	u, _ := url.Parse(auth.AuthorizationURL)
	callback := u.Query().Get("redirect_uri")
	cb, _ := url.Parse(callback)
	if cb.Hostname() != "127.0.0.1" || cb.Port() == "" {
		t.Fatal("callback not ephemeral loopback")
	}
	// Forged state does not consume the valid flow.
	resp, err := http.Get(callback + "?code=forged&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("forged state accepted")
	}
	resp, err = http.Get(auth.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitView(t, m, "oauth-fixture", func(v View) bool { return v.Status == "connected" && v.Authenticated })
	list, _ := m.List(context.Background(), "")
	v = list[0]
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "fixture-oauth-") || strings.Contains(string(b), "fixture-client-secret") {
		t.Fatal("OAuth credential leaked")
	}
	eff, err := m.Effective(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eff[v.Name]), "Bearer fixture-oauth-access") || !strings.Contains(string(eff[v.Name]), `"oauth":false`) {
		t.Fatal("Provider did not receive authorized in-memory snapshot")
	}
	// Force expiration without wall-clock delays; the refresh endpoint is real.
	m.mu.Lock()
	e := m.entries[key("user", "", v.Name)]
	e.Token.Token.Expiry = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	eff, err = m.Effective(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 || !strings.Contains(string(eff[v.Name]), "Bearer fixture-oauth-refreshed") {
		t.Fatal("token refresh failed")
	}
	// Non-identity edits disconnect but preserve the completed grant, including
	// the UI's masked headers/client_secret round trip.
	current, _ := m.List(context.Background(), "")
	draft := current[0].Input
	obj, _ := decodeConfig(draft.Config)
	obj["timeout"] = map[string]any{"catalog": 1234}
	obj["codemode"] = true
	obj["protocol"] = "auto"
	obj["disabled"] = true
	draft.Config, _ = json.Marshal(obj)
	saved, err := m.Save(context.Background(), draft)
	if err != nil || !saved.Authenticated || saved.Status != "disabled" {
		t.Fatal("non-auth edit discarded grant", err)
	}
	obj, _ = decodeConfig(saved.Config)
	obj["disabled"] = false
	saved.Config, _ = json.Marshal(obj)
	saved, err = m.Save(context.Background(), saved.Input)
	if err != nil || !saved.Authenticated || saved.Status != "disconnected" {
		t.Fatal("enable edit discarded grant", err)
	}
	eff, err = m.Effective(context.Background(), "")
	if err != nil || !strings.Contains(string(eff[v.Name]), "Bearer fixture-oauth-refreshed") {
		t.Fatal("non-auth edit lost authorized snapshot")
	}
	// A restarted manager restores only the private grant, not a live session.
	dir := m.dir
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = New(Config{DataDir: filepath.Dir(dir)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	restored, _ := m.List(context.Background(), "")
	if len(restored) != 1 || restored[0].Status != "disconnected" || !restored[0].Authenticated {
		t.Fatal("restart lost grant or auto-connected")
	}
	eff, err = m.Effective(context.Background(), "")
	if err != nil || !strings.Contains(string(eff[v.Name]), "Bearer fixture-oauth-refreshed") {
		t.Fatal("restart lost private provider snapshot")
	}
	// The callback port is no longer exposed after successful authorization.
	client := &http.Client{Timeout: time.Second}
	if r, e := client.Get(callback); e == nil {
		r.Body.Close()
		t.Fatal("callback listener leaked")
	}
	if _, err = m.RevokeAuth(context.Background(), "user", "", v.Name); err != nil {
		t.Fatal(err)
	}
	eff, err = m.Effective(context.Background(), "")
	if err != nil || !strings.Contains(string(eff[v.Name]), "manually-configured") || strings.Contains(string(eff[v.Name]), "fixture-oauth-") {
		t.Fatal("revoke removed user header or retained token")
	}
}

func TestOAuthEndpointPolicyAndShutdown(t *testing.T) {
	for _, s := range []string{"http://external.example/metadata", "http://localhost/metadata", "https://u:p@example.com", "https://example.com?client_secret=value", "file:///secret"} {
		if oauthURL(s) == nil {
			t.Fatalf("unsafe OAuth URL accepted: %s", s)
		}
	}
	if oauthURL("http://127.0.0.1:1234/metadata") != nil || oauthURL("https://auth.example/metadata") != nil {
		t.Fatal("valid OAuth URL rejected")
	}
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	base = ts.URL
	m := newTestManager(t)
	saveJSON(t, m, "user", "", "shutdown", map[string]any{"type": "remote", "url": base + "/mcp", "oauth": map[string]string{"client_id": "fixture"}})
	ar, err := m.StartAuth(context.Background(), "user", "", "shutdown")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ar.AuthorizationURL)
	callback := u.Query().Get("redirect_uri")
	m.Close()
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(callback); err == nil {
		resp.Body.Close()
		t.Fatal("shutdown left callback socket open")
	}
}

func TestOAuthDynamicRegistrationAndMetadataOverride(t *testing.T) {
	var base string
	var registered atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/custom-metadata", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Redirects []string `json:"redirect_uris"`
			Method    string   `json:"token_endpoint_auth_method"`
		}
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Redirects) != 1 || body.Method != "none" {
			t.Error("invalid dynamic registration")
			http.Error(w, "invalid", 400)
			return
		}
		u, _ := url.Parse(body.Redirects[0])
		if u.Hostname() != "127.0.0.1" || u.Port() == "" {
			t.Error("registration redirect is not loopback")
		}
		registered.Store(true)
		writeJSON(w, map[string]any{"client_id": "dynamic-public-client", "redirect_uris": body.Redirects})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	base = ts.URL
	m := newTestManager(t)
	saveJSON(t, m, "user", "", "dcr", map[string]any{"type": "remote", "url": base + "/mcp", "oauth": map[string]string{"auth_server_metadata_url": base + "/custom-metadata"}})
	ar, err := m.StartAuth(context.Background(), "user", "", "dcr")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ar.AuthorizationURL)
	if !registered.Load() || u.Query().Get("client_id") != "dynamic-public-client" {
		t.Fatal("dynamic registration not used")
	}
	callback := u.Query().Get("redirect_uri")
	if _, err = m.RevokeAuth(context.Background(), "user", "", "dcr"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(callback); err == nil {
		resp.Body.Close()
		t.Fatal("revoke left callback listener open")
	}
}

func TestSaveClearsGrantForAuthenticationIdentityChanges(t *testing.T) {
	changes := map[string]func(map[string]any){
		"url":         func(c map[string]any) { c["url"] = "https://other.invalid/mcp" },
		"oauth":       func(c map[string]any) { c["oauth"] = map[string]any{"client_id": "changed"} },
		"headers":     func(c map[string]any) { c["headers"] = map[string]any{"Authorization": "changed"} },
		"environment": func(c map[string]any) { c["environment"] = map[string]any{"TOKEN": "changed"} },
		"transport": func(c map[string]any) {
			for k := range c {
				delete(c, k)
			}
			c["type"] = "local"
			c["command"] = []any{"fixture"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			m := newTestManager(t)
			v := saveJSON(t, m, "user", "", "identity", map[string]any{"type": "remote", "url": "https://mcp.invalid/mcp", "oauth": map[string]any{"client_id": "original"}})
			m.mu.Lock()
			m.entries[key("user", "", "identity")].Token = &storedToken{Token: &oauth2.Token{AccessToken: "fixture-only-unit-token", TokenType: "Bearer"}, TokenURL: "https://auth.invalid/token"}
			m.mu.Unlock()
			c, _ := decodeConfig(v.Config)
			change(c)
			v.Config, _ = json.Marshal(c)
			saved, err := m.Save(context.Background(), v.Input)
			if err != nil || saved.Authenticated {
				t.Fatal("authentication identity edit retained old grant", err)
			}
		})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func mcpHTTPFixture(s *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
}
func waitView(t *testing.T, m *Manager, name string, predicate func(View) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := m.List(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range list {
			if v.Name == name && predicate(v) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	list, _ := m.List(context.Background(), "")
	t.Fatalf("state did not converge: %+v", list)
}
