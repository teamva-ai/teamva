package httpapi

import (
	"context"
	"strconv"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

type identitySurfaceProber struct {
	repo    *testRepository
	userID  string
	service domain.AgentService
	calls   int
}

func (*identitySurfaceProber) NotifyIssueActivation(string) {}
func (p *identitySurfaceProber) Probe(ctx context.Context) error {
	p.calls++
	_, err := p.repo.UpsertAgentService(ctx, p.userID, p.service)
	return err
}

// Device/service results and audit rows come from the real repository. Only
// native Provider discovery is controlled; this does not authenticate a model.
func TestIdentityComputerProbeAndAuditNormalPathsTCP(t *testing.T) {
	repo := newTestRepository(t)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	input := domain.AgentService{ComputerID: computer.ID, Provider: "opencode", ProviderVersion: "fixture",
		LoginStatus: "authenticated", Status: "ready", CapabilityVersion: 1,
		Capabilities: map[string]any{"models": []string{"fixture/model"}}}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	relay := &identitySurfaceProber{repo: repo, userID: user.ID, service: input}
	s := newSystemSurfaceHTTP(t, repo, relay, nil)
	device := decodeData[domain.Computer](t, s.call(t, "GET", "/v1/computers/"+computer.ID, nil, 200))
	if device.ID != computer.ID || device.UserID != user.ID {
		t.Fatal("device projection changed ownership")
	}
	s.call(t, "GET", "/v1/computers/missing-identity-fixture", nil, 404)
	probed := decodeData[domain.AgentService](t, s.call(t, "POST", "/v1/agent-services/"+service.ID+"/probe", nil, 202))
	if relay.calls != 1 || probed.ID != service.ID || probed.Status != "ready" || probed.Version <= service.Version {
		t.Fatal("successful HTTP probe did not publish the refreshed service")
	}
	s.call(t, "POST", "/v1/agent-services/missing-identity-service/probe", nil, 404)
	other := createTestIdentity(t, repo, "identity-surface-other@example.test")
	otherAuth := map[string]string{"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": s.server.URL}
	s.callHeaders(t, "GET", "/v1/computers/"+computer.ID, nil, otherAuth, 404)
	s.callHeaders(t, "POST", "/v1/agent-services/"+service.ID+"/probe", nil, otherAuth, 404)
	if relay.calls != 1 {
		t.Fatal("unknown or foreign service reached machine-wide discovery")
	}
	project := systemSurfaceProject(t, repo, "Audit scope fixture")
	if err := repo.AppendAuditEvent(t.Context(), domain.AuditEvent{UserID: user.ID, ProjectID: project.ID,
		ActorType: "user", ActorRef: user.ID, Action: "identity.surface", Outcome: "succeeded", Details: map[string]any{"source": "fixture"}}); err != nil {
		t.Fatal(err)
	}
	page := decodeData[domain.Page[domain.AuditEvent]](t, s.call(t, "GET", "/v1/audit-events?project_id="+project.ID, nil, 200))
	if len(page.Items) != 1 || page.Items[0].Action != "identity.surface" || page.Items[0].ProjectID != project.ID {
		t.Fatal("audit filter did not return the scoped row")
	}
	foreign := decodeData[domain.Page[domain.AuditEvent]](t, s.callHeaders(t, "GET", "/v1/audit-events?project_id="+project.ID, nil, otherAuth, 200))
	if len(foreign.Items) != 0 {
		t.Fatal("audit events crossed user boundary")
	}
}

func TestAgentConfigurationRevisionAndDeletionLifecycleTCP(t *testing.T) {
	repo := newTestRepository(t)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID,
		Provider: "opencode", ProviderVersion: "fixture", LoginStatus: "authenticated", Status: "ready",
		CapabilityVersion: 1, Capabilities: map[string]any{"models": []string{"fixture/model"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSystemSurfaceHTTP(t, repo, nil, nil)
	path := "/v1/computers/" + computer.ID + "/agents"
	agent := decodeData[domain.Agent](t, s.call(t, "POST", path, map[string]any{
		"agent_service_id": service.ID, "model": "fixture/model", "name": "Lifecycle Agent", "system_prompt": "First prompt",
	}, 201))
	agentPath := "/v1/agents/" + agent.ID
	read := decodeData[domain.Agent](t, s.call(t, "GET", agentPath, nil, 200))
	if agent.ID == "" || agent.CurrentRevisionID == "" || read.ID != agent.ID {
		t.Fatal("configured Agent was not atomically published")
	}
	firstRevision := agent.CurrentRevisionID
	metadata := decodeData[domain.Agent](t, s.call(t, "PATCH", agentPath, map[string]any{
		"expected_version": agent.Version, "name": "Renamed", "note": "Metadata only",
	}, 200))
	if metadata.Version != agent.Version+1 || metadata.CurrentRevisionID != firstRevision {
		t.Fatal("metadata edit unexpectedly replaced immutable configuration")
	}
	configured := decodeData[domain.Agent](t, s.call(t, "PATCH", agentPath, map[string]any{
		"expected_version": metadata.Version, "system_prompt": "Second prompt",
	}, 200))
	if configured.CurrentRevisionID == firstRevision || configured.Version != metadata.Version+1 {
		t.Fatal("configuration edit did not publish a new immutable revision")
	}
	for id, prompt := range map[string]string{firstRevision: "First prompt", configured.CurrentRevisionID: "Second prompt"} {
		detail := decodeData[struct {
			Revision domain.AgentRevision `json:"revision"`
		}](t, s.call(t, "GET", "/v1/agent-revisions/"+id, nil, 200))
		if detail.Revision.SystemPrompt != prompt {
			t.Fatal("a published revision was mutated")
		}
	}
	s.call(t, "DELETE", agentPath+"?expected_version=1", nil, 409)
	s.call(t, "DELETE", agentPath+"?expected_version="+strconv.FormatInt(configured.Version, 10), nil, 204)
	s.call(t, "GET", agentPath, nil, 404)
	s.call(t, "PATCH", agentPath, map[string]any{"expected_version": configured.Version, "name": "Must not revive"}, 404)
	page := decodeData[domain.Page[domain.Agent]](t, s.call(t, "GET", path, nil, 200))
	for _, item := range page.Items {
		if item.ID == agent.ID {
			t.Fatal("deleted Agent remained visible in its parent collection")
		}
	}
}
