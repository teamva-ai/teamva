package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	"github.com/teamva-ai/teamva/server/internal/dispatch"
	"github.com/teamva-ai/teamva/server/internal/httpapi"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gofrs/flock"
)

var ErrLocalAlreadyRunning = errors.New("local Teamva is already running for this data directory")

const localHTTPShutdownTimeout = 10 * time.Second

type LocalConfig struct {
	Address        string
	DataDir        string
	Version        string
	BootstrapNonce string
	ProviderPaths  map[string]string
	Logger         *log.Logger
	DisableRuntime bool
	FrontendFS     fs.FS
	// CLI-only first-run opt-in. Wails uses the Settings-managed listener.
	// AccessToken seeds a password hash, never a stored plaintext credential.
	AllowExternalAccess bool
	AccessToken         string
}

type LocalEndpoint struct {
	Origin       string `json:"origin"`
	BootstrapURL string `json:"-"`
	ReadyURL     string `json:"ready_url"`
	StateFile    string `json:"state_file"`
	// ExternalURLs lists reachable non-loopback origins when external access is
	// enabled. The access token itself is never written here or into logs.
	ExternalURLs []string `json:"external_urls,omitempty"`
}

type Local struct {
	repository *store.SQLite
	dispatcher *dispatch.Local
	server     *http.Server
	listener   net.Listener
	network    *networkAccess
	dataLock   *flock.Flock
	endpoint   LocalEndpoint
	serveErr   chan error
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
	mcp        *mcpmanager.Manager
}

func StartLocal(ctx context.Context, config LocalConfig) (_ *Local, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(config.Address) == "" {
		config.Address = "127.0.0.1:0"
	}
	if strings.TrimSpace(config.DataDir) == "" {
		value, err := DefaultLocalDataDir()
		if err != nil {
			return nil, err
		}
		config.DataDir = value
	}
	if strings.TrimSpace(config.Version) == "" {
		config.Version = "dev"
	}
	if strings.TrimSpace(config.BootstrapNonce) == "" {
		value, err := randomBootstrapNonce()
		if err != nil {
			return nil, err
		}
		config.BootstrapNonce = value
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create local data directory: %w", err)
	}
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve local data directory: %w", err)
	}
	if evaluated, evaluateErr := filepath.EvalSymlinks(dataDir); evaluateErr == nil {
		dataDir = evaluated
	}
	config.DataDir = dataDir

	var (
		dataLock        *flock.Flock
		listener        net.Listener
		repository      *store.SQLite
		localDispatcher *dispatch.Local
		network         *networkAccess
		started         bool
		mcp             *mcpmanager.Manager
	)
	defer func() {
		if started {
			return
		}
		var cleanupErr error
		if network != nil {
			network.Close()
		}
		if listener != nil {
			cleanupErr = errors.Join(cleanupErr, closeListener(listener))
		}
		if localDispatcher != nil {
			// Controller.Close terminates provider processes before waiting for
			// command handlers, so startup rollback can safely wait to completion.
			cleanupErr = errors.Join(cleanupErr, localDispatcher.Close(context.Background()))
		}
		if mcp != nil {
			cleanupErr = errors.Join(cleanupErr, mcp.Close())
		}
		if repository != nil {
			cleanupErr = errors.Join(cleanupErr, repository.Close())
		}
		if dataLock != nil {
			cleanupErr = errors.Join(cleanupErr, dataLock.Close())
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()

	dataLock = flock.New(filepath.Join(config.DataDir, ".teamva.lock"))
	locked, err := dataLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock local data directory: %w", err)
	}
	if !locked {
		closeErr := dataLock.Close()
		dataLock = nil
		return nil, errors.Join(ErrLocalAlreadyRunning, closeErr)
	}
	// There is one service port. Always reserve loopback first; enabling LAN
	// later binds concrete IPv4 interface addresses at this same port number.
	configuredHost, configuredPort, err := net.SplitHostPort(config.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid local API address: %w", err)
	}
	requestedPort, err := strconv.Atoi(configuredPort)
	if err != nil || (requestedPort != 0 && (requestedPort < 1024 || requestedPort > 65535)) {
		return nil, errors.New("service port must be 0 or between 1024 and 65535")
	}
	configuredIP := net.ParseIP(configuredHost)
	if configuredIP == nil || !configuredIP.IsLoopback() {
		if !config.AllowExternalAccess {
			return nil, errors.New("local API may only listen on a loopback address unless TEAMVA_LOCAL_ALLOW_EXTERNAL=true")
		}
		configuredHost = "127.0.0.1"
	}
	repository, err = store.OpenSQLite(ctx, filepath.Join(config.DataDir, "app.db"))
	if err != nil {
		return nil, err
	}
	if err := store.EnsureBuiltinSkillPackages(ctx, repository); err != nil {
		return nil, fmt.Errorf("seed local skills: %w", err)
	}
	user, _, err := repository.LocalIdentity()
	if err != nil {
		return nil, err
	}
	if _, err := repository.EnsureGeneralAgent(ctx, user.ID); err != nil {
		return nil, err
	}
	if err := repository.RecoverWorkOperations(ctx); err != nil {
		return nil, err
	}
	networkConfig, err := repository.NetworkAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("load remote access settings: %w", err)
	}
	// Validate stored security metadata before changing any startup settings.
	network, err = newNetworkAccess(repository, networkConfig)
	if err != nil {
		return nil, err
	}
	if configuredPort == "0" && networkConfig.Port != 0 {
		configuredPort = strconv.Itoa(networkConfig.Port)
	}
	listener, err = net.Listen("tcp", net.JoinHostPort(configuredHost, configuredPort))
	if err != nil {
		return nil, fmt.Errorf("listen on local API address: %w", err)
	}
	bindHost, bindPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return nil, fmt.Errorf("resolve local API address: %w", err)
	}
	origin := "http://" + net.JoinHostPort(bindHost, bindPort)
	port, _ := strconv.Atoi(bindPort)
	if networkConfig.Port != port {
		networkConfig.Port = port
	}
	if config.AccessToken != "" && networkConfig.PasswordHash == "" {
		hash, hashErr := hashRemotePassword(config.AccessToken)
		if hashErr != nil {
			return nil, hashErr
		}
		networkConfig.PasswordHash = hash
		networkConfig.Enabled = config.AllowExternalAccess
	}
	// Persist the unified port even when LAN is off, so restarts retain the
	// same origin. Device trust survives port changes and is bound to the IP.
	if err := repository.SetNetworkAccess(ctx, networkConfig); err != nil {
		return nil, fmt.Errorf("save service network settings: %w", err)
	}
	network, err = newNetworkAccess(repository, networkConfig)
	if err != nil {
		return nil, err
	}
	var runtime httpapi.Runtime
	mcp, err = mcpmanager.New(mcpmanager.Config{DataDir: config.DataDir})
	if err != nil {
		return nil, errors.New("无法读取本机 MCP 私有配置；原文件已保留")
	}
	if !config.DisableRuntime {
		storedPaths, err := repository.ProviderExecutablePaths(ctx)
		if err != nil {
			return nil, fmt.Errorf("load provider executable settings: %w", err)
		}
		if len(storedPaths) > 0 {
			merged := make(map[string]string, len(storedPaths)+len(config.ProviderPaths))
			for id, path := range storedPaths {
				merged[id] = path
			}
			// Explicit programmatic configuration (tests, embedded callers) wins
			// over the persisted setting.
			for id, path := range config.ProviderPaths {
				merged[id] = path
			}
			config.ProviderPaths = merged
		}
		localDispatcher, err = dispatch.NewLocal(ctx, repository, dispatch.LocalConfig{
			DataDir:       filepath.Join(config.DataDir, "runtime"),
			ProviderPaths: config.ProviderPaths,
			Logger:        config.Logger,
			MCPResolver:   mcp.Effective,
		})
		if err != nil {
			return nil, fmt.Errorf("start embedded runtime: %w", err)
		}
		runtime = localDispatcher
	}
	router := httpapi.NewLocal(repository, runtime, config.Version, httpapi.Options{
		AllowedOrigins:      []string{origin},
		WebOrigin:           origin,
		LifecycleContext:    ctx,
		LocalUserID:         user.ID,
		LocalBootstrapNonce: config.BootstrapNonce,
		ExternalAccess:      true, // only the separate password gate admits remote peers
		NetworkAccess:       network,
		MCP:                 mcp,
		Capabilities: httpapi.RuntimeCapabilities{
			Mode: "local",
			Auth: map[string]bool{
				"managed": true, "email": false, "logout": false,
			},
			Topology: map[string]bool{
				"single_computer": true, "computer_configurable": false,
			},
			Runtime: map[string]bool{
				"embedded": true, "cli_install": false,
			},
			Filesystem: map[string]bool{
				"local": true, "native_picker": false,
			},
			Database: map[string]string{"driver": "sqlite"},
		},
	})
	handler := http.Handler(router)
	if config.FrontendFS != nil {
		if info, err := fs.Stat(config.FrontendFS, "index.html"); err != nil || info.IsDir() {
			return nil, errors.New("local frontend is missing index.html; build the web app first")
		}
		handler = newSPAHandler(config.FrontendFS, router)
	}
	if config.Logger != nil {
		next := handler
		handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			// Deliberately log only the escaped path. The desktop bootstrap
			// nonce and any future query credentials must never reach logs.
			config.Logger.Printf("http request method=%s path=%s", request.Method, request.URL.EscapedPath())
			next.ServeHTTP(writer, request)
		})
	}
	network.start(ctx, handler)
	server := &http.Server{
		Handler:           secureLocalHTTP(origin, handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
	}
	stateFile := filepath.Join(config.DataDir, "runtime.json")
	endpoint := LocalEndpoint{
		Origin: origin, ReadyURL: origin + "/readyz",
		BootstrapURL: origin + "/desktop/bootstrap?nonce=" + config.BootstrapNonce,
		StateFile:    stateFile, ExternalURLs: network.View().ExternalURLs,
	}
	if err := writeRuntimeState(stateFile, endpoint); err != nil {
		return nil, err
	}
	local := &Local{
		repository: repository, dispatcher: localDispatcher, server: server, listener: listener, network: network,
		dataLock: dataLock, endpoint: endpoint, serveErr: make(chan error, 1), closeDone: make(chan struct{}),
		mcp: mcp,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			local.serveErr <- err
		}
	}()
	started = true
	return local, nil
}

func newSPAHandler(frontend fs.FS, api http.Handler) http.Handler {
	files := http.FileServer(http.FS(frontend))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/v1/") ||
			request.URL.Path == "/readyz" || request.URL.Path == "/livez" ||
			request.URL.Path == "/desktop/bootstrap" {
			api.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			api.ServeHTTP(writer, request)
			return
		}
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(frontend, path); err == nil {
			writer.Header().Set("Cache-Control", "no-cache")
			if strings.HasPrefix(path, "assets/") {
				writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(writer, request)
			return
		}
		// Missing assets/downloads must not become successful HTML responses.
		// History fallback is only for extensionless application navigation.
		if strings.HasPrefix(path, "assets/") || strings.HasPrefix(path, "downloads/") ||
			strings.HasPrefix(path, "wails/") || filepath.Ext(path) != "" {
			http.NotFound(writer, request)
			return
		}
		requestCopy := request.Clone(request.Context())
		requestCopy.URL.Path = "/"
		writer.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(writer, requestCopy)
	})
}

func setLocalSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
}

// The local listener never authenticates remote clients. Only the independent
// password-protected listener can admit them; exact Host blocks DNS rebinding.
func secureLocalHTTP(origin string, next http.Handler) http.Handler {
	parsed, _ := url.Parse(origin)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setLocalSecurityHeaders(writer)
		if httpapi.IsLoopbackRemote(request.RemoteAddr) {
			if request.Host != parsed.Host {
				// Canonicalize localhost navigation before same-origin session
				// bootstrap. Never accept arbitrary Host or forwarded headers.
				if request.Host == net.JoinHostPort("localhost", parsed.Port()) && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
					http.Redirect(writer, request, origin+request.URL.RequestURI(), http.StatusTemporaryRedirect)
					return
				}
				http.Error(writer, "invalid local host", http.StatusForbidden)
				return
			}
		} else {
			http.Error(writer, "local access required", http.StatusUnauthorized)
			return
		}
		if request.URL.Path == "/remote/login" && request.Method == http.MethodGet {
			http.Redirect(writer, request, "/workbench", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// externalAccessURLs lists concrete LAN IPv4 addresses suitable for binding.
// Never wildcard-bind: disabling LAN must leave only loopback reachable.
func externalAccessURLs(port string) []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	urls := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() || network.IP.IsLinkLocalMulticast() {
				continue
			}
			ipv4 := network.IP.To4()
			if ipv4 == nil {
				continue
			}
			urls = append(urls, "http://"+net.JoinHostPort(ipv4.String(), port))
		}
	}
	sort.Strings(urls)
	return urls
}

func (a *Local) Endpoint() LocalEndpoint {
	endpoint := a.endpoint
	if a.network != nil {
		endpoint.ExternalURLs = a.network.View().ExternalURLs
	}
	return endpoint
}

func (a *Local) Errors() <-chan error { return a.serveErr }

func (a *Local) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.closeOnce.Do(func() { go a.finishClose() })
	select {
	case <-a.closeDone:
		return a.closeErr
	case <-ctx.Done():
		// The background finalizer deliberately keeps the database and data-dir
		// lock until all dispatcher handlers have stopped. A timed-out caller may
		// return, but a second process still cannot enter the same data directory.
		return ctx.Err()
	}
}

func (a *Local) finishClose() {
	if a.network != nil {
		a.network.Close()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), localHTTPShutdownTimeout)
	serverErr := a.server.Shutdown(shutdownContext)
	cancel()
	if serverErr != nil {
		serverErr = errors.Join(serverErr, a.server.Close())
	}
	listenerErr := closeListener(a.listener)
	var dispatcherErr error
	if a.dispatcher != nil {
		dispatcherErr = a.dispatcher.Close(context.Background())
	}
	var mcpErr error
	if a.mcp != nil {
		mcpErr = a.mcp.Close()
	}
	storeErr := a.repository.Close()
	stateErr := os.Remove(a.endpoint.StateFile)
	if errors.Is(stateErr, os.ErrNotExist) {
		stateErr = nil
	}
	var lockErr error
	if a.dataLock != nil {
		lockErr = a.dataLock.Close()
	}
	a.closeErr = errors.Join(serverErr, listenerErr, dispatcherErr, mcpErr, storeErr, stateErr, lockErr)
	close(a.closeDone)
}

// DefaultLocalDataDir returns the per-user directory shared by the local
// server and native desktop capabilities such as logs and diagnostics.
func DefaultLocalDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(base, "Teamva"), nil
}

func randomBootstrapNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func isLoopbackListener(address net.Addr) bool {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeRuntimeState(path string, endpoint LocalEndpoint) error {
	payload, err := json.MarshalIndent(endpoint, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	defer func() { _ = os.Remove(temporary) }()
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return fmt.Errorf("write local runtime state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish local runtime state: %w", err)
	}
	return nil
}

func closeListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	err := listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
