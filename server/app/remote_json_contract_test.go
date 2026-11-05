package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/store"
)

// Exercise the actual password-gated TCP listener on loopback, never a user's
// LAN settings or a real Provider. Expectations precede every request.
func TestRemoteLoginRejectsNonObjectAndNullBeforeAuthentication(t *testing.T) {
	network := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	network.start(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal("prepare isolated loopback password gate")
	}
	origin := view.ExternalURLs[0]
	validPassword, err := json.Marshal(testRemotePassword)
	if err != nil {
		t.Fatal("encode synthetic test password")
	}
	cases := []struct {
		name, body string
	}{
		{"null object", `null`},
		{"array", `[]`},
		{"missing password", `{}`},
		{"null password", `{"password":null}`},
		{"null password whitespace", "{\"password\": \n null}"},
		{"wrong password type", `{"password":true}`},
		{"null trust flag", `{"password":` + string(validPassword) + `,"trust_device":null}`},
		{"wrong trust type", `{"password":` + string(validPassword) + `,"trust_device":"true"}`},
		{"unknown field", `{"password":` + string(validPassword) + `,"username":"unused"}`},
		{"wrong property case", `{"Password":` + string(validPassword) + `}`},
		{"two objects", `{"password":` + string(validPassword) + `} {}`},
		{"oversized", `{"password":"` + strings.Repeat("x", 1024) + `"}`},
	}
	client := newTestClient(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, origin+"/v1/auth/remote-session", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal("construct isolated request")
			}
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("send isolated request")
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != http.StatusBadRequest || len(response.Cookies()) != 0 {
				t.Fatalf("malformed login status=%d cookies=%d, want 400 with no cookie", response.StatusCode, len(response.Cookies()))
			}
		})
	}
	// All malformed attempts must be rejected before bcrypt/rate limiting. A
	// valid request remains usable and creates only the explicitly opted-in trust.
	trustLogin(t, client, origin, testRemotePassword, false)
	requireRemoteStatus(t, remoteProbe(t, client, http.MethodGet, origin, "/v1/auth/remote-session", "", nil), http.StatusNoContent)
	if len(network.config.TrustedDevices) != 0 {
		t.Fatal("invalid trust input persisted a trusted device")
	}
}

func TestRemoteLoginErrorEnvelopeOverHTTP(t *testing.T) {
	network := testNetworkAccess(t, &networkStoreStub{}, store.NetworkAccessConfig{})
	network.start(t.Context(), http.NotFoundHandler())
	view, err := network.Update(t.Context(), remoteUpdate(true, testRemotePassword))
	if err != nil {
		t.Fatal("prepare isolated loopback password gate")
	}
	origin := view.ExternalURLs[0]
	for _, tc := range []struct {
		name, method, body string
		status             int
	}{
		{"authentication required", http.MethodGet, "", http.StatusUnauthorized},
		{"malformed JSON", http.MethodPost, "{", http.StatusBadRequest},
		{"method guard", http.MethodDelete, "", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, origin+"/v1/auth/remote-session", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
			response, err := newTestClient(t).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var envelope struct {
				Error struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					Retryable *bool  `json:"retryable"`
				} `json:"error"`
				Meta struct {
					RequestID string `json:"request_id"`
				} `json:"meta"`
			}
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal("remote API error is not JSON")
			}
			if response.StatusCode != tc.status || envelope.Error.Code == "" || envelope.Error.Message == "" || envelope.Error.Retryable == nil {
				t.Fatalf("remote API error violates status/error contract: status=%d", response.StatusCode)
			}
			if envelope.Meta.RequestID == "" || envelope.Meta.RequestID != response.Header.Get("X-Request-ID") {
				t.Fatal("remote API error lacks matching request identity")
			}
		})
	}
}
