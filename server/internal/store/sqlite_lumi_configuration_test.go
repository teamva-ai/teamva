package store

import (
	"encoding/json"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestLumiUsesCommonAgentConfigurationFromInitialRecord(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	agent, err := f.repository.EnsureGeneralAgent(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	installed := installSkillUninstallTestPackage(t, f)
	name, model, prompt, note := "Roomie", "my-model", "custom agent instructions", "one of my Agents"
	updated, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{
		Name: &name, Note: &note, AgentServiceID: &f.service.ID, Model: &model, SystemPrompt: &prompt, SkillVersionIDs: []string{installed.SkillPackageVersionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Note != note || updated.AgentServiceID != f.service.ID || updated.SystemRole != "general" || updated.CurrentRevisionID == "" {
		t.Fatal("built-in Agent did not use common fields")
	}
	revision, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, updated.CurrentRevisionID)
	if err != nil || revision.SystemPrompt != prompt || revision.Model != model {
		t.Fatal("configuration was not published")
	}
	bindings, err := f.repository.ListAgentRevisionSkillPackages(t.Context(), f.user.ID, revision.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatal("shared Skill bindings missing")
	}
	if err := f.repository.DeleteAgent(t.Context(), f.user.ID, updated.ID, updated.Version); err == nil {
		t.Fatal("built-in deletion protection removed")
	}
	if _, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, updated.ID, agent.Version, AgentConfigurationPatch{Model: &model}); err == nil {
		t.Fatal("stale version accepted")
	}

	session, err := f.repository.CreateAssistantSession(t.Context(), f.user.ID, "customized", domain.AssistantWorkHistory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.SendAssistantMessage(t.Context(), f.user.ID, session.ID, "customized", domain.AssistantMessageRequest{Body: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.repository.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var start nodeprotocol.StartRunPayload
	if json.Unmarshal([]byte(raw), &start) != nil {
		t.Fatal("invalid runtime payload")
	}
	if !strings.Contains(start.SystemPrompt, prompt) || !strings.HasPrefix(start.SystemPrompt, assistantSystemPrompt(domain.AssistantWorkHistory)) || len(start.Skills) != 1 {
		t.Fatal("customized prompt or Skills were ignored")
	}
	if !start.ReadOnly || start.ComputerUse || start.ProjectID != "" || len(start.GitResources) != 0 {
		t.Fatal("unified configuration expanded assistant permissions")
	}
}

func TestCommonProviderChangePublishesImmutableRevisionAndRejectsForeignService(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	second, err := f.repository.UpsertAgentService(t.Context(), f.user.ID, domain.AgentService{ComputerID: f.computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "ordinary", Model: "old"}, "keep", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{AgentServiceID: &second.ID})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, agent.CurrentRevisionID)
	if err != nil || old.AgentServiceID != f.service.ID {
		t.Fatal("old service snapshot changed")
	}
	current, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, updated.CurrentRevisionID)
	if err != nil || current.AgentServiceID != second.ID || current.SystemPrompt != "keep" || updated.CurrentRevisionID == old.ID {
		t.Fatal("new service revision inconsistent")
	}
	otherUser, otherComputer := seedSQLiteOtherIdentity(t, f.repository, "foreign-service")
	foreign, err := f.repository.UpsertAgentService(t.Context(), otherUser, domain.AgentService{ComputerID: otherComputer, Provider: "opencode", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	changedName := "must roll back"
	for _, bad := range []string{"missing-service", foreign.ID} {
		if _, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, updated.Version, AgentConfigurationPatch{AgentServiceID: &bad, Name: &changedName}); err == nil {
			t.Fatal("invalid or foreign service accepted")
		}
	}
	after, err := f.repository.GetAgent(t.Context(), f.user.ID, agent.ID)
	if err != nil || after.Version != updated.Version || after.Name != updated.Name {
		t.Fatal("invalid provider change partially committed")
	}
}
