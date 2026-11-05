package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestInspirationAPIUsesMarkdownStrings(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "inspiration-api")
	defer local.Close(context.Background())
	client := newTestClient(t)
	origin := local.Endpoint().Origin
	session := mutateJSON[testBootstrap](t, client, origin, "POST", "/v1/auth/local-session", "", map[string]any{})
	markdown := "# 想法\n\n**纯 Markdown**，不是编辑器 JSON。\n"
	n := mutateJSONWithHeaders[domain.Inspiration](t, client, origin, "POST", "/v1/inspirations", session.CSRFToken, map[string]string{"Idempotency-Key": "inspiration-one"}, map[string]any{"title": "API 灵感", "markdown": markdown})
	if n.Markdown != markdown || n.ProjectID != "" {
		t.Fatal("global Markdown note was transformed")
	}
	request, err := http.NewRequest("POST", origin+"/v1/inspirations", strings.NewReader(`{"title":"bad","markdown":{"type":"doc","content":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Idempotency-Key", "structured-body")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireRemoteStatus(t, response, 400)
	response, err = client.Get(origin + "/v1/inspirations/" + n.ID + "/markdown")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(raw) != markdown || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/markdown") {
		t.Fatal("download is not exact Markdown")
	}
	updated := mutateJSON[domain.Inspiration](t, client, origin, "PATCH", "/v1/inspirations/"+n.ID, session.CSRFToken, map[string]any{"expected_version": n.Version, "completed": true})
	if !updated.Completed || updated.Markdown != markdown {
		t.Fatal("mark complete changed body")
	}
	page := getJSON[domain.InspirationPage](t, client, origin, "/v1/inspirations?project_id=&status=completed")
	if page.Total != 1 {
		t.Fatal("ungrouped completed filter failed")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "PATCH", origin, "/v1/inspirations/"+n.ID, session.CSRFToken, map[string]any{"expected_version": n.Version, "markdown": "overwrite"}), 409)
}
