package gitread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestProjectRepositoriesScopesNestedDedupAndChanges(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "scope")
	nested := filepath.Join(child, "nested")
	second := filepath.Join(root, "second")
	for _, directory := range []string{child, nested, second} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	initRepository(t, parent)
	writeFile(t, filepath.Join(parent, "outside.txt"), "outside\n")
	writeFile(t, filepath.Join(child, "partial.txt"), "base\n")
	writeFile(t, filepath.Join(child, "old.txt"), "rename me\n")
	writeFile(t, filepath.Join(child, "binary.dat"), "plain\n")
	writeFile(t, filepath.Join(child, "conflict.txt"), "base\n")
	writeBytes(t, filepath.Join(child, "image.png"), append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("before\x00")...))
	gitCommand(t, parent, "add", "--", ".")
	gitCommand(t, parent, "commit", "-m", "initial")
	gitCommand(t, parent, "checkout", "-b", "conflict-side")
	writeFile(t, filepath.Join(child, "conflict.txt"), "side\n")
	gitCommand(t, parent, "add", "--", "scope/conflict.txt")
	gitCommand(t, parent, "commit", "-m", "side conflict")
	gitCommand(t, parent, "checkout", "main")
	writeFile(t, filepath.Join(child, "conflict.txt"), "main\n")
	gitCommand(t, parent, "add", "--", "scope/conflict.txt")
	gitCommand(t, parent, "commit", "-m", "main conflict")
	gitCommandMustFail(t, parent, "merge", "conflict-side")

	initRepository(t, nested)
	writeFile(t, filepath.Join(nested, "nested.txt"), "base\n")
	gitCommand(t, nested, "add", "--", "nested.txt")
	gitCommand(t, nested, "commit", "-m", "nested")
	writeFile(t, filepath.Join(nested, "nested.txt"), "changed\n")

	initRepository(t, second)
	writeFile(t, filepath.Join(second, "second.txt"), "base\n")
	gitCommand(t, second, "add", "--", "second.txt")
	gitCommand(t, second, "commit", "-m", "second")
	writeFile(t, filepath.Join(second, "second.txt"), "changed\n")

	// One path is staged and then modified again, so it must independently
	// appear in both the staged and unstaged areas.
	writeFile(t, filepath.Join(child, "partial.txt"), "staged\n")
	gitCommand(t, parent, "add", "--", "scope/partial.txt")
	writeFile(t, filepath.Join(child, "partial.txt"), "staged\nworktree\n")
	gitCommand(t, parent, "mv", "--", "scope/old.txt", "scope/new.txt")
	writeBytes(t, filepath.Join(child, "binary.dat"), []byte{'a', 0, 'b', '\n'})
	writeBytes(t, filepath.Join(child, "image.png"), append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("after\x00")...))
	writeFile(t, filepath.Join(parent, "outside.txt"), "must stay hidden\n")
	writeFile(t, filepath.Join(child, "newdir", "a.txt"), "a\n")
	writeFile(t, filepath.Join(child, "newdir", "b.txt"), "b\n")
	writeFile(t, filepath.Join(child, "large.txt"), strings.Repeat("large line\n", 600))
	outsideSecret := filepath.Join(root, "outside-secret.txt")
	writeFile(t, outsideSecret, "must never be followed\n")
	if err := os.Symlink(outsideSecret, filepath.Join(child, "outside-link")); err != nil {
		t.Fatal(err)
	}

	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{
		{ResourceID: "res_scope", Name: "Scoped", Path: child},
		{ResourceID: "res_nested", Name: "Nested", Path: nested},
		{ResourceID: "res_second", Name: "Second", Path: second},
	}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project_1", Resources: resources}
	repositories, err := service.ListRepositories(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories.Repositories) != 3 {
		t.Fatalf("repositories=%+v, want parent+nested+second", repositories.Repositories)
	}
	var parentRepository, nestedRepository nodeprotocol.GitRepositorySummary
	for _, repository := range repositories.Repositories {
		switch {
		case repository.Scope == "resource_prefix":
			parentRepository = repository
		case contains(repository.ResourceIDs, "res_nested") && contains(repository.ResourceIDs, "res_scope"):
			nestedRepository = repository
		}
		if filepath.IsAbs(repository.RootDisplay) || strings.Contains(repository.RootDisplay, root) {
			t.Fatalf("absolute path leaked in summary: %+v", repository)
		}
	}
	if parentRepository.ID == "" || parentRepository.Scope != "resource_prefix" {
		t.Fatalf("upper repository scope=%+v", parentRepository)
	}
	if parentRepository.RootDisplay != "Scoped" {
		t.Fatalf("resource-prefix display must remain data-only: %+v", parentRepository)
	}
	if parentRepository.Operation.Kind != "merge" || parentRepository.Counts.Conflict == 0 {
		t.Fatalf("merge/conflict summary=%+v", parentRepository)
	}
	if nestedRepository.ID == "" || len(nestedRepository.ResourceIDs) != 2 {
		t.Fatalf("nested repository was not deduplicated: %+v", nestedRepository)
	}
	if repositories.SnapshotID == "" {
		t.Fatal("repository snapshot_id is required")
	}
	if repositories.Warnings == nil {
		t.Fatal("repository-list warnings must encode as an empty array, not null")
	}
	for _, repository := range repositories.Repositories {
		if repository.ResourceIDs == nil || repository.Warnings == nil {
			t.Fatalf("repository arrays must be non-nil: %+v", repository)
		}
	}
	serializedRepositories, err := json.Marshal(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serializedRepositories, []byte(`"warnings":null`)) ||
		bytes.Contains(serializedRepositories, []byte(`"resource_ids":null`)) {
		t.Fatalf("repository response violated required-array contract: %s", serializedRepositories)
	}

	staged := listChanges(t, service, resources, parentRepository.ID, "staged", "", false)
	unstaged := listChanges(t, service, resources, parentRepository.ID, "unstaged", "", false)
	all := listChanges(t, service, resources, parentRepository.ID, "", "", false)
	if all.Warnings == nil {
		t.Fatal("changes-page warnings must encode as an empty array, not null")
	}
	if all.SnapshotID != repositories.SnapshotID || all.RepositoryGeneration != parentRepository.Generation ||
		findChange(all.Items, "scope/partial.txt") == nil {
		t.Fatalf("combined changes page=%+v", all)
	}
	if findChange(staged.Items, "scope/partial.txt") == nil || findChange(unstaged.Items, "scope/partial.txt") == nil {
		t.Fatalf("partial staged path missing: staged=%+v unstaged=%+v", staged.Items, unstaged.Items)
	}
	rename := findChange(staged.Items, "scope/new.txt")
	if rename == nil || rename.Status != "renamed" || rename.Rename == nil || rename.Rename.From != "scope/old.txt" {
		t.Fatalf("rename=%+v", rename)
	}
	renameDiff, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: rename.ID, Context: 3,
	})
	if err != nil || renameDiff.Comparison != "head_to_index" ||
		(!strings.Contains(renameDiff.Patch, "rename from") && !strings.Contains(renameDiff.Patch, "similarity index")) {
		t.Fatalf("rename diff=%+v err=%v", renameDiff, err)
	}
	if findChange(unstaged.Items, "outside.txt") != nil {
		t.Fatalf("upper-scope repository leaked outside change: %+v", unstaged.Items)
	}
	binary := findChange(unstaged.Items, "scope/binary.dat")
	if binary == nil || !binary.Binary {
		t.Fatalf("binary change=%+v", binary)
	}
	binaryDiff, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: binary.ID, Context: 3,
	})
	if err != nil || binaryDiff.Kind != "binary" {
		t.Fatalf("binary diff=%+v err=%v", binaryDiff, err)
	}
	image := findChange(unstaged.Items, "scope/image.png")
	if image == nil {
		t.Fatalf("image change missing: %+v", unstaged.Items)
	}
	imageDiff, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: image.ID, Context: 3,
	})
	if err != nil || imageDiff.Kind != "image" || imageDiff.Images == nil ||
		imageDiff.Images.Before == nil || imageDiff.Images.After == nil ||
		!strings.HasPrefix(imageDiff.Images.After.DataURL, "data:image/png;base64,") {
		t.Fatalf("image diff=%+v err=%v", imageDiff, err)
	}
	conflicts := listChanges(t, service, resources, parentRepository.ID, "conflict", "", false)
	conflict := findChange(conflicts.Items, "scope/conflict.txt")
	if conflict == nil || conflict.ConflictCode == "" || conflict.Conflict == nil {
		t.Fatalf("conflict=%+v", conflict)
	}
	runSnapshot, err := service.CaptureWorkspaceSnapshot(t.Context(), request, parentRepository.ID, "end")
	if err != nil {
		t.Fatal(err)
	}
	conflictSummarized := false
	for _, area := range runSnapshot.Areas {
		if area.Area == "conflict" && contains(area.Paths, "scope/conflict.txt") {
			conflictSummarized = true
		}
		if area.Area != "conflict" && strings.Contains(area.Patch, "scope/conflict.txt") {
			t.Fatalf("conflict content leaked into %s patch", area.Area)
		}
	}
	if !conflictSummarized {
		t.Fatalf("conflict missing from Run snapshot summary: %+v", runSnapshot.Areas)
	}

	untracked := listChanges(t, service, resources, parentRepository.ID, "untracked", "", false)
	symlink := findChange(untracked.Items, "scope/outside-link")
	if symlink == nil || symlink.Kind != "symlink" {
		t.Fatalf("untracked symlink=%+v", symlink)
	}
	symlinkDiff, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: symlink.ID, Context: 3,
	})
	if err != nil || symlinkDiff.Patch != "" ||
		symlinkDiff.MessageCode != nodeprotocol.GitDiffMessageUntrackedSymlinkSummary ||
		!isASCIIText(symlinkDiff.Message) || strings.Contains(symlinkDiff.Message, "must never be followed") {
		t.Fatalf("symlink diff=%+v err=%v", symlinkDiff, err)
	}
	directory := findChange(untracked.Items, "scope/newdir")
	if directory == nil || directory.Kind != "directory" {
		t.Fatalf("normal untracked directory=%+v in %+v", directory, untracked.Items)
	}
	if directory.Path.Token == "" || directory.Path.Display != directory.DisplayPath {
		t.Fatalf("opaque directory path=%+v", directory.Path)
	}
	directoryDiff, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: directory.ID, Context: 3,
	})
	if err != nil || directoryDiff.MessageCode != nodeprotocol.GitDiffMessageUntrackedDirectoryExpandRequired ||
		!isASCIIText(directoryDiff.Message) {
		t.Fatalf("directory diff=%+v err=%v", directoryDiff, err)
	}
	expanded := listChanges(t, service, resources, parentRepository.ID, "untracked", directory.Path.Token, true)
	if findChange(expanded.Items, "scope/newdir/a.txt") == nil || findChange(expanded.Items, "scope/newdir/b.txt") == nil {
		t.Fatalf("expanded untracked=%+v", expanded.Items)
	}
	firstPage, err := service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		Area: "untracked", Prefix: directory.Path.Token, ExpandUntracked: true, Limit: 1,
	})
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first cursor page=%+v err=%v", firstPage, err)
	}
	secondPage, err := service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		Area: "untracked", Prefix: directory.Path.Token, ExpandUntracked: true,
		Cursor: firstPage.NextCursor, Limit: 1,
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second cursor page=%+v err=%v", secondPage, err)
	}
	_, err = service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		Area: "untracked", Prefix: directory.Path.Token, ExpandUntracked: true,
		Cursor: "cursor_forged", Limit: 1,
	})
	assertGitError(t, err, "invalid_cursor")
	large := findChange(untracked.Items, "scope/large.txt")
	if large == nil {
		t.Fatalf("large untracked change missing: %+v", untracked.Items)
	}
	confirmation, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: large.ID, Context: 3,
	})
	if err != nil || !confirmation.ConfirmRequired || confirmation.Patch != "" ||
		confirmation.MessageCode != nodeprotocol.GitDiffMessageLineConfirmationRequired ||
		!isASCIIText(confirmation.Message) {
		t.Fatalf("large confirmation=%+v err=%v", confirmation, err)
	}
	allowed, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: large.ID, Context: 3, AllowLarge: true,
	})
	if err != nil || allowed.ConfirmRequired || allowed.Patch == "" || strings.Contains(allowed.Patch, root) {
		t.Fatalf("allowed large diff=%+v err=%v", allowed, err)
	}

	_, err = service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: "chg_forged", Context: 3,
	})
	assertGitError(t, err, "change_not_found")
	_, err = service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		Area: "untracked", Prefix: "path_forged", ExpandUntracked: true, Limit: 200,
	})
	assertGitError(t, err, "path_out_of_scope")

	oldChangeID := binary.ID
	events, unsubscribe, err := service.Subscribe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	refresh, err := service.Refresh(context.Background(), nodeprotocol.GitRefreshRequest{
		ProjectID: "project_1", Resources: resources,
	})
	if err != nil || !refresh.Refreshed {
		t.Fatalf("refresh=%+v err=%v", refresh, err)
	}
	eventDeadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.RepositoryID == parentRepository.ID {
				if event.Generation <= parentRepository.Generation || event.Reason != "refresh" {
					t.Fatalf("refresh event=%+v", event)
				}
				goto eventReceived
			}
		case <-eventDeadline:
			t.Fatal("refresh event not received")
		}
	}
eventReceived:
	_, err = service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: parentRepository.ID,
		ChangeID: oldChangeID, Context: 3,
	})
	assertGitError(t, err, "change_not_found")
}

func TestRepositoryExecutionScopeKeepsDisjointResourceRootsExact(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	first := filepath.Join(repositoryRoot, "first")
	second := filepath.Join(repositoryRoot, "second")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	initRepository(t, repositoryRoot)
	resources := []nodeprotocol.GitResourceRequest{
		{ResourceID: "resource-first", Name: "First", Path: first},
		{ResourceID: "resource-second", Name: "Second", Path: second},
	}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project-disjoint-scope", Resources: resources}
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	list, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(list.Repositories) != 1 || list.Repositories[0].Scope != "resource_prefix" {
		t.Fatalf("repositories=%+v err=%v", list.Repositories, err)
	}
	scope, err := service.ResolveRepositoryExecutionScope(t.Context(), request, list.Repositories[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	firstCanonical, _ := filepath.EvalSymlinks(first)
	secondCanonical, _ := filepath.EvalSymlinks(second)
	repositoryCanonical, _ := filepath.EvalSymlinks(repositoryRoot)
	if scope.Full || len(scope.WritableRoots) != 2 ||
		!samePath(scope.WritableRoots[0], firstCanonical) ||
		!samePath(scope.WritableRoots[1], secondCanonical) {
		t.Fatalf("disjoint execution scope=%+v", scope)
	}
	for _, root := range scope.WritableRoots {
		if samePath(root, repositoryCanonical) {
			t.Fatalf("partial roots widened to repository root: %+v", scope)
		}
	}
	if err := service.ValidateRepositoryExecutionScope(t.Context(), request, list.Repositories[0].ID, scope); err != nil {
		t.Fatalf("exact disjoint scope failed validation: %v", err)
	}
}

func TestProjectRequestUsesRunResourceAdmissionLimit(t *testing.T) {
	resources := make([]nodeprotocol.GitResourceRequest, nodeprotocol.MaxGitResourcesPerRun+1)
	for index := range resources {
		resources[index] = nodeprotocol.GitResourceRequest{
			ResourceID: fmt.Sprintf("resource-%d", index), Name: "Code", Path: t.TempDir(),
		}
	}
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	_, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-resource-limit", Resources: resources,
	})
	var gitErr *nodeprotocol.GitReadError
	if !errors.As(err, &gitErr) || gitErr.Code != "invalid_argument" {
		t.Fatalf("oversized resources error=%v", err)
	}
}

func TestRepositoryIdentityIsStableButChangeTokensAreInstanceScoped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "stable")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, root)
	writeFile(t, filepath.Join(root, "tracked.txt"), "before\n")
	gitCommand(t, root, "add", "--", "tracked.txt")
	gitCommand(t, root, "commit", "-m", "base")
	writeFile(t, filepath.Join(root, "tracked.txt"), "after\n")
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "resource-stable", Name: "Stable", Path: root}}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project-stable", Resources: resources}

	first := New()
	second := New()
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })
	firstList, err := first.ListRepositories(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	secondList, err := second.ListRepositories(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstList.Repositories) != 1 || len(secondList.Repositories) != 1 ||
		firstList.Repositories[0].ID != secondList.Repositories[0].ID ||
		firstList.Repositories[0].CommonID != secondList.Repositories[0].CommonID {
		t.Fatalf("repository identity changed across services: first=%+v second=%+v", firstList.Repositories, secondList.Repositories)
	}
	page, err := first.ListChanges(t.Context(), nodeprotocol.GitChangesRequest{
		ProjectID: "project-stable", Resources: resources, RepositoryID: firstList.Repositories[0].ID,
		Area: "unstaged", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("first service changes=%+v err=%v", page, err)
	}
	_, err = second.Diff(t.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: "project-stable", Resources: resources, RepositoryID: secondList.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3,
	})
	assertGitError(t, err, "change_not_found")
}

func TestRepositoryOrderFollowsResourceInputAndRelativeRoots(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "first")
	firstNested := filepath.Join(firstRoot, "nested")
	secondRoot := filepath.Join(base, "second")
	for _, directory := range []string{firstNested, secondRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	initRepository(t, firstRoot)
	initRepository(t, firstNested)
	initRepository(t, secondRoot)
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	list, err := service.ListRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-order",
		Resources: []nodeprotocol.GitResourceRequest{
			{ResourceID: "resource-z", Name: "First", Path: firstRoot},
			{ResourceID: "resource-a", Name: "Second", Path: secondRoot},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repositories) != 3 {
		t.Fatalf("repositories=%+v", list.Repositories)
	}
	want := []string{"First", "First/nested", "Second"}
	for index, expected := range want {
		if list.Repositories[index].RootDisplay != expected {
			t.Fatalf("repository order=%+v, item %d want %q", list.Repositories, index, expected)
		}
	}
}

func TestWatchDebouncesRepositoryEvent(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "watched")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", "file.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.watcher == nil {
		t.Skip("fsnotify is unavailable")
	}
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_watch",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "res_watch", Name: "Watch", Path: repositoryRoot}},
	}
	repositories, err := service.ListRepositories(context.Background(), request)
	if err != nil || len(repositories.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	events, unsubscribe, err := service.Subscribe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "one\n")
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "two\n")
	select {
	case event := <-events:
		if event.RepositoryID != repositories.Repositories[0].ID || event.Generation <= repositories.Repositories[0].Generation || event.Reason == "" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch event not received")
	}
}

func TestSuccessfulWatchRecoveryClearsFallbackWarning(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "watch-recovery")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	service := New()
	t.Cleanup(func() { _ = service.Close() })
	if service.watcher == nil {
		t.Skip("fsnotify is unavailable")
	}
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-watch-recovery",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource-watch-recovery", Name: "Watch", Path: repositoryRoot}},
	}
	listed, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(listed.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", listed, err)
	}
	target := repoTarget{projectID: request.ProjectID, repositoryID: listed.Repositories[0].ID}
	service.markWatchLimited(target)
	service.ensureWatched(request.ProjectID, service.projects[request.ProjectID].repositories[target.repositoryID], time.Now().Add(2*time.Second))

	recovered, err := service.ListRepositories(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range recovered.Repositories[0].Warnings {
		if item.Code == nodeprotocol.GitWarningWatchLimitExceeded {
			t.Fatalf("successful watch retained fallback warning: %+v", recovered.Repositories[0].Warnings)
		}
	}
}

func TestSubmoduleBecomesPeerOnlyWhenExplicitResource(t *testing.T) {
	root := t.TempDir()
	moduleSource := filepath.Join(root, "module-source")
	parent := filepath.Join(root, "parent")
	for _, directory := range []string{moduleSource, parent} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		initRepository(t, directory)
	}
	writeFile(t, filepath.Join(moduleSource, "module.txt"), "module\n")
	gitCommand(t, moduleSource, "add", "--", "module.txt")
	gitCommand(t, moduleSource, "commit", "-m", "module")
	writeFile(t, filepath.Join(parent, "parent.txt"), "parent\n")
	gitCommand(t, parent, "add", "--", "parent.txt")
	gitCommand(t, parent, "commit", "-m", "parent")
	gitCommand(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "--", moduleSource, "vendor/module")
	gitCommand(t, parent, "commit", "-am", "submodule")

	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "res_parent", Name: "Parent", Path: parent}}
	list, err := service.ListRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_submodule", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("implicit submodule list=%+v err=%v", list, err)
	}
	resources = append(resources, nodeprotocol.GitResourceRequest{
		ResourceID: "res_module", Name: "Module", Path: filepath.Join(parent, "vendor", "module"),
	})
	list, err = service.ListRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_submodule", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 2 {
		t.Fatalf("explicit submodule list=%+v err=%v", list, err)
	}
	found := false
	for _, repository := range list.Repositories {
		if contains(repository.ResourceIDs, "res_module") && repository.WorktreeKind == "submodule" {
			found = true
		}
	}
	if !found {
		t.Fatalf("explicit submodule peer missing: %+v", list.Repositories)
	}
}

func TestLinkedWorktreeOutsideResourceIsNotImplicitlyDiscovered(t *testing.T) {
	root := t.TempDir()
	mainRoot := filepath.Join(root, "main")
	linkedRoot := filepath.Join(root, "linked")
	if err := os.MkdirAll(mainRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, mainRoot)
	writeFile(t, filepath.Join(mainRoot, "main.txt"), "main\n")
	gitCommand(t, mainRoot, "add", "--", "main.txt")
	gitCommand(t, mainRoot, "commit", "-m", "main")
	gitCommand(t, mainRoot, "worktree", "add", "-b", "linked-branch", linkedRoot)

	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "res_main", Name: "Main", Path: mainRoot}}
	list, err := service.ListRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_worktree", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 1 || list.Repositories[0].WorktreeKind != "main" {
		t.Fatalf("implicit linked worktree list=%+v err=%v", list, err)
	}
	mainSummary := list.Repositories[0]
	resources = append(resources, nodeprotocol.GitResourceRequest{
		ResourceID: "res_linked", Name: "Linked", Path: linkedRoot,
	})
	list, err = service.ListRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_worktree", Resources: resources,
	})
	if err != nil || len(list.Repositories) != 2 {
		t.Fatalf("explicit linked worktree list=%+v err=%v", list, err)
	}
	foundLinked := false
	for _, repository := range list.Repositories {
		if repository.ID != mainSummary.ID && repository.CommonID == mainSummary.CommonID && repository.WorktreeKind == "linked" {
			foundLinked = true
		}
	}
	if !foundLinked {
		t.Fatalf("linked worktree identity/common ID missing: %+v", list.Repositories)
	}
}

func TestGitReadDisablesExternalDiffTextconvAndFSMonitor(t *testing.T) {
	root := t.TempDir()
	repositoryRoot := filepath.Join(root, "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, repositoryRoot)
	marker := filepath.Join(root, "unsafe-hook-ran")
	hook := filepath.Join(root, "unsafe-hook")
	script := "#!/bin/sh\nprintf ran > \"$GIT_READ_TEST_MARKER\"\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_READ_TEST_MARKER", marker)
	t.Setenv("GIT_EXTERNAL_DIFF", hook)
	writeFile(t, filepath.Join(repositoryRoot, ".gitattributes"), "*.txt diff=unsafe\n")
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "base\n")
	gitCommand(t, repositoryRoot, "add", "--", ".gitattributes", "file.txt")
	gitCommand(t, repositoryRoot, "commit", "-m", "base")
	gitCommand(t, repositoryRoot, "config", "diff.unsafe.textconv", hook)
	gitCommand(t, repositoryRoot, "config", "core.fsmonitor", hook)
	writeFile(t, filepath.Join(repositoryRoot, "file.txt"), "changed\n")

	service := New()
	t.Cleanup(func() { _ = service.Close() })
	resources := []nodeprotocol.GitResourceRequest{{ResourceID: "res_secure", Name: "Secure", Path: repositoryRoot}}
	repositories, err := service.ListRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project_secure", Resources: resources,
	})
	if err != nil || len(repositories.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	page, err := service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_secure", Resources: resources, RepositoryID: repositories.Repositories[0].ID,
		Area: "unstaged", Limit: 200,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("changes=%+v err=%v", page, err)
	}
	if _, err := service.Diff(context.Background(), nodeprotocol.GitDiffRequest{
		ProjectID: "project_secure", Resources: resources, RepositoryID: repositories.Repositories[0].ID,
		ChangeID: page.Items[0].ID, Context: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("external Git executable ran, marker stat err=%v", err)
	}
}

func TestCaptureWorkspaceSnapshotIsScopedBoundedAndMetadataOnlyForUntracked(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	scope := filepath.Join(root, "scope")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepository(t, root)
	writeFile(t, filepath.Join(scope, "staged.txt"), "base\n")
	writeFile(t, filepath.Join(scope, "unstaged.txt"), "base\n")
	writeFile(t, filepath.Join(scope, ".teamva", "tool-results", "result.json"), "base tool result\n")
	writeFile(t, filepath.Join(root, "outside.txt"), "outside\n")
	gitCommand(t, root, "add", "--", ".")
	gitCommand(t, root, "commit", "-m", "base")
	writeFile(t, filepath.Join(scope, "staged.txt"), "staged\n")
	gitCommand(t, root, "add", "--", "scope/staged.txt")
	writeFile(t, filepath.Join(scope, "unstaged.txt"), "unstaged\n")
	writeFile(t, filepath.Join(scope, ".teamva", "tool-results", "result.json"), "tool noise changed\n")
	writeFile(t, filepath.Join(scope, ".teamva", "tool-requests", "request.json"), "tool noise untracked\n")
	writeFile(t, filepath.Join(root, "outside.txt"), "must not appear\n")
	writeFile(t, filepath.Join(scope, "untracked.html"), "<script>must not execute or persist as content</script>\n")

	service := New()
	defer service.Close()
	request := nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-snapshot",
		Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource-scope", Name: "Scope", Path: scope}},
	}
	repositories, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(repositories.Repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	snapshot, err := service.CaptureWorkspaceSnapshot(t.Context(), request, repositories.Repositories[0].ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RepositoryID != repositories.Repositories[0].ID || snapshot.Phase != "start" ||
		snapshot.ContentHash == "" || filepath.IsAbs(snapshot.RootDisplay) {
		t.Fatalf("snapshot metadata=%+v", snapshot)
	}
	var stagedPatch, unstagedPatch string
	for _, area := range snapshot.Areas {
		switch area.Area {
		case "staged":
			stagedPatch = area.Patch
		case "unstaged":
			unstagedPatch = area.Patch
		}
	}
	if !strings.Contains(stagedPatch, "scope/staged.txt") || !strings.Contains(unstagedPatch, "scope/unstaged.txt") {
		t.Fatalf("missing aggregate patches: staged=%q unstaged=%q", stagedPatch, unstagedPatch)
	}
	if strings.Contains(stagedPatch+unstagedPatch, "outside.txt") || strings.Contains(stagedPatch+unstagedPatch, "must not appear") {
		t.Fatal("resource-prefix snapshot leaked an out-of-scope change")
	}
	if len(snapshot.Untracked) != 1 || snapshot.Untracked[0].Path != "scope/untracked.html" ||
		snapshot.Untracked[0].SizeBytes == 0 {
		t.Fatalf("untracked metadata=%+v", snapshot.Untracked)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("must not execute")) {
		t.Fatal("untracked file content entered the snapshot")
	}
	if bytes.Contains(encoded, []byte(".teamva/tool-")) || bytes.Contains(encoded, []byte("tool noise")) {
		t.Fatal("tool bridge noise entered the Run snapshot")
	}
	if len(encoded) > 20<<20 {
		t.Fatalf("snapshot exceeded uncompressed bound: %d", len(encoded))
	}
}

func TestRunSnapshotBoundsEscapedJSONPayload(t *testing.T) {
	snapshot := nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository", Label: "Repository", RootDisplay: "Repository", Phase: "end",
		CapturedAt: time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{{
			Area: "unstaged", Patch: strings.Repeat("<", 4<<20),
			Paths: []string{"web/large.html"}, BinaryPaths: []string{},
		}},
		Untracked: []nodeprotocol.GitRunUntrackedEntry{}, Warnings: []nodeprotocol.GitRepositoryWarning{},
	}
	enforceRunSnapshotPayloadLimit(&snapshot)
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > maxRunSnapshotJSONBytes || snapshot.Areas[0].Patch != "" ||
		!snapshot.Areas[0].Truncated || !snapshot.Truncated {
		t.Fatalf("bounded escaped snapshot bytes=%d snapshot=%+v", len(payload), snapshot)
	}
}

func TestGitErrorFallbackDoesNotExposePathOrCallerMessage(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "private-repository")
	err := gitError("git_unavailable", "failed to inspect "+secretPath, false)
	var readError *nodeprotocol.GitReadError
	if !errors.As(err, &readError) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(readError.Message, secretPath) ||
		strings.Contains(readError.Error(), secretPath) || !isASCIIText(readError.Message) {
		t.Fatalf("unsafe Git error=%+v", readError)
	}
}

func listChanges(t *testing.T, service *Service, resources []nodeprotocol.GitResourceRequest, repositoryID, area, prefix string, expand bool) nodeprotocol.GitChangesPage {
	t.Helper()
	page, err := service.ListChanges(context.Background(), nodeprotocol.GitChangesRequest{
		ProjectID: "project_1", Resources: resources, RepositoryID: repositoryID,
		Area: area, Prefix: prefix, ExpandUntracked: expand, Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func findChange(items []nodeprotocol.GitChange, displayPath string) *nodeprotocol.GitChange {
	for index := range items {
		if items[index].DisplayPath == displayPath {
			return &items[index]
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isASCIIText(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character > 0x7f {
			return false
		}
	}
	return true
}

func assertGitError(t *testing.T, err error, code string) {
	t.Helper()
	var gitErr *nodeprotocol.GitReadError
	if !errors.As(err, &gitErr) || gitErr.Code != code {
		t.Fatalf("error=%v, want GitReadError %s", err, code)
	}
}

func initRepository(t *testing.T, directory string) {
	t.Helper()
	gitCommand(t, directory, "init", "-b", "main")
	gitCommand(t, directory, "config", "user.name", "Git Read Test")
	gitCommand(t, directory, "config", "user.email", "git-read@example.invalid")
}

func gitCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", arguments, err, output)
	}
}

func gitCommandMustFail(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := command.Run(); err == nil {
		t.Fatalf("git %v unexpectedly succeeded", arguments)
	}
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	writeBytes(t, path, []byte(value))
}

func writeBytes(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}
