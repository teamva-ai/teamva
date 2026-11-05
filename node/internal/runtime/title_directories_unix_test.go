//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func newTitleDirectoryTestRuntime(t *testing.T, base string) *Runtime {
	t.Helper()
	engine := New("title-directories", nil, nil, filepath.Join(base, "workspaces"), nil)
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func writeTitleCrashFixture(t *testing.T, attempt string) {
	t.Helper()
	for _, name := range []string{
		"manager/instances/fixture/instructions.md",
		"manager/provider-data/fixture/native-response.json",
		"manager/provider-data/fixture/auth.json",
		"manager/provider-data/fixture/provider-config.json",
	} {
		path := filepath.Join(attempt, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal("cannot create offline title crash fixture")
		}
		if err := os.WriteFile(path, []byte("offline private seed/output/credential fixture"), 0o600); err != nil {
			t.Fatal("cannot write offline title crash fixture")
		}
	}
}

func assertTitleFixturePresent(t *testing.T, attempt string) {
	t.Helper()
	for _, name := range []string{"instructions.md", "native-response.json", "auth.json", "provider-config.json"} {
		branch := "manager/provider-data/fixture"
		if name == "instructions.md" {
			branch = "manager/instances/fixture"
		}
		if _, err := os.Stat(filepath.Join(attempt, branch, name)); err != nil {
			t.Fatal("title cleanup touched a protected active/outside fixture")
		}
	}
}

func assertTitleAttemptGone(t *testing.T, attempt string) {
	t.Helper()
	if _, err := os.Lstat(attempt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale title attempt was retained after cleanup")
	}
}

func TestTitleDirectoriesStartupReapsOnlyControlledAttempts(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, titleDirectoryName)
	legacy := filepath.Join(root, "attempt-1234567890")
	current := filepath.Join(root, "attempt-"+strings.Repeat("a", 32))
	writeTitleCrashFixture(t, legacy)
	writeTitleCrashFixture(t, current)
	outside := filepath.Join(base, "agents", "provider-data", "attempt-123")
	writeTitleCrashFixture(t, outside)
	for _, target := range []string{outside, filepath.Join("..", "..", "agents", "provider-data", "attempt-123")} {
		name := "absolute-outside"
		if !filepath.IsAbs(target) {
			name = "relative-outside"
		}
		if err := os.Symlink(target, filepath.Join(legacy, name)); err != nil {
			t.Fatal(err)
		}
	}
	var protected []string
	for _, name := range []string{"user-notes", "attempt-not-owned", "attempt-12345678901", "attempt-../outside"} {
		path := filepath.Join(root, name)
		writeTitleCrashFixture(t, path)
		protected = append(protected, path)
	}
	// A same-shaped regular file is not a managed directory either.
	file := filepath.Join(root, "attempt-999")
	if err := os.WriteFile(file, []byte("not an attempt directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := newTitleDirectoryTestRuntime(t, base)
	if engine.titleDirectories == nil {
		t.Fatal("safe title directory ownership was unavailable")
	}
	assertTitleAttemptGone(t, legacy)
	assertTitleAttemptGone(t, current)
	assertTitleFixturePresent(t, outside)
	for _, path := range protected {
		assertTitleFixturePresent(t, path)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("startup cleanup deleted an unrelated regular file")
	}
	// Closing the Runtime also clears leftover host attempts before unlocking.
	closingAttempt := filepath.Join(root, "attempt-42")
	writeTitleCrashFixture(t, closingAttempt)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	assertTitleAttemptGone(t, closingAttempt)
	writeTitleCrashFixture(t, legacy)
	restarted := newTitleDirectoryTestRuntime(t, base)
	if restarted.titleDirectories == nil {
		t.Fatal("normal close did not release the title directory lock")
	}
	assertTitleAttemptGone(t, legacy)
	assertTitleFixturePresent(t, outside)
}

func TestTitleDirectoriesRejectSymlinkRootsAndAttemptEntries(t *testing.T) {
	for _, mode := range []string{"root", "absolute-attempt", "relative-attempt"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "user-provider")
			writeTitleCrashFixture(t, filepath.Join(outside, "attempt-321"))
			root := filepath.Join(base, titleDirectoryName)
			link := root
			target := outside
			if mode != "root" {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(root, "attempt-123")
				if mode == "relative-attempt" {
					target = filepath.Join("..", "user-provider")
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			engine := newTitleDirectoryTestRuntime(t, base)
			if engine.titleDirectories != nil {
				t.Fatal("title cleanup accepted a symlink scope/attempt")
			}
			if result, err := engine.GenerateTitle(t.Context(), titleRequest()); err == nil || err.Error() != "generation_failed" || result.Title != "" {
				t.Fatal("an unsafe title scope did not fail closed")
			}
			assertTitleFixturePresent(t, filepath.Join(outside, "attempt-321"))
			if _, err := os.Lstat(link); err != nil {
				t.Fatal("refusing a symlink unexpectedly removed it")
			}
			// An initialization error after acquiring the directory lock must not
			// leave that lock held when the invalid link is repaired.
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			retry := newTitleDirectoryTestRuntime(t, base)
			if retry.titleDirectories == nil {
				t.Fatal("failed initialization leaked a title directory lock")
			}
		})
	}
}

func TestTitleDirectoriesReplacementCannotRedirectAttemptCleanup(t *testing.T) {
	base := t.TempDir()
	engine := newTitleDirectoryTestRuntime(t, base)
	if engine.titleDirectories == nil {
		t.Fatal("missing title directory owner")
	}
	attempt, cleanup, err := engine.titleDirectories.newAttempt()
	if err != nil {
		t.Fatal(err)
	}
	writeTitleCrashFixture(t, attempt)
	root := filepath.Join(base, titleDirectoryName)
	moved := filepath.Join(base, "moved-controlled-titles")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "user-provider")
	outsideAttempt := filepath.Join(outside, filepath.Base(attempt))
	writeTitleCrashFixture(t, outsideAttempt)
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.titleDirectories.newAttempt(); err == nil {
		t.Fatal("new title attempt followed a replaced root symlink")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	assertTitleAttemptGone(t, filepath.Join(moved, filepath.Base(attempt)))
	assertTitleFixturePresent(t, outsideAttempt)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	assertTitleFixturePresent(t, outsideAttempt)
}

func TestTitleDirectoriesParallelRuntimeCannotReapActiveAttempt(t *testing.T) {
	base := t.TempDir()
	engine := newTitleDirectoryTestRuntime(t, base)
	started := make(chan string, 1)
	cancelled, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	engine.titleManagerFactory = func(provider, executable, root string) *Manager {
		manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
		manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, _ adapter.Sink) (adapter.NativeSession, error) {
			attempt := filepath.Dir(root)
			writeTitleCrashFixture(t, attempt)
			started <- attempt
			<-ctx.Done()
			close(cancelled)
			<-release
			return adapter.NativeSession{}, ctx.Err()
		}})
		return manager
	}
	generated := make(chan error, 1)
	go func() { _, err := engine.GenerateTitle(t.Context(), titleRequest()); generated <- err }()
	var attempt string
	select {
	case attempt = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("isolated offline title attempt did not start")
	}
	competitor := newTitleDirectoryTestRuntime(t, base)
	if competitor.titleDirectories != nil {
		t.Fatal("second Runtime acquired an active owner's title scope")
	}
	if _, err := competitor.GenerateTitle(t.Context(), titleRequest()); err == nil {
		t.Fatal("second Runtime started a title without exclusive ownership")
	}
	assertTitleFixturePresent(t, attempt)
	if err := competitor.Close(); err != nil {
		t.Fatal(err)
	}
	closing := make(chan error, 1)
	go func() { closing <- engine.Close() }()
	<-cancelled
	// The ownership lock must outlive ctx cancellation and remain held until
	// the native adapter has actually returned and its attempt has been removed.
	duringClose := newTitleDirectoryTestRuntime(t, base)
	if duringClose.titleDirectories != nil {
		t.Fatal("Runtime released title ownership before native cleanup finished")
	}
	assertTitleFixturePresent(t, attempt)
	select {
	case <-closing:
		t.Fatal("Runtime.Close did not join the active title attempt")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-generated; !errors.Is(err, context.Canceled) {
		t.Fatal("closing title attempt did not observe cancellation")
	}
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
	assertTitleAttemptGone(t, attempt)
	restarted := newTitleDirectoryTestRuntime(t, base)
	if restarted.titleDirectories == nil {
		t.Fatal("finished Runtime did not release title ownership")
	}
}

func TestTitleDirectoriesCloseFailureStillReleasesOwnership(t *testing.T) {
	base := t.TempDir()
	engine := newTitleDirectoryTestRuntime(t, base)
	if engine.titleDirectories == nil {
		t.Fatal("missing title directory owner")
	}
	outside := filepath.Join(base, "user-provider")
	writeTitleCrashFixture(t, outside)
	link := filepath.Join(base, titleDirectoryName, "attempt-123")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err == nil || err.Error() != "generation_failed" {
		t.Fatal("unsafe shutdown cleanup was not reported with a fixed error")
	}
	assertTitleFixturePresent(t, outside)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	restarted := newTitleDirectoryTestRuntime(t, base)
	if restarted.titleDirectories == nil {
		t.Fatal("failed shutdown leaked its directory lock")
	}
}

// This helper is a separate process with a fake adapter, not a Provider/model
// test. Its only stdout is a readiness marker; no seed/config/output is printed.
func TestTitleDirectoriesCrashHelper(t *testing.T) {
	if os.Getenv("TEAMVA_TITLE_CRASH_HELPER") != "1" {
		return
	}
	engine := New("title-crash-helper", nil, nil, filepath.Join(os.Getenv("TEAMVA_TITLE_CRASH_ROOT"), "workspaces"), nil)
	if engine.titleDirectories == nil {
		t.Fatal("crash helper could not own the title scope")
	}
	engine.titleManagerFactory = func(provider, executable, root string) *Manager {
		manager := NewManager(provider, executable, root, nil, log.New(io.Discard, "", 0))
		manager.RegisterTestAdapter(&titleTestAdapter{fakeAdapter: &fakeAdapter{}, startTitle: func(ctx context.Context, _ adapter.RunRequest, _ adapter.Sink) (adapter.NativeSession, error) {
			writeTitleCrashFixture(t, filepath.Dir(root))
			fmt.Fprintln(os.Stdout, "TITLE_CRASH_READY")
			<-ctx.Done()
			return adapter.NativeSession{}, ctx.Err()
		}})
		return manager
	}
	_, _ = engine.GenerateTitle(context.Background(), titleRequest())
	t.Fatal("crash helper unexpectedly returned before being killed")
}

func TestTitleDirectoriesSIGKILLRestartReapsPrivateNativeData(t *testing.T) {
	base := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestTitleDirectoriesCrashHelper$", "-test.count=1")
	command.Env = append(os.Environ(), "TEAMVA_TITLE_CRASH_HELPER=1", "TEAMVA_TITLE_CRASH_ROOT="+base)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "TITLE_CRASH_READY\n" {
			t.Fatal("offline crash helper failed before readiness")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("offline crash helper readiness timed out")
	}
	root := filepath.Join(base, titleDirectoryName)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !isTitleAttemptName(entries[0].Name()) {
		t.Fatal("offline crash helper did not create exactly one managed attempt")
	}
	attempt := filepath.Join(root, entries[0].Name())
	assertTitleFixturePresent(t, attempt)
	competitor := newTitleDirectoryTestRuntime(t, base)
	if competitor.titleDirectories != nil {
		t.Fatal("cross-process competitor acquired a running title's scope")
	}
	assertTitleFixturePresent(t, attempt)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("offline crash helper was not terminated abnormally")
	}
	// No GenerateTitle call: startup itself must erase the orphaned private
	// instructions, native output, auth/config fixtures left by SIGKILL.
	restarted := newTitleDirectoryTestRuntime(t, base)
	if restarted.titleDirectories == nil {
		t.Fatal("SIGKILL did not release title directory ownership")
	}
	assertTitleAttemptGone(t, attempt)
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("restart retained private native attempt files")
	}
}
