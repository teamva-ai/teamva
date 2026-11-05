package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func newResourceTitleHTTPFixture(t *testing.T, f *resourceTitleFetcher) (*testRepository, *gin.Engine, map[string]string, domain.Project) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identity := createTestIdentity(t, repository, user.Email)
	headers := map[string]string{
		"Cookie": sessionCookieName + "=" + identity.token, "X-CSRF-Token": identity.csrf, "Origin": testBrowserOrigin,
	}
	project, err := repository.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "Title test"})
	if err != nil {
		t.Fatal(err)
	}
	api := &API{
		store: repository, resourceTitles: f, webOrigin: testBrowserOrigin,
		allowedOrigins: normalizeOrigins([]string{testBrowserOrigin}), localUserID: user.ID,
	}
	router := gin.New()
	router.Use(requestContext(), authenticate(api), csrfProtection(api), api.captureWorkOperation())
	path := "/v1/projects/:project_id/resources"
	router.POST(path, api.createResource)
	router.GET(path, api.listResources)
	router.PATCH(path+"/:resource_id", api.updateResource)
	return repository, router, headers, project
}

func TestCreateResourceURLAutomaticallyNamesAndPersists(t *testing.T) {
	f, fetches := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><head><title>\n 中文&nbsp; 网站 &amp; &#x1F600;\t资料 </title></head></html>")
	})
	repository, router, auth, project := newResourceTitleHTTPFixture(t, f)
	path := "/v1/projects/" + project.ID + "/resources"
	for _, name := range []any{nil, "", " \t\r\n\u3000"} {
		payload := map[string]any{"kind": "url", "url": " http://public.example/path?q=1#fragment "}
		if name != nil {
			payload["name"] = name
		}
		item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, path, payload, auth, http.StatusCreated))
		if item.Name != "中文 网站 & 😀 资料" || item.URL != strings.TrimSpace(payload["url"].(string)) || item.Status != "ready" || item.Version != 1 {
			t.Fatalf("automatic resource: %+v", item)
		}
	}
	if fetches.Load() != 3 {
		t.Fatalf("fetches=%d, want 3", fetches.Load())
	}
	userID := testLocalUserID(repository, t)
	items, err := repository.ListResources(t.Context(), userID, project.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("persisted resources: %+v %v", items, err)
	}
	for _, item := range items {
		if item.Name != "中文 网站 & 😀 资料" {
			t.Fatalf("title not persisted: %+v", item)
		}
	}
	// Renaming remains explicit and mandatory; no later fetch overwrites it.
	item := items[0]
	request(t, router, http.MethodPatch, path+"/"+item.ID, map[string]any{"name": " ", "expected_version": item.Version}, auth, http.StatusBadRequest)
	renamed := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPatch, path+"/"+item.ID,
		map[string]any{"name": " 用户名称 ", "expected_version": item.Version}, auth, http.StatusOK))
	if renamed.Name != "用户名称" || fetches.Load() != 3 {
		t.Fatalf("rename fetched/changed title: %+v fetches=%d", renamed, fetches.Load())
	}
}

func TestCreateResourceExplicitNameNeverFetches(t *testing.T) {
	f := newResourceTitleFetcher()
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("explicit name reached DNS")
		return nil, errors.New("unexpected lookup")
	})
	f.dialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("explicit name reached dial")
		return nil, errors.New("unexpected dial")
	}
	repository, router, auth, project := newResourceTitleHTTPFixture(t, f)
	for _, rawURL := range []string{"http://public.example/", "http://127.0.0.1/private", "http://host.local/"} {
		item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/resources",
			map[string]any{"kind": "url", "name": " \t自填  名称 &amp;\n", "url": rawURL}, auth, http.StatusCreated))
		if item.Name != "自填  名称 &amp;" || item.URL != rawURL {
			t.Fatalf("explicit name modified: %+v", item)
		}
	}
	items, err := repository.ListResources(t.Context(), testLocalUserID(repository, t), project.ID)
	if err != nil || len(items) != 3 || items[0].Name != "自填  名称 &amp;" {
		t.Fatalf("explicit names not persisted: %+v %v", items, err)
	}
}

func TestCreateResourceURLFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"no title", "text/html", "<html><head></head><body>body</body></html>", 200},
		{"blank title", "text/html", "<title> &nbsp; \n </title>", 200},
		{"not HTML", "text/plain", "<title>must not use this</title>", 200},
		{"server error", "text/html", "<title>error</title>", 500},
		{"oversize", "text/html", strings.Repeat(" ", resourceTitleMaxBytes) + "<title>late</title>", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fetches := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			repository, router, auth, project := newResourceTitleHTTPFixture(t, f)
			rawURL := "http://public.example/full/path?share_token=private-query#private-fragment"
			item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/resources",
				map[string]any{"kind": "url", "url": rawURL}, auth, http.StatusCreated))
			items, err := repository.ListResources(t.Context(), testLocalUserID(repository, t), project.ID)
			if err != nil || len(items) != 1 || item.Name != "http://public.example/full/path" || items[0].Name != item.Name || item.URL != rawURL || fetches.Load() != 1 {
				t.Fatalf("fallback not persisted: %+v items=%+v fetches=%d err=%v", item, items, fetches.Load(), err)
			}
			page, err := repository.QueryWorkRecords(t.Context(), testLocalUserID(repository, t), domain.WorkQuery{IncludeChanges: true, Limit: 200})
			if err != nil || len(page.Items) == 0 {
				t.Fatalf("missing work records: %+v %v", page, err)
			}
			records, err := json.Marshal(page)
			if err != nil || bytes.Contains(records, []byte("private-query")) || bytes.Contains(records, []byte("private-fragment")) {
				t.Fatalf("URL parameters copied into work records: %s %v", records, err)
			}
		})
	}
	for _, stage := range []string{"failure", "timeout", "blocked URL"} {
		t.Run(stage, func(t *testing.T) {
			f := newResourceTitleFetcher()
			f.timeout = 30 * time.Millisecond
			var lookups, dials atomic.Int32
			f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				lookups.Add(1)
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			})
			f.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				dials.Add(1)
				if stage == "timeout" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, errors.New("unreachable")
			}
			_, router, auth, project := newResourceTitleHTTPFixture(t, f)
			rawURL := "http://public.example/full/path?q=1#fragment"
			if stage == "blocked URL" {
				rawURL = "http://127.0.0.1/private"
			}
			item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/resources",
				map[string]any{"kind": "url", "url": rawURL}, auth, http.StatusCreated))
			if item.Name != fallbackResourceName(rawURL) || item.URL != rawURL || (stage == "blocked URL" && (lookups.Load() != 0 || dials.Load() != 0)) {
				t.Fatalf("fallback changed name or fetched blocked URL: %+v lookups=%d dials=%d", item, lookups.Load(), dials.Load())
			}
		})
	}
}

func TestCreateResourceAutomaticNameRuneLimit(t *testing.T) {
	f, fetches := newResourceTitlePipeFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<title>"+strings.Repeat("界😀", 130)+"</title>")
	})
	_, router, auth, project := newResourceTitleHTTPFixture(t, f)
	for _, rawURL := range []string{"http://public.example/", "http://127.0.0.1/" + strings.Repeat("界😀", 130)} {
		item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/resources",
			map[string]any{"kind": "url", "url": rawURL}, auth, http.StatusCreated))
		want := strings.Repeat("界😀", 100)
		if strings.Contains(rawURL, "127.0.0.1") {
			want = truncateResourceName(rawURL)
		}
		if item.Name != want || utf8.RuneCountInString(item.Name) != 200 || !utf8.ValidString(item.Name) || item.URL != rawURL {
			t.Fatalf("invalid truncation: %+v", item)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("blocked URL fetched: %d", fetches.Load())
	}
}

func TestCreateResourceNameAndURLValidation(t *testing.T) {
	f := newResourceTitleFetcher()
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("invalid input reached DNS")
		return nil, errors.New("unexpected lookup")
	})
	_, router, auth, project := newResourceTitleHTTPFixture(t, f)
	path := "/v1/projects/" + project.ID + "/resources"
	for _, kind := range []string{"code_directory", "document_directory"} {
		for _, name := range []any{nil, "", " \n\t\u3000", strings.Repeat("界", 201)} {
			payload := map[string]any{"kind": kind, "path": "/not-read-or-opened"}
			if name != nil {
				payload["name"] = name
			}
			request(t, router, http.MethodPost, path, payload, auth, http.StatusBadRequest)
		}
		item := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost, path,
			map[string]any{"kind": kind, "name": strings.Repeat("界", 200), "path": "/not-read-or-opened"}, auth, http.StatusCreated))
		if utf8.RuneCountInString(item.Name) != 200 {
			t.Fatal("200 rune directory name rejected/truncated")
		}
	}
	for _, rawURL := range []string{"", "https:///nohost", "ftp://public.example/", "file:///path", "https://user:pass@public.example/", "not a URL"} {
		request(t, router, http.MethodPost, path, map[string]any{"kind": "url", "url": rawURL}, auth, http.StatusBadRequest)
	}
	request(t, router, http.MethodPost, path, map[string]any{"kind": "url", "url": "http://public.example/", "path": "/forbidden"}, auth, http.StatusBadRequest)
	request(t, router, http.MethodPost, path, map[string]any{"kind": "url", "url": "http://public.example/", "name": strings.Repeat("界", 201)}, auth, http.StatusBadRequest)
}

func TestCreateResourceAuthorizesBeforeOutboundRequest(t *testing.T) {
	f := newResourceTitleFetcher()
	f.resolver = resourceTitleResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("unauthorized request reached DNS")
		return nil, errors.New("unexpected lookup")
	})
	f.dialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("unauthorized request reached dial")
		return nil, errors.New("unexpected dial")
	}
	repository, router, auth, project := newResourceTitleHTTPFixture(t, f)
	payload := map[string]any{"kind": "url", "url": "http://public.example/"}
	path := "/v1/projects/" + project.ID + "/resources"
	request(t, router, http.MethodPost, path, payload, nil, http.StatusUnauthorized)
	request(t, router, http.MethodPost, path, payload, map[string]string{"Cookie": auth["Cookie"], "Origin": testBrowserOrigin}, http.StatusForbidden)
	request(t, router, http.MethodPost, "/v1/projects/missing/resources", payload, auth, http.StatusNotFound)
	other := createTestIdentity(t, repository, "resource-title-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	request(t, router, http.MethodPost, path, payload, otherAuth, http.StatusNotFound)
	items, err := repository.ListResources(t.Context(), testLocalUserID(repository, t), project.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("unauthorized resource inserted: %+v %v", items, err)
	}
}

func TestCreateResourceCancellationDoesNotInsert(t *testing.T) {
	for _, stage := range []string{"DNS", "dial", "body"} {
		t.Run(stage, func(t *testing.T) {
			f := newResourceTitleFetcher()
			started := make(chan struct{})
			f.resolver = resourceTitleResolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				if stage == "DNS" {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			})
			f.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			var bodyClosed <-chan struct{}
			var fetchStarted <-chan struct{} = started
			if stage == "body" {
				f, fetchStarted, bodyClosed = newResourceTitleBlockedBodyFetcher(t)
			}
			repository, router, auth, project := newResourceTitleHTTPFixture(t, f)
			body, _ := json.Marshal(map[string]any{"kind": "url", "url": "http://public.example/"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+project.ID+"/resources", bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			for name, value := range auth {
				req.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { router.ServeHTTP(recorder, req); close(done) }()
			select {
			case <-fetchStarted:
			case <-time.After(time.Second):
				t.Fatal("fetch did not start")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not stop fetch promptly")
			}
			if bodyClosed != nil {
				select {
				case <-bodyClosed:
				case <-time.After(time.Second):
					t.Fatal("cancellation left body connection open")
				}
			}
			items, err := repository.ListResources(t.Context(), testLocalUserID(repository, t), project.ID)
			if err != nil || len(items) != 0 || recorder.Code != http.StatusRequestTimeout {
				t.Fatalf("cancelled request inserted resource: status=%d items=%+v err=%v", recorder.Code, items, err)
			}
		})
	}
}
