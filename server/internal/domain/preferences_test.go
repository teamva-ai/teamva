package domain

import (
	"encoding/json"
	"testing"
)

func TestNormalizeLocaleMapsSupportedOperatingSystemLanguages(t *testing.T) {
	tests := map[string]string{
		"zh-CN":      LocaleZhCN,
		"zh-Hans-SG": LocaleZhCN,
		"zh-TW":      LocaleZhTW,
		"zh-Hant-HK": LocaleZhTW,
		"en-GB":      LocaleEnUS,
		"ja":         LocaleJaJP,
		"de-AT":      LocaleDeDE,
		"fr-CA":      LocaleFrFR,
		"es-MX":      LocaleEsES,
		"pt-PT":      LocalePtBR,
		"ru":         LocaleRuRU,
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			actual, ok := NormalizeLocale(input)
			if !ok || actual != expected {
				t.Fatalf("NormalizeLocale(%q)=(%q, %v), want (%q, true)", input, actual, ok, expected)
			}
		})
	}
	if actual, ok := NormalizeLocale("it-IT"); ok || actual != "" {
		t.Fatalf("unsupported locale normalized to (%q, %v)", actual, ok)
	}
}

func TestDefaultUserPreferencesAreNotAnExplicitLanguageChoice(t *testing.T) {
	preferences := DefaultUserPreferences("usr_test", "unsupported")
	if preferences.Locale != LocaleEnUS || preferences.LocaleExplicit {
		t.Fatalf("unexpected defaults: %+v", preferences)
	}
}

func TestAppearancePalettes(t *testing.T) {
	for _, palette := range []string{"default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris"} {
		appearance := Appearance{Palette: palette, Accent: "#aB012F", Background: "#112233", Control: "#CcD012"}
		if err := appearance.Validate(); err != nil {
			t.Fatalf("palette %s rejected: %v", palette, err)
		}
	}
	if DefaultUserPreferences("usr_test", LocaleEnUS).Appearance != (Appearance{Palette: "default"}) {
		t.Fatal("default appearance must use the global default palette")
	}
}

func TestAppearanceControlJSONValidation(t *testing.T) {
	for _, color := range []string{"", "#000000", "#ffffff", "#AbCd12"} {
		encoded, err := json.Marshal(Appearance{Palette: "default", Control: color})
		if err != nil {
			t.Fatal(err)
		}
		var appearance Appearance
		if err := json.Unmarshal(encoded, &appearance); err != nil || appearance.Control != color {
			t.Fatalf("control round trip: %+v err=%v", appearance, err)
		}
	}
	for _, body := range []string{
		`null`, `{}`, `{"palette":"project"}`,
		`{"palette":"default","controls":"#123456"}`,
		`{"palette":"default","control":null}`,
		`{"palette":"default","control":123}`,
		`{"palette":"default","control":{}}`,
		`{"palette":"default","control":"#123"}`,
		`{"palette":"default","control":"#12345678"}`,
		`{"palette":"default","control":"#12345G"}`,
		`{"palette":"default","control":"123456"}`,
		`{"palette":"default","control":" #123456"}`,
		`{"palette":"default","control":"#123456\n"}`,
	} {
		var appearance Appearance
		if err := json.Unmarshal([]byte(body), &appearance); err == nil {
			t.Fatalf("accepted invalid appearance: %s", body)
		}
	}
}
