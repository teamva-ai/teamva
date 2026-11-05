package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestStopReasonPreservesAuditAndUsesValidRuntimeMode(t *testing.T) {
	for _, reason := range []string{"graceful", "force", "conversation_goal_pause", "conversation_goal_cancel", "conversation_goal_settled"} {
		t.Run(reason, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			run := f.createRun(t, "stop-reason")
			tx, err := f.repository.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := requestSQLiteGoalRunStopTx(t.Context(), tx, run.ID, reason, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			var key string
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT payload,idempotency_key FROM command_outbox WHERE command_name='run.stop' AND json_extract(payload,'$.run_id')=?`, run.ID).Scan(&raw, &key); err != nil {
				t.Fatal(err)
			}
			var payload nodeprotocol.StopRunPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			want := "graceful"
			if reason == "force" {
				want = "force"
			}
			if payload.Mode != want || !strings.HasSuffix(key, ":"+reason) {
				t.Fatalf("stop reason is not a Runtime mode: mode=%q reason=%q key=%q", payload.Mode, reason, key)
			}
			events, err := f.repository.ListRunEvents(t.Context(), f.user.ID, run.ID)
			if err != nil || len(events) != 2 || events[1].Type != "run.cancelling" || events[1].Payload["mode"] != reason {
				t.Fatal("stop normalization lost the original audit reason")
			}
		})
	}
}

func TestForceStopModeSurvivesOutboxAndEscalatesExistingCancellation(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	run := f.createRun(t, "force-mode")
	for _, mode := range []string{"graceful", "force", "force"} {
		if _, err := f.repository.StopRun(t.Context(), f.user.ID, run.ID, mode); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.repository.db.QueryContext(t.Context(), `SELECT payload FROM command_outbox WHERE command_name='run.stop' AND json_extract(payload,'$.run_id')=? ORDER BY created_at,id`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	modes := map[string]int{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p nodeprotocol.StopRunPayload
		if json.Unmarshal(raw, &p) != nil {
			t.Fatal("invalid stop payload")
		}
		modes[p.Mode]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(modes) != 2 || modes["graceful"] != 1 || modes["force"] != 1 {
		t.Fatalf("force escalation was dropped, duplicated or lost its Runtime mode: %+v", modes)
	}
}

func TestForceStopPreparationFailureCannotBeatAcceptedCancellation(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	run := f.createRun(t, "force-preparation")
	claim, ok, err := f.repository.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
	if err != nil || !ok || claim.Name != "run.start" {
		t.Fatalf("start claim ok=%v err=%v", ok, err)
	}
	if _, err := f.repository.StopRun(t.Context(), f.user.ID, run.ID, "force"); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.FailLocalCommand(t.Context(), claim.ID, claim.ClaimToken, "provider_error", json.RawMessage(`{"error":"context canceled during provider preparation"}`)); err != nil {
		t.Fatal(err)
	}
	current, err := f.repository.LocalRun(t.Context(), run.ID)
	if err != nil || current.Status != domain.RunCancelled {
		t.Fatalf("preparation failure beat force cancellation: status=%s err=%v", current.Status, err)
	}
	events, err := f.repository.ListRunEvents(t.Context(), f.user.ID, run.ID)
	if err != nil || events[len(events)-1].Payload["status"] != domain.RunCancelled {
		t.Fatalf("terminal did not reflect accepted cancellation: %+v err=%v", events, err)
	}
}

func TestStopControlReceiptDistinguishesRequestAckFromRunSettlement(t *testing.T) {
	for _, test := range []struct {
		name, result, wantRun string
		invalid               bool
	}{
		{"pending", `{"interrupted":true,"terminal_pending":true}`, domain.RunCancelling, false},
		{"settled-without-runtime-owner", `{"interrupted":true}`, domain.RunCancelled, false},
		{"invalid-pending-type", `{"interrupted":true,"terminal_pending":"yes"}`, domain.RunCancelling, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			run := f.createRun(t, "stop-receipt")
			start, ok, err := f.repository.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
			if err != nil || !ok {
				t.Fatal("start claim failed", err)
			}
			if _, err := f.repository.StopRun(t.Context(), f.user.ID, run.ID, "graceful"); err != nil {
				t.Fatal(err)
			}
			stop, ok, err := f.repository.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute, start.ID)
			if err != nil || !ok || stop.Name != "run.stop" {
				t.Fatal("stop claim failed", err)
			}
			err = f.repository.CompleteLocalCommand(t.Context(), stop.ID, stop.ClaimToken, json.RawMessage(test.result))
			if (err != nil) != test.invalid {
				t.Fatalf("receipt validation err=%v invalid=%v", err, test.invalid)
			}
			current, err := f.repository.LocalRun(t.Context(), run.ID)
			if err != nil || current.Status != test.wantRun {
				t.Fatalf("receipt incorrectly settled Run: status=%s err=%v", current.Status, err)
			}
			command, err := f.repository.GetLocalCommand(t.Context(), stop.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.invalid {
				if command.Status != LocalCommandProcessing || command.ClaimToken != stop.ClaimToken || command.CompletedAt != nil {
					t.Fatal("invalid receipt was partially committed")
				}
			} else if command.Status != LocalCommandCompleted || command.ClaimToken != "" || command.CompletedAt == nil {
				t.Fatal("request acknowledgement was not durably fenced")
			}
			events, err := f.repository.ListRunEvents(t.Context(), f.user.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			terminals := 0
			for index, event := range events {
				if event.Sequence != int64(index+1) {
					t.Fatal("receipt introduced an event sequence gap")
				}
				if event.Type == "run.finished" {
					terminals++
				}
			}
			wantTerminals := 0
			if test.wantRun == domain.RunCancelled {
				wantTerminals = 1
			}
			if terminals != wantTerminals {
				t.Fatalf("receipt fabricated or lost terminal: got=%d want=%d", terminals, wantTerminals)
			}
		})
	}
}
