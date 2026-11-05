package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Explicit test-fixture approvals only. Never call this from production Run
// preparation, discovery, recovery, or a loop over control-plane ready resources.
func approveRuntimeGitResources(t *testing.T, engine *Runtime, resources []nodeprotocol.GitResourceRequest) {
	t.Helper()
	for _, resource := range resources {
		if err := engine.gitReader.ApproveResource(t.Context(), resource.ResourceID, resource.Path, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func resourceValidationCommand(t *testing.T, resource nodeprotocol.GitResourceRequest, version int64) nodeprotocol.CommandEnvelope {
	t.Helper()
	payload, err := json.Marshal(nodeprotocol.ValidateResourcePayload{
		ResourceID: resource.ResourceID, Path: resource.Path, ValidationVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return nodeprotocol.CommandEnvelope{
		ProtocolVersion: nodeprotocol.Version, MessageID: fmt.Sprintf("validate-%s-%d", resource.ResourceID, version),
		Name: "resource.validate", ComputerID: "cmp-resource-approvals", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
	}
}

func validateRuntimeResource(t *testing.T, engine *Runtime, resource nodeprotocol.GitResourceRequest, version int64) {
	t.Helper()
	record, replayed, err := engine.Handle(t.Context(), resourceValidationCommand(t, resource, version))
	if err != nil || replayed || record.Status != "completed" {
		t.Fatalf("explicit resource validation failed: status=%s replayed=%t err=%v", record.Status, replayed, err)
	}
	var result nodeprotocol.ValidateResourceResult
	if err := json.Unmarshal(record.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid || !result.GitDetected || result.ValidationVersion != version || result.ResourceID != resource.ResourceID {
		t.Fatal("explicit resource validation did not retain its ID/version and real Git detection")
	}
}

func TestRuntimeMissingApprovalAfterRestartRequiresExplicitResourceValidation(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "runtime.db")
	workspace := filepath.Join(base, "workspaces")
	directory := filepath.Join(base, "repository")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, directory)
	resource := nodeprotocol.GitResourceRequest{ResourceID: "ready-resource", Name: "Code", Path: directory}
	request := nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}
	open := func() (*localstore.Store, *Runtime) {
		t.Helper()
		store, err := localstore.Open(t.Context(), databasePath)
		if err != nil {
			t.Fatal(err)
		}
		engine := New("cmp-resource-approvals", store, nil, workspace, nil)
		return store, engine
	}
	store, first := open()
	// Model the control plane's previously ready Resource with no private anchor.
	if _, err := first.ListGitRepositories(t.Context(), request); err == nil {
		t.Fatal("a ready Resource without a durable approval was automatically trusted")
	}
	if _, err := os.Lstat(store.ResourceAuthorizationDirectory()); !os.IsNotExist(err) {
		t.Fatal("resource discovery created an approval or silently backfilled state")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, second := open()
	if _, err := second.ListGitRepositories(t.Context(), request); err == nil {
		t.Fatal("restarting implicitly approved the current Resource path")
	}
	fake := &fakeAdapter{}
	second.RegisterTestAdapter("fake", fake)
	runCommand := func(id string) nodeprotocol.CommandEnvelope {
		payload, err := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: id, ProjectID: request.ProjectID, GitResources: request.Resources,
			IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation", LeaseEpoch: 1,
			Provider: "fake", Prompt: "work", DisableTools: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		return nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command-" + id,
			Name: "run.start", ComputerID: "cmp-resource-approvals", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload}
	}
	if _, _, err := second.Handle(t.Context(), runCommand("unapproved-run")); errorCode(err) != "path_out_of_scope" {
		t.Fatalf("unapproved Run start did not fail closed: %v", err)
	}
	fake.mu.Lock()
	starts := fake.start
	fake.mu.Unlock()
	if starts != 0 {
		t.Fatal("unapproved Resource reached a Provider")
	}
	validateRuntimeResource(t, second, resource, 7)
	list, err := second.ListGitRepositories(t.Context(), request)
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("explicit validation did not restore Resource access: %v", err)
	}
	if _, _, err := second.Handle(t.Context(), runCommand("approved-run")); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, third := open()
	defer store.Close()
	defer third.Close()
	if restored, err := third.ListGitRepositories(t.Context(), request); err != nil || len(restored.Repositories) != 1 || restored.Repositories[0].ID != list.Repositories[0].ID {
		t.Fatalf("explicit approval was not durable across Runtime/Store restart: %v", err)
	}
}

func TestRuntimeResourceValidationNeverApprovesResolvedReplacementSymlink(t *testing.T) {
	base := t.TempDir()
	store, err := localstore.Open(t.Context(), filepath.Join(base, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("cmp-resource-approvals", store, nil, filepath.Join(base, "workspaces"), nil)
	defer engine.Close()
	directory, external := filepath.Join(base, "approved"), filepath.Join(base, "external")
	for _, path := range []string{directory, external} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		initRuntimeGitRepository(t, path)
	}
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	validateRuntimeResource(t, engine, resource, 1)
	entries, err := os.ReadDir(store.ResourceAuthorizationDirectory())
	if err != nil {
		t.Fatal(err)
	}
	var anchorPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			anchorPath = filepath.Join(store.ResourceAuthorizationDirectory(), entry.Name())
		}
	}
	before, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, directory); err != nil {
		t.Fatal(err)
	}
	record, _, err := engine.Handle(t.Context(), resourceValidationCommand(t, resource, 2))
	if errorCode(err) != "resource_unavailable" || record.Status != "failed" || record.ErrorCode != "resource_unavailable" {
		t.Fatalf("replacement symlink validation did not visibly fail: status=%s code=%s err=%v", record.Status, record.ErrorCode, err)
	}
	var failedResult nodeprotocol.ValidateResourceResult
	if decodeErr := json.Unmarshal(record.Result, &failedResult); decodeErr != nil || failedResult.Valid || failedResult.GitDetected {
		t.Fatal("failed validation persisted a successful resource result")
	}
	if strings.Contains(err.Error(), base) || strings.Contains(err.Error(), external) || bytes.Contains(record.Result, []byte(base)) {
		t.Fatal("resource validation failure exposed local filesystem details")
	}
	after, err := os.ReadFile(anchorPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("resource.validate updated the approval to ResolveResource's replacement target")
	}
	if _, err := engine.ListGitRepositories(t.Context(), nodeprotocol.GitRepositoriesRequest{ProjectID: "project", Resources: []nodeprotocol.GitResourceRequest{resource}}); err == nil {
		t.Fatal("replacement path remained usable after failed validation")
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, directory)
	// A fresh real directory at the same path is still not the old approval.
	command := resourceValidationCommand(t, resource, 1)
	command.MessageID += "-new-inode"
	if _, _, err := engine.Handle(t.Context(), command); errorCode(err) != "resource_unavailable" {
		t.Fatalf("same approval version accepted a new directory inode: %v", err)
	}
	validateRuntimeResource(t, engine, resource, 3)
}

func TestRuntimeResourceValidationFailsWhenGitReaderOrApprovalStateUnavailable(t *testing.T) {
	for _, unavailable := range []string{"nil_reader", "closed_reader", "state_not_directory"} {
		t.Run(unavailable, func(t *testing.T) {
			base := t.TempDir()
			store, err := localstore.Open(t.Context(), filepath.Join(base, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			engine := New("cmp-resource-approvals", store, nil, filepath.Join(base, "workspaces"), nil)
			defer engine.Close()
			directory := filepath.Join(base, "repository")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			initRuntimeGitRepository(t, directory)
			switch unavailable {
			case "nil_reader", "closed_reader":
				if err := engine.gitReader.Close(); err != nil {
					t.Fatal(err)
				}
				if unavailable == "nil_reader" {
					engine.gitReader = nil
				}
			case "state_not_directory":
				if err := os.WriteFile(store.ResourceAuthorizationDirectory(), []byte("do not replace"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
			record, _, err := engine.Handle(t.Context(), resourceValidationCommand(t, resource, 1))
			if errorCode(err) != "resource_unavailable" || record.Status != "failed" || record.ErrorCode != "resource_unavailable" {
				t.Fatalf("unavailable authorization incorrectly returned valid=true: status=%s code=%s err=%v", record.Status, record.ErrorCode, err)
			}
			var failedResult nodeprotocol.ValidateResourceResult
			if decodeErr := json.Unmarshal(record.Result, &failedResult); decodeErr != nil || failedResult.Valid || failedResult.GitDetected {
				t.Fatal("unavailable validation persisted a successful resource result")
			}
		})
	}
}

func TestRuntimeRevalidatedResourceAfterRestartCannotResumeOldNativeSession(t *testing.T) {
	base := t.TempDir()
	databasePath, workspace := filepath.Join(base, "runtime.db"), filepath.Join(base, "workspaces")
	directory := filepath.Join(base, "repository")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, directory)
	resource := nodeprotocol.GitResourceRequest{ResourceID: "resource", Name: "Code", Path: directory}
	open := func() (*localstore.Store, *Runtime) {
		t.Helper()
		store, err := localstore.Open(t.Context(), databasePath)
		if err != nil {
			t.Fatal(err)
		}
		return store, New("cmp-resource-approvals", store, nil, workspace, nil)
	}
	command := func(id, hint string) nodeprotocol.CommandEnvelope {
		payload, err := json.Marshal(nodeprotocol.StartRunPayload{
			RunID: id, ProjectID: "project", GitResources: []nodeprotocol.GitResourceRequest{resource},
			IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation", LeaseEpoch: 1,
			Provider: "fake", Prompt: "work", NativeSessionID: hint, DisableTools: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		return nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command-" + id,
			Name: "run.start", ComputerID: "cmp-resource-approvals", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload}
	}
	store, first := open()
	before := &fakeAdapter{nativeSessionID: "native-before-approval"}
	first.RegisterTestAdapter("fake", before)
	validateRuntimeResource(t, first, resource, 1)
	if _, _, err := first.Handle(t.Context(), command("before-restart", "")); err != nil {
		t.Fatal(err)
	}
	before.mu.Lock()
	oldCWD := before.lastCWD
	before.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	initRuntimeGitRepository(t, directory)
	store, second := open()
	defer store.Close()
	defer second.Close()
	after := &fakeAdapter{nativeSessionID: "native-after-approval"}
	second.RegisterTestAdapter("fake", after)
	if _, _, err := second.Handle(t.Context(), command("stale-approval", "native-before-approval")); errorCode(err) != "path_out_of_scope" {
		t.Fatalf("new inode inherited an old Resource approval after restart: %v", err)
	}
	validateRuntimeResource(t, second, resource, 2)
	if _, _, err := second.Handle(t.Context(), command("revalidated-run", "native-before-approval")); err != nil {
		t.Fatal(err)
	}
	after.mu.Lock()
	starts, resumes, newCWD := after.start, after.resume, after.lastCWD
	after.mu.Unlock()
	if starts != 1 || resumes != 0 || newCWD == oldCWD {
		t.Fatal("explicit revalidation transferred old native-session authorization/CWD to a new directory identity")
	}
}
