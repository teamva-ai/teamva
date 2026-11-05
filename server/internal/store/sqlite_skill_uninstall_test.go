package store

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func installSkillUninstallTestPackage(t *testing.T, f sqliteAgentTestFixture) domain.ComputerSkillPackage {
	t.Helper()
	item, err := f.repository.UpsertSkillPackage(t.Context(), domain.SkillPackage{Slug: "uninstall-test", Name: "Uninstall test"})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := f.repository.InstallComputerSkillPackageVersion(t.Context(), f.user.ID, f.computer.ID, item.ID,
		domain.SkillPackageVersion{
			CommitSHA: "0123456789abcdef0123456789abcdef01234567", ContentDigest: "sha256:test",
			Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func TestSQLiteSkillUninstallArtifactFailureAndCancellationRetainOriginalVersion(t *testing.T) {
	for _, scenario := range []string{"failure", "cancel", "nil-callback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSQLiteAgentTestFixture(t)
			t.Cleanup(func() { _ = f.repository.Close() })
			installed := installSkillUninstallTestPackage(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("artifact delete failed")
			callback := func(context.Context, string) error { return failure }
			if scenario == "cancel" {
				callback = func(context.Context, string) error { cancel(); return nil }
			} else if scenario == "nil-callback" {
				callback = nil
			}
			err := f.repository.UninstallComputerSkillPackageWithArtifact(ctx, f.user.ID, f.computer.ID, installed.ID, installed.Version, callback)
			if scenario == "failure" {
				if !errors.Is(err, failure) {
					t.Fatalf("callback error not preserved: %v", err)
				}
			} else if scenario == "cancel" {
				assertDomainCode(t, err, "skill_uninstall_incomplete")
			} else {
				assertDomainCode(t, err, "invalid_argument")
			}
			items, err := f.repository.ListComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID)
			if err != nil || len(items) != 1 || items[0].ID != installed.ID || items[0].Version != installed.Version {
				t.Fatalf("failed uninstall lost retryable record: items=%d err=%v", len(items), err)
			}
			if err := f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID, f.computer.ID, installed.ID, installed.Version,
				func(context.Context, string) error { return nil }); err != nil {
				t.Fatalf("retry with original CAS version failed: %v", err)
			}
		})
	}
}

func TestSQLiteSkillUninstallBoundSkillReturnsConflictAndCanRetryAfterUnbinding(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, f)
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "Bound skill",
	}, "", []string{installed.SkillPackageVersionID})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	remove := func(context.Context, string) error { calls++; return nil }
	err = f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
		f.computer.ID, installed.ID, installed.Version, remove)
	assertDomainCode(t, err, "skill_in_use")
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.HTTPStatus != 409 || conflict.Retryable ||
		!strings.Contains(conflict.Message, "解除所有绑定") || !strings.Contains(conflict.Message, "停止相关运行") {
		t.Fatalf("uninstall must expose an actionable, non-retryable conflict: %v", err)
	}
	items, err := f.repository.ListComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID)
	if err != nil || len(items) != 1 || items[0].ID != installed.ID || items[0].Version != installed.Version || calls != 0 {
		t.Fatalf("bound uninstall changed installation or removed artifact: items=%+v calls=%d err=%v", items, calls, err)
	}
	if _, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: f.service.ID,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
		f.computer.ID, installed.ID, installed.Version, remove); err != nil {
		t.Fatalf("unbound uninstall with original version failed: %v", err)
	}
	items, err = f.repository.ListComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID)
	if err != nil || len(items) != 0 || calls != 1 {
		t.Fatalf("unbound uninstall did not remove installation and artifact: items=%+v calls=%d err=%v", items, calls, err)
	}
}

func TestSQLiteSkillUninstallProtectsPinnedRevisionOfEveryActiveRunState(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, sqliteAgentTestFixture{
		repository: f.repository, user: f.user, computer: f.computer,
	})
	_, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: f.agent.ID, AgentServiceID: f.service.ID,
	}, []string{installed.SkillPackageVersionID})
	if err != nil {
		t.Fatal(err)
	}
	f.instance, _, err = f.repository.EnsureIssueAgentInstance(t.Context(), f.user.ID, f.issue.ID, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := f.createRun(t, "uninstall-active-run")
	// Changing the current Agent configuration does not release the immutable
	// revision already captured by the queued/active run.
	if _, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: f.agent.ID, AgentServiceID: f.service.ID,
	}, nil); err != nil {
		t.Fatal(err)
	}
	var calls int
	remove := func(context.Context, string) error { calls++; return nil }
	for _, status := range []string{"queued", "starting", "running", "waiting_approval", "cancelling"} {
		if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE runs SET status = ? WHERE id = ?`, status, run.ID); err != nil {
			t.Fatal(err)
		}
		assertDomainCode(t, f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
			f.computer.ID, installed.ID, installed.Version, remove), "skill_in_use")
	}
	if calls != 0 {
		t.Fatal("active run allowed artifact deletion")
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE runs SET status = 'succeeded' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
		f.computer.ID, installed.ID, installed.Version, remove); err != nil {
		t.Fatalf("completed historical run prevented uninstall: %v", err)
	}
	if calls != 1 {
		t.Fatalf("artifact deletion calls=%d", calls)
	}
}

func TestSQLiteSkillUninstallWriteLockRejectsConcurrentBindingOnOtherConnection(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, f)
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "Concurrent binding",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.db.ExecContext(t.Context(), `PRAGMA busy_timeout(100)`); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID, f.computer.ID,
			installed.ID, installed.Version, func(context.Context, string) error {
				close(entered)
				<-release
				return nil
			})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall callback not reached")
	}
	// This is a separate DB connection, so the repository's single-connection
	// pool cannot accidentally make the test pass. Its deferred transaction can
	// read the old installation snapshot, but SQLite must reject its write
	// upgrade while uninstall owns the write lock.
	_, bindErr := other.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: f.service.ID,
	}, []string{installed.SkillPackageVersionID})
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if bindErr == nil {
		t.Fatal("concurrent binding published while artifact was being deleted")
	}
	current, err := f.repository.GetAgent(t.Context(), f.user.ID, agent.ID)
	if err != nil || current.CurrentRevisionID != agent.CurrentRevisionID || current.Version != agent.Version {
		t.Fatalf("failed concurrent binding changed Agent: %v", err)
	}
	_, err = other.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: f.service.ID,
	}, []string{installed.SkillPackageVersionID})
	assertDomainCode(t, err, "invalid_argument")
}

func TestSQLiteSkillUninstallSeesConcurrentBindingCommittedBeforeLock(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, f)
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "Binding wins",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	tx, err := other.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := createSQLiteAgentRevisionTx(t.Context(), tx, f.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: f.service.ID,
	}, []string{installed.SkillPackageVersionID}, true); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		finished <- f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID, f.computer.ID,
			installed.ID, installed.Version, func(context.Context, string) error { calls.Add(1); return nil })
	}()
	<-started
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		assertDomainCode(t, err, "skill_in_use")
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall did not finish after binding committed")
	}
	if calls.Load() != 0 {
		t.Fatal("binding committed first but artifact was deleted")
	}
	if err := f.repository.ValidateComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID,
		[]string{installed.SkillPackageVersionID}); err != nil {
		t.Fatalf("bound installation disappeared: %v", err)
	}
}

func TestSkillUninstallSlugValidation(t *testing.T) {
	for _, slug := range []string{"skill", "taste-skill", "Skill_123", strings.Repeat("a", 128)} {
		if !validSkillUninstallSlug(slug) {
			t.Fatalf("valid slug rejected: %q", slug)
		}
	}
	for _, slug := range []string{"", ".", "..", "../outside", "/absolute", `a\b`, "with space", ".hidden", "-leading", "_leading", "a.b", "技能", "a\x00b", strings.Repeat("a", 129)} {
		if validSkillUninstallSlug(slug) {
			t.Fatal("unsafe slug accepted")
		}
	}
}
