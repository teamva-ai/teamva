package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestWorkRecordsBrowserOperationsAndNavigation(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "work-records-test")
	defer local.Close(context.Background())
	origin := local.Endpoint().Origin
	client := newTestClient(t)
	session := mutateJSON[testBootstrap](t, client, origin, "POST", "/v1/auth/local-session", "", map[string]any{})
	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	project := mutateJSON[domain.Project](t, client, origin, "POST", "/v1/projects", session.CSRFToken, map[string]any{"computer_id": computers.Items[0].ID, "name": "工作记录项目", "description": "test-only-do-not-copy-context"})
	q := domain.WorkQuery{Search: project.ID}
	page := mutateJSON[domain.WorkPage](t, client, origin, "POST", "/v1/work-records/query", session.CSRFToken, q)
	if len(page.Items) != 1 || page.Items[0].ObjectID != project.ID || page.Items[0].Title != project.Name || page.Items[0].Outcome != "succeeded" {
		t.Fatalf("mutation metadata missing: %+v", page)
	}
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, []byte("test-only-do-not-copy-context")) {
		t.Fatal("body copied into journal")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "PATCH", origin, "/v1/projects/"+project.ID, session.CSRFToken, map[string]any{"expected_version": 999, "name": "wrong"}), http.StatusConflict)
	page = mutateJSON[domain.WorkPage](t, client, origin, "POST", "/v1/work-records/query", session.CSRFToken, domain.WorkQuery{Filter: &domain.WorkCondition{Field: "outcome", Operator: "eq", Value: "failed"}})
	if len(page.Items) != 1 || page.Items[0].ErrorCode == "" {
		t.Fatal("failed submission missing")
	}
	for range 2 {
		requireRemoteStatus(t, remoteProbe(t, client, "POST", origin, "/v1/work-records/navigation", session.CSRFToken, map[string]any{"id": "nav-test", "sequence": 1, "occurred_at": time.Now().Format(time.RFC3339Nano), "from": map[string]any{}, "to": map[string]any{"project_id": project.ID}}), 204)
	}
	page = mutateJSON[domain.WorkPage](t, client, origin, "POST", "/v1/work-records/query", session.CSRFToken, domain.WorkQuery{Filter: &domain.WorkCondition{Field: "kind", Operator: "eq", Value: "navigation"}})
	if page.Total != 1 {
		t.Fatal("navigation retry duplicated records")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "POST", origin, "/v1/work-records/navigation", session.CSRFToken, map[string]any{"id": "unsafe-nav", "sequence": 2, "occurred_at": time.Now().Format(time.RFC3339Nano), "from": map[string]any{}, "to": map[string]any{"project_id": project.ID}, "body": "not a permitted field"}), 400)
	requireRemoteStatus(t, remoteProbe(t, client, "POST", origin, "/v1/work-records/clear", session.CSRFToken, map[string]any{"query": map[string]any{}, "confirm": false}), 400)
	_ = getJSON[domain.Agent](t, client, origin, "/v1/work-assistant")
}
