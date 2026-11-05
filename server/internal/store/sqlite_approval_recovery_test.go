package store

import (
	"testing"
	"time"
)

func TestSQLiteApprovalListAndRestartRecoverExpiredPending(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := t.Context()
	run, approval := waitingSQLiteApproval(t, f, "expired-recovery", time.Now().UTC().Add(-time.Second))
	items, err := f.repository.ListApprovals(ctx, f.user.ID, "pending")
	if err != nil || len(items) != 0 {
		t.Fatalf("list left expired native request pending: %+v err=%v", items, err)
	}
	var commandID string
	if err := f.repository.db.QueryRowContext(ctx, `SELECT id FROM command_outbox WHERE command_name = 'approval.decide'`).Scan(&commandID); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	f.repository, err = OpenSQLite(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RecoverLocalExecution(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := f.repository.GetApproval(ctx, f.user.ID, approval.ID)
	if err != nil || stored.Status != "expired" || stored.Version != approval.Version+1 {
		t.Fatalf("restart lost durable expiration: %+v err=%v", stored, err)
	}
	command, err := f.repository.GetLocalCommand(ctx, commandID)
	if err != nil || command.Status != LocalCommandFailed {
		t.Fatalf("restart replayed native decision without its live Run: %+v err=%v", command, err)
	}
	recovered, err := f.repository.GetRun(ctx, f.user.ID, run.ID)
	if err != nil || recovered.TerminalReason != "interrupted" {
		t.Fatalf("restart left Provider approval waiting: %+v err=%v", recovered, err)
	}
}
