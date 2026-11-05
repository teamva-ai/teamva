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

func makeSessionRetentionFixture(t *testing.T, manager *Manager, index int, updatedAt time.Time, configBytes int64) (InstanceKey, string) {
	t.Helper()
	key := InstanceKey{
		InstanceID: fmt.Sprintf("session-%d", index), Generation: "generation",
		ProjectID: "project", GitScopeHash: "authorized-resources",
	}
	path := manager.instanceDir(key)
	if err := os.MkdirAll(filepath.Join(path, instanceDataDirectoryName, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Sparse config fixtures exercise the real default byte budget without
	// allocating hundreds of MiB of RAM or writing that many physical bytes.
	file, err := os.Create(filepath.Join(path, "opencode", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(configBytes); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, instanceDataDirectoryName, "opencode", "session.db"), []byte(fmt.Sprintf("native-session-%d", index)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceManifest(path, instanceManifest{
		Version: 1, Provider: manager.provider, SessionKey: generationSessionKey(key),
		UpdatedAt: updatedAt, SessionPersisted: true, ConfigOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	return key, path
}

func assertRetainedNativeSession(t *testing.T, path string, index int) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(path, instanceDataDirectoryName, "opencode", "session.db"))
	if err != nil || string(payload) != fmt.Sprintf("native-session-%d", index) {
		t.Fatalf("native session was pruned: path=%s payload=%q err=%v", path, payload, err)
	}
	if _, err := readInstanceManifest(path); err != nil {
		t.Fatalf("session binding manifest was pruned: %v", err)
	}
}

func TestConfigRetentionDefaultAgeKeepsNativeDataAndRecreatesSameScopedInstance(t *testing.T) {
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	defer manager.Close()
	key, path := makeSessionRetentionFixture(t, manager, 1, time.Now().Add(-31*24*time.Hour), 4096)
	if err := manager.pruneConfigDirs(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRetainedNativeSession(t, path, 1)
	if _, err := os.Stat(filepath.Join(path, "opencode")); !os.IsNotExist(err) {
		t.Fatalf("30-day config was not pruned: %v", err)
	}
	if dataOnly, err := instanceHasOnlySessionData(t.Context(), path); err != nil || !dataOnly {
		t.Fatalf("pruned session home consumes a config slot: dataOnly=%t err=%v", dataOnly, err)
	}
	if _, _, err := manager.Get(t.Context(), "recreated-run", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if recreated := manager.instanceDir(key); recreated != path {
		t.Fatalf("native data moved on recreation: got=%s want=%s", recreated, path)
	}
	assertRetainedNativeSession(t, path, 1)
	for _, changedKey := range []InstanceKey{
		{InstanceID: key.InstanceID, Generation: key.Generation, ProjectID: "other", GitScopeHash: key.GitScopeHash},
		{InstanceID: key.InstanceID, Generation: key.Generation, ProjectID: key.ProjectID, GitScopeHash: "different-resources"},
	} {
		if manager.instanceDir(changedKey) == path {
			t.Fatalf("native data escaped project/resource binding: %+v", changedKey)
		}
	}
	manager.ReleaseRun("recreated-run")
}

func TestConfigRetentionDefaultDirectoryBudgetPrunesConfigNotNativeSessions(t *testing.T) {
	manager := NewManager("opencode", "", t.TempDir(), nil, log.New(io.Discard, "", 0))
	defer manager.Close()
	now := time.Now().UTC()
	paths := make([]string, 0, maxRetainedConfigDirs+2)
	for index := 0; index < maxRetainedConfigDirs+2; index++ {
		_, path := makeSessionRetentionFixture(t, manager, index, now.Add(time.Duration(index)*time.Nanosecond), 4096)
		paths = append(paths, path)
	}
	for pass := 0; pass < 2; pass++ {
		if err := manager.pruneConfigDirs(t.Context()); err != nil {
			t.Fatal(err)
		}
		configs := 0
		for index, path := range paths {
			assertRetainedNativeSession(t, path, index)
			if _, err := os.Stat(filepath.Join(path, "opencode", "config.json")); err == nil {
				configs++
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if configs != maxRetainedConfigDirs {
			t.Fatalf("retained config slots=%d, want %d (pass %d)", configs, maxRetainedConfigDirs, pass)
		}
	}
}

func TestConfigRetentionDefaultByteBudgetExcludesAndProtectsNativeDatabases(t *testing.T) {
	manager := NewManager("opencode", "", t.TempDir(), nil, log.New(io.Discard, "", 0))
	defer manager.Close()
	now := time.Now().UTC()
	paths := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		_, path := makeSessionRetentionFixture(t, manager, index, now.Add(time.Duration(index)*time.Second), 300<<20)
		paths = append(paths, path)
		// A native database larger than the entire config budget must neither
		// force config eviction by itself nor become part of a deletion walk.
		file, err := os.Create(filepath.Join(path, instanceDataDirectoryName, "opencode", "large-native.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxRetainedConfigBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if bytes, err := boundedConfigTreeBytes(t.Context(), path); err != nil || bytes > 301<<20 {
			t.Fatalf("native database counted as config: bytes=%d err=%v", bytes, err)
		}
	}
	if err := manager.pruneConfigDirs(t.Context()); err != nil {
		t.Fatal(err)
	}
	configs := 0
	var configBytes int64
	for index, path := range paths {
		assertRetainedNativeSession(t, path, index)
		info, err := os.Stat(filepath.Join(path, instanceDataDirectoryName, "opencode", "large-native.db"))
		if err != nil || info.Size() != maxRetainedConfigBytes+1 {
			t.Fatalf("native database changed: info=%v err=%v", info, err)
		}
		if _, err := os.Stat(filepath.Join(path, "opencode", "config.json")); err == nil {
			configs++
			bytes, err := boundedConfigTreeBytes(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			configBytes += bytes
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if configs != 1 || configBytes > maxRetainedConfigBytes {
		t.Fatalf("512 MiB config budget not enforced: configs=%d bytes=%d", configs, configBytes)
	}
}

func TestConfigRetentionPreservesDataSymlinkAndDoesNotFollowConfigSymlink(t *testing.T) {
	manager := NewManager("fake", "fake", t.TempDir(), nil, log.New(io.Discard, "", 0))
	defer manager.Close()
	key, path := makeSessionRetentionFixture(t, manager, 1, time.Now().Add(-31*24*time.Hour), 4096)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(path, instanceDataDirectoryName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, instanceDataDirectoryName)); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "config-link")); err != nil {
		t.Fatal(err)
	}
	if err := manager.pruneConfigDirs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(manager.instanceDir(key), instanceDataDirectoryName)); err != nil {
		t.Fatalf("Provider data symlink was deleted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "config-link")); !os.IsNotExist(err) {
		t.Fatalf("config link was not pruned: %v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(payload) != "keep" {
		t.Fatalf("config pruning followed symlink target: payload=%q err=%v", payload, err)
	}
}
