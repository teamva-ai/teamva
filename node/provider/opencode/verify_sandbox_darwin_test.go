//go:build darwin

package opencode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

// The fake CLI performs its writes BEFORE serving any HTTP request, matching the
// independent audit's startup/verification gap. No model or real auth is used.
func TestMacOSManagedVerifyColdStartUsesMinimalWriteBoundary(t *testing.T) {
	isolateNativeFixtureEnvironment(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the verification startup fixture")
	}
	base := permissionFixtureDirectory(t, t.TempDir())
	root := permissionFixtureDirectory(t, base, "business")
	config := permissionFixtureDirectory(t, base, "config")
	data := permissionFixtureDirectory(t, base, "native-data")
	oldIPC := permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "old")
	exactIPC := permissionFixtureDirectory(t, root, ".teamva", "tool-requests", "exact")
	report := filepath.Join(data, "startup-result.json")
	outsideMarker := filepath.Join(base, "outside-marker")
	businessMarker := filepath.Join(root, "business-marker")
	oldMarker := filepath.Join(oldIPC, "old-marker")
	exactMarker := filepath.Join(exactIPC, "exact-marker")
	sessionFile := filepath.Join(data, "saved-session.json")
	writeFile(t, sessionFile, `{"id":"ses_saved"}`)
	source := `import fs from 'node:fs';
import http from 'node:http';
const attempt = target => { try { fs.writeFileSync(target, 'fixture-marker'); return 'allowed'; } catch (error) { return error.code; } };
const result = { outside: attempt(` + quoteJSON(outsideMarker) + `), business: attempt(` + quoteJSON(businessMarker) + `), oldIPC: attempt(` + quoteJSON(oldMarker) + `), exactIPC: attempt(` + quoteJSON(exactMarker) + `) };
fs.writeFileSync(` + quoteJSON(report) + `, JSON.stringify(result));
const server = http.createServer((request, response) => {
  if (request.url === '/api/info') { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ version: '2.0.fixture' })); return; }
  if (request.url === '/api/session/ses_saved') { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ data: JSON.parse(fs.readFileSync(` + quoteJSON(sessionFile) + `, 'utf8')) })); return; }
  response.writeHead(404); response.end();
});
server.listen(0, '127.0.0.1', () => console.log('server listening on http://127.0.0.1:' + server.address().port));
`
	script := filepath.Join(config, "startup-fixture.mjs")
	writeFile(t, script, source)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	executable := filepath.Join(config, "fake-opencode")
	writeFile(t, executable, "#!/bin/sh\nexec "+quote(node)+" "+quote(script)+" \"$@\"\n")
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := newInstance(provider.InstanceOptions{Executable: executable, ConfigDir: config, DataDir: data,
		Config: provider.InstanceConfig{WritableRepositories: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	client := adapter.(*Client)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ok, err := client.VerifySession(ctx, "ses_saved")
	if err != nil || !ok {
		t.Fatal("cold managed VerifySession could not read its stored fixture session")
	}
	readReport := func() map[string]string {
		t.Helper()
		raw, err := os.ReadFile(report)
		var result map[string]string
		if err != nil || json.Unmarshal(raw, &result) != nil {
			t.Fatal("bounded verification CLI did not create its private startup evidence")
		}
		return result
	}
	denied := func(value string) bool { return value == "EPERM" || value == "EACCES" }
	for _, result := range readReport() {
		if !denied(result) {
			t.Fatal("verification startup wrote a business, outside or Run IPC marker without a scope")
		}
	}
	client.mu.Lock()
	oldTree := client.process
	bounded := client.sandboxReady && client.writeSandbox != nil && client.writeSandbox.cwd == config
	client.mu.Unlock()
	if !bounded {
		t.Fatal("managed VerifySession started without its minimum OS profile")
	}
	for _, marker := range []string{outsideMarker, businessMarker, oldMarker, exactMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("minimum verification scope created an unauthorized marker")
		}
	}
	// Later execution must replace the idle minimum profile, not reuse it or
	// retain a naked process. It still cannot inherit another Run's IPC.
	if err := client.ensureStartedScoped(ctx, &provider.RunRequest{CWD: root, ToolRequestDir: exactIPC}); err != nil {
		t.Fatal(err)
	}
	result := readReport()
	if result["business"] != "allowed" || result["exactIPC"] != "allowed" || !denied(result["outside"]) || !denied(result["oldIPC"]) {
		t.Fatal("execution did not replace minimum verification with the exact business/IPC scope")
	}
	select {
	case <-oldTree.Done():
	default:
		t.Fatal("the idle verification process survived the scope transition")
	}
	if _, err := os.Stat(outsideMarker); !os.IsNotExist(err) {
		t.Fatal("scoped execution startup escaped its business roots")
	}
	if exists, err := client.VerifySession(ctx, "ses_saved"); err != nil || !exists {
		t.Fatal("warm metadata verification failed after scoped execution")
	}
	for _, result := range readReport() {
		if !denied(result) {
			t.Fatal("warm VerifySession retained a previous Run's business or IPC grant")
		}
	}
	t.Log("cold VerifySession startup was OS-bounded before native code; later exact execution restarted the idle process and preserved outside/other-IPC denial")
}
