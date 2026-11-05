package runtime

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateRetentionHomes(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, filepath.Join(home, name))
	}
}

func writeRetentionFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createNativeDatabaseFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE native_fixture (session_id TEXT NOT NULL); INSERT INTO native_fixture VALUES ('native-session')`); err != nil {
		t.Fatal(err)
	}
}

func assertNativeDatabaseFixture(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("native database was pruned: %v", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var session string
	if err := database.QueryRow(`SELECT session_id FROM native_fixture`).Scan(&session); err != nil || session != "native-session" {
		t.Fatalf("native database contents changed: %v", err)
	}
}

func TestNativeProviderDataSurvivesDefaultConfigQuotasAndSameKeyRebuild(t *testing.T) {
	for _, native := range []struct {
		provider, sessionFile, databaseFile, configFile string
	}{
		{provider: "codex", sessionFile: "sessions/2026/10/20/native.jsonl", databaseFile: "state_5.sqlite", configFile: "config.toml"},
		{provider: "opencode", sessionFile: "opencode/storage/session/native.json", databaseFile: "opencode/opencode.db", configFile: "opencode/opencode.json"},
	} {
		t.Run(native.provider, func(t *testing.T) {
			for _, quota := range []string{"30-days", "128-configs", "512-MiB"} {
				t.Run(quota, func(t *testing.T) {
					isolateRetentionHomes(t)
					manager := NewManager(native.provider, "unused-native-cli", t.TempDir(), nil, log.New(io.Discard, "", 0))
					defer manager.Close()
					key := InstanceKey{InstanceID: "native-instance", Generation: "generation", ProjectID: "project", GitScopeHash: "resource-scope"}
					configDir, dataDir := manager.instanceDir(key), manager.instanceDataDir(key)
					if err := os.MkdirAll(configDir, 0o700); err != nil {
						t.Fatal(err)
					}
					// Use the real factory and filesystem, but never start a native CLI
					// or create a model session. Native files below are local fixtures.
					item, err := manager.newAdapter(key, configDir, InstanceConfig{SystemPrompt: "before retention"})
					if err != nil {
						t.Fatal(err)
					}
					if err := item.Close(); err != nil {
						t.Fatal(err)
					}
					sessionPath := filepath.Join(dataDir, filepath.FromSlash(native.sessionFile))
					databasePath := filepath.Join(dataDir, filepath.FromSlash(native.databaseFile))
					writeRetentionFile(t, sessionPath, "native session transcript fixture")
					createNativeDatabaseFixture(t, databasePath)
					now := time.Now().UTC()
					updatedAt := now.Add(-time.Hour)
					if quota == "30-days" {
						updatedAt = now.Add(-31 * 24 * time.Hour)
					}
					if err := writeInstanceManifest(configDir, instanceManifest{
						Version: 1, Provider: native.provider, SessionKey: generationSessionKey(key),
						UpdatedAt: updatedAt, SessionPersisted: true, ConfigOnly: true,
					}); err != nil {
						t.Fatal(err)
					}
					if quota == "128-configs" {
						for index := 0; index < maxRetainedConfigDirs; index++ {
							other := key
							other.InstanceID = fmt.Sprintf("newer-config-%d", index)
							path := manager.instanceDir(other)
							writeRetentionFile(t, filepath.Join(path, "config-fixture"), "rebuildable")
							if err := writeInstanceManifest(path, instanceManifest{
								Version: 1, Provider: native.provider, SessionKey: generationSessionKey(other),
								UpdatedAt: now, SessionPersisted: true, ConfigOnly: true,
							}); err != nil {
								t.Fatal(err)
							}
						}
					}
					if quota == "512-MiB" {
						file, err := os.Create(filepath.Join(configDir, "oversized-rebuildable-cache"))
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
					}
					if err := manager.pruneConfigDirs(t.Context()); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(configDir); !os.IsNotExist(err) {
						t.Fatalf("%s did not remove rebuildable instance config: %v", quota, err)
					}
					assertNativeDatabaseFixture(t, databasePath)
					if session, err := os.ReadFile(sessionPath); err != nil || string(session) != "native session transcript fixture" {
						t.Fatal("native session transcript was pruned")
					}
					repository := t.TempDir()
					if _, _, err := manager.Get(t.Context(), "rebuilt-run", key, InstanceConfig{
						SystemPrompt: "after retention", WritableRepositories: []string{repository}, ComputerUse: true,
					}); err != nil {
						t.Fatal(err)
					}
					defer manager.ReleaseRun("rebuilt-run")
					if manager.instanceDataDir(key) != dataDir {
						t.Fatal("same-key rebuild changed the native data home")
					}
					assertNativeDatabaseFixture(t, databasePath)
					if session, err := os.ReadFile(sessionPath); err != nil || string(session) != "native session transcript fixture" {
						t.Fatal("same-key rebuild overwrote native sessions")
					}
					configPath := filepath.Join(configDir, filepath.FromSlash(native.configFile))
					if native.provider == "codex" {
						// Read the paths Codex itself resolves via CODEX_HOME.
						configPath = filepath.Join(dataDir, native.configFile)
						if prompt, err := os.ReadFile(filepath.Join(dataDir, "AGENTS.md")); err != nil || !strings.Contains(string(prompt), "after retention") {
							t.Fatal("rebuilt Codex home lost its refreshed prompt link")
						}
					} else {
						var config map[string]any
						payload, err := os.ReadFile(configPath)
						if err != nil || json.Unmarshal(payload, &config) != nil {
							t.Fatal("rebuilt OpenCode config is unavailable")
						}
						agents, _ := config["agents"].(map[string]any)
						build, _ := agents["build"].(map[string]any)
						if prompt, _ := build["system"].(string); !strings.Contains(prompt, "after retention") {
							t.Fatal("rebuilt OpenCode lost instance instructions")
						}
					}
					if payload, err := os.ReadFile(configPath); err != nil || !strings.Contains(string(payload), "open-computer-use") {
						t.Fatal("native configuration did not survive same-key config recreation")
					}
				})
			}
		})
	}
}

func TestHistoricalProviderDataHomesDoNotConsumeConfigScanBound(t *testing.T) {
	manager := NewManager("fake", "unused", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{})
	defer manager.Close()
	dataRoot := filepath.Join(manager.rootDir, "provider-data")
	for index := 0; index <= maxConfigRetentionEntries; index++ {
		if err := os.MkdirAll(filepath.Join(dataRoot, fmt.Sprintf("historical-%04d", index)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	key := InstanceKey{InstanceID: "current", Generation: "generation", ProjectID: "project", GitScopeHash: "scope"}
	path := manager.instanceDir(key)
	writeRetentionFile(t, filepath.Join(path, "config"), "rebuildable")
	if err := writeInstanceManifest(path, instanceManifest{
		Version: 1, Provider: "fake", SessionKey: generationSessionKey(key),
		UpdatedAt: time.Now().Add(-31 * 24 * time.Hour), SessionPersisted: true, ConfigOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.pruneConfigDirs(t.Context()); err != nil {
		t.Fatalf("historical native homes exhausted config scan bound: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("historical native homes blocked config pruning")
	}
	if _, _, err := manager.Get(t.Context(), "new-run", key, InstanceConfig{}); err != nil {
		t.Fatalf("historical native homes blocked a subsequent run: %v", err)
	}
	manager.ReleaseRun("new-run")
	entries, err := os.ReadDir(dataRoot)
	if err != nil || len(entries) != maxConfigRetentionEntries+2 {
		t.Fatal("config pruning deleted historical native homes")
	}
}

func TestManagerNativeDataBindsEveryScopeBoundaryAndRejectsSymlinks(t *testing.T) {
	for _, providers := range [][2]string{{"codex", "opencode"}, {"opencode", "codex"}} {
		t.Run(providers[0], func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(providers[0], "unused", root, nil, log.New(io.Discard, "", 0))
			key := InstanceKey{InstanceID: "instance", Generation: "generation", ProjectID: "project", GitScopeHash: "scope"}
			dataDir, err := manager.prepareInstanceDataDir(key)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(dataDir, filepath.Join(root, "provider-data")+string(filepath.Separator)) || dataDir == manager.instanceDir(key) {
				t.Fatal("native data is not outside prunable instance config")
			}
			writeRetentionFile(t, filepath.Join(dataDir, "scope-sentinel"), "original native home")
			for field, value := range map[string]string{"instance": "other", "generation": "other", "project": "other", "scope": "other"} {
				other := key
				switch field {
				case "instance":
					other.InstanceID = value
				case "generation":
					other.Generation = value
				case "project":
					other.ProjectID = value
				case "scope":
					other.GitScopeHash = value
				}
				otherDir, err := manager.prepareInstanceDataDir(other)
				if err != nil || otherDir == dataDir {
					t.Fatalf("native data crossed the %s boundary: %v", field, err)
				}
				if _, err := os.ReadFile(filepath.Join(otherDir, "scope-sentinel")); !os.IsNotExist(err) {
					t.Fatalf("another %s scope can read the original native fixture", field)
				}
				writeRetentionFile(t, filepath.Join(otherDir, "scope-sentinel"), "other native home")
				if payload, err := os.ReadFile(filepath.Join(dataDir, "scope-sentinel")); err != nil || string(payload) != "original native home" {
					t.Fatalf("another %s scope overwrote the original native fixture", field)
				}
			}
			otherProvider := NewManager(providers[1], "unused", root, nil, log.New(io.Discard, "", 0))
			if otherProvider.instanceDataDir(key) == dataDir {
				t.Fatal("native data crossed the Provider boundary")
			}
			outside := t.TempDir()
			other := key
			other.Generation = "linked"
			if err := os.Symlink(outside, manager.instanceDataDir(other)); err != nil {
				t.Skipf("symlink fixture unavailable: %v", err)
			}
			if _, err := manager.prepareInstanceDataDir(other); err == nil {
				t.Fatal("manager accepted a symlink to another native data home")
			}
		})
	}
}

func TestUnclassifiedExistingNativeHomeIsNotReclassifiedOrCleaned(t *testing.T) {
	manager := NewManager("codex", "unused", t.TempDir(), nil, log.New(io.Discard, "", 0))
	manager.RegisterTestAdapter(&fakeAdapter{name: "codex"})
	manager.idleAfter = 0
	manager.retention = configRetentionPolicy{}
	defer manager.Close()
	key := InstanceKey{InstanceID: "existing", Generation: "generation"}
	path := manager.instanceDir(key)
	writeRetentionFile(t, filepath.Join(path, "sessions", "native.jsonl"), "existing native session")
	createNativeDatabaseFixture(t, filepath.Join(path, "state_5.sqlite"))
	if _, _, err := manager.Get(t.Context(), "existing-run", key, InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkSessionPersisted(key); err != nil {
		t.Fatal(err)
	}
	manager.ReleaseRun("existing-run")
	manager.RecycleIdle(t.Context())
	manifest, err := readInstanceManifest(path)
	if err != nil || manifest.ConfigOnly {
		t.Fatal("existing native home was reclassified as disposable config")
	}
	assertNativeDatabaseFixture(t, filepath.Join(path, "state_5.sqlite"))
	if payload, err := os.ReadFile(filepath.Join(path, "sessions", "native.jsonl")); err != nil || string(payload) != "existing native session" {
		t.Fatal("existing native session was moved, cleared or deleted")
	}
}
