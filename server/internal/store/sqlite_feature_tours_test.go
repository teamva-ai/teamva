package store

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteFeatureToursDefaultMonotonicPersistenceAndIndependentSettings(t *testing.T) {
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
	var changesBefore, changesAfter int64
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetFeatureTours(ctx, user.ID)
	if err != nil || view.Seen == nil || len(view.Seen) != 0 {
		t.Fatalf("default=%+v err=%v", view, err)
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil || changesAfter != changesBefore {
		t.Fatalf("GET mutated database: before=%d after=%d err=%v", changesBefore, changesAfter, err)
	}
	assertFeatureToursMetadataCount(t, s, user.ID, 0)
	preferences, err := s.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	appearance := domain.Appearance{Palette: "ocean", Accent: "#123456", Background: "#112233"}
	preferences, err = s.UpdateUserPreferences(ctx, user.ID, preferences.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &appearance)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOnboardingDismissed(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	onboarding, err := s.Onboarding(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := domain.FeatureTourIDs()
	// Mark in reverse order: each response must still use the fixed order.
	for i := len(ids) - 1; i >= 0; i-- {
		view, err = s.MarkFeatureTourSeen(ctx, user.ID, ids[i])
		if err != nil || !slices.Equal(view.Seen, ids[i:]) {
			t.Fatalf("mark %s: got=%+v want=%v err=%v", ids[i], view, ids[i:], err)
		}
		before, timestamp := featureToursRawMetadata(t, s, user.ID)
		duplicate, err := s.MarkFeatureTourSeen(ctx, user.ID, ids[i])
		after, afterTimestamp := featureToursRawMetadata(t, s, user.ID)
		if err != nil || !slices.Equal(duplicate.Seen, view.Seen) || before != after || timestamp != afterTimestamp {
			t.Fatalf("duplicate mark changed state: %+v err=%v", duplicate, err)
		}
	}
	gotPreferences, err := s.GetUserPreferences(ctx, user.ID)
	if err != nil || gotPreferences != preferences {
		t.Fatalf("tour marks changed appearance/locale/preferences version: %+v err=%v", gotPreferences, err)
	}
	gotOnboarding, err := s.Onboarding(ctx, user.ID)
	if err != nil || gotOnboarding != onboarding {
		t.Fatalf("tour marks changed two-step onboarding: %+v err=%v", gotOnboarding, err)
	}
	// Conversely, ordinary settings and first-run-guide changes cannot reset tours.
	if _, err := s.UpdateUserPreferences(ctx, user.ID, preferences.Version, domain.LocaleFrFR, domain.ThemeLight, false, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOnboardingDismissed(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.GetFeatureTours(ctx, user.ID)
	if err != nil || !slices.Equal(view.Seen, ids) {
		t.Fatalf("reopened=%+v err=%v", view, err)
	}
}

func TestSQLiteFeatureToursConcurrentConnectionsMerge(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "app.db")
	a, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	user, _, err := a.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := a.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 57)
	appearance := domain.Appearance{Palette: "iris", Accent: "#123456"}
	go func() {
		<-start
		_, err := a.UpdateUserPreferences(ctx, user.ID, preferences.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, &appearance)
		results <- err
	}()
	for i := range 8 {
		for _, id := range domain.FeatureTourIDs() {
			go func() {
				<-start
				s := []*SQLite{a, b}[i%2]
				_, err := s.MarkFeatureTourSeen(ctx, user.ID, id)
				results <- err
			}()
		}
	}
	close(start)
	for range 57 {
		if err := <-results; err != nil {
			t.Errorf("concurrent mark: %v", err)
		}
	}
	view, err := b.GetFeatureTours(ctx, user.ID)
	if err != nil || !slices.Equal(view.Seen, domain.FeatureTourIDs()) {
		t.Fatalf("lost concurrent marks: %+v err=%v", view, err)
	}
	savedPreferences, err := b.GetUserPreferences(ctx, user.ID)
	if err != nil || savedPreferences.Version != preferences.Version+1 || savedPreferences.Appearance != appearance || savedPreferences.Locale != domain.LocaleEnUS || savedPreferences.Theme != domain.ThemeDark || !savedPreferences.AllowWrite || !savedPreferences.LocaleExplicit {
		t.Fatalf("tour marks interfered with concurrent preference autosave: %+v err=%v", savedPreferences, err)
	}
}

func TestSQLiteFeatureToursInvalidAndUserScope(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	s := f.repository
	defer s.Close()
	ctx := t.Context()
	for _, id := range []string{"", "Overview", "overview ", "unknown", "overview.issues", "$.issues"} {
		_, err := s.MarkFeatureTourSeen(ctx, f.user.ID, id)
		assertDomainCode(t, err, "invalid_argument")
	}
	assertFeatureToursMetadataCount(t, s, f.user.ID, 0)
	other := seedSQLiteContractUser(t, s, "tour-other@example.test")
	for userID, id := range map[string]string{f.user.ID: "overview", other.ID: "issues"} {
		if _, err := s.MarkFeatureTourSeen(ctx, userID, id); err != nil {
			t.Fatal(err)
		}
	}
	for userID, want := range map[string]string{f.user.ID: "overview", other.ID: "issues"} {
		view, err := s.GetFeatureTours(ctx, userID)
		if err != nil || !slices.Equal(view.Seen, []string{want}) {
			t.Fatalf("user isolation: %+v err=%v", view, err)
		}
	}
	before, timestamp := featureToursRawMetadata(t, s, other.ID)
	if _, err := s.db.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, sqliteTimeValue(time.Now()), other.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{other.ID, "missing-user"} {
		_, err := s.GetFeatureTours(ctx, id)
		assertDomainCode(t, err, "not_found")
		_, err = s.MarkFeatureTourSeen(ctx, id, "changes")
		assertDomainCode(t, err, "not_found")
	}
	after, afterTimestamp := featureToursRawMetadata(t, s, other.ID)
	if before != after || timestamp != afterTimestamp {
		t.Fatal("deleted user's metadata was changed")
	}
	assertFeatureToursMetadataCount(t, s, "missing-user", 0)
}

func TestSQLiteFeatureToursRejectsCorruptMetadataWithoutRepair(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	s := f.repository
	defer s.Close()
	ctx := t.Context()
	for _, encoded := range []string{
		`null`, `[]`, `true`, `"overview"`, `{"seen":["overview"]}`, `{"unknown":true}`,
		`{"overview":false}`, `{"overview":null}`, `{"overview":1}`, `{"overview":"true"}`,
		`{"overview":true,"issues":false}`, `{"overview":true,"overview":true}`,
		`{"overview":false,"overview":true}`, `{"overview":true`, `{} {}`,
	} {
		t.Run(encoded, func(t *testing.T) {
			// Inject a corrupt persisted value without modifying the product schema.
			if _, err := s.db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatal(err)
			}
			_, err := s.db.Exec(`INSERT INTO app_metadata(key, value, updated_at) VALUES (?, ?, 'unchanged')
				ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, featureToursMetadataKey(f.user.ID), encoded)
			if _, resetErr := s.db.Exec(`PRAGMA ignore_check_constraints = OFF`); resetErr != nil {
				t.Fatal(resetErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetFeatureTours(ctx, f.user.ID); err == nil {
				t.Fatal("corrupt GET succeeded")
			}
			for _, id := range []string{"overview", "changes"} {
				if _, err := s.MarkFeatureTourSeen(ctx, f.user.ID, id); err == nil {
					t.Fatal("corrupt mark succeeded")
				}
			}
			got, timestamp := featureToursRawMetadata(t, s, f.user.ID)
			if got != encoded || timestamp != "unchanged" {
				t.Fatal("corrupt metadata was silently repaired/overwritten")
			}
		})
	}
}

func TestSQLiteFeatureToursFailedWriteRollsBack(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			f := newSQLiteAgentTestFixture(t)
			s := f.repository
			defer s.Close()
			if existing {
				if _, err := s.MarkFeatureTourSeen(t.Context(), f.user.ID, "overview"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`CREATE TRIGGER reject_tour_mark BEFORE UPDATE ON app_metadata
				WHEN NEW.key LIKE 'feature_tours:%' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.MarkFeatureTourSeen(t.Context(), f.user.ID, "changes"); err == nil {
				t.Fatal("failed write succeeded")
			}
			want := []string{}
			if existing {
				want = append(want, "overview")
			}
			view, err := s.GetFeatureTours(t.Context(), f.user.ID)
			if err != nil || !slices.Equal(view.Seen, want) {
				t.Fatalf("failure lost state: %+v err=%v", view, err)
			}
			assertFeatureToursMetadataCount(t, s, f.user.ID, len(want))
		})
	}
}

func featureToursRawMetadata(t *testing.T, s *SQLite, userID string) (string, string) {
	t.Helper()
	var value, timestamp string
	if err := s.db.QueryRow(`SELECT value, updated_at FROM app_metadata WHERE key = ?`, featureToursMetadataKey(userID)).Scan(&value, &timestamp); err != nil {
		t.Fatal(err)
	}
	return value, timestamp
}

func assertFeatureToursMetadataCount(t *testing.T, s *SQLite, userID string, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key = ?`, featureToursMetadataKey(userID)).Scan(&count); err != nil || count != want {
		t.Fatalf("metadata rows=%d want=%d err=%v", count, want, err)
	}
}
