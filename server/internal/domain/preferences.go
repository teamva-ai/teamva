package domain

import (
	"encoding/json"
	"strings"
)

const (
	LocaleZhCN = "zh-CN"
	LocaleZhTW = "zh-TW"
	LocaleEnUS = "en-US"
	LocaleJaJP = "ja-JP"
	LocaleDeDE = "de-DE"
	LocaleFrFR = "fr-FR"
	LocaleEsES = "es-ES"
	LocalePtBR = "pt-BR"
	LocaleRuRU = "ru-RU"

	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// UserPreferences is the server-authoritative user interface configuration.
// Browsers may cache these values for startup rendering, but authenticated
// clients must read and update this resource instead of treating local state
// as authoritative.
type UserPreferences struct {
	UserID         string     `json:"user_id"`
	Locale         string     `json:"locale"`
	LocaleExplicit bool       `json:"locale_explicit"`
	Theme          string     `json:"theme"`
	AllowWrite     bool       `json:"allow_write"`
	Appearance     Appearance `json:"appearance"`
	Version        int64      `json:"version"`
}

// Appearance replaces the entire appearance object when explicitly supplied.
// Empty custom colors mean that the selected palette supplies the color.
type Appearance struct {
	Palette    string `json:"palette"`
	Accent     string `json:"accent,omitempty"`
	Background string `json:"background,omitempty"`
	Control    string `json:"control,omitempty"`
}

func (a Appearance) Validate() error {
	switch a.Palette {
	case "default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris":
	default:
		return Invalid("appearance.palette 不在支持的配色列表中")
	}
	for _, color := range []string{a.Accent, a.Background, a.Control} {
		if color == "" {
			continue
		}
		if len(color) != 7 || color[0] != '#' {
			return Invalid("appearance 自定义颜色仅支持空字符串或 #RRGGBB")
		}
		for _, ch := range color[1:] {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
				return Invalid("appearance 自定义颜色仅支持空字符串或 #RRGGBB")
			}
		}
	}
	return nil
}

// UnmarshalJSON also rejects null, unknown fields and non-string colors rather
// than silently treating them as an omitted custom color.
func (a *Appearance) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var value Appearance
	for name, raw := range fields {
		var target *string
		switch name {
		case "palette":
			target = &value.Palette
		case "accent":
			target = &value.Accent
		case "background":
			target = &value.Background
		case "control":
			target = &value.Control
		default:
			return Invalid("appearance 包含未知字段")
		}
		if strings.TrimSpace(string(raw)) == "null" {
			return Invalid("appearance 字段必须为字符串")
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return err
		}
	}
	if err := value.Validate(); err != nil {
		return err
	}
	*a = value
	return nil
}

func DefaultUserPreferences(userID, locale string) UserPreferences {
	canonical, ok := NormalizeLocale(locale)
	if !ok {
		canonical = LocaleEnUS
	}
	return UserPreferences{
		UserID: userID, Locale: canonical, Theme: ThemeSystem,
		AllowWrite: false, LocaleExplicit: false, Version: 1,
		Appearance: Appearance{Palette: "default"},
	}
}

// NormalizeLocale maps OS/browser language tags to the closest language pack.
// Script-sensitive Chinese variants are handled before the generic language
// prefix so zh-Hant/zh-HK never fall back to Simplified Chinese.
func NormalizeLocale(value string) (string, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch {
	case normalized == "zh-hant", strings.HasPrefix(normalized, "zh-hant-"),
		normalized == "zh-tw", strings.HasPrefix(normalized, "zh-tw-"),
		normalized == "zh-hk", strings.HasPrefix(normalized, "zh-hk-"),
		normalized == "zh-mo", strings.HasPrefix(normalized, "zh-mo-"):
		return LocaleZhTW, true
	case normalized == "zh", normalized == "zh-hans", strings.HasPrefix(normalized, "zh-hans-"),
		normalized == "zh-cn", strings.HasPrefix(normalized, "zh-cn-"),
		normalized == "zh-sg", strings.HasPrefix(normalized, "zh-sg-"):
		return LocaleZhCN, true
	case normalized == "en", strings.HasPrefix(normalized, "en-"):
		return LocaleEnUS, true
	case normalized == "ja", strings.HasPrefix(normalized, "ja-"):
		return LocaleJaJP, true
	case normalized == "de", strings.HasPrefix(normalized, "de-"):
		return LocaleDeDE, true
	case normalized == "fr", strings.HasPrefix(normalized, "fr-"):
		return LocaleFrFR, true
	case normalized == "es", strings.HasPrefix(normalized, "es-"):
		return LocaleEsES, true
	case normalized == "pt", strings.HasPrefix(normalized, "pt-"):
		return LocalePtBR, true
	case normalized == "ru", strings.HasPrefix(normalized, "ru-"):
		return LocaleRuRU, true
	default:
		return "", false
	}
}

func IsSupportedTheme(value string) bool {
	switch value {
	case ThemeSystem, ThemeLight, ThemeDark:
		return true
	default:
		return false
	}
}
