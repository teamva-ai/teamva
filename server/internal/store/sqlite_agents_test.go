package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

type sqliteAgentTestFixture struct {
	path       string
	repository *SQLite
	user       domain.User
	computer   domain.Computer
	service    domain.AgentService
}

func newSQLiteAgentTestFixture(t *testing.T) sqliteAgentTestFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	service, err := repository.UpsertAgentService(ctx, user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", Capabilities: map[string]any{},
		CapabilityVersion: 1,
	})
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	return sqliteAgentTestFixture{path: path, repository: repository, user: user, computer: computer, service: service}
}

func TestSQLiteGeneralAgentUsesLumiDefaultName(t *testing.T) {
	ctx := t.Context()
	fixture := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = fixture.repository.Close() })
	agent, err := fixture.repository.EnsureGeneralAgent(ctx, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != generalAgentName {
		t.Fatalf("general agent name = %q", agent.Name)
	}
	if _, err := fixture.repository.db.ExecContext(
		ctx, `UPDATE agents SET name = ? WHERE id = ?`, legacyGeneralAgentName, agent.ID,
	); err != nil {
		t.Fatal(err)
	}
	renamed, err := fixture.repository.EnsureGeneralAgent(ctx, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != generalAgentName {
		t.Fatalf("legacy general agent name = %q", renamed.Name)
	}
	if _, err := fixture.repository.db.ExecContext(
		ctx, `UPDATE agents SET name = '自定义名称' WHERE id = ?`, agent.ID,
	); err != nil {
		t.Fatal(err)
	}
	customized, err := fixture.repository.EnsureGeneralAgent(ctx, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if customized.Name != "自定义名称" {
		t.Fatalf("customized general agent name = %q", customized.Name)
	}
}

func TestSQLiteComputerSkillsInstallValidateAndUninstall(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = fixture.repository.Close() })
	if err := EnsureBuiltinSkillPackages(ctx, fixture.repository); err != nil {
		t.Fatal(err)
	}
	packages, err := fixture.repository.ListSkillPackages(ctx, fixture.user.ID)
	if err != nil || len(packages) == 0 {
		t.Fatalf("list Skill packages: items=%d err=%v", len(packages), err)
	}
	assertDomainCode(t, fixture.repository.ValidateComputerSkillPackages(
		ctx, fixture.user.ID, fixture.computer.ID, []string{packages[0].ID},
	), "invalid_argument")
	installed, err := fixture.repository.InstallComputerSkillPackageVersion(
		ctx, fixture.user.ID, fixture.computer.ID, packages[0].ID,
		domain.SkillPackageVersion{
			CommitSHA: "1111111111111111111111111111111111111111", ContentDigest: "sha256:first",
			Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1, SizeBytes: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if installed.SkillPackageID != packages[0].ID || installed.SkillPackageVersionID == packages[0].ID {
		t.Fatalf("unexpected installation: %+v", installed)
	}
	if err := fixture.repository.ValidateComputerSkillPackages(
		ctx, fixture.user.ID, fixture.computer.ID, []string{installed.SkillPackageVersionID},
	); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.repository.ListComputerSkillPackages(ctx, fixture.user.ID, fixture.computer.ID)
	if err != nil || len(items) != 1 || items[0].ID != installed.ID {
		t.Fatalf("list installed Skills: items=%+v err=%v", items, err)
	}
	if err := fixture.repository.UninstallComputerSkillPackage(
		ctx, fixture.user.ID, fixture.computer.ID, installed.ID, installed.Version,
	); err != nil {
		t.Fatal(err)
	}
	assertDomainCode(t, fixture.repository.ValidateComputerSkillPackages(
		ctx, fixture.user.ID, fixture.computer.ID, []string{installed.SkillPackageVersionID},
	), "invalid_argument")
}

func TestSQLiteRealSkillInstallIsIdempotentAndNormalizesJSON(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = fixture.repository.Close() })
	if err := EnsureBuiltinSkillPackages(ctx, fixture.repository); err != nil {
		t.Fatal(err)
	}
	packages, err := fixture.repository.ListSkillPackages(ctx, fixture.user.ID)
	if err != nil || len(packages) == 0 {
		t.Fatalf("list Skill packages: items=%d err=%v", len(packages), err)
	}
	installed, err := fixture.repository.InstallComputerSkillPackageVersion(
		ctx, fixture.user.ID, fixture.computer.ID, packages[0].ID,
		domain.SkillPackageVersion{
			CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
			ContentDigest: "sha256:test", Manifest: map[string]any{"files": []any{"SKILL.md"}},
			FileCount: 1, SizeBytes: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.repository.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: fixture.service.ID, Name: "bound",
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := fixture.repository.CreateAgentRevision(ctx, fixture.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: fixture.service.ID,
	}, []string{installed.SkillPackageVersionID})
	if err != nil {
		t.Fatal(err)
	}

	reinstalled, err := fixture.repository.InstallComputerSkillPackageVersion(
		ctx, fixture.user.ID, fixture.computer.ID, packages[0].ID,
		domain.SkillPackageVersion{
			CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
			ContentDigest: "sha256:test", Manifest: map[string]any{"files": []any{"SKILL.md"}},
			FileCount: 1, SizeBytes: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reinstalled.ID != installed.ID || reinstalled.SkillPackageVersionID != installed.SkillPackageVersionID {
		t.Fatalf("idempotent installation identity changed: before=%+v after=%+v", installed, reinstalled)
	}
	version, err := fixture.repository.GetSkillPackageVersion(ctx, fixture.user.ID, reinstalled.SkillPackageVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if version.CommitSHA != "0123456789abcdef0123456789abcdef01234567" ||
		version.ContentDigest != "sha256:test" || version.FileCount != 1 ||
		version.ExecutableFiles == nil || len(version.ExecutableFiles) != 0 ||
		version.ProviderInstall == nil {
		t.Fatalf("real Skill artifact was not normalized: %+v", version)
	}
	var executableType, providerType string
	if err := fixture.repository.db.QueryRowContext(ctx, `
		SELECT json_type(executable_files), json_type(provider_install)
		FROM skill_package_versions WHERE id = ?
	`, reinstalled.SkillPackageVersionID).Scan(&executableType, &providerType); err != nil {
		t.Fatal(err)
	}
	if executableType != "array" || providerType != "object" {
		t.Fatalf("artifact JSON types = executable:%s provider:%s", executableType, providerType)
	}
	bindings, err := fixture.repository.ListAgentRevisionSkillPackages(ctx, fixture.user.ID, revision.ID)
	if err != nil || len(bindings) != 1 || bindings[0].SkillPackageVersionID != reinstalled.SkillPackageVersionID {
		t.Fatalf("Agent binding changed after idempotent reinstall: bindings=%+v err=%v", bindings, err)
	}
}

func TestSQLiteAgentsRevisionsSkillsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	repository := fixture.repository

	if err := EnsureBuiltinSkillPackages(ctx, repository); err != nil {
		t.Fatal(err)
	}
	// Seed 必须按 slug 幂等，且保留第一次写入的稳定 ID。
	if err := EnsureBuiltinSkillPackages(ctx, repository); err != nil {
		t.Fatal(err)
	}
	packages, err := repository.ListSkillPackages(ctx, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != len(builtinSkillSeeds) {
		t.Fatalf("builtin package count = %d, want %d", len(packages), len(builtinSkillSeeds))
	}

	agent, err := repository.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: fixture.service.ID,
		Model: "openai/test", Name: "SQLite Agent", Note: "durable",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 未安装的 Skill 不能绑定，失败也不能留下半条 AgentRevision。
	_, err = repository.CreateAgentRevision(ctx, fixture.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: fixture.service.ID,
		Model: agent.Model, SystemPrompt: "first",
	}, []string{packages[0].ID, "missing_skill_version"})
	assertDomainCode(t, err, "invalid_argument")
	var revisionCount int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT count(*) FROM agent_revisions WHERE agent_id = ?`, agent.ID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repository.GetAgent(ctx, fixture.user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 || unchanged.CurrentRevisionID != "" || unchanged.Version != 1 {
		t.Fatalf("failed revision transaction leaked state: revisions=%d agent=%+v", revisionCount, unchanged)
	}
	installedIDs := make([]string, 0, 2)
	commits := []string{"2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"}
	for index, packageItem := range packages[:2] {
		installed, installErr := repository.InstallComputerSkillPackageVersion(
			ctx, fixture.user.ID, fixture.computer.ID, packageItem.ID,
			domain.SkillPackageVersion{
				CommitSHA: commits[index], ContentDigest: "sha256:installed-" + packageItem.Slug,
				Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1, SizeBytes: 4,
			},
		)
		if installErr != nil {
			t.Fatal(installErr)
		}
		installedIDs = append(installedIDs, installed.SkillPackageVersionID)
	}

	revision, err := repository.CreateAgentRevision(ctx, fixture.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: fixture.service.ID,
		Model: agent.Model, SystemPrompt: "be exact",
	}, []string{installedIDs[1], installedIDs[0]})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := repository.GetAgent(ctx, fixture.user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.CurrentRevisionID != revision.ID || fresh.Version != 2 {
		t.Fatalf("current revision was not advanced atomically: %+v", fresh)
	}
	bindings, err := repository.ListAgentRevisionSkillPackages(ctx, fixture.user.ID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].Position != 0 || bindings[0].SkillPackageVersionID != installedIDs[1] ||
		bindings[1].Position != 1 || bindings[1].SkillPackageVersionID != installedIDs[0] {
		t.Fatalf("unexpected revision skills: %+v", bindings)
	}
	version, err := repository.GetSkillPackageVersion(ctx, fixture.user.ID, installedIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if version.SkillPackageID != packages[1].ID || version.CommitSHA != commits[1] ||
		version.Manifest == nil || version.ProviderInstall == nil || len(version.ExecutableFiles) != 0 {
		t.Fatalf("unexpected installed Skill version: %+v", version)
	}

	updated, err := repository.UpdateAgent(ctx, fixture.user.ID, agent.ID, fresh.Version, func(item *domain.Agent) {
		item.Name = "Renamed"
		item.Model = "openai/next"
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateAgent(ctx, fixture.user.ID, agent.ID, fresh.Version, func(item *domain.Agent) {
		item.Name = "stale"
	})
	assertDomainCode(t, err, "version_conflict")
	if updated.Model != "openai/next" {
		t.Fatalf("agent model was not persisted: %+v", updated)
	}

	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = OpenSQLite(ctx, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	restored, err := repository.GetAgent(ctx, fixture.user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredRevision, err := repository.GetAgentRevision(ctx, fixture.user.ID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredBindings, err := repository.ListAgentRevisionSkillPackages(ctx, fixture.user.ID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Name != updated.Name || restored.Model != updated.Model ||
		restored.CurrentRevisionID != revision.ID || restoredRevision.SystemPrompt != "be exact" ||
		len(restoredBindings) != 2 {
		t.Fatalf("Agent graph changed after restart: agent=%+v revision=%+v bindings=%+v",
			restored, restoredRevision, restoredBindings)
	}
}

func TestSQLiteAgentTenantIsolation(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	defer func() { _ = fixture.repository.Close() }()
	repository := fixture.repository

	otherUserID, otherComputerID := seedSQLiteOtherIdentity(t, repository, "agent-other")
	otherService, err := repository.UpsertAgentService(ctx, otherUserID, domain.AgentService{
		ComputerID: otherComputerID, Provider: "opencode", LoginStatus: "authenticated",
		Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := repository.CreateAgent(ctx, otherUserID, domain.Agent{
		ComputerID: otherComputerID, AgentServiceID: otherService.ID, Name: "Other Agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherRevision, err := repository.CreateAgentRevision(ctx, otherUserID, domain.AgentRevision{
		AgentID: otherAgent.ID, AgentServiceID: otherService.ID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = repository.ListAgents(ctx, fixture.user.ID, otherComputerID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetAgent(ctx, fixture.user.ID, otherAgent.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetAgentRevision(ctx, fixture.user.ID, otherRevision.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.ListAgentRevisionSkillPackages(ctx, fixture.user.ID, otherRevision.ID)
	assertDomainCode(t, err, "not_found")
	if err := repository.DeleteAgent(ctx, fixture.user.ID, otherAgent.ID, otherAgent.Version+1); err == nil {
		t.Fatal("cross-tenant Agent delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "not_found")
	}
}

func seedSQLiteOtherIdentity(t *testing.T, repository *SQLite, suffix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	userID, computerID := newID("usr"), newID("cmp")
	now := sqliteTimeValue(time.Now())
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, status, version, created_at, updated_at)
		VALUES (?, ?, 'Other', 'active', 1, ?, ?)
	`, userID, suffix+"@teamva.invalid", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO computers (
			id, user_id, name, platform, architecture, fingerprint, status,
			protocol_version, version, created_at, updated_at
		) VALUES (?, ?, 'Other computer', 'test', 'test', ?, 'online', 1, 1, ?, ?)
	`, computerID, userID, "fingerprint-"+suffix, now, now); err != nil {
		t.Fatal(err)
	}
	return userID, computerID
}
