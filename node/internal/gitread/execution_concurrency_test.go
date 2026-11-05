package gitread

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Hold all Git slots until both fresh execution-scope discoveries are in
// flight. This reproduces the two-mention startup race without timing sleeps,
// a Provider process, or credentials.
func holdExecutionGit(t *testing.T, service *Service) func() {
	t.Helper()
	for range cap(service.gitSem) {
		service.gitSem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range cap(service.gitSem) {
				<-service.gitSem
			}
		})
	}
	t.Cleanup(release)
	return release
}

func awaitExecutionFlights(t *testing.T, service *Service, count int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		service.mu.Lock()
		flights := len(service.flights)
		service.mu.Unlock()
		if flights >= count {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("expected %d gated execution discoveries, got %d", count, flights)
		}
	}
}

func TestParallelExecutionScopesSurviveDiscoveryRefreshBarrier(t *testing.T) {
	for _, mode := range []string{"same-scope", "different-scope", "different-project"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			root := approvalRepository(t, filepath.Join(base, "repository"))
			service := approvalService(t, filepath.Join(base, "approvals"))
			requests := [2]nodeprotocol.GitRepositoriesRequest{}
			ids := [2]string{}
			want := [2]RepositoryExecutionScope{}
			for index := range requests {
				path := root
				resourceID := "resource"
				projectID := "project"
				if mode == "different-scope" {
					resourceID = []string{"left", "right"}[index]
					path = filepath.Join(root, resourceID)
					if err := os.MkdirAll(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "different-project" {
					projectID = []string{"project-left", "project-right"}[index]
				}
				resource := nodeprotocol.GitResourceRequest{ResourceID: resourceID, Name: resourceID, Path: path}
				approve(t, service, resource, 1)
				requests[index] = nodeprotocol.GitRepositoriesRequest{ProjectID: projectID, Resources: []nodeprotocol.GitResourceRequest{resource}}
				ids[index], want[index] = approvedScope(t, service, requests[index])
			}
			release := holdExecutionGit(t, service)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			type result struct {
				scope RepositoryExecutionScope
				err   error
			}
			done := [2]chan result{make(chan result, 1), make(chan result, 1)}
			for index := range requests {
				go func() {
					scope, err := service.ResolveRepositoryExecutionScope(ctx, requests[index], ids[index])
					if err == nil {
						err = service.ValidateRepositoryExecutionScope(ctx, requests[index], ids[index], scope)
					}
					if err == nil {
						_, err = service.CaptureWorkspaceSnapshot(ctx, requests[index], ids[index], "start")
					}
					done[index] <- result{scope: scope, err: err}
				}()
				awaitExecutionFlights(t, service, index+1)
			}
			release()
			for index := range done {
				select {
				case result := <-done[index]:
					if result.err != nil || !reflect.DeepEqual(result.scope, want[index]) {
						t.Errorf("parallel scope %d: scope=%+v want=%+v err=%v", index, result.scope, want[index], result.err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

func TestExecutionScopeCancellationWhileGitIsGated(t *testing.T) {
	base := t.TempDir()
	root := approvalRepository(t, filepath.Join(base, "repository"))
	service := approvalService(t, filepath.Join(base, "approvals"))
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
	approve(t, service, resource, 1)
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	id, _ := approvedScope(t, service, request)
	release := holdExecutionGit(t, service)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := service.ResolveRepositoryExecutionScope(ctx, request, id)
		done <- err
	}()
	awaitExecutionFlights(t, service, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled execution scope: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execution scope ignored cancellation")
	}
	release()
}

func TestExecutionScopeCacheRetryDoesNotRenewRevokedOrReplacedAnchors(t *testing.T) {
	for _, change := range []string{"revoke-approval", "replace-inode", "replace-with-symlink", "new-approval-version"} {
		t.Run(change, func(t *testing.T) {
			base := t.TempDir()
			root := approvalRepository(t, filepath.Join(base, "repository"))
			state := filepath.Join(base, "approvals")
			service := approvalService(t, state)
			resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
			approve(t, service, resource, 1)
			request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
			id, scope := approvedScope(t, service, request)
			record := filepath.Join(state, resourceApprovalName(resource.ResourceID))
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			release := holdExecutionGit(t, service)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := [2]chan error{make(chan error, 1), make(chan error, 1)}
			for index := range done {
				go func() {
					_, err := service.ResolveRepositoryExecutionScope(ctx, request, id)
					done[index] <- err
				}()
				awaitExecutionFlights(t, service, index+1)
			}
			switch change {
			case "revoke-approval":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
			case "replace-inode", "replace-with-symlink":
				if err := os.Rename(root, root+"-original"); err != nil {
					t.Fatal(err)
				}
				if change == "replace-inode" {
					approvalRepository(t, root)
				} else if err := os.Symlink(root+"-original", root); err != nil {
					t.Fatal(err)
				}
			case "new-approval-version":
				approve(t, service, resource, 2) // Explicit action, never a read fallback.
			}
			afterChange, readErr := os.ReadFile(record)
			release()
			for index := range done {
				select {
				case err := <-done[index]:
					// No barrier retry may turn a real approval/identity change into
					// a successful authorization of the in-flight old scope.
					assertGitErrorOneOf(t, err, "path_out_of_scope", "repository_not_found")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			afterReads, err := os.ReadFile(record)
			if change == "revoke-approval" {
				if !os.IsNotExist(readErr) || !os.IsNotExist(err) {
					t.Fatal("execution read recreated a revoked approval")
				}
			} else {
				if readErr != nil || err != nil || !bytes.Equal(afterChange, afterReads) {
					t.Fatal("execution read modified an explicit approval")
				}
				if change != "new-approval-version" && !bytes.Equal(before, afterReads) {
					t.Fatal("identity replacement silently renewed the old approval")
				}
			}
			if err := service.ValidateRepositoryExecutionScope(ctx, request, id, scope); err == nil {
				t.Fatal("old execution scope survived revocation/replacement")
			}
		})
	}
}

func TestExecutionRepositoryPinsSurviveOnlyCacheRetirement(t *testing.T) {
	base := t.TempDir()
	root := approvalRepository(t, filepath.Join(base, "repository"))
	service := approvalService(t, filepath.Join(base, "approvals"))
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
	approve(t, service, resource, 1)
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	id, want := approvedScope(t, service, request)
	item, err := service.executionRepository(t.Context(), request, id)
	if err != nil {
		t.Fatal(err)
	}
	defer item.closeIdentity()
	service.mu.Lock()
	cached := service.projects[request.ProjectID].repositories[id]
	service.mu.Unlock()
	service.invalidateProject(request.ProjectID, "refresh")
	if !errors.Is(cached.validateIdentity(), errRepositoryIdentityClosed) {
		t.Fatal("fixture did not retire the cached identity handles")
	}
	scope, err := executionScopeForRepository(item)
	if err != nil || !reflect.DeepEqual(scope, want) {
		t.Fatalf("cache refresh closed independently pinned execution scope: %v", err)
	}
	if _, err := service.runGit(t.Context(), item, statusOutputLimit, false, "status", "--porcelain=v2", "-z"); err != nil {
		t.Fatalf("cache refresh invalidated an active snapshot Git read: %v", err)
	}
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	approvalRepository(t, root)
	if _, err := executionScopeForRepository(item); err == nil {
		t.Fatal("independent pins accepted a same-path inode replacement")
	}
	assertGitErrorOneOf(t, service.validateExecutionAuthorization(t.Context(), request, item), "path_out_of_scope")
}

func TestExecutionScopeServiceCloseWhileGitIsGated(t *testing.T) {
	base := t.TempDir()
	root := approvalRepository(t, filepath.Join(base, "repository"))
	service := approvalService(t, filepath.Join(base, "approvals"))
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
	approve(t, service, resource, 1)
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	id, _ := approvedScope(t, service, request)
	release := holdExecutionGit(t, service)
	done := make(chan error, 1)
	go func() {
		_, err := service.ResolveRepositoryExecutionScope(t.Context(), request, id)
		done <- err
	}()
	awaitExecutionFlights(t, service, 1)
	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err := <-done:
		assertGitErrorOneOf(t, err, "runtime_closed")
	case <-time.After(3 * time.Second):
		t.Fatal("service close did not stop execution discovery")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service close retained gated operations")
	}
	release()
}

func TestOnlyCacheLifecycleErrorsAreRetryableInternally(t *testing.T) {
	for _, err := range []error{
		gitError("snapshot_expired", "identity changed", true),
		gitError("snapshot_expired", "Git metadata changed", true),
		resourceAuthorizationError(), context.Canceled, context.DeadlineExceeded,
	} {
		if isRepositoryCacheInvalidated(err) {
			t.Fatalf("non-cache error marked for internal retry: %v", err)
		}
	}
	if !isRepositoryCacheInvalidated(repositoryCacheInvalidated()) {
		t.Fatal("cache barrier marker lost")
	}
}

func TestRepositoryDiscoveryBarrierIsDistinctFromFilesystemExpiration(t *testing.T) {
	base := t.TempDir()
	root := approvalRepository(t, filepath.Join(base, "repository"))
	service := approvalService(t, filepath.Join(base, "approvals"))
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
	approve(t, service, resource, 1)
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}, Refresh: true}
	hash, err := service.authorizedResourceHash(t.Context(), request.Resources)
	if err != nil {
		t.Fatal(err)
	}
	release := holdExecutionGit(t, service)
	done := make(chan error, 1)
	go func() {
		_, err := service.listRepositories(t.Context(), request, hash)
		done <- err
	}()
	awaitExecutionFlights(t, service, 1)
	// No directory, approval, or Git metadata changes: only the cache epoch moves.
	service.invalidateProject(request.ProjectID, "refresh")
	release()
	select {
	case err := <-done:
		assertGitErrorOneOf(t, err, "snapshot_expired")
		if !isRepositoryCacheInvalidated(err) {
			t.Fatalf("refresh barrier lost its internal cause classification: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gated discovery did not finish")
	}
}

func TestRepositoryCacheRetryBudgetStopsPersistentCacheChurn(t *testing.T) {
	base := t.TempDir()
	root := approvalRepository(t, filepath.Join(base, "repository"))
	service := approvalService(t, filepath.Join(base, "approvals"))
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: root}
	approve(t, service, resource, 1)
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	hash, err := service.authorizedResourceHash(t.Context(), request.Resources)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	close(finished)
	service.mu.Lock()
	key := "repositories:" + request.ProjectID + ":" + hash + ":" + strconv.FormatUint(service.projectEpoch, 10)
	service.flights[key] = &flight{done: finished, err: repositoryCacheInvalidated()}
	service.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = service.ListRepositories(ctx, request)
	if !isRepositoryCacheInvalidated(err) || ctx.Err() != nil {
		t.Fatalf("persistent cache churn did not stop at its retry budget: %v", err)
	}
	_, err = service.executionRepository(ctx, request, "not-resolved")
	if !isRepositoryCacheInvalidated(err) || ctx.Err() != nil {
		t.Fatalf("execution reread did not stop at its retry budget: %v", err)
	}
}
