package httpapi

import (
	"errors"
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// localSession lets an ordinary browser enter the local workbench without a
// desktop bridge or a secret in its URL. Admission is restricted to a JSON POST
// from an accepted origin: the exact configured origin for loopback clients, or
// the request's own origin for external peers (which already passed the access
// token gate). StartLocal still validates Host for loopback peers, so DNS
// rebinding cannot acquire this session. Mutations require the CSRF token.
func (a *API) localSession(c *gin.Context) {
	origin, err := CanonicalBrowserOrigin(c.GetHeader("Origin"))
	if err != nil || !a.originAllowed(c, origin) ||
		c.ContentType() != "application/json" ||
		(c.GetHeader("Sec-Fetch-Site") != "" && c.GetHeader("Sec-Fetch-Site") != "same-origin") {
		respondError(c, domain.Forbidden("csrf_origin", "请求来源不合法"))
		return
	}
	if a.localUserID == "" {
		c.Status(http.StatusNotFound)
		return
	}
	var request struct{}
	if !bindJSON(c, &request) {
		return
	}
	if token, _ := c.Cookie(sessionCookieName); token != "" {
		authentication, authErr := a.store.AuthenticateSession(
			c.Request.Context(), token, sessionIdleTTL, sessionRotationInterval, sessionPreviousTokenTTL,
		)
		if authErr == nil {
			if err := a.applyBrowserAuthentication(c, token, authentication); err != nil {
				respondError(c, err)
				return
			}
			a.getAuthBootstrap(c)
			return
		}
		var domainErr *domain.Error
		if !errors.As(authErr, &domainErr) || domainErr.Code != "invalid_session" {
			respondError(c, authErr)
			return
		}
	}
	meta, err := a.sessionMetaFromRequest(c)
	if err != nil {
		respondError(c, err)
		return
	}
	user, session, err := a.store.CreateLocalSession(c.Request.Context(), a.localUserID, sessionIdleTTL, meta)
	if err != nil {
		respondError(c, err)
		return
	}
	a.setBrowserCookie(c, sessionCookieName, session.Token, int(sessionIdleTTL.Seconds()))
	c.Set(userKey, user)
	c.Set(sessionIDKey, session.ID)
	a.getAuthBootstrap(c)
}
