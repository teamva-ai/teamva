package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) getUserPreferences(c *gin.Context) {
	preferences, err := a.store.GetUserPreferences(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, preferences, preferences.Version)
}

func (a *API) updateUserPreferences(c *gin.Context) {
	var request struct {
		ExpectedVersion int64           `json:"expected_version" binding:"required,min=1"`
		Locale          *string         `json:"locale" binding:"omitempty,max=32"`
		Theme           *string         `json:"theme" binding:"omitempty,max=16"`
		AllowWrite      *bool           `json:"allow_write"`
		Appearance      json.RawMessage `json:"appearance"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Locale == nil && request.Theme == nil && request.AllowWrite == nil && request.Appearance == nil {
		respondError(c, domain.Invalid("至少提供一项用户设置"))
		return
	}
	userID := authenticatedUser(c).ID
	current, err := a.store.GetUserPreferences(c.Request.Context(), userID)
	if err != nil {
		respondError(c, err)
		return
	}
	locale := current.Locale
	theme := current.Theme
	allowWrite := current.AllowWrite
	changed := make([]string, 0, 4)
	var appearance *domain.Appearance
	if request.Appearance != nil {
		appearance = &domain.Appearance{}
		if err := json.Unmarshal(request.Appearance, appearance); err != nil {
			respondError(c, domain.Invalid("appearance 不符合外观配置契约"))
			return
		}
		changed = append(changed, "appearance")
	}
	if request.Locale != nil {
		canonical, ok := domain.NormalizeLocale(*request.Locale)
		if !ok {
			respondError(c, domain.Invalid("locale 不在支持的语言列表中"))
			return
		}
		locale = canonical
		changed = append(changed, "locale")
	}
	if request.Theme != nil {
		theme = strings.ToLower(strings.TrimSpace(*request.Theme))
		if !domain.IsSupportedTheme(theme) {
			respondError(c, domain.Invalid("theme 仅支持 system、light 或 dark"))
			return
		}
		changed = append(changed, "theme")
	}
	if request.AllowWrite != nil {
		allowWrite = *request.AllowWrite
		changed = append(changed, "allow_write")
	}
	// Omitted fields came from this exact snapshot. Never write them using a
	// different requested version, even if another request advances that version
	// between this read and the store's compare-and-swap.
	if current.Version != request.ExpectedVersion {
		respondError(c, domain.Conflict("version_conflict", "用户设置版本已变化",
			map[string]any{"current_version": current.Version}))
		return
	}
	updated, err := a.store.UpdateUserPreferences(
		c.Request.Context(), userID, request.ExpectedVersion, locale, theme, allowWrite,
		current.LocaleExplicit || request.Locale != nil,
		appearance,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	a.audit(c, userID, "user.preferences_updated", map[string]any{"fields": changed})
	respond(c, http.StatusOK, updated, updated.Version)
}
