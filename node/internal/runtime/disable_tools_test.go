package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestDisableToolsReachesProviderAndSkipsCapabilitiesWithoutDisablingReadOnlyAssistant(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	observed := make(chan adapter.RunRequest, 5)
	engine.RegisterTestAdapter("fake", &fakeAdapter{onStart: func(request adapter.RunRequest) { observed <- request }})
	resolverCalls := 0
	engine.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		resolverCalls++
		return nil, nil
	})
	for _, mode := range []struct {
		name                                string
		readonly, noTools, issue, assistant bool
	}{
		{"audit", true, true, false, false},
		{"branch", true, true, false, false},
		{"plain-no-tools", false, true, true, false},
		{"assistant", true, false, false, true},
		{"issue", false, false, true, false},
	} {
		payload := nodeprotocol.StartRunPayload{RunID: mode.name, ProjectID: "project", Provider: "fake", LeaseEpoch: 1,
			ReadOnly: mode.readonly, DisableTools: mode.noTools, ComputerUse: mode.noTools, Prompt: "fixture"}
		if mode.issue {
			payload.IssueID = "issue"
		}
		if mode.assistant {
			payload.AssistantSessionID, payload.ProjectID = "assistant", ""
		}
		runIPCBridgeCommand(t, engine, payload)
		request := <-observed
		if request.DisableTools != mode.noTools || request.ReadOnly != mode.readonly {
			t.Fatalf("%s: RunRequest lost distinct ReadOnly/DisableTools flags", mode.name)
		}
		if (request.ToolRequestDir != "") != (mode.assistant || (mode.issue && !mode.noTools)) {
			t.Fatalf("%s: native IPC capability did not follow NoTools", mode.name)
		}
		if mode.noTools && strings.Contains(request.Prompt, "issue_comment_create") {
			t.Fatal("NoTools received FileBridge instructions")
		}
	}
	if resolverCalls != 1 {
		t.Fatalf("custom MCP resolver invoked %d times, want ordinary Issue only", resolverCalls)
	}
}

func TestNoToolsFlagChangesInstanceDigestAndReplacesOnlyIdleNativeCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	manager := NewManager("opencode", "unused-offline-executable", t.TempDir(), nil, log.New(io.Discard, "", 0))
	defer manager.Close()
	key := InstanceKey{InstanceID: "instance", Generation: "generation", ProjectID: "project"}
	config := InstanceConfig{SystemPrompt: "fixture", ReadOnly: true}
	first, _, err := manager.Get(t.Context(), "first", key, config)
	if err != nil {
		t.Fatal(err)
	}
	initialDigest, _ := instanceConfigDigest(config)
	config.DisableTools = true
	config.ComputerUse = true
	config.MCPServers = map[string]json.RawMessage{"blocked": json.RawMessage(`{"type":"local","command":["must-not-start"]}`)}
	narrowed := config
	narrowed.ComputerUse, narrowed.MCPServers = false, nil
	narrowedDigest, _ := instanceConfigDigest(narrowed)
	if initialDigest == narrowedDigest {
		t.Fatal("instance digest ignored DisableTools")
	}
	path := filepath.Join(manager.instanceDir(key), "opencode", "opencode.json")
	before, _ := os.ReadFile(path)
	if _, _, err := manager.Get(t.Context(), "busy", key, config); err == nil {
		t.Fatal("busy catalog allowed NoTools role change")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("busy role change rewrote active native config")
	}
	manager.ReleaseRun("first")
	second, _, err := manager.Get(t.Context(), "second", key, config)
	if err != nil || second == first {
		t.Fatalf("NoTools reused a tool-enabled adapter: %v", err)
	}
	if manager.instances[key].configDigest != narrowedDigest {
		t.Fatal("manager did not freeze exact NoTools/MCP/ComputerUse snapshot")
	}
	after, _ = os.ReadFile(path)
	if strings.Contains(string(after), "must-not-start") || strings.Contains(string(after), "open-computer-use") {
		t.Fatal("NoTools injected MCP or Computer Use")
	}
	manager.ReleaseRun("second")
	config.DisableTools, config.ComputerUse, config.MCPServers = false, false, nil
	third, _, err := manager.Get(t.Context(), "third", key, config)
	if err != nil || third == second {
		t.Fatalf("legitimate read-only assistant retained tool-free native catalog: %v", err)
	}
	manager.ReleaseRun("third")
}
