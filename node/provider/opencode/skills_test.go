package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestSkillConfigUsesExactHostBindingsNotSiblingSources(t *testing.T) {
	isolateUserHomes(t)
	dir := permissionFixtureDirectory(t, t.TempDir())
	for _, id := range []string{"bound", "stale"} {
		writeFile(t, filepath.Join(dir, "skills", id, "SKILL.md"), "fixture "+id)
	}
	config := provider.InstanceConfig{ReadOnly: true, Skills: []nodeprotocol.SkillPayload{{Slug: "bound"}}}
	adapter, err := newInstance(provider.InstanceOptions{ConfigDir: dir, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client := adapter.(*Client)
	cfg := readConfig(t, dir)
	sources := cfg["skills"].([]any)
	if len(sources) != 1 || sources[0] != filepath.Join(dir, "skills", "bound") {
		t.Fatal("Skill discovery source includes unbound siblings")
	}
	options := cfg["plugins"].([]any)[0].(map[string]any)["options"].(map[string]any)
	bindings := options["boundSkills"].([]any)
	bound := bindings[0].(map[string]any)
	if len(bindings) != 1 || bound["id"] != "bound" || bound["path"] != filepath.Join(dir, "skills", "bound", "SKILL.md") {
		t.Fatal("native Skill transform lacks an exact ID and private entry path")
	}
	rules := client.permissionRules(provider.RunRequest{CWD: dir, ReadOnly: true})
	if permissionEffect(rules, "skill", "bound") != "allow" || permissionEffect(rules, "skill", "stale") != "deny" || permissionEffect(rules, "skill", "report") != "deny" {
		t.Fatal("read-only rules permit an unbound global/native Skill")
	}
}

func TestSkillBindingRejectsMissingAliasedAndDuplicateEntries(t *testing.T) {
	isolateUserHomes(t)
	for _, kind := range []string{"missing", "entry-symlink", "directory-symlink", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			dir := permissionFixtureDirectory(t, t.TempDir())
			target := filepath.Join(dir, "skills", "bound", "SKILL.md")
			other := permissionFixtureDirectory(t, t.TempDir())
			writeFile(t, filepath.Join(other, "SKILL.md"), "unbound fixture")
			config := provider.InstanceConfig{Skills: []nodeprotocol.SkillPayload{{Slug: "bound"}}}
			switch kind {
			case "entry-symlink":
				permissionFixtureDirectory(t, filepath.Dir(target))
				if err := os.Symlink(filepath.Join(other, "SKILL.md"), target); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				permissionFixtureDirectory(t, dir, "skills")
				if err := os.Symlink(other, filepath.Dir(target)); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				writeFile(t, target, "bound fixture")
				config.Skills = append(config.Skills, config.Skills[0])
			}
			if err := WriteConfig(dir, config); err == nil || strings.Contains(err.Error(), other) {
				t.Fatal("unsafe Skill entry accepted or private path leaked")
			}
		})
	}
}

func TestSkillBindingPreservesNestedBoundPackages(t *testing.T) {
	isolateUserHomes(t)
	dir := permissionFixtureDirectory(t, t.TempDir())
	for _, id := range []string{"review", "format"} {
		writeFile(t, filepath.Join(dir, "skills", "package", "team", id, "SKILL.md"), "nested "+id)
	}
	writeFile(t, filepath.Join(dir, "skills", "package", "README.md"), "not a bound Skill")
	if err := WriteConfig(dir, provider.InstanceConfig{DisableTools: true, Skills: []nodeprotocol.SkillPayload{{Slug: "package"}}}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig(t, dir)
	options := cfg["plugins"].([]any)[0].(map[string]any)["options"].(map[string]any)
	if len(cfg["skills"].([]any)) != 1 || len(options["boundSkills"].([]any)) != 2 || len(options["instructions"].([]any)) != 2 {
		t.Fatal("nested bound package lost native entries/NoTools text or exposed README as a Skill")
	}
	// V2 IDs use the immediate parent, not the package slug or whole subpath.
	writeFile(t, filepath.Join(dir, "skills", "package", "duplicate", "review", "SKILL.md"), "ambiguous review")
	if err := WriteConfig(dir, provider.InstanceConfig{Skills: []nodeprotocol.SkillPayload{{Slug: "package"}}}); err == nil {
		t.Fatal("duplicate nested native IDs were silently substituted")
	}
}

func TestSkillBindingVerificationFailsClosedOnNativeMismatch(t *testing.T) {
	for _, kind := range []string{"exact", "empty", "unbound", "shadow", "duplicate", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			bindings := []skillBinding{{ID: "bound", Path: "/private/fixture/bound/SKILL.md"}}
			switch kind {
			case "empty":
				bindings = nil
			case "unbound":
				bindings = append(bindings, skillBinding{ID: "global", Path: "/fixture/global/SKILL.md"})
			case "shadow":
				bindings[0].Path = "/fixture/global/bound/SKILL.md"
			case "duplicate":
				bindings = append(bindings, bindings[0])
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/rpc/teamva.skill-bindings/seal" && kind != "unavailable" {
					_, _ = w.Write([]byte(`{"output":true}`))
					return
				}
				if r.URL.Path != "/api/skill" || kind == "unavailable" {
					w.WriteHeader(404)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": bindings})
			}))
			defer server.Close()
			client := testClient(t)
			client.baseURL = server.URL
			client.boundSkills = []skillBinding{{ID: "bound", Path: "/private/fixture/bound/SKILL.md"}}
			if err := client.verifySkillBindings(t.Context(), "/fixture"); (err == nil) != (kind == "exact") {
				t.Fatal("native Skill registry mismatch did not fail closed")
			}
		})
	}
}
