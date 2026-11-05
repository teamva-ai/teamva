package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

type fakeGitRelay struct {
	mu           sync.Mutex
	requests     []nodeprotocol.GitRepositoriesRequest
	repositories []nodeprotocol.GitRepositorySummary
	truncated    bool
	snapshots    map[string][]nodeprotocol.GitRunWorkspaceSnapshot
	snapshotRuns []string
	changes      func(nodeprotocol.GitChangesRequest) nodeprotocol.GitChangesPage
	changeLimits []int
	diff         nodeprotocol.GitDiffResult
}

func (f *fakeGitRelay) ListGitRepositories(_ context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	configured := append([]nodeprotocol.GitRepositorySummary(nil), f.repositories...)
	truncated := f.truncated
	f.mu.Unlock()
	items := configured
	if items == nil {
		resourceIDs := []string{}
		if len(request.Resources) > 0 {
			resourceIDs = []string{request.Resources[0].ResourceID}
		}
		items = []nodeprotocol.GitRepositorySummary{{
			ID: "repo_test", CommonID: "common_test", Label: "repo", RootDisplay: "Code",
			ResourceIDs: resourceIDs, Scope: "full", Generation: 1,
			State: "clean", Warnings: []nodeprotocol.GitRepositoryWarning{},
		}}
	}
	return nodeprotocol.GitRepositoryList{
		SnapshotID: "snapshot_test", Repositories: items,
		Truncated: truncated, Warnings: []nodeprotocol.GitRepositoryWarning{},
	}, nil
}

func TestIssueRunAndScheduleUseProjectWorkspace(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	alice := createTestIdentity(t, repository, "repo-alice@example.test")
	bob := createTestIdentity(t, repository, "repo-bob@example.test")
	headers := func(identity testIdentity) map[string]string {
		return map[string]string{
			"Cookie":       sessionCookieName + "=" + identity.token,
			"X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin,
		}
	}
	relay := &fakeGitRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	computer := createTestComputer(t, repository, headers(alice), "Repository Mac")
	project, err := repository.CreateProject(t.Context(), alice.userID, domain.Project{
		ComputerID: computer.ID, Name: "Repository Project",
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := repository.CreateResource(t.Context(), alice.userID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "code_directory", Name: "Code", Path: filepath.Join(t.TempDir(), "code"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.UpdateResource(t.Context(), alice.userID, resource.ID, resource.Version, func(item *domain.ProjectResource) {
		item.Status = "ready"
		item.GitDetected = true
	}); err != nil {
		t.Fatal(err)
	}
	service, err := repository.UpsertAgentService(t.Context(), alice.userID, domain.AgentService{
		ComputerID: computer.ID, Provider: "opencode", LoginStatus: "authenticated", Status: "ready",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(t.Context(), alice.userID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: service.ID, Name: "Runner", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.CreateAgentRevision(t.Context(), alice.userID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: service.ID,
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Repository selection fields are no longer part of any product mutation
	// contract. Strict JSON decoding rejects stale clients rather than silently
	// persisting a misleading single-repository binding.
	legacyIssue := request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "legacy issue", "git_repository_id": "repo-a",
	}, headers(alice), http.StatusBadRequest)
	if legacyIssue.Error == nil || legacyIssue.Error.Code != "invalid_argument" {
		t.Fatalf("legacy issue field error=%+v", legacyIssue.Error)
	}
	withoutRuntime := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	issueResponse := request(t, withoutRuntime, http.MethodPost,
		"/v1/projects/"+project.ID+"/issues", map[string]any{
			"title": "project workspace",
		}, headers(alice), http.StatusCreated)
	issue := decodeData[domain.Issue](t, issueResponse)
	if issue.ProjectID != project.ID || bytes.Contains(issueResponse.Data, []byte("git_repository")) {
		t.Fatalf("workspace issue=%+v data=%s", issue, issueResponse.Data)
	}
	crossTenant := request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "cross tenant",
	}, headers(bob), http.StatusNotFound)
	if crossTenant.Error == nil || crossTenant.Error.Code != "not_found" {
		t.Fatalf("cross-tenant issue error=%+v", crossTenant.Error)
	}

	scheduleRequest := map[string]any{
		"name": "nested repositories", "schedule_kind": "cron",
		"schedule_expression": "0 9 * * *", "timezone": "UTC",
		"prompt":   []map[string]any{{"type": "text", "text": "work"}},
		"agent_id": agent.ID,
	}
	legacySchedule := map[string]any{
		"name": "legacy", "schedule_kind": "cron", "schedule_expression": "0 9 * * *", "timezone": "UTC",
		"prompt": []map[string]any{{"type": "text", "text": "work"}}, "agent_id": agent.ID,
		"git_repository_id": "repo-a",
	}
	legacyScheduleResponse := request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/scheduled-tasks", legacySchedule, headers(alice), http.StatusBadRequest)
	if legacyScheduleResponse.Error == nil || legacyScheduleResponse.Error.Code != "invalid_argument" {
		t.Fatalf("legacy schedule field error=%+v", legacyScheduleResponse.Error)
	}
	scheduleResponse := request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/scheduled-tasks", scheduleRequest, headers(alice), http.StatusCreated)
	scheduledTask := decodeData[domain.ScheduledTask](t, scheduleResponse)
	if scheduledTask.ProjectID != project.ID || bytes.Contains(scheduleResponse.Data, []byte("git_repository")) {
		t.Fatalf("scheduled task=%+v", scheduledTask)
	}
	schedulePatch := map[string]any{
		"name": "nested repositories updated", "schedule_kind": "cron",
		"schedule_expression": "30 9 * * *", "timezone": "UTC",
		"prompt":           []map[string]any{{"type": "text", "text": "updated work"}},
		"agent_id":         agent.ID,
		"expected_version": scheduledTask.Version,
	}
	patchedScheduleResponse := request(t, router, http.MethodPatch,
		"/v1/scheduled-tasks/"+scheduledTask.ID, schedulePatch, headers(alice), http.StatusOK)
	patchedSchedule := decodeData[domain.ScheduledTask](t, patchedScheduleResponse)
	if patchedSchedule.Name != "nested repositories updated" {
		t.Fatalf("patched schedule=%+v", patchedSchedule)
	}
	instance, _, err := repository.EnsureIssueAgentInstance(t.Context(), alice.userID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	mentionResponse := request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "@Runner work", "mentioned_agent_ids": []string{agent.ID},
	}, headers(alice), http.StatusCreated)
	mention := decodeData[domain.IssueComment](t, mentionResponse)
	if mention.IssueID != issue.ID {
		t.Fatalf("workspace mention=%+v", mention)
	}
	legacyRun := request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": instance.ID, "input": "work", "expected_issue_version": issue.Version,
		"git_repository_id": "repo-a",
	}, map[string]string{
		"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
		"Origin": testBrowserOrigin, "Idempotency-Key": "legacy-repository-run",
	}, http.StatusBadRequest)
	if legacyRun.Error == nil || legacyRun.Error.Code != "invalid_argument" {
		t.Fatalf("legacy run field error=%+v", legacyRun.Error)
	}
	runResponse := request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": instance.ID, "input": "work", "expected_issue_version": issue.Version,
	}, map[string]string{
		"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
		"Origin": testBrowserOrigin, "Idempotency-Key": "project-workspace-run",
	}, http.StatusAccepted)
	workspaceRun := decodeData[domain.Run](t, runResponse)
	if workspaceRun.ProjectID != project.ID || bytes.Contains(runResponse.Data, []byte("git_repository")) {
		t.Fatalf("workspace run=%+v data=%s", workspaceRun, runResponse.Data)
	}
	replayedWithoutRuntime := request(t, withoutRuntime, http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": instance.ID, "input": "work", "expected_issue_version": 999,
	}, map[string]string{
		"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
		"Origin": testBrowserOrigin, "Idempotency-Key": "project-workspace-run",
	}, http.StatusOK)
	replayedRun := decodeData[domain.Run](t, replayedWithoutRuntime)
	if !replayedWithoutRuntime.Meta.Replayed || replayedRun.ID != workspaceRun.ID {
		t.Fatalf("early replay=%+v meta=%+v", replayedRun, replayedWithoutRuntime.Meta)
	}
	changedReplay := request(t, withoutRuntime, http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": instance.ID, "input": "changed", "expected_issue_version": 999,
	}, map[string]string{
		"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
		"Origin": testBrowserOrigin, "Idempotency-Key": "project-workspace-run",
	}, http.StatusConflict)
	if changedReplay.Error == nil || changedReplay.Error.Code != "idempotency_conflict" {
		t.Fatalf("changed replay must conflict: %+v", changedReplay.Error)
	}
	unsupportedHistory := request(t, withoutRuntime, http.MethodGet,
		"/v1/runs/"+workspaceRun.ID+"/git/changes", nil, headers(alice), http.StatusNotImplemented)
	if unsupportedHistory.Error == nil || unsupportedHistory.Error.Code != "capability_unsupported" {
		t.Fatalf("unsupported run history error=%+v", unsupportedHistory.Error)
	}
	emptyHistoryResponse := request(t, router, http.MethodGet,
		"/v1/runs/"+workspaceRun.ID+"/git/changes", nil, headers(alice), http.StatusOK)
	if emptyHistoryResponse.Recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("run changes Cache-Control=%q", emptyHistoryResponse.Recorder.Header().Get("Cache-Control"))
	}
	emptyHistory := decodeData[nodeprotocol.GitRunChangesResult](t, emptyHistoryResponse)
	if emptyHistory.Snapshots == nil || emptyHistory.ProviderReports == nil ||
		len(emptyHistory.Snapshots) != 0 || len(emptyHistory.ProviderReports) != 0 {
		t.Fatalf("empty run history must use collections: %+v", emptyHistory)
	}
	relay.mu.Lock()
	relay.snapshotRuns = nil
	relay.snapshots = map[string][]nodeprotocol.GitRunWorkspaceSnapshot{
		workspaceRun.ID: {
			{
				RepositoryID: "repo-a", Label: "A", RootDisplay: "First", Phase: "start",
				Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{}, ContentHash: "snapshot-a",
			},
			{
				RepositoryID: "repo-b", Label: "B", RootDisplay: "Second", Phase: "start",
				Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{}, ContentHash: "snapshot-b",
			},
		},
	}
	relay.mu.Unlock()
	changesResponse := request(t, router, http.MethodGet, "/v1/runs/"+workspaceRun.ID+"/git/changes", nil,
		headers(alice), http.StatusOK)
	runChanges := decodeData[nodeprotocol.GitRunChangesResult](t, changesResponse)
	if runChanges.RunID != workspaceRun.ID || len(runChanges.Snapshots) != 2 {
		t.Fatalf("run changes=%+v", runChanges)
	}
	request(t, router, http.MethodGet, "/v1/runs/"+workspaceRun.ID+"/git/changes", nil,
		headers(bob), http.StatusNotFound)
	relay.mu.Lock()
	if len(relay.snapshotRuns) != 1 || relay.snapshotRuns[0] != workspaceRun.ID {
		t.Fatalf("tenant-unscoped Run ID reached runtime: %+v", relay.snapshotRuns)
	}
	relay.mu.Unlock()

	// Partial repository scopes and incomplete discovery no longer gate product
	// persistence. Node validates the complete project workspace at dispatch time.
	relay.mu.Lock()
	relay.repositories = []nodeprotocol.GitRepositorySummary{{
		ID: "repo-partial", CommonID: "common-partial", Label: "Partial", Scope: "resource_prefix", State: "clean",
	}}
	relay.truncated = true
	relay.mu.Unlock()
	partialIssueResponse := request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "partial workspace",
	}, headers(alice), http.StatusCreated)
	partialIssue := decodeData[domain.Issue](t, partialIssueResponse)
	partialInstance, _, err := repository.EnsureIssueAgentInstance(t.Context(), alice.userID, partialIssue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	partialRunResponse := request(t, router, http.MethodPost, "/v1/issues/"+partialIssue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": partialInstance.ID, "input": "work",
		"expected_issue_version": partialIssue.Version,
	}, map[string]string{
		"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
		"Origin": testBrowserOrigin, "Idempotency-Key": "partial-workspace-run",
	}, http.StatusAccepted)
	partialRun := decodeData[domain.Run](t, partialRunResponse)
	if partialRun.ProjectID != project.ID {
		t.Fatalf("partial workspace run=%+v", partialRun)
	}
	partialScheduleRequest := map[string]any{
		"name": "partial workspace schedule", "schedule_kind": "cron",
		"schedule_expression": "0 9 * * *", "timezone": "UTC",
		"prompt":   []map[string]any{{"type": "text", "text": "work"}},
		"agent_id": agent.ID,
	}
	partialScheduleResponse := request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/scheduled-tasks", partialScheduleRequest,
		headers(alice), http.StatusCreated)
	partialSchedule := decodeData[domain.ScheduledTask](t, partialScheduleResponse)
	if partialSchedule.ProjectID != project.ID {
		t.Fatalf("partial workspace schedule=%+v", partialSchedule)
	}
	runNow := request(t, router, http.MethodPost,
		"/v1/scheduled-tasks/"+partialSchedule.ID+"/run-now", nil,
		map[string]string{
			"Cookie": sessionCookieName + "=" + alice.token, "X-CSRF-Token": alice.csrf,
			"Origin": testBrowserOrigin, "Idempotency-Key": "partial-workspace-run-now",
		}, http.StatusAccepted)
	trigger := decodeData[domain.ScheduledTaskRun](t, runNow)
	if trigger.RunID == "" {
		t.Fatalf("workspace run-now=%+v", trigger)
	}
}

func (f *fakeGitRelay) ListGitChanges(_ context.Context, request nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changeLimits = append(f.changeLimits, request.Limit)
	if f.changes != nil {
		return f.changes(request), nil
	}
	return nodeprotocol.GitChangesPage{}, nil
}

func (f *fakeGitRelay) ReadGitDiff(context.Context, nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.diff, nil
}

func (f *fakeGitRelay) RefreshGit(context.Context, nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	return nodeprotocol.GitRefreshResult{Refreshed: true}, nil
}

func (f *fakeGitRelay) SubscribeGitEvents(context.Context, nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	events := make(chan nodeprotocol.GitEvent)
	return events, func() { close(events) }, nil
}

func (f *fakeGitRelay) GetRunGitSnapshots(_ context.Context, runID string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotRuns = append(f.snapshotRuns, runID)
	return append([]nodeprotocol.GitRunWorkspaceSnapshot(nil), f.snapshots[runID]...), nil
}

func (f *fakeGitRelay) NotifyIssueActivation(string) {}

func TestGitHTTPUsesTenantScopedReadyResourcesAndNoRuntimeCapability(t *testing.T) {
	ginSetTestMode(t)
	repository := newTestRepository(t)
	alice := createTestIdentity(t, repository, "git-alice@example.test")
	bob := createTestIdentity(t, repository, "git-bob@example.test")
	headers := func(identity testIdentity) map[string]string {
		return map[string]string{
			"Cookie":       sessionCookieName + "=" + identity.token,
			"X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin,
		}
	}
	computer := createTestComputer(t, repository, headers(alice), "Git Mac")
	project, err := repository.CreateProject(t.Context(), alice.userID, domain.Project{
		ComputerID: computer.ID, Name: "Git Project",
	})
	if err != nil {
		t.Fatal(err)
	}
	actualPath := filepath.Join(t.TempDir(), "source")
	resource, err := repository.CreateResource(t.Context(), alice.userID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "code_directory", Name: "Code", Path: actualPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = repository.UpdateResource(t.Context(), alice.userID, resource.ID, resource.Version, func(item *domain.ProjectResource) {
		item.Status = "ready"
		item.GitDetected = true
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateResource(t.Context(), alice.userID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "code_directory", Name: "Pending", Path: filepath.Join(t.TempDir(), "pending"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nonGit, err := repository.CreateResource(t.Context(), alice.userID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "code_directory", Name: "Ready container",
		Path: filepath.Join(t.TempDir(), "ready-container"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nonGit, err = repository.UpdateResource(t.Context(), alice.userID, nonGit.ID, nonGit.Version,
		func(item *domain.ProjectResource) {
			item.Status = "ready"
			item.GitDetected = false
		})
	if err != nil {
		t.Fatal(err)
	}

	withoutRuntime := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	response := request(t, withoutRuntime, http.MethodGet,
		"/v1/projects/"+project.ID+"/git/repositories", nil, headers(alice), http.StatusNotImplemented)
	if response.Error == nil || response.Error.Code != "capability_unsupported" {
		t.Fatalf("without runtime error=%+v", response.Error)
	}

	relay := &fakeGitRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	ownerResponse := request(t, router, http.MethodGet,
		"/v1/projects/"+project.ID+"/git/repositories?refresh=true&path=/browser/must/not/win",
		nil, headers(alice), http.StatusOK)
	snapshot := decodeData[nodeprotocol.GitRepositoryList](t, ownerResponse)
	if snapshot.SnapshotID != "snapshot_test" || len(snapshot.Repositories) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if ownerResponse.Recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("repositories Cache-Control=%q", ownerResponse.Recorder.Header().Get("Cache-Control"))
	}
	if ownerResponse.Recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("repositories X-Content-Type-Options=%q",
			ownerResponse.Recorder.Header().Get("X-Content-Type-Options"))
	}
	for _, endpoint := range []string{
		"/v1/projects/" + project.ID + "/git/repositories/repo_test/changes",
		"/v1/projects/" + project.ID + "/git/repositories/repo_test/changes/change_test/diff",
	} {
		gitResponse := request(t, router, http.MethodGet, endpoint, nil, headers(alice), http.StatusOK)
		if gitResponse.Recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Cache-Control=%q", endpoint, gitResponse.Recorder.Header().Get("Cache-Control"))
		}
	}
	hugeDisplay := strings.Repeat("x", 150<<10)
	relay.mu.Lock()
	relay.changeLimits = nil
	relay.changes = func(request nodeprotocol.GitChangesRequest) nodeprotocol.GitChangesPage {
		items := make([]nodeprotocol.GitChange, request.Limit)
		for index := range items {
			items[index] = nodeprotocol.GitChange{
				ID: "change", PathID: "path", Area: "unstaged", Status: "modified", Kind: "file",
				Path:        nodeprotocol.GitDisplayPath{Token: "token", Display: hugeDisplay},
				DisplayPath: hugeDisplay, DiffAvailable: true,
			}
		}
		return nodeprotocol.GitChangesPage{
			SnapshotID: "snapshot", RepositoryID: "repo_test", Items: items,
			NextCursor: "next-page", Warnings: []nodeprotocol.GitRepositoryWarning{},
		}
	}
	relay.diff = nodeprotocol.GitDiffResult{
		SnapshotID: "snapshot", RepositoryID: "repo_test", ChangeID: "change_test",
		Path:  nodeprotocol.GitDisplayPath{Token: "token", Display: "src/main.go"},
		Patch: strings.Repeat("<>&", 8<<20),
	}
	relay.mu.Unlock()
	boundedChangesResponse := request(t, router, http.MethodGet,
		"/v1/projects/"+project.ID+"/git/repositories/repo_test/changes",
		nil, headers(alice), http.StatusOK)
	boundedChanges := decodeData[nodeprotocol.GitChangesPage](t, boundedChangesResponse)
	if !boundedChanges.Truncated || boundedChanges.NextCursor == "" || len(boundedChanges.Items) >= 200 {
		t.Fatalf("bounded changes items=%d truncated=%t cursor=%q", len(boundedChanges.Items),
			boundedChanges.Truncated, boundedChanges.NextCursor)
	}
	boundedDiffResponse := request(t, router, http.MethodGet,
		"/v1/projects/"+project.ID+"/git/repositories/repo_test/changes/change_test/diff",
		nil, headers(alice), http.StatusOK)
	boundedDiff := decodeData[nodeprotocol.GitDiffResult](t, boundedDiffResponse)
	if boundedDiff.Patch != "" || !boundedDiff.Truncated || boundedDiff.Truncation == nil ||
		boundedDiff.Truncation.Reason != "response_budget" || !boundedDiff.ConfirmRequired {
		t.Fatalf("bounded diff=%+v", boundedDiff)
	}
	relay.mu.Lock()
	limits := append([]int(nil), relay.changeLimits...)
	relay.mu.Unlock()
	if len(limits) < 2 || limits[0] != 200 || limits[len(limits)-1] >= 200 {
		t.Fatalf("change response was not re-paged: limits=%v", limits)
	}
	if bytes.Contains(ownerResponse.Recorder.Body.Bytes(), []byte(actualPath)) {
		t.Fatal("absolute resource path leaked to browser response")
	}
	relay.mu.Lock()
	seen := append([]nodeprotocol.GitRepositoriesRequest(nil), relay.requests...)
	relay.mu.Unlock()
	if len(seen) != 1 || len(seen[0].Resources) != 1 || seen[0].Resources[0].Path != actualPath ||
		seen[0].Resources[0].ResourceID != resource.ID || !seen[0].Refresh {
		t.Fatalf("runtime request=%+v", seen)
	}
	nonGit, err = repository.UpdateResource(t.Context(), alice.userID, nonGit.ID, nonGit.Version,
		func(item *domain.ProjectResource) { item.GitDetected = true })
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodGet, "/v1/projects/"+project.ID+"/git/repositories",
		nil, headers(alice), http.StatusOK)
	relay.mu.Lock()
	seen = append([]nodeprotocol.GitRepositoriesRequest(nil), relay.requests...)
	relay.mu.Unlock()
	if len(seen) != 2 || len(seen[1].Resources) != 2 {
		t.Fatalf("container false→true did not use unified resources: %+v", seen)
	}

	response = request(t, router, http.MethodGet,
		"/v1/projects/"+project.ID+"/git/repositories", nil, headers(bob), http.StatusNotFound)
	if response.Error == nil || response.Error.Code != "not_found" {
		t.Fatalf("cross-tenant error=%+v", response.Error)
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.requests) != 2 {
		t.Fatalf("cross-tenant request reached runtime: %+v", relay.requests)
	}
}

var _ Runtime = (*fakeGitRelay)(nil)
var _ GitRead = (*fakeGitRelay)(nil)
