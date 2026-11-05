package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type inputNormalizationFixture struct {
	repository *testRepository
	router     http.Handler
	auth       map[string]string
	computerID string
	project    domain.Project
	memory     domain.ProjectMemoryEntry
	issue      domain.Issue
	comment    domain.IssueComment
}

func newInputNormalizationFixture(t *testing.T) inputNormalizationFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	f := inputNormalizationFixture{repository: repository, router: router, auth: auth, computerID: computerID(t, repository)}
	f.project = decodeData[domain.Project](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": f.computerID, "name": "Original project", "description": "Original description",
	}, auth, http.StatusCreated))
	f.memory = decodeData[domain.ProjectMemoryEntry](t, request(t, router, http.MethodPost, "/v1/projects/"+f.project.ID+"/memories", map[string]any{
		"key": "original", "kind": "custom-kind", "title": "Original memory", "content": "Original content",
	}, auth, http.StatusCreated))
	f.issue = decodeData[domain.Issue](t, request(t, router, http.MethodPost, "/v1/projects/"+f.project.ID+"/issues", map[string]any{
		"title": "Original issue",
	}, auth, http.StatusCreated))
	f.comment = decodeData[domain.IssueComment](t, request(t, router, http.MethodPost, "/v1/issues/"+f.issue.ID+"/comments", map[string]any{
		"body": "Original comment",
	}, auth, http.StatusCreated))
	return f
}

// Snapshot every persisted column, not only row counts: rejected mixed-field
// requests must leave content, versions, revisions and transactional history intact.
// Failed HTTP operations are intentionally recorded by middleware; only those
// expected failure records are excluded, never business change records.
func inputNormalizationSnapshot(t *testing.T, repository *testRepository) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, table := range []string{"projects", "project_memory_entries", "project_memory_versions", "issues", "issue_comments", "issue_comment_revisions", "work_records"} {
		query := "SELECT * FROM " + table
		if table == "work_records" {
			query += " WHERE kind <> 'operation' OR outcome <> 'failed'"
		}
		rows, err := repository.db.QueryContext(t.Context(), query+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			records = append(records, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(encoded)
	}
	return result
}

func assertInputNormalizationError(t *testing.T, response testResponse, code string) {
	t.Helper()
	if response.Error == nil || response.Error.Code != code {
		t.Fatalf("error = %+v, want %s", response.Error, code)
	}
}

func TestInputNormalizationRejectsWhitespaceWithoutWrites(t *testing.T) {
	f := newInputNormalizationFixture(t)
	before := inputNormalizationSnapshot(t, f.repository)
	for _, whitespace := range []struct{ name, value string }{
		{"empty", ""}, {"ascii", "   "}, {"controls", "\t\r\n"},
		{"nel", "\u0085"}, {"nbsp", "\u00a0"}, {"em-space", "\u2003"}, {"unicode-mixed", " \u2028\u2029\u3000\t"},
	} {
		t.Run(whitespace.name, func(t *testing.T) {
			cases := []struct {
				name, method, path string
				body               map[string]any
			}{
				{"project-create", http.MethodPost, "/v1/projects", map[string]any{"computer_id": f.computerID, "name": whitespace.value, "description": "must not persist"}},
				{"project-update", http.MethodPatch, "/v1/projects/" + f.project.ID, map[string]any{"name": whitespace.value, "description": "must not persist", "expected_version": f.project.Version}},
				{"memory-update", http.MethodPatch, "/v1/projects/" + f.project.ID + "/memories/" + f.memory.ID, map[string]any{"title": whitespace.value, "content": "must not persist", "expected_version": f.memory.Version}},
				{"comment-create", http.MethodPost, "/v1/issues/" + f.issue.ID + "/comments", map[string]any{"body": whitespace.value}},
				{"comment-update", http.MethodPatch, "/v1/comments/" + f.comment.ID, map[string]any{"body": whitespace.value, "expected_version": f.comment.Version}},
			}
			for _, field := range []string{"key", "kind", "source_type"} {
				if field == "source_type" && whitespace.value == "" {
					continue // Explicit empty source_type retains its existing "user" default.
				}
				body := map[string]any{"key": "rejected", "kind": "free-kind", "title": "Valid title", "content": "must not persist", "source_type": "user"}
				body[field] = whitespace.value
				cases = append(cases, struct {
					name, method, path string
					body               map[string]any
				}{"memory-create-" + field, http.MethodPost, "/v1/projects/" + f.project.ID + "/memories", body})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					assertInputNormalizationError(t, request(t, f.router, tc.method, tc.path, tc.body, f.auth, http.StatusBadRequest), "invalid_argument")
					if after := inputNormalizationSnapshot(t, f.repository); !reflect.DeepEqual(after, before) {
						t.Fatal("rejected request changed persisted rows, versions, revisions or work records")
					}
				})
			}
		})
	}
}

func TestInputNormalizationPreservesOwnershipVersionAndDefaultProtection(t *testing.T) {
	f := newInputNormalizationFixture(t)
	projectPath := "/v1/projects/" + f.project.ID
	memoryPath := projectPath + "/memories/" + f.memory.ID
	commentPath := "/v1/comments/" + f.comment.ID
	f.project = decodeData[domain.Project](t, request(t, f.router, http.MethodPatch, projectPath, map[string]any{"description": "Updated", "expected_version": f.project.Version}, f.auth, http.StatusOK))
	f.memory = decodeData[domain.ProjectMemoryEntry](t, request(t, f.router, http.MethodPatch, memoryPath, map[string]any{"title": "Updated", "content": "Updated", "expected_version": f.memory.Version}, f.auth, http.StatusOK))
	f.comment = decodeData[domain.IssueComment](t, request(t, f.router, http.MethodPatch, commentPath, map[string]any{"body": "Updated", "expected_version": f.comment.Version}, f.auth, http.StatusOK))
	defaultPath := "/v1/projects/" + domain.DefaultProjectID(f.computerID)
	defaultProject := decodeData[domain.Project](t, request(t, f.router, http.MethodPatch, defaultPath, map[string]any{"description": "Editable default description", "expected_version": 1}, f.auth, http.StatusOK))
	other := createTestIdentity(t, f.repository, "normalization-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
	before := inputNormalizationSnapshot(t, f.repository)
	cases := []struct {
		name, method, path string
		body               map[string]any
		auth               map[string]string
		status             int
		code               string
	}{
		{"project-stale-valid", http.MethodPatch, projectPath, map[string]any{"name": "Valid", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"project-stale-blank", http.MethodPatch, projectPath, map[string]any{"name": "\u2003", "description": "must not persist", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"memory-stale-valid", http.MethodPatch, memoryPath, map[string]any{"title": "Valid", "content": "must not persist", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"memory-stale-blank", http.MethodPatch, memoryPath, map[string]any{"title": "\u2003", "content": "must not persist", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"comment-stale-valid", http.MethodPatch, commentPath, map[string]any{"body": "Valid", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"comment-stale-blank", http.MethodPatch, commentPath, map[string]any{"body": "\u2003", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"project-missing", http.MethodPatch, "/v1/projects/missing", map[string]any{"name": "\u2003", "expected_version": 1}, f.auth, http.StatusNotFound, "not_found"},
		{"project-other-owner", http.MethodPatch, projectPath, map[string]any{"name": "\u2003", "expected_version": 1}, otherAuth, http.StatusNotFound, "not_found"},
		{"project-create-other-owner", http.MethodPost, "/v1/projects", map[string]any{"computer_id": f.computerID, "name": "\u2003"}, otherAuth, http.StatusNotFound, "not_found"},
		{"default-other-owner", http.MethodPatch, defaultPath, map[string]any{"name": "\u2003", "expected_version": defaultProject.Version}, otherAuth, http.StatusNotFound, "not_found"},
		{"default-stale", http.MethodPatch, defaultPath, map[string]any{"name": "\u2003", "expected_version": 1}, f.auth, http.StatusConflict, "version_conflict"},
		{"default-protected", http.MethodPatch, defaultPath, map[string]any{"name": "\u2003", "description": "must not persist", "expected_version": defaultProject.Version}, f.auth, http.StatusForbidden, "protected_project"},
		{"memory-missing", http.MethodPatch, projectPath + "/memories/missing", map[string]any{"title": "\u2003", "content": "must not persist", "expected_version": 1}, f.auth, http.StatusNotFound, "not_found"},
		{"memory-other-owner", http.MethodPatch, memoryPath, map[string]any{"title": "\u2003", "content": "must not persist", "expected_version": 1}, otherAuth, http.StatusNotFound, "not_found"},
		{"memory-create-other-owner", http.MethodPost, projectPath + "/memories", map[string]any{"key": "\u2003", "kind": "custom", "title": "Valid", "content": "must not persist"}, otherAuth, http.StatusNotFound, "not_found"},
		{"comment-missing", http.MethodPatch, "/v1/comments/missing", map[string]any{"body": "\u2003", "expected_version": 1}, f.auth, http.StatusNotFound, "not_found"},
		{"comment-other-owner", http.MethodPatch, commentPath, map[string]any{"body": "\u2003", "expected_version": 1}, otherAuth, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertInputNormalizationError(t, request(t, f.router, tc.method, tc.path, tc.body, tc.auth, tc.status), tc.code)
			if after := inputNormalizationSnapshot(t, f.repository); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected request changed persisted state")
			}
		})
	}
	request(t, f.router, http.MethodPost, memoryPath+"/archive", map[string]any{"expected_version": f.memory.Version}, f.auth, http.StatusNoContent)
	before = inputNormalizationSnapshot(t, f.repository)
	assertInputNormalizationError(t, request(t, f.router, http.MethodPatch, memoryPath, map[string]any{
		"title": "\u2003", "content": "must not persist", "expected_version": f.memory.Version + 1,
	}, f.auth, http.StatusNotFound), "not_found")
	if after := inputNormalizationSnapshot(t, f.repository); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected archived memory update changed persisted state")
	}
}

func TestInputNormalizationPreservesValidAndOmittedFields(t *testing.T) {
	f := newInputNormalizationFixture(t)
	project := decodeData[domain.Project](t, request(t, f.router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": f.computerID, "name": "\u2003 Normalized project \u3000", "description": "  Preserve description \n",
	}, f.auth, http.StatusCreated))
	if project.Name != "Normalized project" || project.Description != "  Preserve description \n" {
		t.Fatalf("project normalization = %+v", project)
	}
	project = decodeData[domain.Project](t, request(t, f.router, http.MethodPatch, "/v1/projects/"+project.ID, map[string]any{
		"description": "", "expected_version": project.Version,
	}, f.auth, http.StatusOK))
	if project.Name != "Normalized project" || project.Description != "" {
		t.Fatalf("omitted name or explicitly empty description changed semantics: %+v", project)
	}
	const markdown = "\n    indented code  \n\ntext with hard break  \n\u2003"
	const freeKind = " custom/private-kind "
	const freeSource = " external/custom-source "
	memory := decodeData[domain.ProjectMemoryEntry](t, request(t, f.router, http.MethodPost, "/v1/projects/"+project.ID+"/memories", map[string]any{
		"key": "\u2003 normalized-key \u3000", "kind": freeKind, "title": "\u2003 Normalized title \u3000", "content": markdown,
		"source_type": freeSource, "source_ref": "  preserve ref  ",
	}, f.auth, http.StatusCreated))
	if memory.Key != "normalized-key" || memory.Title != "Normalized title" || memory.Kind != freeKind || memory.Content != markdown || memory.SourceType != freeSource || memory.SourceRef != "  preserve ref  " {
		t.Fatalf("memory normalization changed free metadata or Markdown: %+v", memory)
	}
	const whitespaceContent = " \t\r\n\u2003 "
	memoryPath := "/v1/projects/" + project.ID + "/memories/" + memory.ID
	memory = decodeData[domain.ProjectMemoryEntry](t, request(t, f.router, http.MethodPatch, memoryPath, map[string]any{
		"title": "\u2003 Updated title \u3000", "content": whitespaceContent, "expected_version": memory.Version,
	}, f.auth, http.StatusOK))
	if memory.Title != "Updated title" || memory.Content != whitespaceContent || memory.Kind != freeKind || memory.SourceType != freeSource {
		t.Fatalf("memory update changed content or omitted metadata: %+v", memory)
	}
	versions := decodeData[domain.Page[domain.ProjectMemoryVersion]](t, request(t, f.router, http.MethodGet, memoryPath+"/versions", nil, f.auth, http.StatusOK))
	if len(versions.Items) != 2 || versions.Items[0].Content != markdown || versions.Items[1].Content != whitespaceContent {
		t.Fatalf("memory versions did not preserve exact Markdown: %+v", versions.Items)
	}
	for i, source := range []map[string]any{{}, {"source_type": ""}} {
		body := map[string]any{"key": fmt.Sprintf("default-source-%d", i), "kind": "arbitrary", "title": "Valid", "content": "Valid"}
		for key, value := range source {
			body[key] = value
		}
		created := decodeData[domain.ProjectMemoryEntry](t, request(t, f.router, http.MethodPost, "/v1/projects/"+project.ID+"/memories", body, f.auth, http.StatusCreated))
		if created.SourceType != "user" {
			t.Fatalf("default source_type = %q", created.SourceType)
		}
	}
	commentPath := "/v1/comments/" + f.comment.ID
	unchanged := decodeData[domain.IssueComment](t, request(t, f.router, http.MethodPatch, commentPath, map[string]any{"expected_version": f.comment.Version}, f.auth, http.StatusOK))
	if unchanged.Body != f.comment.Body {
		t.Fatalf("omitted comment body changed: %+v", unchanged)
	}
	revisions := decodeData[domain.Page[domain.IssueCommentRevision]](t, request(t, f.router, http.MethodGet, commentPath+"/revisions", nil, f.auth, http.StatusOK))
	if len(revisions.Items) != 0 {
		t.Fatal("omitted body created a revision")
	}
	edited := decodeData[domain.IssueComment](t, request(t, f.router, http.MethodPatch, commentPath, map[string]any{"body": "\u2003 Edited comment \u3000", "expected_version": unchanged.Version}, f.auth, http.StatusOK))
	if edited.Body != "Edited comment" {
		t.Fatalf("comment normalization = %q", edited.Body)
	}
	revisions = decodeData[domain.Page[domain.IssueCommentRevision]](t, request(t, f.router, http.MethodGet, commentPath+"/revisions", nil, f.auth, http.StatusOK))
	if len(revisions.Items) != 1 || revisions.Items[0].Body != f.comment.Body {
		t.Fatalf("comment revisions = %+v", revisions.Items)
	}
	for _, body := range []map[string]any{{}, {"title": ""}, {"title": " \u2003\u3000\t"}} {
		issue := decodeData[domain.Issue](t, request(t, f.router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", body, f.auth, http.StatusCreated))
		if issue.Title != "新任务" {
			t.Fatalf("empty Issue title must initialize the automatic placeholder: %+v", issue)
		}
	}
	issue := decodeData[domain.Issue](t, request(t, f.router, http.MethodPatch, "/v1/issues/"+f.issue.ID, map[string]any{
		"worktree_hint": true, "expected_version": f.issue.Version,
	}, f.auth, http.StatusOK))
	if issue.Title != f.issue.Title || !issue.WorktreeHint {
		t.Fatalf("omitted Issue title changed: %+v", issue)
	}
	issue = decodeData[domain.Issue](t, request(t, f.router, http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"title": " \u2003\u3000\t", "status": "resolved", "expected_version": issue.Version,
	}, f.auth, http.StatusOK))
	if issue.Title != "" || issue.Status != domain.IssueResolved || !issue.WorktreeHint {
		t.Fatalf("empty Issue title must allow other valid updates: %+v", issue)
	}
}
