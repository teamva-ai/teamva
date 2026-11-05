package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/httpapi"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/google/uuid"
)

const remoteSessionTTL = 12 * time.Hour

// Browser cookie storage is capped (Chrome: 400 days). This is a renewable
// transport lifetime, NOT a device trust expiration. Trust on the server has
// no deadline; authenticated access refreshes the HttpOnly cookie.
const trustedCookieLifetime = 400 * 24 * time.Hour
const maxRemoteSessions = 256

type remoteSession struct {
	host    string
	expires time.Time
	trusted bool
}

func (n *networkAccess) restoreTrustedDevices() error {
	if len(n.config.TrustedDevices) > maxRemoteSessions || (len(n.config.TrustedDevices) > 0 && n.config.PasswordHash == "") {
		return errors.New("invalid stored trusted devices")
	}
	seen := make(map[[32]byte]bool)
	for _, device := range n.config.TrustedDevices {
		decoded, err := hex.DecodeString(device.TokenHash)
		host, port, hostErr := net.SplitHostPort(device.Host)
		ip := net.ParseIP(host)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != device.TokenHash || hostErr != nil || ip == nil || ip.IsUnspecified() || portErr != nil || portNumber < 1024 || portNumber > 65535 {
			return errors.New("invalid stored trusted device")
		}
		var digest [32]byte
		copy(digest[:], decoded)
		if seen[digest] {
			return errors.New("duplicate stored trusted device")
		}
		seen[digest] = true
		n.sessions[digest] = remoteSession{host: device.Host, trusted: true}
	}
	return nil
}

// Ordinary sessions are port-scoped. Remembered credentials may follow this
// same service to a new port on the same IP, but only when its private store
// recognizes their digest. Another service's cookie never grants access.
func remoteCookieName(r *http.Request) string {
	_, port, _ := net.SplitHostPort(r.Host) // validated by the remote listener
	return "am_remote_" + port
}

func remoteCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: remoteCookieName(r), Value: value, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: maxAge}
}

func remoteError(w http.ResponseWriter, status int, code string) {
	message := map[string]string{
		"remote_auth_required":    "Remote access sign-in required.",
		"csrf_origin":             "Request origin is not allowed.",
		"invalid_content_type":    "Content-Type must be application/json.",
		"invalid_argument":        "Invalid request.",
		"remote_login_limited":    "Too many remote sign-in attempts.",
		"remote_password_invalid": "Invalid remote access password.",
		"method_not_allowed":      "HTTP method not allowed.",
		"internal":                "Remote access request failed.",
	}[code]
	if message == "" {
		message = http.StatusText(status)
	}
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = "req_" + uuid.NewString()
		w.Header().Set("X-Request-ID", requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "retryable": status == http.StatusTooManyRequests},
		"meta":  map[string]any{"request_id": requestID},
	})
}

func (n *networkAccess) authenticateRemote(instance *remoteListener, r *http.Request) (time.Time, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.current == nil || n.current != instance {
		return time.Time{}, false
	}
	_, session, ok := n.remoteCredentialLocked(r)
	if session.trusted {
		return time.Time{}, ok
	}
	return session.expires, ok
}

func sameRemoteIP(left, right string) bool {
	lh, _, le := net.SplitHostPort(left)
	rh, _, re := net.SplitHostPort(right)
	lip, rip := net.ParseIP(lh), net.ParseIP(rh)
	return le == nil && re == nil && lip != nil && rip != nil && lip.Equal(rip)
}

// n.mu must be held. Prefer permanent trust over an ordinary session. Cookies
// are shared across ports by browsers, so an old port name can carry valid
// trust after a service port change without weakening ordinary sessions.
func (n *networkAccess) remoteCredentialLocked(r *http.Request) (*http.Cookie, remoteSession, bool) {
	var ordinary *http.Cookie
	var ordinarySession remoteSession
	for _, cookie := range r.Cookies() {
		if !strings.HasPrefix(cookie.Name, "am_remote_") || len(cookie.Value) != 43 {
			continue
		}
		digest := sha256.Sum256([]byte(cookie.Value))
		session, exists := n.sessions[digest]
		if !exists {
			continue
		}
		if session.trusted {
			if sameRemoteIP(session.host, r.Host) {
				return cookie, session, true
			}
			continue
		}
		if !time.Now().Before(session.expires) {
			delete(n.sessions, digest)
			continue
		}
		if cookie.Name == remoteCookieName(r) && session.host == r.Host {
			ordinary, ordinarySession = cookie, session
		}
	}
	return ordinary, ordinarySession, ordinary != nil
}

func (n *networkAccess) refreshTrustedCookie(instance *remoteListener, w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.current == nil || n.current != instance {
		return
	}
	if cookie, session, ok := n.remoteCredentialLocked(r); ok && session.trusted {
		http.SetCookie(w, remoteCookie(r, cookie.Value, int(trustedCookieLifetime.Seconds())))
	}
}

func (n *networkAccess) clearOrdinarySessionsLocked() {
	for digest, session := range n.sessions {
		if !session.trusted {
			delete(n.sessions, digest)
		}
	}
}

func (n *networkAccess) remoteLoginRequired(w http.ResponseWriter, r *http.Request) {
	// Fetch/SSE clients receive JSON, never a successful HTML login page.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/v1/") &&
		(r.Header.Get("Sec-Fetch-Mode") == "navigate" || strings.Contains(r.Header.Get("Accept"), "text/html")) {
		http.Redirect(w, r, "/remote/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	remoteError(w, http.StatusUnauthorized, "remote_auth_required")
}

func (n *networkAccess) remoteSessionEndpoint(instance *remoteListener, w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(httpapi.RemoteSessionMethods(), r.Method) {
		w.Header().Set("Allow", strings.Join(httpapi.RemoteSessionMethods(), ", "))
		remoteError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.Method == http.MethodGet {
		if _, ok := n.authenticateRemote(instance, r); !ok {
			remoteError(w, http.StatusUnauthorized, "remote_auth_required")
			return
		}
		n.refreshTrustedCookie(instance, w, r)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// No existing session/CSRF token is available on the login page. Exact
	// Origin plus JSON and Fetch Metadata prevent cross-site/login-CSRF forms.
	if r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		remoteError(w, http.StatusForbidden, "csrf_origin")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		remoteError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	var body struct {
		Password    string `json:"password"`
		TrustDevice bool   `json:"trust_device"`
	}
	// Decode the object shape before typed values: encoding/json otherwise
	// treats an explicit null string/bool as its zero value. Invalid login
	// requests must not authenticate or consume password-verification capacity.
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		remoteError(w, http.StatusBadRequest, "invalid_argument")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		remoteError(w, http.StatusBadRequest, "invalid_argument")
		return
	}
	if _, exists := fields["password"]; !exists {
		remoteError(w, http.StatusBadRequest, "invalid_argument")
		return
	}
	for name, value := range fields {
		if string(value) == "null" || !utf8.Valid(value) {
			remoteError(w, http.StatusBadRequest, "invalid_argument")
			return
		}
		var err error
		switch name {
		case "password":
			err = json.Unmarshal(value, &body.Password)
		case "trust_device":
			err = json.Unmarshal(value, &body.TrustDevice)
		default:
			remoteError(w, http.StatusBadRequest, "invalid_argument")
			return
		}
		if err != nil {
			remoteError(w, http.StatusBadRequest, "invalid_argument")
			return
		}
	}
	if len(body.Password) > 72 {
		remoteError(w, http.StatusBadRequest, "invalid_argument")
		return
	}
	n.mu.Lock()
	password := n.password
	n.mu.Unlock()
	valid, limited := password.check(body.Password)
	if limited {
		w.Header().Set("Retry-After", "2")
		remoteError(w, http.StatusTooManyRequests, "remote_login_limited")
		return
	}
	if !valid {
		remoteError(w, http.StatusUnauthorized, "remote_password_invalid")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		remoteError(w, http.StatusInternalServerError, "internal")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.current != instance || n.password != password {
		remoteError(w, http.StatusUnauthorized, "remote_auth_required")
		return
	}
	now := time.Now()
	for digest, session := range n.sessions {
		if !session.trusted && !now.Before(session.expires) {
			delete(n.sessions, digest)
		}
	}
	var oldDigest [32]byte
	old, oldSession, hasOldCookie := n.remoteCredentialLocked(r)
	if hasOldCookie {
		oldDigest = sha256.Sum256([]byte(old.Value))
	}
	count := len(n.sessions)
	if _, replacing := n.sessions[oldDigest]; hasOldCookie && replacing {
		count--
	}
	if count >= maxRemoteSessions {
		w.Header().Set("Retry-After", "60")
		remoteError(w, http.StatusTooManyRequests, "remote_login_limited")
		return
	}
	ttl := remoteSessionTTL
	expires := now.Add(ttl)
	trusted := body.TrustDevice || (hasOldCookie && oldSession.trusted)
	if trusted {
		ttl = trustedCookieLifetime
		expires = time.Time{}
	}
	digest := sha256.Sum256([]byte(token))
	config := n.config
	// Always create a new slice: a failed store write must not mutate live state.
	config.TrustedDevices = make([]store.TrustedRemoteDevice, 0, len(n.config.TrustedDevices)+1)
	for _, device := range n.config.TrustedDevices {
		if !hasOldCookie || device.TokenHash != hex.EncodeToString(oldDigest[:]) {
			config.TrustedDevices = append(config.TrustedDevices, device)
		}
	}
	if trusted {
		config.TrustedDevices = append(config.TrustedDevices, store.TrustedRemoteDevice{TokenHash: hex.EncodeToString(digest[:]), Host: r.Host})
	}
	if trusted || len(config.TrustedDevices) != len(n.config.TrustedDevices) {
		if err := n.repository.SetNetworkAccess(r.Context(), config); err != nil {
			remoteError(w, http.StatusInternalServerError, "internal")
			return
		}
		n.config = config
	}
	if hasOldCookie {
		delete(n.sessions, oldDigest)
	}
	n.sessions[digest] = remoteSession{host: r.Host, expires: expires, trusted: trusted}
	http.SetCookie(w, remoteCookie(r, token, int(ttl.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}
