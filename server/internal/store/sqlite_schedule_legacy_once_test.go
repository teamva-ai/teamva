package store

import (
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func createLegacyPastOnce(t *testing.T, f sqliteExecutionFixture) domain.ScheduledTask {
	t.Helper()
	due := time.Now().UTC().Add(-2 * time.Minute)
	task, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, domain.ScheduledTask{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Name: "legacy past once",
		ScheduleKind: "once", ScheduleExpression: due.Format(time.RFC3339), Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "legitimate past instruction"}},
		ConcurrencyPolicy: "allow", MisfirePolicy: "run_once", NextRunAt: &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	var profiles int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM app_metadata WHERE key = ?`, "scheduled_task_config:"+task.ID).Scan(&profiles); err != nil || profiles != 0 || task.IssueMode != "" {
		t.Fatalf("not a legacy task: profiles=%d task=%+v err=%v", profiles, task, err)
	}
	return task
}

// Include every dispatch side effect, not just the schedule's history row.
func legacyOnceSideEffects(t *testing.T, repository *SQLite) [6]int {
	t.Helper()
	var counts [6]int
	err := repository.db.QueryRowContext(t.Context(), `SELECT
		(SELECT count(*) FROM runs),
		(SELECT count(*) FROM issues),
		(SELECT count(*) FROM issue_comments),
		(SELECT count(*) FROM issue_comment_mentions),
		(SELECT count(*) FROM scheduled_task_runs),
		(SELECT count(*) FROM command_outbox WHERE command_name = 'run.start')`).Scan(
		&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5])
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestSQLiteLegacyOnceCorruptCalendarRejectsManualAndIsolatesDue(t *testing.T) {
	for _, corruption := range []string{"expression", "timezone"} {
		for _, trigger := range []string{"manual", "due"} {
			t.Run(corruption+"/"+trigger, func(t *testing.T) {
				f := newSQLiteExecutionFixture(t)
				defer f.repository.Close()
				task := createLegacyPastOnce(t, f)
				statement := `UPDATE scheduled_tasks SET schedule_expression = 'not-rfc3339' WHERE id = ?`
				if corruption == "timezone" {
					statement = `UPDATE scheduled_tasks SET timezone = 'Invalid/Timezone' WHERE id = ?`
				}
				if _, err := f.repository.db.ExecContext(t.Context(), statement, task.ID); err != nil {
					t.Fatal(err)
				}
				before := legacyOnceSideEffects(t, f.repository)
				if trigger == "manual" {
					_, _, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, "manual:corrupt-once", time.Now().UTC())
					assertDomainCode(t, err, "invalid_argument")
					if after := legacyOnceSideEffects(t, f.repository); after != before {
						t.Fatalf("invalid manual trigger created side effects: before=%v after=%v", before, after)
					}
					return
				}

				// A malformed older occurrence must not prevent another legitimate
				// past once from being dispatched in the same scheduler tick.
				healthy := createLegacyPastOnce(t, f)
				handled, err := f.repository.TriggerDueLocalSchedules(t.Context(), time.Now().UTC())
				if err != nil || handled != 2 {
					t.Fatalf("handled=%d err=%v", handled, err)
				}
				want := before
				want[0]++ // Only the healthy standalone Run.
				want[4]++
				want[5]++
				if after := legacyOnceSideEffects(t, f.repository); after != want {
					t.Fatalf("invalid due occurrence created side effects: want=%v after=%v", want, after)
				}
				stored, err := f.repository.GetScheduledTask(t.Context(), f.user.ID, task.ID)
				if err != nil || stored.Enabled || stored.DispatchAttempts != 1 || stored.LastDispatchError != "invalid_argument" {
					t.Fatalf("malformed once was not isolated: task=%+v err=%v", stored, err)
				}
				history, err := f.repository.ListScheduledTaskRuns(t.Context(), f.user.ID, task.ID)
				if err != nil || len(history) != 0 {
					t.Fatalf("invalid once history=%+v err=%v", history, err)
				}
				history, err = f.repository.ListScheduledTaskRuns(t.Context(), f.user.ID, healthy.ID)
				if err != nil || len(history) != 1 || history[0].RunID == "" || history[0].IssueID != "" {
					t.Fatalf("healthy once history=%+v err=%v", history, err)
				}
				stored, err = f.repository.GetScheduledTask(t.Context(), f.user.ID, healthy.ID)
				if err != nil || stored.Enabled || stored.NextRunAt != nil {
					t.Fatalf("healthy once was not completed: task=%+v err=%v", stored, err)
				}
				if handled, err := f.repository.TriggerDueLocalSchedules(t.Context(), time.Now().UTC()); err != nil || handled != 0 {
					t.Fatalf("disabled once retriggered: handled=%d err=%v", handled, err)
				}
			})
		}
	}
}

func TestSQLiteLegacyPastOnceAllowsManualRerun(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	task := createLegacyPastOnce(t, f)
	before := legacyOnceSideEffects(t, f.repository)
	var firstRunID string
	for _, key := range []string{"manual:past-once-first", "manual:past-once-again"} {
		trigger, replayed, err := f.repository.TriggerScheduledTask(t.Context(), task.ID, key, time.Now().UTC())
		if err != nil || replayed || trigger.RunID == "" || trigger.RunID == firstRunID || trigger.IssueID != "" {
			t.Fatalf("legitimate past once rejected: trigger=%+v replayed=%t err=%v", trigger, replayed, err)
		}
		firstRunID = trigger.RunID
	}
	want := before
	want[0] += 2
	want[4] += 2
	want[5] += 2
	if after := legacyOnceSideEffects(t, f.repository); after != want {
		t.Fatalf("past once changed standalone semantics: want=%v after=%v", want, after)
	}
}
