package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Presence is an HTTP contract, not a change to Manager CAS or Provider default
// resolution. All requests below use real loopback TCP, disposable SQLite and
// either a private temporary Manager or a non-executing model capability relay.
type systemPresenceMCP struct {
	*mcpmanager.Manager
	saves atomic.Int64
}

func (m *systemPresenceMCP) Save(ctx context.Context, input mcpmanager.Input) (mcpmanager.View, error) {
	m.saves.Add(1)
	return m.Manager.Save(ctx, input)
}

func systemPresenceMutationCounts(t *testing.T, repo *testRepository) [4]int64 {
	t.Helper()
	var counts [4]int64
	if err := repo.db.QueryRowContext(t.Context(), `SELECT
		(SELECT count(*) FROM audit_events), (SELECT count(*) FROM command_outbox),
		(SELECT count(*) FROM app_metadata), (SELECT count(*) FROM work_records)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
		t.Fatal(err)
	}
	return counts
}

func systemPresencePrivateSnapshot(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal("read disposable MCP configuration snapshot")
	}
	return raw
}

func systemPresenceReject(t *testing.T, s *systemSurfaceHTTP, method, path string, body any) {
	t.Helper()
	response, status, err := s.exchange(t.Context(), method, path, body, s.auth)
	if err != nil {
		t.Fatal("presence HTTP exchange failed")
	}
	if status != 400 {
		t.Errorf("presence contract accepted invalid input: status=%d want=400", status)
	}
	if response.Error == nil || response.Error.Code != "invalid_argument" {
		t.Error("presence rejection did not use the generic safe contract error")
	}
	if response.Recorder.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Recorder.Body.String(), systemSurfaceSecret) {
		t.Fatal("presence response cached or exposed private fixture")
	}
}

func TestSystemRequestPresenceMCPSaveVersionBeforePrivateManager(t *testing.T) {
	repo := newTestRepository(t)
	dir := t.TempDir()
	private, err := mcpmanager.New(mcpmanager.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = private.Close() })
	manager := &systemPresenceMCP{Manager: private}
	s := newSystemSurfaceHTTP(t, repo, nil, manager)
	// Reusing this header after rejection must not consume a creation or cache an
	// invalid result. These endpoints must not create a durable command at all.
	s.auth["Idempotency-Key"] = "system-presence-mcp-same-attempt"
	input := func() map[string]any {
		return map[string]any{
			"name": "presence", "scope": "user", "version": int64(0),
			"config": map[string]any{"type": "remote", "url": "http://127.0.0.1:1/mcp", "disabled": true,
				"headers":     map[string]string{"Authorization": systemSurfaceSecret},
				"environment": map[string]string{}, "oauth": map[string]any{}},
		}
	}
	for _, scenario := range []struct {
		name, field string
		value       any
		omit        bool
	}{
		{"version-missing", "version", nil, true},
		{"version-negative", "version", -1, false},
		{"version-null", "version", nil, false},
		{"version-string", "version", "0", false},
		{"version-fraction", "version", 0.5, false},
		{"version-bool", "version", false, false},
		{"version-overflow", "version", json.Number("9223372036854775808"), false},
		{"name-missing", "name", nil, true},
		{"scope-missing", "scope", nil, true},
		{"config-missing", "config", nil, true},
		{"config-null", "config", nil, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			body := input()
			if scenario.omit {
				delete(body, scenario.field)
			} else {
				body[scenario.field] = scenario.value
			}
			beforeCalls := manager.saves.Load()
			beforeCounts := systemPresenceMutationCounts(t, repo)
			beforePrivate := systemPresencePrivateSnapshot(t, filepath.Join(dir, "mcp", "config.json"))
			systemPresenceReject(t, s, "POST", "/v1/mcp/servers", body)
			if manager.saves.Load() != beforeCalls {
				t.Error("invalid presence input reached private MCP Manager.Save")
			}
			if after := systemPresenceMutationCounts(t, repo); after != beforeCounts {
				t.Error("invalid presence input mutated SQLite metadata, command or audit records")
			}
			if after := systemPresencePrivateSnapshot(t, filepath.Join(dir, "mcp", "config.json")); !bytes.Equal(beforePrivate, after) {
				t.Error("invalid presence input wrote private MCP configuration")
			}
		})
	}
	// Explicit zero remains a valid create and the private configuration keeps
	// legitimate empty maps. A masked full-entry update still uses native CAS.
	created := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers", input(), 200))
	if created.Version < 1 || created.Status != "disabled" || !strings.Contains(string(created.Config), mcpmanager.KeepSecret) {
		t.Fatal("explicit version zero or valid empty private objects were rejected/rewritten")
	}
	updated := decodeData[mcpmanager.View](t, s.call(t, "POST", "/v1/mcp/servers", created.Input, 200))
	if updated.Version != created.Version+1 {
		t.Fatal("presence DTO changed native Manager version semantics")
	}
	s.call(t, "POST", "/v1/mcp/servers", created.Input, 409)
	effective, err := private.Effective(t.Context(), "")
	if err != nil || !strings.Contains(string(effective["presence"]), systemSurfaceSecret) {
		t.Fatal("masked DTO round trip lost private secret")
	}
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

func TestSystemRequestPresenceMCPTargetOptionalPositiveSnapshotOnly(t *testing.T) {
	repo := newTestRepository(t)
	manager := &stubMCP{}
	s := newSystemSurfaceHTTP(t, repo, nil, manager)
	s.auth["Idempotency-Key"] = "system-presence-action-same-attempt"
	for _, action := range []string{"connect", "disconnect", "auth", "revoke"} {
		t.Run(action, func(t *testing.T) {
			path := "/v1/mcp/servers/fixture/" + action
			for _, invalid := range []struct {
				name  string
				value any
			}{
				{"zero", 0}, {"negative", -1}, {"null", nil}, {"string", "1"},
				{"fraction", 1.5}, {"bool", true}, {"overflow", json.Number("9223372036854775808")},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					beforeCalls := len(manager.calls)
					beforeCounts := systemPresenceMutationCounts(t, repo)
					systemPresenceReject(t, s, "POST", path, map[string]any{"scope": "user", "version": invalid.value})
					if len(manager.calls) != beforeCalls {
						t.Error("invalid page snapshot version reached MCP action")
					}
					if after := systemPresenceMutationCounts(t, repo); after != beforeCounts {
						t.Error("invalid page snapshot mutated durable metadata")
					}
				})
			}
			for _, valid := range []map[string]any{
				{"scope": "user"}, {"scope": "user", "version": int64(1)}, {"scope": "user", "version": int64(9223372036854775807)},
			} {
				beforeCalls := len(manager.calls)
				s.call(t, "POST", path, valid, 200)
				if len(manager.calls) != beforeCalls+1 || manager.calls[beforeCalls].action != action ||
					manager.calls[beforeCalls].scope != "user" || manager.calls[beforeCalls].name != "fixture" {
					t.Fatal("optional/stale positive snapshot changed the action target or introduced CAS")
				}
			}
		})
	}
}

func TestSystemRequestPresenceModelRequiredFieldsAndExplicitEmptyDefaults(t *testing.T) {
	repo := newTestRepository(t)
	relay := &modelSetupRelay{}
	s := newSystemSurfaceHTTP(t, repo, relay, nil)
	s.auth["Idempotency-Key"] = "system-presence-model-same-attempt"
	input := func() map[string]any {
		return map[string]any{"preset_id": "deepseek", "base_url": "", "model": "fixture-model", "api_key": systemSurfaceSecret}
	}
	for _, operation := range []struct{ name, method, suffix string }{{"save", "PUT", ""}, {"test", "POST", "/test"}} {
		t.Run(operation.name, func(t *testing.T) {
			path := "/v1/system/programming-tools/opencode/model-providers" + operation.suffix
			for _, scenario := range []struct {
				name, field string
				value       any
				omit        bool
			}{
				{"preset-missing", "preset_id", nil, true}, {"base-missing", "base_url", nil, true}, {"model-missing", "model", nil, true},
				{"preset-null", "preset_id", nil, false}, {"base-null", "base_url", nil, false}, {"model-null", "model", nil, false},
				{"key-null", "api_key", nil, false}, {"base-overlong", "base_url", strings.Repeat("x", 2049), false},
				{"base-type", "base_url", 1, false}, {"model-type", "model", false, false}, {"unknown", "prompt", "not allowed", false},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					body := input()
					if scenario.omit {
						delete(body, scenario.field)
					} else {
						body[scenario.field] = scenario.value
					}
					beforeSaved, beforeTested := relay.saved, relay.tested
					beforeCounts := systemPresenceMutationCounts(t, repo)
					systemPresenceReject(t, s, operation.method, path, body)
					if relay.saved != beforeSaved || relay.tested != beforeTested {
						t.Error("invalid presence input reached model Provider relay")
					}
					if after := systemPresenceMutationCounts(t, repo); after != beforeCounts {
						t.Error("invalid model input mutated SQLite configuration/audit/outbox metadata")
					}
				})
			}
			// Explicit empty BaseURL is not omission: Provider preset-default logic
			// must still receive it unchanged. Model emptiness also stays Provider
			// validation, not an accidental required-on-string HTTP change.
			for _, draft := range []map[string]any{
				input(),
				{"preset_id": "deepseek", "base_url": "", "model": ""},
				{"preset_id": "deepseek", "base_url": "", "model": "fixture-model", "api_key": ""},
				{"preset_id": "custom", "base_url": "https://example.invalid/" + strings.Repeat("p", 2048-len("https://example.invalid/")), "model": "fixture-model", "api_key": systemSurfaceSecret},
			} {
				beforeSaved, beforeTested := relay.saved, relay.tested
				s.call(t, operation.method, path, draft, 200)
				want := nodeprotocol.ModelProviderInput{PresetID: draft["preset_id"].(string), BaseURL: draft["base_url"].(string), Model: draft["model"].(string)}
				if key, ok := draft["api_key"].(string); ok {
					want.APIKey = key
				}
				if relay.input != want || (operation.name == "save" && (relay.saved != beforeSaved+1 || relay.tested != beforeTested)) ||
					(operation.name == "test" && (relay.tested != beforeTested+1 || relay.saved != beforeSaved)) {
					t.Fatal("explicit empty/default or write-only input was rewritten, dropped or sent to the wrong capability")
				}
			}
		})
	}
	// The existing safe error projection remains intact after changing only the
	// HTTP DTO. No real Provider, model, filesystem configuration or network call.
	relay.failure = errors.New(systemSurfaceSecret)
	s.call(t, "POST", "/v1/system/programming-tools/opencode/model-providers/test", input(), 503)
	systemSurfaceAssertNoSQLiteSecret(t, repo)
}

var _ MCPManager = (*systemPresenceMCP)(nil)
