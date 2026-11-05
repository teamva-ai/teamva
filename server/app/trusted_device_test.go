package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/store"
)

func startTrustTestLocal(t *testing.T, directory string) *Local {
	t.Helper()
	local, err := StartLocal(t.Context(), LocalConfig{DataDir: directory, DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	return local
}

func trustLogin(t *testing.T, client *http.Client, origin, password string, trust bool, alreadyTrusted ...bool) *http.Cookie {
	t.Helper()
	response := remoteProbe(t, client, "POST", origin, "/v1/auth/remote-session", "", map[string]any{"password": password, "trust_device": trust})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("login failed: status %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected a single remote cookie")
	}
	ttl := remoteSessionTTL
	if trust || (len(alreadyTrusted) > 0 && alreadyTrusted[0]) {
		ttl = trustedCookieLifetime
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge != int(ttl.Seconds()) || cookies[0].Domain != "" {
		t.Fatal("incorrect trusted device cookie attributes")
	}
	return cookies[0]
}

func TestTrustedDeviceSurvivesRestartWithoutPassword(t *testing.T) {
	directory := t.TempDir()
	local := startTrustTestLocal(t, directory)
	view, err := local.network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := view.ExternalURLs[0]
	trusted, ordinary := newTestClient(t), newTestClient(t)
	cookie := trustLogin(t, trusted, origin, testRemotePassword, true)
	trustLogin(t, ordinary, origin, testRemotePassword, false)
	config, err := local.repository.NetworkAccess(t.Context())
	if err != nil || len(config.TrustedDevices) != 1 {
		t.Fatal("only the opted-in device should be persisted")
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	if config.TrustedDevices[0].TokenHash != hex.EncodeToString(digest[:]) {
		t.Fatal("store must contain only the digest of a random trusted credential")
	}
	encoded, _ := json.Marshal(config)
	if strings.Contains(string(encoded), "expires_at") {
		t.Fatal("remembered devices must have no stored expiration")
	}
	public, _ := json.Marshal(local.network.View())
	if strings.Contains(string(encoded), cookie.Value) || strings.Contains(string(encoded), testRemotePassword) || strings.Contains(string(public), "token_hash") || strings.Contains(string(public), "trusted_devices") {
		t.Fatal("trusted credentials leaked into storage or public settings")
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	local = startTrustTestLocal(t, directory)
	if local.network.View().Port != view.Port {
		t.Fatal("remote port not restored")
	}
	requireRemoteStatus(t, remoteProbe(t, trusted, "GET", origin, "/v1/auth/remote-session", "", nil), http.StatusNoContent)
	requireRemoteStatus(t, remoteProbe(t, trusted, "GET", origin, "/readyz", "", nil), http.StatusOK)
	requireRemoteStatus(t, remoteProbe(t, ordinary, "GET", origin, "/readyz", "", nil), http.StatusUnauthorized)
	requireRemoteStatus(t, remoteProbe(t, newTestClient(t), "GET", origin, "/readyz", "", nil), http.StatusUnauthorized)
	// A trusted browser can bootstrap a fresh business session without sending
	// its password again, including after the application's restart.
	requireRemoteStatus(t, remoteProbe(t, trusted, "POST", origin, "/v1/auth/local-session", "", map[string]any{}), http.StatusOK)
}

func TestOnlyPasswordChangeRevokesTrustedDevices(t *testing.T) {
	for _, action := range []string{"password", "disable", "port", "uncheck", "same-password", "disabled-restart"} {
		t.Run(action, func(t *testing.T) {
			directory := t.TempDir()
			local := startTrustTestLocal(t, directory)
			view, err := local.network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
			if err != nil {
				t.Fatal(err)
			}
			origin := view.ExternalURLs[0]
			client := newTestClient(t)
			cookie := trustLogin(t, client, origin, testRemotePassword, true)
			replay := newTestClient(t)
			parsed, _ := url.Parse(origin)
			replay.Jar.SetCookies(parsed, []*http.Cookie{cookie})
			switch action {
			case "password":
				_, err = local.network.Update(t.Context(), remoteUpdate(true, "test-only-new-password"))
			case "same-password":
				_, err = local.network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
			case "disable":
				_, err = local.network.Update(t.Context(), remoteUpdate(false, ""))
				if err == nil {
					_, err = local.network.Update(t.Context(), remoteUpdate(true, ""))
				}
			case "port":
				// Remembered cookies from the previous port remain valid for this
				// same service/IP, not for another service sharing that IP.
				reserved, reserveErr := net.Listen("tcp4", "127.0.0.1:0")
				if reserveErr != nil {
					t.Fatal(reserveErr)
				}
				address := reserved.Addr().String()
				_ = reserved.Close()
				if err = local.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				local, err = StartLocal(t.Context(), LocalConfig{DataDir: directory, Address: address, DisableRuntime: true})
				if err != nil {
					t.Fatal(err)
				}
				newOrigin := local.network.View().ExternalURLs[0]
				requireRemoteStatus(t, remoteProbe(t, client, "GET", newOrigin, "/readyz", "", nil), http.StatusOK)
				requireRemoteStatus(t, remoteProbe(t, replay, "GET", newOrigin, "/readyz", "", nil), http.StatusOK)
				if err = local.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				local, err = StartLocal(t.Context(), LocalConfig{DataDir: directory, Address: fmt.Sprintf("127.0.0.1:%d", view.Port), DisableRuntime: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = local.Close(context.Background()) })
			case "uncheck":
				// Re-login rotates the token, but does not forget a device that
				// was explicitly remembered earlier.
				trustLogin(t, client, origin, testRemotePassword, false, true)
				requireRemoteStatus(t, remoteProbe(t, replay, "GET", origin, "/readyz", "", nil), http.StatusUnauthorized)
			case "disabled-restart":
				if _, err = local.network.Update(t.Context(), remoteUpdate(false, "")); err != nil {
					t.Fatal(err)
				}
				if err = local.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				local = startTrustTestLocal(t, directory)
				if local.network.View().ExternalAccess {
					t.Fatal("restart unexpectedly enabled LAN")
				}
				_, err = local.network.Update(t.Context(), remoteUpdate(true, ""))
			}
			if err != nil {
				t.Fatal(err)
			}
			config, err := local.repository.NetworkAccess(t.Context())
			want := 1
			if action == "password" {
				want = 0
			}
			if err != nil || len(config.TrustedDevices) != want {
				t.Fatal("only password changes may revoke persistent trust")
			}
			if err := local.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			startTrustTestLocal(t, directory)
			status := http.StatusOK
			if action == "password" {
				status = http.StatusUnauthorized
			}
			requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/readyz", "", nil), status)
			if action == "uncheck" {
				status = http.StatusUnauthorized
			}
			requireRemoteStatus(t, remoteProbe(t, replay, "GET", origin, "/readyz", "", nil), status)
		})
	}
}

func TestTrustedDeviceWriteFailureDoesNotReplaceSessionOrRevokeTrust(t *testing.T) {
	repository := &networkStoreStub{}
	network := testNetworkAccess(t, repository, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	client := newTestClient(t)
	trustLogin(t, client, origin, testRemotePassword, true)
	before := repository.config
	network.mu.Lock()
	repository.err = errors.New("test-only persistence failure")
	network.mu.Unlock()
	for _, trust := range []bool{true, false} {
		response := remoteProbe(t, client, "POST", origin, "/v1/auth/remote-session", "", map[string]any{"password": testRemotePassword, "trust_device": trust})
		if len(response.Cookies()) != 0 {
			t.Fatal("failed persistence must not issue a replacement cookie")
		}
		requireRemoteStatus(t, response, http.StatusInternalServerError)
		requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/readyz", "", nil), http.StatusOK)
	}
	if _, err := network.Update(t.Context(), remoteUpdate(false, "")); err == nil {
		t.Fatal("failed trust revocation accepted")
	}
	if !reflect.DeepEqual(repository.config, before) || !reflect.DeepEqual(network.config, before) {
		t.Fatal("failed persistence changed live trusted configuration")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/readyz", "", nil), http.StatusOK)
}

func TestConcurrentTrustedLoginsCannotSurvivePasswordChange(t *testing.T) {
	repository := &networkStoreStub{}
	network := testNetworkAccess(t, repository, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	var group sync.WaitGroup
	clients := make([]*http.Client, 6)
	for index := range clients {
		clients[index] = newTestClient(t)
		client := clients[index]
		group.Go(func() {
			body, _ := json.Marshal(map[string]any{"password": testRemotePassword, "trust_device": true})
			request, _ := http.NewRequest("POST", origin+"/v1/auth/remote-session", strings.NewReader(string(body)))
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				return
			} // Rotation intentionally disconnects in-flight remote connections.
			defer response.Body.Close()
			if response.StatusCode != 204 && response.StatusCode != 401 && response.StatusCode != 429 {
				t.Errorf("unexpected login status %d", response.StatusCode)
			}
		})
	}
	if _, err := network.Update(t.Context(), remoteUpdate(true, "test-only-race-new-password")); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	if len(repository.config.TrustedDevices) != 0 {
		t.Fatal("a racing login reintroduced revoked trust")
	}
	for _, client := range clients {
		requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/readyz", "", nil), 401)
	}
}

func TestPermanentTrustHasNoDeadlineAndRenewsBrowserCookie(t *testing.T) {
	network := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, deadline := r.Context().Deadline()
		w.Header().Set("X-Test-Deadline", fmt.Sprint(deadline))
		w.WriteHeader(http.StatusOK)
	}))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	trusted, ordinary := newTestClient(t), newTestClient(t)
	cookie := trustLogin(t, trusted, origin, testRemotePassword, true)
	trustLogin(t, ordinary, origin, testRemotePassword, false)
	digest := sha256.Sum256([]byte(cookie.Value))
	network.mu.Lock()
	session := network.sessions[digest]
	initialSession := session
	// Even a past ordinary-session deadline must never expire permanent trust.
	session.expires = time.Now().Add(-10 * 365 * 24 * time.Hour)
	network.sessions[digest] = session
	network.mu.Unlock()
	if !initialSession.trusted || !initialSession.expires.IsZero() {
		t.Fatal("trusted session has an expiration")
	}
	for _, path := range []string{"/readyz", "/v1/auth/remote-session"} {
		response := remoteProbe(t, trusted, "GET", origin, path, "", nil)
		renewed := response.Cookies()
		if len(renewed) != 1 || renewed[0].Value != cookie.Value || renewed[0].MaxAge != int(trustedCookieLifetime.Seconds()) || !renewed[0].HttpOnly {
			t.Fatal("trusted cookie was not securely renewed")
		}
		if path == "/readyz" && response.Header.Get("X-Test-Deadline") != "false" {
			t.Fatal("permanent SSE/request got an expiration deadline")
		}
		_ = response.Body.Close()
	}
	response := remoteProbe(t, ordinary, "GET", origin, "/readyz", "", nil)
	if response.Header.Get("X-Test-Deadline") != "true" || len(response.Cookies()) != 0 {
		t.Fatal("ordinary session unexpectedly became permanent")
	}
	requireRemoteStatus(t, response, http.StatusOK)
	other := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	other.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	otherView, err := other.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	requireRemoteStatus(t, remoteProbe(t, trusted, "GET", fmt.Sprintf("http://127.0.0.1:%d", otherView.Port), "/readyz", "", nil), http.StatusUnauthorized)
}
