//go:build darwin || linux

package gitread

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/fsnotify/fsnotify"
)

func TestRealFSNotifyPureReadsKeepCursorsAndMutationsInvalidate(t *testing.T) {
	fixture := newRealWatchFixture(t)
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.watcher == nil {
		t.Skip("fsnotify is unavailable")
	}

	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-real-watch",
		Resources: []nodeprotocol.GitResourceRequest{
			{ResourceID: "resource-scope", Name: "Scoped", Path: fixture.scope},
			{ResourceID: "resource-second", Name: "Second", Path: fixture.second},
		},
	}
	listed, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(listed.Repositories) != 3 {
		t.Fatalf("repositories=%+v err=%v", listed, err)
	}
	var parent, second nodeprotocol.GitRepositorySummary
	for _, item := range listed.Repositories {
		switch {
		case item.Scope == "resource_prefix":
			parent = item
		case contains(item.ResourceIDs, "resource-second"):
			second = item
		}
	}
	if parent.ID == "" || second.ID == "" {
		t.Fatalf("parent/second repository missing: %+v", listed.Repositories)
	}

	rawWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawWatcher.Close() })
	if err := rawWatcher.Add(fixture.unstagedOnly); err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	installedParent := service.projects[request.ProjectID].repositories[parent.ID]
	initialFingerprint := installedParent.fingerprint
	initialGeneration := installedParent.summary.Generation
	service.mu.Unlock()
	indexBefore, indexObserved := observeWatchPath(filepath.Join(fixture.parent, ".git", "index"))
	if !indexObserved {
		t.Fatal("could not observe source index before pure reads")
	}

	// The four areas deliberately start together. A read in one area must not
	// advance the shared repository generation or evict the other three pages.
	type areaResult struct {
		area string
		page nodeprotocol.GitChangesPage
		err  error
	}
	start := make(chan struct{})
	results := make(chan areaResult, 4)
	var workers sync.WaitGroup
	for _, area := range []string{"conflict", "staged", "unstaged", "untracked"} {
		workers.Add(1)
		go func(area string) {
			defer workers.Done()
			<-start
			page, listErr := service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
				ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
				Area: area, Limit: 1,
			})
			results <- areaResult{area: area, page: page, err: listErr}
		}(area)
	}
	close(start)
	workers.Wait()
	close(results)
	firstPages := make(map[string]nodeprotocol.GitChangesPage, 4)
	for result := range results {
		if result.err != nil || len(result.page.Items) != 1 || result.page.NextCursor == "" {
			t.Fatalf("%s first page=%+v err=%v", result.area, result.page, result.err)
		}
		firstPages[result.area] = result.page
	}

	if runtime.GOOS == "darwin" {
		// git diff --numstat reads the real worktree. Darwin reports its atime
		// update as NOTE_ATTRIB/fsnotify.Chmod. Feed that real kernel event through
		// the service synchronously as a deterministic watch-loop barrier.
		event := waitForRawChmod(t, rawWatcher, fixture.unstagedOnly)
		service.queueWatchEvent(event)
		service.flushPendingEvents(time.Now().Add(3 * time.Second))
	}
	for area, first := range firstPages {
		secondPage, pageErr := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
			ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
			Area: area, Cursor: first.NextCursor, Limit: 1,
		})
		if pageErr != nil || len(secondPage.Items) != 1 ||
			secondPage.RepositoryGeneration != first.RepositoryGeneration {
			t.Fatalf("%s retained cursor page=%+v err=%v", area, secondPage, pageErr)
		}
	}

	// Keep another cursor while Diff reads an untracked file directly from the
	// source worktree. This covers the non-Git secureRead path as well as status
	// and numstat subprocesses.
	untrackedFirst, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
		Area: "untracked", Limit: 1,
	})
	if err != nil || untrackedFirst.NextCursor == "" {
		t.Fatalf("untracked retained page=%+v err=%v", untrackedFirst, err)
	}
	untrackedAll, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
		Area: "untracked", Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	untrackedChange := findChange(untrackedAll.Items, "scope/read-only-untracked.txt")
	if untrackedChange == nil {
		t.Fatalf("untracked read target missing: %+v", untrackedAll.Items)
	}
	if _, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
		ChangeID: untrackedChange.ID, Context: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: parent.ID,
		Area: "untracked", Cursor: untrackedFirst.NextCursor, Limit: 1,
	}); err != nil {
		t.Fatalf("cursor failed after pure Diff: %v", err)
	}

	service.mu.Lock()
	installedParent = service.projects[request.ProjectID].repositories[parent.ID]
	stableGeneration := installedParent.summary.Generation
	stableFingerprint := installedParent.fingerprint
	service.mu.Unlock()
	indexAfter, indexStillObserved := observeWatchPath(filepath.Join(fixture.parent, ".git", "index"))
	if stableGeneration != initialGeneration || stableFingerprint != initialFingerprint ||
		!indexStillObserved || indexAfter != indexBefore {
		t.Fatalf("pure reads changed generation/fingerprint/index: generation=%d/%d fingerprint_equal=%t index_equal=%t",
			stableGeneration, initialGeneration, stableFingerprint == initialFingerprint,
			indexStillObserved && indexAfter == indexBefore)
	}

	events, unsubscribe, err := service.Subscribe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	// A nested source write must still invalidate through the recursively
	// installed watch, emit to concurrent subscribers, and reject the old page.
	parentCursor := mustFirstCursor(t, service, request, parent.ID)
	writeFile(t, fixture.deepTracked, "real worktree mutation\n")
	writeEvent := waitForRepositoryGeneration(t, events, parent.ID, parentCursor.RepositoryGeneration)
	assertCursorInvalid(t, service, request, parent.ID, parentCursor)

	// Refresh the dirty summary without an explicit refresh barrier, then prove
	// that real index and ref updates remain visible independently.
	parentNow := repositorySummary(t, service, request, parent.ID)
	if parentNow.Generation < writeEvent.Generation {
		t.Fatalf("parent generation regressed: summary=%d event=%d", parentNow.Generation, writeEvent.Generation)
	}

	secondCursor := mustFirstCursor(t, service, request, second.ID)
	gitCommand(t, fixture.second, "add", "--", "second.txt")
	addEvent := waitForRepositoryGeneration(t, events, second.ID, secondCursor.RepositoryGeneration)
	assertCursorInvalid(t, service, request, second.ID, secondCursor)
	secondNow := repositorySummary(t, service, request, second.ID)
	if secondNow.Generation < addEvent.Generation {
		t.Fatalf("second generation after add regressed: summary=%d event=%d", secondNow.Generation, addEvent.Generation)
	}

	commitCursor := mustFirstCursor(t, service, request, second.ID)
	gitCommand(t, fixture.second, "commit", "-m", "real ref mutation")
	_ = waitForRepositoryGeneration(t, events, second.ID, commitCursor.RepositoryGeneration)
	assertCursorInvalid(t, service, request, second.ID, commitCursor)

	// Chmod is also delivered as fsnotify.Chmod, but unlike an atime-only read
	// it changes the mode/ctime fingerprint and must never be suppressed.
	_ = repositorySummary(t, service, request, second.ID)
	chmodCursor := mustFirstCursor(t, service, request, second.ID)
	if err := os.Chmod(fixture.secondUntrackedA, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = waitForRepositoryGeneration(t, events, second.ID, chmodCursor.RepositoryGeneration)
	assertCursorInvalid(t, service, request, second.ID, chmodCursor)
}

type realWatchFixture struct {
	parent           string
	scope            string
	second           string
	unstagedOnly     string
	deepTracked      string
	secondUntrackedA string
}

func newRealWatchFixture(t *testing.T) realWatchFixture {
	t.Helper()
	base := t.TempDir()
	parent := filepath.Join(base, "mono")
	scope := filepath.Join(parent, "scope")
	nested := filepath.Join(scope, "depth-one", "nested")
	second := filepath.Join(base, "second")
	for _, directory := range []string{nested, second, filepath.Join(scope, "ordinary", "deep")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	initRepository(t, parent)
	writeFile(t, filepath.Join(parent, ".gitignore"), "scope/depth-one/nested/\n")
	writeFile(t, filepath.Join(parent, "outside.txt"), "outside base\n")
	writeFile(t, filepath.Join(scope, "partial.txt"), "partial base\n")
	writeFile(t, filepath.Join(scope, "staged-only.txt"), "staged base\n")
	unstagedOnly := filepath.Join(scope, "unstaged-only.txt")
	writeFile(t, unstagedOnly, "unstaged base\n")
	deepTracked := filepath.Join(scope, "ordinary", "deep", "event.txt")
	writeFile(t, deepTracked, "event base\n")
	for _, name := range []string{"conflict-a.txt", "conflict-b.txt"} {
		writeFile(t, filepath.Join(scope, name), "conflict base\n")
	}
	gitCommand(t, parent, "add", "--", ".")
	gitCommand(t, parent, "commit", "-m", "initial parent")
	gitCommand(t, parent, "checkout", "-b", "conflict-side")
	for _, name := range []string{"conflict-a.txt", "conflict-b.txt"} {
		writeFile(t, filepath.Join(scope, name), "conflict side\n")
	}
	gitCommand(t, parent, "add", "--", "scope/conflict-a.txt", "scope/conflict-b.txt")
	gitCommand(t, parent, "commit", "-m", "side conflicts")
	gitCommand(t, parent, "checkout", "main")
	for _, name := range []string{"conflict-a.txt", "conflict-b.txt"} {
		writeFile(t, filepath.Join(scope, name), "conflict main\n")
	}
	gitCommand(t, parent, "add", "--", "scope/conflict-a.txt", "scope/conflict-b.txt")
	gitCommand(t, parent, "commit", "-m", "main conflicts")
	gitCommandMustFail(t, parent, "merge", "conflict-side")
	writeFile(t, filepath.Join(scope, "partial.txt"), "partial staged\n")
	gitCommand(t, parent, "add", "--", "scope/partial.txt")
	writeFile(t, filepath.Join(scope, "partial.txt"), "partial staged\npartial worktree\n")
	writeFile(t, filepath.Join(scope, "staged-only.txt"), "staged changed\n")
	gitCommand(t, parent, "add", "--", "scope/staged-only.txt")
	writeFile(t, unstagedOnly, "unstaged changed\n")
	writeFile(t, filepath.Join(scope, "untracked-a.txt"), "untracked a\n")
	writeFile(t, filepath.Join(scope, "untracked-b.txt"), "untracked b\n")
	untrackedRead := filepath.Join(scope, "read-only-untracked.txt")
	writeFile(t, untrackedRead, "read without mutation\n")

	initRepository(t, nested)
	writeFile(t, filepath.Join(nested, "nested.txt"), "nested base\n")
	gitCommand(t, nested, "add", "--", "nested.txt")
	gitCommand(t, nested, "commit", "-m", "initial nested")
	writeFile(t, filepath.Join(nested, "nested.txt"), "nested changed\n")

	initRepository(t, second)
	writeFile(t, filepath.Join(second, "second.txt"), "second base\n")
	gitCommand(t, second, "add", "--", "second.txt")
	gitCommand(t, second, "commit", "-m", "initial second")
	writeFile(t, filepath.Join(second, "second.txt"), "second changed\n")
	secondUntrackedA := filepath.Join(second, "untracked-a.txt")
	writeFile(t, secondUntrackedA, "second untracked a\n")
	writeFile(t, filepath.Join(second, "untracked-b.txt"), "second untracked b\n")

	// Make the first content reads deterministically update atime on Darwin.
	for _, path := range []string{unstagedOnly} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Now().Add(-48*time.Hour), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	return realWatchFixture{
		parent: parent, scope: scope, second: second, unstagedOnly: unstagedOnly,
		deepTracked: deepTracked, secondUntrackedA: secondUntrackedA,
	}
}

func waitForRawChmod(t *testing.T, watcher *fsnotify.Watcher, path string) fsnotify.Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				t.Fatal("raw fsnotify event stream closed")
			}
			if samePath(event.Name, path) && event.Op == fsnotify.Chmod {
				return event
			}
		case err, ok := <-watcher.Errors:
			if ok && err != nil {
				t.Fatal(err)
			}
		case <-timer.C:
			t.Fatalf("raw fsnotify did not observe the private read of %s", filepath.Base(path))
		}
	}
}

func waitForRepositoryGeneration(t *testing.T, events <-chan nodeprotocol.GitEvent, repositoryID string, previous int64) nodeprotocol.GitEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Git event stream closed")
			}
			if event.RepositoryID == repositoryID && event.Generation > previous {
				return event
			}
		case <-timer.C:
			t.Fatalf("repository %s was not invalidated within two seconds", repositoryID)
		}
	}
}

func mustFirstCursor(t *testing.T, service *Service, request nodeprotocol.GitRepositoriesRequest, repositoryID string) nodeprotocol.GitChangesPage {
	t.Helper()
	page, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: repositoryID,
		Area: "untracked", Limit: 1,
	})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("cursor page=%+v err=%v", page, err)
	}
	return page
}

func assertCursorInvalid(t *testing.T, service *Service, request nodeprotocol.GitRepositoriesRequest, repositoryID string, page nodeprotocol.GitChangesPage) {
	t.Helper()
	_, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources, RepositoryID: repositoryID,
		Area: "untracked", Cursor: page.NextCursor, Limit: 1,
	})
	assertGitError(t, err, "invalid_cursor")
}

func repositorySummary(t *testing.T, service *Service, request nodeprotocol.GitRepositoriesRequest, repositoryID string) nodeprotocol.GitRepositorySummary {
	t.Helper()
	listed, err := service.ListRepositories(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.Repositories {
		if item.ID == repositoryID {
			return item
		}
	}
	t.Fatalf("repository %s disappeared", repositoryID)
	return nodeprotocol.GitRepositorySummary{}
}
