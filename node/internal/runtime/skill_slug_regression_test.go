package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestSkillOperationsRejectPathLikeSlugsBeforeFilesystemAccess(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "must-survive")
	if err := os.WriteFile(sentinel, []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{workspaceRoot: workspace}
	for _, slug := range []string{"", ".", "..", "../workspace", "a/b", "a\\b", "-hidden", " a", "a ", strings.Repeat("x", 129)} {
		for _, name := range []string{"skill.uninstall", "skill.open"} {
			payload, _ := json.Marshal(map[string]any{"slug": slug})
			if _, err := runtime.execute(t.Context(), nodeprotocol.CommandEnvelope{Name: name, Payload: payload}); err == nil {
				t.Fatalf("%s accepted path-like slug", name)
			}
		}
		if _, err := installSkill(t.Context(), root, nodeprotocol.InstallSkillPayload{Slug: slug, SourceURL: "https://github.com/test-only/skills"}); err == nil {
			t.Fatal("installer accepted path-like slug")
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("rejected uninstall touched the data directory")
	}
	for _, slug := range []string{"a", "skill-name_2", strings.Repeat("x", 128)} {
		if !validSkillSlug(slug) {
			t.Fatal("safe leaf slug rejected")
		}
	}
}
