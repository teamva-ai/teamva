package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestLongObjectTitleDoesNotBlockWorkOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newTestRepository(t)
	router := newRouter(repo, nil, "work-operation", true)
	auth := testLoginHeaders(t, repo, router)
	project, err := repo.CreateProject(t.Context(), testLocalUserID(repo, t), domain.Project{
		ComputerID: computerID(t, repo), Name: "work operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, changeTitle := range []bool{true, false} {
		created := request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues",
			map[string]any{"title": strings.Repeat("界", 334)}, auth, http.StatusCreated)
		issue := decodeData[domain.Issue](t, created)
		if changeTitle {
			updated := request(t, router, http.MethodPatch, "/v1/issues/"+issue.ID,
				map[string]any{"title": "short", "expected_version": issue.Version}, auth, http.StatusOK)
			issue = decodeData[domain.Issue](t, updated)
		}
		request(t, router, http.MethodDelete,
			fmt.Sprintf("/v1/issues/%s?expected_version=%d", issue.ID, issue.Version), nil, auth, http.StatusNoContent)
	}
	rows, err := repo.db.QueryContext(t.Context(), `SELECT title FROM work_records WHERE kind='operation'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(title) || len(title) > 1000 {
			t.Fatalf("operation title not safely bounded: bytes=%d", len(title))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkOperationsDoNotUseClientTraceAsPrimaryKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newTestRepository(t)
	router := newRouter(repo, nil, "work-operation", true)
	auth := testLoginHeaders(t, repo, router)
	auth["X-Request-ID"] = "shared-trace"
	project, err := repo.CreateProject(t.Context(), testLocalUserID(repo, t), domain.Project{
		ComputerID: computerID(t, repo), Name: "work operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, title := range []string{"first operation", "second operation"} {
		issue := decodeData[domain.Issue](t, request(t, router, http.MethodPost,
			"/v1/projects/"+project.ID+"/issues", map[string]any{"title": title}, auth, http.StatusCreated))
		want[issue.ID] = title
	}
	rows, err := repo.db.QueryContext(t.Context(), `SELECT object_id,title,outcome FROM work_records WHERE kind='operation' AND request_id='shared-trace'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, outcome string
		if err := rows.Scan(&id, &title, &outcome); err != nil {
			t.Fatal(err)
		}
		if want[id] != title || outcome != "succeeded" {
			t.Fatalf("trace merged operations: id=%s title=%s outcome=%s", id, title, outcome)
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing %d operation rows", len(want))
	}
}

func TestRequestIDsAreSafeBoundedTraceMetadata(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 151), "line\nbreak", "space id", "用户"} {
		if validRequestID(id) {
			t.Fatalf("invalid trace accepted: bytes=%d", len(id))
		}
	}
	for _, id := range []string{"req_123", "trace:1.2-3", strings.Repeat("x", 150)} {
		if !validRequestID(id) {
			t.Fatal("safe trace rejected")
		}
	}
}
