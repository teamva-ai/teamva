package app

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestOnboardingAPIRespondsToAgentState(t *testing.T) {
	local := startTestLocal(t, t.TempDir(), "onboarding-api")
	defer local.Close(context.Background())
	client := newTestClient(t)
	origin := local.Endpoint().Origin
	session := mutateJSON[testBootstrap](t, client, origin, "POST", "/v1/auth/local-session", "", map[string]any{})

	view := getJSON[domain.Onboarding](t, client, origin, "/v1/system/onboarding")
	if view.Dismissed || !view.Needed {
		t.Fatalf("fresh database must need onboarding: %+v", view)
	}
	view = mutateJSON[domain.Onboarding](t, client, origin, "PUT", "/v1/system/onboarding", session.CSRFToken, map[string]any{"dismissed": true})
	if !view.Dismissed || view.Needed {
		t.Fatalf("dismissal not applied: %+v", view)
	}
	view = mutateJSON[domain.Onboarding](t, client, origin, "PUT", "/v1/system/onboarding", session.CSRFToken, map[string]any{"dismissed": false})
	if view.Dismissed || !view.Needed {
		t.Fatalf("reopen not applied: %+v", view)
	}

	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	user, _, err := local.repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := local.repository.UpsertAgentService(context.Background(), user.ID, domain.AgentService{
		ComputerID: computers.Items[0].ID, Provider: "codex", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.repository.EnsureGeneralAgent(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	view = getJSON[domain.Onboarding](t, client, origin, "/v1/system/onboarding")
	if !view.Needed {
		t.Fatal("unconfigured general agent must not satisfy onboarding")
	}
	agent := mutateJSON[domain.Agent](t, client, origin, "POST", "/v1/computers/"+computers.Items[0].ID+"/agents", session.CSRFToken, map[string]any{
		"agent_service_id": service.ID, "name": "引导测试 Agent", "model": "", "system_prompt": "",
	})
	view = getJSON[domain.Onboarding](t, client, origin, "/v1/system/onboarding")
	if view.Needed {
		t.Fatal("user agent must satisfy onboarding")
	}
	requireRemoteStatus(t, remoteProbe(t, client, "DELETE", origin, "/v1/agents/"+agent.ID+"?expected_version="+strconv.FormatInt(agent.Version, 10), session.CSRFToken, map[string]any{}), http.StatusNoContent)
	view = getJSON[domain.Onboarding](t, client, origin, "/v1/system/onboarding")
	if !view.Needed {
		t.Fatal("deleting the last agent must make onboarding needed again")
	}
}
