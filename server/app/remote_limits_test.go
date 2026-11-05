package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/httpapi"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestRemoteLoginLimitLeavesLocalAndExistingSessionsAvailable(t *testing.T) {
	local := startTrustTestLocal(t, t.TempDir())
	view, err := local.network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := view.ExternalURLs[0]
	ordinary, trusted := newTestClient(t), newTestClient(t)
	trustLogin(t, ordinary, origin, testRemotePassword, false)
	trustLogin(t, trusted, origin, testRemotePassword, true)

	request := httptest.NewRequest(http.MethodPost, origin+httpapi.RemoteSessionPath,
		strings.NewReader(`{"password":"`+testRemotePassword+`"}`))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	password := local.network.password
	password.mu.Lock()
	password.tokens, password.last = 0, time.Now()
	password.mu.Unlock()
	local.network.serveRemote(local.network.current, response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatal("an exhausted shared login budget must reject even a correct password")
	}
	for _, client := range []*http.Client{ordinary, trusted} {
		requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, httpapi.RemoteSessionPath, "", nil), http.StatusNoContent)
		requireRemoteStatus(t, remoteProbe(t, client, "POST", origin, "/v1/auth/local-session", "", map[string]any{}), http.StatusOK)
		requireRemoteStatus(t, remoteProbe(t, client, "GET", origin, "/v1/computers", "", nil), http.StatusOK)
	}
	client, localOrigin := newTestClient(t), local.Endpoint().Origin
	bootstrap := mutateJSON[testBootstrap](t, client, localOrigin, "POST", "/v1/auth/local-session", "", map[string]any{})
	mutateJSON[httpapi.NetworkAccessView](t, client, localOrigin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(false, ""))
	requireRemoteStatus(t, remoteProbe(t, client, "GET", localOrigin, "/readyz", "", nil), http.StatusOK)
}

func TestTrustedSessionCapacityPreservesTrustAndLocalRecovery(t *testing.T) {
	local := startTrustTestLocal(t, t.TempDir())
	view, err := local.network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal(err)
	}
	origin := view.ExternalURLs[0]
	trusted := newTestClient(t)
	trustLogin(t, trusted, origin, testRemotePassword, true)
	config, err := local.repository.NetworkAccess(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < maxRemoteSessions; index++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("test-only-capacity-%d", index)))
		config.TrustedDevices = append(config.TrustedDevices, store.TrustedRemoteDevice{
			TokenHash: hex.EncodeToString(digest[:]), Host: strings.TrimPrefix(origin, "http://"),
		})
	}
	if err := local.repository.SetNetworkAccess(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	local.network.mu.Lock()
	local.network.config = config
	err = local.network.restoreTrustedDevices()
	local.network.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := remoteProbe(t, newTestClient(t), "POST", origin, httpapi.RemoteSessionPath, "", map[string]any{"password": testRemotePassword})
	if len(response.Cookies()) != 0 || response.Header.Get("Retry-After") != "60" {
		t.Fatal("a full trust registry must not issue a new credential or evict existing trust")
	}
	requireRemoteStatus(t, response, http.StatusTooManyRequests)
	requireRemoteStatus(t, remoteProbe(t, trusted, "GET", origin, "/readyz", "", nil), http.StatusOK)
	// A recognized browser may rotate its own credential even at capacity.
	trustLogin(t, trusted, origin, testRemotePassword, false, true)
	config, err = local.repository.NetworkAccess(t.Context())
	if err != nil || len(config.TrustedDevices) != maxRemoteSessions {
		t.Fatal("re-login at capacity lost permanent trust")
	}
	client, localOrigin := newTestClient(t), local.Endpoint().Origin
	bootstrap := mutateJSON[testBootstrap](t, client, localOrigin, "POST", "/v1/auth/local-session", "", map[string]any{})
	mutateJSON[httpapi.NetworkAccessView](t, client, localOrigin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(true, testRemotePassword))
	config, err = local.repository.NetworkAccess(t.Context())
	if err != nil || len(config.TrustedDevices) != maxRemoteSessions {
		t.Fatal("saving the same password must not evict trust at capacity")
	}
	const changed = "test-only-capacity-new-password"
	mutateJSON[httpapi.NetworkAccessView](t, client, localOrigin, "PUT", "/v1/system/network-access", bootstrap.CSRFToken, remoteUpdate(true, changed))
	config, err = local.repository.NetworkAccess(t.Context())
	if err != nil || len(config.TrustedDevices) != 0 {
		t.Fatal("a local password change must atomically revoke the full trust registry")
	}
	requireRemoteStatus(t, remoteProbe(t, trusted, "GET", origin, "/readyz", "", nil), http.StatusUnauthorized)
	remoteLogin(t, newTestClient(t), origin, changed, http.StatusNoContent)
}
