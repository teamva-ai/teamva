package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Use the actual pinned CLI and its real native schema/session endpoint, but
// only an isolated localhost fake model. The model oracle supplies known usage
// rather than trusting either our adapter or the native event to define it.
func TestBundledUsageWarmColdResumeOffline(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for offline native usage verification")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	configDir := permissionFixtureDirectory(t, base, "config")
	dataDir := permissionFixtureDirectory(t, base, "data")
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 6 && len(body.Tools) != 0 {
			t.Error("independent NoTools audit advertised tools")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any, usage any) {
			value, _ := json.Marshal(map[string]any{
				"id": fmt.Sprintf("offline-usage-%d", call), "object": "chat.completion.chunk", "model": "usage-fixture",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": usage,
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", value)
		}
		chunk(map[string]any{"role": "assistant", "content": `{"verdict":"continue","note":"offline usage fixture"}`}, nil, nil)
		chunk(map[string]any{}, "stop", map[string]any{
			"prompt_tokens": call * 100, "completion_tokens": call * 10, "total_tokens": call * 110,
		})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{
		PresetID: "custom", BaseURL: server.URL + "/v1", Model: "usage-fixture", APIKey: "offline-fixture-only",
	})
	if err != nil {
		t.Fatal("save isolated fake provider failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	newClient := func(noTools bool, config string) *Client {
		t.Helper()
		item, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: config, DataDir: dataDir,
			Config: provider.InstanceConfig{SystemPrompt: "Reply with the supplied fixture JSON.", ReadOnly: noTools, DisableTools: noTools}})
		if err != nil {
			t.Fatal(err)
		}
		client := item.(*Client)
		t.Cleanup(func() { _ = client.Close() })
		cfg := readConfig(t, config)
		cfg["agents"].(map[string]any)["title"] = map[string]any{"disabled": true}
		raw, _ := json.Marshal(cfg)
		writeFile(t, filepath.Join(config, "opencode", "opencode.json"), string(raw))
		return client
	}
	client := newClient(false, configDir)
	var sessionID string
	var emittedTotal int64
	for round := 1; round <= 3; round++ {
		if round == 3 {
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client = newClient(false, configDir) // A genuinely cold native process/client.
		}
		events, sink := captureSink()
		request := provider.RunRequest{RunID: fmt.Sprintf("offline-usage-%d", round), CWD: cwd,
			Prompt: "Return the fixture JSON.", Model: view.ModelRef, NativeSessionID: sessionID}
		var session provider.NativeSession
		if round == 1 {
			session, err = client.Start(ctx, request, sink)
		} else {
			session, err = client.Resume(ctx, request, sink)
		}
		if err != nil {
			t.Fatalf("round %d failed: %v", round, err)
		}
		if round == 1 {
			var info struct {
				Version string `json:"version"`
			}
			if err := client.getJSON(ctx, "/api/info", &info); err != nil || info.Version != "2.0.18" {
				t.Fatalf("usage contract must be verified on pinned V2.0.18: version=%q err=%v", info.Version, err)
			}
		}
		if sessionID != "" && session.ID != sessionID {
			t.Fatal("Resume replaced the native session")
		}
		sessionID = session.ID
		var info struct {
			Data struct {
				ID     string `json:"id"`
				Tokens struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
				} `json:"tokens"`
			} `json:"data"`
		}
		if err := client.getJSON(ctx, "/api/session/"+url.PathEscape(sessionID), &info); err != nil {
			t.Fatal(err)
		}
		wantInput := int64(round * (round + 1) / 2 * 100)
		wantOutput := int64(round * (round + 1) / 2 * 10)
		if info.Data.ID != sessionID || info.Data.Tokens.Input != wantInput || info.Data.Tokens.Output != wantOutput {
			t.Fatalf("native GET schema/cumulative semantics differ: round=%d tokens=%+v want input=%d output=%d", round, info.Data.Tokens, wantInput, wantOutput)
		}
		var perRun int64
		for _, event := range *events {
			if event.Type == "usage.updated" {
				if event.Payload["input_tokens"] != int64(round*100) || event.Payload["output_tokens"] != int64(round*10) ||
					event.Payload["accounting_mode"] != "delta" {
					t.Errorf("round %d component oracle differs: %+v", round, event.Payload)
				}
				perRun += event.Payload["total_tokens"].(int64)
			}
		}
		emittedTotal += perRun
		if perRun != int64(round*110) {
			t.Errorf("round %d usage delta=%d want=%d (native session cumulative=%d)", round, perRun, round*110, wantInput+wantOutput)
		}
		t.Logf("round=%d native session cumulative=%d adapter per-run=%d adapter cumulative=%d", round, wantInput+wantOutput, perRun, emittedTotal)
	}
	if emittedTotal != 660 {
		t.Errorf("work cumulative=%d want=660", emittedTotal)
	}
	// Corrupt only the native session GET response, never the actual database.
	// Both failure classes must refuse Resume before any model prompt is sent.
	originalTransport := client.http.Transport
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		client.http.Transport = usageFixtureTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/session/"+sessionID {
				raw, _ := json.Marshal(map[string]any{"data": map[string]any{
					"id": sessionID, "agent": "build", "location": map[string]any{"directory": cwd},
				}}) // Deliberately missing mandatory tokens.
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			}
			return originalTransport.RoundTrip(r)
		})
		events, sink := captureSink()
		_, err := client.Resume(ctx, provider.RunRequest{RunID: "offline-baseline-failure", CWD: cwd,
			Prompt: "must not reach model", Model: view.ModelRef, NativeSessionID: sessionID}, sink)
		if err == nil || !strings.Contains(err.Error(), "usage baseline") || len(*events) != 0 {
			t.Fatalf("baseline failure was swallowed: status=%d err=%v events=%d", status, err, len(*events))
		}
	}
	client.http.Transport = originalTransport
	mu.Lock()
	nBefore := calls
	mu.Unlock()
	if nBefore != 3 {
		t.Fatalf("failed baseline submitted a model prompt: calls=%d want=3", nBefore)
	}
	// Concurrent callers of the same client/session are serialized by runMu.
	// Each must fetch its own current baseline, not the shared pre-race value.
	type resumeResult struct {
		usage int64
		err   error
	}
	results := make(chan resumeResult, 2)
	start := make(chan struct{})
	for i := range 2 {
		go func(i int) {
			<-start
			events, sink := captureSink()
			session, err := client.Resume(ctx, provider.RunRequest{RunID: fmt.Sprintf("offline-concurrent-%d", i), CWD: cwd,
				Prompt: "Return the fixture JSON.", Model: view.ModelRef, NativeSessionID: sessionID}, sink)
			if err == nil && session.ID != sessionID {
				err = fmt.Errorf("concurrent Resume replaced native session")
			}
			var usage int64
			for _, event := range *events {
				if event.Type == "usage.updated" {
					usage += event.Payload["total_tokens"].(int64)
				}
			}
			results <- resumeResult{usage: usage, err: err}
		}(i)
	}
	close(start)
	parallelUsage := make([]int64, 0, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		parallelUsage = append(parallelUsage, result.usage)
		emittedTotal += result.usage
	}
	slices.Sort(parallelUsage)
	if !slices.Equal(parallelUsage, []int64{440, 550}) || emittedTotal != 1650 {
		t.Fatalf("concurrent Resume accounting=%v cumulative=%d want=[440 550]/1650", parallelUsage, emittedTotal)
	}
	var concurrentInfo struct {
		Data struct {
			Tokens struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"tokens"`
		} `json:"data"`
	}
	if err := client.getJSON(ctx, "/api/session/"+url.PathEscape(sessionID), &concurrentInfo); err != nil ||
		concurrentInfo.Data.Tokens.Input != 1500 || concurrentInfo.Data.Tokens.Output != 150 {
		t.Fatalf("concurrent native cumulative oracle differs: tokens=%+v err=%v", concurrentInfo.Data.Tokens, err)
	}
	t.Logf("concurrent per-run=%v native and adapter cumulative=%d; failed baselines submitted no prompts", parallelUsage, emittedTotal)
	var schema struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := client.getJSON(ctx, "/openapi.json", &schema); err != nil {
		t.Fatal(err)
	}
	type schemaObject struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	var sessionSchema, tokenSchema, cacheSchema schemaObject
	if json.Unmarshal(schema.Components.Schemas["Session.Info"], &sessionSchema) != nil || !slices.Contains(sessionSchema.Required, "tokens") ||
		json.Unmarshal(schema.Components.Schemas["TokenUsage.Info"], &tokenSchema) != nil ||
		!strings.Contains(string(sessionSchema.Properties["tokens"]), "TokenUsage.Info") {
		t.Fatal("native OpenAPI no longer requires Session.Info.tokens / TokenUsage.Info")
	}
	for _, field := range []string{"input", "output", "reasoning", "cache"} {
		if !slices.Contains(tokenSchema.Required, field) {
			t.Fatalf("native TokenUsage.Info no longer requires %s", field)
		}
	}
	if json.Unmarshal(tokenSchema.Properties["cache"], &cacheSchema) != nil ||
		!slices.Contains(cacheSchema.Required, "read") || !slices.Contains(cacheSchema.Required, "write") {
		t.Fatal("native TokenUsage.Info no longer requires cache.read/write")
	}
	t.Log("V2.0.18 native OpenAPI: Session.Info.tokens and all five TokenUsage.Info counters are mandatory")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	audit := newClient(true, permissionFixtureDirectory(t, base, "audit-config"))
	events, sink := captureSink()
	session, err := audit.Start(ctx, provider.RunRequest{RunID: "offline-independent-audit", CWD: cwd,
		Prompt: "Audit the supplied summary only.", Model: view.ModelRef, ReadOnly: true, DisableTools: true}, sink)
	if err != nil || session.ID == sessionID || session.ID == "" {
		t.Fatalf("independent audit session failed: err=%v", err)
	}
	var auditUsage int64
	for _, event := range *events {
		if event.Type == "usage.updated" {
			auditUsage += event.Payload["total_tokens"].(int64)
		}
		if event.Type == "provider.event" && event.Payload["kind"] == "tool" {
			t.Fatal("independent NoTools audit acquired a tool event")
		}
	}
	if auditUsage != 660 {
		t.Errorf("independent audit usage=%d want=660", auditUsage)
	}
	if err := audit.getJSON(ctx, "/api/session/"+url.PathEscape(session.ID), &concurrentInfo); err != nil ||
		concurrentInfo.Data.Tokens.Input != 600 || concurrentInfo.Data.Tokens.Output != 60 {
		t.Fatalf("independent audit native oracle differs: tokens=%+v err=%v", concurrentInfo.Data.Tokens, err)
	}
	if err := audit.getJSON(ctx, "/api/session/"+url.PathEscape(sessionID), &concurrentInfo); err != nil ||
		concurrentInfo.Data.Tokens.Input != 1500 || concurrentInfo.Data.Tokens.Output != 150 {
		t.Fatalf("independent audit changed work session usage: tokens=%+v err=%v", concurrentInfo.Data.Tokens, err)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 6 {
		t.Errorf("model oracle calls=%d want=6 (no auxiliary calls)", n)
	}
	t.Logf("independent NoTools audit: native and adapter usage=%d; work session unchanged at 1650; fake model requests=%d", auditUsage, n)
	t.Log("real bundled CLI + native GET/schema + localhost fake model only; no real model/account/service accessed")
}

type usageFixtureTransport func(*http.Request) (*http.Response, error)

func (f usageFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
