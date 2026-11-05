package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteProjectIgnoresAndPreservesLegacyColorColumn(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user, computer, err := s.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "ordinary", Description: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ordinary.ID, domain.DefaultProjectID(computer.ID)} {
		var inserted string
		if err := s.db.QueryRow(`SELECT theme FROM projects WHERE id=?`, id).Scan(&inserted); err != nil || inserted != "hallmark" {
			t.Fatalf("legacy column insert: %q err=%v", inserted, err)
		}
		if _, err := s.db.Exec(`UPDATE projects SET theme='flexoki' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		before, err := s.GetProject(ctx, user.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE projects SET theme='chamber' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		after, err := s.GetProject(ctx, user.ID, id)
		if err != nil || after != before {
			t.Fatalf("physical legacy color changed project projection: before=%+v after=%+v err=%v", before, after, err)
		}
		updated, err := s.UpdateProject(ctx, user.ID, id, after.Version, func(item *domain.Project) { item.Description = "updated" })
		if err != nil || updated.Description != "updated" || updated.Version != after.Version+1 {
			t.Fatalf("business update failed: %+v err=%v", updated, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := s.ListProjects(ctx, user.ID, computer.ID)
	if err != nil || len(projects) != 2 {
		t.Fatalf("reopened projects: %+v err=%v", projects, err)
	}
	for _, project := range projects {
		var legacy string
		if err := s.db.QueryRow(`SELECT theme FROM projects WHERE id=?`, project.ID).Scan(&legacy); err != nil || legacy != "chamber" {
			t.Fatalf("business update or reopen rewrote legacy color: %q err=%v", legacy, err)
		}
		encoded, err := json.Marshal(project)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["theme"]; exists {
			t.Fatalf("domain project exposes legacy color: %s", encoded)
		}
	}
}
