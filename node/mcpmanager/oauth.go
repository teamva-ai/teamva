package mcpmanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type AuthResult struct {
	AuthorizationURL string `json:"authorization_url,omitempty"`
	Status           string `json:"status"`
}
type storedToken struct {
	Token        *oauth2.Token    `json:"token"`
	ClientID     string           `json:"client_id"`
	ClientSecret string           `json:"client_secret,omitempty"`
	TokenURL     string           `json:"token_url"`
	AuthStyle    oauth2.AuthStyle `json:"auth_style"`
	Resource     string           `json:"resource"`
}
type authAttempt struct {
	cancel  context.CancelFunc
	server  *http.Server
	mu      sync.Mutex
	stopped bool
}

func (a *authAttempt) stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	server := a.server
	a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	if server != nil {
		_ = server.Close()
	}
}
func (a *authAttempt) setServer(server *http.Server) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return false
	}
	a.server = server
	return true
}

// oauthURL forbids non-loopback cleartext and credential-bearing endpoints.
func oauthURL(s string) error {
	if safeURL(s) != nil {
		return ErrInvalid
	}
	u, _ := url.Parse(s)
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return ErrInvalid
		}
	}
	return nil
}

type oauthTransport struct{ base http.RoundTripper }

func (t oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if oauthURL(r.URL.String()) != nil {
		return nil, ErrAuth
	}
	return t.base.RoundTrip(r)
}
func oauthHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: oauthTransport{base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, oauthHTTPClient())
}

func (m *Manager) StartAuth(ctx context.Context, scope, projectID, name string) (AuthResult, error) {
	if validateIdentity(scope, projectID, name) != nil {
		return AuthResult{}, ErrInvalid
	}
	k := key(scope, projectID, name)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return AuthResult{}, ErrClosed
	}
	e := m.entries[k]
	if e == nil {
		m.mu.Unlock()
		return AuthResult{}, ErrNotFound
	}
	c, _ := decodeConfig(e.Config)
	if !oauthEnabled(c) || c["disabled"] == true {
		m.mu.Unlock()
		return AuthResult{}, ErrInvalid
	}
	stopEntry(e)
	life, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
	a := &authAttempt{cancel: cancel}
	e.auth = a
	e.status = "auth_pending"
	e.errorCode = ""
	m.mu.Unlock()
	fail := func() (AuthResult, error) {
		a.stop()
		m.mu.Lock()
		if m.entries[k] == e && e.auth == a {
			e.auth = nil
			e.status = "needs_auth"
			e.errorCode = "mcp_auth_failed"
		}
		m.mu.Unlock()
		return AuthResult{}, ErrAuth
	}
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, 30*time.Second)
	defer discoveryCancel()
	stop := context.AfterFunc(life, discoveryCancel)
	defer stop()
	meta, resource, scopes, err := discoverOAuth(discoveryCtx, c)
	if err != nil {
		return fail()
	}
	o, _ := c["oauth"].(map[string]any)
	// Only an ephemeral loopback redirect is served. Fixed redirect configuration
	// is preserved for Providers, but may not expose a second fixed callback port.
	if o["callback_port"] != nil || o["redirect_uri"] != nil {
		return fail()
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fail()
	}
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	clientID, _ := o["client_id"].(string)
	clientSecret, _ := o["client_secret"].(string)
	style := oauth2.AuthStyleInParams
	if clientSecret != "" && (len(meta.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_basic")) {
		style = oauth2.AuthStyleInHeader
	}
	if clientID == "" {
		clientID, clientSecret, err = registerOAuth(discoveryCtx, meta, redirect)
		if err != nil {
			listener.Close()
			return fail()
		}
		if clientSecret != "" && (len(meta.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_basic")) {
			style = oauth2.AuthStyleInHeader
		}
	}
	if clientID == "" || strings.ContainsAny(clientID, "\x00\r\n") || strings.ContainsAny(clientSecret, "\x00\r\n") {
		listener.Close()
		return fail()
	}
	if configured, ok := o["scope"].(string); ok {
		scopes = strings.Fields(configured)
	}
	cfg := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect, Scopes: scopes, Endpoint: oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, AuthStyle: style}}
	state := randomString()
	verifier := oauth2.GenerateVerifier()
	authorizationURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", resource))
	codes := make(chan string, 1)
	var used bool
	var callbackMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		q, err := url.ParseQuery(r.URL.RawQuery)
		if r.Method != "GET" || r.Host != listener.Addr().String() || err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid authorization callback", http.StatusBadRequest)
			return
		}
		iss := q.Get("iss")
		if (meta.AuthorizationResponseIssParameterSupported && iss == "") || (iss != "" && strings.TrimSuffix(iss, "/") != strings.TrimSuffix(meta.Issuer, "/")) {
			http.Error(w, "Invalid issuer", http.StatusBadRequest)
			return
		}
		if q.Get("error") != "" || len(q["code"]) != 1 || q.Get("code") == "" || len(q.Get("code")) > 8192 {
			http.Error(w, "Authorization was not completed", http.StatusBadRequest)
			a.stop()
			return
		}
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if used {
			http.Error(w, "Authorization already completed", http.StatusConflict)
			return
		}
		used = true
		select {
		case codes <- q.Get("code"):
			io.WriteString(w, "Authorization received. You may close this window.")
		case <-life.Done():
			http.Error(w, "Authorization expired", http.StatusGone)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	// Publish the callback server before launching it; cancellation cannot race an
	// uninitialized server pointer. All access is synchronized by Manager.mu.
	m.mu.Lock()
	if m.closed || m.entries[k] != e || e.auth != a || life.Err() != nil {
		m.mu.Unlock()
		listener.Close()
		return fail()
	}
	if !a.setServer(server) {
		m.mu.Unlock()
		listener.Close()
		return fail()
	}
	m.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	go func() {
		defer a.stop()
		var code string
		select {
		case code = <-codes:
		case <-life.Done():
			m.authFailed(k, e, a)
			return
		}
		token, err := cfg.Exchange(oauthContext(life), code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", resource))
		if err != nil || token == nil || token.AccessToken == "" || !strings.EqualFold(token.Type(), "Bearer") {
			m.authFailed(k, e, a)
			return
		}
		st := &storedToken{Token: token, ClientID: clientID, ClientSecret: clientSecret, TokenURL: meta.TokenEndpoint, AuthStyle: style, Resource: resource}
		m.mu.Lock()
		if m.closed || m.entries[k] != e || e.auth != a || life.Err() != nil {
			m.mu.Unlock()
			return
		}
		old := e.Token
		e.Token = st
		if m.persistLocked() != nil {
			e.Token = old
			e.status = "needs_auth"
			e.errorCode = "mcp_storage_failed"
			e.auth = nil
			m.mu.Unlock()
			return
		}
		e.auth = nil
		m.mu.Unlock()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		_ = server.Shutdown(shutdownCtx)
		shutdownCancel()
		a.stop()
		// Authentication and connection are separate operations; a successful token
		// exchange persists even if this server's catalog later fails.
		_, _ = m.Connect(m.ctx, scope, projectID, name)
	}()
	return AuthResult{AuthorizationURL: authorizationURL, Status: "auth_pending"}, nil
}
func (m *Manager) authFailed(k string, e *entry, a *authAttempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.entries[k] == e && e.auth == a {
		e.auth = nil
		e.status = "needs_auth"
		e.errorCode = "mcp_auth_failed"
	}
}

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("secure random unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func discoverOAuth(ctx context.Context, c map[string]any) (*oauthex.AuthServerMeta, string, []string, error) {
	endpoint := c["url"].(string)
	if oauthURL(endpoint) != nil {
		return nil, "", nil, ErrAuth
	}
	u, _ := url.Parse(endpoint)
	client := oauthHTTPClient()
	o, _ := c["oauth"].(map[string]any)
	resource := endpoint
	var scopes []string
	var issuer, metadata string
	if override, ok := o["auth_server_metadata_url"].(string); ok && override != "" {
		metadata = override
		var raw struct {
			Issuer string `json:"issuer"`
		}
		if getOAuthJSON(ctx, metadata, &raw) != nil || oauthURL(raw.Issuer) != nil {
			return nil, "", nil, ErrAuth
		}
		issuer = raw.Issuer
	} else {
		// Probe without user headers; credentials must never be carried into metadata
		// requests or authorization-server origins.
		req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		resp, err := client.Do(req)
		metadataURL := ""
		if err == nil {
			challenges, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
			resp.Body.Close()
			for _, ch := range challenges {
				if ch.Scheme == "bearer" && ch.Params["resource_metadata"] != "" {
					metadataURL = ch.Params["resource_metadata"]
					break
				}
			}
		}
		paths := []string{}
		if metadataURL != "" {
			paths = append(paths, metadataURL)
		}
		if u.Path != "" && u.Path != "/" {
			paths = append(paths, u.Scheme+"://"+u.Host+"/.well-known/oauth-protected-resource"+u.EscapedPath())
		}
		paths = append(paths, u.Scheme+"://"+u.Host+"/.well-known/oauth-protected-resource")
		for _, p := range paths {
			if oauthURL(p) != nil {
				continue
			}
			prm, err := oauthex.GetProtectedResourceMetadata(ctx, p, endpoint, client)
			if err == nil && prm != nil && len(prm.AuthorizationServers) > 0 {
				issuer = prm.AuthorizationServers[0]
				resource = prm.Resource
				scopes = prm.ScopesSupported
				break
			}
		}
		if issuer == "" {
			issuer = u.Scheme + "://" + u.Host
		}
		if oauthURL(issuer) != nil {
			return nil, "", nil, ErrAuth
		}
		iu, _ := url.Parse(issuer)
		metadata = iu.Scheme + "://" + iu.Host + "/.well-known/oauth-authorization-server" + strings.TrimSuffix(iu.EscapedPath(), "/")
	}
	meta, err := oauthex.GetAuthServerMeta(ctx, metadata, issuer, client)
	if err != nil || meta == nil || !slices.Contains(meta.CodeChallengeMethodsSupported, "S256") {
		return nil, "", nil, ErrAuth
	}
	for _, endpoint := range []string{meta.Issuer, meta.AuthorizationEndpoint, meta.TokenEndpoint} {
		if oauthURL(endpoint) != nil {
			return nil, "", nil, ErrAuth
		}
	}
	if meta.RegistrationEndpoint != "" && oauthURL(meta.RegistrationEndpoint) != nil {
		return nil, "", nil, ErrAuth
	}
	return meta, resource, scopes, nil
}
func getOAuthJSON(ctx context.Context, endpoint string, out any) error {
	if oauthURL(endpoint) != nil {
		return ErrAuth
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return ErrAuth
	}
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return ErrAuth
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrAuth
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(b) > 1024*1024 || json.Unmarshal(b, out) != nil {
		return ErrAuth
	}
	return nil
}
func registerOAuth(ctx context.Context, meta *oauthex.AuthServerMeta, redirect string) (string, string, error) {
	if meta.RegistrationEndpoint == "" || oauthURL(meta.RegistrationEndpoint) != nil {
		return "", "", ErrAuth
	}
	body, _ := json.Marshal(map[string]any{"client_name": "Teamva", "redirect_uris": []string{redirect}, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	req, err := http.NewRequestWithContext(ctx, "POST", meta.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", ErrAuth
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return "", "", ErrAuth
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", "", ErrAuth
	}
	var result struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result) != nil || result.ClientID == "" || (len(result.RedirectURIs) > 0 && !slices.Contains(result.RedirectURIs, redirect)) {
		return "", "", ErrAuth
	}
	return result.ClientID, result.ClientSecret, nil
}

func (m *Manager) RevokeAuth(ctx context.Context, scope, projectID, name string) (View, error) {
	if validateIdentity(scope, projectID, name) != nil {
		return View{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return View{}, ErrClosed
	}
	if ctx.Err() != nil {
		return View{}, ctx.Err()
	}
	e := m.entries[key(scope, projectID, name)]
	if e == nil {
		return View{}, ErrNotFound
	}
	old := e.Token
	e.Token = nil
	if m.persistLocked() != nil {
		e.Token = old
		return View{}, ErrStorage
	}
	stopEntry(e)
	c, _ := decodeConfig(e.Config)
	e.status = baseStatus(c)
	e.errorCode = ""
	return m.viewLocked(e), nil
}

type refreshingSource struct {
	ctx context.Context
	m   *Manager
	k   string
	e   *entry
}

func (st *storedToken) source(ctx context.Context, m *Manager, k string, e *entry) oauth2.TokenSource {
	return &refreshingSource{ctx: ctx, m: m, k: k, e: e}
}
func (s *refreshingSource) Token() (*oauth2.Token, error) {
	// Token refresh is serialized per entry, including refresh-token rotation.
	s.e.tokenMu.Lock()
	defer s.e.tokenMu.Unlock()
	m := s.m
	m.mu.Lock()
	if m.closed || m.entries[s.k] != s.e || s.e.Token == nil {
		m.mu.Unlock()
		return nil, ErrAuth
	}
	st := s.e.Token
	copyToken := *st.Token
	m.mu.Unlock()
	if copyToken.Valid() {
		return &copyToken, nil
	}
	if st.Token.RefreshToken == "" || oauthURL(st.TokenURL) != nil {
		return nil, ErrAuth
	}
	cfg := &oauth2.Config{ClientID: st.ClientID, ClientSecret: st.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: st.TokenURL, AuthStyle: st.AuthStyle}}
	httpClient := oauthHTTPClient()
	httpClient.Transport = &resourceTransport{base: httpClient.Transport, endpoint: st.TokenURL, resource: st.Resource}
	token, err := cfg.TokenSource(context.WithValue(s.ctx, oauth2.HTTPClient, httpClient), &copyToken).Token()
	if err != nil || token.AccessToken == "" || !strings.EqualFold(token.Type(), "Bearer") {
		return nil, ErrAuth
	}
	next := *st
	next.Token = token
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.entries[s.k] != s.e || s.e.Token != st {
		return nil, ErrAuth
	}
	s.e.Token = &next
	if m.persistLocked() != nil {
		s.e.Token = st
		return nil, ErrStorage
	}
	return token, nil
}

// oauth2.Config.TokenSource has no token-endpoint options. Add RFC 8707's
// resource indicator only to this credential-bound token endpoint.
type resourceTransport struct {
	base               http.RoundTripper
	endpoint, resource string
}

func (t *resourceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && r.URL.String() == t.endpoint && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 128*1024+1))
		r.Body.Close()
		if err != nil || len(body) > 128*1024 {
			return nil, ErrAuth
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, ErrAuth
		}
		values.Set("resource", t.resource)
		encoded := values.Encode()
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(strings.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
	}
	return t.base.RoundTrip(r)
}
