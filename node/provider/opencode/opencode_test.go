package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

type capturedEvent struct {
	Type    string
	Payload map[string]any
}

func captureSink() (*[]capturedEvent, provider.Sink) {
	events := make([]capturedEvent, 0, 16)
	sink := func(_ context.Context, event provider.Event) error {
		events = append(events, capturedEvent{Type: event.Type, Payload: event.Payload})
		return nil
	}
	return &events, sink
}

func testClient(t *testing.T) *Client {
	t.Helper()
	client, err := New("unused-opencode")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHandleEventMapsProtocol(t *testing.T) {
	client := testClient(t)
	state := &runState{
		request: provider.RunRequest{RunID: "run-1", ReadOnly: false},
		session: "ses_1", compacted: map[string]string{},
	}
	tests := []struct {
		name     string
		event    v2Event
		wantType string
		check    func(t *testing.T, payload map[string]any)
	}{
		{
			name:     "execution started",
			event:    v2Event{ID: "evt_1", Type: "session.execution.started", Data: map[string]any{"sessionID": "ses_1"}},
			wantType: "run.started",
			check: func(t *testing.T, payload map[string]any) {
				if payload["native_session_id"] != "ses_1" {
					t.Fatalf("native session missing: %#v", payload)
				}
			},
		},
		{
			name: "text delta",
			event: v2Event{ID: "evt_2", Type: "session.text.delta", Data: map[string]any{
				"sessionID": "ses_1", "assistantMessageID": "msg_1", "ordinal": float64(0), "delta": "你好",
			}},
			wantType: "output.delta",
			check: func(t *testing.T, payload map[string]any) {
				if payload["part_id"] != "text:msg_1:0" || payload["text"] != "你好" || payload["message_id"] != "msg_1" {
					t.Fatalf("unexpected delta payload: %#v", payload)
				}
			},
		},
		{
			name: "text ended",
			event: v2Event{ID: "evt_3", Type: "session.text.ended", Data: map[string]any{
				"sessionID": "ses_1", "assistantMessageID": "msg_1", "ordinal": float64(0), "text": "你好",
			}},
			wantType: "provider.event",
			check: func(t *testing.T, payload map[string]any) {
				if payload["kind"] != "text" || payload["part_id"] != "text:msg_1:0" || payload["text"] != "你好" {
					t.Fatalf("unexpected text payload: %#v", payload)
				}
			},
		},
		{
			name: "reasoning delta",
			event: v2Event{ID: "evt_4", Type: "session.reasoning.delta", Data: map[string]any{
				"sessionID": "ses_1", "assistantMessageID": "msg_1", "ordinal": float64(1), "delta": "想",
			}},
			wantType: "provider.event",
			check: func(t *testing.T, payload map[string]any) {
				if payload["kind"] != "reasoning" || payload["part_id"] != "reasoning:msg_1:1" {
					t.Fatalf("unexpected reasoning payload: %#v", payload)
				}
			},
		},
		{
			name: "tool success",
			event: v2Event{ID: "evt_5", Type: "session.tool.success", Data: map[string]any{
				"sessionID": "ses_1", "assistantMessageID": "msg_1", "id": "call_1", "name": "shell",
				"content": []any{map[string]any{"type": "text", "text": "hi"}}, "metadata": map[string]any{"exit": float64(0)},
			}},
			wantType: "provider.event",
			check: func(t *testing.T, payload map[string]any) {
				if payload["kind"] != "tool" || payload["phase"] != "success" || payload["call_id"] != "call_1" || payload["name"] != "shell" {
					t.Fatalf("unexpected tool payload: %#v", payload)
				}
			},
		},
		{
			name: "usage updated",
			event: v2Event{ID: "evt_6", Type: "session.usage.updated", Data: map[string]any{
				"sessionID": "ses_1", "cost": 0.01,
				"tokens": map[string]any{"input": float64(100), "output": float64(10), "reasoning": float64(2), "cache": map[string]any{"read": float64(5), "write": float64(1)}},
			}},
			wantType: "usage.updated",
			check: func(t *testing.T, payload map[string]any) {
				if payload["total_tokens"] != int64(118) || payload["accounting_mode"] != "delta" {
					t.Fatalf("unexpected usage payload: %#v", payload)
				}
			},
		},
		{
			name:     "execution succeeded",
			event:    v2Event{ID: "evt_7", Type: "session.execution.succeeded", Data: map[string]any{"sessionID": "ses_1"}},
			wantType: "run.completed",
			check: func(t *testing.T, payload map[string]any) {
				if payload["status"] != "completed" {
					t.Fatalf("unexpected terminal payload: %#v", payload)
				}
			},
		},
		{
			name:     "execution failed",
			event:    v2Event{ID: "evt_8", Type: "session.execution.failed", Data: map[string]any{"sessionID": "ses_1", "message": "boom"}},
			wantType: "run.error",
			check: func(t *testing.T, payload map[string]any) {
				if payload["status"] != "failed" {
					t.Fatalf("unexpected error payload: %#v", payload)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events, sink := captureSink()
			if err := client.handleEvent(context.Background(), state, sink, test.event); err != nil {
				t.Fatal(err)
			}
			if len(*events) != 1 {
				t.Fatalf("expected one event, got %#v", *events)
			}
			if (*events)[0].Type != test.wantType {
				t.Fatalf("event type = %q, want %q", (*events)[0].Type, test.wantType)
			}
			test.check(t, (*events)[0].Payload)
		})
	}
}

func TestPermissionAsksBecomeApprovals(t *testing.T) {
	client := testClient(t)
	state := &runState{request: provider.RunRequest{RunID: "run-1"}, session: "ses_1", compacted: map[string]string{}}
	events, sink := captureSink()
	event := v2Event{ID: "evt_1", Type: "permission.asked", Data: map[string]any{
		"sessionID": "ses_1", "id": "per_1", "action": "shell", "resources": []any{"ls"},
	}}
	if err := client.handleEvent(context.Background(), state, sink, event); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || (*events)[0].Type != "approval.requested" {
		t.Fatalf("expected approval request, got %#v", *events)
	}
	if (*events)[0].Payload["request_id"] != "per_1" || (*events)[0].Payload["action_kind"] != "shell" {
		t.Fatalf("unexpected approval payload: %#v", (*events)[0].Payload)
	}
	if _, ok := client.approvals["per_1"]; !ok {
		t.Fatal("approval must be pending")
	}
}

func TestReadOnlyPermissionAsksAreRejectedNatively(t *testing.T) {
	client := testClient(t)
	state := &runState{request: provider.RunRequest{RunID: "run-1", ReadOnly: true}, session: "ses_1", compacted: map[string]string{}}
	events, sink := captureSink()
	event := v2Event{ID: "evt_1", Type: "permission.asked", Data: map[string]any{
		"sessionID": "ses_1", "id": "per_1", "action": "shell",
	}}
	if err := client.handleEvent(context.Background(), state, sink, event); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatalf("read-only runs must not surface approvals: %#v", *events)
	}
	if _, ok := client.approvals["per_1"]; ok {
		t.Fatal("read-only approval must not be stored")
	}
}

func TestUsageDeltasAccumulatePerSession(t *testing.T) {
	client := testClient(t)
	state := &runState{request: provider.RunRequest{RunID: "run-1"}, session: "ses_1", compacted: map[string]string{}}
	events, sink := captureSink()
	emit := func(input, output float64) {
		event := v2Event{ID: "evt", Type: "session.usage.updated", Data: map[string]any{
			"sessionID": "ses_1", "tokens": map[string]any{"input": input, "output": output, "reasoning": float64(0), "cache": map[string]any{"read": float64(0), "write": float64(0)}},
		}}
		if err := client.handleEvent(context.Background(), state, sink, event); err != nil {
			t.Fatal(err)
		}
	}
	emit(100, 10)
	emit(160, 30)
	if len(*events) != 2 {
		t.Fatalf("expected two usage deltas, got %#v", *events)
	}
	if (*events)[0].Payload["total_tokens"] != int64(110) || (*events)[1].Payload["total_tokens"] != int64(80) {
		t.Fatalf("unexpected usage deltas: %#v", *events)
	}
}

func TestListModelsDeduplicatesProviderScopedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/model" {
			http.NotFound(w, r)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "opencode" || password == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "deepseek-flash", "modelID": "deepseek-flash", "providerID": "enmooy"},
			{"id": "deepseek-flash", "modelID": "deepseek-flash", "providerID": "enmooy"},
			{"id": "gpt-6", "modelID": "gpt-6", "providerID": "openai"},
		}})
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	models, err := client.listModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "enmooy/deepseek-flash,openai/gpt-6" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestVerifySessionDistinguishesMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_ok":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "ses_ok"}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"_tag":"NotFoundError"}`))
		}
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	ok, err := client.verifySession(context.Background(), "ses_ok")
	if err != nil || !ok {
		t.Fatalf("expected session to verify: ok=%v err=%v", ok, err)
	}
	ok, err = client.verifySession(context.Background(), "ses_missing")
	if err != nil || ok {
		t.Fatalf("expected missing session: ok=%v err=%v", ok, err)
	}
}

func TestInterruptAndSteerRequestShapes(t *testing.T) {
	type request struct {
		Method string
		Path   string
		Body   map[string]any
	}
	requests := make([]request, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, request{Method: r.Method, Path: r.URL.Path, Body: body})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	if err := client.interruptSession(context.Background(), "ses_1"); err != nil {
		t.Fatal(err)
	}
	if err := client.steerSession(context.Background(), "ses_1", map[string]any{"text": "补充", "delivery": "steer"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %#v", requests)
	}
	if requests[0].Method != http.MethodPost || requests[0].Path != "/api/session/ses_1/interrupt" {
		t.Fatalf("unexpected interrupt request: %#v", requests[0])
	}
	if requests[1].Path != "/api/session/ses_1/prompt" || requests[1].Body["delivery"] != "steer" {
		t.Fatalf("unexpected steer request: %#v", requests[1])
	}
}

func TestStreamEventsFiltersSessionsAndCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/event" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(event v2Event) {
			payload, _ := json.Marshal(event)
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(v2Event{ID: "evt_other", Type: "session.text.delta", Data: map[string]any{"sessionID": "ses_other", "delta": "noise"}})
		write(v2Event{ID: "evt_1", Type: "session.execution.started", Data: map[string]any{"sessionID": "ses_1"}})
		write(v2Event{ID: "evt_2", Type: "session.text.delta", Data: map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "ordinal": float64(0), "delta": "ok"}})
		write(v2Event{ID: "evt_3", Type: "session.execution.succeeded", Data: map[string]any{"sessionID": "ses_1"}})
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	state := &runState{request: provider.RunRequest{RunID: "run-1"}, session: "ses_1", compacted: map[string]string{}}
	events, sink := captureSink()
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go client.streamEvents(context.Background(), state, sink, ready, done)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event stream did not finish")
	}
	if len(*events) != 3 {
		t.Fatalf("unexpected events: %#v", *events)
	}
	if (*events)[0].Type != "run.started" || (*events)[1].Type != "output.delta" || (*events)[2].Type != "run.completed" {
		t.Fatalf("unexpected event sequence: %#v", *events)
	}
}

func TestServerProcessReceivesAuthEnvironment(t *testing.T) {
	isolateUserHomes(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	executable := filepath.Join(dir, "fake-opencode")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then printf '2.0.test\n'; exit 0; fi
env | grep -E '^OPENCODE_(SERVER_)?PASSWORD=' | sort > %q
exit 1
`, envPath)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(executable)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = client.Probe(ctx)
	payload, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("fake server did not run: %v", err)
	}
	for _, name := range []string{"OPENCODE_PASSWORD", "OPENCODE_SERVER_PASSWORD"} {
		if !strings.Contains(string(payload), name+"="+client.password+"\n") {
			t.Fatalf("server process missing %s", name)
		}
	}
}

func vendoredBinary(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("TEAMVA_OPENCODE_BINARY"); configured != "" {
		return configured
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	candidate := filepath.Join(root, "vendor", "opencode", runtime.GOOS+"-"+runtime.GOARCH, "opencode")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	return ""
}

// TestVendoredBinaryLifecycle exercises the real bundled runtime without any
// model call. Run with TEAMVA_OPENCODE_TEST=1 after
// scripts/vendor/opencode.sh.
func TestVendoredBinaryLifecycle(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 to run against the vendored runtime")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Skip("vendored opencode binary not found; run scripts/vendor/opencode.sh")
	}
	isolateNativeFixtureEnvironment(t)
	client, err := New(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	dir := t.TempDir()
	client.WithConfigDir(dir)
	client.WithDataDir(filepath.Join(dir, "data"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	manifest, steps, err := client.Diagnose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Available || !strings.Contains(manifest.ProviderVersion, "2.") {
		t.Fatalf("unexpected manifest: %#v steps=%#v", manifest, steps)
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session", map[string]any{
		"title": "Teamva lifecycle test", "location": map[string]any{"directory": dir},
	}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.ID == "" {
		t.Fatal("session create returned no id")
	}
	ok, err := client.VerifySession(ctx, created.Data.ID)
	if err != nil || !ok {
		t.Fatalf("session must verify: ok=%v err=%v", ok, err)
	}
	ok, err = client.VerifySession(ctx, "ses_missing")
	if err != nil || ok {
		t.Fatalf("missing session must not verify: ok=%v err=%v", ok, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestVendoredBinaryRunsPrompt runs one real prompt through the vendored
// runtime. It needs the user's configured credentials and an explicit model,
// for example:
//
//	TEAMVA_OPENCODE_TEST=1 \
//	TEAMVA_OPENCODE_MODEL=enmooy/deepseek-flash go test ./provider/opencode/
func TestVendoredBinaryRunsPrompt(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 and TEAMVA_OPENCODE_MODEL to run")
	}
	model := os.Getenv("TEAMVA_OPENCODE_MODEL")
	binary := vendoredBinary(t)
	if model == "" || binary == "" {
		t.Skip("TEAMVA_OPENCODE_MODEL and the vendored binary are required")
	}
	configDir := t.TempDir()
	if err := WriteConfig(configDir, provider.InstanceConfig{SystemPrompt: "只回复两个字：收到"}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(configDir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if source := authSource(); source != "" {
		if err := provider.LinkAuth(source, filepath.Join(dataDir, "opencode", "auth.json")); err != nil {
			t.Logf("auth link unavailable: %v", err)
		}
	}
	client, err := New(binary)
	if err != nil {
		t.Fatal(err)
	}
	client.WithConfigDir(configDir)
	client.WithDataDir(dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	events, sink := captureSink()
	session, err := client.Start(ctx, provider.RunRequest{
		RunID: "run-vendored-test", CWD: configDir, Prompt: "你好", Model: model,
	}, sink)
	if err != nil {
		t.Fatalf("start failed: %v (events=%#v)", err, *events)
	}
	if session.ID == "" {
		t.Fatal("run returned no native session")
	}
	var output, completed bool
	for _, event := range *events {
		if event.Type == "output.delta" {
			output = true
		}
		if event.Type == "run.completed" {
			completed = true
		}
	}
	if !output || !completed {
		t.Fatalf("expected streamed output and completion: %#v", *events)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}
