package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Real bundled OpenCode, isolated homes, and a local synthetic API only. This
// does not contact an actual model vendor or consume an account's tokens.
func TestBundledManagedModelUsesConfiguredAPI(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for bundled synthetic API verification")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Skip("bundled runtime missing")
	}
	isolateNativeFixtureEnvironment(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("native runtime did not use the saved key")
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if !strings.Contains(string(raw), "Hello") {
			t.Error("native request omitted Hello")
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"model-setup-fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"model-setup-fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","model":"model-setup-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	defer server.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "model-setup-fixture", APIKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: t.TempDir(), DataDir: t.TempDir(), Config: provider.InstanceConfig{ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	events, sink := captureSink()
	session, err := client.Start(ctx, provider.RunRequest{RunID: "fixture-model-setup", CWD: t.TempDir(), Prompt: "Hello", Model: view.ModelRef, ReadOnly: true}, sink)
	if err != nil {
		t.Fatalf("native synthetic API request failed: %v; requests=%d", err, calls.Load())
	}
	if session.ID == "" || calls.Load() == 0 {
		t.Fatal("native runtime did not use the configured API")
	}
	for _, event := range *events {
		if event.Type == "output.delta" {
			return
		}
	}
	t.Fatal("native runtime did not produce a model reply")
}
