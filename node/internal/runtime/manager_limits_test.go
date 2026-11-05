package runtime

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerBoundsInstancesAndActiveRuns(t *testing.T) {
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	defer manager.Close()
	for index := 0; index < maxManagedInstances; index++ {
		runID := fmt.Sprintf("run-instance-%d", index)
		key := InstanceKey{InstanceID: fmt.Sprintf("instance-%d", index), Generation: "generation"}
		if _, _, err := manager.Get(t.Context(), runID, key, InstanceConfig{}); err != nil {
			t.Fatalf("create bounded instance %d: %v", index, err)
		}
		manager.ReleaseRun(runID)
	}
	if _, _, err := manager.Get(t.Context(), "run-instance-overflow",
		InstanceKey{InstanceID: "instance-overflow", Generation: "generation"}, InstanceConfig{}); err == nil {
		t.Fatal("manager accepted an instance beyond its hard limit")
	}

	runManager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	runManager.RegisterTestAdapter(&fakeAdapter{})
	defer runManager.Close()
	key := InstanceKey{InstanceID: "shared-instance", Generation: "generation"}
	for index := 0; index < maxManagedRuns; index++ {
		if _, _, err := runManager.Get(t.Context(), fmt.Sprintf("run-active-%d", index), key, InstanceConfig{}); err != nil {
			t.Fatalf("register bounded active run %d: %v", index, err)
		}
	}
	if _, _, err := runManager.Get(t.Context(), "run-active-overflow", key, InstanceConfig{}); err == nil {
		t.Fatal("manager accepted an active run beyond its hard limit")
	}
}

func TestProjectWorkspaceHashesUntrustedProjectID(t *testing.T) {
	root := t.TempDir()
	runtime := &Runtime{workspaceRoot: root}
	directory, _, _, err := runtime.prepareProjectWorkspace("../../outside-project", nil)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		len(relative) < len("projects"+string(filepath.Separator)) {
		t.Fatalf("project workspace escaped root: directory=%q relative=%q err=%v", directory, relative, err)
	}
}

func TestConfigRetentionDeletesOnlyInactivePersistedDirectoriesAndNeverCredentialTargets(t *testing.T) {
	root := t.TempDir()
	manager := NewManager("fake", "fake", root, nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	manager.idleAfter = 0
	manager.retention = configRetentionPolicy{maxDirectories: 1, maxBytes: 1 << 20, maxAge: 24 * time.Hour}
	defer manager.Close()
	credentialTargets := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		key := InstanceKey{InstanceID: fmt.Sprintf("retained-%d", index), Generation: "generation"}
		runID := fmt.Sprintf("retention-run-%d", index)
		if _, _, err := manager.Get(t.Context(), runID, key, InstanceConfig{}); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "provider-credential")
		if err := os.WriteFile(target, []byte("credential-sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		credentialTargets = append(credentialTargets, target)
		if err := os.Symlink(target, filepath.Join(manager.instanceDir(key), "auth-link")); err != nil {
			t.Skipf("symlink fixture unavailable: %v", err)
		}
		if err := manager.MarkSessionPersisted(key); err != nil {
			t.Fatal(err)
		}
		manager.ReleaseRun(runID)
		manager.RecycleIdle(t.Context())
	}
	entries, err := os.ReadDir(filepath.Join(root, "instances"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("retained config directories=%d, want 1", len(entries))
	}
	for _, target := range credentialTargets {
		payload, err := os.ReadFile(target)
		if err != nil || string(payload) != "credential-sentinel" {
			t.Fatalf("credential target was followed or removed: payload=%q err=%v", payload, err)
		}
	}
}

func TestConfigRetentionProtectsActiveAndUnpersistedDirectories(t *testing.T) {
	root := t.TempDir()
	manager := NewManager("fake", "fake", root, nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	manager.idleAfter = 0
	manager.retention = configRetentionPolicy{maxDirectories: 0, maxBytes: 0, maxAge: 0}
	defer manager.Close()
	activeKey := InstanceKey{InstanceID: "active", Generation: "generation"}
	if _, _, err := manager.Get(t.Context(), "active-run", activeKey, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkSessionPersisted(activeKey); err != nil {
		t.Fatal(err)
	}
	manager.RecycleIdle(t.Context())
	if _, err := os.Stat(manager.instanceDir(activeKey)); err != nil {
		t.Fatalf("active config directory was deleted: %v", err)
	}

	unpersistedKey := InstanceKey{InstanceID: "unpersisted", Generation: "generation"}
	if _, _, err := manager.Get(t.Context(), "unpersisted-run", unpersistedKey, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	manager.ReleaseRun("unpersisted-run")
	manager.RecycleIdle(t.Context())
	if _, err := os.Stat(manager.instanceDir(unpersistedKey)); err != nil {
		t.Fatalf("unpersisted config directory was deleted: %v", err)
	}
}
