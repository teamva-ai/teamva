package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Both Providers remain active while B is prepared. They deliberately share
// instance/generation/project, changing only the registered resource set.
func TestRuntimeActiveWorkspaceScopesKeepManifestAndNativeDataIsolated(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-workspace-scopes", store, nil, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer func() {
		cancel()
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	}()
	resources := make([][]nodeprotocol.GitResourceRequest, 2)
	roots := make([]string, 2)
	repositoryIDs := make([]string, 2)
	repository := t.TempDir()
	initRuntimeGitRepository(t, repository)
	for index := range resources {
		root := filepath.Join(repository, fmt.Sprintf("scope-%d", index))
		writeRetentionFile(t, filepath.Join(root, "observed.txt"), fmt.Sprintf("scope %d data", index))
		roots[index], err = filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		resources[index] = []nodeprotocol.GitResourceRequest{{
			ResourceID: fmt.Sprintf("resource-%d", index), Name: fmt.Sprintf("Code %d", index), Path: root,
		}}
		approveRuntimeGitResources(t, engine, resources[index])
		list, err := engine.ListGitRepositories(ctx, nodeprotocol.GitRepositoriesRequest{
			ProjectID: "project-workspace-scopes", Resources: resources[index],
		})
		if err != nil || len(list.Repositories) != 1 {
			t.Fatalf("repository %d: list=%+v err=%v", index, list, err)
		}
		repositoryIDs[index] = list.Repositories[0].ID
	}
	if repositoryIDs[0] != repositoryIDs[1] {
		t.Fatal("fixture must change the authorization scope, not the repository identity")
	}
	mcp := map[string]json.RawMessage{"fixture": json.RawMessage(`{"type":"remote","url":"https://mcp.example.invalid"}`)}
	engine.SetMCPResolver(func(_ context.Context, projectID string) (map[string]json.RawMessage, error) {
		if projectID != "project-workspace-scopes" {
			return nil, fmt.Errorf("unexpected project %q", projectID)
		}
		return mcp, nil
	})
	type observation struct {
		request  adapter.RunRequest
		manifest []byte
		err      error
	}
	observe := func(request adapter.RunRequest) observation {
		manifest, err := os.ReadFile(filepath.Join(request.CWD, "AGENTS.md"))
		return observation{request: request, manifest: manifest, err: err}
	}
	enteredA, enteredB, recheckedA := make(chan observation, 1), make(chan observation, 1), make(chan observation, 1)
	recheckA, releaseA, releaseB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	providerA := &fakeAdapter{nativeSessionID: "native-scope-a"}
	providerB := &fakeAdapter{nativeSessionID: "native-scope-b"}
	providerA.onStart = func(request adapter.RunRequest) {
		enteredA <- observe(request)
		select {
		case <-recheckA:
			recheckedA <- observe(request)
		case <-ctx.Done():
			return
		}
		select {
		case <-releaseA:
		case <-ctx.Done():
		}
	}
	providerB.onStart = func(request adapter.RunRequest) {
		enteredB <- observe(request)
		select {
		case <-releaseB:
		case <-ctx.Done():
		}
	}
	command := func(runID, hint string, resources []nodeprotocol.GitResourceRequest) nodeprotocol.CommandEnvelope {
		payload, err := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: runID, ProjectID: "project-workspace-scopes", IssueID: "issue-workspace-scopes",
			IssueAgentInstanceID: "shared-instance", IssueAgentGenerationID: "shared-generation",
			LeaseEpoch: 1, Provider: "fake", Prompt: "work", SystemPrompt: "scope instructions",
			GitResources: resources, NativeSessionID: hint, WorktreeHint: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		return nodeprotocol.CommandEnvelope{
			ProtocolVersion: nodeprotocol.Version, MessageID: "command-" + runID, Name: "run.start",
			ComputerID: "cmp-workspace-scopes", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
		}
	}
	start := func(command nodeprotocol.CommandEnvelope) <-chan error {
		done := make(chan error, 1)
		go func() {
			record, replayed, err := engine.Handle(ctx, command)
			if err == nil && (replayed || record.Status != "completed") {
				err = fmt.Errorf("record=%+v replayed=%t", record, replayed)
			}
			done <- err
		}()
		return done
	}
	awaitObservation := func(channel <-chan observation) observation {
		t.Helper()
		select {
		case observed := <-channel:
			if observed.err != nil {
				t.Fatal(observed.err)
			}
			return observed
		case <-time.After(15 * time.Second):
			t.Fatal("Provider did not reach its manifest read")
			return observation{}
		}
	}
	awaitRun := func(done <-chan error) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("Run did not complete")
		}
	}
	engine.RegisterTestAdapter("fake", providerA)
	doneA := start(command("run-workspace-a", "", resources[0]))
	beforeA := awaitObservation(enteredA)
	manager := engine.managers["fake"]
	manager.mu.Lock()
	keyA := manager.runs["run-workspace-a"]
	instanceA := manager.instances[keyA]
	digestA := instanceA.configDigest
	manager.mu.Unlock()
	dataA := manager.instanceDataDir(keyA)
	writeRetentionFile(t, filepath.Join(dataA, "native-sentinel"), "scope A native data")
	writeRetentionFile(t, filepath.Join(beforeA.request.CWD, "workspace-sentinel"), "scope A files")

	engine.RegisterTestAdapter("fake", providerB)
	// Even an explicit native A hint must become a fresh native B session.
	doneB := start(command("run-workspace-b", "native-scope-a", resources[1]))
	observedB := awaitObservation(enteredB)
	close(recheckA)
	afterA := awaitObservation(recheckedA)
	if !bytes.Equal(beforeA.manifest, afterA.manifest) {
		t.Fatal("preparing scope B changed the active scope A repository map")
	}
	if beforeA.request.CWD == observedB.request.CWD || observedB.request.NativeSessionID != "" {
		t.Fatal("scope B reused scope A's CWD or native session")
	}
	if !reflect.DeepEqual(workspaceManifestRoots(t, beforeA.manifest), map[string][]string{repositoryIDs[0]: {roots[0]}}) ||
		!reflect.DeepEqual(workspaceManifestRoots(t, observedB.manifest), map[string][]string{repositoryIDs[1]: {roots[1]}}) {
		t.Fatal("scope-private manifest did not retain exact repository authorization")
	}
	manager.mu.Lock()
	keyB := manager.runs["run-workspace-b"]
	instanceB := manager.instances[keyB]
	digestB := instanceB.configDigest
	unchangedA := instanceA.configDigest == digestA && instanceA.activeRuns == 1
	bothActive := len(manager.runs) == 2 && instanceB.activeRuns == 1
	manager.mu.Unlock()
	if !unchangedA || !bothActive || keyA.GitScopeHash == keyB.GitScopeHash {
		t.Fatal("parallel scopes changed A's config or did not create independent active instances")
	}
	for index, observed := range []observation{beforeA, observedB} {
		want, err := instanceConfigDigest(InstanceConfig{
			SystemPrompt: "scope instructions", WritableRepositories: []string{roots[index]},
			WorkspaceInstructions: filepath.Join(observed.request.CWD, "AGENTS.md"), WorktreeHint: true, MCPServers: mcp,
		})
		if err != nil || want != [][32]byte{digestA, digestB}[index] {
			t.Fatalf("scope %d config snapshot widened roots, lost MCP or changed worktree hint: %v", index, err)
		}
	}
	dataB := manager.instanceDataDir(keyB)
	if dataA == dataB || manager.instanceDir(keyA) == manager.instanceDir(keyB) {
		t.Fatal("distinct scopes shared Provider config/native homes")
	}
	if _, err := os.Stat(filepath.Join(dataB, "native-sentinel")); !os.IsNotExist(err) {
		t.Fatal("scope B could see scope A's native data")
	}
	writeRetentionFile(t, filepath.Join(dataB, "native-sentinel"), "scope B native data")
	close(releaseA)
	close(releaseB)
	awaitRun(doneA)
	awaitRun(doneB)
	for index, key := range []InstanceKey{keyA, keyB} {
		session, found, err := engine.loadGenerationSession(ctx, key, "fake")
		if err != nil || !found || session != []string{"native-scope-a", "native-scope-b"}[index] {
			t.Fatalf("scope %d generation session=%q found=%t err=%v", index, session, found, err)
		}
		snapshots, err := engine.GetRunGitSnapshots(ctx, []string{"run-workspace-a", "run-workspace-b"}[index])
		if err != nil || len(snapshots) != 2 {
			t.Fatalf("scope %d snapshots=%+v err=%v", index, snapshots, err)
		}
		for _, snapshot := range snapshots {
			if snapshot.RepositoryID != repositoryIDs[index] {
				t.Fatal("Run snapshot was attributed to the other authorization scope")
			}
			// Git observations are bounded/best-effort. Concurrent end captures
			// under different resource sets can fail; an explicit metadata-only
			// failure must never look clean or include the other scope's data.
			if snapshot.Phase == "end" && snapshot.Truncated && len(snapshot.Warnings) == 1 &&
				snapshot.Warnings[0].Code == nodeprotocol.GitWarningSnapshotCaptureFailed {
				if len(snapshot.Untracked) != 0 {
					t.Fatal("failed snapshot retained files from another scope")
				}
				for _, area := range snapshot.Areas {
					if len(area.Paths) != 0 || area.Patch != "" {
						t.Fatal("failed snapshot retained patch data from another scope")
					}
				}
				continue
			}
			if len(snapshot.Untracked) != 1 || snapshot.Untracked[0].Path != fmt.Sprintf("scope-%d/observed.txt", index) {
				t.Fatalf("scope %d snapshot included another scope's files: %+v", index, snapshot)
			}
		}
	}
	providerA.mu.Lock()
	providerA.onStart = nil
	providerA.mu.Unlock()
	// An unrelated B hint cannot override A's persisted session on return to A.
	awaitRun(start(command("run-workspace-a-return", "native-scope-b", resources[0])))
	providerA.mu.Lock()
	cwdA, startsA, resumesA := providerA.lastCWD, providerA.start, providerA.resume
	providerA.mu.Unlock()
	providerB.mu.Lock()
	startsB, resumesB := providerB.start, providerB.resume
	providerB.mu.Unlock()
	if cwdA != beforeA.request.CWD || startsA != 1 || resumesA != 1 || startsB != 1 || resumesB != 0 {
		t.Fatalf("A/B/A reuse: cwd=%q startsA=%d resumesA=%d startsB=%d resumesB=%d", cwdA, startsA, resumesA, startsB, resumesB)
	}
	for path, want := range map[string]string{
		filepath.Join(dataA, "native-sentinel"):   "scope A native data",
		filepath.Join(dataB, "native-sentinel"):   "scope B native data",
		filepath.Join(cwdA, "workspace-sentinel"): "scope A files",
	} {
		if payload, err := os.ReadFile(path); err != nil || string(payload) != want {
			t.Fatalf("scope switch lost retained files: %v", err)
		}
	}
}

func TestProjectWorkspaceScopeKeepsDisjointResourceRootsExact(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-disjoint-workspace", store, nil, t.TempDir(), nil)
	defer engine.Close()
	repository := t.TempDir()
	initRuntimeGitRepository(t, repository)
	rootA, rootC := filepath.Join(repository, "a"), filepath.Join(repository, "c")
	for _, path := range []string{rootA, filepath.Join(repository, "b"), rootC} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	resources := []nodeprotocol.GitResourceRequest{
		{ResourceID: "resource-a", Name: "A", Path: rootA},
		{ResourceID: "resource-c", Name: "C", Path: rootC},
	}
	approveRuntimeGitResources(t, engine, resources)
	_, repositories, err := engine.resolveProjectWorkspaceRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-disjoint", Resources: resources,
	})
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	cwd, roots, scopeHash, err := engine.prepareProjectWorkspace("project-disjoint", repositories)
	if err != nil {
		t.Fatal(err)
	}
	canonicalA, _ := filepath.EvalSymlinks(rootA)
	canonicalC, _ := filepath.EvalSymlinks(rootC)
	want := []string{canonicalA, canonicalC}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("disjoint authorization widened: roots=%q want=%q", roots, want)
	}
	manifest, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(workspaceManifestRoots(t, manifest), map[string][]string{repositories[0].ID: want}) {
		t.Fatal("repository map promoted disjoint writable roots to their common parent")
	}
	// Input ordering is not an authorization change; resolved repositories and
	// roots remain canonical, preserving the CWD/native scope on subsequent runs.
	resources[0], resources[1] = resources[1], resources[0]
	_, again, err := engine.resolveProjectWorkspaceRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: "project-disjoint", Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nextCWD, nextRoots, nextHash, err := engine.prepareProjectWorkspace("project-disjoint", again); err != nil || cwd != nextCWD || scopeHash != nextHash || !reflect.DeepEqual(nextRoots, want) {
		t.Fatalf("unchanged resource set lost stable scoped CWD: %v", err)
	}
}

func TestProjectWorkspaceNewCWDContractRetainsLegacyFilesAndRejectsLegacySession(t *testing.T) {
	store, err := localstore.Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-legacy-workspace", store, nil, t.TempDir(), nil)
	defer engine.Close()
	provider := &fakeAdapter{}
	engine.RegisterTestAdapter("fake", provider)
	legacyCWD, err := engine.projectWorkspaceBase("project-legacy")
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the previous length-delimited key, without changing any schema
	// or migrating/removing the previous workspace/native data.
	legacyHash := keyDirectorySegment(fmt.Sprintf("project-workspace:%d:%s", len(legacyCWD), legacyCWD))
	legacyKey := InstanceKey{InstanceID: "instance-legacy", Generation: "generation-legacy", ProjectID: "project-legacy", GitScopeHash: legacyHash}
	manager := engine.managers["fake"]
	legacyHome, err := manager.prepareInstanceDataDir(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	writeRetentionFile(t, filepath.Join(legacyCWD, "AGENTS.md"), "legacy repository map")
	writeRetentionFile(t, filepath.Join(legacyHome, "native-sentinel"), "legacy native data")
	if err := engine.saveGenerationSession(t.Context(), legacyKey, "fake", "native_1"); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(nodeprotocol.StartRunPayload{
		RunID: "run-legacy-workspace", ProjectID: legacyKey.ProjectID,
		IssueAgentInstanceID: legacyKey.InstanceID, IssueAgentGenerationID: legacyKey.Generation,
		LeaseEpoch: 1, Provider: "fake", Prompt: "work", NativeSessionID: "native_1", DisableTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := engine.Handle(t.Context(), nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: "command-legacy-workspace", Name: "run.start",
		ComputerID: "cmp-legacy-workspace", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	starts, resumes, cwd := provider.start, provider.resume, provider.lastCWD
	provider.mu.Unlock()
	if starts != 1 || resumes != 0 || cwd == legacyCWD || filepath.Dir(cwd) != filepath.Dir(legacyCWD) {
		t.Fatal("new scope CWD contract resumed a legacy project-wide native session")
	}
	for path, want := range map[string]string{
		filepath.Join(legacyCWD, "AGENTS.md"):        "legacy repository map",
		filepath.Join(legacyHome, "native-sentinel"): "legacy native data",
	} {
		if payload, err := os.ReadFile(path); err != nil || string(payload) != want {
			t.Fatalf("legacy files were removed or overwritten: %v", err)
		}
	}
}

func workspaceManifestRoots(t *testing.T, manifest []byte) map[string][]string {
	t.Helper()
	start := bytes.IndexByte(manifest, '[')
	if start == -1 {
		t.Fatal("manifest has no repository JSON")
	}
	var repositories []struct {
		ID            string   `json:"id"`
		WritableRoots []string `json:"writable_roots"`
	}
	if err := json.Unmarshal(manifest[start:], &repositories); err != nil {
		t.Fatal(err)
	}
	roots := make(map[string][]string, len(repositories))
	for _, repository := range repositories {
		roots[repository.ID] = repository.WritableRoots
	}
	return roots
}
