package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestEmptyApprovalListOverHTTP(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "approval-list-contract")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	origin := local.Endpoint().Origin
	client := newTestClient(t)
	mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	for _, suffix := range []string{"", "?status=pending", "?status=approved", "?status=denied", "?status=expired"} {
		t.Run(suffix, func(t *testing.T) {
			page := getJSON[domain.Page[domain.Approval]](t, client, origin, "/v1/approvals"+suffix)
			if page.Items == nil || len(page.Items) != 0 {
				t.Fatal("fresh approval list must be an empty array")
			}
		})
	}
}

func TestProjectListRejectsMissingComputerOverHTTP(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "project-list-contract")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	origin := local.Endpoint().Origin
	client := newTestClient(t)
	mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	for _, path := range []string{"/v1/projects", "/v1/projects?computer_id=", "/v1/projects?computer_id=%20%09%E3%80%80"} {
		requireRemoteStatus(t, remoteProbe(t, client, http.MethodGet, origin, path, "", nil), http.StatusBadRequest)
	}
	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	getJSON[domain.Page[domain.Project]](t, client, origin, "/v1/projects?computer_id="+computers.Items[0].ID)
}
