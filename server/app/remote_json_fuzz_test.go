package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"
)

// This oracle checks the published request shape independently of the handler.
// Login input is at most 72 UTF-8 bytes, matching password configuration/bcrypt.
func validRemoteLoginJSON(data []byte) bool {
	if len(data) > 1024 || !utf8.Valid(data) {
		return false
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil {
		return false
	}
	password, ok := object["password"].(string)
	if !ok || len(password) > 72 {
		return false
	}
	for key, value := range object {
		switch key {
		case "password":
		case "trust_device":
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func FuzzRemoteLoginJSONContract(f *testing.F) {
	for _, data := range [][]byte{
		[]byte(`{}`), []byte(`null`), []byte(`{"password":""}`),
		[]byte(`{"password":"incorrect","trust_device":false}`),
		[]byte(`{"password":"incorrect","trust_device":null}`),
		[]byte(`{"password":null}`), []byte(`{"Password":"incorrect"}`),
		append(append([]byte(`{"password":"`), bytes.Repeat([]byte("x"), 73)...), []byte(`"}`)...),
		[]byte("{\"password\":\"\xff\"}"),
	} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// No listening socket, password verifier or mutable repository is needed
		// here: syntactically valid inputs must reach authentication and get 401;
		// invalid ones must get 400 first. TCP success is tested separately.
		instance := &remoteListener{}
		network := &networkAccess{current: instance, password: newRemotePassword("")}
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43119/v1/auth/remote-session", bytes.NewReader(data))
		request.Header.Set("Origin", "http://127.0.0.1:43119")
		request.Header.Set("Content-Type", "application/json")
		want := http.StatusBadRequest
		if validRemoteLoginJSON(data) {
			want = http.StatusUnauthorized
		}
		response := httptest.NewRecorder()
		network.remoteSessionEndpoint(instance, response, request)
		if response.Code != want || len(response.Result().Cookies()) != 0 {
			t.Fatalf("input shape status=%d want=%d; no credential must be issued", response.Code, want)
		}
	})
}
