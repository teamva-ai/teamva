package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func normalizeOrigins(origins []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if value, err := CanonicalBrowserOrigin(origin); err == nil {
			allowed[value] = struct{}{}
		}
	}
	return allowed
}

// IsLoopbackRemote reports whether the TCP peer is on the loopback interface.
// Unknown or malformed peers are deliberately treated as remote.
func IsLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Local requests use configured origins. Requests admitted by the remote
// password gate must use that request's exact HTTP origin, including when a
// loopback client connects to the independent remote listener.
func (a *API) originAllowed(c *gin.Context, origin string) bool {
	if a.externalAccess && IsRemoteAccess(c.Request) {
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Scheme == "http" && strings.EqualFold(parsed.Host, c.Request.Host)
	}
	if _, ok := a.allowedOrigins[origin]; ok {
		return true
	}
	return false
}

func (a *API) cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		rawOrigin := strings.TrimSpace(c.GetHeader("Origin"))
		if rawOrigin == "" {
			c.Next()
			return
		}
		c.Header("Vary", "Origin")
		origin, err := CanonicalBrowserOrigin(rawOrigin)
		if err != nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if !a.originAllowed(c, origin) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, If-Match, Last-Event-ID, X-Request-ID, X-CSRF-Token")
		c.Header("Access-Control-Expose-Headers", "ETag, X-Request-ID")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

const (
	requestIDKey     = "request_id"
	userKey          = "authenticated_user"
	computerKey      = "authenticated_computer"
	tokenKey         = "session_token"
	sessionIDKey     = "session_id"
	sessionDeviceKey = "session_device_id"
	csrfVerifiedKey  = "csrf_verified"
)

func requestContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !validRequestID(id) {
			id = "req_" + uuid.NewString()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 150 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.:", char)) {
			return false
		}
	}
	return true
}

// authenticate accepts browser sessions exclusively through an HttpOnly
// cookie. There is no alternative Bearer/remote authentication path.
func authenticate(api *API) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieToken, cookieErr := c.Cookie(sessionCookieName)
		if cookieErr == nil && cookieToken != "" {
			authentication, err := api.store.AuthenticateSession(
				c.Request.Context(), cookieToken, sessionIdleTTL,
				sessionRotationInterval, sessionPreviousTokenTTL,
			)
			if err != nil {
				api.clearBrowserCookie(c, sessionCookieName)
				respondError(c, err)
				c.Abort()
				return
			}
			if err := api.applyBrowserAuthentication(c, cookieToken, authentication); err != nil {
				api.clearBrowserCookie(c, sessionCookieName)
				respondError(c, err)
				c.Abort()
				return
			}
			c.Next()
			return
		}
		respondError(c, domain.Unauthorized("unauthenticated", "需要登录后访问"))
		c.Abort()
	}
}

func (a *API) applyBrowserAuthentication(c *gin.Context, token string, authentication domain.SessionAuthentication) error {
	// Never let a late response overwrite a rotated cookie with its previous
	// token. This is normal concurrent-session security, not protocol compatibility.
	switch authentication.TokenMatch {
	case domain.SessionTokenMatchCurrent:
		if authentication.RotatedToken != "" {
			token = authentication.RotatedToken
		}
		a.setBrowserCookie(c, sessionCookieName, token, int(sessionIdleTTL.Seconds()))
	case domain.SessionTokenMatchPrevious:
		// Accepted only within the bounded overlap window; do not emit Set-Cookie.
	default:
		return domain.Unauthorized("invalid_session", "登录状态无效或已过期")
	}
	c.Set(userKey, authentication.User)
	c.Set(tokenKey, token)
	c.Set(sessionIDKey, authentication.Session.ID)
	c.Set(sessionDeviceKey, authentication.Session.DeviceID)
	return nil
}

func authenticatedUser(c *gin.Context) domain.User {
	value, _ := c.Get(userKey)
	user, _ := value.(domain.User)
	return user
}

func sessionToken(c *gin.Context) string {
	value, _ := c.Get(tokenKey)
	token, _ := value.(string)
	return token
}

func sessionID(c *gin.Context) string {
	value, _ := c.Get(sessionIDKey)
	id, _ := value.(string)
	return id
}

func sessionDeviceID(c *gin.Context) string {
	value, _ := c.Get(sessionDeviceKey)
	id, _ := value.(string)
	return id
}

// csrfProtection rejects cross-site state-changing requests made with cookie
// authentication. Two independent checks run:
//   - the X-CSRF-Token header must match the session's CSRF value;
//   - the Origin (or Referer when absent) must match an explicitly trusted
//     browser origin. Missing or malformed source headers fail closed.
func csrfProtection(api *API) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet ||
			c.Request.Method == http.MethodHead ||
			c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		origin, err := requestSourceOrigin(c)
		if err != nil {
			respondError(c, domain.Forbidden("csrf_origin", "请求来源不合法"))
			c.Abort()
			return
		}
		if !api.originAllowed(c, origin) {
			respondError(c, domain.Forbidden("csrf_origin", "请求来源不合法"))
			c.Abort()
			return
		}
		csrf := strings.TrimSpace(c.GetHeader("X-CSRF-Token"))
		if csrf == "" || !api.store.VerifySessionCSRF(c.Request.Context(), sessionID(c), csrf) {
			respondError(c, domain.Forbidden("csrf_failed", "CSRF 校验失败"))
			c.Abort()
			return
		}
		c.Set(csrfVerifiedKey, true)
		c.Next()
	}
}

func requestID(c *gin.Context) string {
	value, _ := c.Get(requestIDKey)
	id, _ := value.(string)
	return id
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
