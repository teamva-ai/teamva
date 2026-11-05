package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type modelSetupRelay struct {
	saved, tested int
	input         nodeprotocol.ModelProviderInput
	failure       error
}

func (*modelSetupRelay) NotifyIssueActivation(string) {}
func (r *modelSetupRelay) ModelProviderSettings(context.Context, string) (nodeprotocol.ModelProviderSettings, error) {
	return nodeprotocol.ModelProviderSettings{Presets: []nodeprotocol.ModelProviderPreset{}, Configured: []nodeprotocol.ModelProviderView{}}, r.failure
}
func (r *modelSetupRelay) SaveModelProvider(_ context.Context, _ string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error) {
	r.saved++
	r.input = input
	return nodeprotocol.ModelProviderView{PresetID: input.PresetID, Name: "Custom", BaseURL: input.BaseURL, Model: input.Model, ModelRef: "teamva-custom/" + input.Model, KeyConfigured: true}, r.failure
}
func (r *modelSetupRelay) TestModelProvider(_ context.Context, _ string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error) {
	r.tested++
	r.input = input
	return nodeprotocol.ModelProviderTestResult{OK: true, Code: "success"}, r.failure
}

func TestModelSetupHTTPIsLocalAuthenticatedCSRFAndWriteOnly(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	owner := createTestIdentity(t, repository, "model-setup@example.test")
	relay := &modelSetupRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	headers := map[string]string{"Cookie": sessionCookieName + "=" + owner.token, "X-CSRF-Token": owner.csrf, "Origin": testBrowserOrigin}
	path := "/v1/system/programming-tools/opencode/model-providers"
	body := map[string]any{"preset_id": "custom", "base_url": "http://127.0.0.1:1234/v1", "model": "test-model", "api_key": "model-setup-private-fixture"}
	for _, operation := range []struct {
		method, path string
		input        any
	}{{"GET", path, nil}, {"PUT", path, body}, {"POST", path + "/test", body}} {
		response := localRequest(t, router, operation.method, operation.path, operation.input, headers, 200)
		if strings.Contains(response.Recorder.Body.String(), "model-setup-private-fixture") || response.Recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("settings were cached or leaked credentials")
		}
		localRequest(t, router, operation.method, operation.path, operation.input, nil, 401)
		remote := httptest.NewRequest(operation.method, operation.path, strings.NewReader(`{"preset_id":"custom"}`))
		remote = MarkRemoteAccess(remote)
		for key, value := range headers {
			remote.Header.Set(key, value)
		}
		remote.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, remote)
		if recorder.Code != 403 {
			t.Fatalf("remote model settings allowed: %d", recorder.Code)
		}
	}
	if relay.saved != 1 || relay.tested != 1 || relay.input.APIKey != "model-setup-private-fixture" {
		t.Fatal("requests were not relayed directly")
	}
	localRequest(t, router, http.MethodPut, path, body, map[string]string{"Cookie": headers["Cookie"], "Origin": testBrowserOrigin}, 403)
	localRequest(t, router, http.MethodPost, path+"/test", map[string]any{"prompt": "arbitrary text"}, headers, 400)
	if relay.saved != 1 || relay.tested != 1 {
		t.Fatal("unauthorized or unknown input reached runtime")
	}
	relay.failure = errors.New("upstream echo: model-setup-private-fixture")
	response := localRequest(t, router, http.MethodPost, path+"/test", body, headers, 503)
	if strings.Contains(response.Recorder.Body.String(), "model-setup-private-fixture") {
		t.Fatal("raw upstream error leaked")
	}
}
