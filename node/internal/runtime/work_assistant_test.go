package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestWorkAssistantRuntimeScopeAndReadOnlyBridge(t *testing.T) {
	db, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	engine := New("computer", db, nil, root, nil)
	defer engine.Close()
	provider := &fakeAdapter{onStart: func(r adapter.RunRequest) {
		if !r.ReadOnly || !strings.HasPrefix(r.CWD, filepath.Join(root, "home")+string(filepath.Separator)) || r.ToolRequestDir == "" {
			t.Error("assistant missing private read-only workspace")
		}
		if !strings.Contains(r.Prompt, "work_records_query") || strings.Contains(r.Prompt, "issue_comment_create") {
			t.Error("assistant received mutation tools")
		}
	}}
	engine.RegisterTestAdapter("fake", provider)
	start := nodeprotocol.StartRunPayload{RunID: "run_assistant", AssistantSessionID: "session_one", Provider: "fake", LeaseEpoch: 1, ReadOnly: true, Prompt: "what did I do"}
	command := func(id string, p nodeprotocol.StartRunPayload) nodeprotocol.CommandEnvelope {
		raw, _ := json.Marshal(p)
		return nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: id, Name: "run.start", ComputerID: "computer", SentAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Payload: raw}
	}
	result, _, err := engine.Handle(t.Context(), command("cmd_assistant", start))
	if err != nil || result.Status != "completed" {
		t.Fatalf("start %s %v", result.Status, err)
	}
	for index, change := range []func(*nodeprotocol.StartRunPayload){func(p *nodeprotocol.StartRunPayload) { p.ReadOnly = false }, func(p *nodeprotocol.StartRunPayload) { p.ComputerUse = true }, func(p *nodeprotocol.StartRunPayload) { p.ProjectID = "business" }, func(p *nodeprotocol.StartRunPayload) { p.IssueID = "issue" }, func(p *nodeprotocol.StartRunPayload) { p.AssistantSessionID = "../../escape" }} {
		p := start
		p.RunID = "run_invalid_" + string(rune('a'+index))
		change(&p)
		result, _, err := engine.Handle(t.Context(), command(p.RunID, p))
		if err == nil && result.Status == "completed" {
			t.Fatal("invalid assistant scope accepted")
		}
	}
	broker := NewToolBroker(nil, "run_tools", "", "", "agent")
	broker.workAssistant = true
	bridge, err := NewFileBridge(t.TempDir(), "run_tools", broker, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.dispatch(t.Context(), "issue_comment_create", map[string]any{"body": "denied"}); err == nil {
		t.Fatal("bridge exposed a write tool")
	}
}

func newWorkAssistantSkillRuntime(t *testing.T) (*Runtime, nodeprotocol.StartRunPayload, string) {
	t.Helper()
	root := t.TempDir()
	db, err := localstore.Open(t.Context(), filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := New("computer", db, nil, filepath.Join(root, "workspaces"), nil)
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close assistant runtime: %v", err)
		}
	})
	installed := filepath.Join(root, "skills", "assistant_skill")
	if err := os.MkdirAll(filepath.Join(installed, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":            "---\nname: assistant_skill\ndescription: review records\n---\nRead references/rules.md.\n",
		"references/rules.md": "Summarize saved work records without modifying them.\n",
	} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := inspectSkillTree(installed)
	if err != nil {
		t.Fatal(err)
	}
	return engine, nodeprotocol.StartRunPayload{
		RunID: "run_assistant_skill", AssistantSessionID: "session_skill", AgentID: "agent",
		Provider: "fake", LeaseEpoch: 1, ReadOnly: true, Prompt: "Review my work",
		SystemPrompt: "Use my configured review instructions", Model: "test-model",
		Skills: []nodeprotocol.SkillPayload{{Slug: "assistant_skill", Name: "Review", ContentDigest: tree.digest}},
	}, installed
}

func workAssistantCommand(t *testing.T, name, id string, payload any) nodeprotocol.CommandEnvelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	return nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: id, Name: name, ComputerID: "computer",
		SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: raw,
	}
}

func assertWorkAssistantSkillRequest(t *testing.T, engine *Runtime, payload nodeprotocol.StartRunPayload, request adapter.RunRequest) {
	t.Helper()
	home := filepath.Join(engine.workspaceRoot, "home", keyDirectorySegment(payload.AssistantSessionID))
	if request.CWD != home || !request.ReadOnly || request.NativeSessionID != "" || request.IssueID != "" {
		t.Errorf("assistant escaped its independent read-only home: %+v", request)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Error(err)
	}
	if request.ToolRequestDir != filepath.Join(canonicalHome, ".teamva", "tool-requests", payload.RunID) {
		t.Errorf("unexpected assistant IPC directory: %q", request.ToolRequestDir)
	}
	if !strings.Contains(request.Prompt, "work_records_query") || strings.Contains(request.Prompt, "issue_comment_create") || strings.Contains(request.Prompt, "issue_comment_update") {
		t.Error("configured Skills expanded the assistant tool catalog")
	}
	engine.managersMu.Lock()
	manager := engine.managers[payload.Provider]
	engine.managersMu.Unlock()
	manager.mu.Lock()
	key, bound := manager.runs[payload.RunID]
	instance := manager.instances[key]
	manager.mu.Unlock()
	wantKey := InstanceKey{InstanceID: "assistant-" + payload.AssistantSessionID, Generation: payload.RunID, GitScopeHash: "work-assistant-read-only-v1"}
	if !bound || key != wantKey || instance == nil {
		t.Errorf("assistant lost its authorization/session scope: key=%+v bound=%v", key, bound)
		return
	}
	// Skills are provider instance configuration, not RunRequest fields. Check
	// both the actual Manager configuration fingerprint and its complete private
	// Skill tree while fakeAdapter.Start is running, not just decoded payloads.
	wantDigest, err := instanceConfigDigest(InstanceConfig{
		Model: payload.Model, SystemPrompt: payload.SystemPrompt, Skills: payload.Skills,
		WorkspaceInstructions: filepath.Join(home, "AGENTS.md"), ReadOnly: true,
	})
	if err != nil || instance.configDigest != wantDigest {
		t.Error("Provider configuration lost Skills or widened read-only/MCP/write permissions")
	}
	if instance.configDir == home || instance.configDir != manager.instanceDir(wantKey) {
		t.Error("Skills were not isolated in the scoped Provider configuration")
	}
	for _, skill := range payload.Skills {
		copied := filepath.Join(instance.configDir, "skills", skill.Slug)
		tree, err := inspectSkillTree(copied)
		if err != nil || tree.digest != skill.ContentDigest || len(tree.skillFiles) != 1 || len(tree.files) != 2 {
			t.Errorf("Provider did not receive the complete verified Skill: tree=%+v err=%v", tree, err)
		}
	}
}

func assertWorkAssistantReleased(t *testing.T, engine *Runtime, runID string) {
	t.Helper()
	engine.managersMu.Lock()
	manager := engine.managers["fake"]
	engine.managersMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.runs[runID]; exists {
		t.Error("assistant left a Run bound to its Provider")
	}
	for _, instance := range manager.instances {
		if instance.activeRuns != 0 {
			t.Error("assistant left an active Provider instance")
		}
	}
}

func TestWorkAssistantRuntimeStartsWithVerifiedSkills(t *testing.T) {
	engine, start, installed := newWorkAssistantSkillRuntime(t)
	provider := &fakeAdapter{onStart: func(request adapter.RunRequest) {
		assertWorkAssistantSkillRequest(t, engine, start, request)
	}}
	engine.RegisterTestAdapter("fake", provider)
	engine.SetMCPResolver(func(context.Context, string) (map[string]json.RawMessage, error) {
		t.Error("read-only assistant invoked the custom MCP resolver")
		return nil, nil
	})
	oldKey := InstanceKey{InstanceID: "assistant-" + start.AssistantSessionID, Generation: start.RunID}
	if err := engine.saveGenerationSession(t.Context(), oldKey, "fake", "legacy-native-session"); err != nil {
		t.Fatal(err)
	}
	result, _, err := engine.Handle(t.Context(), workAssistantCommand(t, "run.start", "assistant-skills-start", start))
	if err != nil || result.Status != "completed" {
		t.Fatalf("configured assistant did not start: status=%s err=%v", result.Status, err)
	}
	provider.mu.Lock()
	starts, resumes := provider.start, provider.resume
	provider.mu.Unlock()
	if starts != 1 || resumes != 0 {
		t.Fatalf("assistant starts=%d resumes=%d; must not resume old authorization", starts, resumes)
	}
	newKey := oldKey
	newKey.GitScopeHash = "work-assistant-read-only-v1"
	if saved, found, err := engine.loadGenerationSession(t.Context(), newKey, "fake"); err != nil || !found || saved != "native_1" {
		t.Fatalf("assistant session not saved under its read-only scope: %q %v %v", saved, found, err)
	}
	if saved, found, err := engine.loadGenerationSession(t.Context(), oldKey, "fake"); err != nil || !found || saved != "legacy-native-session" {
		t.Fatal("assistant overwrote a native session with different authorization")
	}
	assertWorkAssistantReleased(t, engine, start.RunID)
	ipc := filepath.Join(engine.workspaceRoot, "home", keyDirectorySegment(start.AssistantSessionID), ".teamva", "tool-requests", start.RunID)
	if _, err := os.Stat(ipc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("assistant IPC was not removed after completion: %v", err)
	}
	if tree, err := inspectSkillTree(installed); err != nil || tree.digest != start.Skills[0].ContentDigest {
		t.Fatal("assistant changed the installed Skill")
	}
}

func TestWorkAssistantRuntimeRejectsUnsafeSkillsAndScopes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *nodeprotocol.StartRunPayload, string)
	}{
		{"read_only_false", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.ReadOnly = false }},
		{"project", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.ProjectID = "business" }},
		{"issue", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.IssueID = "issue" }},
		{"conversation", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.ConversationNodeID = "conversation" }},
		{"computer_use", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.ComputerUse = true }},
		{"disable_tools", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.DisableTools = true }},
		{"native_session", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.NativeSessionID = "native_1" }},
		{"git_resources", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) {
			p.GitResources = []nodeprotocol.GitResourceRequest{{Path: "/outside"}}
		}},
		{"assistant_directory", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.AssistantSessionID = "../../escape" }},
		{"skill_directory_traversal", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.Skills[0].Slug = "../assistant_skill" }},
		{"skill_sanitized_alias", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.Skills[0].Slug = "assistant/skill" }},
		{"skill_absolute_directory", func(_ *testing.T, p *nodeprotocol.StartRunPayload, installed string) { p.Skills[0].Slug = installed }},
		{"skill_digest", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) {
			p.Skills[0].ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{"skill_missing", func(_ *testing.T, p *nodeprotocol.StartRunPayload, _ string) { p.Skills[0].Slug = "not-installed" }},
		{"skill_symlink", func(t *testing.T, _ *nodeprotocol.StartRunPayload, installed string) {
			if err := os.Symlink(filepath.Join(installed, "SKILL.md"), filepath.Join(installed, "references", "escape.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"skill_root_symlink", func(t *testing.T, _ *nodeprotocol.StartRunPayload, installed string) {
			if err := os.Rename(installed, installed+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(installed+"-original", installed); err != nil {
				t.Fatal(err)
			}
		}},
		{"skill_modified_content", func(t *testing.T, _ *nodeprotocol.StartRunPayload, installed string) {
			if err := os.WriteFile(filepath.Join(installed, "references", "rules.md"), []byte("modified after binding"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, start, installed := newWorkAssistantSkillRuntime(t)
			provider := &fakeAdapter{}
			engine.RegisterTestAdapter("fake", provider)
			test.mutate(t, &start, installed)
			result, _, err := engine.Handle(t.Context(), workAssistantCommand(t, "run.start", "unsafe-assistant", start))
			if err == nil || result.Status != "failed" {
				t.Fatalf("unsafe assistant accepted: status=%s err=%v", result.Status, err)
			}
			provider.mu.Lock()
			starts, resumes := provider.start, provider.resume
			provider.mu.Unlock()
			if starts != 0 || resumes != 0 {
				t.Fatalf("unsafe assistant reached Provider: starts=%d resumes=%d", starts, resumes)
			}
			assertWorkAssistantReleased(t, engine, start.RunID)
		})
	}
	for _, field := range []string{"path", "directory", "content"} {
		t.Run("unknown_skill_"+field, func(t *testing.T) {
			engine, start, _ := newWorkAssistantSkillRuntime(t)
			provider := &fakeAdapter{}
			engine.RegisterTestAdapter("fake", provider)
			raw, err := json.Marshal(start)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			payload["skills"].([]any)[0].(map[string]any)[field] = "../../outside"
			result, _, err := engine.Handle(t.Context(), workAssistantCommand(t, "run.start", "unsafe-skill-field", payload))
			if err == nil || result.Status != "failed" || errorCode(err) != "invalid_command" {
				t.Fatalf("arbitrary Skill %s accepted: status=%s err=%v", field, result.Status, err)
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if provider.start != 0 || provider.resume != 0 {
				t.Fatal("arbitrary Skill filesystem fields reached Provider")
			}
		})
	}
}

func TestWorkAssistantRuntimeStopsWithSkillsAndCleansIPC(t *testing.T) {
	engine, start, installed := newWorkAssistantSkillRuntime(t)
	started := make(chan struct{})
	provider := &fakeAdapter{waitForCancel: true, onStart: func(request adapter.RunRequest) {
		assertWorkAssistantSkillRequest(t, engine, start, request)
		close(started)
	}}
	engine.RegisterTestAdapter("fake", provider)
	command := workAssistantCommand(t, "run.start", "assistant-skill-blocked", start)
	done := make(chan error, 1)
	go func() {
		_, _, err := engine.Handle(t.Context(), command)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("configured assistant never reached Provider Start")
	}
	stop := workAssistantCommand(t, "run.stop", "assistant-skill-stop", nodeprotocol.StopRunPayload{RunID: start.RunID, Provider: "fake", LeaseEpoch: 1})
	result, _, err := engine.Handle(t.Context(), stop)
	if err != nil || result.Status != "completed" {
		t.Fatalf("assistant stop failed: status=%s err=%v", result.Status, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped assistant error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped assistant did not release Provider Start")
	}
	assertWorkAssistantReleased(t, engine, start.RunID)
	ipc := filepath.Join(engine.workspaceRoot, "home", keyDirectorySegment(start.AssistantSessionID), ".teamva", "tool-requests", start.RunID)
	if _, err := os.Stat(ipc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped assistant retained transient IPC: %v", err)
	}
	if tree, err := inspectSkillTree(installed); err != nil || tree.digest != start.Skills[0].ContentDigest {
		t.Fatal("stopping assistant changed the installed Skill")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.closed != 1 {
		t.Fatalf("Provider cleanup count=%d, want 1", provider.closed)
	}
}
