package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLitePreferencesAppearancePersistenceAndScope(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user, _, err := s.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.GetUserPreferences(ctx, user.ID)
	if err != nil || initial.Appearance != (domain.Appearance{Palette: "default"}) {
		t.Fatalf("defaults=%+v err=%v", initial, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key = ?`, appearanceMetadataKey(user.ID)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reading default must not backfill metadata: count=%d err=%v", count, err)
	}
	custom := domain.Appearance{Palette: "default", Accent: "#aB12Cd", Background: "#112233", Control: "#aBc012"}
	saved, err := s.UpdateUserPreferences(ctx, user.ID, initial.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &custom)
	if err != nil || saved.Appearance != custom || saved.Version != initial.Version+1 {
		t.Fatalf("save=%+v err=%v", saved, err)
	}
	// A non-appearance caller preserves the metadata, including custom colors.
	saved, err = s.UpdateUserPreferences(ctx, user.ID, saved.Version, domain.LocaleFrFR, domain.ThemeLight, false, true, nil)
	if err != nil || saved.Appearance != custom {
		t.Fatalf("legacy update=%+v err=%v", saved, err)
	}
	other := seedSQLiteContractUser(t, s, "appearance-other@example.test")
	otherInitial, err := s.GetUserPreferences(ctx, other.ID)
	if err != nil || otherInitial.Appearance.Palette != "default" {
		t.Fatalf("other defaults=%+v err=%v", otherInitial, err)
	}
	otherSaved, err := s.UpdateUserPreferences(ctx, other.ID, otherInitial.Version, otherInitial.Locale, otherInitial.Theme, false, false, &domain.Appearance{Palette: "ocean"})
	if err != nil {
		t.Fatal(err)
	}
	// Reopen the exact same database: no schema change, migration or reset.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for userID, want := range map[string]domain.UserPreferences{user.ID: saved, other.ID: otherSaved} {
		got, err := s.GetUserPreferences(ctx, userID)
		if err != nil || got != want {
			t.Fatalf("reopened preferences=%+v want=%+v err=%v", got, want, err)
		}
	}
	// Whole-object replacement clears all prior custom colors.
	replaced, err := s.UpdateUserPreferences(ctx, user.ID, saved.Version, saved.Locale, saved.Theme, saved.AllowWrite, saved.LocaleExplicit, &domain.Appearance{Palette: "forest"})
	if err != nil || replaced.Appearance != (domain.Appearance{Palette: "forest"}) {
		t.Fatalf("replacement=%+v err=%v", replaced, err)
	}
	if _, err := s.db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, sqliteTimeValue(time.Now()), other.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetUserPreferences(ctx, other.ID)
	assertDomainCode(t, err, "not_found")
	_, err = s.UpdateUserPreferences(ctx, other.ID, otherSaved.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &custom)
	assertDomainCode(t, err, "not_found")
	_, err = s.UpdateUserPreferences(ctx, "missing-user", 1, domain.LocaleEnUS, domain.ThemeDark, true, true, &custom)
	assertDomainCode(t, err, "not_found")
}

func TestSQLitePreferencesAppearanceAtomicity(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "insert"
		if existing {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			s, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			user, _, err := s.LocalIdentity()
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.GetUserPreferences(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				before, err = s.UpdateUserPreferences(ctx, user.ID, before.Version, before.Locale, before.Theme, false, false, &domain.Appearance{Palette: "iris", Control: "#123456"})
				if err != nil {
					t.Fatal(err)
				}
			}
			// SQLite triggers inject a metadata write failure after the preferences
			// UPDATE. Every legacy field and its version must roll back too.
			if _, err := s.db.Exec(`CREATE TRIGGER reject_appearance BEFORE INSERT ON app_metadata
				WHEN NEW.key LIKE 'user_appearance:%' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			_, err = s.UpdateUserPreferences(ctx, user.ID, before.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &domain.Appearance{Palette: "flexoki", Accent: "#ABCDEF", Control: "#abcdef"})
			if err == nil {
				t.Fatal("metadata failure unexpectedly succeeded")
			}
			after, err := s.GetUserPreferences(ctx, user.ID)
			if err != nil || after != before {
				t.Fatalf("failed write was not atomic: before=%+v after=%+v err=%v", before, after, err)
			}
			if _, err := s.db.Exec(`DROP TRIGGER reject_appearance`); err != nil {
				t.Fatal(err)
			}
			_, err = s.UpdateUserPreferences(ctx, user.ID, before.Version+1, domain.LocaleEnUS, domain.ThemeDark, true, true, &domain.Appearance{Palette: "ocean", Control: "#000000"})
			assertDomainCode(t, err, "version_conflict")
			for _, invalid := range []domain.Appearance{{Palette: "invalid"}, {Palette: "project"}, {Palette: "default", Accent: "#FFF"}, {Palette: "iris", Background: "red"}, {Palette: "default", Control: "#FFF"}, {Palette: "default", Control: "#12345G"}} {
				_, err = s.UpdateUserPreferences(ctx, user.ID, before.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &invalid)
				assertDomainCode(t, err, "invalid_argument")
			}
			after, err = s.GetUserPreferences(ctx, user.ID)
			if err != nil || after != before {
				t.Fatalf("invalid/stale writes changed preferences: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestSQLitePreferencesAppearanceConcurrentVersion(t *testing.T) {
	ctx := t.Context()
	s, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user, _, err := s.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		preferences domain.UserPreferences
		err         error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, appearance := range []*domain.Appearance{nil, {Palette: "chamber", Accent: "#123456", Control: "#abcdef"}} {
		go func(appearance *domain.Appearance) {
			<-start
			item, err := s.UpdateUserPreferences(ctx, user.ID, before.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, appearance)
			results <- result{item, err}
		}(appearance)
	}
	close(start)
	var successes int
	var winner domain.UserPreferences
	for range 2 {
		result := <-results
		if result.err != nil {
			assertDomainCode(t, result.err, "version_conflict")
		} else {
			successes++
			winner = result.preferences
		}
	}
	after, err := s.GetUserPreferences(ctx, user.ID)
	if err != nil || successes != 1 || after != winner || after.Version != before.Version+1 {
		t.Fatalf("lost/mixed update: successes=%d winner=%+v after=%+v err=%v", successes, winner, after, err)
	}
}

func TestSQLitePreferencesStoredPaletteProjectionIsReadOnly(t *testing.T) {
	for _, palette := range []string{"project", "default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris"} {
		t.Run(palette, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "app.db")
			s, err := OpenSQLite(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			user, _, err := s.LocalIdentity()
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.GetUserPreferences(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw := ` {"palette":"` + palette + `", "accent":"#aBc123","background":"#102030","control":"#AbC456"} `
			stamp := "2026-01-02T03:04:05Z"
			if _, err := s.db.Exec(`INSERT INTO app_metadata (key,value,updated_at) VALUES (?,?,?)`, appearanceMetadataKey(user.ID), raw, stamp); err != nil {
				t.Fatal(err)
			}
			if palette == "project" {
				palette = "default"
			}
			want := before
			want.Appearance = domain.Appearance{Palette: palette, Accent: "#aBc123", Background: "#102030", Control: "#AbC456"}
			for range 2 {
				got, err := s.GetUserPreferences(ctx, user.ID)
				if err != nil || got != want {
					t.Fatalf("stored palette projection: got=%+v want=%+v err=%v", got, want, err)
				}
				var value, updatedAt string
				if err := s.db.QueryRow(`SELECT value,updated_at FROM app_metadata WHERE key=?`, appearanceMetadataKey(user.ID)).Scan(&value, &updatedAt); err != nil || value != raw || updatedAt != stamp {
					t.Fatalf("read rewrote appearance metadata: value=%q stamp=%q err=%v", value, updatedAt, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenSQLite(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
			}
			// An unrelated preference update must not persist the projection either.
			updated, err := s.UpdateUserPreferences(ctx, user.ID, want.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, nil)
			if err != nil || updated.Appearance != want.Appearance || updated.Version != want.Version+1 {
				t.Fatalf("unrelated preference save: %+v err=%v", updated, err)
			}
			var value, updatedAt string
			if err := s.db.QueryRow(`SELECT value,updated_at FROM app_metadata WHERE key=?`, appearanceMetadataKey(user.ID)).Scan(&value, &updatedAt); err != nil || value != raw || updatedAt != stamp {
				t.Fatalf("unrelated save rewrote appearance metadata: value=%q stamp=%q err=%v", value, updatedAt, err)
			}
		})
	}
}
