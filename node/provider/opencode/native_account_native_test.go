package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

// Actual bundled runtime, fake V2 credentials/public catalog, no prompt or model
// request. The account DB is not the native runtime's DB and is never migrated.
func TestBundledNativeAccountProjectionWithoutInference(t *testing.T) {
	if os.Getenv("TEAMVA_OPENCODE_TEST") != "1" {
		t.Skip("set TEAMVA_OPENCODE_TEST=1 for native account metadata verification")
	}
	binary := vendoredBinary(t)
	if binary == "" {
		t.Fatal("bundled OpenCode missing")
	}
	isolateNativeFixtureEnvironment(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	catalog, _ := json.Marshal(map[string]any{"deepseek": map[string]any{
		"id": "deepseek", "name": "fake native account metadata", "npm": "@ai-sdk/openai-compatible", "api": server.URL + "/v1",
		"env": []string{"DEEPSEEK_API_KEY"}, "models": map[string]any{"native-account-current": map[string]any{
			"id": "native-account-current", "name": "native-account-current", "release_date": "2026-10-26",
			"attachment": false, "reasoning": false, "tool_call": true, "cost": map[string]int{"input": 1, "output": 1},
			"limit": map[string]int{"context": 200000, "output": 32000}, "modalities": map[string]any{"input": []string{"text"}, "output": []string{"text"}},
		}},
	}})
	accountPath := nativeAccountFixture(t, []nativeCredentialFixture{
		{id: "selected-key", integration: "deepseek", kind: "key", key: "fake-native-account-key", active: 1},
		{id: "selected-oauth", integration: "openai", kind: "oauth", active: 1},
	}, catalog)
	before, _ := os.ReadFile(accountPath)
	base := permissionFixtureDirectory(t, t.TempDir())
	cwd := permissionFixtureDirectory(t, base, "workspace")
	configDir := permissionFixtureDirectory(t, base, "config")
	adapter, err := newInstance(provider.InstanceOptions{Executable: binary, ConfigDir: configDir,
		DataDir: permissionFixtureDirectory(t, base, "managed-data"),
		Config:  provider.InstanceConfig{WritableRepositories: []string{filepath.Dir(accountPath)}}})
	if err != nil {
		t.Fatal(err)
	}
	client := adapter.(*Client)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	request := provider.RunRequest{CWD: cwd, RunID: "metadata-native-account"}
	if err := client.ensureStartedScoped(ctx, &request); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyPermissionGuard(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	if err := client.verifySkillBindings(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	models, err := client.listModelsAt(ctx, cwd)
	if err != nil || !reflect.DeepEqual(models, []string{"deepseek/native-account-current"}) {
		t.Fatalf("managed runtime did not reuse the V2 API-key/public catalog projection: models=%v err=%v", models, err)
	}
	probe, err := newModelAwareProbe(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	manifest, err := probe.Probe(ctx)
	if err != nil || !manifest.Available || !reflect.DeepEqual(manifest.Models, models) {
		t.Fatalf("Probe and managed catalog projection differ: models=%v err=%v", manifest.Models, err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	id, baseline, err := client.prepareSession(ctx, request)
	if err != nil || id == "" || baseline != (usageTotals{}) {
		t.Fatalf("native API-key metadata session failed: %v", err)
	}
	var session struct {
		Data struct {
			Model  modelReference  `json:"model"`
			Tokens json.RawMessage `json:"tokens"`
		} `json:"data"`
	}
	if err := client.getJSON(ctx, "/api/session/"+id, &session); err != nil || session.Data.Model.ProviderID != "deepseek" || session.Data.Model.ID != "native-account-current" {
		t.Fatal("managed native session selected a bundled snapshot instead of imported public metadata")
	}
	usage, err := decodeUsageTotals(session.Data.Tokens)
	if err != nil || usage != (usageTotals{}) || calls.Load() != 0 {
		t.Fatal("metadata projection performed inference")
	}
	// The account parent is an authorized business root, but original native
	// DB and sidecars must still be denied by the real OS sandbox.
	for _, source := range []string{accountPath, accountPath + "-wal", accountPath + "-shm"} {
		cmd, err := client.writeSandbox.command("/bin/sh", "-c", `printf blocked > "$1"`, "fixture", source)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Env = client.writeSandbox.environment(managedEnv())
		if err := cmd.Run(); err == nil {
			t.Fatal("OS sandbox allowed writing the original native account/sidecar")
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(accountPath)
	if !bytes.Equal(before, after) {
		t.Fatal("account projection or managed runtime modified the source native database")
	}
	t.Log("Probe and managed catalogs match; V2 selected API key/current public cache reused; OAuth excluded; source DB unchanged; DB/WAL/SHM OS writes denied; usage=0 model requests=0")
}
