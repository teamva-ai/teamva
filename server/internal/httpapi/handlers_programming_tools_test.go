package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

type programmingToolRuntimeRelay struct {
	items         []nodeprotocol.ProviderExecutable
	setProvider   string
	setExecutable string
	setCalls      int
}

func (r *programmingToolRuntimeRelay) NotifyIssueActivation(string) {}

func (r *programmingToolRuntimeRelay) ProviderExecutables() ([]nodeprotocol.ProviderExecutable, error) {
	return r.items, nil
}

func (r *programmingToolRuntimeRelay) SetProviderExecutable(
	_ context.Context, provider, executable string,
) (nodeprotocol.ProviderExecutable, error) {
	r.setProvider = provider
	r.setExecutable = executable
	r.setCalls++
	if executable == "" {
		return nodeprotocol.ProviderExecutable{Provider: provider, Path: "opencode", Source: "bundled"}, nil
	}
	return nodeprotocol.ProviderExecutable{Provider: provider, Path: executable, Source: "custom"}, nil
}

// localRequest mirrors request but presents a loopback peer. The local-only
// settings endpoints reject the shared helper's synthetic non-loopback address.
func localRequest(
	t *testing.T, handler http.Handler, method, path string, body any,
	headers map[string]string, expectedStatus int,
) testResponse {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	httpRequest := httptest.NewRequest(method, path, bytes.NewReader(payload))
	httpRequest.RemoteAddr = "127.0.0.1:54321"
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		httpRequest.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != expectedStatus {
		t.Fatalf("%s %s expected %d, got %d: %s", method, path, expectedStatus, recorder.Code, recorder.Body.String())
	}
	var response testResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response for %s %s: %v", method, path, err)
	}
	response.Recorder = recorder
	return response
}

func TestProgrammingToolExecutableSettingsRoundTrip(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	owner := createTestIdentity(t, repository, "programming-tools@example.test")
	headers := func(identity testIdentity) map[string]string {
		return map[string]string{
			"Cookie":       sessionCookieName + "=" + identity.token,
			"X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin,
		}
	}
	relay := &programmingToolRuntimeRelay{items: []nodeprotocol.ProviderExecutable{
		{Provider: "codex", Path: "codex", Source: "path"},
		{Provider: "opencode", Path: "/opt/bundled/opencode", Source: "bundled"},
	}}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})

	if err := repository.SetProviderExecutablePath(t.Context(), "opencode", "/opt/tools/opencode"); err != nil {
		t.Fatal(err)
	}
	page := decodeData[domain.Page[ProgrammingToolExecutable]](
		t, localRequest(t, router, http.MethodGet, "/v1/system/programming-tools", nil, headers(owner), http.StatusOK))
	if len(page.Items) != 2 {
		t.Fatalf("items=%+v", page.Items)
	}
	byProvider := make(map[string]ProgrammingToolExecutable, len(page.Items))
	for _, item := range page.Items {
		byProvider[item.Provider] = item
	}
	if got := byProvider["opencode"]; got.Path != "/opt/tools/opencode" || got.Resolved != "/opt/bundled/opencode" || got.Source != "bundled" {
		t.Fatalf("opencode executable=%+v", got)
	}
	if got := byProvider["codex"]; got.Path != "" || got.Resolved != "codex" || got.Source != "path" {
		t.Fatalf("codex executable=%+v", got)
	}

	binary := filepath.Join(t.TempDir(), "opencode-external")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	updated := decodeData[ProgrammingToolExecutable](
		t, localRequest(t, router, http.MethodPut, "/v1/system/programming-tools/opencode",
			map[string]any{"path": binary}, headers(owner), http.StatusOK))
	if updated.Provider != "opencode" || updated.Path != binary || updated.Resolved != binary || updated.Source != "custom" {
		t.Fatalf("updated executable=%+v", updated)
	}
	if relay.setCalls != 1 || relay.setProvider != "opencode" || relay.setExecutable != binary {
		t.Fatalf("runtime relay did not receive the exact path: %+v", relay)
	}
	if stored, err := repository.ProviderExecutablePath(t.Context(), "opencode"); err != nil || stored != binary {
		t.Fatalf("stored path=%q err=%v", stored, err)
	}

	// Invalid input never reaches the runtime or overwrites the stored value.
	for _, value := range []string{
		filepath.Join("relative", "opencode"),
		filepath.Join(t.TempDir(), "missing-opencode"),
		t.TempDir(),
	} {
		localRequest(t, router, http.MethodPut, "/v1/system/programming-tools/opencode",
			map[string]any{"path": value}, headers(owner), http.StatusBadRequest)
	}
	localRequest(t, router, http.MethodPut, "/v1/system/programming-tools/unknown",
		map[string]any{"path": binary}, headers(owner), http.StatusBadRequest)
	if relay.setCalls != 1 {
		t.Fatalf("invalid input reached runtime: calls=%d", relay.setCalls)
	}
	if stored, err := repository.ProviderExecutablePath(t.Context(), "opencode"); err != nil || stored != binary {
		t.Fatalf("invalid input changed stored path: %q err=%v", stored, err)
	}

	cleared := decodeData[ProgrammingToolExecutable](
		t, localRequest(t, router, http.MethodPut, "/v1/system/programming-tools/opencode",
			map[string]any{"path": ""}, headers(owner), http.StatusOK))
	if cleared.Path != "" || cleared.Source != "bundled" {
		t.Fatalf("cleared executable=%+v", cleared)
	}
	if relay.setCalls != 2 || relay.setExecutable != "" {
		t.Fatalf("runtime relay did not receive the clear: %+v", relay)
	}
	if stored, err := repository.ProviderExecutablePath(t.Context(), "opencode"); err != nil || stored != "" {
		t.Fatalf("stored path after clear=%q err=%v", stored, err)
	}
}

func TestProgrammingToolExecutableSettingsRejectRemoteAndUnsupportedRuntime(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	owner := createTestIdentity(t, repository, "programming-tools-remote@example.test")
	relay := &programmingToolRuntimeRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})

	remote := httptest.NewRequest(http.MethodPut, "/v1/system/programming-tools/opencode",
		strings.NewReader(`{"path":""}`))
	remote = MarkRemoteAccess(remote)
	remote.Header.Set("Content-Type", "application/json")
	remote.Header.Set("Cookie", sessionCookieName+"="+owner.token)
	remote.Header.Set("X-CSRF-Token", owner.csrf)
	remote.Header.Set("Origin", testBrowserOrigin)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, remote)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if relay.setCalls != 0 {
		t.Fatalf("remote update reached runtime: %+v", relay)
	}

	unsupported := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	localRequest(t, unsupported, http.MethodGet, "/v1/system/programming-tools", nil,
		map[string]string{
			"Cookie":       sessionCookieName + "=" + owner.token,
			"X-CSRF-Token": owner.csrf, "Origin": testBrowserOrigin,
		}, http.StatusNotImplemented)
}
