package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

type coreContractFixture struct {
	repository CoreLocalRepository
	user       domain.User
	computer   domain.Computer
	otherUser  domain.User
}

func TestSQLiteCoreRepositoryContract(t *testing.T) {
	repository, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	otherUser := seedSQLiteContractUser(t, repository, "other@contract.invalid")
	runCoreRepositoryContract(t, coreContractFixture{
		repository: repository, user: user, computer: computer, otherUser: otherUser,
	})
}

func runCoreRepositoryContract(t *testing.T, fixture coreContractFixture) {
	t.Helper()
	ctx := t.Context()
	if err := fixture.repository.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	preferences, err := fixture.repository.GetUserPreferences(ctx, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	updatedPreferences, err := fixture.repository.UpdateUserPreferences(
		ctx, fixture.user.ID, preferences.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, nil,
	)
	if err != nil || updatedPreferences.Version != preferences.Version+1 || !updatedPreferences.AllowWrite {
		t.Fatalf("updated preferences=%+v err=%v", updatedPreferences, err)
	}
	if _, err := fixture.repository.UpdateUserPreferences(
		ctx, fixture.user.ID, preferences.Version, domain.LocaleZhCN, domain.ThemeLight, false, true, nil,
	); !isContractConflict(err) {
		t.Fatalf("stale preferences error=%v, want version conflict", err)
	}

	project, err := fixture.repository.CreateProject(ctx, fixture.user.ID, domain.Project{
		ComputerID: fixture.computer.ID, Name: "Contract Project", Description: "shared semantics",
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedProject, err := fixture.repository.UpdateProject(ctx, fixture.user.ID, project.ID, project.Version, func(item *domain.Project) {
		item.Name = "Contract Project Updated"
		item.Description = "updated semantics"
	})
	if err != nil || updatedProject.Version != project.Version+1 || updatedProject.Description != "updated semantics" {
		t.Fatalf("updated project=%+v err=%v", updatedProject, err)
	}
	if _, err := fixture.repository.UpdateProject(ctx, fixture.user.ID, project.ID, project.Version, func(*domain.Project) {}); !isContractConflict(err) {
		t.Fatalf("stale project error=%v, want version conflict", err)
	}
	if _, err := fixture.repository.GetProject(ctx, fixture.otherUser.ID, project.ID); !isContractNotFound(err) {
		t.Fatalf("cross-tenant project error=%v, want not found", err)
	}

	resource, err := fixture.repository.CreateResource(ctx, fixture.user.ID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "url", Name: "Contract Docs",
		URL: "https://example.test/contract", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := fixture.repository.ListResources(ctx, fixture.user.ID, project.ID)
	if err != nil || len(resources) != 1 || resources[0].ID != resource.ID {
		t.Fatalf("resources=%+v err=%v", resources, err)
	}

	service, err := fixture.repository.UpsertAgentService(ctx, fixture.user.ID, domain.AgentService{
		ComputerID: fixture.computer.ID, Provider: "opencode", ProviderVersion: "contract",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{"models": []string{"contract/model"}},
		CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.repository.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: service.ID,
		Model: "contract/model", Name: "Contract Agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := fixture.repository.CreateAgentRevision(ctx, fixture.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: service.ID, Model: agent.Model,
		SystemPrompt: "contract prompt",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = fixture.repository.GetAgent(ctx, fixture.user.ID, agent.ID)
	if err != nil || agent.CurrentRevisionID != revision.ID {
		t.Fatalf("agent revision=%+v revision=%+v err=%v", agent, revision, err)
	}

	issue, err := fixture.repository.CreateIssue(ctx, fixture.user.ID, domain.Issue{
		ProjectID: project.ID, Title: "Contract Issue", Description: "shared issue",
		Status: domain.IssueOpen, CreatedByUserID: fixture.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := fixture.repository.CreateIssueComment(ctx, fixture.user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: fixture.user.ID, Body: "original contract comment",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	updatedComment, err := fixture.repository.UpdateIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version, func(item *domain.IssueComment) error {
		item.Body = "updated contract comment"
		return nil
	})
	if err != nil || updatedComment.Version != comment.Version+1 {
		t.Fatalf("updated comment=%+v err=%v", updatedComment, err)
	}
	revisions, err := fixture.repository.ListIssueCommentRevisions(ctx, fixture.user.ID, comment.ID)
	if err != nil || len(revisions) != 1 || revisions[0].Body != comment.Body {
		t.Fatalf("comment revisions=%+v err=%v", revisions, err)
	}

	memory, err := fixture.repository.CreateMemory(ctx, fixture.user.ID, domain.ProjectMemoryEntry{
		ProjectID: project.ID, Key: "contract", Kind: "fact", Title: "Contract",
		Content: "v1", SourceType: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	memory, err = fixture.repository.UpdateMemory(
		ctx, fixture.user.ID, project.ID, memory.ID, memory.Version, "Contract", "v2", "user",
	)
	if err != nil {
		t.Fatal(err)
	}
	memoryVersions, err := fixture.repository.ListMemoryVersions(ctx, fixture.user.ID, project.ID, memory.ID)
	if err != nil || len(memoryVersions) != 2 || memoryVersions[len(memoryVersions)-1].Version != memory.Version {
		t.Fatalf("memory versions=%+v memory=%+v err=%v", memoryVersions, memory, err)
	}

	policy, err := fixture.repository.CreatePolicyRule(ctx, fixture.user.ID, domain.PolicyRule{
		ProjectID: project.ID, ActionKind: "file.write", Effect: "require_approval",
		Conditions: map[string]any{}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err = fixture.repository.UpdatePolicyRule(ctx, fixture.user.ID, policy.ID, policy.Version, func(item *domain.PolicyRule) {
		item.Effect = "allow"
	})
	if err != nil || policy.Effect != "allow" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}

	task, err := fixture.repository.CreateScheduledTask(ctx, fixture.user.ID, domain.ScheduledTask{
		ProjectID: project.ID, Name: "Contract Schedule", Enabled: true,
		ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "UTC",
		Prompt: []domain.ContentBlock{{Type: "text", Text: "contract"}}, AgentID: agent.ID,
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	storedTask, err := fixture.repository.GetScheduledTask(ctx, fixture.user.ID, task.ID)
	if err != nil || storedTask.AgentID != agent.ID || storedTask.ScheduleExpression != task.ScheduleExpression {
		t.Fatalf("scheduled task=%+v err=%v", storedTask, err)
	}
}

func seedSQLiteContractUser(t *testing.T, repository *SQLite, email string) domain.User {
	t.Helper()
	now := time.Now().UTC()
	user := domain.User{
		Versioned: domain.Versioned{ID: newID("usr"), Version: 1, CreatedAt: now, UpdatedAt: now},
		Email:     email, DisplayName: "Other Contract User", Status: "active",
	}
	if _, err := repository.db.ExecContext(t.Context(), `
		INSERT INTO users (
			id, email, display_name, status,
			version, created_at, updated_at
		) VALUES (?, ?, ?, 'active', 1, ?, ?)
	`, user.ID, user.Email, user.DisplayName, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		t.Fatal(err)
	}
	return user
}

func isContractConflict(err error) bool {
	var domainError *domain.Error
	return errors.As(err, &domainError) && domainError.Code == "version_conflict"
}

func isContractNotFound(err error) bool {
	var domainError *domain.Error
	return errors.As(err, &domainError) && domainError.Code == "not_found"
}
