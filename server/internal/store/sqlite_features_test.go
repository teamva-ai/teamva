package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteFeaturesSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "features.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "durable features",
	})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := repository.CreateMemory(ctx, user.ID, domain.ProjectMemoryEntry{
		ProjectID: project.ID, Key: "architecture", Kind: "fact", Title: "Architecture",
		Content: "SQLite", SourceType: "user", SourceRef: "settings",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	policyExpiry := time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)
	policy, err := repository.CreatePolicyRule(ctx, user.ID, domain.PolicyRule{
		ProjectID: project.ID, ActionKind: "shell", Effect: "require_approval",
		Conditions: map[string]any{"commands": []string{"rm", "mv"}}, ExpiresAt: &policyExpiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	nextRun := time.Date(2035, 2, 3, 4, 5, 6, 0, time.UTC)
	task, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{
		ProjectID: project.ID, Name: "daily review", ScheduleKind: "cron",
		ScheduleExpression: "0 9 * * *", Timezone: "Asia/Shanghai",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "review"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once", NextRunAt: &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	auditTime := time.Date(2034, 6, 1, 12, 0, 0, 123, time.UTC)
	if err := repository.AppendAuditEvent(ctx, domain.AuditEvent{
		ID: "aud_restart", UserID: user.ID, ProjectID: project.ID,
		ActorType: "user", Action: "features.created", Outcome: "succeeded",
		Details: map[string]any{"source": "desktop", "count": 3}, OccurredAt: auditTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	restoredMemory, err := repository.GetMemory(ctx, user.ID, project.ID, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredMemory.Content != "SQLite" || restoredMemory.SourceRef != "settings" {
		t.Fatalf("memory changed after restart: %#v", restoredMemory)
	}
	policies, err := repository.ListPolicyRules(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].ID != policy.ID ||
		policies[0].Conditions["commands"] == nil || policies[0].ExpiresAt == nil ||
		!policies[0].ExpiresAt.Equal(policyExpiry) {
		t.Fatalf("policy changed after restart: %#v", policies)
	}
	restoredTask, err := repository.GetScheduledTask(ctx, user.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredTask.Prompt) != 1 || restoredTask.Prompt[0].Text != "review" ||
		restoredTask.NextRunAt == nil || !restoredTask.NextRunAt.Equal(nextRun) {
		t.Fatalf("scheduled task changed after restart: %#v", restoredTask)
	}
	events, err := repository.ListAuditEvents(ctx, user.ID, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "aud_restart" || events[0].Details["source"] != "desktop" {
		t.Fatalf("audit event changed after restart: %#v", events)
	}
}

func TestSQLiteFeatureTenantIsolation(t *testing.T) {
	ctx := context.Background()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "tenants.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	owner, ownerComputer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ownerProject, err := repository.CreateProject(ctx, owner.ID, domain.Project{ComputerID: ownerComputer.ID, Name: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	otherUserID, otherComputerID := insertSQLiteFeatureTenant(t, ctx, repository)
	otherProject, err := repository.CreateProject(ctx, otherUserID, domain.Project{ComputerID: otherComputerID, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	otherMemory, err := repository.CreateMemory(ctx, otherUserID, domain.ProjectMemoryEntry{
		ProjectID: otherProject.ID, Key: "private", Kind: "fact", Title: "Private",
		Content: "secret", SourceType: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	otherPolicy, err := repository.CreatePolicyRule(ctx, otherUserID, domain.PolicyRule{
		ProjectID: otherProject.ID, ActionKind: "shell", Effect: "deny",
		Conditions: map[string]any{"private": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := repository.CreateScheduledTask(ctx, otherUserID, domain.ScheduledTask{
		ProjectID: otherProject.ID, Name: "private schedule", ScheduleKind: "once",
		ScheduleExpression: "2035-01-01T00:00:00Z", Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "secret"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = repository.ListMemories(ctx, owner.ID, otherProject.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetMemory(ctx, owner.ID, otherProject.ID, otherMemory.ID)
	assertDomainCode(t, err, "not_found")
	err = repository.ArchiveMemory(ctx, owner.ID, otherProject.ID, otherMemory.ID, otherMemory.Version)
	assertDomainCode(t, err, "not_found")
	_, err = repository.UpdatePolicyRule(ctx, owner.ID, otherPolicy.ID, otherPolicy.Version, func(item *domain.PolicyRule) {
		item.Effect = "allow"
	})
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetScheduledTask(ctx, owner.ID, otherTask.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.ListScheduledTaskRuns(ctx, owner.ID, otherTask.ID)
	assertDomainCode(t, err, "not_found")

	if err := repository.AppendAuditEvent(ctx, domain.AuditEvent{
		ID: "aud_other", UserID: otherUserID, ProjectID: otherProject.ID,
		ActorType: "user", Action: "private", Outcome: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	// 即使调用方写入了 user/project 不一致的事件，读取侧的项目 JOIN 也不能泄漏。
	if err := repository.AppendAuditEvent(ctx, domain.AuditEvent{
		ID: "aud_mismatched_scope", UserID: owner.ID, ProjectID: otherProject.ID,
		ActorType: "system", Action: "malformed", Outcome: "failed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendAuditEvent(ctx, domain.AuditEvent{
		ID: "aud_owner", UserID: owner.ID, ProjectID: ownerProject.ID,
		ActorType: "user", Action: "visible", Outcome: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := repository.ListAuditEvents(ctx, owner.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "aud_owner" {
		t.Fatalf("cross-tenant audit event leaked: %#v", events)
	}
}

func TestSQLiteMemoryVersionsArchiveAndRestore(t *testing.T) {
	ctx := context.Background()
	repository, user, _, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	memory, err := repository.CreateMemory(ctx, user.ID, domain.ProjectMemoryEntry{
		ProjectID: project.ID, Key: "decision", Kind: "decision", Title: "v1",
		Content: "first", SourceType: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateMemory(ctx, user.ID, project.ID, memory.ID, memory.Version, "v2", "second", "agent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateMemory(ctx, user.ID, project.ID, memory.ID, memory.Version, "stale", "stale", "user")
	assertDomainCode(t, err, "version_conflict")
	if _, err := repository.UpdateMemory(ctx, user.ID, project.ID, memory.ID, updated.Version, "not committed", "not committed", ""); err == nil {
		t.Fatal("memory update without a valid history actor unexpectedly succeeded")
	}
	afterRollback, err := repository.GetMemory(ctx, user.ID, project.ID, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRollback.Version != updated.Version || afterRollback.Title != updated.Title || afterRollback.Content != updated.Content {
		t.Fatalf("memory update was not atomic with history insertion: %#v", afterRollback)
	}
	versions, err := repository.ListMemoryVersions(ctx, user.ID, project.ID, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 1 || versions[0].ActorType != "user" ||
		versions[1].Version != 2 || versions[1].Title != "v2" || versions[1].ActorType != "agent" {
		t.Fatalf("unexpected memory history: %#v", versions)
	}
	if err := repository.ArchiveMemory(ctx, user.ID, project.ID, memory.ID, memory.Version); err == nil {
		t.Fatal("stale archive unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "version_conflict")
	}
	if err := repository.ArchiveMemory(ctx, user.ID, project.ID, memory.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	listed, err := repository.ListMemories(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("archived memory remained active: %#v", listed)
	}
	archived, err := repository.GetMemory(ctx, user.ID, project.ID, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.DeletedAt == nil || archived.Version != updated.Version+1 {
		t.Fatalf("unexpected archived memory: %#v", archived)
	}
	_, err = repository.RestoreMemory(ctx, user.ID, project.ID, memory.ID, updated.Version)
	assertDomainCode(t, err, "version_conflict")
	restored, err := repository.RestoreMemory(ctx, user.ID, project.ID, memory.ID, archived.Version)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DeletedAt != nil || restored.Version != archived.Version+1 || restored.Content != "second" {
		t.Fatalf("unexpected restored memory: %#v", restored)
	}
	versions, err = repository.ListMemoryVersions(ctx, user.ID, project.ID, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("archive/restore unexpectedly changed content history: %#v", versions)
	}
}

func TestSQLitePolicyOptimisticLock(t *testing.T) {
	ctx := context.Background()
	repository, user, _, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	policy, err := repository.CreatePolicyRule(ctx, user.ID, domain.PolicyRule{
		ProjectID: project.ID, ActionKind: "filesystem.write", Effect: "require_approval",
		Conditions: map[string]any{"paths": []string{"/tmp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Enabled || policy.Version != 1 {
		t.Fatalf("unexpected policy defaults: %#v", policy)
	}
	updated, err := repository.UpdatePolicyRule(ctx, user.ID, policy.ID, policy.Version, func(item *domain.PolicyRule) {
		item.Effect = "allow"
		item.Enabled = false
		item.Conditions = map[string]any{"paths": []string{"/workspace"}, "recursive": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdatePolicyRule(ctx, user.ID, policy.ID, policy.Version, func(item *domain.PolicyRule) {
		item.Effect = "deny"
	})
	assertDomainCode(t, err, "version_conflict")
	if err := repository.DeletePolicyRule(ctx, user.ID, policy.ID, policy.Version); err == nil {
		t.Fatal("stale policy delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "version_conflict")
	}
	policies, err := repository.ListPolicyRules(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Version != updated.Version || policies[0].Enabled ||
		policies[0].Effect != "allow" || policies[0].Conditions["recursive"] != true {
		t.Fatalf("unexpected updated policy: %#v", policies)
	}
	if err := repository.DeletePolicyRule(ctx, user.ID, policy.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	policies, err = repository.ListPolicyRules(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 0 {
		t.Fatalf("deleted policy remained active: %#v", policies)
	}
}

func TestSQLiteScheduledTaskCRUDAndRuns(t *testing.T) {
	ctx := context.Background()
	repository, user, _, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	next := time.Date(2036, 3, 2, 1, 0, 0, 0, time.UTC)
	task, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{
		ProjectID: project.ID, Name: "once", ScheduleKind: "once",
		ScheduleExpression: next.Format(time.RFC3339), Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "first"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip", NextRunAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !task.Enabled || task.Version != 1 {
		t.Fatalf("unexpected schedule defaults: %#v", task)
	}
	updated, err := repository.UpdateScheduledTask(ctx, user.ID, task.ID, task.Version, func(item *domain.ScheduledTask) {
		item.Name = "daily"
		item.Enabled = false
		item.ScheduleKind = "cron"
		item.ScheduleExpression = "0 8 * * *"
		item.Prompt = []domain.ContentBlock{{Type: "text", Text: "updated"}}
		item.MisfirePolicy = "run_once"
		item.NextRunAt = nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateScheduledTask(ctx, user.ID, task.ID, task.Version, func(item *domain.ScheduledTask) {
		item.Name = "stale"
	})
	assertDomainCode(t, err, "version_conflict")
	listed, err := repository.ListScheduledTasks(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetScheduledTask(ctx, user.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || got.Version != updated.Version || got.Name != "daily" || got.Enabled ||
		got.NextRunAt != nil || len(got.Prompt) != 1 || got.Prompt[0].Text != "updated" {
		t.Fatalf("unexpected scheduled task: listed=%#v got=%#v", listed, got)
	}

	older := time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO scheduled_task_runs
			(id, scheduled_task_id, trigger_key, scheduled_for, status, created_at)
		VALUES (?, ?, 'older', ?, 'pending', ?), (?, ?, 'newer', ?, 'failed', ?)
	`, "str_older", task.ID, sqliteTimeValue(older), sqliteTimeValue(older),
		"str_newer", task.ID, sqliteTimeValue(newer), sqliteTimeValue(newer)); err != nil {
		t.Fatal(err)
	}
	runs, err := repository.ListScheduledTaskRuns(ctx, user.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != "str_newer" || runs[1].ID != "str_older" ||
		!runs[0].ScheduledFor.Equal(newer) {
		t.Fatalf("unexpected scheduled task runs: %#v", runs)
	}
}

func TestSQLiteScheduledTaskRejectsInlineImagePrompt(t *testing.T) {
	ctx := context.Background()
	repository, user, _, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	_, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{
		ProjectID: project.ID, Name: "image", ScheduleKind: "cron",
		ScheduleExpression: "* * * * *", Timezone: "UTC",
		Prompt: []domain.ContentBlock{{
			Type: "image", MimeType: "image/png", Filename: "capture.png", DataURL: tinyPNGDataURL,
		}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip",
	})
	assertDomainCode(t, err, "invalid_argument")
}

func TestSQLiteAuditFiltersAndOrdering(t *testing.T) {
	ctx := context.Background()
	repository, user, computer, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	secondProject, err := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	runOne := insertSQLiteFeatureRun(t, ctx, repository, project.ID, "run_filter_one")
	runTwo := insertSQLiteFeatureRun(t, ctx, repository, secondProject.ID, "run_filter_two")
	base := time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []domain.AuditEvent{
		{ID: "aud_global", UserID: user.ID, ActorType: "system", Action: "global", Outcome: "succeeded", OccurredAt: base},
		{ID: "aud_project_run", UserID: user.ID, ProjectID: project.ID, RunID: runOne, ActorType: "agent", Action: "run", Outcome: "succeeded", Details: map[string]any{"attempt": 1}, OccurredAt: base.Add(time.Minute)},
		{ID: "aud_second", UserID: user.ID, ProjectID: secondProject.ID, RunID: runTwo, ActorType: "agent", Action: "second", Outcome: "failed", OccurredAt: base.Add(2 * time.Minute)},
		{ID: "aud_project", UserID: user.ID, ProjectID: project.ID, ActorType: "user", Action: "project", Outcome: "succeeded", OccurredAt: base.Add(3 * time.Minute)},
	}
	for _, event := range events {
		if err := repository.AppendAuditEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	all, err := repository.ListAuditEvents(ctx, user.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].ID != "aud_project" || all[3].ID != "aud_global" {
		t.Fatalf("unexpected audit ordering: %#v", all)
	}
	projectEvents, err := repository.ListAuditEvents(ctx, user.ID, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projectEvents) != 2 || projectEvents[0].ID != "aud_project" || projectEvents[1].ID != "aud_project_run" {
		t.Fatalf("unexpected project audit filter: %#v", projectEvents)
	}
	runEvents, err := repository.ListAuditEvents(ctx, user.ID, "", runOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(runEvents) != 1 || runEvents[0].ID != "aud_project_run" || runEvents[0].Details["attempt"] != float64(1) {
		t.Fatalf("unexpected run audit filter: %#v", runEvents)
	}
}

func newSQLiteFeatureProject(t *testing.T, ctx context.Context) (*SQLite, domain.User, domain.Computer, domain.Project) {
	t.Helper()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "features.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "features"})
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	return repository, user, computer, project
}

func insertSQLiteFeatureTenant(t *testing.T, ctx context.Context, repository *SQLite) (string, string) {
	t.Helper()
	userID, computerID := newID("usr"), newID("cmp")
	now := sqliteTimeValue(time.Now())
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, status, version, created_at, updated_at)
		VALUES (?, ?, 'Other', 'active', 1, ?, ?)
	`, userID, userID+"@teamva.invalid", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO computers (
			id, user_id, name, platform, architecture, fingerprint, status,
			protocol_version, version, created_at, updated_at
		) VALUES (?, ?, 'Other computer', 'test', 'test', ?, 'online', 1, 1, ?, ?)
	`, computerID, userID, "fingerprint-"+computerID, now, now); err != nil {
		t.Fatal(err)
	}
	return userID, computerID
}

func insertSQLiteFeatureRun(t *testing.T, ctx context.Context, repository *SQLite, projectID, id string) string {
	t.Helper()
	now := sqliteTimeValue(time.Now())
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO runs (id, project_id, status, input, idempotency_key, created_at, updated_at)
		VALUES (?, ?, 'succeeded', '[]', ?, ?, ?)
	`, id, projectID, "audit:"+id, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}
