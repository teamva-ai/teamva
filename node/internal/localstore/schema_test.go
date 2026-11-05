package localstore

import (
	"bytes"
	"database/sql"
	"errors"
	"github.com/teamva-ai/teamva/protocol/sqlitebaseline"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeBaselineRejectsBeforeRecoveryWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimCommand(t.Context(), Command{MessageID: "in-flight", Name: "probe", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE database_baseline SET id='unsupported-generation'`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path); !errors.Is(err, sqlitebaseline.ErrUnsupported) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("unsupported runtime opened: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("recovery touched rejected database: %v", err)
	}
}
