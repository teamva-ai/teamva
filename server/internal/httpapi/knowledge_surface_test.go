package httpapi

import (
	"strconv"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestWorkRecordDetailAndFilterDeletionNormalPathsTCP(t *testing.T) {
	repo := newTestRepository(t)
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	project := systemSurfaceProject(t, repo, "Work detail lifecycle")
	const content = "Private body canary: not work-record metadata"
	s.call(t, "POST", "/v1/projects/"+project.ID+"/memories", map[string]any{
		"key": "work-detail", "kind": "fact", "title": "Work detail memory", "content": content,
	}, 201)
	query := map[string]any{"include_changes": true, "filter": map[string]any{
		"field": "project_id", "operator": "eq", "value": project.ID,
	}}
	page := decodeData[domain.Page[domain.WorkRecord]](t, s.call(t, "POST", "/v1/work-records/query", query, 200))
	if len(page.Items) == 0 {
		t.Fatal("successful mutation did not produce transactional work history")
	}
	record := page.Items[0]
	response := s.call(t, "GET", "/v1/work-records/"+record.ID, nil, 200)
	detail := decodeData[struct {
		Record   domain.WorkRecord `json:"record"`
		Children []any             `json:"children"`
		Links    []any             `json:"links"`
	}](t, response)
	if detail.Record.ID != record.ID || detail.Children == nil || detail.Links == nil || strings.Contains(string(response.Data), content) {
		t.Fatal("work detail is incomplete or copied body content")
	}
	filter := decodeData[domain.WorkFilter](t, s.call(t, "POST", "/v1/work-record-filters", map[string]any{
		"name": "Scoped history", "query": query,
	}, 200))
	before := decodeData[domain.Page[domain.WorkFilter]](t, s.call(t, "GET", "/v1/work-record-filters", nil, 200))
	if len(before.Items) != 1 || before.Items[0].ID != filter.ID {
		t.Fatal("saved filter was not readable")
	}
	s.call(t, "DELETE", "/v1/work-record-filters/"+filter.ID, nil, 204)
	after := decodeData[domain.Page[domain.WorkFilter]](t, s.call(t, "GET", "/v1/work-record-filters", nil, 200))
	if len(after.Items) != 0 {
		t.Fatal("deleted filter remained in collection")
	}
	s.call(t, "GET", "/v1/work-records/"+record.ID, nil, 200)
}

func TestInspirationDetailGroupsAndDeletionNormalPathsTCP(t *testing.T) {
	repo := newTestRepository(t)
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	project := systemSurfaceProject(t, repo, "Inspiration lifecycle")
	headers := map[string]string{}
	for key, value := range s.auth {
		headers[key] = value
	}
	headers["Idempotency-Key"] = "knowledge-inspiration-fixture"
	const markdown = "# Complete content\n\nMarkdown 🧪 must survive exactly."
	note := decodeData[domain.Inspiration](t, s.callHeaders(t, "POST", "/v1/inspirations", map[string]any{
		"project_id": project.ID, "title": "Grouped note", "markdown": markdown,
	}, headers, 201))
	path := "/v1/inspirations/" + note.ID
	read := decodeData[domain.Inspiration](t, s.call(t, "GET", path, nil, 200))
	if read.ID != note.ID || read.Markdown != markdown || read.ProjectID != project.ID {
		t.Fatal("inspiration detail lost content or grouping")
	}
	groups := decodeData[struct {
		Items []struct {
			ProjectID string `json:"project_id"`
			Total     int    `json:"total"`
		} `json:"items"`
	}](t, s.call(t, "GET", "/v1/inspirations/groups", nil, 200))
	found := false
	for _, group := range groups.Items {
		if group.ProjectID == project.ID && group.Total == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("global inspiration groups omitted the persisted note")
	}
	updated := decodeData[domain.Inspiration](t, s.call(t, "PATCH", path, map[string]any{
		"expected_version": note.Version, "completed": true,
	}, 200))
	s.call(t, "DELETE", path+"?expected_version="+strconv.FormatInt(note.Version, 10), nil, 409)
	s.call(t, "GET", path, nil, 200)
	s.call(t, "DELETE", path+"?expected_version="+strconv.FormatInt(updated.Version, 10), nil, 204)
	s.call(t, "GET", path, nil, 404)
	page := decodeData[domain.Page[domain.Inspiration]](t, s.call(t, "GET", "/v1/inspirations?project_id="+project.ID, nil, 200))
	if len(page.Items) != 0 {
		t.Fatal("deleted inspiration remained in its group")
	}
}
