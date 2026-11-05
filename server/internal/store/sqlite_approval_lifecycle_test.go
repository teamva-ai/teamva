package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func waitingSQLiteApproval(t *testing.T, f sqliteExecutionFixture, key string, expires time.Time) (domain.Run, domain.Approval) {
	t.Helper()
	ctx := t.Context()
	run := f.createRun(t, key)
	start, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || start.Name != "run.start" {
		t.Fatalf("start=%+v ok=%t err=%v", start, ok, err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, start.ID, start.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	requested := expires.Add(-10 * time.Minute)
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{
		{ID: key + "-started", RunID: run.ID, LeaseEpoch: 1, Sequence: 1, Type: "run.started", OccurredAt: requested.Add(-time.Second)},
		{ID: key + "-approval", RunID: run.ID, LeaseEpoch: 1, Sequence: 2, Type: "approval.requested", OccurredAt: requested,
			Payload: map[string]any{"request_id": key, "action_kind": "shell", "normalized_action": map[string]any{"command": "pwd"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var approval domain.Approval
	if err := scanSQLiteApproval(f.repository.db.QueryRowContext(ctx, sqliteApprovalSelect+` WHERE approvals.run_id = ?`, run.ID), &approval); err != nil {
		t.Fatal(err)
	}
	if approval.Status != "pending" || !approval.ExpiresAt.Equal(expires) {
		t.Fatalf("native approval deadline = %+v, want %v", approval, expires)
	}
	return run, approval
}

func requireSQLiteRunStatus(t *testing.T, f sqliteExecutionFixture, runID, want string) {
	t.Helper()
	run, err := f.repository.GetRun(t.Context(), f.user.ID, runID)
	if err != nil || run.Status != want {
		t.Fatalf("run=%+v err=%v, want status %s", run, err, want)
	}
}

func TestSQLiteApprovalExpiryPersistsOneNativeDenialAndResumesOnlyAfterDelivery(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	run, approval := waitingSQLiteApproval(t, f, "deadline", time.Now().UTC().Add(-time.Second))
	requireSQLiteRunStatus(t, f, run.ID, domain.RunWaiting)
	command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err == nil && !ok {
		// Expiration enqueues with the transaction's wall clock, which can be
		// slightly newer than this claim's cutoff. The next dispatch tick sees it.
		command, ok, err = f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	}
	if err != nil || !ok || command.Name != "approval.decide" {
		t.Fatalf("expiry denial command=%+v ok=%t err=%v", command, ok, err)
	}
	var decision nodeprotocol.ApprovalDecisionPayload
	if err := json.Unmarshal(command.Payload, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "denied" || decision.Scope != "once" || decision.RequestID != approval.ProviderApprovalRef || decision.RunID != run.ID {
		t.Fatalf("unsafe timeout decision: %+v", decision)
	}
	stored, err := f.repository.GetApproval(ctx, f.user.ID, approval.ID)
	if err != nil || stored.Status != "expired" || stored.Version != approval.Version+1 || stored.DecidedAt == nil {
		t.Fatalf("expired approval=%+v err=%v", stored, err)
	}
	for _, value := range []string{"approved", "denied"} {
		_, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, stored.Version, approval.ActionDigest, value, "once")
		assertDomainCode(t, err, "approval_expired")
	}
	if count, err := f.repository.ExpireLocalApprovals(ctx, time.Now().UTC().Add(time.Minute)); err != nil || count != 0 {
		t.Fatalf("repeat expiration count=%d err=%v", count, err)
	}
	pending, err := f.repository.ListApprovals(ctx, f.user.ID, "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("expired approval still listed pending: %+v err=%v", pending, err)
	}
	requireSQLiteRunStatus(t, f, run.ID, domain.RunWaiting)
	if err := f.repository.MarkLocalCommandUncertain(ctx, command.ID, command.ClaimToken, "transport_unknown"); err != nil {
		t.Fatal(err)
	}
	retry, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || retry.ID != command.ID || retry.IdempotencyKey != command.IdempotencyKey {
		t.Fatalf("denial retry was not stable: %+v ok=%t err=%v", retry, ok, err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, retry.ID, retry.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	requireSQLiteRunStatus(t, f, run.ID, domain.RunRunning)
	var commands int
	if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM command_outbox WHERE command_name = 'approval.decide'`).Scan(&commands); err != nil || commands != 1 {
		t.Fatalf("expiry commands=%d err=%v", commands, err)
	}
}

func TestSQLiteExpiredDecisionCommitsDenialDespiteReturningConflict(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	_, approval := waitingSQLiteApproval(t, f, "expired-ui", time.Now().UTC().Add(-time.Second))
	_, err := f.repository.DecideApproval(t.Context(), f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "session")
	assertDomainCode(t, err, "approval_expired")
	var status string
	var commands int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT status FROM approvals WHERE id = ?`, approval.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM command_outbox WHERE command_name = 'approval.decide' AND json_extract(payload, '$.decision') = 'denied'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || commands != 1 {
		t.Fatalf("409 rolled back expiry: status=%s commands=%d", status, commands)
	}
}

func TestSQLiteNativeDecisionResumesRunOnlyAfterConfirmedDelivery(t *testing.T) {
	for _, value := range []string{"approved", "denied"} {
		t.Run(value, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			ctx := t.Context()
			run, approval := waitingSQLiteApproval(t, f, value, time.Now().UTC().Add(time.Minute))
			if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, value, "once"); err != nil {
				t.Fatal(err)
			}
			requireSQLiteRunStatus(t, f, run.ID, domain.RunWaiting)
			command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
			if err != nil || !ok || command.Name != "approval.decide" {
				t.Fatalf("claim=%+v ok=%t err=%v", command, ok, err)
			}
			if err := f.repository.CompleteLocalCommand(ctx, command.ID, command.ClaimToken, nil); err != nil {
				t.Fatal(err)
			}
			requireSQLiteRunStatus(t, f, run.ID, domain.RunRunning)
		})
	}
}

func TestSQLiteTerminalRunFencesAlreadyClaimedApprovalCompletion(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	run, approval := waitingSQLiteApproval(t, f, "terminal-in-flight", time.Now().UTC().Add(time.Minute))
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "once"); err != nil {
		t.Fatal(err)
	}
	command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%t err=%v", command, ok, err)
	}
	if err := f.repository.FailLocalRun(ctx, run.ID, "terminal-before-decision-ack"); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, command.ID, command.ClaimToken, nil); err == nil {
		t.Fatal("late approved result overwrote terminal Run/command")
	}
	requireSQLiteRunStatus(t, f, run.ID, domain.RunFailed)
}

func TestSQLiteOldApprovalCompletionCannotClearNewerWaitingApproval(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	run, first := waitingSQLiteApproval(t, f, "first-request", time.Now().UTC().Add(time.Minute))
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, first.ID, first.Version, first.ActionDigest, "approved", "once"); err != nil {
		t.Fatal(err)
	}
	command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%t err=%v", command, ok, err)
	}
	if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{
		ID: "newer-approval", RunID: run.ID, LeaseEpoch: 1, Sequence: 3, Type: "approval.requested", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"request_id": "newer-request", "normalized_action": map[string]any{"command": "ls"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, command.ID, command.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	requireSQLiteRunStatus(t, f, run.ID, domain.RunWaiting)
	pending, err := f.repository.ListApprovals(ctx, f.user.ID, "pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("newer pending=%+v err=%v", pending, err)
	}
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, pending[0].ID, pending[0].Version, pending[0].ActionDigest, "denied", "once"); err != nil {
		t.Fatal(err)
	}
	next, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok || next.ID == command.ID {
		t.Fatalf("newer decision=%+v ok=%t err=%v", next, ok, err)
	}
	if err := f.repository.CompleteLocalCommand(ctx, next.ID, next.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	requireSQLiteRunStatus(t, f, run.ID, domain.RunRunning)
}

func TestSQLiteApprovalDecisionAndExpiryAreSerializedWithTerminalState(t *testing.T) {
	for iteration := range 8 {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			ctx := t.Context()
			expires := time.Now().UTC().Add(time.Hour)
			run, approval := waitingSQLiteApproval(t, f, "race", expires)
			var wg sync.WaitGroup
			start := make(chan struct{})
			errorsOut := make(chan error, 3)
			for _, action := range []func() error{
				func() error { _, err := f.repository.ExpireLocalApprovals(ctx, expires); return err },
				func() error {
					_, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "once")
					var conflict *domain.Error
					if errors.As(err, &conflict) && conflict.HTTPStatus == 409 {
						return nil
					}
					return err
				},
				func() error { return f.repository.FailLocalRun(ctx, run.ID, "terminal-race") },
			} {
				wg.Add(1)
				go func(action func() error) { defer wg.Done(); <-start; errorsOut <- action() }(action)
			}
			close(start)
			wg.Wait()
			close(errorsOut)
			for err := range errorsOut {
				if err != nil {
					t.Fatal(err)
				}
			}
			requireSQLiteRunStatus(t, f, run.ID, domain.RunFailed)
			var pending, liveCommands, decisions int
			if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*) FROM approvals WHERE run_id = ? AND status = 'pending'`, run.ID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if err := f.repository.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(status IN ('pending','processing','uncertain')), 0) FROM command_outbox WHERE command_name = 'approval.decide'`).Scan(&decisions, &liveCommands); err != nil {
				t.Fatal(err)
			}
			if pending != 0 || liveCommands != 0 || decisions > 1 {
				t.Fatalf("terminal race left pending=%d live_commands=%d decisions=%d", pending, liveCommands, decisions)
			}
		})
	}
}

func TestSQLiteStopFencesInFlightApprovalAndLateProviderEvents(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint("expired=", expired), func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			ctx := t.Context()
			expires := time.Now().UTC().Add(time.Minute)
			if expired {
				expires = time.Now().UTC().Add(-time.Second)
			}
			run, approval := waitingSQLiteApproval(t, f, "stop-fence", expires)
			if !expired {
				if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "once"); err != nil {
					t.Fatal(err)
				}
			}
			old, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
			if err == nil && !ok {
				old, ok, err = f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
			}
			if err != nil || !ok || old.Name != "approval.decide" {
				t.Fatalf("old=%+v ok=%t err=%v", old, ok, err)
			}
			if _, err := f.repository.StopRun(ctx, f.user.ID, run.ID, "force"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{
				{ID: "late-started", RunID: run.ID, LeaseEpoch: 1, Sequence: 3, Type: "run.started", OccurredAt: time.Now().UTC()},
				{ID: "late-request", RunID: run.ID, LeaseEpoch: 1, Sequence: 4, Type: "approval.requested", OccurredAt: time.Now().UTC(), Payload: map[string]any{"request_id": "after-stop"}},
			}); err != nil {
				t.Fatal(err)
			}
			requireSQLiteRunStatus(t, f, run.ID, domain.RunCancelling)
			stop, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute, old.ID)
			if err != nil || !ok || stop.Name != "run.stop" {
				t.Fatalf("stop=%+v ok=%t err=%v", stop, ok, err)
			}
			if err := f.repository.CompleteLocalCommand(ctx, old.ID, old.ClaimToken, nil); err == nil {
				t.Fatal("late authorization overwrote stopped run")
			}
			if err := f.repository.CompleteLocalCommand(ctx, stop.ID, stop.ClaimToken, nil); err != nil {
				t.Fatal(err)
			}
			requireSQLiteRunStatus(t, f, run.ID, domain.RunCancelled)
		})
	}
}

func TestSQLiteRestartClosesUnconfirmedApprovalDecision(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()
	run, approval := waitingSQLiteApproval(t, f, "restart-decision", time.Now().UTC().Add(time.Minute))
	if _, err := f.repository.DecideApproval(ctx, f.user.ID, approval.ID, approval.Version, approval.ActionDigest, "approved", "once"); err != nil {
		t.Fatal(err)
	}
	command, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%t err=%v", command, ok, err)
	}
	if err := f.repository.MarkLocalCommandUncertain(ctx, command.ID, command.ClaimToken, "crash-before-ack"); err != nil {
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
	requireSQLiteRunStatus(t, f, run.ID, domain.RunFailed)
	stored, err := f.repository.GetLocalCommand(ctx, command.ID)
	if err != nil || stored.Status != LocalCommandFailed {
		t.Fatalf("unconfirmed decision recovered as %+v, err=%v", stored, err)
	}
	if next, ok, err := f.repository.ClaimLocalCommand(ctx, time.Now().UTC(), time.Minute); err != nil || ok {
		t.Fatalf("replayed obsolete authorization after restart: %+v ok=%t err=%v", next, ok, err)
	}
}
