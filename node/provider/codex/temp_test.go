package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestRunTempStaysInsideExistingIPCAndNeverFollowsLinks(t *testing.T) {
	root := t.TempDir()
	request := adapter.RunRequest{ToolRequestDir: root, ReadOnly: true}
	if err := prepareCodexRunTemp(request); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, codexRunTempName)); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("Run temp is not private")
	}
	want := filepath.Join(root, codexRunTempName)
	env := codexRunConfig(request)["shell_environment_policy"].(map[string]any)["set"]
	if !reflect.DeepEqual(env, map[string]any{"TMPDIR": want, "TMP": want, "TEMP": want, "TMPPREFIX": filepath.Join(want, "zsh")}) {
		t.Fatal("native temp variables do not all use exact IPC")
	}
	if err := prepareCodexRunTemp(adapter.RunRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, found := codexRunConfig(adapter.RunRequest{})["shell_environment_policy"]; found {
		t.Fatal("no-bridge Run gained a temp override")
	}
	for _, kind := range []string{"symlink", "file", "missing-ipc", "symlink-ipc"} {
		t.Run(kind, func(t *testing.T) {
			ipc := t.TempDir()
			outside := t.TempDir()
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, filepath.Join(ipc, codexRunTempName)); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(ipc, codexRunTempName), []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-ipc":
				ipc = filepath.Join(ipc, "missing")
			case "symlink-ipc":
				path := filepath.Join(ipc, "alias")
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
				ipc = path
			}
			if err := prepareCodexRunTemp(adapter.RunRequest{ToolRequestDir: ipc}); err == nil {
				t.Fatal("unsafe Run temp accepted")
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatal("Run temp preparation wrote through an alias")
			}
		})
	}
}
