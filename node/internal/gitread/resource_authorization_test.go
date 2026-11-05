package gitread

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func approvalRepository(t *testing.T, directory string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	initRepository(t, canonical)
	return canonical
}

func approvalService(t *testing.T, state string) *Service {
	t.Helper()
	service := NewWithResourceAuthorizationDirectory(state)
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func approve(t *testing.T, service *Service, resource nodeprotocol.GitResourceRequest, version int64) {
	t.Helper()
	if err := service.ApproveResource(t.Context(), resource.ResourceID, resource.Path, version); err != nil {
		t.Fatal(err)
	}
}

func approvedScope(t *testing.T, service *Service, request nodeprotocol.GitRepositoriesRequest) (string, RepositoryExecutionScope) {
	t.Helper()
	list, err := service.ListRepositories(t.Context(), request)
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("expected one approved repository: count=%d err=%v", len(list.Repositories), err)
	}
	scope, err := service.ResolveRepositoryExecutionScope(t.Context(), request, list.Repositories[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return list.Repositories[0].ID, scope
}

// Formal regression for the read-only audit's restart/symlink reproducer. The
// canonical path remains the persisted authorization even without a live token.
func TestApprovedResourceSymlinkReplacementCannotReauthorizeAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture symlinks require Unix permissions")
	}
	base := t.TempDir()
	approved := approvalRepository(t, filepath.Join(base, "approved"))
	external := approvalRepository(t, filepath.Join(base, "external"))
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "approved-project", Resources: []nodeprotocol.GitResourceRequest{
		{ResourceID: "same-approved-resource", Name: "Approved", Path: approved},
	}}
	first := New()
	oldID, oldScope := approvedScope(t, first, request)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(approved, approved+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, approved); err != nil {
		t.Fatal(err)
	}
	second := New()
	defer second.Close()
	for _, refresh := range []bool{false, true} {
		request.Refresh = refresh
		_, err := second.ListRepositories(t.Context(), request)
		assertGitErrorOneOf(t, err, "path_out_of_scope")
	}
	if second.DetectResourceRepository(t.Context(), approved) {
		t.Fatal("resource detection followed a replacement symlink")
	}
	assertGitErrorOneOf(t, second.ValidateRepositoryExecutionScope(t.Context(), request, oldID, oldScope), "path_out_of_scope")
}

func TestDurableResourceApprovalRequiresExplicitValidationAndSurvivesRestart(t *testing.T) {
	base := t.TempDir()
	directory := approvalRepository(t, filepath.Join(base, "repository"))
	state := filepath.Join(base, "private-approvals")
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	first := approvalService(t, state)
	_, err := first.ListRepositories(t.Context(), request)
	assertGitErrorOneOf(t, err, "path_out_of_scope")
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("discovery created private approval state")
	}
	approve(t, first, resource, 7)
	for _, name := range []string{"", resourceApprovalName(resource.ResourceID), resourceApprovalName(resource.ResourceID) + ".lock"} {
		info, err := os.Lstat(filepath.Join(state, name))
		if err != nil || info.Mode().Perm()&0o077 != 0 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("approval state is not private, real filesystem state")
		}
	}
	id, old := approvedScope(t, first, request)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := approvalService(t, state)
	newID, restored := approvedScope(t, second, request)
	if newID != id || restored.Hash != old.Hash || restored.Root != directory {
		t.Fatal("unchanged approval did not retain its exact scope across restart")
	}
	approve(t, second, resource, 7) // Exact replay is idempotent.
	approve(t, second, resource, 8) // Explicit new validation fences old scope/session hints.
	_, renewed := approvedScope(t, second, request)
	if renewed.Hash == old.Hash {
		t.Fatal("explicit new approval version retained an old native-session scope")
	}
	assertGitErrorOneOf(t, second.ValidateRepositoryExecutionScope(t.Context(), request, id, old), "snapshot_expired")
}

func TestDurableResourceApprovalRejectsSamePathNewInodeUntilExplicitNewVersion(t *testing.T) {
	base := t.TempDir()
	directory := approvalRepository(t, filepath.Join(base, "repository"))
	state := filepath.Join(base, "private-approvals")
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	first := approvalService(t, state)
	approve(t, first, resource, 10)
	oldID, old := approvedScope(t, first, request)
	recordPath := filepath.Join(state, resourceApprovalName(resource.ResourceID))
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	approvalRepository(t, directory)
	second := approvalService(t, state)
	_, err = second.ListRepositories(t.Context(), request)
	assertGitErrorOneOf(t, err, "path_out_of_scope")
	assertGitErrorOneOf(t, second.ApproveResource(t.Context(), resource.ResourceID, directory, 10), "path_out_of_scope")
	assertGitErrorOneOf(t, second.ApproveResource(t.Context(), resource.ResourceID, directory, 9), "path_out_of_scope")
	after, err := os.ReadFile(recordPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read or stale validation silently updated the durable approval")
	}
	approve(t, second, resource, 11)
	newID, renewed := approvedScope(t, second, request)
	if newID == oldID || renewed.Hash == old.Hash {
		t.Fatal("replacement directory reused the old repository/native-session scope")
	}
	assertGitErrorOneOf(t, second.ValidateRepositoryExecutionScope(t.Context(), request, oldID, old), "repository_not_found")
}

func TestDurableResourceApprovalRejectsRenamedAncestorSymlinkAndKeepsCanonicalPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture symlinks require Unix permissions")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "parent")
	directory := approvalRepository(t, filepath.Join(parent, "repository"))
	state := filepath.Join(base, "private-approvals")
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	first := approvalService(t, state)
	approve(t, first, resource, 1)
	_, old := approvedScope(t, first, request)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent+"-renamed", parent); err != nil {
		t.Fatal(err)
	}
	second := approvalService(t, state)
	_, err = second.ListRepositories(t.Context(), request)
	assertGitErrorOneOf(t, err, "path_out_of_scope")
	// Even a newer validation cannot approve an arbitrary symlink path. A user
	// must select/persist the new canonical directory or restore the real path.
	assertGitErrorOneOf(t, second.ApproveResource(t.Context(), resource.ResourceID, directory, 2), "path_out_of_scope")
	canonical := filepath.Join(parent+"-renamed", "repository")
	resource.Path = canonical
	request.Resources[0] = resource
	approve(t, second, resource, 2)
	_, renewed := approvedScope(t, second, request)
	if renewed.Root != canonical || renewed.Hash == old.Hash {
		t.Fatal("explicit canonical replacement did not create a new exact scope")
	}
}

func TestDurablePartialResourceApprovalsKeepDisjointRootsExact(t *testing.T) {
	base := t.TempDir()
	repository := approvalRepository(t, filepath.Join(base, "repository"))
	state := filepath.Join(base, "private-approvals")
	resources := []nodeprotocol.GitResourceRequest{}
	for _, name := range []string{"scope-a", "scope-c"} {
		directory := filepath.Join(repository, name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		resources = append(resources, nodeprotocol.GitResourceRequest{ResourceID: name, Name: name, Path: directory})
	}
	first := approvalService(t, state)
	for _, resource := range resources {
		approve(t, first, resource, 3)
	}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: resources}
	id, before := approvedScope(t, first, request)
	reordered := request
	reordered.Resources = []nodeprotocol.GitResourceRequest{resources[1], resources[0]}
	_, reorderedScope := approvedScope(t, first, reordered)
	if reorderedScope.Hash != before.Hash {
		t.Fatal("resource ordering changed the durable authorization/native-session scope")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := approvalService(t, state)
	_, scope := approvedScope(t, second, request)
	want := []string{resources[0].Path, resources[1].Path}
	sort.Strings(want)
	sort.Strings(scope.WritableRoots)
	if scope.Full || scope.Hash != before.Hash || strings.Join(scope.WritableRoots, "\x00") != strings.Join(want, "\x00") {
		t.Fatal("partial authorizations widened or lost their durable identity")
	}
	// Replacing one approved child keeps the enclosing Git repository ID intact,
	// but must still revoke the old authorization and native-session scope.
	child := resources[0].Path
	if err := os.Rename(child, child+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	assertGitErrorOneOf(t, second.ValidateRepositoryExecutionScope(t.Context(), request, id, before), "path_out_of_scope")
	approve(t, second, resources[0], 4)
	newID, renewed := approvedScope(t, second, request)
	if newID != id || renewed.Hash == before.Hash || len(renewed.WritableRoots) != 2 {
		t.Fatal("partial root replacement did not fence an old native-session scope")
	}
}

func TestDurableResourceApprovalCorruptionAndSymlinkNeverSelfHeal(t *testing.T) {
	base := t.TempDir()
	directory := approvalRepository(t, filepath.Join(base, "repository"))
	state := filepath.Join(base, "private-approvals")
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	service := approvalService(t, state)
	approve(t, service, resource, 1)
	path := filepath.Join(state, resourceApprovalName(resource.ResourceID))
	for _, data := range []string{"{", `{"format":2}`, strings.Repeat("x", maxResourceApprovalBytes+1)} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := service.ListRepositories(t.Context(), request)
		assertGitErrorOneOf(t, err, "path_out_of_scope")
		assertGitErrorOneOf(t, service.ApproveResource(t.Context(), resource.ResourceID, directory, 2), "path_out_of_scope")
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatal("invalid private approval was rewritten")
		}
	}
	if runtime.GOOS != "windows" {
		external := filepath.Join(base, "outside-state")
		if err := os.WriteFile(external, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		_, err := service.ListRepositories(t.Context(), request)
		assertGitErrorOneOf(t, err, "path_out_of_scope")
		assertGitErrorOneOf(t, service.ApproveResource(t.Context(), resource.ResourceID, directory, 2), "path_out_of_scope")
		data, err := os.ReadFile(external)
		if err != nil || string(data) != "unchanged" {
			t.Fatal("approval followed a private-state symlink")
		}
	}
}

func TestDurableResourceApprovalsSerializeAcrossServicesAndHonorCancellation(t *testing.T) {
	base := t.TempDir()
	directory := approvalRepository(t, filepath.Join(base, "repository"))
	state := filepath.Join(base, "private-approvals")
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	services := []*Service{approvalService(t, state), approvalService(t, state)}
	approve(t, services[0], resource, 1)
	var wg sync.WaitGroup
	for version := int64(2); version <= 12; version++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = services[version%2].ApproveResource(t.Context(), resource.ResourceID, directory, version)
		}()
	}
	wg.Wait()
	payload, err := os.ReadFile(filepath.Join(state, resourceApprovalName(resource.ResourceID)))
	if err != nil {
		t.Fatal(err)
	}
	var approval resourceApproval
	if err := json.Unmarshal(payload, &approval); err != nil || approval.ValidationVersion != 12 {
		t.Fatal("concurrent validation downgraded or corrupted the durable approval")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := services[0].ApproveResource(ctx, resource.ResourceID, directory, 13); err != context.Canceled {
		t.Fatalf("cancelled validation error=%v", err)
	}
}

func TestApprovedCanonicalSelectionAndMacOSSystemAliases(t *testing.T) {
	base := t.TempDir()
	directory := approvalRepository(t, filepath.Join(base, "repository"))
	if runtime.GOOS != "windows" {
		selection := filepath.Join(base, "selection")
		if err := os.Symlink(directory, selection); err != nil {
			t.Fatal(err)
		}
		// Registration may resolve an intentional selection; reads must retain
		// its canonical result, not reinterpret the user's alias on every Run.
		canonical, err := filepath.EvalSymlinks(selection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := canonicalResourceDirectory(selection); err == nil {
			t.Fatal("untrusted alias was accepted as an approved Resource path")
		}
		if got, err := canonicalResourceDirectory(canonical); err != nil || got != directory {
			t.Fatal("legitimately registered canonical path was rejected")
		}
	}
	if runtime.GOOS == "darwin" && strings.HasPrefix(directory, "/private/var/") {
		alias := strings.TrimPrefix(directory, "/private")
		service := approvalService(t, filepath.Join(base, "private-approvals"))
		resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: alias}
		approve(t, service, resource, 1)
		_, scope := approvedScope(t, service, nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}})
		if scope.Root != directory || len(scope.WritableRoots) != 1 || scope.WritableRoots[0] != directory {
			t.Fatal("fixed macOS /var alias did not retain the canonical root")
		}
	}
}
