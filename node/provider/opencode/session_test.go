package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestSessionCreateResolvesModelBeforeSubmitting(t *testing.T) {
	for _, test := range []struct {
		name           string
		explicit       string
		configured     any
		defaultModel   any
		catalog        []map[string]any
		want           modelReference
		wantDefaults   int32
		wantCatalogues int32
	}{
		{name: "explicit model skips discovery", explicit: "selected/model", want: modelReference{ProviderID: "selected", ID: "model"}},
		{name: "explicit model variant skips discovery", explicit: "selected/model#high", want: modelReference{ProviderID: "selected", ID: "model", Variant: "high"}},
		{name: "explicit root selection survives missing catalogue entry", configured: "configured/unavailable", want: modelReference{ProviderID: "configured", ID: "unavailable"}},
		{name: "explicit root object preserves variant", configured: map[string]string{"providerID": "configured", "model": "alias", "variant": "high"},
			want: modelReference{ProviderID: "configured", ID: "alias", Variant: "high"}},
		{name: "native candidate is accepted only when in catalogue", defaultModel: map[string]any{"providerID": "configured", "id": "alias", "modelID": "upstream-id"},
			catalog: []map[string]any{{"providerID": "configured", "id": "alias", "modelID": "upstream-id"}},
			want:    modelReference{ProviderID: "configured", ID: "alias"}, wantDefaults: 1, wantCatalogues: 1},
		{name: "non-null obsolete native default is not a user selection", defaultModel: map[string]any{"providerID": "obsolete", "id": "absent"},
			catalog: []map[string]any{{"providerID": "current", "id": "dynamic"}},
			want:    modelReference{ProviderID: "current", ID: "dynamic"}, wantDefaults: 2, wantCatalogues: 1},
		{name: "null default chooses first current catalogue id", catalog: []map[string]any{
			{"providerID": "current", "id": "dynamic-alias", "modelID": "different-upstream-id"},
			{"providerID": "another", "id": "second", "modelID": "second"},
		}, want: modelReference{ProviderID: "current", ID: "dynamic-alias"}, wantDefaults: 2, wantCatalogues: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var defaults, catalogues, creates atomic.Int32
			request := provider.RunRequest{CWD: "/fixture/work tree", RunID: "default-model", Model: test.explicit, ReadOnly: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/config":
					entries := []any{}
					if test.configured != nil {
						entries = append(entries, map[string]any{"type": "document", "info": map[string]any{"model": test.configured}})
					}
					_ = json.NewEncoder(w).Encode(entries)
				case "/api/model/default", "/api/model":
					if r.URL.Query().Get("location[directory]") != request.CWD {
						t.Error("model selection did not use the session location")
					}
					if r.URL.Path == "/api/model/default" {
						defaults.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"data": test.defaultModel})
					} else {
						catalogues.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"data": test.catalog})
					}
				case "/api/session":
					creates.Add(1)
					var body struct {
						Model       modelReference      `json:"model"`
						Agent       string              `json:"agent"`
						Permissions []map[string]string `json:"permissions"`
					}
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != test.want || body.Agent != "build" {
						t.Errorf("session model/agent not explicitly set: %+v", body)
					}
					if permissionEffect(body.Permissions, "edit", "business.txt") != "deny" {
						t.Error("default model selection dropped read-only permissions")
					}
					_, _ = w.Write([]byte(`{"data":{"id":"session-created"}}`))
				default:
					t.Error("unexpected endpoint (model inference must not occur)")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			sessionID, baseline, err := client.prepareSession(t.Context(), request)
			if err != nil || sessionID != "session-created" || baseline != (usageTotals{}) {
				t.Fatalf("session preparation failed: id=%q baseline=%+v err=%v", sessionID, baseline, err)
			}
			if creates.Load() != 1 || defaults.Load() != test.wantDefaults || catalogues.Load() != test.wantCatalogues {
				t.Fatalf("unexpected requests: creates=%d defaults=%d catalogues=%d", creates.Load(), defaults.Load(), catalogues.Load())
			}
		})
	}
}

func TestConfiguredModelUsesHighestPriorityNativeRootSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" || r.URL.Query().Get("location[directory]") != "/fixture" {
			t.Error("explicit configuration was replaced by automatic discovery")
		}
		_, _ = w.Write([]byte(`[
			{"type":"document","info":{"model":"lower/ignored"}},
			{"type":"directory","path":"/fixture"},
			{"type":"document","info":{"model":{"providerID":"higher","model":"chosen","variant":"high"}}},
			{"type":"document","info":{"shell":"/bin/sh"}}
		]`))
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	model, err := client.resolveModel(t.Context(), provider.RunRequest{CWD: "/fixture"})
	if err != nil || model != (modelReference{ProviderID: "higher", ID: "chosen", Variant: "high"}) {
		t.Fatalf("native root configuration priority/shape lost: model=%+v err=%v", model, err)
	}
}

func TestInvalidConfiguredModelDoesNotSilentlySelectAnotherProvider(t *testing.T) {
	for _, model := range []string{`null`, `"invalid"`, `{}`, `{"providerID":"only-provider"}`} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/config" {
					t.Error("invalid root selection silently triggered fallback")
				}
				_, _ = w.Write([]byte(`[{"type":"document","info":{"model":` + model + `}}]`))
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			if _, err := client.resolveModel(t.Context(), provider.RunRequest{CWD: "/fixture"}); err == nil {
				t.Fatal("invalid configured root selection was accepted")
			}
		})
	}
}

func TestDefaultModelWaitsForCatalogueAndRechecksNativePreference(t *testing.T) {
	for _, becomesDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalogue fallback", true: "native preference settles during warmup"}[becomesDefault], func(t *testing.T) {
			var catalogues, defaults atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/config" {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				var data any
				if r.URL.Path == "/api/model/default" {
					if defaults.Add(1) > 1 && becomesDefault {
						data = map[string]string{"providerID": "preferred", "id": "configured"}
					}
				} else if r.URL.Path == "/api/model" {
					data = []any{}
					if catalogues.Add(1) >= 2 {
						data = []any{map[string]string{"providerID": "fresh", "id": "current"}}
						if becomesDefault {
							data = append(data.([]any), map[string]string{"providerID": "preferred", "id": "configured"})
						}
					}
				} else {
					t.Error("unexpected request")
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			model, err := client.resolveModel(t.Context(), provider.RunRequest{CWD: "/fixture"})
			want := modelReference{ProviderID: "fresh", ID: "current"}
			if becomesDefault {
				want = modelReference{ProviderID: "preferred", ID: "configured"}
			}
			if err != nil || model != want || catalogues.Load() != 2 || defaults.Load() != 2 {
				t.Fatalf("warmup/default precedence failed: model=%+v err=%v catalogue=%d defaults=%d", model, err, catalogues.Load(), defaults.Load())
			}
		})
	}
}

func TestDefaultModelSelectionFailsClosedWithoutFallbackRetries(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit string
		status   int
		response string
		want     string
	}{
		{name: "invalid explicit selection", explicit: "not-provider-scoped", want: "provider/model"},
		{name: "native error", status: http.StatusServiceUnavailable, response: `{}`, want: "HTTP 503"},
		{name: "missing envelope data", response: `{}`, want: "default model response invalid"},
		{name: "wrong default shape", response: `{"data":"provider/model"}`, want: "default model response invalid"},
		{name: "incomplete default", response: `{"data":{"id":"model"}}`, want: "default model response invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/config" {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				requests.Add(1)
				if r.URL.Path != "/api/model/default" {
					t.Error("failed selection silently fell back or submitted a session/prompt")
				}
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			_, _, err := client.prepareSession(t.Context(), provider.RunRequest{CWD: "/fixture", Model: test.explicit})
			wantRequests := int32(1)
			if test.explicit != "" {
				wantRequests = 0
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || requests.Load() != wantRequests {
				t.Fatalf("selection failure swallowed/retried: err=%v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestDefaultModelEmptyCatalogueFailsBeforeSessionCreate(t *testing.T) {
	var catalogues atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			_, _ = w.Write([]byte(`[]`))
		case "/api/model/default":
			_, _ = w.Write([]byte(`{"data":{"providerID":"fallback","id":"absent-from-catalogue"}}`))
		case "/api/model":
			catalogues.Add(1)
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Error("empty catalogue submitted a session or inference request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	started := time.Now()
	_, _, err := client.prepareSession(t.Context(), provider.RunRequest{CWD: "/fixture"})
	if err == nil || !strings.Contains(err.Error(), "no available model") || time.Since(started) < modelListWarmup || catalogues.Load() < 2 {
		t.Fatalf("empty catalogue did not fail after bounded warmup: err=%v requests=%d", err, catalogues.Load())
	}
}

func TestSessionCreateFailureDoesNotChangeSelectedProvider(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session" {
			t.Error("failed explicit request triggered fallback discovery")
		}
		creates.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"model unavailable"}`))
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	_, _, err := client.prepareSession(t.Context(), provider.RunRequest{CWD: "/fixture", Model: "selected/unavailable"})
	if err == nil || creates.Load() != 1 {
		t.Fatalf("failed selection was retried: err=%v creates=%d", err, creates.Load())
	}
}

func TestResumePreservesExplicitModelAndRepairsOnlyUnselectedModel(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    any
		explicit string
		want     modelReference
	}{
		{name: "preserve durable selection", model: modelReference{ProviderID: "old", ID: "still-selected", Variant: "high"}, explicit: "new/ignored"},
		{name: "null model receives explicit request", explicit: "selected/model", want: modelReference{ProviderID: "selected", ID: "model"}},
		{name: "null model receives current default", want: modelReference{ProviderID: "current", ID: "dynamic"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var switches, patches, defaults atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/config":
					_, _ = w.Write([]byte(`[]`))
				case r.URL.Path == "/api/model":
					_, _ = w.Write([]byte(`{"data":[{"providerID":"current","id":"dynamic"}]}`))
				case r.URL.Path == "/api/session/existing" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
						"id": "existing", "agent": "build", "model": test.model,
						"location": map[string]string{"directory": "/fixture"}, "tokens": usageFixtureTokens(100, 10, 2, 30, 4),
					}})
				case r.URL.Path == "/api/session/existing" && r.Method == http.MethodPatch:
					patches.Add(1)
					var body struct {
						Permissions []map[string]string `json:"permissions"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || permissionEffect(body.Permissions, "edit", "business.txt") != "deny" {
						t.Error("Resume dropped current read-only permissions")
					}
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/api/session/existing/model" && r.Method == http.MethodPost:
					switches.Add(1)
					var body struct {
						Model modelReference `json:"model"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != test.want || patches.Load() != 1 {
						t.Errorf("wrong model switch/body/order: %+v", body)
					}
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/api/model/default":
					defaults.Add(1)
					_, _ = w.Write([]byte(`{"data":{"providerID":"current","id":"dynamic"}}`))
				default:
					t.Error("unexpected endpoint or inference request")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			id, baseline, err := client.prepareSession(t.Context(), provider.RunRequest{NativeSessionID: "existing", CWD: "/fixture", Model: test.explicit, ReadOnly: true})
			if err != nil || id != "existing" || baseline != (usageTotals{input: 100, output: 10, reasoning: 2, cacheRead: 30, cacheWrite: 4}) || patches.Load() != 1 {
				t.Fatalf("Resume changed session/usage: id=%q baseline=%+v patches=%d err=%v", id, baseline, patches.Load(), err)
			}
			wantSwitches, wantDefaults := int32(1), int32(0)
			if test.model != nil {
				wantSwitches = 0
			} else if test.explicit == "" {
				wantDefaults = 1
			}
			if switches.Load() != wantSwitches || defaults.Load() != wantDefaults {
				t.Fatalf("Resume changed an existing selection: switches=%d defaults=%d", switches.Load(), defaults.Load())
			}
		})
	}
}

func TestResumeInvalidBaselineOrScopeDoesNotResolveOrSwitchModel(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"missing baseline": {"id": "existing", "location": map[string]string{"directory": "/fixture"}},
		"changed location": {"id": "existing", "location": map[string]string{"directory": "/other"}, "tokens": usageFixtureTokens(0, 0, 0, 0, 0)},
		"invalid model": {"id": "existing", "location": map[string]string{"directory": "/fixture"}, "tokens": usageFixtureTokens(0, 0, 0, 0, 0),
			"model": map[string]string{"id": "incomplete"}},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/session/existing" || r.Method != http.MethodGet {
					t.Error("invalid session was mutated or submitted")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			_, _, err := client.prepareSession(t.Context(), provider.RunRequest{NativeSessionID: "existing", CWD: "/fixture"})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("invalid Resume continued: err=%v requests=%d", err, calls.Load())
			}
		})
	}
}

func TestDefaultModelWarmupHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config" {
			_, _ = w.Write([]byte(`[]`))
		} else if r.URL.Path == "/api/model/default" {
			_, _ = w.Write([]byte(`{"data":null}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := client.resolveModel(ctx, provider.RunRequest{CWD: "/fixture"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("warmup ignored caller cancellation: %v", err)
	}
}

func TestListModelsUsesCatalogIDForAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"providerID":"custom","id":"first-alias","modelID":"upstream"},{"providerID":"custom","id":"second-alias","modelID":"upstream"},{"providerID":"custom","modelID":"missing-catalog-id"}]}`))
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	models, err := client.listModels(t.Context())
	if err != nil || !reflect.DeepEqual(models, []string{"custom/first-alias", "custom/second-alias"}) {
		t.Fatalf("catalog aliases collapsed into upstream IDs: models=%v err=%v", models, err)
	}
}
