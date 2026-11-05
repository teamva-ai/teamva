package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (a *API) getOnboarding(c *gin.Context) {
	view, err := a.store.Onboarding(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, view, 0)
}

func (a *API) updateOnboarding(c *gin.Context) {
	var request struct {
		Dismissed *bool `json:"dismissed" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	userID := authenticatedUser(c).ID
	if err := a.store.SetOnboardingDismissed(c.Request.Context(), userID, *request.Dismissed); err != nil {
		respondError(c, err)
		return
	}
	view, err := a.store.Onboarding(c.Request.Context(), userID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, view, 0)
}
