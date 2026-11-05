package gitread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestConfiguredCleanProcessAndSmudgeFiltersNeverExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell marker fixture requires Unix")
	}
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, ".gitattributes"),
		"*.txt filter=localbad\n*.lfs filter=lfs\n*.system filter=systembad\n")
	writeFile(t, filepath.Join(repositoryRoot, "local.txt"), "base\n")
	writeFile(t, filepath.Join(repositoryRoot, "asset.lfs"), "base lfs payload\n")
	writeFile(t, filepath.Join(repositoryRoot, "system.system"), "base system payload\n")
	writeFile(t, filepath.Join(repositoryRoot, "global.global"), "base\n")
	moduleSource := filepath.Join(base, "module-source")
	if err := os.MkdirAll(moduleSource, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, moduleSource)
	writeFile(t, filepath.Join(moduleSource, ".gitattributes"), "*.sub filter=subbad\n")
	writeFile(t, filepath.Join(moduleSource, "module.sub"), "base\n")
	gitCommand(t, moduleSource, "add", "--", ".")
	gitCommand(t, moduleSource, "commit", "-m", "module base")
	gitCommand(t, repositoryRoot, "-c", "protocol.file.allow=always", "submodule", "add", "--", moduleSource, "vendor/module")
	gitCommand(t, repositoryRoot, "add", "--", ".")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")

	marker := filepath.Join(base, "filter-ran")
	hook := filepath.Join(base, "malicious-filter")
	writeFile(t, hook, "#!/bin/sh\nprintf ran >> "+strconv.Quote(marker)+"\nexit 0\n")
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clean", "process", "smudge"} {
		gitCommand(t, repositoryRoot, "config", "filter.localbad."+key, hook)
		gitCommand(t, repositoryRoot, "config", "filter.lfs."+key, hook)
	}
	gitCommand(t, repositoryRoot, "config", "filter.localbad.required", "true")
	gitCommand(t, repositoryRoot, "config", "filter.lfs.required", "true")
	checkedOutModule := filepath.Join(repositoryRoot, "vendor", "module")
	for _, key := range []string{"clean", "process", "smudge"} {
		gitCommand(t, checkedOutModule, "config", "filter.subbad."+key, hook)
	}
	gitCommand(t, checkedOutModule, "config", "filter.subbad.required", "true")

	globalAttributes := filepath.Join(base, "global-attributes")
	writeFile(t, globalAttributes, "*.global filter=globalbad\n")
	globalConfig := filepath.Join(base, "global-config")
	gitConfigFile(t, globalConfig, "core.attributesFile", globalAttributes)
	for _, key := range []string{"clean", "process", "smudge"} {
		gitConfigFile(t, globalConfig, "filter.globalbad."+key, hook)
	}
	gitConfigFile(t, globalConfig, "filter.globalbad.required", "true")
	systemConfig := filepath.Join(base, "system-config")
	for _, key := range []string{"clean", "process", "smudge"} {
		gitConfigFile(t, systemConfig, "filter.systembad."+key, hook)
	}
	gitConfigFile(t, systemConfig, "filter.systembad.required", "true")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", systemConfig)

	writeFile(t, filepath.Join(repositoryRoot, "local.txt"), "changed locally\n")
	writeFile(t, filepath.Join(repositoryRoot, "asset.lfs"), "changed lfs payload\n")
	writeFile(t, filepath.Join(repositoryRoot, "system.system"), "changed system payload\n")
	writeFile(t, filepath.Join(repositoryRoot, "global.global"), "changed globally\n")
	writeFile(t, filepath.Join(checkedOutModule, "module.sub"), "changed in submodule\n")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "filter-resource", Name: "Code", Path: repositoryRoot}}
	repositoryList, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "filter-project", Resources: resources,
	})
	if err != nil || len(repositoryList.Repositories) != 1 || repositoryList.Repositories[0].State == "unavailable" {
		t.Fatalf("repositories=%+v err=%v", repositoryList, err)
	}
	page, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "filter-project", Resources: resources, RepositoryID: repositoryList.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	if err != nil || len(page.Items) < 2 {
		t.Fatalf("changes=%+v err=%v", page, err)
	}
	for _, change := range page.Items {
		if _, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
			ProjectID: "filter-project", Resources: resources, RepositoryID: repositoryList.Repositories[0].ID,
			ChangeID: change.ID, Context: 3,
		}); err != nil {
			t.Fatalf("diff %s (%s): %v", change.Path.Display, change.ID, err)
		}
	}
	if _, err := service.CaptureWorkspaceSnapshot(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "filter-project", Resources: resources,
	}, repositoryList.Repositories[0].ID, "end"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("configured Git filter executed, marker stat error=%v", err)
	}
}

func TestRepositoryIDChangesAfterSamePathRepositoryRecreation(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "file.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "changed\n")
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "identity-resource", Name: "Code", Path: repositoryRoot}}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "identity-project", Resources: resources}
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	first, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(first.Repositories) != 1 {
		t.Fatalf("first repositories=%+v err=%v", first, err)
	}
	changes, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: resources, RepositoryID: first.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	if err != nil || len(changes.Items) != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}

	removed := filepath.Join(base, "removed-repository")
	if err := os.Rename(repositoryRoot, removed); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	_, err = service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: request.ProjectID, Resources: resources, RepositoryID: first.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	assertGitErrorOneOf(t, err, "snapshot_expired", "repository_not_found")
	_, err = service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: request.ProjectID, Resources: resources, RepositoryID: first.Repositories[0].ID,
		ChangeID: changes.Items[0].ID, Context: 3,
	})
	assertGitErrorOneOf(t, err, "snapshot_expired", "repository_not_found", "change_not_found")
	second, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: request.ProjectID, Resources: resources, Refresh: true,
	})
	if err != nil || len(second.Repositories) != 1 {
		t.Fatalf("second repositories=%+v err=%v", second, err)
	}
	if second.Repositories[0].ID == first.Repositories[0].ID {
		t.Fatalf("recreated repository reused stable ID %q", first.Repositories[0].ID)
	}
	if _, err := service.ResolveRepositoryRoot(t.Context(), request, first.Repositories[0].ID); err == nil {
		t.Fatal("old repository ID resolved after same-path recreation")
	}
}

func TestRepositoryIdentityRejectsSymlinkedReplacementAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink ancestor fixture requires Unix permissions")
	}
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	repositoryRoot := filepath.Join(parent, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "ancestor-resource", Name: "Code", Path: repositoryRoot}}
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "ancestor-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	movedParent := filepath.Join(base, "moved-parent")
	if err := os.Rename(parent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(movedParent, parent); err != nil {
		t.Fatal(err)
	}
	_, err = service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "ancestor-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	assertGitErrorOneOf(t, err, "path_out_of_scope", "snapshot_expired", "repository_not_found")
}

func TestChangeSnapshotsReuseIDsAndSurviveMoreThanThirtyThreeQueries(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	for index := 0; index < 40; index++ {
		writeFile(t, filepath.Join(repositoryRoot, fmt.Sprintf("dir-%02d", index), "a.txt"), "a\n")
		writeFile(t, filepath.Join(repositoryRoot, fmt.Sprintf("dir-%02d", index), "b.txt"), "b\n")
	}
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "cache-resource", Name: "Code", Path: repositoryRoot}}
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "cache-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	directories, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "cache-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "untracked", Limit: 100,
	})
	if err != nil || len(directories.Items) != 40 {
		t.Fatalf("directories=%+v err=%v", directories, err)
	}
	var firstRequest nodeprotocol.GitChangesRequest
	var firstChangeID, firstCursor string
	for index, directory := range directories.Items {
		request := nodeprotocol.GitChangesRequest{
			ProjectID: "cache-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
			Area: "untracked", Prefix: directory.Path.Token, ExpandUntracked: true, Limit: 1,
		}
		page, pageErr := service.ListChanges(t.Context(), request)
		if pageErr != nil || len(page.Items) != 1 || page.NextCursor == "" {
			t.Fatalf("expanded page %d=%+v err=%v", index, page, pageErr)
		}
		if index == 0 {
			firstRequest = request
			firstChangeID = page.Items[0].ID
			firstCursor = page.NextCursor
		}
	}
	reused, err := service.ListChanges(t.Context(), firstRequest)
	if err != nil || len(reused.Items) != 1 || reused.Items[0].ID != firstChangeID || reused.NextCursor != firstCursor {
		t.Fatalf("cached snapshot was rebuilt: page=%+v err=%v", reused, err)
	}
	firstRequest.Cursor = firstCursor
	next, err := service.ListChanges(t.Context(), firstRequest)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == firstChangeID {
		t.Fatalf("retained cursor failed after 40 snapshots: page=%+v err=%v", next, err)
	}
}

func TestRefreshDoesNotJoinPreBarrierRepositoryFlight(t *testing.T) {
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	projectID := "refresh-barrier-project"
	oldKey := "repositories:" + projectID + ":" + hashResources(nil) + ":0"
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = service.singleflight(context.Background(), oldKey, func() (any, error) {
			close(started)
			<-release
			return "old", nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := service.Refresh(ctx, nodeprotocol.GitRefreshRequest{ProjectID: projectID})
	if err != nil || !result.Refreshed {
		close(release)
		<-finished
		t.Fatalf("refresh joined old flight: result=%+v err=%v", result, err)
	}
	close(release)
	<-finished
}

func TestChangeSnapshotEvictionClearsIndexesAndNotifiesSubscribedProject(t *testing.T) {
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	repo := &repository{id: "repository", summary: nodeprotocol.GitRepositorySummary{Generation: 7}}
	project := &projectSnapshot{
		projectID: "eviction-project", resourcesHash: "resources", snapshotID: "project-snapshot",
		repositories: map[string]*repository{"repository": repo}, ordered: []string{"repository"},
	}
	events := make(chan nodeprotocol.GitEvent, 8)
	service.mu.Lock()
	service.installProjectLocked(project)
	subscriber := &eventSubscriber{channel: events}
	service.subscribers[project.projectID] = map[*eventSubscriber]struct{}{subscriber: {}}
	var oldestSnapshotID, oldestChangeID, oldestPathToken string
	for index := 0; index < maxChangeSnapshots+1; index++ {
		snapshot := service.newChangeSnapshotLocked(project, repo, fmt.Sprintf("key-%d", index),
			"unstaged", "", false, []internalChange{{
				path:   fmt.Sprintf("file-%d.txt", index),
				public: nodeprotocol.GitChange{Area: "unstaged", DisplayPath: fmt.Sprintf("file-%d.txt", index)},
			}}, nil, false)
		if index == 0 {
			oldestSnapshotID = snapshot.id
			oldestChangeID = snapshot.items[0].public.ID
			oldestPathToken = snapshot.items[0].public.Path.Token
		}
	}
	_, snapshotExists := service.changes[oldestSnapshotID]
	_, changeExists := service.changeIndex[oldestChangeID]
	_, pathExists := service.pathIndex[oldestPathToken]
	cacheCount := service.changeLRU.Len()
	cacheBytes := service.changeBytes
	service.mu.Unlock()
	if snapshotExists || changeExists || pathExists {
		t.Fatalf("eviction left stale indexes: snapshot=%t change=%t path=%t", snapshotExists, changeExists, pathExists)
	}
	if cacheCount > maxChangeSnapshots || cacheBytes > maxChangeSnapshotBytes {
		t.Fatalf("cache bounds count=%d bytes=%d", cacheCount, cacheBytes)
	}
	select {
	case event := <-events:
		if event.RepositoryID != repo.id || event.Reason != "change_snapshot_evicted" {
			t.Fatalf("eviction event=%+v", event)
		}
	default:
		t.Fatal("subscribed project snapshot was silently evicted")
	}
}

func TestProjectAndFlightStateRemainBounded(t *testing.T) {
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	service.mu.Lock()
	for index := 0; index < maxProjectSnapshots+8; index++ {
		projectID := fmt.Sprintf("project-%d", index)
		repositoryID := fmt.Sprintf("repository-%d", index)
		service.installProjectLocked(&projectSnapshot{
			projectID: projectID, snapshotID: "snapshot-" + projectID, resourcesHash: "resources",
			repositories: map[string]*repository{repositoryID: {
				id: repositoryID, summary: nodeprotocol.GitRepositorySummary{ID: repositoryID},
			}},
			ordered: []string{repositoryID},
		})
	}
	projectCount, projectBytes := len(service.projects), service.projectBytes
	for index := 0; index < maxFlights; index++ {
		service.flights[fmt.Sprintf("occupied-%d", index)] = &flight{done: make(chan struct{})}
	}
	service.mu.Unlock()
	if projectCount > maxProjectSnapshots || projectBytes > maxProjectSnapshotBytes {
		t.Fatalf("project cache exceeded bounds: count=%d bytes=%d", projectCount, projectBytes)
	}
	_, err := service.singleflight(t.Context(), "overflow", func() (any, error) {
		t.Fatal("overflow flight executed")
		return nil, nil
	})
	var readError *nodeprotocol.GitReadError
	if !errors.As(err, &readError) || readError.Code != "git_busy" {
		t.Fatalf("flight overflow error=%v", err)
	}
	service.mu.Lock()
	service.flights = make(map[string]*flight)
	service.mu.Unlock()
}

func TestSubscriberOverflowCoalescesNewestEventPerRepository(t *testing.T) {
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	subscriber := &eventSubscriber{channel: make(chan nodeprotocol.GitEvent, maxRepositories)}
	service.mu.Lock()
	service.subscribers["project"] = map[*eventSubscriber]struct{}{subscriber: {}}
	for index := 0; index < maxRepositories; index++ {
		service.emitLocked("project", nodeprotocol.GitEvent{
			RepositoryID: fmt.Sprintf("repository-%d", index), Generation: 1, Reason: "initial",
		})
	}
	service.emitLocked("project", nodeprotocol.GitEvent{
		RepositoryID: "repository-0", Generation: 9, Reason: "newest",
	})
	service.mu.Unlock()
	seen := make(map[string]nodeprotocol.GitEvent)
	for len(subscriber.channel) > 0 {
		event := <-subscriber.channel
		seen[event.RepositoryID] = event
	}
	if len(seen) != maxRepositories || seen["repository-0"].Generation != 9 ||
		seen["repository-0"].Reason != "newest" {
		t.Fatalf("coalesced events=%d repository-0=%+v", len(seen), seen["repository-0"])
	}
}

func TestStatusUsesAreaSpecificModesForPartiallyStagedTypeChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode fixture requires Unix")
	}
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	path := filepath.Join(repositoryRoot, "kind.txt")
	writeFile(t, path, "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "kind.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", path); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repositoryRoot, "add", "--", "kind.txt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "worktree file\n")

	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "mode-resource", Name: "Code", Path: repositoryRoot}}
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "mode-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	staged, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "mode-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "staged", Limit: 20,
	})
	if err != nil || len(staged.Items) != 1 || staged.Items[0].Kind != "symlink" {
		t.Fatalf("staged type=%+v err=%v", staged, err)
	}
	unstaged, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "mode-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	if err != nil || len(unstaged.Items) != 1 || unstaged.Items[0].Kind != "file" {
		t.Fatalf("unstaged type=%+v err=%v", unstaged, err)
	}
}

func TestDiffByteConfirmationCannotBeBypassedByOneLongLine(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "long.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "long.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	writeFile(t, filepath.Join(repositoryRoot, "long.txt"), strings.Repeat("x", diffConfirmationLimit+64<<10))
	gitCommand(t, repositoryRoot, "add", "--", "long.txt")
	largeImage := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		bytes.Repeat([]byte{'p'}, diffConfirmationLimit)...)
	writeBytes(t, filepath.Join(repositoryRoot, "large.png"), largeImage)
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "large-resource", Name: "Code", Path: repositoryRoot}}
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "large-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	page, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "large-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "staged", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("changes=%+v err=%v", page, err)
	}
	confirmation, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "large-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3,
	})
	if err != nil || !confirmation.ConfirmRequired || confirmation.Patch != "" || confirmation.Images != nil {
		t.Fatalf("confirmation=%+v err=%v", confirmation, err)
	}
	allowed, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "large-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3, AllowLarge: true,
	})
	if err != nil || allowed.ConfirmRequired || len(allowed.Patch) <= diffConfirmationLimit {
		t.Fatalf("allowed diff bytes=%d result=%+v err=%v", len(allowed.Patch), allowed, err)
	}
	untracked, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "large-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "untracked", Limit: 20,
	})
	if err != nil || len(untracked.Items) != 1 {
		t.Fatalf("untracked image=%+v err=%v", untracked, err)
	}
	imageConfirmation, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "large-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		ChangeID: untracked.Items[0].ID, Context: 3,
	})
	if err != nil || !imageConfirmation.ConfirmRequired || imageConfirmation.Images != nil || imageConfirmation.Patch != "" {
		t.Fatalf("large image was encoded before confirmation: result=%+v err=%v", imageConfirmation, err)
	}
}

func TestLargeConflictWithoutNumStatsStillRequiresConfirmation(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	path := filepath.Join(repositoryRoot, "conflict.txt")
	writeFile(t, path, strings.Repeat("base\n", 600))
	gitCommand(t, repositoryRoot, "add", "--", "conflict.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	gitCommand(t, repositoryRoot, "checkout", "-b", "side")
	writeFile(t, path, strings.Repeat("side\n", 600))
	gitCommand(t, repositoryRoot, "add", "--", "conflict.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "side")
	gitCommand(t, repositoryRoot, "checkout", "main")
	writeFile(t, path, strings.Repeat("main\n", 600))
	gitCommand(t, repositoryRoot, "add", "--", "conflict.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "main")
	gitCommandMustFail(t, repositoryRoot, "merge", "side")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "conflict-resource", Name: "Code", Path: repositoryRoot}}
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "conflict-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	page, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "conflict-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "conflict", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("conflicts=%+v err=%v", page, err)
	}
	result, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "conflict-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3,
	})
	if err != nil || !result.ConfirmRequired || result.Patch != "" {
		t.Fatalf("large conflict result=%+v err=%v", result, err)
	}
}

func TestPatchAuthorizationRejectsEveryCrossScopeHeader(t *testing.T) {
	repository := &repository{authPrefixes: []string{"scope"}}
	change := internalChange{path: "scope/new.txt", oldPath: "scope/old.txt"}
	allowed := []byte("diff --git a/scope/old.txt b/scope/new.txt\nsimilarity index 100%\n" +
		"rename from scope/old.txt\nrename to scope/new.txt\n")
	if err := validatePatchAuthorization(allowed, repository, change); err != nil {
		t.Fatalf("authorized rename rejected: %v", err)
	}
	attacks := [][]byte{
		[]byte("diff --git a/outside.txt b/scope/new.txt\n"),
		[]byte("diff --git a/scope/old.txt b/scope/new.txt\nrename from outside.txt\nrename to scope/new.txt\n"),
		[]byte("diff --git a/scope/old.txt b/scope/new.txt\ncopy from scope/old.txt\ncopy to outside.txt\n"),
		[]byte("diff --cc outside.txt\n"),
		[]byte("diff --git a/scope/old.txt b/scope/new.txt\n--- a/outside.txt\n+++ b/scope/new.txt\n"),
	}
	for index, patch := range attacks {
		if err := validatePatchAuthorization(patch, repository, change); err == nil {
			t.Fatalf("cross-scope patch %d was accepted: %q", index, patch)
		}
	}
}

func TestUntrackedDiffUsesOneBoundedReadAndDoesNotInvokeGitAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell marker fixture requires Unix")
	}
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "untracked.txt"), "one read\n")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: repositoryRoot}}
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "single-read-project", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", list, err)
	}
	page, err := service.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "single-read-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		Area: "untracked", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("untracked=%+v err=%v", page, err)
	}
	marker := filepath.Join(base, "git-invoked")
	fakeGit := filepath.Join(base, "git")
	writeFile(t, fakeGit, "#!/bin/sh\nprintf invoked > "+strconv.Quote(marker)+"\nexit 99\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(fakeGit)
	if err != nil {
		t.Fatal(err)
	}
	service.gitPath, service.gitInfo, service.gitErr = fakeGit, info, nil
	result, err := service.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "single-read-project", Resources: resources, RepositoryID: list.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3,
	})
	if err != nil || !strings.Contains(result.Patch, "+one read") {
		t.Fatalf("untracked diff=%+v err=%v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untracked diff invoked Git for a second path read: %v", err)
	}
}

func TestStatusParserBoundsHundredThousandRecords(t *testing.T) {
	payload := hundredThousandStatusRecords()
	repository := &repository{authPrefixes: []string{""}}
	var parsed parsedStatus
	allocations := testing.AllocsPerRun(1, func() {
		parsed = parseStatusOutput(payload, repository)
	})
	if parsed.counts.Staged != 100_000 || len(parsed.items) != maxSnapshotChanges || !parsed.truncated {
		t.Fatalf("parsed counts=%+v items=%d truncated=%t", parsed.counts, len(parsed.items), parsed.truncated)
	}
	if allocations > 350_000 {
		t.Fatalf("status parser allocations %.0f exceed bounded threshold", allocations)
	}
}

func BenchmarkParseHundredThousandStatusRecords(b *testing.B) {
	payload := hundredThousandStatusRecords()
	repository := &repository{authPrefixes: []string{""}}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result := parseStatusOutput(payload, repository)
		if result.counts.Staged != 100_000 {
			b.Fatal(result.counts)
		}
	}
}

func hundredThousandStatusRecords() []byte {
	var payload bytes.Buffer
	payload.Grow(8 << 20)
	for index := 0; index < 100_000; index++ {
		payload.WriteString("1 M. N... 100644 100644 100644 abcdef abcdef file-")
		payload.Write(strconv.AppendInt(nil, int64(index), 10))
		payload.WriteByte(0)
	}
	return payload.Bytes()
}

func gitConfigFile(t *testing.T, path, key, value string) {
	t.Helper()
	command := exec.Command("git", "config", "--file", path, key, value)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config --file %s %s: %v: %s", path, key, err, output)
	}
}

func assertGitErrorOneOf(t *testing.T, err error, codes ...string) {
	t.Helper()
	var gitErr *nodeprotocol.GitReadError
	if !errors.As(err, &gitErr) {
		t.Fatalf("error=%v, want one of %v", err, codes)
	}
	for _, code := range codes {
		if gitErr.Code == code {
			return
		}
	}
	t.Fatalf("error=%v, want one of %v", err, codes)
}
