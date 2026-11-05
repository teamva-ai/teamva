package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/httpapi"
	"github.com/teamva-ai/teamva/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Synthetic test passwords only; never supplied by environment or real users.
const testRemotePassword = "test-only-remote-password"

func remoteUpdate(enabled bool, password string) httpapi.NetworkAccessUpdate {
	return httpapi.NetworkAccessUpdate{Enabled: &enabled, Password: password}
}

// Unit tests simulate the LAN socket on loopback; full Local integration tests
// below use real interface addresses alongside the passwordless local socket.
func testNetworkAccess(t *testing.T, repository networkAccessStore, config store.NetworkAccessConfig) *networkAccess {
	t.Helper()
	network, err := newNetworkAccess(repository, config)
	if err != nil {
		t.Fatal(err)
	}
	network.addresses = func(port string) []string { return []string{"http://127.0.0.1:" + port} }
	t.Cleanup(network.Close)
	return network
}

func remoteProbe(t *testing.T, client *http.Client, method, origin, path, csrf string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, origin+path, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func remoteLogin(t *testing.T, client *http.Client, origin, password string, status int) {
	t.Helper()
	response := remoteProbe(t, client, "POST", origin, "/v1/auth/remote-session", "", map[string]string{"password": password})
	if response.Header.Get("WWW-Authenticate") != "" {
		t.Fatal("native browser login must never be challenged")
	}
	if status == http.StatusNoContent {
		cookies := response.Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Domain != "" || cookies[0].Path != "/" {
			t.Fatal("login must issue a host-only HttpOnly Strict cookie")
		}
	}
	requireRemoteStatus(t, response, status)
}

func requireRemoteStatus(t *testing.T, response *http.Response, status int) {
	t.Helper()
	_ = response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("HTTP status = %d, want %d", response.StatusCode, status)
	}
}

func TestRemoteAccessLiveSettingsAndRestart(t *testing.T) {
	dataDir := t.TempDir()
	local := startTestLocal(t, dataDir, "remote-test-nonce")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	origin := local.Endpoint().Origin
	client := newTestClient(t)
	bootstrap := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	view := getJSON[httpapi.NetworkAccessView](t, client, origin, "/v1/system/network-access")
	if view.ExternalAccess || view.PasswordSet || !view.CanManage || !isLoopbackListener(local.listener.Addr()) {
		t.Fatal("fresh default must be loopback only, manageable locally and without a password")
	}
	// No password, weak password, removed port field, missing toggle or CSRF cannot
	// open the listener. A remote-access password is not a CSRF substitute.
	for _, body := range []map[string]any{
		{"enabled": true},
		{"enabled": true, "password": "short"},
		{"enabled": true, "port": 80, "password": testRemotePassword},
		{"password": testRemotePassword},
	} {
		requireRemoteStatus(t, remoteProbe(t, client, "PUT", origin, "/v1/system/network-access", bootstrap.CSRFToken, body), http.StatusBadRequest)
	}
	requireRemoteStatus(t, remoteProbe(t, client, "PUT", origin, "/v1/system/network-access", "", remoteUpdate(true, testRemotePassword)), http.StatusForbidden)
	view = mutateJSON[httpapi.NetworkAccessView](t, client, origin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(true, testRemotePassword))
	if !view.ExternalAccess || !view.PasswordSet || view.Port == 0 || local.Endpoint().Origin != origin {
		t.Fatal("enabling remote access must not change the local origin")
	}
	persisted, err := local.repository.NetworkAccess(t.Context())
	if err != nil || !persisted.Enabled || persisted.Port != view.Port || persisted.PasswordHash == testRemotePassword || bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte(testRemotePassword)) != nil {
		t.Fatal("only the salted password verifier and enabled/port settings should be persisted")
	}
	if local.listener.Addr().(*net.TCPAddr).Port != view.Port || len(view.ExternalURLs) == 0 {
		t.Fatal("local and LAN must share exactly one port")
	}
	// Local stays passwordless; the same port on the LAN IP uses the gate.
	requireRemoteStatus(t, remoteProbe(t, newTestClient(t), "GET", origin, "/readyz", "", nil), http.StatusOK)
	remoteOrigin := view.ExternalURLs[0]
	remote := newTestClient(t)
	for _, host := range []string{strings.TrimPrefix(origin, "http://"), fmt.Sprintf("localhost:%d", view.Port)} {
		request, err := http.NewRequest("GET", remoteOrigin+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		request.Header.Set("X-Forwarded-For", "127.0.0.1")
		request.Header.Set("Forwarded", "for=127.0.0.1")
		response, err := remote.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			t.Fatal("LAN request spoofed a passwordless local identity")
		}
	}
	for _, path := range []string{"/", "/readyz", "/v1/auth/csrf", "/v1/projects/test/git/events"} {
		response := remoteProbe(t, remote, "GET", remoteOrigin, path, "", nil)
		if response.Header.Get("WWW-Authenticate") != "" {
			t.Fatal("remote browser must not receive a native login challenge")
		}
		requireRemoteStatus(t, response, http.StatusUnauthorized)
	}
	remoteLogin(t, remote, remoteOrigin, "test-only-wrong-password", http.StatusUnauthorized)
	remoteLogin(t, remote, remoteOrigin, testRemotePassword, http.StatusNoContent)
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/readyz", "", nil), http.StatusOK)
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/desktop/bootstrap?nonce=remote-test-nonce", "", nil), http.StatusNotFound)
	response := remoteProbe(t, remote, "POST", remoteOrigin, "/v1/auth/local-session", "", map[string]any{})
	var session struct {
		Data testBootstrap `json:"data"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&session) != nil {
		t.Fatal("authenticated remote browser must be able to create a same-origin session")
	}
	_ = response.Body.Close()
	response = remoteProbe(t, remote, "GET", remoteOrigin, "/v1/system/network-access", "", nil)
	var remoteView struct {
		Data httpapi.NetworkAccessView `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&remoteView) != nil || remoteView.Data.CanManage {
		t.Fatal("remote session must be read-only for network settings")
	}
	_ = response.Body.Close()
	requireRemoteStatus(t, remoteProbe(t, remote, "PUT", remoteOrigin, "/v1/system/network-access", session.Data.CSRFToken, remoteUpdate(false, "")), http.StatusForbidden)

	// A live SSE connection is disconnected when the password changes.
	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	project := mutateJSON[domain.Project](t, client, origin, "POST", "/v1/projects", bootstrap.CSRFToken, map[string]any{"computer_id": computers.Items[0].ID, "name": "remote-stream"})
	stream := remoteProbe(t, remote, "GET", remoteOrigin, "/v1/projects/"+project.ID+"/git/events", "", nil)
	if stream.StatusCode != http.StatusOK {
		t.Fatal("remote SSE did not start")
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream.Body); _ = stream.Body.Close(); close(done) }()
	const changed = "test-only-changed-password"
	mutateJSON[httpapi.NetworkAccessView](t, client, origin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(true, changed))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("password rotation must disconnect existing SSE")
	}
	// Existing session cookies cannot bypass password rotation.
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/v1/computers", "", nil), http.StatusUnauthorized)
	remoteLogin(t, remote, remoteOrigin, testRemotePassword, http.StatusUnauthorized)
	remoteLogin(t, remote, remoteOrigin, changed, http.StatusNoContent)
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/v1/computers", "", nil), http.StatusOK)
	view = mutateJSON[httpapi.NetworkAccessView](t, client, origin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(false, ""))
	if view.ExternalAccess || !view.PasswordSet || len(view.ExternalURLs) != 0 {
		t.Fatal("disable must preserve password but remove remote URLs")
	}
	if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(remoteOrigin, "http://"), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("disabled remote port still accepts connections")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/readyz", "", nil), http.StatusOK)
	view = mutateJSON[httpapi.NetworkAccessView](t, client, origin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(true, ""))
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/readyz", "", nil), http.StatusUnauthorized)
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := startTestLocal(t, dataDir, "remote-restart-nonce")
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	if got := restarted.network.View(); !got.ExternalAccess || got.Port != view.Port || !got.PasswordSet {
		t.Fatal("Wails-style restart without LAN flags must restore saved access settings")
	}
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/readyz", "", nil), http.StatusUnauthorized)
	remoteLogin(t, remote, remoteOrigin, changed, http.StatusNoContent)
	requireRemoteStatus(t, remoteProbe(t, remote, "GET", remoteOrigin, "/readyz", "", nil), http.StatusOK)
}

func TestRemoteAccessDisabledStateAndCLISeed(t *testing.T) {
	dataDir := t.TempDir()
	local, err := StartLocal(t.Context(), LocalConfig{DataDir: dataDir, Address: "0.0.0.0:0", AllowExternalAccess: true, DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	if got := local.network.View(); got.ExternalAccess || got.PasswordSet {
		t.Fatal("CLI opt-in without a password must remain local-only")
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	local, err = StartLocal(t.Context(), LocalConfig{DataDir: dataDir, Address: "0.0.0.0:0", AllowExternalAccess: true, AccessToken: testRemotePassword, DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	view := local.network.View()
	stored, err := local.repository.NetworkAccess(t.Context())
	if err != nil || !view.ExternalAccess || view.Port == 0 || stored.Port != view.Port {
		t.Fatal("CLI automatic port must be persisted after first binding")
	}
	if _, err := local.network.Update(t.Context(), remoteUpdate(false, "")); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A subsequent CLI invocation must not undo a saved disable or replace the
	// Settings-managed password with an environment seed.
	local, err = StartLocal(t.Context(), LocalConfig{DataDir: dataDir, Address: "0.0.0.0:0", AllowExternalAccess: true, AccessToken: "test-only-ignored-seed", DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := local.network.View(); got.ExternalAccess || !got.PasswordSet || got.Port != view.Port {
		t.Fatal("saved disable must survive both Wails and CLI restarts")
	}
	stored, err = local.repository.NetworkAccess(t.Context())
	if err != nil || bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(testRemotePassword)) != nil {
		t.Fatal("existing password was replaced by CLI seed")
	}
}

type networkStoreStub struct {
	config store.NetworkAccessConfig
	err    error
}

func (s *networkStoreStub) SetNetworkAccess(_ context.Context, config store.NetworkAccessConfig) error {
	if s.err == nil {
		s.config = config
	}
	return s.err
}

func TestRemoteAccessFailureAtomicityAndConcurrency(t *testing.T) {
	repository := &networkStoreStub{}
	network := testNetworkAccess(t, repository, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	before := repository.config
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	repository.err = errors.New("test-only store failure")
	if _, err := network.Update(t.Context(), remoteUpdate(false, "")); err == nil {
		t.Fatal("failed persistence accepted")
	}
	if !network.View().ExternalAccess || !reflect.DeepEqual(repository.config, before) {
		t.Fatal("failed save disabled working listener")
	}
	repository.err = nil
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 5 {
				_ = network.View()
				_, err := network.Update(t.Context(), remoteUpdate(true, ""))
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
	// Startup bind failure is recoverable from the local settings window.
	blockedConfig := before
	blockedConfig.Port = port
	blocked := testNetworkAccess(t, repository, blockedConfig)
	blocked.start(t.Context(), http.NotFoundHandler())
	if got := blocked.View(); !got.ListenError || got.ExternalAccess {
		t.Fatal("busy startup port should report failure without opening it")
	}
	if _, err := blocked.Update(t.Context(), remoteUpdate(true, "")); err == nil {
		t.Fatal("occupied shared port accepted")
	}
	if !reflect.DeepEqual(repository.config, before) || network.View().Port != view.Port {
		t.Fatal("bind failure modified configuration")
	}
	_ = occupied.Close()
	if _, err := blocked.Update(t.Context(), remoteUpdate(true, "")); err != nil {
		t.Fatal(err)
	}
	if got := blocked.View(); got.ListenError || !got.ExternalAccess {
		t.Fatal("retry did not clear startup error")
	}
}

func TestRemotePasswordValidationRateLimitAndHostGuard(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("界", 25), "test\npassword"} {
		if _, err := hashRemotePassword(password); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	hash, err := hashRemotePassword(testRemotePassword)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		// bcrypt can take longer than the real one-second refill window under
		// race/load. Virtual time tests the production limit, not CPU speed.
		password := newRemotePassword(hash)
		if valid, limited := password.check(testRemotePassword); !valid || limited {
			t.Fatal("valid password rejected")
		}
		password.mu.Lock()
		password.tokens, password.last = 0, time.Now()
		password.mu.Unlock()
		if valid, limited := password.check("test-only-wrong"); valid || !limited {
			t.Fatal("failed attempts not bounded")
		}
		if valid, limited := password.check(testRemotePassword); valid || !limited {
			t.Fatal("all password logins should be rate limited, not ordinary session requests")
		}
		time.Sleep(time.Second)
		if valid, limited := password.check(testRemotePassword); !valid || limited {
			t.Fatal("one-second refill did not admit exactly one login")
		}
		if valid, limited := password.check("test-only-wrong"); valid || !limited {
			t.Fatal("refill admitted more than one login per second")
		}
		time.Sleep(time.Hour)
		if valid, limited := password.check(testRemotePassword); !valid || limited {
			t.Fatal("refilled login rejected")
		}
		password.mu.Lock()
		remaining := password.tokens
		password.mu.Unlock()
		if remaining != 9 {
			t.Fatalf("refill exceeded the ten-attempt burst: remaining=%v", remaining)
		}
	})
	network := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || !httpapi.IsRemoteAccess(r) {
			t.Error("authorization headers must be removed and request marked remote")
		}
		w.WriteHeader(http.StatusOK)
	}))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{fmt.Sprintf("attacker.test:%d", view.Port), "127.0.0.1:1", fmt.Sprintf("0.0.0.0:%d", view.Port)} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.SetBasicAuth("admin", testRemotePassword)
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		network.serveRemote(network.current, w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("invalid host accepted")
		}
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", view.Port), nil)
	r.SetBasicAuth("admin", testRemotePassword)
	w := httptest.NewRecorder()
	network.serveRemote(network.current, w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("legacy Basic must not authenticate or prompt the browser")
	}
	client := newTestClient(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	remoteLogin(t, client, origin, testRemotePassword, http.StatusNoContent)
	requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/", "", nil), http.StatusOK)
}

func TestSharedPortBindAndSaveFailuresReleasePreparedSockets(t *testing.T) {
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	repository := &networkStoreStub{}
	network := testNetworkAccess(t, repository, store.NetworkAccessConfig{Port: port})
	network.start(t.Context(), http.NotFoundHandler())
	assertClosed := func() {
		t.Helper()
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatal("failed update leaked a listening socket")
		}
		if network.View().ExternalAccess || repository.config.Enabled {
			t.Fatal("failed update changed active settings")
		}
	}
	addresses := network.addresses
	network.addresses = func(port string) []string {
		// The second bind conflicts with the first, simulating a partial
		// multi-interface prepare failure without requiring a real LAN.
		return []string{"http://127.0.0.1:" + port, "http://127.0.0.1:" + port}
	}
	if _, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword)); err == nil {
		t.Fatal("partial bind failure accepted")
	}
	assertClosed()
	network.addresses = func(string) []string { return nil }
	if _, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword)); err == nil {
		t.Fatal("no-interface update accepted")
	}
	assertClosed()
	network.addresses = addresses
	repository.err = errors.New("test-only write failure")
	if _, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword)); err == nil {
		t.Fatal("failed save accepted")
	}
	assertClosed()
	repository.err = nil
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil || view.Port != port || !view.ExternalAccess {
		t.Fatal("same-port retry failed")
	}
}
