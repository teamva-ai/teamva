package store

import (
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestOnboardingDerivesFromAgentStateAndDismissal(t *testing.T) {
	fixture := newSQLiteAgentTestFixture(t)
	defer fixture.repository.Close()
	s := fixture.repository
	ctx := t.Context()

	assertState := func(userID string, dismissed, needed bool, name string) {
		t.Helper()
		view, err := s.Onboarding(ctx, userID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if view.Dismissed != dismissed || view.Needed != needed {
			t.Fatalf("%s: got dismissed=%t needed=%t", name, view.Dismissed, view.Needed)
		}
	}

	assertState(fixture.user.ID, false, true, "fresh database")
	if err := s.SetOnboardingDismissed(ctx, fixture.user.ID, true); err != nil {
		t.Fatal(err)
	}
	assertState(fixture.user.ID, true, false, "dismissed")
	if err := s.SetOnboardingDismissed(ctx, fixture.user.ID, false); err != nil {
		t.Fatal(err)
	}
	assertState(fixture.user.ID, false, true, "reopened")
	if _, err := s.EnsureGeneralAgent(ctx, fixture.user.ID); err != nil {
		t.Fatal(err)
	}
	assertState(fixture.user.ID, false, true, "unconfigured general agent is still empty")
	if _, err := s.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: fixture.service.ID, Name: "用户 Agent",
	}); err != nil {
		t.Fatal(err)
	}
	assertState(fixture.user.ID, false, false, "user agent")

	configured := newSQLiteAgentTestFixture(t)
	defer configured.repository.Close()
	general, err := configured.repository.EnsureGeneralAgent(t.Context(), configured.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configured.repository.ConfigureGeneralAgent(t.Context(), configured.user.ID, configured.service.ID, "test-model", general.Version); err != nil {
		t.Fatal(err)
	}
	view, err := configured.repository.Onboarding(t.Context(), configured.user.ID)
	if err != nil || view.Needed {
		t.Fatalf("configured general agent must satisfy onboarding: %+v %v", view, err)
	}
}
