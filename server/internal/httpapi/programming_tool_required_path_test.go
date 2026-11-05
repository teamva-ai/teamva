package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProgrammingToolPathMustBeExplicitOverHTTP(t *testing.T) {
	repository := newTestRepository(t)
	relay := &programmingToolRuntimeRelay{}
	router := newRouterWithOrigins(repository, relay, "path-contract", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	const previous = "/isolated-fixture/previous-provider"
	if err := repository.SetProviderExecutablePath(t.Context(), "opencode", previous); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"path": nil}, http.StatusBadRequest},
		{map[string]any{"path": ""}, http.StatusOK},
	} {
		payload, err := json.Marshal(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPut, server.URL+"/v1/system/programming-tools/opencode", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range auth {
			request.Header.Set(key, value)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("explicit path status=%d want=%d", response.StatusCode, tc.status)
		}
		if tc.status == http.StatusBadRequest {
			stored, err := repository.ProviderExecutablePath(t.Context(), "opencode")
			if err != nil || stored != previous || relay.setCalls != 0 {
				t.Fatal("missing/null path changed Runtime or persisted configuration")
			}
		}
	}
	if relay.setCalls != 1 || relay.setExecutable != "" {
		t.Fatal("explicit empty path must still restore automatic resolution")
	}
}
