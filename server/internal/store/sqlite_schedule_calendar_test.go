package store

import (
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteScheduleRejectsImpossibleCalendarWithoutVersionChurn(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	due := time.Time{}
	input := domain.ScheduledTask{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Name: "impossible",
		ScheduleKind: "cron", ScheduleExpression: "0 0 31 2 *", Timezone: "UTC",
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "must not run"}},
		ConcurrencyPolicy: "allow", MisfirePolicy: "run_once", NextRunAt: &due,
	}
	_, err := f.repository.CreateScheduledTask(ctx, f.user.ID, input)
	assertDomainCode(t, err, "schedule_no_next_run")
	input.ScheduleExpression = "0 0 * * *"
	task, err := f.repository.CreateScheduledTask(ctx, f.user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.UpdateScheduledTask(ctx, f.user.ID, task.ID, task.Version, func(item *domain.ScheduledTask) {
		item.ScheduleExpression = "0 0 31 2 *"
	})
	assertDomainCode(t, err, "schedule_no_next_run")
	stored, err := f.repository.GetScheduledTask(ctx, f.user.ID, task.ID)
	if err != nil || stored.Version != task.Version || stored.ScheduleExpression != task.ScheduleExpression {
		t.Fatalf("invalid update changed task=%+v err=%v", stored, err)
	}
	// Exercise a malformed durable row, including the old zero-time cursor.
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE scheduled_tasks SET schedule_expression = '0 0 31 2 *' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:impossible", time.Now().UTC()); err == nil {
		t.Fatal("manual trigger ran impossible calendar")
	}
	for iteration := range 3 {
		handled, err := f.repository.TriggerDueLocalSchedules(ctx, time.Now().UTC())
		wantHandled := 0
		if iteration == 0 {
			wantHandled = 1
		}
		if err != nil || handled != wantHandled {
			t.Fatalf("iteration=%d handled=%d err=%v", iteration, handled, err)
		}
		stored, err := f.repository.GetScheduledTask(ctx, f.user.ID, task.ID)
		if err != nil || stored.Enabled || stored.LastDispatchError != "schedule_no_next_run" || stored.DispatchAttempts != 1 || stored.Version != task.Version+1 {
			t.Fatalf("invalid calendar did not disable exactly once: %+v err=%v", stored, err)
		}
	}
	var runs, commands int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs WHERE scheduled_task_id = ?`, task.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || commands != 0 {
		t.Fatalf("run_once executed impossible calendar: runs=%d commands=%d", runs, commands)
	}
}
