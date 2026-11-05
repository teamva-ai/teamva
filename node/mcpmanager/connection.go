package mcpmanager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

var ErrConnection = errors.New("mcp_connection_failed")
var ErrAuth = errors.New("mcp_auth_required")
var ErrUnsupported = errors.New("mcp_protocol_unsupported")

func (m *Manager) Connect(ctx context.Context, scope, projectID, name string) (View, error) {
	return m.connect(ctx, scope, projectID, name, nil)
}

func (m *Manager) connect(ctx context.Context, scope, projectID, name string, handler auth.OAuthHandler) (View, error) {
	if validateIdentity(scope, projectID, name) != nil {
		return View{}, ErrInvalid
	}
	k := key(scope, projectID, name)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return View{}, ErrClosed
	}
	e := m.entries[k]
	if e == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	c, _ := decodeConfig(e.Config)
	if c["disabled"] == true {
		v := m.viewLocked(e)
		m.mu.Unlock()
		return v, ErrInvalid
	}
	// The OAuth worker owns its attempt; normal Connect cancels pending authorization.
	if handler == nil {
		stopEntry(e)
	} else {
		e.generation++
		if e.cancel != nil {
			e.cancel()
		}
		if e.session != nil {
			_ = e.session.Close()
			e.session = nil
		}
	}
	gen := e.generation
	life, cancel := context.WithCancel(m.ctx)
	e.cancel = cancel
	e.status = "connecting"
	e.errorCode = ""
	tok := e.Token
	m.mu.Unlock()
	timeout := phaseTimeout(c, "startup", 20*time.Second)
	initctx, initcancel := context.WithTimeout(ctx, timeout)
	defer initcancel()
	// A concurrent disconnect/save must also cancel initialization.
	stop := context.AfterFunc(life, initcancel)
	defer stop()
	var transport mcp.Transport
	if c["type"] == "local" {
		args := c["command"].([]any)
		argv := make([]string, len(args))
		for i, s := range args {
			argv[i] = s.(string)
		}
		cmd := exec.CommandContext(life, argv[0], argv[1:]...)
		cmd.Stderr = io.Discard
		cmd.WaitDelay = time.Second
		prepareProcessCancellation(cmd)
		cmd.Env = processEnvironment(c)
		if cwd, ok := c["cwd"].(string); ok {
			cmd.Dir = cwd
		}
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 250 * time.Millisecond}
	} else {
		endpoint := c["url"].(string)
		client := privateHTTPClient(c)
		if handler == nil && tok != nil {
			handler = &storedAuthHandler{source: tok.source(life, m, k, e)}
		}
		transport = &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, OAuthHandler: handler, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 4 * 1024 * 1024}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "Teamva MCP Manager", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	opts := &mcp.ClientSessionOptions{}
	if c["protocol"] == nil || c["protocol"] == "legacy" {
		opts.ProtocolVersion = "2025-11-25"
	}
	session, err := client.Connect(initctx, transport, opts)
	if err != nil && c["type"] == "remote" && c["protocol"] != "2026-07-28" && initctx.Err() == nil {
		hc := privateHTTPClient(c)
		if handler != nil {
			if source, sourceErr := handler.TokenSource(life); sourceErr == nil && source != nil {
				hc.Transport = &oauth2.Transport{Source: source, Base: hc.Transport}
			}
		}
		session, err = client.Connect(initctx, &mcp.SSEClientTransport{Endpoint: c["url"].(string), HTTPClient: hc, MaxEventSize: 4 * 1024 * 1024}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	}
	if err == nil && c["protocol"] == "2026-07-28" && session.InitializeResult().ProtocolVersion != "2026-07-28" {
		_ = session.Close()
		session = nil
		err = ErrUnsupported
	}
	var tools []*mcp.Tool
	var resources []*mcp.Resource
	var templates []*mcp.ResourceTemplate
	var prompts []*mcp.Prompt
	if err == nil {
		catctx, catcancel := context.WithTimeout(ctx, phaseTimeout(c, "catalog", 20*time.Second))
		defer catcancel()
		stopCat := context.AfterFunc(life, catcancel)
		defer stopCat()
		caps := session.InitializeResult().Capabilities
		if caps != nil && caps.Tools != nil {
			for t, listErr := range session.Tools(catctx, nil) {
				if listErr != nil {
					err = listErr
					break
				}
				tools = append(tools, t)
				if len(tools) > 5000 {
					err = ErrConnection
					break
				}
			}
		}
		if err == nil && caps != nil && caps.Resources != nil {
			for r, listErr := range session.Resources(catctx, nil) {
				if listErr != nil {
					err = listErr
					break
				}
				resources = append(resources, r)
				if len(resources) > 5000 {
					err = ErrConnection
					break
				}
			}
			if err == nil {
				for r, listErr := range session.ResourceTemplates(catctx, nil) {
					if listErr != nil {
						err = listErr
						break
					}
					templates = append(templates, r)
					if len(templates) > 5000 {
						err = ErrConnection
						break
					}
				}
			}
		}
		if err == nil && caps != nil && caps.Prompts != nil {
			for p, listErr := range session.Prompts(catctx, nil) {
				if listErr != nil {
					err = listErr
					break
				}
				prompts = append(prompts, p)
				if len(prompts) > 5000 {
					err = ErrConnection
					break
				}
			}
		}
	}
	if err != nil {
		cancel()
		if session != nil {
			_ = session.Close()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.entries[k] != e || e.generation != gen {
		cancel()
		if session != nil {
			_ = session.Close()
		}
		return View{}, ErrConflict
	}
	if err != nil {
		e.status = "error"
		e.errorCode = "mcp_connection_failed"
		if errors.Is(err, ErrUnsupported) {
			e.status = "unsupported"
			e.errorCode = "mcp_protocol_unsupported"
			return m.viewLocked(e), ErrUnsupported
		}
		if c["type"] == "remote" && handler == nil && oauthEnabled(c) {
			e.status = "needs_auth"
			e.errorCode = "mcp_auth_required"
		}
		return m.viewLocked(e), ErrConnection
	}
	e.session = session
	e.tools = tools
	e.resources = resources
	e.templates = templates
	e.prompts = prompts
	e.protocol = session.InitializeResult().ProtocolVersion
	e.status = "connected"
	go m.watch(k, e, gen, session)
	return m.viewLocked(e), nil
}
func (m *Manager) watch(k string, e *entry, gen uint64, s *mcp.ClientSession) {
	_ = s.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.entries[k] == e && e.generation == gen && e.session == s {
		e.session = nil
		e.status = "disconnected"
		e.errorCode = "mcp_connection_closed"
		if e.cancel != nil {
			e.cancel()
		}
	}
}
func phaseTimeout(c map[string]any, phase string, fallback time.Duration) time.Duration {
	if t, ok := c["timeout"].(map[string]any); ok {
		if n, ok := t[phase].(float64); ok {
			return time.Duration(n) * time.Millisecond
		}
	}
	return fallback
}
func processEnvironment(c map[string]any) []string {
	vals := map[string]string{}
	for _, k := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "USER", "LOGNAME", "SHELL"} {
		if v, ok := os.LookupEnv(k); ok {
			vals[k] = v
		}
	}
	if custom, ok := c["environment"].(map[string]any); ok {
		for k, v := range custom {
			vals[k] = v.(string)
		}
	}
	out := make([]string, 0, len(vals))
	for k, v := range vals {
		out = append(out, k+"="+v)
	}
	return out
}

type headerTransport struct {
	headers map[string]any
	base    http.RoundTripper
	origin  string
}

func (t *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != t.origin {
		return nil, ErrConnection
	}
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	for k, v := range t.headers {
		if http.CanonicalHeaderKey(k) == "Authorization" && r.Header.Get("Authorization") != "" {
			continue
		}
		r.Header.Set(k, v.(string))
	}
	return t.base.RoundTrip(r)
}
func privateHTTPClient(c map[string]any) *http.Client {
	endpoint, _ := c["url"].(string)
	u, _ := url.Parse(endpoint)
	headers, _ := c["headers"].(map[string]any)
	return &http.Client{Transport: &headerTransport{headers: headers, base: http.DefaultTransport, origin: u.Scheme + "://" + u.Host}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func oauthEnabled(c map[string]any) bool { return c["type"] == "remote" && c["oauth"] != false }

type storedAuthHandler struct{ source oauth2.TokenSource }

func (h *storedAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h.source, nil
}
func (h *storedAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return ErrAuth
}
