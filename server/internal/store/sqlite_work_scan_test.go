package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueWorkPendingIndexLifecycle(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	assertPending := func(want int) {
		t.Helper()
		var got int
		if err := f.repository.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key=?`, "issue_work_pending:v1:"+f.issue.ID+":"+comment.ID).Scan(&got); err != nil || got != want {
			t.Fatalf("pending marker = %d, want %d: %v", got, want, err)
		}
	}
	assertPending(1)
	finishWork(t, f, consumeWork(t, f), "ready for next item", "completed")
	advanceWork(t, f, 1)
	assertPending(0)
	if len(workComment(t, f, comment.ID).WorkItems) != 2 {
		t.Fatal("finished batch history was discarded")
	}
	f = newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment = createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	assertPending(1)
	if err := f.repository.DeleteIssueComment(t.Context(), f.user.ID, comment.ID, comment.Version); err != nil {
		t.Fatal(err)
	}
	assertPending(0)
}

func TestIssueWorkPendingMissingBatchFailsClosed(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	if _, err := f.repository.db.Exec(`DELETE FROM app_metadata WHERE key=?`, issueWorkKey(f.issue.ID, comment.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.AdvanceIssueWorkItems(t.Context(), time.Now()); err == nil {
		t.Fatal("lost pending batch silently ignored")
	}
}

func TestIssueWorkPendingIndexMissingRefusesReopen(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	if _, err := f.repository.db.Exec(`DELETE FROM app_metadata WHERE key GLOB 'issue_work_pending:v1:*'`); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenSQLite(t.Context(), f.path); err == nil {
		reopened.Close()
		t.Fatal("missing scheduling index was silently accepted")
	}
}

func TestDeleteIssueRetiresWorkSchedulingAtomically(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	if _, err := f.repository.db.Exec(`CREATE TRIGGER fail_retire_work BEFORE DELETE ON app_metadata WHEN OLD.key GLOB 'issue_work_pending:v1:*' BEGIN SELECT RAISE(ABORT,'test retirement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.DeleteIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version); err == nil {
		t.Fatal("index retirement failure did not roll back deletion")
	}
	if _, err := f.repository.GetIssue(t.Context(), f.user.ID, f.issue.ID); err != nil {
		t.Fatal("failed deletion hid issue")
	}
	if _, err := f.repository.db.Exec(`DROP TRIGGER fail_retire_work`); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.DeleteIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key GLOB 'issue_work_pending:v1:*'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted work still scheduled: %d %v", count, err)
	}
	batch, exists, err := readSQLiteIssueWorkBatch(t.Context(), f.repository.db, f.issue.ID, comment.ID)
	if err != nil || !exists || !batch.Cancelled {
		t.Fatalf("work history lost or not cancelled: %v %v", exists, err)
	}
	if err := checkSQLiteIssueWorkIndex(t.Context(), f.repository.db); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkAdvanceIssueWorkItemsCompletedHistory(b *testing.B) {
	repository, err := OpenSQLite(b.Context(), filepath.Join(b.TempDir(), "app.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer repository.Close()
	tx, err := repository.db.BeginTx(b.Context(), nil)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	raw := `{"schema":1,"items":[{"mention_id":"wmen_done","instruction":"` + strings.Repeat("x", 2048) + `"}]}`
	for i := range 2000 {
		if _, err := tx.Exec(`INSERT INTO app_metadata(key,value) VALUES(?,?)`, fmt.Sprintf("issue_work:v1:issue:%06d", i), raw); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if count, err := repository.AdvanceIssueWorkItems(b.Context(), time.Now()); err != nil || count != 0 {
			b.Fatalf("advanced %d: %v", count, err)
		}
	}
}
