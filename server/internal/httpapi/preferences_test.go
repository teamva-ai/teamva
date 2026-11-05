package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestUserPreferencesAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)

	initial := decodeData[domain.UserPreferences](t, request(
		t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK,
	))
	if initial.Locale != domain.LocaleZhCN || initial.LocaleExplicit || initial.Theme != domain.ThemeSystem || initial.AllowWrite || initial.Appearance != (domain.Appearance{Palette: "default"}) {
		t.Fatalf("unexpected defaults: %+v", initial)
	}

	updated := decodeData[domain.UserPreferences](t, request(
		t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
			"expected_version": initial.Version,
			"locale":           domain.LocaleEnUS,
			"theme":            domain.ThemeDark,
			"allow_write":      true,
		}, auth, http.StatusOK,
	))
	if updated.Locale != domain.LocaleEnUS || !updated.LocaleExplicit || updated.Theme != domain.ThemeDark || !updated.AllowWrite || updated.Version != initial.Version+1 {
		t.Fatalf("unexpected update: %+v", updated)
	}

	conflict := request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": initial.Version, "theme": domain.ThemeLight,
	}, auth, http.StatusConflict)
	if conflict.Error == nil || conflict.Error.Code != "version_conflict" {
		t.Fatalf("expected version_conflict, got %+v", conflict.Error)
	}
}

func TestUserPreferencesAppearanceAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	get := func(auth map[string]string) domain.UserPreferences {
		return decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	}
	initial := get(auth)
	custom := domain.Appearance{Palette: "default", Accent: "#aBc123", Background: "#102030", Control: "#CdE012"}
	saved := decodeData[domain.UserPreferences](t, request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": initial.Version, "appearance": custom,
		"locale": "en-US", "theme": "dark", "allow_write": true,
	}, auth, http.StatusOK))
	if saved.Appearance != custom || saved.Locale != "en-US" || !saved.LocaleExplicit || saved.Theme != "dark" || !saved.AllowWrite || saved.Version != initial.Version+1 || get(auth) != saved {
		t.Fatalf("appearance + preferences save failed: %+v", saved)
	}
	preserved := decodeData[domain.UserPreferences](t, request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": saved.Version, "theme": "light",
	}, auth, http.StatusOK))
	if preserved.Appearance != custom || preserved.Locale != saved.Locale || preserved.AllowWrite != saved.AllowWrite || !preserved.LocaleExplicit {
		t.Fatalf("omitted fields lost: %+v", preserved)
	}
	conflict := request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": saved.Version, "appearance": map[string]any{"palette": "ocean", "control": "#000000"}, "allow_write": false,
	}, auth, http.StatusConflict)
	if conflict.Error == nil || conflict.Error.Code != "version_conflict" || get(auth) != preserved {
		t.Fatal("stale appearance patch changed preferences")
	}
	request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": preserved.Version + 1, "appearance": map[string]any{"palette": "ocean", "control": "#ffffff"},
	}, auth, http.StatusConflict)
	if get(auth) != preserved {
		t.Fatal("future-version appearance patch changed preferences")
	}
	replacement := decodeData[domain.UserPreferences](t, request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": preserved.Version, "appearance": map[string]any{"palette": "forest"},
	}, auth, http.StatusOK))
	if replacement.Appearance != (domain.Appearance{Palette: "forest"}) || replacement.Theme != preserved.Theme || replacement.Locale != preserved.Locale || !replacement.LocaleExplicit || !replacement.AllowWrite {
		t.Fatalf("whole-object replacement failed: %+v", replacement)
	}
	other := createTestIdentity(t, repository, "appearance-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	otherInitial := get(otherAuth)
	if otherInitial.Appearance != (domain.Appearance{Palette: "default"}) {
		t.Fatal("appearance crossed user boundary")
	}
	request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": otherInitial.Version, "appearance": map[string]any{"palette": "iris", "accent": "", "background": "", "control": ""},
	}, otherAuth, http.StatusOK)
	if get(auth) != replacement || get(otherAuth).Appearance != (domain.Appearance{Palette: "iris"}) {
		t.Fatal("independent appearance saves crossed user boundary")
	}
}

func TestUserPreferencesAppearanceValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	initial := decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	for name, appearance := range map[string]any{
		"null": nil, "string": "ocean", "array": []any{}, "missing palette": map[string]any{},
		"unknown palette":    map[string]any{"palette": "neon"},
		"retired palette":    map[string]any{"palette": "project"},
		"empty palette":      map[string]any{"palette": ""},
		"palette case":       map[string]any{"palette": "Ocean"},
		"palette whitespace": map[string]any{"palette": " ocean"},
		"null palette":       map[string]any{"palette": nil},
		"unknown field":      map[string]any{"palette": "ocean", "extra": "value"},
		"null accent":        map[string]any{"palette": "ocean", "accent": nil},
		"number accent":      map[string]any{"palette": "ocean", "accent": 123},
		"short accent":       map[string]any{"palette": "ocean", "accent": "#123"},
		"long accent":        map[string]any{"palette": "ocean", "accent": "#11223344"},
		"nonhex accent":      map[string]any{"palette": "ocean", "accent": "#12345G"},
		"unprefixed accent":  map[string]any{"palette": "ocean", "accent": "123456"},
		"whitespace accent":  map[string]any{"palette": "ocean", "accent": " #123456"},
		"newline accent":     map[string]any{"palette": "ocean", "accent": "#123456\n"},
		"css background":     map[string]any{"palette": "ocean", "background": "rgb(1,2,3)"},
		"short background":   map[string]any{"palette": "ocean", "background": "#FFF"},
		"null background":    map[string]any{"palette": "ocean", "background": nil},
		"null control":       map[string]any{"palette": "default", "control": nil},
		"number control":     map[string]any{"palette": "default", "control": 123},
		"array control":      map[string]any{"palette": "default", "control": []string{}},
		"short control":      map[string]any{"palette": "default", "control": "#123"},
		"long control":       map[string]any{"palette": "default", "control": "#11223344"},
		"nonhex control":     map[string]any{"palette": "default", "control": "#12345G"},
		"unprefixed control": map[string]any{"palette": "default", "control": "123456"},
		"whitespace control": map[string]any{"palette": "default", "control": " #123456"},
		"newline control":    map[string]any{"palette": "default", "control": "#123456\n"},
		"unknown controls":   map[string]any{"palette": "default", "controls": "#123456"},
	} {
		t.Run(name, func(t *testing.T) {
			response := request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
				"expected_version": initial.Version, "appearance": appearance, "locale": "fr-FR", "theme": "dark", "allow_write": true,
			}, auth, http.StatusBadRequest)
			if response.Error == nil || response.Error.Code != "invalid_argument" {
				t.Fatalf("expected invalid_argument, got %+v", response.Error)
			}
			current := decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
			if current != initial {
				t.Fatalf("invalid patch mutated preferences: %+v", current)
			}
		})
	}
}

func TestUserPreferencesAppearanceAPIMetadataFailureRollsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	initial := decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	if _, err := repository.db.Exec(`CREATE TRIGGER reject_appearance BEFORE INSERT ON app_metadata
		WHEN NEW.key LIKE 'user_appearance:%' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": initial.Version, "appearance": map[string]any{"palette": "flexoki", "control": "#123456"}, "locale": "fr-FR", "theme": "dark", "allow_write": true,
	}, auth, http.StatusInternalServerError)
	current := decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	if current != initial {
		t.Fatalf("failed metadata write changed preferences: %+v", current)
	}
}

func TestUserPreferencesValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	initial := decodeData[domain.UserPreferences](t, request(
		t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK,
	))

	for name, body := range map[string]map[string]any{
		"unsupported locale": {"expected_version": initial.Version, "locale": "it-IT"},
		"unsupported theme":  {"expected_version": initial.Version, "theme": "neon"},
		"empty patch":        {"expected_version": initial.Version},
	} {
		t.Run(name, func(t *testing.T) {
			response := request(t, router, http.MethodPatch, "/v1/me/preferences", body, auth, http.StatusBadRequest)
			if response.Error == nil || response.Error.Code != "invalid_argument" {
				t.Fatalf("expected invalid_argument, got %+v", response.Error)
			}
		})
	}
}

func TestUserPreferencesLegacyProjectPaletteReadOnlyProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	get := func() domain.UserPreferences {
		return decodeData[domain.UserPreferences](t, request(t, router, http.MethodGet, "/v1/me/preferences", nil, auth, http.StatusOK))
	}
	initial := get()
	raw := ` {"palette":"project","accent":"#aBc123","background":"#123456"} `
	key := "user_appearance:" + initial.UserID
	stamp := "2026-01-02T03:04:05Z"
	if _, err := repository.db.Exec(`INSERT INTO app_metadata (key,value,updated_at) VALUES (?,?,?)`, key, raw, stamp); err != nil {
		t.Fatal(err)
	}
	projected := get()
	want := initial
	want.Appearance = domain.Appearance{Palette: "default", Accent: "#aBc123", Background: "#123456"}
	if projected != want {
		t.Fatalf("legacy GET projection: got=%+v want=%+v", projected, want)
	}
	request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": projected.Version, "appearance": map[string]any{"palette": "project", "control": "#123456"},
	}, auth, http.StatusBadRequest)
	if get() != want {
		t.Fatal("rejected legacy palette mutated preferences")
	}
	var value, updatedAt string
	if err := repository.db.QueryRow(`SELECT value,updated_at FROM app_metadata WHERE key=?`, key).Scan(&value, &updatedAt); err != nil || value != raw || updatedAt != stamp {
		t.Fatalf("GET or rejected PATCH rewrote stored appearance: value=%q stamp=%q err=%v", value, updatedAt, err)
	}
	custom := projected.Appearance
	custom.Control = "#CdEf12"
	saved := decodeData[domain.UserPreferences](t, request(t, router, http.MethodPatch, "/v1/me/preferences", map[string]any{
		"expected_version": projected.Version, "appearance": custom,
	}, auth, http.StatusOK))
	if saved.Version != projected.Version+1 || saved.Appearance != custom || get() != saved {
		t.Fatalf("normalized palette explicit save: %+v", saved)
	}
}
