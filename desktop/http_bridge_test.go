package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	serverapp "github.com/teamva-ai/teamva/server/app"
)

func TestDesktopHTTPBridgeBootstrapsAndForwardsMutation(t *testing.T) {
	frontend := fstest.MapFS{"index.html": {Data: []byte("desktop-index")}}
	service, err := serverapp.StartLocal(t.Context(), serverapp.LocalConfig{
		Address: "127.0.0.1:0", DataDir: t.TempDir(), Version: "test",
		BootstrapNonce: "bridge-bootstrap", DisableRuntime: true, FrontendFS: frontend,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })

	app := newDesktopApp(frontend, "test")
	app.service = service
	close(app.startupDone)
	bridge := newDesktopHTTPBridge(app)

	root := httptest.NewRecorder()
	bridge.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "http://wails/", nil))
	if root.Code != http.StatusOK || root.Body.String() != "desktop-index" {
		t.Fatalf("root = %d %q", root.Code, root.Body.String())
	}

	bootstrap := bridgeJSON(t, bridge, http.MethodGet, "/v1/auth/bootstrap", nil, "")
	csrf := nestedString(t, bootstrap, "data", "csrf_token")
	computers := bridgeJSON(t, bridge, http.MethodGet, "/v1/computers", nil, "")
	data, ok := computers["data"].(map[string]any)
	if !ok {
		t.Fatalf("computers data = %#v", computers["data"])
	}
	items, ok := data["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("computers items = %#v", data["items"])
	}
	computer, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("computer = %#v", items[0])
	}
	computerID, _ := computer["id"].(string)

	created := bridgeJSON(t, bridge, http.MethodPost, "/v1/projects",
		[]byte(`{"computer_id":"`+computerID+`","name":"Bridge project"}`), csrf)
	if nestedString(t, created, "data", "name") != "Bridge project" {
		t.Fatalf("created project = %#v", created)
	}
}

func TestDesktopHTTPBridgeBootstrapFailureDoesNotExposeNonce(t *testing.T) {
	const nonce = "test-only-private-bridge-nonce"
	frontend := fstest.MapFS{"index.html": {Data: []byte("desktop-index")}}
	service, err := serverapp.StartLocal(t.Context(), serverapp.LocalConfig{
		Address: "127.0.0.1:0", DataDir: t.TempDir(), BootstrapNonce: nonce,
		DisableRuntime: true, FrontendFS: frontend,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	app := newDesktopApp(frontend, "test")
	app.service = service
	close(app.startupDone)
	bridge := newDesktopHTTPBridge(app).(*desktopHTTPBridge)
	bridge.client.Transport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("test-only-connect-failure")
	}}
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails/", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "test-only-connect-failure") {
		t.Fatal("bootstrap connection failure must remain diagnosable")
	}
	if strings.Contains(response.Body.String(), nonce) {
		t.Fatal("private bootstrap nonce was exposed in the startup error page")
	}
	bridge.client.Transport = nil
	retry := httptest.NewRecorder()
	bridge.ServeHTTP(retry, httptest.NewRequest(http.MethodGet, "http://wails/", nil))
	if retry.Code != http.StatusOK || retry.Body.String() != "desktop-index" {
		t.Fatal("a failed connection must not consume the bootstrap opportunity")
	}
}

func bridgeJSON(
	t *testing.T, handler http.Handler, method, path string, body []byte, csrf string,
) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, "http://wails"+path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Origin", "wails://wails")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("%s %s = %d %s", method, path, response.Code, response.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func nestedString(t *testing.T, input map[string]any, keys ...string) string {
	t.Helper()
	var current any = input
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object in %#v", strings.Join(keys, "."), input)
		}
		current = object[key]
	}
	value, ok := current.(string)
	if !ok || value == "" {
		t.Fatalf("%s is not a non-empty string in %#v", strings.Join(keys, "."), input)
	}
	return value
}
