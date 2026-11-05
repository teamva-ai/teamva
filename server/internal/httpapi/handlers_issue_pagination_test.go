package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestIssueListOptionalPaginationContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newTestRepository(t)
	router := newRouterWithOrigins(repo, nil, "issue-page", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	projects := decodeData[domain.Page[domain.Project]](t, request(t, router, http.MethodGet,
		"/v1/projects?computer_id="+computerID(t, repo), nil, auth, http.StatusOK))
	base := "/v1/projects/" + projects.Items[0].ID + "/issues"
	first := decodeData[domain.Issue](t, request(t, router, http.MethodPost, base,
		map[string]any{"title": "ÄPFEL 100%_"}, auth, http.StatusCreated))
	second := decodeData[domain.Issue](t, request(t, router, http.MethodPost, base,
		map[string]any{"title": "Second", "status": "resolved"}, auth, http.StatusCreated))
	page := decodeData[store.IssuePage](t, request(t, router, http.MethodGet, base+"?limit=1", nil, auth, http.StatusOK))
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("first page: %+v", page)
	}
	next := decodeData[store.IssuePage](t, request(t, router, http.MethodGet, base+"?limit=1&offset=1", nil, auth, http.StatusOK))
	if next.Total != 2 || len(next.Items) != 1 || next.Items[0].ID != first.ID {
		t.Fatalf("second page: %+v", next)
	}
	filtered := decodeData[store.IssuePage](t, request(t, router, http.MethodGet,
		base+"?search="+url.QueryEscape(" äpfel 100%_ ")+"&status=open", nil, auth, http.StatusOK))
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != first.ID {
		t.Fatalf("filtered whole-project page: %+v", filtered)
	}
	all := decodeData[domain.Page[domain.Issue]](t, request(t, router, http.MethodGet, base, nil, auth, http.StatusOK))
	if len(all.Items) != 2 || all.Items[0].ID != first.ID {
		t.Fatalf("unpaged caller changed: %+v", all)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=", "limit=no", "offset=-1", "offset=1000001", "offset=no", "status=unknown", "search=" + strings.Repeat("x", 501)} {
		request(t, router, http.MethodGet, base+"?"+query, nil, auth, http.StatusBadRequest)
	}
	other := createTestIdentity(t, repo, "issue-page-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	request(t, router, http.MethodGet, base+"?limit=1", nil, otherAuth, http.StatusNotFound)
}
