package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/teamva-ai/teamva/node/provider"
)

// writeSandbox is a provider-owned OS write boundary, not a command allowlist.
// The CLI and all children inherit it. Filesystem reads and network access are
// intentionally not treated as confidential/isolated by this feature.
type writeSandbox struct {
	cwd         string
	profilePath string
	fingerprint string
	env         map[string]string
	runTemp     *runTempDirectory
}

func (s *writeSandbox) command(executable string, args ...string) (*exec.Cmd, error) {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, errors.New("opencode sandbox executable is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, errors.New("opencode sandbox executable is unavailable")
	}
	return platformSandboxCommand(s.profilePath, resolved, args...)
}

func (s *writeSandbox) environment(environ []string) []string {
	filtered := make([]string, 0, len(environ)+len(s.env))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := s.env[name]; !replaced {
			filtered = append(filtered, entry)
		}
	}
	for name, value := range s.env {
		filtered = append(filtered, name+"="+value)
	}
	return filtered
}

// prepareRunSandbox runs under the Run serialization lock. Restart only an idle
// native service when its exact write scope changes (notably per-Run IPC and
// read-only Resume). Native sessions remain in the durable instance data home.
func (c *Client) prepareRunSandbox(ctx context.Context, request provider.RunRequest) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	return c.prepareSandboxLocked(ctx, request)
}

// The caller holds startMu across both profile selection and native spawn, so a
// concurrent verification cannot replace a just-prepared execution profile.
func (c *Client) prepareSandboxLocked(ctx context.Context, request provider.RunRequest) error {
	c.mu.Lock()
	active := c.active != nil
	previous := c.writeSandbox
	c.mu.Unlock()
	if active {
		return errors.New("cannot change opencode sandbox during an active Run")
	}
	if previous != nil && previous.runTemp != nil && request.ToolRequestDir != "" && !request.DisableTools && !c.disableTools && previous.runTemp.path == filepath.Join(request.ToolRequestDir, ".tmp") {
		if err := previous.runTemp.validate(); err != nil {
			return err
		}
	}
	sandbox, err := buildWriteSandbox(ctx, c, request)
	if err != nil {
		return err // No silently unsandboxed fallback and no fake shell success.
	}
	c.mu.Lock()
	ready := c.sandboxReady
	c.mu.Unlock()
	if previous == nil || previous.fingerprint != sandbox.fingerprint || !ready {
		if err := c.stopProcess(); err != nil {
			return errors.New("stop previous opencode sandbox failed")
		}
	}
	c.mu.Lock()
	c.writeSandbox = sandbox
	c.mu.Unlock()
	return nil
}

func sandboxPrivateDirectory(parent, name string) (string, error) {
	value := filepath.Join(parent, name)
	if err := os.MkdirAll(value, 0o700); err != nil {
		return "", errors.New("prepare opencode sandbox private directory failed")
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("opencode sandbox private directory must be a real directory")
	}
	if err := os.Chmod(value, 0o700); err != nil {
		return "", errors.New("secure opencode sandbox private directory failed")
	}
	return permissionDirectory(value)
}

func sandboxContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func buildWriteSandbox(ctx context.Context, c *Client, request provider.RunRequest) (*writeSandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := platformSandboxAvailable(); err != nil {
		return nil, err
	}
	config, err := permissionDirectory(c.configDir)
	if err != nil {
		return nil, errors.New("opencode OS write sandbox requires a private configuration directory")
	}
	// Direct SDK clients can start metadata-only verification with an empty
	// private config home. Prepare its native directory as the host so the CLI
	// never needs write authority over configuration merely to bootstrap.
	if _, err := sandboxPrivateDirectory(config, "opencode"); err != nil {
		return nil, err
	}
	if c.dataDir == "" {
		c.dataDir, err = sandboxPrivateDirectory(config, "data")
		if err != nil {
			return nil, err
		}
	}
	// Factory instances already prepare this directory. Direct SDK clients may
	// provide a not-yet-created private data home via WithDataDir; create only
	// that explicit absolute path, never fall back to the user's default store.
	if !filepath.IsAbs(c.dataDir) || os.MkdirAll(c.dataDir, 0o700) != nil {
		return nil, errors.New("opencode OS write sandbox requires a private absolute native data directory")
	}
	dataInfo, statErr := os.Lstat(c.dataDir)
	if statErr != nil || !dataInfo.IsDir() || dataInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("opencode OS write sandbox requires a private real native data directory")
	}
	data, err := permissionDirectory(c.dataDir)
	if err != nil {
		return nil, errors.New("opencode OS write sandbox requires a private native data directory")
	}
	if sandboxContains(data, config) || sandboxContains(data, request.CWD) {
		return nil, errors.New("opencode native data must not encompass configuration or the working directory")
	}
	for _, root := range c.writableRoots {
		if sandboxContains(data, root) {
			return nil, errors.New("opencode native data must not encompass a business repository")
		}
	}
	if request.ToolRequestDir != "" && (sandboxContains(config, request.ToolRequestDir) || sandboxContains(data, request.ToolRequestDir)) {
		return nil, errors.New("opencode host IPC must be separate from private configuration and native data")
	}
	private, err := sandboxPrivateDirectory(data, "sandbox")
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, item := range []struct{ directory, variable string }{
		{"cache", "XDG_CACHE_HOME"}, {"state", "XDG_STATE_HOME"}, {"tmp", "TMPDIR"},
		{"npm", "npm_config_cache"}, {"go-build", "GOCACHE"}, {"go-mod", "GOMODCACHE"},
		{"python", "PYTHONPYCACHEPREFIX"}, {"bun", "BUN_INSTALL_CACHE_DIR"},
	} {
		value, err := sandboxPrivateDirectory(private, item.directory)
		if err != nil {
			return nil, err
		}
		env[item.variable] = value
	}
	env["TMP"], env["TEMP"] = env["TMPDIR"], env["TMPDIR"]
	// zsh's here-document storage does not follow TMPDIR alone. Never inherit
	// an ambient TMPPREFIX (possibly global /tmp) into a managed process.
	env["TMPPREFIX"] = ""
	env["OPENCODE_TEAMVA_RUN_TMP"] = ""
	var runTemp *runTempDirectory
	if request.ToolRequestDir != "" && !request.DisableTools && !c.disableTools {
		runTemp, err = prepareRunTempDirectory(request.ToolRequestDir)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"TMPDIR", "TMP", "TEMP", "OPENCODE_TEAMVA_RUN_TMP"} {
			env[key] = runTemp.path
		}
		env["TMPPREFIX"] = filepath.Join(runTemp.path, "zsh")
	}
	// Apple's /usr/bin/git is an xcrun shim. Its lookup cache ignores TMPDIR
	// and otherwise tries writing the user's global confstr temp directory.
	// This documented xcrun flag avoids that write rather than granting /var.
	env["xcrun_nocache"] = "1"
	tools, err := platformSandboxToolEnvironment(ctx, private)
	if err != nil {
		return nil, err
	}
	for name, value := range tools {
		env[name] = value
	}
	cwd, err := permissionDirectory(request.CWD)
	if err != nil {
		return nil, errors.New("opencode OS write sandbox requires a real working directory")
	}
	profile, err := platformWriteProfile(ctx, c, request, config, data)
	if err != nil {
		return nil, err
	}
	profilePath := filepath.Join(config, "write-sandbox.sb")
	info, err := os.Lstat(profilePath)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("opencode sandbox profile must be a private real file")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("prepare opencode sandbox profile failed")
	}
	file, err := os.CreateTemp(config, ".write-sandbox-*")
	if err != nil {
		return nil, errors.New("write opencode sandbox profile failed")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(profile)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || os.Rename(file.Name(), profilePath) != nil {
		return nil, errors.New("write opencode sandbox profile failed")
	}
	// Scope reuse includes the exact per-Run environment, not just profile text.
	// A warm Resume must never keep the previous Run's temp/IPC environment.
	encodedEnv, _ := json.Marshal(env)
	sandbox := &writeSandbox{cwd: cwd, profilePath: profilePath,
		fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(cwd+"\x00"+profile+"\x00"+string(encodedEnv)))), env: env, runTemp: runTemp}
	if runTemp != nil {
		if err := runTemp.validate(); err != nil {
			return nil, err
		}
	}
	if err := platformValidateSandbox(ctx, sandbox); err != nil {
		return nil, err
	}
	return sandbox, nil
}
