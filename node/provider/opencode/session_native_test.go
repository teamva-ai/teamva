package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Actual pinned native API/schema and session persistence, but no prompt,
// inference, real credentials, or real account files are used by this fixture.
func TestBundledDefaultModelSelectionWithoutInference(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for native default model verification (no inference)")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	var inferenceCalls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inferenceCalls.Add(1)
		t.Error("metadata-only fixture contacted the model endpoint")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer modelServer.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{
		PresetID: "custom", BaseURL: modelServer.URL + "/v1", Model: "default-selection-fixture", APIKey: "fixture-not-a-real-key",
	})
	if err != nil {
		t.Fatal("save isolated synthetic provider failed")
	}
	globalConfig, err := configHome()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(globalConfig, "opencode", "opencode.json"), quoteJSON(map[string]string{"model": view.ModelRef}))
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	configDir := permissionFixtureDirectory(t, base, "config")
	adapter, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: configDir,
		DataDir: permissionFixtureDirectory(t, base, "data"), Config: provider.InstanceConfig{ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	client := adapter.(*Client)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("native model fixture cleanup failed")
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	request := provider.RunRequest{CWD: cwd, ReadOnly: true, RunID: "metadata-default"}
	if err := client.ensureStartedScoped(ctx, &request); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyPermissionGuard(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	if err := client.verifySkillBindings(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	// Exercise native warmup before checking the configured default contract.
	models, err := client.listModelsAt(ctx, cwd)
	if err != nil || !slices.Contains(models, view.ModelRef) {
		t.Fatalf("synthetic model did not appear in native catalogue: err=%v", err)
	}
	want, _ := parseModelReference(view.ModelRef)
	model, err := client.defaultModel(ctx, cwd)
	if err != nil || model == nil || *model != want {
		t.Fatalf("native default envelope/ID contract differs: model=%+v err=%v", model, err)
	}
	id, baseline, err := client.prepareSession(ctx, request)
	if err != nil || id == "" || baseline != (usageTotals{}) {
		t.Fatalf("native default session failed: id=%q baseline=%+v err=%v", id, baseline, err)
	}
	// The pinned session API normalizes an omitted variant to "default".
	want.Variant = "default"
	check := func(id string, selected modelReference) {
		t.Helper()
		var session struct {
			Data struct {
				Model       modelReference      `json:"model"`
				Tokens      json.RawMessage     `json:"tokens"`
				Permissions []map[string]string `json:"permissions"`
			} `json:"data"`
		}
		if err := client.getJSON(ctx, "/api/session/"+id, &session); err != nil || session.Data.Model != selected {
			t.Fatalf("native durable model differs: model=%+v err=%v", session.Data.Model, err)
		}
		tokens, err := decodeUsageTotals(session.Data.Tokens)
		if err != nil || tokens != (usageTotals{}) || permissionEffect(session.Data.Permissions, "edit", "business.txt") != "deny" {
			t.Fatalf("session preparation changed usage or read-only rules: tokens=%+v err=%v", tokens, err)
		}
	}
	check(id, want)
	// Existing explicitly selected models must remain stable even when a new
	// request supplies another selection; metadata-only Resume sends no prompt.
	request.NativeSessionID, request.Model = id, "different/must-not-be-selected"
	if resumed, _, err := client.prepareSession(ctx, request); err != nil || resumed != id {
		t.Fatalf("native Resume changed the session: %v", err)
	}
	check(id, want)
	// Reproduce the old app's durable model=NULL session without a model call.
	var legacy struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := client.requestJSON(ctx, http.MethodPost, "/api/session", map[string]any{
		"location": map[string]string{"directory": cwd}, "agent": "build",
	}, &legacy); err != nil || legacy.Data.ID == "" {
		t.Fatal("prepare native unselected session failed")
	}
	request.NativeSessionID, request.Model = legacy.Data.ID, ""
	if resumed, _, err := client.prepareSession(ctx, request); err != nil || resumed != legacy.Data.ID {
		t.Fatalf("native unselected Resume failed: %v", err)
	}
	check(legacy.Data.ID, want)
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := client.getJSON(ctx, "/openapi.json", &spec); err != nil {
		t.Fatal(err)
	}
	var dataSchema struct {
		AnyOf []struct {
			Ref  string `json:"$ref"`
			Type string `json:"type"`
		} `json:"anyOf"`
	}
	data := spec.Paths["/api/model/default"].Get.Responses["200"].Content["application/json"].Schema.Properties["data"]
	if json.Unmarshal(data, &dataSchema) != nil || len(dataSchema.AnyOf) != 2 ||
		dataSchema.AnyOf[0].Ref != "#/components/schemas/Model.Info" || dataSchema.AnyOf[1].Type != "null" {
		t.Fatal("native model.default no longer returns an envelope containing Model.Info | null")
	}
	if inferenceCalls.Load() != 0 {
		t.Fatal("default selection triggered inference")
	}
	client.mu.Lock()
	process := client.process
	client.mu.Unlock()
	if process == nil {
		t.Fatal("native model fixture process exited unexpectedly")
	}
	if err := client.Close(); err != nil {
		t.Fatal("native model fixture cleanup failed")
	}
	select {
	case <-process.Done():
	case <-ctx.Done():
		t.Fatal("native model fixture process was not reaped")
	}
	t.Log("native default envelope, explicit create model, durable Resume, NULL-model repair and unchanged usage/permissions verified; zero inference requests")
}

// Unlike the configured fixture above, global/instance config has no root model
// or provider overrides. The native OPENCODE_MODELS_URL source replaces the
// bundled snapshot between default lookup and catalog lookup. Both responses
// are actual native API responses; no model endpoint/response is mocked.
func TestBundledUnconfiguredDefaultStaysInCurrentCatalogueWithoutInference(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for unconfigured native defaults (no inference)")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	base := permissionFixtureDirectory(t, t.TempDir())
	var inferenceCalls atomic.Int32
	refresh := make(chan struct{})
	var release sync.Once
	releaseRefresh := func() { release.Do(func() { close(refresh) }) }
	var firstDefault atomic.Pointer[modelReference]
	var intercepted atomic.Bool
	var catalog []byte
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api.json" {
			select {
			case <-refresh:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(catalog)
			case <-r.Context().Done():
			}
			return
		}
		inferenceCalls.Add(1)
		t.Error("metadata-only fixture contacted the model endpoint")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer func() { releaseRefresh(); modelServer.Close() }()
	models := map[string]any{}
	for i, id := range []string{"metadata-catalog-current", "metadata-catalog-second"} {
		models[id] = map[string]any{"id": id, "name": id, "release_date": fmt.Sprintf("2026-10-%02d", 26-i),
			"attachment": false, "reasoning": false, "tool_call": true, "cost": map[string]int{"input": 0, "output": 0},
			"limit": map[string]int{"context": 200000, "output": 32000}, "modalities": map[string]any{"input": []string{"text"}, "output": []string{"text"}}}
	}
	catalog, _ = json.Marshal(map[string]any{"opencode": map[string]any{"id": "opencode", "name": "metadata-only catalog fixture",
		"npm": "@ai-sdk/openai-compatible", "env": []string{}, "api": modelServer.URL + "/v1", "models": models}})
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	wrapper := filepath.Join(base, "opencode-catalog-fixture")
	writeFile(t, wrapper, "#!/bin/sh\nexport OPENCODE_MODELS_URL="+quote(modelServer.URL)+"\nexec "+quote(binary)+" \"$@\"\n")
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd := permissionFixtureDirectory(t, base, "workspace")
	configDir := permissionFixtureDirectory(t, base, "config")
	dataDir := permissionFixtureDirectory(t, base, "data")
	var prompts atomic.Int32
	newClient := func() *Client {
		t.Helper()
		adapter, err := newInstance(provider.InstanceOptions{Executable: wrapper, ConfigDir: configDir, DataDir: dataDir,
			Config: provider.InstanceConfig{ReadOnly: true}})
		if err != nil {
			t.Fatal(err)
		}
		client := adapter.(*Client)
		t.Cleanup(func() { _ = client.Close() })
		cfg := readConfig(t, configDir)
		if cfg["model"] != nil || cfg["provider"] != nil || cfg["providers"] != nil {
			t.Fatal("unconfigured fixture unexpectedly has a root model/provider override")
		}
		original := client.http.Transport
		client.http.Transport = usageFixtureTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/prompt") || strings.HasSuffix(r.URL.Path, "/generate") || strings.HasSuffix(r.URL.Path, "/command") {
				prompts.Add(1)
				return nil, fmt.Errorf("metadata-only fixture refuses inference")
			}
			if r.URL.Path == "/api/model/default" && intercepted.CompareAndSwap(false, true) {
				// Capture a REAL native default from the initial bundled snapshot,
				// then let the native async metadata refresh finish before the caller
				// sees it. This makes the otherwise timing-sensitive bug deterministic.
				response, err := original.RoundTrip(r)
				if err != nil {
					releaseRefresh()
					return nil, err
				}
				raw, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				var initial struct {
					Data *modelReference `json:"data"`
				}
				if err != nil || json.Unmarshal(raw, &initial) != nil || initial.Data == nil {
					releaseRefresh()
					return nil, fmt.Errorf("native initial fallback was not materialized")
				}
				firstDefault.Store(initial.Data)
				releaseRefresh()
				deadline := time.Now().Add(12 * time.Second)
				for {
					current, err := client.listModelsOnceAt(r.Context(), cwd)
					if err != nil {
						return nil, err
					}
					if len(current) == 2 && slices.Contains(current, "opencode/metadata-catalog-current") {
						response.Body = io.NopCloser(bytes.NewReader(raw))
						return response, nil
					}
					if time.Now().After(deadline) {
						return nil, fmt.Errorf("native metadata source did not refresh")
					}
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			return original.RoundTrip(r)
		})
		return client
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	client := newClient()
	for _, phase := range []string{"cold", "warm", "restarted"} {
		if phase == "restarted" {
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client = newClient()
		}
		request := provider.RunRequest{CWD: cwd, ReadOnly: true, RunID: "metadata-unconfigured-" + phase}
		if err := client.ensureStartedScoped(ctx, &request); err != nil {
			t.Fatal(err)
		}
		if err := client.verifyPermissionGuard(ctx, cwd); err != nil {
			t.Fatal(err)
		}
		if err := client.verifySkillBindings(ctx, cwd); err != nil {
			t.Fatal(err)
		}
		if configured, err := client.configuredModel(ctx, cwd); err != nil || configured != nil {
			t.Fatal("native config unexpectedly materialized a root selection")
		}
		id, baseline, err := client.prepareSession(ctx, request)
		if err != nil || id == "" || baseline != (usageTotals{}) {
			t.Fatalf("unconfigured native session failed: %v", err)
		}
		current, err := client.listModelsAt(ctx, cwd)
		if err != nil || len(current) == 0 {
			t.Fatalf("native refreshed catalog unavailable: %v", err)
		}
		initial := firstDefault.Load()
		if initial == nil {
			t.Fatal("native initial fallback was not observed")
		}
		candidate := initial.ProviderID + "/" + initial.ID
		t.Logf("phase=%s root-model=absent captured-native-default=%s default-in-current-catalog=%t catalog-first=%s count=%d",
			phase, candidate, slices.Contains(current, candidate), current[0], len(current))
		if slices.Contains(current, candidate) {
			t.Fatal("native catalog replacement did not produce the stale-default counterexample")
		}
		var session struct {
			Data struct {
				Model  modelReference  `json:"model"`
				Tokens json.RawMessage `json:"tokens"`
			} `json:"data"`
		}
		if err := client.getJSON(ctx, "/api/session/"+id, &session); err != nil {
			t.Fatal(err)
		}
		selected := session.Data.Model.ProviderID + "/" + session.Data.Model.ID
		if !slices.Contains(current, selected) {
			t.Fatalf("phase=%s selected model %s is absent from current native catalog", phase, selected)
		}
		tokens, err := decodeUsageTotals(session.Data.Tokens)
		if err != nil || tokens != (usageTotals{}) {
			t.Fatal("metadata-only session accumulated inference usage")
		}
		t.Logf("phase=%s selected=%s in-current-catalog=true usage=0", phase, selected)
	}
	if prompts.Load() != 0 || inferenceCalls.Load() != 0 {
		t.Fatal("metadata fixture attempted inference")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}
