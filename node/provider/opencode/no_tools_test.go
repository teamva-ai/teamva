package opencode

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestNoToolsHasNoNativeExceptionOrMCPAndKeepsBoundInstructions(t *testing.T) {
	isolateUserHomes(t)
	dir, workspace := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dir, "skills", "review", "SKILL.md"), "bound-skill-prompt-fixture")
	writeFile(t, filepath.Join(workspace, "AGENTS.md"), "workspace-prompt-fixture")
	config := provider.InstanceConfig{
		SystemPrompt: "Only return JSON", DisableTools: true, ComputerUse: true,
		WritableRepositories: []string{workspace}, WorkspaceInstructions: filepath.Join(workspace, "AGENTS.md"),
		Skills:     []nodeprotocol.SkillPayload{{Slug: "review"}},
		MCPServers: map[string]json.RawMessage{"custom": json.RawMessage(`{"type":"local","command":["must-not-start"]}`)},
	}
	adapter, err := newInstance(provider.InstanceOptions{ConfigDir: dir, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client := adapter.(*Client)
	client.sandboxReady = true // Even an available OS sandbox must not grant tools.
	for _, request := range []provider.RunRequest{
		{CWD: workspace, ToolRequestDir: workspace},
		{CWD: workspace, ReadOnly: true, ToolRequestDir: workspace},
	} {
		rules := client.permissionRules(request)
		if len(rules) != 1 || rules[0]["action"] != "*" || rules[0]["resource"] != "*" || rules[0]["effect"] != "deny" {
			t.Fatal("immutable NoTools boundary acquired a native/IPC/Skill exception")
		}
	}
	cfg := readConfig(t, dir)
	if cfg["mcp"] != nil || client.computerUse {
		t.Fatal("NoTools injected MCP/Computer Use")
	}
	options := cfg["plugins"].([]any)[0].(map[string]any)["options"].(map[string]any)
	texts := options["instructions"].([]any)
	if options["disableTools"] != true || len(texts) != 2 || texts[1] != "bound-skill-prompt-fixture" {
		t.Fatal("NoTools lost bound Skill prompt text or immutable plugin flag")
	}
	if cfg["agents"].(map[string]any)["build"].(map[string]any)["system"] != config.SystemPrompt {
		t.Fatal("NoTools lost native system override")
	}
	// A request may narrow a normal instance too, but never relax a NoTools one.
	normal := testClient(t)
	if rules := normal.permissionRules(provider.RunRequest{DisableTools: true}); len(rules) != 1 || rules[0]["action"] != "*" {
		t.Fatal("per-request NoTools did not deny all native tools")
	}
	if _, err := normal.Start(context.Background(), provider.RunRequest{DisableTools: true}, nil); err == nil || !strings.Contains(err.Error(), "tool-free instance") {
		t.Fatal("NoTools started a tool-enabled instance (potential startup MCP injection)")
	}
	events, sink := captureSink()
	if err := client.handleEvent(context.Background(), &runState{request: provider.RunRequest{RunID: "fixture"}}, sink,
		v2Event{Type: "session.tool.success", Data: map[string]any{"name": "read"}}); err == nil || len(*events) != 0 {
		t.Fatal("a broken native NoTools boundary was projected as successful tool execution")
	}
}

func TestNoToolsDSMLRemainsInvalidPlainAssistantText(t *testing.T) {
	client := testClient(t)
	client.disableTools = true
	state := &runState{request: provider.RunRequest{RunID: "offline-dsml", DisableTools: true}, session: "fixture"}
	text := `<｜DSML｜function_calls><｜DSML｜invoke name="Bash"><｜DSML｜parameter name="command">echo fixture</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜function_calls>`
	events, sink := captureSink()
	if err := client.handleEvent(context.Background(), state, sink, v2Event{Type: "session.text.delta", Data: map[string]any{"delta": text}}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || (*events)[0].Type != "output.delta" || (*events)[0].Payload["text"] != text || json.Valid([]byte(text)) {
		t.Fatal("DSML text was interpreted as a native tool or a valid audit verdict")
	}
}
