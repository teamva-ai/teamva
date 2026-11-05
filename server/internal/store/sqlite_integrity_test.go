package store

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/protocol/sqlitebaseline"
	_ "modernc.org/sqlite"
)

func TestOpenSQLiteUsesPowerLossDurableSynchronousMode(t *testing.T) {
	repository, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	var synchronous int
	if err := repository.db.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	// SQLite defines FULL as 2. NORMAL (1) is insufficient before cross-DB ACK.
	if synchronous != 2 {
		t.Fatalf("SQLite synchronous=%d, want FULL (2)", synchronous)
	}
}

func TestOpenSQLiteChecksExistingDatabaseBeforeAnyMigrationOrSeedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	original := []byte("not a sqlite control database\x00preserve exactly")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	knownTime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, knownTime, knownTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLite(t.Context(), path)
	if repository != nil {
		_ = repository.Close()
		t.Fatal("corrupt SQLite database unexpectedly opened")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "integrity") {
		t.Fatalf("corrupt SQLite error=%v", err)
	}
	afterPayload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPayload, original) || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("corrupt database was modified: size=%d/%d mtime=%s/%s",
			after.Size(), before.Size(), after.ModTime(), before.ModTime())
	}
}

func TestOpenSQLiteQuickCheckRejectsDamagedBTreeWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "damaged-btree.db")
	db := openRawSQLite(t, path)
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE damaged_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL)
	`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO damaged_rows (value) VALUES (?)
		`, strings.Repeat("x", 2048)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) < 18 {
		t.Fatalf("SQLite fixture too small: %d", len(payload))
	}
	pageSize := int(binary.BigEndian.Uint16(payload[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize <= 0 || len(payload) <= pageSize {
		t.Fatalf("invalid SQLite page size=%d file=%d", pageSize, len(payload))
	}
	// Page 2 is the root b-tree page of the first user table. An invalid page
	// type leaves the SQLite header/schema readable while quick_check must walk
	// the damaged tree and reject it.
	payload[pageSize] = 0xff
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	knownTime := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(path, knownTime, knownTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLite(t.Context(), path)
	if repository != nil {
		_ = repository.Close()
		t.Fatal("damaged b-tree unexpectedly opened")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "quick_check") {
		t.Fatalf("damaged b-tree error=%v", err)
	}
	afterPayload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPayload, payload) || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("quick_check path modified damaged database: size=%d/%d mtime=%s/%s",
			after.Size(), before.Size(), after.ModTime(), before.ModTime())
	}
}

func TestOpenSQLiteRejectsHealthyOldSchemaWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db := openRawSQLite(t, path)
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE schema_migrations (version INTEGER);
		INSERT INTO schema_migrations VALUES (21);
		CREATE TABLE important_data (value TEXT);
		INSERT INTO important_data VALUES ('preserve this database');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLite(t.Context(), path)
	if repository != nil {
		_ = repository.Close()
		t.Fatal("old schema unexpectedly opened")
	}
	if !errors.Is(err, sqlitebaseline.ErrUnsupported) {
		t.Fatalf("old baseline error=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected database changed: %v", err)
	}
}

func TestOpenSQLiteRejectsNonRegularExistingPath(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	current, err := OpenSQLite(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLite(t.Context(), link)
	if repository != nil {
		_ = repository.Close()
		t.Fatal("SQLite symlink unexpectedly opened")
	}
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error=%v", err)
	}
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}
