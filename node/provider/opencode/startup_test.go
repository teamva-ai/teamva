package opencode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadServerAddress(t *testing.T) {
	address, err := readServerAddress(strings.NewReader("non-sensitive startup log\nserver listening on http://127.0.0.1:45678\r\n"))
	if err != nil || address != "http://127.0.0.1:45678" {
		t.Fatalf("address=%q err=%v", address, err)
	}
	for _, invalid := range []string{
		"", "http://127.0.0.1:45678", "http://127.0.0.1:0", "http://127.0.0.1:65536",
		"http://127.0.0.1:04567", "http://127.0.0.1:+4567", "http://127.0.0.1:-1",
		"http://localhost:4567", "http://0.0.0.0:4567", "http://[::1]:4567", "https://127.0.0.1:4567",
		"http://private-value@127.0.0.1:4567", "http://127.0.0.1:4567/", "http://127.0.0.1:4567?private-value",
		"http://127.0.0.1:4567#private-value", "http://127.0.0.1:4567#", "http://127.0.0.1:4567?", "http://127.0.0.1:4567 ",
	} {
		input := "server listening on " + invalid + "\n"
		if invalid == "http://127.0.0.1:45678" {
			input = "unrecognized marker " + invalid + "\n"
		}
		if _, err := readServerAddress(strings.NewReader(input)); err == nil {
			t.Errorf("accepted invalid/unannounced address: %q", invalid)
		} else if strings.Contains(err.Error(), "private-value") {
			t.Fatal("startup error disclosed process output")
		}
	}
}

func TestReadServerAddressBoundsUntrustedOutput(t *testing.T) {
	const truncatedMarker = "server listening on http://127.0.0.1:12"
	// The output limit falls after :12, before the actual port's remaining
	// digits and newline. It must not turn 12345 into port 12.
	padding := strings.Repeat("x\n", (maxServerStartupOutput-len(truncatedMarker))/2)
	if len(padding)+len(truncatedMarker) < maxServerStartupOutput {
		padding = "\n" + padding
	}
	for _, output := range []string{
		strings.Repeat("private-value", 6000),
		strings.Repeat("private-value\n", maxServerStartupOutput/14+1) + "server listening on http://127.0.0.1:4567\n",
		"server listening on http://127.0.0.1:4567",
		padding + truncatedMarker + "345\n",
	} {
		if _, err := readServerAddress(strings.NewReader(output)); err == nil {
			t.Fatal("accepted startup output beyond protocol bound")
		} else if strings.Contains(err.Error(), "private-value") {
			t.Fatal("startup output limit error disclosed process output")
		}
	}
}

func serveFixtureClient(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	isolateUserHomes(t)
	dir := t.TempDir()
	t.Setenv("TEAMVA_TEST_OPENCODE_SERVE_MODE", mode)
	t.Setenv("TEAMVA_TEST_OPENCODE_SERVE_DIR", dir)
	executable := filepath.Join(dir, "fake-opencode")
	writeFile(t, executable, fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestOpenCodeServeFixture$ -- \"$@\"\n", os.Args[0]))
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, dir
}

func waitForFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not publish startup state")
}

func TestServerStartupUsesDynamicPortAndWaitsForPrivateReadyOutput(t *testing.T) {
	client, dir := serveFixtureClient(t, "delayed-ready")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.ensureStarted(ctx) }()
	waitForFixtureFile(t, filepath.Join(dir, "bound"))
	// A listener alone is not identity/readiness evidence. No authenticated
	// HTTP request may be sent before our launched process announces it.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "requested")); !os.IsNotExist(err) {
		t.Fatal("HTTP request sent before the private ready marker")
	}
	if client.currentBaseURL() != "" {
		t.Fatal("unannounced endpoint became available to concurrent callers")
	}
	writeFile(t, filepath.Join(dir, "release"), "ready")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	waitForFixtureFile(t, filepath.Join(dir, "requested"))
	if !strings.HasPrefix(client.currentBaseURL(), "http://127.0.0.1:") {
		t.Fatal("dynamic loopback endpoint was not installed")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if client.currentBaseURL() != "" {
		t.Fatal("closed server kept an authenticated endpoint")
	}
}

func TestServerStartupCancellationDoesNotProbeUnannouncedListener(t *testing.T) {
	client, dir := serveFixtureClient(t, "silent")
	ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
	defer cancel()
	err := client.ensureStarted(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded startup cancellation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "requested")); !os.IsNotExist(err) {
		t.Fatal("password sent to an endpoint without a ready marker")
	}
	if client.currentBaseURL() != "" {
		t.Fatal("failed startup retained an endpoint")
	}
}

func TestServerStartupRejectsExitedChildWithoutReadyOutput(t *testing.T) {
	client, _ := serveFixtureClient(t, "exit")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.ensureStarted(ctx); err == nil {
		t.Fatal("accepted exited child without a private listening address")
	}
}

func TestServerRequestsNeverForwardCredentialsViaRedirect(t *testing.T) {
	var forwarded atomic.Int32
	unknown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer unknown.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, unknown.URL, http.StatusFound)
	}))
	defer server.Close()
	client := testClient(t)
	client.baseURL = server.URL
	if err := client.getJSON(t.Context(), "/api/info", nil); err == nil {
		t.Fatal("unexpectedly followed server redirect")
	}
	if forwarded.Load() != 0 {
		t.Fatal("private server request reached an unrelated redirect endpoint")
	}
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("private loopback traffic can use an ambient proxy")
	}
}

func TestDiagnoseSurfacesSanitizedInvalidGlobalJSONC(t *testing.T) {
	isolateUserHomes(t)
	const private = "private-global-config-key"
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "opencode.jsonc"), `{"`+private+`": [,]}`)
	executable := filepath.Join(t.TempDir(), "version-only-opencode")
	writeFile(t, executable, "#!/bin/sh\n[ \"$1\" = --version ] && printf '2.0.test\\n'\n")
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	manifest, steps, err := client.Diagnose(t.Context())
	if err != nil || manifest.Available {
		t.Fatalf("invalid global configuration should fail diagnosis: err=%v", err)
	}
	var visible bool
	for _, step := range steps {
		if step.ID == "server" && step.Status == "failed" && strings.Contains(step.Detail, "opencode.jsonc") {
			visible = true
		}
		if strings.Contains(step.Detail, private) {
			t.Fatal("diagnostic disclosed configuration contents")
		}
	}
	if !visible {
		t.Fatal("invalid JSONC did not produce a visible diagnostic")
	}
}

// This subprocess fixture never starts a provider or invokes a model. The
// shell wrapper forwards the real adapter's serve arguments for validation.
func TestOpenCodeServeFixture(t *testing.T) {
	mode := os.Getenv("TEAMVA_TEST_OPENCODE_SERVE_MODE")
	if mode == "" {
		return
	}
	arguments := strings.Join(os.Args, " ")
	if !strings.Contains(arguments, "serve --hostname 127.0.0.1 --port 0") {
		os.Exit(2)
	}
	if mode == "exit" {
		os.Exit(0)
	}
	if mode == "managed-ready" && (os.Getenv("OPENCODE_CONFIG_PROJECT_DISABLE") != "1" || os.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG") != "1") {
		os.Exit(4)
	}
	dir := os.Getenv("TEAMVA_TEST_OPENCODE_SERVE_DIR")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = os.WriteFile(filepath.Join(dir, "requested"), []byte("request received"), 0o600)
		username, password, ok := r.BasicAuth()
		if !ok || username != "opencode" || password != os.Getenv("OPENCODE_SERVER_PASSWORD") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.0.fixture"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	_ = os.WriteFile(filepath.Join(dir, "bound"), []byte("bound with dynamic port"), 0o600)
	if mode == "managed-ready" {
		fmt.Printf("server listening on http://%s\n", listener.Addr())
	}
	if mode == "delayed-ready" {
		for {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Printf("server listening on http://%s\n", listener.Addr())
	}
	select {}
}

func TestManagedServerDisablesUncontrolledProjectConfigDiscovery(t *testing.T) {
	client, _ := serveFixtureClient(t, "managed-ready")
	client.WithConfigDir(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := client.ensureStarted(ctx)
	if runtime.GOOS != "darwin" {
		if err == nil || !strings.Contains(err.Error(), "sandbox") {
			t.Fatal("managed startup without an available OS sandbox must fail explicitly")
		}
		return
	}
	if err != nil {
		t.Fatalf("managed server did not disable project config discovery: %v", err)
	}
}
