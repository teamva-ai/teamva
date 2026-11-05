package store

import (
	"sync"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestConfiguredAgentCreationRollsBackInvalidSkills(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	_, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "must not exist",
	}, "prompt", []string{"not-installed"})
	if err == nil {
		t.Fatal("invalid installation accepted")
	}
	items, err := f.repository.ListAgents(t.Context(), f.user.ID, f.computer.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("half-created agents=%d err=%v", len(items), err)
	}
}

func TestConfiguredAgentUpdateIsAtomicAndPreservesOmittedFields(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "agent", Model: "original",
	}, "keep this prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	name, model := "new name", "new model"
	_, err = f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{
		Name: &name, Model: &model, SkillVersionIDs: []string{"not-installed"},
	})
	if err == nil {
		t.Fatal("invalid update accepted")
	}
	current, err := f.repository.GetAgent(t.Context(), f.user.ID, agent.ID)
	if err != nil || current.Version != agent.Version || current.Name != agent.Name || current.Model != agent.Model || current.CurrentRevisionID != agent.CurrentRevisionID {
		t.Fatalf("failed update changed agent: err=%v", err)
	}
	updated, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{Name: &name})
	if err != nil || updated.CurrentRevisionID != agent.CurrentRevisionID || updated.Version != agent.Version+1 {
		t.Fatalf("metadata update: err=%v", err)
	}
	updated, err = f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, updated.Version, AgentConfigurationPatch{Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, updated.CurrentRevisionID)
	if err != nil || revision.Model != model || revision.SystemPrompt != "keep this prompt" || updated.Model != revision.Model {
		t.Fatalf("inconsistent revision: err=%v", err)
	}
	oldRevision, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, agent.CurrentRevisionID)
	if err != nil || oldRevision.Model != "original" {
		t.Fatalf("immutable revision mutated: err=%v", err)
	}
	unchanged, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, updated.Version, AgentConfigurationPatch{Model: &model})
	if err != nil || unchanged.CurrentRevisionID != updated.CurrentRevisionID {
		t.Fatalf("identical config created a revision: err=%v", err)
	}
}

func TestConfiguredAgentConcurrentUpdateHasOneWinner(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "agent"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, model := range []string{"first", "second"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := f.repository.UpdateConfiguredAgent(t.Context(), f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{Model: &model})
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !isContractConflict(err) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent winners=%d", winners)
	}
	current, err := f.repository.GetAgent(t.Context(), f.user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := f.repository.GetAgentRevision(t.Context(), f.user.ID, current.CurrentRevisionID)
	if err != nil || current.Model != revision.Model || current.Version != agent.Version+1 {
		t.Fatalf("torn configuration: err=%v", err)
	}
}

func TestAgentDeletionRequiresAnIdleUnscheduledAgent(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	agent, err := f.repository.GetAgent(t.Context(), f.user.ID, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := f.createRun(t, "delete-guard")
	assertConversationError(t, f.repository.DeleteAgent(t.Context(), f.user.ID, agent.ID, agent.Version), "agent_in_use")
	if err := f.repository.FailLocalRun(t.Context(), run.ID, "stopped-for-delete"); err != nil {
		t.Fatal(err)
	}
	task, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, domain.ScheduledTask{
		ProjectID: f.project.ID, AgentID: agent.ID, Name: "active schedule", Enabled: true,
		ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "UTC",
		Prompt: []domain.ContentBlock{{Type: "text", Text: "work"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertConversationError(t, f.repository.DeleteAgent(t.Context(), f.user.ID, agent.ID, agent.Version), "agent_in_use")
	if _, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, task.Version, func(item *domain.ScheduledTask) { item.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.DeleteAgent(t.Context(), f.user.ID, agent.ID, agent.Version); err != nil {
		t.Fatal(err)
	}
}
