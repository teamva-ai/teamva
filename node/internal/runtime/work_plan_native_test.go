package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Exercise GenerateWorkPlan itself with the real bundled CLI, real private
// configuration and native HTTP/SSE projection. Only the model is synthetic.
func TestWorkPlanBundledOpenCodeOffline(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for real bundled Runtime work-plan acceptance (localhost model only)")
	}
	_, source, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository bundled runtime")
	}
	binary := filepath.Join(filepath.Dir(source), "..", "..", "..", "vendor", "opencode", goruntime.GOOS+"-"+goruntime.GOARCH, "opencode")
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatal("repository bundled OpenCode is missing or not executable")
	}

	// Match the Provider's native fixture convention, before constructing any
	// Provider: preserve PATH/temp only, never ambient auth, plugins or proxies.
	pathEnv, tempEnv := os.Getenv("PATH"), os.Getenv("TMPDIR")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		t.Setenv(name, "")
	}
	t.Setenv("PATH", pathEnv)
	t.Setenv("TMPDIR", tempEnv)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	for variable, directory := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache"} {
		t.Setenv(variable, filepath.Join(home, directory))
	}
	writeFixture := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	project := filepath.Join(base, "project")
	projectFile, hostFile := filepath.Join(project, "keep.txt"), filepath.Join(base, "host", "keep.txt")
	writeFixture(projectFile, "project unchanged")
	writeFixture(hostFile, "host unchanged")
	writeFixture(filepath.Join(project, "AGENTS.md"), "WORK_PLAN_NATIVE_BUSINESS_INSTRUCTIONS_MUST_NOT_LEAK")
	writeFixture(filepath.Join(home, ".agents", "skills", "ambient-work-plan", "SKILL.md"), "---\nname: ambient-work-plan\ndescription: WORK_PLAN_NATIVE_AMBIENT_SKILL_MUST_NOT_LEAK\n---\nNever inject this unbound Skill into a draft.")

	// Reuse only the invariant assertions/hooks, not its fake Provider factory.
	engine := newWorkPlanRuntime(t)
	if engine.titleManagerFactory != nil {
		t.Fatal("native fixture acquired a fake Manager")
	}
	if err := os.MkdirAll(engine.workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mode := "valid"
	primaryCalls, auxiliaryCalls, metadataCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models.json" {
			mu.Lock()
			metadataCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected native fixture HTTP endpoint")
			w.WriteHeader(404)
			return
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
			t.Error("invalid native model request")
			w.WriteHeader(400)
			return
		}
		system, allText, primary := "", "", false
		for _, message := range body.Messages {
			text := workPlanNativeContent(message.Content)
			allText += text
			if message.Role == "system" {
				system += text
			}
			if message.Role == "user" {
				var input struct {
					Input  string                       `json:"input"`
					Agents []nodeprotocol.WorkPlanAgent `json:"agents"`
				}
				if json.Unmarshal([]byte(text), &input) == nil && strings.HasPrefix(input.Input, "WORK_PLAN_NATIVE_INPUT_") {
					primary = true
					if !reflect.DeepEqual(input.Agents, workPlanRequest().Agents) {
						t.Error("native request changed the explicit Agent allowlist")
					}
				}
			}
		}
		if len(body.Tools) != 0 {
			t.Error("real outbound model request advertised tools")
		}
		if body.Model != "work-plan-native-fixture" {
			t.Error("native request selected a non-fixture model")
		}
		mu.Lock()
		current := mode
		if primary {
			primaryCalls++
		} else {
			auxiliaryCalls++
		}
		call := primaryCalls
		mu.Unlock()
		if primary {
			if !strings.Contains(system, workPlanSystemPrompt) || strings.Contains(system, "You are an AI agent running in OpenCode, a coding agent harness.") {
				t.Error("fixed work-plan system was absent or native coding system survived")
			}
			for _, forbidden := range []string{"# Code Mode", "tools.opencode", "<available_skills>", "WORK_PLAN_NATIVE_AMBIENT_SKILL_MUST_NOT_LEAK", "WORK_PLAN_NATIVE_BUSINESS_INSTRUCTIONS_MUST_NOT_LEAK"} {
				if strings.Contains(allText, forbidden) {
					t.Error("native work-plan request included tools, workspace instructions or an unbound Skill")
				}
			}
			if call == 1 && (strings.Contains(allText, project) || strings.Contains(allText, hostFile)) {
				t.Error("native draft received an unrelated business/host filesystem scope")
			}
			assertWorkPlanNativeScope(t, engine)
		}
		if primary && current == "upstream-failure" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "offline upstream failure must not become a draft: " + workPlanFixture}})
			return
		}
		text := workPlanFixture
		if !primary {
			text = "Offline work plan"
		} // The native auxiliary title agent also uses this local model.
		delta := map[string]any{"role": "assistant", "content": text}
		finish := "stop"
		if primary && current == "hostile-tools" && call == 1 {
			calls := []map[string]any{}
			add := func(name string, input any) {
				arguments, _ := json.Marshal(input)
				calls = append(calls, map[string]any{"index": len(calls), "id": fmt.Sprintf("rogue-%d", len(calls)), "type": "function", "function": map[string]any{"name": name, "arguments": string(arguments)}})
			}
			add("write", map[string]any{"path": projectFile, "content": "must not overwrite project"})
			add("write", map[string]any{"path": hostFile, "content": "must not overwrite host"})
			add("shell", map[string]any{"command": "printf compromised > " + workPlanNativeShellQuote(hostFile), "workdir": project, "timeout": 1000})
			add("execute", map[string]any{"code": "return await tools.opencode.session_list({})"})
			add("issue_comment_create", map[string]any{"body": "must not activate another Agent", "mentioned_agent_ids": []string{"a"}})
			delta = map[string]any{"role": "assistant", "tool_calls": calls}
			finish = "tool_calls"
		}
		// If a broken boundary continued after hostile calls, this valid answer
		// must NOT rescue the failed attempt into a successful draft.
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			data, _ := json.Marshal(map[string]any{"id": "work-plan-native", "object": "chat.completion.chunk", "model": body.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		chunk(delta, nil)
		chunk(map[string]any{}, finish)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// The wrapper only supplies offline metadata flags, then execs the exact
	// repository binary. Provider configuration/transport are not mocked.
	wrapper := filepath.Join(base, "offline-opencode")
	script := "#!/bin/sh\nexport OPENCODE_DISABLE_MODELS_FETCH=1\nexport OPENCODE_MODELS_URL=" + workPlanNativeShellQuote(server.URL+"/models.json") + "\nexec " + workPlanNativeShellQuote(binary) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	engine.executables["opencode"] = wrapper
	definition, ok := catalog.Lookup("opencode")
	if !ok || definition.ModelSetup == nil {
		t.Fatal("bundled Provider ModelSetup missing")
	}
	view, err := definition.ModelSetup().Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "work-plan-native-fixture", APIKey: "offline-work-plan-only"})
	if err != nil {
		t.Fatal("cannot configure isolated localhost model")
	}
	// Also pin the auxiliary title/default model, never a public/free endpoint.
	config, _ := json.Marshal(map[string]any{"model": view.ModelRef})
	writeFixture(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "opencode.json"), string(config))
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, wrapper, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "opencode v2.0.18" {
		t.Fatalf("fixture requires real bundled OpenCode 2.0.18: version=%q err=%v", strings.TrimSpace(string(version)), err)
	}
	beforeProject, beforeHost, beforeHome := workPlanNativeTree(t, project), workPlanNativeTree(t, filepath.Dir(hostFile)), workPlanNativeTree(t, home)
	beforeWorkspace := workPlanNativeTree(t, engine.workspaceRoot)
	for _, stage := range []string{"valid", "hostile-tools", "upstream-failure"} {
		t.Run(stage, func(t *testing.T) {
			mu.Lock()
			mode, primaryCalls, auxiliaryCalls = stage, 0, 0
			mu.Unlock()
			request := workPlanRequest()
			request.ExecutionID, request.Provider, request.Model, request.Input = "native-"+stage, "opencode", view.ModelRef, "WORK_PLAN_NATIVE_INPUT_"+stage
			result, err := engine.GenerateWorkPlan(ctx, request)
			mu.Lock()
			primary, auxiliary := primaryCalls, auxiliaryCalls
			mu.Unlock()
			if primary == 0 {
				t.Error("GenerateWorkPlan never reached the real outbound model HTTP")
			}
			if stage == "valid" {
				expected, decodeErr := nodeprotocol.DecodeWorkPlan(workPlanFixture, request.Agents)
				if err != nil || decodeErr != nil || !reflect.DeepEqual(result, expected) {
					t.Errorf("real native work-plan JSON was not decoded: %v", err)
				}
			} else if !errors.Is(err, errTitleGeneration) || len(result.WorkItems) != 0 {
				t.Errorf("failed native attempt became a draft or leaked a non-generic error: %v", err)
			}
			assertWorkPlanNoDurableEffects(t, engine)
			if !reflect.DeepEqual(beforeProject, workPlanNativeTree(t, project)) || !reflect.DeepEqual(beforeHost, workPlanNativeTree(t, filepath.Dir(hostFile))) || !reflect.DeepEqual(beforeHome, workPlanNativeTree(t, home)) || !reflect.DeepEqual(beforeWorkspace, workPlanNativeTree(t, engine.workspaceRoot)) {
				t.Error("native draft modified project/host files or synthetic account home")
			}
			t.Logf("real Runtime.GenerateWorkPlan: primary model HTTP=%d auxiliary local title HTTP=%d; business DB/files unchanged", primary, auxiliary)
		})
	}
	mu.Lock()
	metadata := metadataCalls
	mu.Unlock()
	t.Logf("bundled 2.0.18, isolated HOME/config/data, localhost fake model and metadata only (metadata HTTP=%d)", metadata)
}

func workPlanNativeContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		if part.Type == "text" {
			text += part.Text
		}
	}
	return text
}

func workPlanNativeShellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'"
}

func assertWorkPlanNativeScope(t *testing.T, engine *Runtime) {
	t.Helper()
	attempts, err := os.ReadDir(engine.titleDirectories.path)
	if err != nil || len(attempts) != 1 {
		t.Error("native draft did not own exactly one isolated attempt")
		return
	}
	attempt := filepath.Join(engine.titleDirectories.path, attempts[0].Name())
	entries, err := os.ReadDir(filepath.Join(attempt, "workspace"))
	if err != nil || len(entries) != 0 {
		t.Error("native draft acquired workspace files, instructions or IPC")
	}
	configs := 0
	err = filepath.WalkDir(filepath.Join(attempt, "manager"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "opencode.json" {
			return nil
		}
		configs++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var config struct {
			Agents map[string]struct {
				System string `json:"system"`
			} `json:"agents"`
			Skills      []json.RawMessage `json:"skills"`
			MCP         json.RawMessage   `json:"mcp"`
			Permissions []struct {
				Action   string `json:"action"`
				Resource string `json:"resource"`
				Effect   string `json:"effect"`
			} `json:"permissions"`
			Plugins []struct {
				Options struct {
					DisableTools bool              `json:"disableTools"`
					Instructions []string          `json:"instructions"`
					BoundSkills  []json.RawMessage `json:"boundSkills"`
				} `json:"options"`
			} `json:"plugins"`
		}
		if json.Unmarshal(data, &config) != nil {
			t.Error("invalid actual native private configuration")
			return nil
		}
		if config.Agents["build"].System != workPlanSystemPrompt || len(config.Skills) != 0 || len(config.MCP) != 0 || len(config.Permissions) != 1 || config.Permissions[0].Action != "*" || config.Permissions[0].Resource != "*" || config.Permissions[0].Effect != "deny" {
			t.Error("actual native configuration acquired non-draft capabilities")
		}
		if len(config.Plugins) != 1 || !config.Plugins[0].Options.DisableTools || len(config.Plugins[0].Options.Instructions) != 0 || len(config.Plugins[0].Options.BoundSkills) != 0 {
			t.Error("actual native guard lost NoTools or received workspace/Skill instructions")
		}
		return nil
	})
	if err != nil || configs != 1 {
		t.Error("could not verify the real private native configuration")
	}
}

func workPlanNativeTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[rel] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[rel] += fmt.Sprintf(":%x", sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal("cannot snapshot synthetic native fixture files")
	}
	return files
}
