package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestModelSetupPrivatePersistenceAndRuntimeConfig(t *testing.T) {
	isolateUserHomes(t)
	home, _ := configHome()
	userFile := filepath.Join(home, "opencode", "opencode.jsonc")
	userContent := `{"model":"existing/model","providers":{"existing":{"name":"Keep me"}}}`
	writeFile(t, userFile, userContent)
	input := nodeprotocol.ModelProviderInput{PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "fixture-private-key"}
	view, err := (modelSetup{}).Save(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if view.ModelRef != "teamva-deepseek/deepseek-chat" || !view.KeyConfigured {
		t.Fatal("invalid configured projection")
	}
	settings, err := (modelSetup{}).Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(settings)
	if strings.Contains(string(public), input.APIKey) || strings.Contains(string(public), `"api_key"`) {
		t.Fatal("credential leaked into public settings")
	}
	path, _ := modelSetupPath()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential file must be private")
	}
	if actual, err := os.ReadFile(userFile); err != nil || string(actual) != userContent {
		t.Fatal("existing OpenCode settings were overwritten")
	}
	input.APIKey = ""
	input.Model = "deepseek-reasoner"
	if _, err := (modelSetup{}).Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	stored, err := readModelSetup()
	if err != nil || stored.Items["deepseek"].APIKey != "fixture-private-key" {
		t.Fatal("same-endpoint key not retained")
	}
	input.BaseURL = "https://another.example/v1"
	if _, err := (modelSetup{}).Save(t.Context(), input); err == nil {
		t.Fatal("old credential reused for a different endpoint")
	}
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig(t, dir)
	if cfg["model"] != "existing/model" {
		t.Fatal("native default model was lost")
	}
	providers := cfg["providers"].(map[string]any)
	managed := providers["teamva-deepseek"].(map[string]any)
	models := managed["models"].(map[string]any)
	if models["deepseek-chat"] == nil || models["deepseek-reasoner"] == nil {
		t.Fatal("adding another Agent's model removed a previously configured model")
	}
	if managed["package"] != "@opencode/ai/providers/openai-compatible" || managed["settings"].(map[string]any)["apiKey"] != "fixture-private-key" {
		t.Fatal("managed runtime cannot use the configured key")
	}
	if providers["existing"] == nil {
		t.Fatal("unrelated providers were removed")
	}
}

func TestModelSetupHelloUsesEachProtocolWithoutSavingOrReturningSecrets(t *testing.T) {
	for _, preset := range []string{"openai", "anthropic", "google"} {
		t.Run(preset, func(t *testing.T) {
			isolateUserHomes(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				if json.Unmarshal(raw, &body) != nil {
					t.Error("invalid test request")
				}
				if r.Method != "POST" {
					t.Error("test must be a single POST")
				}
				switch preset {
				case "openai":
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-private-key" {
						t.Error("incorrect OpenAI protocol")
					}
					messages := body["messages"].([]any)
					if len(messages) != 1 || messages[0].(map[string]any)["content"] != "Hello" {
						t.Error("test prompt must be exactly Hello")
					}
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fixture-private-key"}}]}`))
				case "anthropic":
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture-private-key" || r.Header.Get("anthropic-version") == "" {
						t.Error("incorrect Anthropic protocol")
					}
					messages := body["messages"].([]any)
					if messages[0].(map[string]any)["content"] != "Hello" {
						t.Error("incorrect prompt")
					}
					_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"fixture-private-key"}]}`))
				case "google":
					if r.URL.Path != "/v1/models/test-model:generateContent" || r.Header.Get("x-goog-api-key") != "fixture-private-key" || r.URL.RawQuery != "" {
						t.Error("incorrect Google protocol or key in URL")
					}
					contents := body["contents"].([]any)
					parts := contents[0].(map[string]any)["parts"].([]any)
					if parts[0].(map[string]any)["text"] != "Hello" {
						t.Error("incorrect prompt")
					}
					_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"fixture-private-key"}]}}]}`))
				}
			}))
			defer server.Close()
			result, err := (modelSetup{}).Test(t.Context(), nodeprotocol.ModelProviderInput{PresetID: preset, BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "fixture-private-key"})
			if err != nil || !result.OK || calls.Load() != 1 {
				t.Fatalf("test failed ok=%t calls=%d err=%v", result.OK, calls.Load(), err)
			}
			public, _ := json.Marshal(result)
			if strings.Contains(string(public), "fixture-private-key") {
				t.Fatal("provider echo leaked a credential")
			}
			path, _ := modelSetupPath()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("test saved draft credentials")
			}
		})
	}
}

func TestModelSetupRejectsInvalidURLsAndSymlinkFiles(t *testing.T) {
	isolateUserHomes(t)
	for _, address := range []string{"http://untrusted.example/v1", "https://user:password@example.test/v1", "https://example.test/v1?key=secret", "https://example.test/v1#fragment", "file:///private/file", "http://169.254.169.254/v1"} {
		_, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: address, Model: "test", APIKey: "fixture-key"})
		if err == nil || strings.Contains(err.Error(), address) {
			t.Fatal("invalid address accepted or echoed")
		}
	}
	path, _ := modelSetupPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"version":1,"items":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: "http://127.0.0.1:1/v1", Model: "test"}); err == nil {
		t.Fatal("credential symlink accepted")
	}
}

func TestModelSetupHelloRejectsErrorsAndDoesNotFollowRedirects(t *testing.T) {
	isolateUserHomes(t)
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	for _, test := range []struct {
		status     int
		body, code string
	}{
		{401, `{"error":"fixture-key"}`, "authentication_failed"}, {429, "secret", "rate_limited"},
		{200, `{"error":"bad model"}`, "invalid_response"}, {200, "<html>ok</html>", "invalid_response"},
		{302, "", "request_failed"}, {200, strings.Repeat("x", (64<<10)+1), "invalid_response"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		result, err := (modelSetup{}).Test(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL, Model: "test", APIKey: "fixture-key"})
		server.Close()
		if err != nil || result.OK || result.Code != test.code {
			t.Fatalf("status=%d code=%s err=%v", test.status, result.Code, err)
		}
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("credentials were redirected")
	}
}

func TestModelSetupHelloHonorsCancellation(t *testing.T) {
	isolateUserHomes(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := (modelSetup{}).Test(ctx, nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: "http://127.0.0.1:1/v1", Model: "test"})
	if err != nil || result.OK || result.Code != "timeout" {
		t.Fatal("cancelled test did not stop")
	}
}

func TestBundledModelSetupCatalogUsesManagedProvider(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for bundled no-model catalog verification")
	}
	executable, err := filepath.Abs("../../../vendor/opencode/darwin-arm64/opencode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(executable); err != nil {
		t.Skip("bundled runtime unavailable")
	}
	isolateUserHomes(t)
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: "http://127.0.0.1:1/v1", Model: "model-setup-fixture", APIKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newModelAwareProbe(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	manifest, err := client.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range manifest.Models {
		if model == view.ModelRef {
			return
		}
	}
	t.Fatalf("native catalog omitted configured model (available=%t, count=%d)", manifest.Available, len(manifest.Models))
}
