package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/httpapi"
	"github.com/teamva-ai/teamva/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type networkAccessStore interface {
	SetNetworkAccess(context.Context, store.NetworkAccessConfig) error
}

// Local and LAN use ONE port number. Independent sockets on concrete LAN IPv4
// addresses preserve the loopback origin and its sessions across LAN toggles.
type networkAccess struct {
	mu          sync.Mutex
	repository  networkAccessStore
	config      store.NetworkAccessConfig
	password    *remotePassword
	current     *remoteListener
	handler     http.Handler
	listenError bool
	addresses   func(string) []string
	closed      bool
	sessions    map[[32]byte]remoteSession
}

type remoteListener struct {
	server    *http.Server
	listeners []net.Listener
	urls      []string
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
}

func (l *remoteListener) close() {
	_ = l.server.Close()
	for _, listener := range l.listeners {
		_ = listener.Close()
	}
}

func (l *remoteListener) connectionState(conn net.Conn, state http.ConnState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch state {
	case http.StateNew:
		l.conns[conn] = struct{}{}
	case http.StateClosed, http.StateHijacked:
		delete(l.conns, conn)
	}
}

func (l *remoteListener) disconnect() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for conn := range l.conns {
		_ = conn.Close()
	}
}

// Password verification only happens on login, never on assets or SSE requests.
// Attempts are globally bounded (10 burst, 1/sec), with at most two bcrypt jobs.
type remotePassword struct {
	mu       sync.Mutex
	hash     []byte
	tokens   float64
	last     time.Time
	checking chan struct{}
}

func newRemotePassword(hash string) *remotePassword {
	return &remotePassword{hash: []byte(hash), tokens: 10, last: time.Now(), checking: make(chan struct{}, 2)}
}

func (p *remotePassword) check(password string) (valid, limited bool) {
	if len(password) > 72 || len(p.hash) == 0 {
		return false, false
	}
	p.mu.Lock()
	now := time.Now()
	p.tokens = min(10, p.tokens+now.Sub(p.last).Seconds())
	p.last = now
	if p.tokens < 1 {
		p.mu.Unlock()
		return false, true
	}
	p.tokens--
	p.mu.Unlock()
	select {
	case p.checking <- struct{}{}:
		defer func() { <-p.checking }()
	default:
		return false, true
	}
	if bcrypt.CompareHashAndPassword(p.hash, []byte(password)) != nil {
		return false, false
	}
	return true, false
}

func validateRemotePassword(password string) error {
	if !utf8.ValidString(password) || strings.TrimSpace(password) == "" || utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return domain.Invalid("远程访问密码至少需要 8 个字符，且不能超过 72 个 UTF-8 字节")
	}
	for _, char := range password {
		if unicode.IsControl(char) {
			return domain.Invalid("远程访问密码不能包含控制字符")
		}
	}
	return nil
}

func hashRemotePassword(password string) (string, error) {
	if err := validateRemotePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func newNetworkAccess(repository networkAccessStore, config store.NetworkAccessConfig) (*networkAccess, error) {
	if config.Port != 0 && (config.Port < 1024 || config.Port > 65535) {
		return nil, errors.New("invalid stored remote access port")
	}
	if config.PasswordHash != "" {
		cost, err := bcrypt.Cost([]byte(config.PasswordHash))
		if err != nil || cost != bcrypt.DefaultCost {
			return nil, errors.New("invalid stored remote password verifier")
		}
	}
	if config.Enabled && config.PasswordHash == "" {
		return nil, errors.New("remote access requires a configured password")
	}
	network := &networkAccess{repository: repository, config: config, password: newRemotePassword(config.PasswordHash), sessions: make(map[[32]byte]remoteSession), addresses: externalAccessURLs}
	if err := network.restoreTrustedDevices(); err != nil {
		return nil, err
	}
	return network, nil
}

func (n *networkAccess) View() httpapi.NetworkAccessView {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.viewLocked()
}

func (n *networkAccess) viewLocked() httpapi.NetworkAccessView {
	urls := []string{}
	if n.current != nil {
		urls = append(urls, n.current.urls...)
	}
	return httpapi.NetworkAccessView{
		ExternalAccess: n.current != nil, ExternalURLs: urls, Port: n.config.Port,
		PasswordSet: n.config.PasswordHash != "", ListenError: n.listenError,
	}
}

func (n *networkAccess) prepare(port int) (*remoteListener, int, error) {
	instance := &remoteListener{conns: make(map[net.Conn]struct{})}
	instance.server = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.serveRemote(instance, w, r) }),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 90 * time.Second, ConnState: instance.connectionState,
	}
	addresses := n.addresses(strconv.Itoa(port))
	if len(addresses) == 0 {
		return nil, 0, domain.Conflict("remote_access_port_unavailable", "没有可用的局域网 IPv4 地址，请连接网络后重试", nil)
	}
	for _, address := range addresses {
		parsed, err := url.Parse(address)
		if err != nil {
			instance.close()
			return nil, 0, err
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort(parsed.Hostname(), strconv.Itoa(port)))
		if err != nil {
			instance.close()
			return nil, 0, domain.Conflict("remote_access_port_unavailable", "服务端口在局域网地址上不可用，请释放占用后重试", nil)
		}
		port = listener.Addr().(*net.TCPAddr).Port
		instance.listeners = append(instance.listeners, listener)
		instance.urls = append(instance.urls, "http://"+listener.Addr().String())
	}
	return instance, port, nil
}

func (n *networkAccess) serve(instance *remoteListener) {
	for _, listener := range instance.listeners {
		go func() {
			err := instance.server.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				n.mu.Lock()
				if n.current == instance {
					n.current, n.listenError = nil, true
				}
				n.mu.Unlock()
				instance.close()
			}
		}()
	}
}

// A failed LAN bind must not prevent the local window from opening. The user
// can release the conflicting listener or reconnect the network, then retry.
func (n *networkAccess) start(ctx context.Context, handler http.Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handler = handler
	if !n.config.Enabled {
		return
	}
	instance, port, err := n.prepare(n.config.Port)
	if err != nil {
		n.listenError = true
		return
	}
	if n.config.Port != port {
		config := n.config
		config.Port = port
		if err := n.repository.SetNetworkAccess(ctx, config); err != nil {
			instance.close()
			n.listenError = true
			return
		}
		n.config = config
		n.clearOrdinarySessionsLocked()
	}
	n.current, n.config.Port = instance, port
	n.serve(instance)
}

func (n *networkAccess) Update(ctx context.Context, update httpapi.NetworkAccessUpdate) (httpapi.NetworkAccessView, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return httpapi.NetworkAccessView{}, domain.Invalid("服务正在关闭")
	}
	if update.Enabled == nil {
		return httpapi.NetworkAccessView{}, domain.Invalid("请指定是否开启远程访问")
	}
	config := n.config
	config.Enabled = *update.Enabled
	if update.Password != "" {
		if err := validateRemotePassword(update.Password); err != nil {
			return httpapi.NetworkAccessView{}, err
		}
	}
	passwordChanged := update.Password != "" && bcrypt.CompareHashAndPassword([]byte(config.PasswordHash), []byte(update.Password)) != nil
	if passwordChanged {
		hash, err := hashRemotePassword(update.Password)
		if err != nil {
			return httpapi.NetworkAccessView{}, err
		}
		config.PasswordHash = hash
	}
	if config.Enabled && config.PasswordHash == "" {
		return httpapi.NetworkAccessView{}, domain.Invalid("开启远程访问前请先设置密码")
	}
	current := n.current
	var prepared *remoteListener
	if config.Enabled && current == nil {
		var err error
		prepared, config.Port, err = n.prepare(config.Port)
		if err != nil {
			return httpapi.NetworkAccessView{}, err
		}
	}
	if passwordChanged {
		config.TrustedDevices = nil
	}
	if err := n.repository.SetNetworkAccess(ctx, config); err != nil {
		if prepared != nil {
			prepared.close()
		}
		return httpapi.NetworkAccessView{}, err
	}
	n.config, n.listenError = config, false
	if passwordChanged {
		n.password = newRemotePassword(config.PasswordHash)
	}
	if passwordChanged {
		clear(n.sessions)
	} else if !config.Enabled || prepared != nil {
		n.clearOrdinarySessionsLocked()
	}
	if current != nil && (!config.Enabled || prepared != nil) {
		n.current = nil
		current.close() // immediately closes SSE and idle keep-alives
	} else if current != nil && passwordChanged {
		current.disconnect()
	}
	if prepared != nil {
		n.current = prepared
		n.serve(prepared)
	}
	return n.viewLocked(), nil
}

func (n *networkAccess) serveRemote(instance *remoteListener, w http.ResponseWriter, r *http.Request) {
	setLocalSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Teamva-Remote", "true")
	n.mu.Lock()
	current, handler, port := n.current, n.handler, n.config.Port
	n.mu.Unlock()
	if current == nil || current != instance {
		http.Error(w, "remote access disabled", http.StatusUnauthorized)
		return
	}
	// Only literal IP origins are supported. Never trust Host/Forwarded to bypass
	// authentication, even when this listener is reached from 127.0.0.1.
	host, requestPort, err := net.SplitHostPort(r.Host)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || ip.IsUnspecified() || requestPort != strconv.Itoa(port) {
		http.Error(w, "invalid remote host", http.StatusForbidden)
		return
	}
	if r.URL.Path == httpapi.RemoteSessionPath {
		n.remoteSessionEndpoint(instance, w, r)
		return
	}
	// Only the login shell and immutable frontend assets are public. Business
	// APIs, health, downloads and private desktop bootstrap stay behind the gate.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.URL.Path == "/remote/login" || r.URL.Path == "/favicon.svg" || strings.HasPrefix(r.URL.Path, "/assets/")) {
		handler.ServeHTTP(w, httpapi.MarkRemoteAccess(r))
		return
	}
	expires, valid := n.authenticateRemote(instance, r)
	if !valid {
		n.remoteLoginRequired(w, r)
		return
	}
	if expires.IsZero() {
		n.refreshTrustedCookie(instance, w, r)
	} else {
		// Only ordinary sessions have a deadline, including their SSE streams.
		ctx, cancel := context.WithDeadline(r.Context(), expires)
		defer cancel()
		r = r.WithContext(ctx)
	}
	r.Header.Del("Authorization") // business handlers/providers never see it
	handler.ServeHTTP(w, httpapi.MarkRemoteAccess(r))
}

func (n *networkAccess) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = true
	clear(n.sessions)
	if n.current != nil {
		n.current.close()
		n.current = nil
	}
}
