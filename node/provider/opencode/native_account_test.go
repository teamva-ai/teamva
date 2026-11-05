package opencode

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

type nativeCredentialFixture struct {
	id, integration, kind, key string
	active, created            int
}

func nativeAccountFixture(t *testing.T, credentials []nativeCredentialFixture, catalog []byte) string {
	t.Helper()
	path, err := nativeAccountPath()
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		t.Fatal("prepare fake native account failed")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("open fake native account failed")
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE credential (id TEXT PRIMARY KEY, integration_id TEXT, value TEXT NOT NULL, active INTEGER NOT NULL, time_created INTEGER NOT NULL);
		CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);`); err != nil {
		t.Fatal("create fake native account schema failed")
	}
	for _, credential := range credentials {
		value := map[string]any{"type": credential.kind}
		if credential.kind == "key" {
			value["key"] = credential.key
		} else {
			value["access"] = "fake-oauth-access-must-not-be-imported"
			value["refresh"] = "fake-oauth-refresh-must-not-be-imported"
		}
		raw, _ := json.Marshal(value)
		if _, err := db.Exec(`INSERT INTO credential VALUES (?, ?, ?, ?, ?)`, credential.id, credential.integration, string(raw), credential.active, credential.created); err != nil {
			t.Fatal("write fake native credential failed")
		}
	}
	if catalog != nil {
		raw, _ := json.Marshal(map[string]any{"updatedAt": 1, "body": string(catalog)})
		if _, err := db.Exec(`INSERT INTO kv VALUES ('models-dev:catalog', ?)`, string(raw)); err != nil {
			t.Fatal("write fake native catalog failed")
		}
	}
	return path
}

func TestNativeAccountImportsSelectedKeysAndOnlyPublicCatalogReadOnly(t *testing.T) {
	isolateUserHomes(t)
	catalog := []byte(`{"fixture":{"id":"fixture","name":"public metadata","models":{}}}`)
	path := nativeAccountFixture(t, []nativeCredentialFixture{
		{id: "older", integration: "deepseek", kind: "key", key: "fake-older-key", created: 1},
		{id: "selected", integration: "deepseek", kind: "key", key: "fake-selected-key", active: 1, created: 2},
		{id: "newer-inactive", integration: "deepseek", kind: "key", key: "fake-inactive-key", created: 3},
		{id: "openai-old", integration: "openai", kind: "key", key: "fake-old-openai-key", created: 1},
		{id: "openai-selected", integration: "openai", kind: "oauth", active: 1, created: 2},
	}, catalog)
	before, _ := os.ReadFile(path)
	account, err := readNativeAccount()
	if err != nil || account.keys["deepseek"] != "fake-selected-key" || account.keys["openai"] != "" || !bytes.Equal(account.catalog, catalog) {
		t.Fatal("V2 selected API-key/public catalog projection differs")
	}
	// A selected V2 OAuth account must not silently become an old JSON API key.
	home, _ := dataHome()
	writeFile(t, filepath.Join(home, "opencode", "auth.json"), `{"openai":{"type":"api","key":"fake-stale-legacy"}}`)
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig(t, dir)
	providers := cfg["providers"].(map[string]any)
	settings := providers["deepseek"].(map[string]any)["settings"].(map[string]any)
	if settings["apiKey"] != "fake-selected-key" || providers["openai"] != nil {
		t.Fatal("private native API-key projection/unsupported OAuth boundary differs")
	}
	for _, name := range []string{"opencode.json", nativeCatalogFilename} {
		file := filepath.Join(dir, "opencode", name)
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("native account projection is not private 0600")
		}
		raw, _ := os.ReadFile(file)
		if bytes.Contains(raw, []byte("fake-oauth")) || bytes.Contains(raw, []byte("fake-stale-legacy")) || bytes.Contains(raw, []byte("fake-inactive-key")) {
			t.Fatal("unsupported/unselected credential was copied into private configuration")
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only account projection changed the source native database")
	}
	probe, err := newModelAwareProbe("unused-opencode")
	if err != nil {
		t.Fatal(err)
	}
	probeClient := probe.(*Client)
	if !probeClient.probeOnly || probeClient.configDir == "" || probeClient.dataDir == "" || probeClient.dataDir == filepath.Dir(filepath.Dir(path)) {
		t.Fatal("Probe is still using the unprojected global account/database")
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	paths, err := accountAuthSourcePaths()
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(path)
	for _, protected := range []string{canonical, canonical + "-wal", canonical + "-shm"} {
		if !slices.Contains(paths, protected) {
			t.Fatal("original V2 database/sidecar lost OS write protection")
		}
	}
}

func TestNativeAccountKeepsExplicitCanonicalAndLegacyKeys(t *testing.T) {
	isolateUserHomes(t)
	nativeAccountFixture(t, []nativeCredentialFixture{
		{id: "canonical", integration: "canonical", kind: "key", key: "fake-native-key", active: 1},
		{id: "legacy", integration: "legacy", kind: "key", key: "fake-native-key", active: 1},
	}, nil)
	home, _ := configHome()
	writeFile(t, filepath.Join(home, "opencode", "opencode.json"), `{
		"providers":{"canonical":{"settings":{"apiKey":"fake-explicit-canonical"}}},
		"provider":{"legacy":{"options":{"apiKey":"fake-explicit-legacy"}}}
	}`)
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
	if bytes.Contains(raw, []byte("fake-native-key")) || !bytes.Contains(raw, []byte("fake-explicit-canonical")) || !bytes.Contains(raw, []byte("fake-explicit-legacy")) {
		t.Fatal("native account import overwrote explicit provider credentials")
	}
}

func TestNativeAccountMissingDoesNotCreateOrUpgradeDatabase(t *testing.T) {
	isolateUserHomes(t)
	path, _ := nativeAccountPath()
	if _, err := readNativeAccount(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only account lookup created a native database")
	}
	writeFile(t, path, "fake-unsupported-native-store-sentinel")
	before, _ := os.ReadFile(path)
	if _, err := readNativeAccount(); err == nil || strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), path) {
		t.Fatal("unsupported native account was accepted or exposed private data")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("unsupported native account was modified/recreated")
	}
}
