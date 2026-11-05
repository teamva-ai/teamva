package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/idna"
)

const (
	resourceNameMaxRunes      = 200
	resourceTitleMaxBytes     = 512 << 10
	resourceTitleTimeout      = 4 * time.Second
	resourceTitleMaxRedirects = 4
)

var errResourceTitleUnavailable = errors.New("resource title unavailable")

type resourceTitleResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Dependencies are private and injectable by package tests; no environment or
// user-configurable switch can bypass this dedicated outbound policy.
type resourceTitleFetcher struct {
	resolver    resourceTitleResolver
	dialContext func(context.Context, string, string) (net.Conn, error)
	timeout     time.Duration
}

func newResourceTitleFetcher() *resourceTitleFetcher {
	dialer := &net.Dialer{Timeout: resourceTitleTimeout}
	return &resourceTitleFetcher{
		resolver: net.DefaultResolver, dialContext: dialer.DialContext, timeout: resourceTitleTimeout,
	}
}

func (f *resourceTitleFetcher) fetch(parent context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, f.timeout)
	defer cancel()
	u, err := url.Parse(rawURL)
	if err != nil || f.validateURL(ctx, u) != nil {
		return "", errResourceTitleUnavailable
	}
	// Never share the default transport, proxy environment, cookie jar or browser
	// headers. Disable reuse so every redirect connection is freshly validated.
	transport := &http.Transport{
		Proxy: nil,
		// net/http may detach dialing from the request's cancellation so a
		// connection can be reused. This client never reuses it: bind DNS and
		// dialing explicitly to our complete fetch budget and parent cancellation.
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			return f.dialPublic(ctx, network, address)
		},
		DisableKeepAlives: true, DisableCompression: true,
		TLSHandshakeTimeout: f.timeout, ResponseHeaderTimeout: f.timeout,
		MaxResponseHeaderBytes: 32 << 10,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: f.timeout,
		CheckRedirect: f.checkRedirect,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errResourceTitleUnavailable
	}
	setResourceTitleHeaders(req)
	response, err := client.Do(req)
	if err != nil {
		return "", errResourceTitleUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || response.Header.Get("Content-Encoding") != "" {
		return "", errResourceTitleUnavailable
	}
	return readResourceHTMLTitle(ctx, response.Body, response.Header.Get("Content-Type"))
}

func (f *resourceTitleFetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || len(via) > resourceTitleMaxRedirects ||
		(via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
		return errResourceTitleUnavailable
	}
	setResourceTitleHeaders(req)
	return f.validateURL(req.Context(), req.URL)
}

func setResourceTitleHeaders(req *http.Request) {
	req.Header = make(http.Header)
	req.Header.Set("Accept", "text/html, application/xhtml+xml")
	req.Header.Set("User-Agent", "Teamva-ResourceTitle/1.0")
}

func (f *resourceTitleFetcher) validateURL(ctx context.Context, u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" || u.Opaque != "" {
		return errResourceTitleUnavailable
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errResourceTitleUnavailable
		}
	}
	_, err := f.publicAddresses(ctx, u.Hostname())
	return err
}

func (f *resourceTitleFetcher) publicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !resourceTitlePublicIP(ip) {
			return nil, errResourceTitleUnavailable
		}
		return []netip.Addr{ip}, nil
	}
	host, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || host == "" {
		return nil, errResourceTitleUnavailable
	}
	host = strings.ToLower(host)
	for _, suffix := range []string{"localhost", "local", "localdomain", "internal", "home.arpa"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return nil, errResourceTitleUnavailable
		}
	}
	addresses, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return nil, errResourceTitleUnavailable
	}
	// Reject the entire DNS answer if even one address is unsafe. Do not merely
	// pick its public answer and hope the next lookup returns the same address.
	for _, ip := range addresses {
		if !resourceTitlePublicIP(ip) {
			return nil, errResourceTitleUnavailable
		}
	}
	return addresses, nil
}

func (f *resourceTitleFetcher) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, errResourceTitleUnavailable
	}
	addresses, err := f.publicAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range addresses {
		// Dial an IP literal, never the hostname: rebinding cannot trigger an
		// unchecked resolver call in net.Dialer. TLS still verifies the URL host.
		conn, err := f.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err != nil {
			continue
		}
		remote, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err != nil || remote.Addr().Unmap() != ip {
			conn.Close()
			return nil, errResourceTitleUnavailable
		}
		return conn, nil
	}
	return nil, errResourceTitleUnavailable
}

// In addition to Go's unicast/private checks, deny IANA special-purpose ranges,
// translation/tunnel ranges and metadata endpoints (including Azure's virtual
// infrastructure IP). IPv6 is conservatively limited to public 2000::/3.
var resourceTitleDeniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"), netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3ffe::/16"), netip.MustParsePrefix("3fff::/20"),
}

var resourceTitleGlobalIPv6 = netip.MustParsePrefix("2000::/3")

func resourceTitlePublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Is4In6() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ip.Is6() && !resourceTitleGlobalIPv6.Contains(ip)) {
		return false
	}
	for _, prefix := range resourceTitleDeniedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func readResourceHTMLTitle(ctx context.Context, body io.Reader, contentType string) (string, error) {
	// Limit both wire bytes and decoded bytes. A closing title must occur inside
	// the budget; a truncated title is not silently accepted. Stop immediately
	// at </title> rather than downloading the rest of the page or subresources.
	reader := bufio.NewReader(io.LimitReader(body, resourceTitleMaxBytes))
	if contentType == "" {
		preview, _ := reader.Peek(512)
		contentType = http.DetectContentType(preview)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "text/html" && mediaType != "application/xhtml+xml") {
		return "", errResourceTitleUnavailable
	}
	decoded, err := charset.NewReader(reader, contentType)
	if err != nil {
		return "", errResourceTitleUnavailable
	}
	tokens := html.NewTokenizer(io.LimitReader(decoded, resourceTitleMaxBytes))
	tokens.SetMaxBuf(resourceTitleMaxBytes)
	var title strings.Builder
	inTitle := false
	ignoredDepth := 0
	for ctx.Err() == nil {
		switch tokens.Next() {
		case html.ErrorToken:
			return "", errResourceTitleUnavailable
		case html.StartTagToken:
			name, _ := tokens.TagName()
			if ignoredResourceTitleContainer(string(name)) {
				ignoredDepth++
				continue
			}
			if ignoredDepth != 0 {
				continue
			}
			if string(name) == "title" {
				inTitle = true
			} else if string(name) == "body" {
				return "", errResourceTitleUnavailable
			}
		case html.TextToken:
			if inTitle {
				// Tokenizer.Text decodes HTML entities once, including numeric ones.
				title.Write(tokens.Text())
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if ignoredDepth != 0 {
				if ignoredResourceTitleContainer(string(name)) {
					ignoredDepth--
				}
				continue
			}
			if string(name) == "title" && inTitle {
				name := truncateResourceName(strings.Join(strings.Fields(title.String()), " "))
				if name != "" {
					return name, nil
				}
				return "", errResourceTitleUnavailable
			}
		}
	}
	return "", ctx.Err()
}

func ignoredResourceTitleContainer(name string) bool {
	return name == "template" || name == "svg" || name == "math"
}

func truncateResourceName(value string) string {
	count := 0
	for index := range value {
		if count == resourceNameMaxRunes {
			return value[:index]
		}
		count++
	}
	return value
}

func fallbackResourceName(rawURL string) string {
	// Keep sharing tokens and other query/fragment data out of names and the
	// ordinary name projection in work records. The resource URL is unchanged.
	withoutFragment, _, _ := strings.Cut(rawURL, "#")
	withoutQuery, _, _ := strings.Cut(withoutFragment, "?")
	return truncateResourceName(withoutQuery)
}
