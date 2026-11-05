package opencode

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
	providerprocess "github.com/teamva-ai/teamva/node/provider/process"
)

// Real bundled server + offline stdio fixture. Never creates a session or calls
// a model. Native stdout/API bodies stay private (they may include config data).
func TestBundledMCPContractWithoutModel(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for native offline MCP verification (no model)")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	binary, _ = filepath.Abs(binary)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for offline MCP fixture")
	}
	_, source, _, _ := runtime.Caller(0)
	fixture := filepath.Join(filepath.Dir(source), "..", "..", "..", "scripts", "e2e", "fixtures", "mcp-server.mjs")
	if _, err := os.Stat(fixture); err != nil {
		t.Fatal("offline MCP fixture missing")
	}
	home := isolateUserHomes(t)
	configDir, workspace := t.TempDir(), t.TempDir()
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	// No os.Environ or native account files: only isolated paths and executable
	// lookup. Disable catalog fetching as well; no inference endpoint is invoked.
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + configDir, "XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"OPENCODE_CONFIG_PROJECT_DISABLE=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1"}
	// Serve requires its own private transport authentication; this random test
	// credential is unrelated to MCP/provider auth and is never logged/copied.
	serverPassword := rand.Text()
	env = append(env, "OPENCODE_PASSWORD="+serverPassword, "OPENCODE_SERVER_PASSWORD="+serverPassword)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	versionCmd := exec.Command(binary, "--version")
	versionCmd.Env, versionCmd.Dir = env, workspace
	version, err := providerprocess.CombinedOutput(ctx, versionCmd, 1024)
	if err != nil || !regexp.MustCompile(`^opencode v?[0-9]+\.[0-9]+\.[0-9]+\s*$`).Match(version) {
		t.Fatal("could not identify bundled version (raw output suppressed)")
	}
	t.Logf("native MCP contract: %s; no session/model requests", strings.TrimSpace(string(version)))
	wrapper, marker := filepath.Join(workspace, "verify-fixture.mjs"), filepath.Join(workspace, "fixture-started")
	// Verify actual native child argv/environment/cwd before importing the shared
	// fixture. It writes only a PID marker; never copies credentials or logs env.
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	wrapperText := fmt.Sprintf(`import { writeFileSync } from "node:fs";
if (process.argv[2] !== "argument with spaces" || process.env.FIXTURE_PRIVATE !== "test-only-private-value" || process.cwd() !== %s) process.exit(17);
writeFileSync(%s, String(process.pid), { mode: 0o600 });
await import(%s);
`, quote(workspace), quote(marker), quote((&url.URL{Scheme: "file", Path: fixture}).String()))
	if err := os.WriteFile(wrapper, []byte(wrapperText), 0o600); err != nil {
		t.Fatal(err)
	}
	local, _ := json.Marshal(map[string]any{"type": "local", "command": []string{node, wrapper, "argument with spaces"},
		"cwd": workspace, "environment": map[string]string{"FIXTURE_PRIVATE": "test-only-private-value"},
		"protocol": "legacy", "codemode": false, "timeout": map[string]int{"startup": 10000, "catalog": 5000, "execution": 7000}})
	config := provider.InstanceConfig{MCPServers: map[string]json.RawMessage{
		"fixture-local":    local,
		"fixture-disabled": json.RawMessage(`{"type":"local","command":["must-not-run-disabled-mcp"],"disabled":true}`),
		"fixture-remote":   json.RawMessage(`{"type":"remote","url":"http://127.0.0.1:1/offline-mcp","disabled":true,"headers":{"X-Fixture-Private":"test-only-private-value"},"oauth":{"client_id":"fixture-client","client_secret":"test-only-private-value","callback_port":19876,"redirect_uri":"http://127.0.0.1:19876/callback"}}`),
	}}
	if err := WriteConfig(configDir, config); err != nil {
		t.Fatal("write native MCP config failed")
	}
	// Even a valid workspace config must not inject an unapproved MCP server.
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(`{"mcp":{"servers":{"ambient-unapproved":{"type":"local","command":["must-not-run-ambient-mcp"]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Env, cmd.Dir = env, workspace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	tree, err := providerprocess.Start(cmd)
	if err != nil {
		t.Fatal("start bundled MCP test server failed")
	}
	defer func() {
		_ = stdout.Close()
		if err := tree.Close(); err != nil {
			t.Error("native MCP server/child cleanup failed")
		}
		// Process-tree shutdown must also reap the actual fixture, not merely
		// return after the top-level server exits. No process arguments are logged.
		pids := []int{cmd.Process.Pid}
		if markerRaw, err := os.ReadFile(marker); err == nil {
			if pid, err := strconv.Atoi(string(markerRaw)); err == nil {
				pids = append(pids, pid)
			}
		}
		if runtime.GOOS != "windows" {
			for _, pid := range pids {
				process, err := os.FindProcess(pid)
				if err != nil {
					continue
				}
				deadline := time.Now().Add(3 * time.Second)
				for process.Signal(syscall.Signal(0)) == nil {
					if time.Now().After(deadline) {
						t.Error("native MCP server/fixture process survived cleanup")
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				_ = process.Release()
			}
		}
	}()
	addressResult := make(chan string, 1)
	go func() { address, _ := readServerAddress(stdout); addressResult <- address }()
	var address string
	select {
	case address = <-addressResult:
		if address == "" {
			t.Fatal("native MCP server did not publish readiness")
		}
	case <-ctx.Done():
		t.Fatal("native MCP server readiness timed out")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	lastHTTPStatus := 0
	get := func(path string, target any) bool {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address+path, nil)
		req.SetBasicAuth("opencode", serverPassword)
		response, err := client.Do(req)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		lastHTTPStatus = response.StatusCode
		return response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target) == nil
	}
	query := "?location[directory]=" + url.QueryEscape(workspace)
	var states struct {
		Data []struct {
			Name   string `json:"name"`
			Status struct {
				Status string `json:"status"`
			} `json:"status"`
		} `json:"data"`
	}
	for {
		if get("/api/mcp"+query, &states) {
			ready := false
			for _, state := range states.Data {
				if state.Name == "fixture-local" && state.Status.Status == "connected" {
					ready = true
				}
				if state.Name == "fixture-local" && state.Status.Status == "failed" {
					_, markerErr := os.Stat(marker)
					t.Fatalf("native fixture connection failed; argv/env/cwd verification marker present=%v (native details suppressed)", markerErr == nil)
				}
			}
			if ready {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native offline MCP did not connect: HTTP=%d configured=%d (response/config values suppressed)", lastHTTPStatus, len(states.Data))
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(states.Data) != 3 {
		t.Fatal("native server inherited uncontrolled MCP or omitted managed servers")
	}
	for _, state := range states.Data {
		if state.Name != "fixture-local" && state.Status.Status != "disabled" {
			t.Fatal("native server lost disabled MCP state")
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("native fixture did not verify argv/environment/cwd")
	}
	var resources struct {
		Data json.RawMessage `json:"data"`
	}
	if !get("/api/mcp/resource"+query, &resources) || !strings.Contains(string(resources.Data), "fixture://readme") {
		t.Fatal("native server did not expose offline fixture resource catalog")
	}
	t.Log("native MCP connected; argv/env/cwd verified; disabled servers stayed disabled; resource catalog contains fixture resource; ambient project MCP excluded")
}
