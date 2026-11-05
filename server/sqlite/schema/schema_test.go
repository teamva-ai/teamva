package schema

import (
	"database/sql"
	"errors"
	"github.com/teamva-ai/teamva/protocol/sqlitebaseline"
	_ "modernc.org/sqlite"
	"testing"
)

func TestCurrentBaselineAndSchemaGuards(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := Definition.Initialize(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"project_search_memories_latest", "project_search_issues_latest", "project_search_comments_latest", "project_search_runs_latest"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("search index %s missing from baseline: count=%d err=%v", index, count, err)
		}
	}
	if err := Definition.Initialize(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"issues", "runs", "scheduled_tasks"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name IN ('git_repository_id','git_repository_scope')`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("obsolete columns in %s: %d %v", table, count, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name) VALUES ('u','u@example.test','Owner'); INSERT INTO computers(id,user_id,name,platform,architecture,fingerprint,status) VALUES('c','u','Mac','darwin','arm64','local','online'); INSERT INTO projects(id,computer_id,name) VALUES('p','c','Project'); INSERT INTO issues(id,project_id,created_by_user_id) VALUES('i','p','u');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE issues SET status='completed' WHERE id='i'`); err == nil {
		t.Fatal("obsolete issue status accepted")
	}
	if _, err := db.Exec(`INSERT INTO user_preferences(user_id,locale) VALUES ('u','fr-FR')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX run_events_page_budget`); err != nil {
		t.Fatal(err)
	}
	if err := Definition.Check(t.Context(), db); !errors.Is(err, sqlitebaseline.ErrUnsupported) {
		t.Fatalf("schema drift accepted: %v", err)
	}
}

func TestInitializationFailureIsAtomic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	invalid := sqlitebaseline.Definition{ID: "invalid", SQL: `CREATE TABLE keep_atomic(id INTEGER); THIS IS NOT SQL;`}
	if err := invalid.Initialize(t.Context(), db); err == nil {
		t.Fatal("invalid baseline succeeded")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial initialization: %d %v", count, err)
	}
}
