package embedded

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProviderExecutableSettingsResolveDefaultsAndOverrides(t *testing.T) {
	root := t.TempDir()
	// Keep bundled-runtime discovery away from the repository vendor tree; the
	// default source must be the manifest command in this fixture.
	t.Chdir(t.TempDir())
	t.Setenv("TEAMVA_OPENCODE_PATH", "")

	custom := writeRecordingExecutable(t, root, "custom-opencode", filepath.Join(root, "custom.log"), "opencode test")
	controller, err := New(t.Context(), Config{
		ComputerID: "computer_exec", DataDir: filepath.Join(root, "data"),
		WorkspaceDir:  filepath.Join(root, "workspaces"),
		ProviderPaths: map[string]string{"opencode": custom},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })

	find := func(provider string) (path, source string) {
		t.Helper()
		for _, item := range controller.ProviderExecutables() {
			if item.Provider == provider {
				return item.Path, item.Source
			}
		}
		t.Fatalf("provider %s missing from executable projection", provider)
		return "", ""
	}

	if path, source := find("opencode"); path != custom || source != "custom" {
		t.Fatalf("opencode=%q source=%q", path, source)
	}
	if path, source := find("codex"); path != "codex" || source != "path" {
		t.Fatalf("codex=%q source=%q", path, source)
	}

	cleared, err := controller.SetProviderExecutable(context.Background(), "opencode", "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Path != "opencode" || cleared.Source != "path" {
		t.Fatalf("cleared=%+v", cleared)
	}

	second := writeRecordingExecutable(t, root, "second-opencode", filepath.Join(root, "second.log"), "opencode test")
	updated, err := controller.SetProviderExecutable(context.Background(), "opencode", second)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Path != second || updated.Source != "custom" {
		t.Fatalf("updated=%+v", updated)
	}
	if path, source := find("opencode"); path != second || source != "custom" {
		t.Fatalf("projection after update=%q source=%q", path, source)
	}

	if _, err := controller.SetProviderExecutable(context.Background(), "unknown", second); err == nil {
		t.Fatal("unknown provider must be rejected")
	}
}
