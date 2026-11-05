package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestMCPResolverReadOnlyBoundaryAndPrivateSnapshot(t *testing.T) {
	r := &Runtime{}
	calls := 0
	source := map[string]json.RawMessage{
		"custom":            json.RawMessage(`{"type":"remote","url":"https://example.test/mcp","headers":{"Authorization":"private-test-token"}}`),
		"open-computer-use": json.RawMessage(`{"type":"local","command":["override"]}`),
	}
	r.SetMCPResolver(func(_ context.Context, project string) (map[string]json.RawMessage, error) {
		calls++
		if project != "project" {
			t.Fatal("resolver received wrong project")
		}
		return source, nil
	})
	got, err := r.resolveMCP(t.Context(), "", true)
	if err != nil || len(got) != 0 || calls != 0 {
		t.Fatal("read-only resolver was invoked")
	}
	got, err = r.resolveMCP(t.Context(), "project", false)
	if err != nil || len(got) != 1 || calls != 1 {
		t.Fatal("normal run did not receive its effective MCP")
	}
	source["custom"][0] = '['
	if !json.Valid(got["custom"]) {
		t.Fatal("resolver result was not copied into a private run snapshot")
	}
	r.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		return nil, errors.New("private-test-token")
	})
	if _, err := r.resolveMCP(t.Context(), "project", false); err == nil || strings.Contains(err.Error(), "private-test-token") {
		t.Fatal("resolver error was not sanitized")
	}
}

func TestManagedMCPRefreshRestartsOnlyIdleNativeInstance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, providerID := range []string{"opencode", "codex"} {
		t.Run(providerID, func(t *testing.T) {
			manager := NewManager(providerID, "unused-test-executable", t.TempDir(), nil, log.New(io.Discard, "", 0))
			defer manager.Close()
			key := InstanceKey{InstanceID: "instance", Generation: "gen", ProjectID: "project"}
			config := InstanceConfig{MCPServers: map[string]json.RawMessage{
				"custom": json.RawMessage(`{"type":"remote","url":"https://old.example.test/mcp"}`),
			}}
			first, _, err := manager.Get(t.Context(), "first", key, config)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(manager.instanceDir(key), "config.toml")
			if providerID == "opencode" {
				configPath = filepath.Join(manager.instanceDir(key), "opencode", "opencode.json")
			}
			before, err := os.ReadFile(configPath)
			if err != nil || !strings.Contains(string(before), "old.example.test") {
				t.Fatal("initial MCP not configured")
			}
			config.MCPServers["custom"] = json.RawMessage(`{"type":"remote","url":"https://new.example.test/mcp","headers":{"Authorization":"private-test-token"}}`)
			if _, _, err := manager.Get(t.Context(), "busy", key, config); err == nil {
				t.Fatal("busy instance permitted configuration replacement")
			}
			after, _ := os.ReadFile(configPath)
			if string(before) != string(after) || manager.instances[key].adapter != first {
				t.Fatal("busy MCP refresh changed active files or adapter")
			}
			manager.instances[key].session = "native-session-preserved"
			manager.ReleaseRun("first")
			second, session, err := manager.Get(t.Context(), "second", key, config)
			if err != nil || second == first || session != "native-session-preserved" {
				t.Fatal("idle native instance was reused without refreshing MCP")
			}
			after, _ = os.ReadFile(configPath)
			if !strings.Contains(string(after), "new.example.test") || strings.Contains(string(after), "old.example.test") {
				t.Fatal("next run did not receive updated MCP")
			}
			manifest, _ := os.ReadFile(filepath.Join(manager.instanceDir(key), instanceManifestName))
			if strings.Contains(string(manifest), "private-test-token") || strings.Contains(string(manifest), "new.example.test") {
				t.Fatal("MCP persisted into retention metadata")
			}
			manager.ReleaseRun("second")
			config.ReadOnly = true
			third, _, err := manager.Get(t.Context(), "readonly", key, config)
			if err != nil || third == second {
				t.Fatal("read-only transition retained native MCP instance")
			}
			after, _ = os.ReadFile(configPath)
			if strings.Contains(string(after), "private-test-token") || strings.Contains(string(after), "new.example.test") {
				t.Fatal("read-only transition retained arbitrary MCP")
			}
		})
	}
}

func TestRunMCPResolutionBypassesDurableCommandsAndEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := localstore.Open(t.Context(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("computer", store, nil, t.TempDir(), nil)
	defer engine.Close()
	engine.RegisterTestAdapter("fake", &fakeAdapter{})
	calls := 0
	engine.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		calls++
		return map[string]json.RawMessage{"remote": json.RawMessage(`{"type":"remote","url":"https://example.test/mcp","headers":{"Authorization":"private-test-token"}}`)}, nil
	})
	for i, readOnly := range []bool{false, true} {
		id := string(rune('a' + i))
		payload, _ := json.Marshal(nodeprotocol.StartRunPayload{RunID: "run_" + id, ProjectID: "project", Provider: "fake", LeaseEpoch: 1, ReadOnly: readOnly})
		record, _, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "cmd_" + id, Name: "run.start", ComputerID: "computer",
			SentAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Payload: payload,
		})
		if err != nil || record.Status != "completed" {
			t.Fatalf("MCP-backed run failed: %v status=%s", err, record.Status)
		}
	}
	if calls != 1 {
		t.Fatalf("resolver calls=%d, want only normal Run", calls)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "private-test-token") || strings.Contains(string(raw), "https://example.test/mcp") {
			t.Fatal("MCP credentials/configuration leaked into runtime DB/WAL")
		}
	}
}
