package codex

import (
	"errors"
	"os"
	"path/filepath"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

const codexRunTempName = ".tmp"

func codexRunTempDir(request adapter.RunRequest) string {
	if request.ToolRequestDir == "" {
		return ""
	}
	return filepath.Join(request.ToolRequestDir, codexRunTempName)
}

// The temp directory is inside an already authorized host-created Run IPC
// leaf, never a new filesystem grant. Root-relative operations cannot follow a
// model-planted .tmp link outside that leaf. No bridge means no host temp write.
func prepareCodexRunTemp(request adapter.RunRequest) error {
	if request.ToolRequestDir == "" {
		return nil
	}
	info, err := os.Lstat(request.ToolRequestDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex Run IPC must be a real directory")
	}
	root, err := os.OpenRoot(request.ToolRequestDir)
	if err != nil {
		return errors.New("open codex Run IPC for private temp failed")
	}
	defer root.Close()
	if err := root.Mkdir(codexRunTempName, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("prepare codex Run private temp failed")
	}
	leaf, err := root.Lstat(codexRunTempName)
	if err != nil || !leaf.IsDir() || leaf.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex Run private temp must be a real directory")
	}
	tmp, err := root.OpenRoot(codexRunTempName)
	if err != nil {
		return errors.New("open codex Run private temp failed")
	}
	defer tmp.Close()
	opened, err := tmp.Stat(".")
	if err != nil || !os.SameFile(leaf, opened) {
		return errors.New("codex Run private temp changed")
	}
	if err := tmp.Chmod(".", 0o700); err != nil {
		return errors.New("secure codex Run private temp failed")
	}
	return nil
}

func codexRunConfig(request adapter.RunRequest, readOnlyPaths ...string) map[string]any {
	config := codexPermissionConfig(readOnlyPaths...)
	if temp := codexRunTempDir(request); temp != "" {
		// Native 0.163.0 config/src/shell_environment_policy.rs::ShellEnvironmentPolicyToml.set.
		// zsh does NOT derive its heredoc location from TMPDIR: its documented
		// TMPPREFIX defaults to /tmp/zsh and must be redirected independently.
		config["shell_environment_policy"] = map[string]any{"set": map[string]any{"TMPDIR": temp, "TMP": temp, "TEMP": temp, "TMPPREFIX": filepath.Join(temp, "zsh")}}
	}
	return config
}
