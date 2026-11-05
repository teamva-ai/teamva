package localstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourceAuthorizationDirectoryIsPrivateStateWithoutSQLChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(path), "resource-authorizations")
	if got := store.ResourceAuthorizationDirectory(); got != want {
		t.Fatalf("private approval location=%q", got)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Fatal("opening a Store silently created resource approval state")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.ResourceAuthorizationDirectory() != want {
		t.Fatal("approval location changed across database reopen")
	}
	if (&Store{}).ResourceAuthorizationDirectory() != "" {
		t.Fatal("a nondurable Store exposed a fallback approval location")
	}
}
