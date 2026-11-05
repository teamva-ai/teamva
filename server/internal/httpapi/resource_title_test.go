package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

type resourceTitleResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resourceTitleResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type resourceTitleTestConn struct {
	net.Conn
	remote net.Addr
}

func (c resourceTitleTestConn) RemoteAddr() net.Addr { return c.remote }

// The real HTTP transport runs over an in-memory pipe. Neither DNS nor any
// socket can contact the public Internet, a local service or a real Provider.
func newResourceTitlePipeFetcher(t *testing.T, handler http.HandlerFunc) (*resourceTitleFetcher, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	f := newResourceTitleFetcher()
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	})
	f.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		peer, err := netip.ParseAddrPort(address)
		if err != nil {
			return nil, errors.New("test dial must receive an IP literal")
		}
		client, server := net.Pipe()
		t.Cleanup(func() { client.Close(); server.Close() })
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			defer req.Body.Close()
			requests.Add(1)
			recorder := httptest.NewRecorder()
			handler(recorder, req)
			response := recorder.Result()
			defer response.Body.Close()
			_ = response.Write(server)
		}()
		remote := &net.TCPAddr{IP: peer.Addr().AsSlice(), Port: int(peer.Port())}
		return resourceTitleTestConn{Conn: client, remote: remote}, nil
	}
	return f, requests
}

func TestResourceHTMLTitleParsing(t *testing.T) {
	gb, err := simplifiedchinese.GB18030.NewEncoder().String("<html><head><title>中文网站 &amp; 资料</title></head></html>")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"entities whitespace Chinese", "text/html; charset=utf-8", "<TITLE> \n 中文&nbsp; 网站\t&amp; &#x1F600;\r\n &#20013; </TITLE>", "中文 网站 & 😀 中"},
		{"decode exactly once", "text/html", "<title>&amp;lt;literal&amp;gt;</title>", "&lt;literal&gt;"},
		{"implicit UTF8", "text/html", "<title>中文网站 😀</title>", "中文网站 😀"},
		{"GB18030 header", "text/html; charset=gb18030", gb, "中文网站 & 资料"},
		{"GB18030 meta", "text/html", "<meta charset=gb18030>" + gb, "中文网站 & 资料"},
		{"XHTML", "application/xhtml+xml; charset=UTF-8", "<html><head><title>网站</title></head></html>", "网站"},
		{"sniff missing MIME", "", "<!doctype html><title>网站</title>", "网站"},
		{"rune boundary", "text/html; charset=utf-8", "<title>" + strings.Repeat("界😀", 101) + "</title>", strings.Repeat("界😀", 100)},
		{"ignore script and comment", "text/html", "<!-- <title>fake</title> --><script>let s='<title>fake</title>'</script><title>Real</title>", "Real"},
		{"ignore template and foreign title", "text/html", "<template><title>fake</title></template><svg><title>SVG title</title></svg><title>Real</title>", "Real"},
		{"no title", "text/html", "<html><head></head><body>content</body></html>", ""},
		{"blank title", "text/html", "<title> \n &nbsp; </title>", ""},
		{"not HTML", "text/plain", "<title>not HTML</title>", ""},
		{"JSON", "application/json", `{"title":"not HTML"}`, ""},
		{"unclosed title", "text/html", "<title>unfinished", ""},
		{"oversize before title", "text/html", strings.Repeat(" ", resourceTitleMaxBytes) + "<title>Late</title>", ""},
		{"oversize title", "text/html", "<title>" + strings.Repeat("a", resourceTitleMaxBytes) + "</title>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readResourceHTMLTitle(t.Context(), strings.NewReader(tc.body), tc.contentType)
			if got != tc.want || (err == nil) != (tc.want != "") || !utf8.ValidString(got) {
				t.Fatalf("title=%q err=%v, want=%q", got, err, tc.want)
			}
		})
	}
}

type resourceTitleCountingReader struct{ n int }

func (r *resourceTitleCountingReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = ' '
	}
	r.n += len(p)
	return len(p), nil // Deliberately infinite; the parser must stop itself.
}

func TestResourceTitleReadBudgetAndEarlyStop(t *testing.T) {
	reader := &resourceTitleCountingReader{}
	_, err := readResourceHTMLTitle(t.Context(), reader, "text/html")
	if err == nil || reader.n > resourceTitleMaxBytes {
		t.Fatalf("unbounded read: n=%d err=%v", reader.n, err)
	}
	reader = &resourceTitleCountingReader{}
	title, err := readResourceHTMLTitle(t.Context(), io.MultiReader(strings.NewReader("<title>Early</title>"), reader), "text/html")
	if title != "Early" || err != nil || reader.n > 8192 {
		t.Fatalf("downloaded remainder: title=%q n=%d err=%v", title, reader.n, err)
	}
}

func TestResourceNameFallbackDropsOnlyQueryAndFragment(t *testing.T) {
	for _, tc := range []struct{ rawURL, want string }{
		{"https://public.example/full/path", "https://public.example/full/path"},
		{"https://public.example/full/path?share_token=secret#private", "https://public.example/full/path"},
		{"https://public.example/full/path#private?share_token=secret", "https://public.example/full/path"},
		{"https://public.example/a%3Fb%23c?secret", "https://public.example/a%3Fb%23c"},
		{"https://public.example/?secret", "https://public.example/"},
		{"https://public.example/#secret", "https://public.example/"},
		{"https://public.example/?#", "https://public.example/"},
	} {
		if got := fallbackResourceName(tc.rawURL); got != tc.want {
			t.Errorf("fallback %q = %q, want %q", tc.rawURL, got, tc.want)
		}
	}
}

func TestResourceTitlePublicIPPolicy(t *testing.T) {
	for _, value := range []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.100.100.200", "127.0.0.1", "127.1.2.3",
		"169.254.169.254", "169.254.170.2", "168.63.129.16", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.175.48.1", "198.18.0.1",
		"198.19.255.255", "198.51.100.2", "203.0.113.3", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:127.0.0.1", "::ffff:8.8.8.8", "::192.168.1.1", "fe80::1", "fe80::1%en0", "fc00::1", "fd00:ec2::254",
		"ff02::1", "64:ff9b::808:808", "64:ff9b:1::1", "100::1", "2001::1", "2001:2::1", "2001:20::1", "2001:db8::1",
		"2002:7f00:1::1", "2620:4f:8000::1", "3ffe::1", "3fff::1", "5f00::1",
	} {
		t.Run(value, func(t *testing.T) {
			if resourceTitlePublicIP(netip.MustParseAddr(value)) {
				t.Fatal("accepted special-purpose address")
			}
		})
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "93.184.215.14", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !resourceTitlePublicIP(netip.MustParseAddr(value)) {
			t.Errorf("rejected public address %s", value)
		}
	}
	if resourceTitlePublicIP(netip.Addr{}) {
		t.Fatal("accepted invalid address")
	}
}

func TestResourceTitleURLAndDNSPolicy(t *testing.T) {
	for _, rawURL := range []string{
		"http://localhost/", "http://LOCALHOST./", "http://sub.localhost/", "http://host.local/", "http://metadata.google.internal/",
		"http://home.arpa/", "http://127.0.0.1/", "http://10.1.2.3/", "http://[::1]/", "http://[::ffff:8.8.8.8]/",
		"http://[fe80::1%25en0]/", "http://169.254.169.254/", "https://user:pass@public.example/", "ftp://public.example/",
		"http://public.example:0/", "http://public.example:65536/", "http:///missing",
	} {
		t.Run(rawURL, func(t *testing.T) {
			f := newResourceTitleFetcher()
			f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				t.Error("invalid host reached DNS")
				return nil, errors.New("unexpected lookup")
			})
			f.dialContext = func(context.Context, string, string) (net.Conn, error) {
				t.Error("invalid host reached dial")
				return nil, errors.New("unexpected dial")
			}
			if _, err := f.fetch(t.Context(), rawURL); err == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, tc := range []struct {
		name string
		ips  []netip.Addr
	}{
		{"empty", nil},
		{"mixed", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.1.2.3")}},
		{"mapped", []netip.Addr{netip.MustParseAddr("::ffff:8.8.8.8")}},
		{"oversize", make([]netip.Addr, 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResourceTitleFetcher()
			f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return tc.ips, nil })
			f.dialContext = func(context.Context, string, string) (net.Conn, error) {
				t.Error("unsafe DNS answer reached dial")
				return nil, errors.New("unexpected dial")
			}
			if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil {
				t.Fatal("unsafe DNS answer accepted")
			}
		})
	}
}

func TestResourceTitleRebindingAndPinnedDial(t *testing.T) {
	f := newResourceTitleFetcher()
	var lookups atomic.Int32
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	f.dialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("rebound private answer reached dial")
		return nil, errors.New("unexpected dial")
	}
	if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || lookups.Load() != 2 {
		t.Fatalf("rebind: lookups=%d err=%v", lookups.Load(), err)
	}

	f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.example:8080" {
			t.Errorf("lost original HTTP host: %q", r.Host)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<title>Pinned</title>")
	})
	dial := f.dialContext
	f.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.215.14:8080" {
			t.Errorf("not pinned to public IP: %q", address)
		}
		return dial(ctx, network, address)
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	title, err := f.fetch(t.Context(), "http://public.example:8080/")
	if err != nil || title != "Pinned" || requests.Load() != 1 {
		t.Fatalf("pinned fetch: %q %v requests=%d", title, err, requests.Load())
	}

	// Defense in depth: a dial implementation returning a different peer must
	// be closed before sending even the HTTP request.
	f.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		return resourceTitleTestConn{Conn: conn, remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}}, err
	}
	if _, err := f.fetch(t.Context(), "http://public.example:8080/"); err == nil || requests.Load() != 1 {
		t.Fatalf("mismatched peer accepted: err=%v requests=%d", err, requests.Load())
	}
}

func TestResourceTitleRedirectPolicy(t *testing.T) {
	for _, location := range []string{"http://127.0.0.1/", "http://10.1.2.3/", "http://[::ffff:8.8.8.8]/", "http://user:pass@public.example/", "ftp://public.example/"} {
		t.Run(location, func(t *testing.T) {
			f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			})
			if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || requests.Load() != 1 {
				t.Fatalf("unsafe redirect fetched: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
	t.Run("loop limit", func(t *testing.T) {
		f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/loop")
			w.WriteHeader(http.StatusFound)
		})
		if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || requests.Load() != resourceTitleMaxRedirects+1 {
			t.Fatalf("redirect limit: requests=%d err=%v", requests.Load(), err)
		}
	})
	t.Run("safe redirects have no ambient credentials", func(t *testing.T) {
		f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
			for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Referer", "X-CSRF-Token"} {
				if r.Header.Get(name) != "" {
					t.Errorf("forwarded %s", name)
				}
			}
			if r.URL.Path == "/" {
				w.Header().Set("Set-Cookie", "session=not-forwarded; Path=/")
				w.Header().Set("Location", "http://next.example/title")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<title>Redirected</title>")
		})
		title, err := f.fetch(t.Context(), "http://public.example/")
		if err != nil || title != "Redirected" || requests.Load() != 2 {
			t.Fatalf("safe redirect: title=%q requests=%d err=%v", title, requests.Load(), err)
		}
	})
	t.Run("redirect DNS is validated again", func(t *testing.T) {
		f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://next.example/title")
			w.WriteHeader(http.StatusFound)
		})
		f.resolver = resourceTitleResolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host == "next.example" {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
		})
		if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || requests.Load() != 1 {
			t.Fatalf("redirect DNS bypassed: requests=%d err=%v", requests.Load(), err)
		}
	})
	t.Run("HTTPS downgrade", func(t *testing.T) {
		f := newResourceTitleFetcher()
		f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			t.Error("downgrade reached DNS")
			return nil, errors.New("unexpected lookup")
		})
		previous, _ := http.NewRequest(http.MethodGet, "https://public.example/", nil)
		next, _ := http.NewRequest(http.MethodGet, "http://public.example/", nil)
		if f.checkRedirect(next, []*http.Request{previous}) == nil {
			t.Fatal("HTTPS downgrade allowed")
		}
	})
}

func newResourceTitleBlockedBodyFetcher(t *testing.T) (*resourceTitleFetcher, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	started, closed := make(chan struct{}), make(chan struct{})
	f := newResourceTitleFetcher()
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	})
	f.dialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		peer, err := netip.ParseAddrPort(address)
		if err != nil {
			return nil, err
		}
		client, server := net.Pipe()
		t.Cleanup(func() { client.Close(); server.Close() })
		go func() {
			defer server.Close()
			defer close(closed)
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			defer req.Body.Close()
			_, err = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nConnection: close\r\n\r\n<title>Waiting")
			if err != nil {
				return
			}
			close(started)
			var p [1]byte
			_, _ = server.Read(p[:]) // EOF only when the HTTP client closes it.
		}()
		return resourceTitleTestConn{Conn: client, remote: &net.TCPAddr{IP: peer.Addr().AsSlice(), Port: int(peer.Port())}}, nil
	}
	return f, started, closed
}

func TestResourceTitleBodyTimeoutClosesConnection(t *testing.T) {
	f, _, closed := newResourceTitleBlockedBodyFetcher(t)
	f.timeout = 30 * time.Millisecond
	start := time.Now()
	if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("body did not time out: elapsed=%v err=%v", time.Since(start), err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("body timeout left connection open")
	}
}

func TestResourceTitleResponseHeaderLimit(t *testing.T) {
	f, requests := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Large", strings.Repeat("x", 40<<10))
		_, _ = io.WriteString(w, "<title>not accepted</title>")
	})
	if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || requests.Load() != 1 {
		t.Fatalf("response headers not bounded: requests=%d err=%v", requests.Load(), err)
	}
}

func TestResourceTitleTimeoutAndCancellation(t *testing.T) {
	for _, stage := range []string{"DNS", "dial"} {
		t.Run(stage, func(t *testing.T) {
			f := newResourceTitleFetcher()
			f.timeout = 30 * time.Millisecond
			f.resolver = resourceTitleResolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				if stage == "DNS" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			})
			f.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
			start := time.Now()
			if _, err := f.fetch(t.Context(), "http://public.example/"); err == nil || time.Since(start) > time.Second {
				t.Fatalf("timeout failed: elapsed=%v err=%v", time.Since(start), err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := f.fetch(ctx, "http://public.example/"); err == nil {
				t.Fatal("cancelled fetch succeeded")
			}
		})
	}
}
