package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// This test never reads the user's HOME, Skill body, auth, or configuration.
// The real bundled CLI discovers only our synthetic HOME/project sentinels;
// the actual outbound model HTTP and native skill tool are then inspected.
func TestBundledBoundSkillsIsolationOffline(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for offline native Skill isolation")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	home := os.Getenv("HOME")
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	writeSkill := func(root, id string) {
		t.Helper()
		writeFile(t, filepath.Join(root, id, "SKILL.md"), "---\nname: "+id+"\ndescription: DESCRIPTION_"+id+"\n---\nBODY_"+id+"\n")
		writeFile(t, filepath.Join(root, id, "reference.txt"), "REFERENCE_"+id)
	}
	globalIDs := []string{"unbound-home-agents-sentinel", "unbound-home-claude-sentinel", "unbound-home-native-sentinel", "unbound-project-sentinel", "unbound-private-stale-sentinel", "unbound-private-native-sentinel"}
	writeSkill(filepath.Join(home, ".agents", "skills"), globalIDs[0])
	writeSkill(filepath.Join(home, ".claude", "skills"), globalIDs[1])
	writeSkill(filepath.Join(home, ".config", "opencode", "skills"), globalIDs[2])
	writeSkill(filepath.Join(cwd, ".opencode", "skills"), globalIDs[3])
	configDir, dataDir := permissionFixtureDirectory(t, base, "config"), permissionFixtureDirectory(t, base, "data")
	const alpha = "bound-alpha-sentinel"
	const beta = "bound-beta-sentinel"
	for _, id := range []string{alpha, beta} {
		writeFile(t, filepath.Join(home, ".agents", "skills", id, "SKILL.md"), "---\nname: "+id+"\ndescription: SHADOW_DESCRIPTION_"+id+"\n---\nSHADOW_BODY_"+id+"\n")
	}
	type stage struct {
		name                        string
		bound                       []string
		readonly, noTools, parallel bool
	}
	stages := []stage{
		{"cold-alpha", []string{alpha}, true, false, false},
		{"warm-alpha", []string{alpha}, true, false, false},
		{"recreated-alpha", []string{alpha}, true, false, false},
		{"rebound-beta", []string{beta}, true, false, false},
		{"zero-binding", nil, true, false, false},
		{"no-tools-alpha", []string{alpha}, true, true, false},
		{"normal-beta", []string{beta}, false, false, false},
		{"parallel-alpha", []string{alpha}, true, false, true},
		{"parallel-beta", []string{beta}, true, false, true},
	}
	var mu sync.Mutex
	calls := map[string]int{}
	violations := []string{}
	ready, release := make(chan string, 4), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	availableBlocks := regexp.MustCompile(`(?s)<available_skills>(.*?)</available_skills>`)
	ids := regexp.MustCompile(`<id>([^<]+)</id>`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		system, allText := "", ""
		for _, msg := range body.Messages {
			var text string
			if json.Unmarshal(msg.Content, &text) != nil {
				text = string(msg.Content)
			}
			allText += text
			if msg.Role == "system" {
				system += text
			}
		}
		var st stage
		for _, candidate := range stages {
			if strings.Contains(system, "SKILL_ROLE_"+candidate.name+"\n") {
				st = candidate
			}
		}
		if st.name == "" {
			t.Error("unrecognized offline Skill request role")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		calls[st.name]++
		number := calls[st.name]
		seen := []string{}
		for _, block := range availableBlocks.FindAllStringSubmatch(system, -1) {
			for _, id := range ids.FindAllStringSubmatch(block[1], -1) {
				seen = append(seen, id[1])
			}
		}
		slices.Sort(seen)
		seen = slices.Compact(seen)
		want := append([]string(nil), st.bound...)
		if st.noTools {
			want = nil
		}
		slices.Sort(want)
		if !slices.Equal(seen, want) {
			violations = append(violations, fmt.Sprintf("%s: model Skill catalog IDs=%v want=%v", st.name, seen, want))
		}
		if st.name == "cold-alpha" && number >= 3 && !strings.Contains(system, "UPDATED_DESCRIPTION_"+alpha) {
			violations = append(violations, "warm Resume did not refresh the bound Skill description")
		}
		for _, global := range globalIDs {
			if strings.Contains(system, "DESCRIPTION_"+global) || strings.Contains(allText, "BODY_"+global) {
				violations = append(violations, st.name+": unbound sentinel entered model context: "+global)
			}
		}
		for _, id := range []string{alpha, beta} {
			if strings.Contains(allText, "SHADOW_BODY_"+id) || strings.Contains(system, "SHADOW_DESCRIPTION_"+id) {
				violations = append(violations, st.name+": same-ID ambient Skill replaced private binding")
			}
		}
		if st.noTools && (len(body.Tools) != 0 || !strings.Contains(system, "BODY_"+alpha)) {
			violations = append(violations, "NoTools lost bound text or exposed native tools")
		}
		if st.parallel {
			for _, other := range []string{alpha, beta} {
				if !slices.Contains(st.bound, other) && strings.Contains(allText, "BODY_"+other) {
					violations = append(violations, st.name+": parallel binding body crossed instances")
				}
			}
		}
		mu.Unlock()
		if number == 1 && st.parallel {
			ready <- st.name
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		delta := map[string]any{"role": "assistant", "content": "offline Skill fixture complete"}
		finish := "stop"
		if (number == 1 || (st.name == "cold-alpha" && number == 3)) && !st.noTools {
			toolCalls := []map[string]any{}
			add := func(id, name string, input any) {
				raw, _ := json.Marshal(input)
				toolCalls = append(toolCalls, map[string]any{"index": len(toolCalls), "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}})
			}
			if len(st.bound) > 0 {
				add("bound-load", "skill", map[string]any{"id": st.bound[0]})
			}
			add("global-denied", "skill", map[string]any{"id": globalIDs[0]})
			other := alpha
			if slices.Contains(st.bound, alpha) {
				other = beta
			}
			add("unbound-denied", "skill", map[string]any{"id": other})
			delta, finish = map[string]any{"role": "assistant", "tool_calls": toolCalls}, "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			raw, _ := json.Marshal(map[string]any{"id": "skill-fixture", "object": "chat.completion.chunk", "model": "bound-skills-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		chunk(delta, nil)
		chunk(map[string]any{}, finish)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	view, err := (modelSetup{}).Save(t.Context(), nodeprotocol.ModelProviderInput{PresetID: "custom", BaseURL: server.URL + "/v1", Model: "bound-skills-fixture", APIKey: "offline-fixture-only"})
	if err != nil {
		t.Fatal("save isolated localhost model failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	clients := []*Client{}
	newClient := func(st stage, configDir, dataDir string) *Client {
		t.Helper()
		// Both private artifacts exist even when only one (or zero) is bound.
		for _, id := range []string{alpha, beta, globalIDs[4]} {
			writeSkill(filepath.Join(configDir, "skills"), id)
		}
		writeSkill(filepath.Join(configDir, "opencode", "skills"), "unbound-private-native-sentinel")
		config := provider.InstanceConfig{SystemPrompt: "SKILL_ROLE_" + st.name + "\nUse only the actual available Skill catalog.", ReadOnly: st.readonly, DisableTools: st.noTools, WritableRepositories: []string{cwd}}
		for _, id := range st.bound {
			config.Skills = append(config.Skills, nodeprotocol.SkillPayload{Slug: id, Name: id})
		}
		item, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: configDir, DataDir: dataDir, Config: config})
		if err != nil {
			t.Fatal(err)
		}
		client := item.(*Client)
		clients = append(clients, client)
		t.Cleanup(func() { _ = client.Close() })
		cfg := readConfig(t, configDir)
		cfg["agents"].(map[string]any)["title"] = map[string]any{"disabled": true}
		raw, _ := json.Marshal(cfg)
		writeFile(t, filepath.Join(configDir, "opencode", "opencode.json"), string(raw))
		return client
	}
	check := func(st stage, client *Client, events []capturedEvent) error {
		phases := map[string]string{}
		diagnostics := []string{}
		for _, event := range events {
			if event.Type == "run.error" || (event.Type == "provider.event" && event.Payload["kind"] == "tool" && event.Payload["phase"] == "failed") {
				diagnostics = append(diagnostics, fmt.Sprintf("%v", event.Payload))
			}
			if event.Type == "provider.event" && event.Payload["kind"] == "tool" {
				id := stringValue(event.Payload["call_id"])
				phases[id] = stringValue(event.Payload["phase"])
				if id == "bound-load" && event.Payload["phase"] == "success" {
					raw, _ := json.Marshal(event.Payload["content"])
					if !strings.Contains(string(raw), "BODY_"+st.bound[0]) {
						return fmt.Errorf("%s loaded the wrong bound Skill body", st.name)
					}
				}
			}
		}
		if !st.noTools {
			if len(st.bound) > 0 && phases["bound-load"] != "success" {
				return fmt.Errorf("%s: legitimate bound Skill did not load; phases=%v diagnostics=%v", st.name, phases, diagnostics)
			}
			if phases["global-denied"] != "failed" || phases["unbound-denied"] != "failed" {
				return fmt.Errorf("%s: native skill tool loaded an unbound Skill; phases=%v diagnostics=%v", st.name, phases, diagnostics)
			}
		}
		var catalog struct {
			Data []struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			} `json:"data"`
		}
		if err := client.getJSON(ctx, "/api/skill?location[directory]="+cwd, &catalog); err != nil {
			return err
		}
		got := []string{}
		for _, entry := range catalog.Data {
			got = append(got, entry.ID)
		}
		slices.Sort(got)
		want := append([]string(nil), st.bound...)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("%s: native Skill registry IDs=%v want=%v", st.name, got, want)
		}
		return nil
	}
	var client *Client
	native := ""
	for index, st := range stages[:7] {
		if index != 1 {
			if client != nil {
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
			client = newClient(st, configDir, dataDir)
			if index == 5 {
				native = ""
			}
		} else {
			// Same immutable binding/system, same warm process; the stage marker
			// remains the cold role while the extra user turn resumes its session.
			st.name = stages[0].name
			// Exercise the native config source watcher/replay, not just initial
			// registry construction. Ambient sources must remain filtered while
			// the one legitimate bound entry changes on the same warm process.
			writeFile(t, filepath.Join(configDir, "skills", alpha, "SKILL.md"), "---\nname: "+alpha+"\ndescription: UPDATED_DESCRIPTION_"+alpha+"\n---\nBODY_"+alpha+"\n")
			for {
				var catalog struct {
					Data []struct {
						ID          string `json:"id"`
						Description string `json:"description"`
					} `json:"data"`
				}
				if err := client.getJSON(ctx, "/api/skill?location[directory]="+cwd, &catalog); err != nil {
					t.Fatal("native Skill source refresh failed")
				}
				if len(catalog.Data) != 1 || catalog.Data[0].ID != alpha {
					t.Fatal("native source refresh reintroduced unbound Skills")
				}
				if catalog.Data[0].Description == "UPDATED_DESCRIPTION_"+alpha {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("native bound Skill source watcher did not refresh")
				case <-time.After(25 * time.Millisecond):
				}
			}
		}
		if index == 2 {
			if ok, err := client.VerifySession(ctx, native); err != nil || !ok {
				t.Fatal("cold Skill session verification failed")
			}
		}
		previous := client.process
		events, sink := captureSink()
		request := provider.RunRequest{RunID: fmt.Sprintf("skills-%d", index), CWD: cwd, Prompt: "List only the names of the actually available bound Skills.", Model: view.ModelRef, NativeSessionID: native, ReadOnly: st.readonly, DisableTools: st.noTools}
		var session provider.NativeSession
		if native == "" {
			session, err = client.Start(ctx, request, sink)
		} else {
			session, err = client.Resume(ctx, request, sink)
		}
		if err != nil {
			var catalog struct {
				Data []skillBinding `json:"data"`
			}
			_ = client.getJSON(ctx, "/api/skill?location[directory]="+cwd, &catalog)
			t.Fatalf("%s: %v; synthetic native registry=%v host=%v", st.name, err, catalog.Data, client.boundSkills)
		}
		if index == 1 && previous != client.process {
			t.Fatal("warm Skill Resume unexpectedly replaced its exact process")
		}
		native = session.ID
		if err := check(st, client, *events); err != nil {
			t.Error(err)
		}
		t.Logf("native Skill stage %s complete (binding=%v NoTools=%v)", stages[index].name, st.bound, st.noTools)
	}
	results := make(chan error, 2)
	for _, st := range stages[7:] {
		parallel := newClient(st, permissionFixtureDirectory(t, t.TempDir()), permissionFixtureDirectory(t, t.TempDir()))
		go func(st stage) {
			events, sink := captureSink()
			_, err := parallel.Start(ctx, provider.RunRequest{RunID: st.name, CWD: cwd, Prompt: "List actual bound Skill names.", Model: view.ModelRef, ReadOnly: true}, sink)
			if err == nil {
				err = check(st, parallel, *events)
			}
			results <- err
		}(st)
	}
	for range 2 {
		select {
		case <-ready:
		case err := <-results:
			t.Fatalf("parallel Skill service ended early: %v", err)
		case <-ctx.Done():
			t.Fatal("parallel Skill requests did not overlap")
		}
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	mu.Lock()
	problems := append([]string(nil), violations...)
	requestCount := 0
	for name, number := range calls {
		want := 2
		if name == "cold-alpha" {
			want = 4 // First Run plus a truly warm Resume, each with tool continuation.
		} else if name == "no-tools-alpha" {
			want = 1
		}
		if number != want {
			problems = append(problems, fmt.Sprintf("%s: native model requests=%d want=%d", name, number, want))
		}
		requestCount += number
	}
	if len(calls) != len(stages)-1 { // warm-alpha deliberately keeps the cold system marker.
		problems = append(problems, "one or more native Skill stages never reached the local model")
	}
	mu.Unlock()
	for _, problem := range problems {
		t.Error(problem)
	}
	// When only a same-ID HOME shadow is discoverable, never substitute it for
	// the requested private file. Fail before any model HTTP, even on a real CLI.
	missing := newClient(stage{name: "missing-bound", bound: []string{alpha}, readonly: true}, permissionFixtureDirectory(t, t.TempDir()), permissionFixtureDirectory(t, t.TempDir()))
	cfg := readConfig(t, missing.configDir)
	cfg["skills"] = []string{}
	raw, _ := json.Marshal(cfg)
	writeFile(t, filepath.Join(missing.configDir, "opencode", "opencode.json"), string(raw))
	_, sink := captureSink()
	if _, err := missing.Start(ctx, provider.RunRequest{RunID: "missing-bound", CWD: cwd, Prompt: "must not reach the model", Model: view.ModelRef}, sink); err == nil || !strings.Contains(err.Error(), "Skill registry") {
		t.Fatal("missing private native Skill did not fail closed before prompt admission")
	}
	mu.Lock()
	after := 0
	for _, number := range calls {
		after += number
	}
	mu.Unlock()
	if after != requestCount {
		t.Fatal("binding mismatch sent a model request")
	}
	// Preserve the existing installed-package contract: a bound artifact can
	// contain multiple nested SKILL.md entries, with native parent-derived IDs.
	nestedConfig := permissionFixtureDirectory(t, t.TempDir())
	writeSkill(filepath.Join(nestedConfig, "skills", alpha, "team"), "bound-nested-sentinel")
	writeFile(t, filepath.Join(nestedConfig, "skills", alpha, "README.md"), "must not become a registered Skill")
	nested := newClient(stage{name: "nested-package", bound: []string{alpha}, readonly: true}, nestedConfig, permissionFixtureDirectory(t, t.TempDir()))
	if err := nested.ensureStartedScoped(ctx, &provider.RunRequest{CWD: cwd, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := nested.verifyPermissionGuard(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	if err := nested.verifySkillBindings(ctx, cwd); err != nil || len(nested.boundSkills) != 2 {
		t.Fatal("native nested bound package did not preserve exact entry paths/IDs")
	}
	t.Log("native source refresh, same-ID HOME shadow rejection before model HTTP, nested bound package entries and README exclusion verified")
	for _, client := range clients {
		client.mu.Lock()
		tree := client.process
		client.mu.Unlock()
		if err := client.Close(); err != nil {
			t.Error("native Skill fixture process cleanup failed")
		}
		if tree != nil {
			select {
			case <-tree.Done():
			default:
				t.Error("native Skill fixture service survived cleanup")
			}
		}
	}
	t.Log("real native outbound Skill catalog, exact bound body loading, no-bound zero registry, NoTools text-only binding, cold/warm Resume and disjoint parallel bindings exercised; synthetic HOME/model only")
}
