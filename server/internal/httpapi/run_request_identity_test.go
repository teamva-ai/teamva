package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestIssueRunOmittedAndEmptyAttachmentsAreSameRequestTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	path := "/v1/issues/" + f.issue.ID + "/runs"
	body := f.issueRunBody("same request without attachments")
	first := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, path, body, "empty-attachments", http.StatusAccepted))
	body["attachments"] = []domain.ContentBlock{}
	second := f.request(http.MethodPost, path, body, "empty-attachments", http.StatusOK)
	replayed := runtimeSurfaceData[domain.Run](t, second)
	if replayed.ID != first.ID || !second.Meta.Replayed {
		t.Fatal("equivalent empty attachment representations did not replay")
	}
}

func TestIssueRunInternalKeyCannotMasqueradeAsHTTPRequestTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	const key = "internal-runtime-fixture-key"
	created, _, err := f.repo.CreateRun(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, key, domain.Run{
		IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
		Input: []domain.ContentBlock{{Type: "text", Text: "Generated internal context", Internal: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodPost, "/v1/issues/"+f.issue.ID+"/runs", f.issueRunBody("Different HTTP user intent"), key, http.StatusConflict)
	if response.Error == nil || response.Error.Code != "idempotency_conflict" {
		t.Fatal("unverifiable HTTP replay must fail closed")
	}
	runs, err := f.repo.ListRunsByIssue(t.Context(), f.user.ID, f.issue.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != created.ID {
		t.Fatal("rejected replay changed the internally admitted Run")
	}
}
