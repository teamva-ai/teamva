package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryIsDeterministicAndValidatesManifests(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "fixturecli"), os.DirFS("testdata/fixturecli")); err != nil {
		t.Fatal(err)
	}
	first, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generate(root)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("non-deterministic registry: %v", err)
	}
	if !bytes.Contains(first, []byte("/provider/fixturecli")) {
		t.Fatal("new directory was not discovered")
	}
	for _, invalid := range []string{
		`{"api_version":999,"id":"fixturecli","display_name":"Fixture","command":"fixture"}`,
		`{"api_version":1,"id":"other","display_name":"Fixture","command":"fixture"}`,
		`{"api_version":1,"id":"fixturecli","display_name":"Fixture","command":"fixture","typo":true}`,
		`{"api_version":1,"id":"fixturecli","display_name":"Fixture","command":"fixture"} {}`,
	} {
		if err := os.WriteFile(filepath.Join(root, "fixturecli", "provider.json"), []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := generate(root); err == nil {
			t.Fatalf("invalid metadata accepted: %s", invalid)
		}
	}
}

// This is a source-extension integration test, not a hand-registered mock: copy
// the unchanged execution core, add ONE provider directory, generate, then run
// its public Controller through SQLite/outbox and cleanup.
func TestNewDirectoryRunsWithoutCoreEdits(t *testing.T) {
	if testing.Short() {
		t.Skip("source-extension compilation test")
	}
	nodeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	root := filepath.Join(workspace, "node")
	for _, dir := range []string{"provider", "embedded", "internal"} {
		if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(nodeRoot, dir))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(nodeRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.CopyFS(filepath.Join(workspace, "protocol"), os.DirFS(filepath.Join(nodeRoot, "..", "protocol"))); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "provider", "fixturecli"), os.DirFS("testdata/fixturecli")); err != nil {
		t.Fatal(err)
	}
	generated, err := generate(filepath.Join(root, "provider"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "provider", "catalog", "providers_generated.go"), generated, 0o600); err != nil {
		t.Fatal(err)
	}
	harness, err := os.ReadFile("testdata/extension_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "embedded", "provider_extension_test.go"), harness, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "./embedded", "-run", "^TestDiscoveredProviderRuns$", "-count=1")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("new-folder extension failed: %v\n%s", err, output)
	}
}
