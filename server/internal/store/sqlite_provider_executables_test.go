package store

import (
	"path/filepath"
	"testing"
)

func TestProviderExecutablePathsPersistAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	paths, err := repository.ProviderExecutablePaths(ctx)
	if err != nil || len(paths) != 0 {
		t.Fatalf("fresh paths=%v err=%v", paths, err)
	}
	if stored, err := repository.ProviderExecutablePath(ctx, "opencode"); err != nil || stored != "" {
		t.Fatalf("fresh single path=%q err=%v", stored, err)
	}

	if err := repository.SetProviderExecutablePath(ctx, "opencode", "/opt/tools/opencode"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetProviderExecutablePath(ctx, "codex", "/opt/tools/codex"); err != nil {
		t.Fatal(err)
	}
	paths, err = repository.ProviderExecutablePaths(ctx)
	if err != nil || paths["opencode"] != "/opt/tools/opencode" || paths["codex"] != "/opt/tools/codex" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if stored, err := repository.ProviderExecutablePath(ctx, "opencode"); err != nil || stored != "/opt/tools/opencode" {
		t.Fatalf("single path=%q err=%v", stored, err)
	}
	if err := repository.SetProviderExecutablePath(ctx, "codex", ""); err != nil {
		t.Fatal(err)
	}
	paths, err = repository.ProviderExecutablePaths(ctx)
	if err != nil || paths["opencode"] != "/opt/tools/opencode" || len(paths) != 1 {
		t.Fatalf("after clear paths=%v err=%v", paths, err)
	}

	// The setting survives a reopen, which is what the next service start reads.
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if stored, err := reopened.ProviderExecutablePath(t.Context(), "opencode"); err != nil || stored != "/opt/tools/opencode" {
		t.Fatalf("reopened path=%q err=%v", stored, err)
	}
}
