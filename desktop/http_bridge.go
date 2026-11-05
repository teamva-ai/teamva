package main

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
)

// desktopHTTPBridge keeps the WebView on Wails' own origin while forwarding a
// fixed set of same-app requests to the randomly bound loopback service. Its
// private cookie jar owns the local HttpOnly session; no token is exposed to
// JavaScript or persisted by the bridge.
type desktopHTTPBridge struct {
	app    *desktopApp
	client *http.Client

	bootstrapMu  sync.Mutex
	bootstrapped bool
}

func newDesktopHTTPBridge(app *desktopApp) http.Handler {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return &desktopHTTPBridge{app: app, client: &http.Client{Jar: jar}}
}

func (bridge *desktopHTTPBridge) ServeHTTP(writer http.ResponseWriter, incoming *http.Request) {
	if !bridge.app.waitForStartup(incoming.Context()) {
		return
	}
	bridge.app.mu.RLock()
	service, startErr := bridge.app.service, bridge.app.startErr
	bridge.app.mu.RUnlock()
	if startErr != nil || service == nil {
		if startErr == nil {
			startErr = errors.New("本地服务启动失败")
		}
		writeDesktopStartupError(writer, startErr.Error())
		return
	}

	endpoint := service.Endpoint()
	target, err := url.Parse(endpoint.Origin)
	if err != nil {
		writeDesktopStartupError(writer, err.Error())
		return
	}

	isRoot := incoming.Method == http.MethodGet && incoming.URL.Path == "/"
	if isRoot {
		bridge.bootstrapMu.Lock()
		defer bridge.bootstrapMu.Unlock()
	}
	requestURL := *target
	requestURL.Path = incoming.URL.Path
	requestURL.RawPath = incoming.URL.RawPath
	requestURL.RawQuery = incoming.URL.RawQuery
	if isRoot && !bridge.bootstrapped {
		bootstrapURL, parseErr := url.Parse(endpoint.BootstrapURL)
		if parseErr != nil {
			writeDesktopStartupError(writer, parseErr.Error())
			return
		}
		requestURL = *bootstrapURL
	}

	outgoing, err := http.NewRequestWithContext(
		incoming.Context(), incoming.Method, requestURL.String(), incoming.Body,
	)
	if err != nil {
		http.Error(writer, "invalid desktop request", http.StatusBadRequest)
		return
	}
	copyDesktopRequestHeaders(outgoing.Header, incoming.Header)
	if incoming.Method != http.MethodGet && incoming.Method != http.MethodHead {
		outgoing.Header.Set("Origin", endpoint.Origin)
		outgoing.Header.Set("Referer", endpoint.Origin+"/")
	}

	response, err := bridge.client.Do(outgoing)
	if err != nil {
		writeDesktopStartupError(writer, err.Error())
		return
	}
	defer response.Body.Close()
	if isRoot && !bridge.bootstrapped && response.StatusCode >= 200 && response.StatusCode < 300 {
		bridge.bootstrapped = true
	}
	copyDesktopResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func copyDesktopRequestHeaders(destination, source http.Header) {
	for name, values := range source {
		if desktopHopByHopHeader(name) || strings.EqualFold(name, "Cookie") ||
			strings.EqualFold(name, "Origin") || strings.EqualFold(name, "Referer") {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func copyDesktopResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		if desktopHopByHopHeader(name) || strings.EqualFold(name, "Set-Cookie") {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func desktopHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeDesktopStartupError(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(writer, "<!doctype html><meta charset=utf-8><title>Teamva</title>"+
		"<main style='font:16px system-ui;padding:48px'><h1>Teamva</h1><p>本地服务启动失败</p><pre>"+
		html.EscapeString(redactLogText(message))+"</pre></main>")
}
