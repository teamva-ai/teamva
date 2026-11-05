package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (a *API) setBrowserCookie(c *gin.Context, name, value string, maxAge int) {
	// The SPA and API share the loopback origin. Lax allows top-level navigation;
	// mutation admission independently checks the exact Origin and CSRF token.
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, maxAge, "/", "", a.cookieSecure, true)
}

func (a *API) clearBrowserCookie(c *gin.Context, name string) {
	a.setBrowserCookie(c, name, "", -1)
}
