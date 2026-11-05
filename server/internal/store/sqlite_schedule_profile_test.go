package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func createProfileSchedule(t *testing.T, f sqliteExecutionFixture, mode string) domain.ScheduledTask {
	t.Helper()
	task := domain.ScheduledTask{ProjectID: f.project.ID, Name: "定时工作", AgentID: f.agent.ID,
		ScheduleKind: "daily", Times: []string{"17:45", "08:15", "08:15"}, Timezone: "Asia/Shanghai",
		IssueMode: mode, Prompt: []domain.ContentBlock{{Type: "text", Text: "请执行真实定时指令"}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once"}
	if mode == "existing" {
		task.IssueID = f.issue.ID
	}
	created, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, task)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestSQLiteScheduledIssueModesAtomicAndHistory(t *testing.T) {
	for _, mode := range []string{"new", "existing"} {
		t.Run(mode, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			task := createProfileSchedule(t, f, mode)
			first, replayed, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:first", time.Now())
			if err != nil || replayed || first.IssueID == "" || first.RunID == "" {
				t.Fatalf("first=%+v replay=%t err=%v", first, replayed, err)
			}
			if (mode == "existing") != (first.IssueID == f.issue.ID) {
				t.Fatal(first)
			}
			run, err := f.repository.GetRun(t.Context(), f.user.ID, first.RunID)
			if err != nil || run.IssueID != first.IssueID || run.IssueAgentInstanceID == "" || run.ReadOnly {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			if !strings.Contains(run.Input[0].Text, "请执行真实定时指令") || !strings.Contains(run.Input[0].Text, "issue_comment_create") {
				t.Fatal(run.Input)
			}
			comments, err := f.repository.ListIssueComments(t.Context(), f.user.ID, first.IssueID)
			if err != nil || len(comments) != 2 {
				t.Fatalf("comments=%+v err=%v", comments, err)
			}
			var users, working int
			for _, comment := range comments {
				if comment.AuthorKind == "user" && comment.Body == task.Prompt[0].Text {
					users++
				}
				if comment.Status == "working" {
					working++
				}
			}
			if users != 1 || working != 1 {
				t.Fatal(comments)
			}
			var consumed int
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_comment_mentions m JOIN issue_comments c ON c.id=m.comment_id WHERE c.issue_id=? AND m.status='completed' AND m.consumed_at IS NOT NULL`, first.IssueID).Scan(&consumed); err != nil || consumed != 1 {
				t.Fatalf("consumed=%d err=%v", consumed, err)
			}
			replay, yes, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:first", time.Now())
			if err != nil || !yes || replay.IssueID != first.IssueID || replay.RunID != first.RunID {
				t.Fatalf("replay=%+v yes=%t err=%v", replay, yes, err)
			}
			_, _, err = f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:other", time.Now())
			assertDomainCode(t, err, "schedule_concurrency_forbidden")
			history, err := f.repository.ListScheduledTaskRuns(t.Context(), f.user.ID, task.ID)
			if err != nil || len(history) != 1 || history[0].IssueID != first.IssueID {
				t.Fatalf("history=%+v err=%v", history, err)
			}
			if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=?`, first.RunID); err != nil {
				t.Fatal(err)
			}
			second, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:second", time.Now())
			if err != nil || (mode == "new" && second.IssueID == first.IssueID) || (mode == "existing" && second.IssueID != first.IssueID) {
				t.Fatalf("second=%+v err=%v", second, err)
			}
		})
	}
}

func TestSQLiteScheduleProfilePauseReopenAndCalendarAdvance(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	task := createProfileSchedule(t, f, "new")
	paused, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, task.Version, func(task *domain.ScheduledTask) { task.Enabled = false })
	if err != nil || paused.IssueMode != "new" || len(paused.Times) != 2 {
		t.Fatalf("paused=%+v err=%v", paused, err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	due := time.Date(2026, 10, 22, 0, 15, 0, 0, time.UTC)
	resumed, err := repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, paused.Version, func(task *domain.ScheduledTask) { task.Enabled = true; task.NextRunAt = &due })
	if err != nil || resumed.ScheduleKind != "daily" || resumed.Times[0] != "08:15" {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	want, err := domain.NextScheduledTaskTime(resumed, due)
	if err != nil {
		t.Fatal(err)
	}
	count, err := repository.TriggerDueLocalSchedules(t.Context(), due)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	stored, err := repository.GetScheduledTask(t.Context(), f.user.ID, task.ID)
	if err != nil || stored.NextRunAt == nil || !stored.NextRunAt.Equal(want) {
		t.Fatalf("stored=%+v want=%s err=%v", stored, want, err)
	}
}

func TestSQLiteScheduleExistingRejectsUnavailableAndTenant(t *testing.T) {
	for _, state := range []string{"closed", "resolved", "deleted", "paused", "cross_project"} {
		t.Run(state, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			task := createProfileSchedule(t, f, "existing")
			switch state {
			case "closed", "resolved":
				_, _ = f.repository.db.ExecContext(t.Context(), `UPDATE issues SET status=? WHERE id=?`, state, f.issue.ID)
			case "deleted":
				_, _ = f.repository.db.ExecContext(t.Context(), `UPDATE issues SET deleted_at=? WHERE id=?`, sqliteTimeValue(time.Now()), f.issue.ID)
			case "paused":
				_, _ = f.repository.db.ExecContext(t.Context(), `UPDATE issue_agent_instances SET status='paused' WHERE id=?`, f.instance.ID)
			case "cross_project":
				project, err := f.repository.CreateProject(t.Context(), f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "other"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.repository.db.ExecContext(t.Context(), `UPDATE issues SET project_id=? WHERE id=?`, project.ID, f.issue.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:reject", time.Now()); err == nil {
				t.Fatal("triggered unavailable target")
			}
			var count int
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_task_runs WHERE scheduled_task_id=?`, task.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("count=%d err=%v", count, err)
			}
			if _, err := f.repository.GetScheduledTask(t.Context(), "foreign-user", task.ID); err == nil {
				t.Fatal("foreign owner read schedule")
			}
			due := time.Now().Add(-time.Minute)
			if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE scheduled_tasks SET next_run_at=? WHERE id=?`, sqliteTimeValue(due), task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.TriggerDueLocalSchedules(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			stored, err := f.repository.GetScheduledTask(t.Context(), f.user.ID, task.ID)
			if err != nil || stored.Enabled || stored.LastDispatchError == "" || stored.IssueMode != "existing" {
				t.Fatalf("stored=%+v err=%v", stored, err)
			}
		})
	}
}

func TestSQLiteScheduleProfileRollbackAndFailClosed(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	task := createProfileSchedule(t, f, "new")
	if _, err := f.repository.db.ExecContext(t.Context(), `CREATE TRIGGER fail_schedule_profile BEFORE UPDATE ON app_metadata WHEN NEW.key LIKE 'scheduled_task_config:%' BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, task.Version, func(task *domain.ScheduledTask) { task.Name = "must roll back"; task.Times = []string{"09:00"} })
	if err == nil {
		t.Fatal("expected failure")
	}
	stored, err := f.repository.GetScheduledTask(t.Context(), f.user.ID, task.ID)
	if err != nil || stored.Name != task.Name || stored.Version != task.Version || len(stored.Times) != 2 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER fail_schedule_profile; DELETE FROM app_metadata WHERE key=?`, "scheduled_task_config:"+task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:missing", time.Now()); err == nil {
		t.Fatal("missing profile fell back")
	}
	due := time.Now().Add(-time.Minute)
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE scheduled_tasks SET next_run_at=? WHERE id=?`, sqliteTimeValue(due), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.TriggerDueLocalSchedules(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var enabled, attempts int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT enabled,dispatch_attempts FROM scheduled_tasks WHERE id=?`, task.ID).Scan(&enabled, &attempts); err != nil || enabled != 0 || attempts != 1 {
		t.Fatalf("enabled=%d attempts=%d err=%v", enabled, attempts, err)
	}
}

func TestSQLiteScheduleConcurrentIdempotencyAndAtomicRunFailure(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	task := createProfileSchedule(t, f, "new")
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.repository.TriggerScheduledTask(context.Background(), task.ID, "manual:parallel", time.Now())
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issues WHERE project_id=?`, f.project.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("issues=%d err=%v", count, err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE runs SET status='succeeded'; CREATE TRIGGER fail_scheduled_run BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'test run failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:rollback", time.Now()); err == nil {
		t.Fatal("run insert accepted")
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issues WHERE project_id=?`, f.project.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("partial issue=%d err=%v", count, err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER fail_scheduled_run`); err != nil {
		t.Fatal(err)
	}
	paused, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, task.Version, func(task *domain.ScheduledTask) { task.Enabled = false })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(t.Context(), paused.ID, "manual:stopped", time.Now()); err == nil {
		t.Fatal("paused task triggered")
	}
}

func TestSQLiteScheduleProfileCreationRollbackCorruptionAndLegacy(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	if _, err := f.repository.db.ExecContext(t.Context(), `CREATE TRIGGER fail_profile_creation BEFORE INSERT ON app_metadata WHEN NEW.key LIKE 'scheduled_task_config:%' BEGIN SELECT RAISE(ABORT,'test creation rollback'); END`); err != nil {
		t.Fatal(err)
	}
	input := domain.ScheduledTask{ProjectID: f.project.ID, Name: "rollback", AgentID: f.agent.ID, ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "UTC", IssueMode: "new", Prompt: []domain.ContentBlock{{Type: "text", Text: "work"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "skip"}
	if _, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, input); err == nil {
		t.Fatal("creation should roll back")
	}
	var count int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER fail_profile_creation`); err != nil {
		t.Fatal(err)
	}
	task, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE app_metadata SET value='{"version":2,"issue_mode":"new"}' WHERE key=?`, "scheduled_task_config:"+task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:corrupt", time.Now()); err == nil {
		t.Fatal("corrupt profile ran")
	}
	// Old repository callers/rows without a profile keep their standalone behavior.
	input.IssueMode = ""
	legacy, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	trigger, _, err := f.repository.TriggerScheduledTask(t.Context(), legacy.ID, "manual:legacy", time.Now())
	if err != nil || trigger.RunID == "" || trigger.IssueID != "" {
		t.Fatalf("legacy=%+v err=%v", trigger, err)
	}
}

func TestSQLiteScheduleRejectsDisabledWriterButStillAllowsPause(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	task := createProfileSchedule(t, f, "new")
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE agents SET status='disabled' WHERE id=?`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:disabled", time.Now()); err == nil {
		t.Fatal("disabled writer triggered")
	}
	paused, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, task.Version, func(task *domain.ScheduledTask) { task.Enabled = false })
	if err != nil || paused.Enabled || paused.IssueMode != "new" {
		t.Fatalf("paused=%+v err=%v", paused, err)
	}
	if _, err := f.repository.UpdateScheduledTask(t.Context(), f.user.ID, task.ID, paused.Version, func(task *domain.ScheduledTask) { task.Enabled = true }); err == nil {
		t.Fatal("resumed disabled writer")
	}
}

func TestSQLiteProfileOnceCronAndWeeklyDispatchUseHydratedCalendar(t *testing.T) {
	for _, kind := range []string{"once", "cron", "weekly"} {
		t.Run(kind, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			due := time.Date(2030, 11, 3, 5, 30, 0, 0, time.UTC)
			input := domain.ScheduledTask{ProjectID: f.project.ID, Name: kind, AgentID: f.agent.ID, ScheduleKind: kind, Timezone: "America/New_York", IssueMode: "new", Prompt: []domain.ContentBlock{{Type: "text", Text: "work"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once", NextRunAt: &due}
			switch kind {
			case "once":
				input.ScheduleExpression = "2030-11-03T01:30:00-04:00"
			case "cron":
				input.ScheduleExpression = "30 1 * * *"
			case "weekly":
				input.Times = []string{"01:30"}
				input.Weekdays = []int{0}
			}
			task, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.TriggerDueLocalSchedules(t.Context(), due); err != nil {
				t.Fatal(err)
			}
			stored, err := f.repository.GetScheduledTask(t.Context(), f.user.ID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ScheduleKind != kind || stored.IssueMode != "new" {
				t.Fatal(stored)
			}
			if kind == "once" {
				if stored.Enabled || stored.NextRunAt != nil {
					t.Fatal(stored)
				}
			} else {
				want, err := domain.NextScheduledTaskTime(task, due)
				if err != nil {
					t.Fatal(err)
				}
				if stored.NextRunAt == nil || !stored.NextRunAt.Equal(want) {
					t.Fatalf("stored=%+v want=%v", stored, want)
				}
			}
			history, err := f.repository.ListScheduledTaskRuns(t.Context(), f.user.ID, task.ID)
			if err != nil || len(history) != 1 || history[0].IssueID == "" || !history[0].ScheduledFor.Equal(due) {
				t.Fatalf("history=%+v err=%v", history, err)
			}
		})
	}
}
