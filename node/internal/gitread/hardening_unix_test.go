//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"golang.org/x/sys/unix"
)

func TestSandboxRejectsSymlinkedMetadataParent(t *testing.T) {
	repositoryRoot := t.TempDir()
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "tracked.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "tracked.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "initial")
	gitCommand(t, repositoryRoot, "config", "core.sparseCheckout", "true")

	outside := t.TempDir()
	secret := "outside-sparse-secret-must-not-leak"
	writeFile(t, filepath.Join(outside, "sparse-checkout"), secret+"\n")
	infoPath := filepath.Join(repositoryRoot, ".git", "info")
	if err := os.RemoveAll(infoPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, infoPath); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}

	service := New()
	defer service.Close()
	response, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-symlinked-metadata",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: repositoryRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("sandbox leaked metadata reached through a symlinked parent: %s", encoded)
	}
	if len(response.Repositories) != 1 || response.Repositories[0].State != "unavailable" {
		t.Fatalf("symlinked metadata parent was not rejected: %+v", response.Repositories)
	}
}

func TestSandboxRejectsSymlinkedObjectFile(t *testing.T) {
	repositoryRoot := t.TempDir()
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "tracked.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "tracked.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "initial")
	ref, err := os.ReadFile(filepath.Join(repositoryRoot, ".git", "refs", "heads", "main"))
	if err != nil {
		t.Fatal(err)
	}
	objectID := strings.TrimSpace(string(ref))
	if len(objectID) < 3 {
		t.Fatalf("invalid object id %q", objectID)
	}
	objectPath := filepath.Join(repositoryRoot, ".git", "objects", objectID[:2], objectID[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-object")
	secret := "outside-object-secret-must-not-leak"
	writeFile(t, outside, secret)
	if err := os.Symlink(outside, objectPath); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}

	service := New()
	defer service.Close()
	response, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-symlinked-object",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: repositoryRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("sandbox leaked a symlinked object file: %s", encoded)
	}
	if len(response.Repositories) != 1 || response.Repositories[0].State != "unavailable" {
		t.Fatalf("symlinked object file was not rejected: %+v", response.Repositories)
	}
}

func TestClonedObjectPostCheckRejectsAtomicallyInsertedRecursiveAlternates(t *testing.T) {
	snapshot := t.TempDir()
	nested := filepath.Join(snapshot, "nested-object-store")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "info")
	if err := os.MkdirAll(prepared, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(prepared, "alternates"), "/outside/object/store\n")
	// Model an atomic objects/info replacement that lands after the source-side
	// pre-check but before the completed private-copy post-check.
	if err := os.Rename(prepared, filepath.Join(nested, "info")); err != nil {
		t.Fatal(err)
	}
	err := validateClonedObjectAlternates(t.Context(), snapshot)
	var gitErr *nodeprotocol.GitReadError
	if !errors.As(err, &gitErr) || gitErr.Code != "git_unsupported" {
		t.Fatalf("recursive alternates post-check error=%v", err)
	}
}

func TestPartialExecutionScopeHashChangesWithDirectoryIdentity(t *testing.T) {
	if !platformGitSandboxSupported() {
		t.Skip("safe Git sandbox is unavailable on this platform")
	}
	repositoryRoot := t.TempDir()
	scopePath := filepath.Join(repositoryRoot, "scope")
	if err := os.Mkdir(scopePath, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-scope-identity",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Scope", Path: scopePath}},
	}
	service := New()
	defer service.Close()
	repositories, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(repositories.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories.Repositories, err)
	}
	repositoryID := repositories.Repositories[0].ID
	before, err := service.ResolveRepositoryExecutionScope(t.Context(), request, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(scopePath, scopePath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scopePath, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := service.ResolveRepositoryExecutionScope(t.Context(), request, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatalf("partial scope hash ignored directory replacement: before=%+v after=%+v", before, after)
	}
	if err := service.ValidateRepositoryExecutionScope(t.Context(), request, repositoryID, before); err == nil {
		t.Fatal("replaced partial scope accepted its stale execution boundary")
	}
}

func TestSecureReadRegularRejectsTrackedFIFOWithoutBlocking(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	path := filepath.Join(repositoryRoot, "tracked.txt")
	writeFile(t, path, "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "tracked.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, err := secureReadRegular(ctx, repositoryRoot, "tracked.txt", 1<<20)
	if !errors.Is(err, errUnsafePath) {
		t.Fatalf("FIFO read error=%v, want unsafe path", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("FIFO open blocked for %s", elapsed)
	}
}

func TestDefaultServiceIgnoresHostilePATHGit(t *testing.T) {
	base := t.TempDir()
	fakeGit := filepath.Join(base, "git")
	marker := filepath.Join(base, "executed")
	writeFile(t, fakeGit, "#!/bin/sh\nprintf invoked > "+strconv.Quote(marker)+"\nexit 0\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", base)
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.gitErr != nil {
		t.Fatal(service.gitErr)
	}
	if samePath(service.gitPath, fakeGit) {
		t.Fatalf("hostile PATH executable was trusted: %q", service.gitPath)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hostile PATH executable ran during service initialization: %v", err)
	}
}

func TestGitExecutableSnapshotCannotBeSwappedAfterServiceCreation(t *testing.T) {
	base := t.TempDir()
	initRepository(t, base)
	fakeGit := filepath.Join(base, "git")
	marker := filepath.Join(base, "swapped-executable-ran")
	writeFile(t, fakeGit, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	identity, err := service.confirmRepository(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	repo := &repository{
		root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
		rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
		commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
		markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
		settings: identity.settings,
	}
	t.Cleanup(repo.closeIdentity)
	writeFile(t, fakeGit, "#!/bin/sh\nprintf swapped > "+strconv.Quote(marker)+"\nexit 0\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.runGit(t.Context(), repo, 1<<20, false, "status", "--porcelain=v2", "-z"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("post-construction executable replacement ran: %v", err)
	}
}

func TestRunGitUsesPinnedWorktreeAcrossABASwap(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "target.txt"), "original")
	ready := filepath.Join(base, "ready")
	readDone := filepath.Join(base, "read-done")
	observed := filepath.Join(base, "observed")
	fakeGit := filepath.Join(base, "git")
	writeFile(t, fakeGit, `#!/bin/sh
printf ready > `+strconv.Quote(ready)+`
sleep 0.2
value=$(/bin/cat target.txt)
printf '%s' "$value" > `+strconv.Quote(observed)+`
printf done > `+strconv.Quote(readDone)+`
sleep 0.2
printf '%s' "$value"
`)
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	identity, err := service.confirmRepository(t.Context(), repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository := &repository{
		root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
		rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
		commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
		markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
		settings: identity.settings,
	}
	t.Cleanup(repository.closeIdentity)
	result := make(chan struct {
		output gitOutput
		err    error
	}, 1)
	go func() {
		output, runErr := service.runGit(context.Background(), repository, 1<<20, false,
			"status", "--porcelain=v2", "-z")
		result <- struct {
			output gitOutput
			err    error
		}{output: output, err: runErr}
	}()
	waitForPath := func(path string) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); ; {
			if _, statErr := os.Stat(path); statErr == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", filepath.Base(path))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForPath(ready)
	moved := repositoryRoot + "-moved"
	if err := os.Rename(repositoryRoot, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repositoryRoot, "target.txt"), "replacement")
	waitForPath(readDone)
	if err := os.RemoveAll(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	completed := <-result
	if completed.err != nil || string(completed.output.data) != "original" {
		t.Fatalf("ABA-bound Git output=%q err=%v", completed.output.data, completed.err)
	}
	payload, err := os.ReadFile(observed)
	if err != nil || string(payload) != "original" {
		t.Fatalf("Git observed replacement worktree: payload=%q err=%v", payload, err)
	}
}

func TestDiffRejectsCrossScopeRenameMetadataFromGitProcess(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	scopeRoot := filepath.Join(repositoryRoot, "scope")
	if err := os.MkdirAll(scopeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	fakeGit := filepath.Join(base, "git")
	writeFile(t, fakeGit, "#!/bin/sh\nprintf '%s\\n' 'diff --git a/outside.txt b/scope/new.txt' 'similarity index 100%' 'rename from outside.txt' 'rename to scope/new.txt'\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	identity, err := service.confirmRepository(t.Context(), repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "scope-resource", Name: "Scope", Path: scopeRoot}}
	repo := &repository{
		id: "repository", root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
		rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
		commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
		markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
		settings: identity.settings, authPrefixes: []string{"scope"},
		summary: nodeprotocol.GitRepositorySummary{ID: "repository", Generation: 1},
	}
	project := &projectSnapshot{
		projectID: "cross-scope-project", resourcesHash: hashResources(resources), snapshotID: "project-snapshot",
		repositories: map[string]*repository{"repository": repo}, ordered: []string{"repository"},
	}
	service.mu.Lock()
	service.installProjectLocked(project)
	snapshot := service.newChangeSnapshotLocked(project, repo, "cross-scope-change", "staged", "", false,
		[]internalChange{{
			path:   "scope/new.txt",
			public: nodeprotocol.GitChange{Area: "staged", Status: "renamed", Kind: "file", DisplayPath: "scope/new.txt"},
		}}, nil, false)
	changeID := snapshot.items[0].public.ID
	service.mu.Unlock()
	_, err = service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "cross-scope-project", Resources: resources, RepositoryID: repo.id,
		ChangeID: changeID, Context: 3,
	})
	var readError *nodeprotocol.GitReadError
	if !errors.As(err, &readError) || readError.Code != "path_out_of_scope" {
		t.Fatalf("cross-scope rename diff error=%v", err)
	}
}

func TestSubscribeRegistersBeforeRepositoryInitialization(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	ready := filepath.Join(base, "git-ready")
	release := filepath.Join(base, "git-release")
	fakeGit := filepath.Join(base, "git")
	writeFile(t, fakeGit, `#!/bin/sh
printf ready > `+strconv.Quote(ready)+`
while [ ! -f `+strconv.Quote(release)+` ]; do sleep 0.01; done
exit 1
`)
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "atomic-subscribe-project",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: repositoryRoot}},
	}
	type subscriptionResult struct {
		events      <-chan nodeprotocol.GitEvent
		unsubscribe func()
		err         error
	}
	result := make(chan subscriptionResult, 1)
	go func() {
		events, unsubscribe, err := service.Subscribe(context.Background(), request)
		result <- subscriptionResult{events: events, unsubscribe: unsubscribe, err: err}
	}()
	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription initialization did not reach Git read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	service.mu.Lock()
	if len(service.subscribers[request.ProjectID]) != 1 {
		service.mu.Unlock()
		t.Fatal("subscriber was not registered before initialization")
	}
	service.emitLocked(request.ProjectID, nodeprotocol.GitEvent{
		RepositoryID: "repository-race", Generation: 7, Reason: "initialization_race",
	})
	service.mu.Unlock()
	writeFile(t, release, "release")
	completed := <-result
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	defer completed.unsubscribe()
	select {
	case event := <-completed.events:
		if event.RepositoryID != "repository-race" || event.Generation != 7 {
			t.Fatalf("initialization event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event racing subscription initialization was lost")
	}
}

func TestServiceCloseKillsWholeFakeGitProcessGroup(t *testing.T) {
	base := t.TempDir()
	initRepository(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(base, "child-pid")
	fakeGit := filepath.Join(bin, "git")
	writeFile(t, fakeGit, `#!/bin/sh
for argument in "$@"; do
  if [ "$argument" = "config" ]; then
    exit 1
  fi
done
sleep 300 &
child=$!
printf '%s' "$child" > `+strconv.Quote(childPIDFile)+`
wait "$child"
`)
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	identity, err := service.confirmRepository(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	repository := &repository{
		root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
		rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
		commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
		markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
		settings: identity.settings,
	}
	t.Cleanup(repository.closeIdentity)
	result := make(chan error, 1)
	go func() {
		_, err := service.runGit(context.Background(), repository, 1<<20, false, "status", "--porcelain=v2", "-z")
		result <- err
	}()
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(childPIDFile)
		if err == nil {
			childPID, err = strconv.Atoi(string(payload))
			if err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID <= 0 {
		_ = service.Close()
		t.Fatal("fake Git child did not start")
	}
	closedAt := time.Now()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closedAt); elapsed > 3*time.Second {
		t.Fatalf("Service.Close took %s", elapsed)
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("runGit remained active after Service.Close")
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		err := unix.Kill(childPID, 0)
		if errors.Is(err, unix.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake Git descendant %d survived Close (kill probe=%v)", childPID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSuccessfulGitLeaderExitStillReapsDescendantProcessGroup(t *testing.T) {
	base := t.TempDir()
	initRepository(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(base, "detached-child-pid")
	fakeGit := filepath.Join(bin, "git")
	writeFile(t, fakeGit, `#!/bin/sh
sleep 300 </dev/null >/dev/null 2>&1 &
child=$!
printf '%s' "$child" > `+strconv.Quote(childPIDFile)+`
exit 0
`)
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	identity, err := service.confirmRepository(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	repository := &repository{
		root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
		rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
		commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
		markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
		settings: identity.settings,
	}
	t.Cleanup(repository.closeIdentity)
	if _, err := service.runGit(t.Context(), repository, 1<<20, false, "status", "--porcelain=v2", "-z"); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(payload))
	if err != nil || childPID <= 0 {
		t.Fatalf("child pid=%q err=%v", payload, err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		err := unix.Kill(childPID, 0)
		if errors.Is(err, unix.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("successful Git leader left descendant %d alive (probe=%v)", childPID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcherRebuildsAfterDirectoryDeleteAndRecreate(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	nested := filepath.Join(repositoryRoot, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(nested, "file.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", ".")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.watcher == nil {
		t.Skip("fsnotify is unavailable")
	}
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "watch-rebuild-project",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "watch-rebuild-resource", Name: "Code", Path: repositoryRoot}},
	}
	if _, err := service.ListRepositories(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe, err := service.Subscribe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	waitForGitEvent(t, events, 4*time.Second)
	drainGitEvents(events, 600*time.Millisecond)
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	waitForGitEvent(t, events, 4*time.Second)
	drainGitEvents(events, 600*time.Millisecond)
	writeFile(t, filepath.Join(nested, "after-recreate.txt"), "changed\n")
	waitForGitEvent(t, events, 4*time.Second)
}

func TestWatcherContinuousEventsInvalidateWithinMaximumDelay(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	path := filepath.Join(repositoryRoot, "file.txt")
	writeFile(t, path, "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "file.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.watcher == nil {
		t.Skip("fsnotify is unavailable")
	}
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "watch-max-project",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "watch-max-resource", Name: "Code", Path: repositoryRoot}},
	}
	if _, err := service.ListRepositories(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe, err := service.Subscribe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	started := time.Now()
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		for index := 0; index < 30; index++ {
			_ = os.WriteFile(path, []byte(fmt.Sprintf("write-%d\n", index)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	waitForGitEvent(t, events, 3*time.Second)
	if elapsed := time.Since(started); elapsed > 2600*time.Millisecond {
		t.Fatalf("continuous write invalidation arrived after %s", elapsed)
	}
	<-writesDone
}

func TestReconcileHundredRepositoriesIsBoundedlyConcurrent(t *testing.T) {
	base := t.TempDir()
	initRepository(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeGit := filepath.Join(bin, "git")
	writeFile(t, fakeGit, "#!/bin/sh\nsleep 0.1\nexit 1\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewWithExecutable(fakeGit)
	t.Cleanup(func() { _ = service.Close() })
	identity, err := service.confirmRepository(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	project := &projectSnapshot{
		projectID: "reconcile-project", resourcesHash: hashResources(nil), snapshotID: "snapshot",
		repositories: make(map[string]*repository),
	}
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("repository-%03d", index)
		project.ordered = append(project.ordered, id)
		project.repositories[id] = &repository{
			id: id, root: identity.root, gitDir: identity.gitDir, commonDir: identity.commonDir,
			rootIdentity: identity.rootIdentity, gitIdentity: identity.gitIdentity,
			commonIdentity: identity.commonIdentity, objectIdentity: identity.objectIdentity,
			markerIdentity: identity.markerIdentity, commonMarker: identity.commonMarker,
			settings:     identity.settings,
			authPrefixes: []string{""}, summary: nodeprotocol.GitRepositorySummary{ID: id, Generation: int64(index + 1)},
		}
	}
	service.mu.Lock()
	service.installProjectLocked(project)
	service.mu.Unlock()
	started := time.Now()
	service.reconcile()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("100-repository reconcile took %s; work was effectively serial", elapsed)
	}
}

func waitForGitEvent(t *testing.T, events <-chan nodeprotocol.GitEvent, timeout time.Duration) nodeprotocol.GitEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("Git event channel closed")
		}
		return event
	case <-time.After(timeout):
		t.Fatal("Git event was not received")
		return nodeprotocol.GitEvent{}
	}
}

func drainGitEvents(events <-chan nodeprotocol.GitEvent, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case <-events:
		case <-timer.C:
			return
		}
	}
}
