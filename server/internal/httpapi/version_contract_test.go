package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Valid payloads differ only in the version field. A malformed version must be
// rejected before any object lookup, mutation, command or mention is published.
func TestPositiveVersionContractAcrossHTTPMutations(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "version-contract", true, []string{testBrowserOrigin})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	auth := testLoginHeaders(t, repository, router)
	call := func(method, path string, body any, want int) json.RawMessage {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range auth {
			request.Header.Set(key, value)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "version-contract-only")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		encoded, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, response.StatusCode, want)
		}
		var envelope struct{ Data json.RawMessage }
		if len(encoded) > 0 && json.Unmarshal(encoded, &envelope) != nil {
			t.Fatal("response is not JSON")
		}
		return envelope.Data
	}
	var project domain.Project
	if err := json.Unmarshal(call(http.MethodPost, "/v1/projects", map[string]any{"computer_id": computerID(t, repository), "name": "Version contract"}, 201), &project); err != nil {
		t.Fatal(err)
	}
	root := "/v1/projects/" + project.ID
	var memory domain.ProjectMemoryEntry
	json.Unmarshal(call(http.MethodPost, root+"/memories", map[string]any{"key": "version", "kind": "fact", "title": "Original", "content": "Keep"}, 201), &memory)
	var issue domain.Issue
	json.Unmarshal(call(http.MethodPost, root+"/issues", map[string]any{"title": "Original"}, 201), &issue)
	var comment domain.IssueComment
	json.Unmarshal(call(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{"body": "Original"}, 201), &comment)
	var resource domain.ProjectResource
	json.Unmarshal(call(http.MethodPost, root+"/resources", map[string]any{"kind": "url", "name": "Original", "url": "https://example.test/version"}, 201), &resource)
	var note domain.Inspiration
	json.Unmarshal(call(http.MethodPost, "/v1/inspirations", map[string]any{"title": "Original", "markdown": "Keep"}, 201), &note)
	if _, err := repository.EnsureGeneralAgent(t.Context(), testLocalUserID(repository, t)); err != nil {
		t.Fatal(err)
	}
	agents, err := repository.ListAgents(t.Context(), testLocalUserID(repository, t), project.ComputerID)
	if err != nil || len(agents) == 0 {
		t.Fatal("missing built-in Agent fixture")
	}
	for _, id := range []string{project.ID, memory.ID, issue.ID, comment.ID, resource.ID, note.ID} {
		if id == "" {
			t.Fatal("HTTP setup did not produce an object")
		}
	}
	type mutation struct {
		method, path, field string
		body                map[string]any
	}
	cases := []mutation{
		{"PATCH", root, "expected_version", map[string]any{"name": "Must not change"}},
		{"PATCH", root + "/memories/" + memory.ID, "expected_version", map[string]any{"title": "Changed", "content": "Changed"}},
		{"POST", root + "/memories/" + memory.ID + "/archive", "expected_version", nil},
		{"POST", root + "/memories/" + memory.ID + "/restore", "expected_version", nil},
		{"PATCH", "/v1/issues/" + issue.ID, "expected_version", map[string]any{"title": "Changed"}},
		{"PATCH", "/v1/comments/" + comment.ID, "expected_version", map[string]any{"body": "Changed"}},
		{"PATCH", "/v1/agents/" + agents[0].ID, "expected_version", map[string]any{"note": "Changed"}},
		{"PATCH", root + "/resources/" + resource.ID, "expected_version", map[string]any{"name": "Changed"}},
		{"POST", root + "/resources/" + resource.ID + "/validate", "expected_version", nil},
		{"PATCH", "/v1/inspirations/" + note.ID, "expected_version", map[string]any{"title": "Changed"}},
		{"POST", "/v1/goals/missing/pause", "expected_version", nil},
		{"POST", "/v1/goals/missing/resume", "expected_version", nil},
		{"POST", "/v1/goals/missing/cancel", "expected_version", nil},
		{"POST", "/v1/issues/" + issue.ID + "/agent-instances/missing/pause", "expected_version", nil},
		{"POST", "/v1/issues/" + issue.ID + "/agent-instances/missing/resume", "expected_version", nil},
		{"POST", "/v1/approvals/missing/decisions", "expected_version", map[string]any{"action_digest": "test-digest", "decision": "approved"}},
		{"PATCH", "/v1/policy-rules/missing", "expected_version", map[string]any{"effect": "deny"}},
		{"POST", "/v1/issues/" + issue.ID + "/runs", "expected_issue_version", map[string]any{"issue_agent_instance_id": "missing", "input": "Changed"}},
		{"PATCH", "/v1/scheduled-tasks/missing", "expected_version", map[string]any{"name": "Future", "schedule_kind": "once", "schedule_expression": "2040-01-01T00:00:00Z", "timezone": "UTC", "prompt": []map[string]any{{"type": "text", "text": "No model call"}}}},
		{"POST", "/v1/scheduled-tasks/missing/pause", "expected_version", nil},
		{"POST", "/v1/scheduled-tasks/missing/resume", "expected_version", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			for _, version := range []any{-1, 0, nil, "1", 1.5, json.Number("9223372036854775808")} {
				body := map[string]any{tc.field: version}
				for key, value := range tc.body {
					body[key] = value
				}
				call(tc.method, tc.path, body, http.StatusBadRequest)
			}
		})
	}
	var after domain.ProjectMemoryEntry
	json.Unmarshal(call("GET", root+"/memories/"+memory.ID, nil, 200), &after)
	if after.Version != memory.Version || after.Content != memory.Content || after.DeletedAt != nil {
		t.Fatal("invalid versions changed memory state")
	}
	var afterComments domain.Page[domain.IssueComment]
	if err := json.Unmarshal(call("GET", "/v1/issues/"+issue.ID+"/comments", nil, 200), &afterComments); err != nil {
		t.Fatal(err)
	}
	if len(afterComments.Items) != 1 || afterComments.Items[0].Version != comment.Version || afterComments.Items[0].Body != comment.Body {
		t.Fatal("invalid versions changed comment state")
	}
}
