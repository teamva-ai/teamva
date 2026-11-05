package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestEventOutboxBackpressureReservesPrioritySlotAndRollsBackSequence(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.outboxLimits = eventOutboxLimits{
		softCount: 2, softBytes: 1 << 20,
		hardCount: 3, hardBytes: 2 << 20,
	}
	for index := 0; index < 2; index++ {
		event, err := store.AppendEvent(t.Context(), "run-backpressure", 1, adapter.Event{
			Type: "output.delta", Payload: map[string]any{"text": fmt.Sprintf("chunk-%d", index)},
		})
		if err != nil || event.Sequence != int64(index+1) {
			t.Fatalf("append normal event %d: event=%+v err=%v", index, event, err)
		}
	}
	if _, err := store.AppendEvent(t.Context(), "run-backpressure", 1, adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "must-wait"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("normal event above the soft limit error=%v", err)
	}
	terminal, err := store.AppendEvent(t.Context(), "run-backpressure", 1, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed", "reason": "outbox_full"},
	})
	if err != nil || terminal.Sequence != 3 {
		t.Fatalf("reserved terminal append=%+v err=%v", terminal, err)
	}
	if _, err := store.AppendEvent(t.Context(), "run-backpressure", 1, adapter.Event{
		Type: "node.diagnostic", Payload: map[string]any{"code": "after_hard_limit"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("priority event above the hard limit error=%v", err)
	}
	assertEventOutboxUsage(t, store, 3)
	assertEventSequenceCursor(t, store, "run-backpressure", 1, 3)

	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: "run-backpressure", LeaseEpoch: 1, Sequence: 3,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventOutboxUsage(t, store, 0)
	var cursors int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors WHERE run_id = 'run-backpressure'
	`).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("terminal ACK cursor count=%d err=%v", cursors, err)
	}
}

func TestEventOutboxReservesOnePriorityTerminalForEachOf128ManagedRuns(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AppendEvent(t.Context(), "normal-at-soft-edge", 1, adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "normal"},
	}); err != nil {
		t.Fatal(err)
	}
	normalUsage := assertEventOutboxUsage(t, store, 1)
	store.outboxLimits = eventOutboxLimits{
		softCount: 1, softBytes: normalUsage.bytes,
		hardCount: 1 + reservedPriorityEventCount,
		hardBytes: normalUsage.bytes + reservedPriorityEventBytes,
	}
	if _, err := store.AppendEvent(t.Context(), "normal-cannot-use-reserve", 1, adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "reserved"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("normal event consumed priority reserve: %v", err)
	}
	cursors := make([]EventCursor, 0, reservedPriorityEventCount)
	for index := int64(0); index < reservedPriorityEventCount; index++ {
		runID := fmt.Sprintf("priority-run-%03d", index)
		tx, err := store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ensureEventOutboxCapacity(t.Context(), tx, runID, 1,
			int64(nodeprotocol.MaxRunEventBytes), true); err != nil {
			tx.Rollback()
			t.Fatalf("maximum priority terminal %d did not fit: %v", index, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		event, err := store.AppendEvent(t.Context(), runID, 1, adapter.Event{
			Type: "run.finished", Payload: map[string]any{"status": "failed", "index": index},
		})
		if err != nil || event.Sequence != 1 {
			t.Fatalf("priority terminal %d: event=%+v err=%v", index, event, err)
		}
		if _, err := store.db.ExecContext(t.Context(), `
			UPDATE event_log SET wire_bytes = ? WHERE id = ?
		`, nodeprotocol.MaxRunEventBytes, event.ID); err != nil {
			t.Fatal(err)
		}
		cursors = append(cursors, EventCursor{RunID: runID, LeaseEpoch: 1, Sequence: 1})
	}
	rejectedRunID := fmt.Sprintf("priority-run-%03d", reservedPriorityEventCount)
	if _, err := store.AppendEvent(t.Context(), rejectedRunID, 1, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("129th priority terminal error=%v", err)
	}
	fullUsage := assertEventOutboxUsage(t, store, 1+reservedPriorityEventCount)
	if fullUsage.bytes != store.outboxLimits.hardBytes {
		t.Fatalf("128 maximum terminal reservations used bytes=%d want hard=%d",
			fullUsage.bytes, store.outboxLimits.hardBytes)
	}
	var rejectedCursor int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors WHERE run_id = ?
	`, rejectedRunID).Scan(&rejectedCursor); err != nil || rejectedCursor != 0 {
		t.Fatalf("rejected priority event advanced cursor: rows=%d err=%v", rejectedCursor, err)
	}
	if err := store.AckEventsThrough(t.Context(), cursors); err != nil {
		t.Fatal(err)
	}
	remaining := assertEventOutboxUsage(t, store, 1)
	if remaining != normalUsage {
		t.Fatalf("terminal ACK trigger usage=%+v want normal=%+v", remaining, normalUsage)
	}
	var terminalCursors int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors WHERE run_id LIKE 'priority-run-%'
	`).Scan(&terminalCursors); err != nil || terminalCursors != 0 {
		t.Fatalf("terminal ACK cleanup left cursors=%d err=%v", terminalCursors, err)
	}
}

func TestEventOutboxTerminalReserveIsOneSlotPerRunLease(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.outboxLimits = eventOutboxLimits{
		softCount: 1, softBytes: 1 << 20,
		hardCount: 3, hardBytes: 3 << 20,
	}
	if _, err := store.AppendEvent(t.Context(), "normal", 1, adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "soft-full"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(t.Context(), "run-a", 1, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed"},
	}); err != nil {
		t.Fatalf("first Run terminal did not use reserve: %v", err)
	}
	if _, err := store.AppendEvent(t.Context(), "run-a", 1, adapter.Event{
		Type: "run.completed", Payload: map[string]any{"status": "completed"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("same Run consumed a second terminal reserve: %v", err)
	}
	if _, err := store.AppendEvent(t.Context(), "run-a", 2, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed"},
	}); err != nil {
		t.Fatalf("second lease could not use its own terminal reserve: %v", err)
	}
	assertEventOutboxUsage(t, store, 3)
}

func TestEventOutboxByteLimitsReserveTerminalCapacity(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	normal := adapter.Event{
		ID: "byte-normal-1", RunID: "run-byte-limit", LeaseEpoch: 1, Sequence: 1,
		Type: "provider.event", Payload: map[string]any{"text": "normal"}, OccurredAt: now,
	}
	terminal := adapter.Event{
		ID: "byte-terminal-2", RunID: "run-byte-limit", LeaseEpoch: 1, Sequence: 2,
		Type: "run.completed", Payload: map[string]any{"status": "completed"}, OccurredAt: now,
	}
	normalBytes := encodedEventBytes(t, normal)
	terminalBytes := encodedEventBytes(t, terminal)
	store.outboxLimits = eventOutboxLimits{
		softCount: 9, softBytes: normalBytes,
		hardCount: 10, hardBytes: normalBytes + terminalBytes,
	}
	if _, err := store.AppendEvent(t.Context(), normal.RunID, normal.LeaseEpoch, normal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(t.Context(), normal.RunID, normal.LeaseEpoch, adapter.Event{
		ID: "byte-normal-rejected", Sequence: 2, Type: "provider.event",
		Payload: map[string]any{"text": "x"}, OccurredAt: now,
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("normal byte overflow error=%v", err)
	}
	if _, err := store.AppendEvent(t.Context(), terminal.RunID, terminal.LeaseEpoch, terminal); err != nil {
		t.Fatalf("terminal did not fit reserved byte budget: %v", err)
	}
	if _, err := store.AppendEvent(t.Context(), terminal.RunID, terminal.LeaseEpoch, adapter.Event{
		ID: "byte-diagnostic-3", Sequence: 3, Type: "node.diagnostic",
		Payload: map[string]any{"code": "full"}, OccurredAt: now,
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("priority byte overflow error=%v", err)
	}
	usage := assertEventOutboxUsage(t, store, 2)
	if usage.bytes != normalBytes+terminalBytes {
		t.Fatalf("outbox bytes=%d want=%d", usage.bytes, normalBytes+terminalBytes)
	}
	defaults := defaultEventOutboxLimits()
	if defaults.softCount != maxUnackedEventCount || defaults.softBytes != maxUnackedEventBytes ||
		defaults.hardCount-defaults.softCount != reservedPriorityEventCount ||
		defaults.hardBytes-defaults.softBytes != reservedPriorityEventBytes ||
		reservedPriorityEventCount != 128 ||
		reservedPriorityEventBytes != 128*int64(nodeprotocol.MaxRunEventBytes) {
		t.Fatalf("default outbox does not reserve 128 maximum terminals: %+v", defaults)
	}
}

func TestEventOutboxMetadataTracksReplacementACKAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEvent(t.Context(), "run-accounting", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"text": string(make([]byte, 32<<10))},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := assertEventOutboxUsage(t, store, 1)
	replacement, err := store.ReplaceEventWithDiagnostic(t.Context(), event, "server_event_constraint")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Type != "node.diagnostic" {
		t.Fatalf("replacement=%+v", replacement)
	}
	after := assertEventOutboxUsage(t, store, 1)
	if after.bytes >= before.bytes {
		t.Fatalf("replacement did not reduce accounting: before=%d after=%d", before.bytes, after.bytes)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted := assertEventOutboxUsage(t, store, 1)
	if restarted != after {
		t.Fatalf("restarted usage=%+v want=%+v", restarted, after)
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: replacement.RunID, LeaseEpoch: replacement.LeaseEpoch, Sequence: replacement.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventOutboxUsage(t, store, 0)
}

func TestEventOutboxTriggersTrackACKWireTransitionsAndDelete(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event, err := store.AppendEvent(t.Context(), "run-trigger-transitions", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := assertEventOutboxUsage(t, store, 1)
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET acked_at = ?, wire_bytes = ? WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), initial.bytes+17, event.ID); err != nil {
		t.Fatal(err)
	}
	assertEventOutboxUsage(t, store, 0)
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET wire_bytes = ? WHERE id = ?
	`, initial.bytes+31, event.ID); err != nil {
		t.Fatal(err)
	}
	assertEventOutboxUsage(t, store, 0)
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET acked_at = NULL, wire_bytes = ? WHERE id = ?
	`, initial.bytes+47, event.ID); err != nil {
		t.Fatal(err)
	}
	restored := assertEventOutboxUsage(t, store, 1)
	if restored.bytes != initial.bytes+47 {
		t.Fatalf("restored trigger bytes=%d want=%d", restored.bytes, initial.bytes+47)
	}
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM event_log WHERE id = ?`, event.ID); err != nil {
		t.Fatal(err)
	}
	assertEventOutboxUsage(t, store, 0)
}

func TestEventOutboxMissingMetadataIsRejectedWithoutBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	dropEventOutboxMetadata(t, raw)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES
			('legacy-outbox-1', 'run-legacy-outbox', 1, 1, 'provider.event', '{}', 301, ?),
			('legacy-outbox-2', 'run-legacy-outbox', 1, 2, 'run.completed', '{}', 302, ?)
	`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(t.Context(), path)
	if err == nil {
		store.Close()
		t.Fatal("missing metadata was recreated")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var rows, metadata int
	if err := raw.QueryRow(`SELECT count(*) FROM event_log`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='event_outbox_state'`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || metadata != 0 {
		t.Fatalf("rejected database changed: rows=%d metadata=%d", rows, metadata)
	}
}

func TestEventOutboxMalformedMigrationRollsBackWithoutClearing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	dropEventOutboxMetadata(t, raw)
	if _, err := raw.ExecContext(t.Context(), `
		CREATE TABLE event_outbox_state (sentinel TEXT NOT NULL);
		INSERT INTO event_outbox_state (sentinel) VALUES ('preserve-me');
	`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("malformed outbox metadata schema was accepted")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var sentinel string
	if err := raw.QueryRowContext(t.Context(), `SELECT sentinel FROM event_outbox_state`).Scan(&sentinel); err != nil || sentinel != "preserve-me" {
		t.Fatalf("failed migration changed metadata: sentinel=%q err=%v", sentinel, err)
	}
}

func TestEventOutboxMetadataMismatchRefusesStartupWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(t.Context(), "run-metadata-mismatch", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"ok": true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		UPDATE event_outbox_state SET unacked_bytes = unacked_bytes + 1 WHERE singleton = 1
	`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	var corruptedBytes int64
	if err := raw.QueryRowContext(t.Context(), `
		SELECT unacked_bytes FROM event_outbox_state WHERE singleton = 1
	`).Scan(&corruptedBytes); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("mismatched event outbox metadata was silently repaired")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var retainedBytes int64
	var events int
	if err := raw.QueryRowContext(t.Context(), `
		SELECT unacked_bytes FROM event_outbox_state WHERE singleton = 1
	`).Scan(&retainedBytes); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_log WHERE run_id = 'run-metadata-mismatch'
	`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if retainedBytes != corruptedBytes || events != 1 {
		t.Fatalf("failed startup repaired/deleted state: bytes=%d want=%d events=%d",
			retainedBytes, corruptedBytes, events)
	}
}

func TestCurrentOverLimitOutboxStartsAndDrains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	legacyRows := defaultEventOutboxLimits().hardBytes/int64(nodeprotocol.MaxRunEventBytes) + 1
	if _, err := raw.ExecContext(t.Context(), `
		WITH RECURSIVE numbers(value) AS (
			VALUES(1) UNION ALL SELECT value + 1 FROM numbers WHERE value < ?
		)
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		)
		SELECT printf('legacy-overlimit-%02d', value), printf('run-overlimit-%02d', value),
		       1, 1, 'provider.event', '{}', ?, ?
		FROM numbers
	`, legacyRows, nodeprotocol.MaxRunEventBytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	// This fixture keeps the current schema and its transactional metadata;
	// it tests capacity reduction/draining, not reconstruction of an old DB.
	if _, err := raw.Exec(`INSERT INTO event_sequence_cursors SELECT run_id, lease_epoch, MAX(sequence) FROM event_log GROUP BY run_id, lease_epoch`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open over-limit legacy outbox: %v", err)
	}
	defer store.Close()
	usage := assertEventOutboxUsage(t, store, legacyRows)
	if usage.bytes <= store.outboxLimits.hardBytes {
		t.Fatalf("legacy fixture is not over hard limit: %+v", usage)
	}
	if _, err := store.AppendEvent(t.Context(), "run-new-while-overlimit", 1, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed"},
	}); !errors.Is(err, ErrEventOutboxFull) {
		t.Fatalf("over-limit append error=%v", err)
	}
	for index := int64(0); index < legacyRows; index++ {
		events, err := store.UnackedEvents(t.Context(), 1)
		if err != nil || len(events) != 1 {
			t.Fatalf("drain step %d events=%+v err=%v", index, events, err)
		}
		if err := store.AckEventsThrough(t.Context(), []EventCursor{{
			RunID: events[0].RunID, LeaseEpoch: events[0].LeaseEpoch, Sequence: events[0].Sequence,
		}}); err != nil {
			t.Fatalf("drain ACK %d: %v", index, err)
		}
	}
	assertEventOutboxUsage(t, store, 0)
}

func TestUnackedEventsUsesFairGlobalAppendOrderAndRunPrefixes(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, runID := range []string{"run-a", "run-b", "run-a", "run-c", "run-b"} {
		if _, err := store.AppendEvent(t.Context(), runID, 1, adapter.Event{
			Type: "provider.event", Payload: map[string]any{"run": runID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.UnackedEvents(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	wantRuns := []string{"run-a", "run-b", "run-a", "run-c", "run-b"}
	wantSequences := []int64{1, 1, 2, 1, 2}
	if len(events) != len(wantRuns) {
		t.Fatalf("events=%+v", events)
	}
	for index := range events {
		if events[index].RunID != wantRuns[index] || events[index].Sequence != wantSequences[index] {
			t.Fatalf("global order at %d=%s/%d want=%s/%d", index,
				events[index].RunID, events[index].Sequence, wantRuns[index], wantSequences[index])
		}
	}
}

func TestPoisonConversionPreservesTerminalProgressForReadAndReplacement(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tests := []struct {
		originalType string
		wantType     string
		wantStatus   string
	}{
		{originalType: "run.completed", wantType: "run.finished", wantStatus: "failed"},
		{originalType: "run.finished", wantType: "run.finished", wantStatus: "failed"},
		{originalType: "run.error", wantType: "run.finished", wantStatus: "failed"},
		{originalType: "provider.event", wantType: "node.diagnostic"},
	}
	for index, test := range tests {
		runID := fmt.Sprintf("run-replace-poison-%d", index)
		event, err := store.AppendEvent(t.Context(), runID, 1, adapter.Event{
			Type: test.originalType, Payload: map[string]any{"untrusted": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		replacement, err := store.ReplaceEventWithDiagnostic(t.Context(), event, "server_event_constraint")
		if err != nil {
			t.Fatal(err)
		}
		assertPoisonOutcome(t, replacement, test.originalType, test.wantType, test.wantStatus)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, test := range tests[:3] {
		if _, err := store.db.ExecContext(t.Context(), `
			INSERT INTO event_log (
				id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
			) VALUES (?, ?, 1, 1, ?, X'ff', 256, ?)
		`, fmt.Sprintf("read-poison-%d", index), fmt.Sprintf("run-read-poison-%d", index),
			test.originalType, now); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.UnackedEvents(t.Context(), nodeprotocol.MaxRunEventsPerBatch)
	if err != nil {
		t.Fatal(err)
	}
	readPoison := make(map[string]adapter.Event)
	for _, event := range events {
		if len(event.RunID) >= len("run-read-poison-") && event.RunID[:len("run-read-poison-")] == "run-read-poison-" {
			readPoison[event.RunID] = event
		}
	}
	for index, test := range tests[:3] {
		event, ok := readPoison[fmt.Sprintf("run-read-poison-%d", index)]
		if !ok {
			t.Fatalf("missing dynamic poison event %d in %+v", index, events)
		}
		assertPoisonOutcome(t, event, test.originalType, test.wantType, test.wantStatus)
	}
}

func TestInvalidOccurredAtPoisonIsDeterministicAcrossReadACKRetryAndReplace(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES ('invalid-time-read', 'run-invalid-time-read', 1, 1,
		          'provider.event', '{"ok":true}', 256, 'not-a-time')
	`); err != nil {
		t.Fatal(err)
	}
	first, err := store.UnackedEvents(t.Context(), 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first poison read=%+v err=%v", first, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.AckEventsThrough(canceled, []EventCursor{{
		RunID: first[0].RunID, LeaseEpoch: first[0].LeaseEpoch, Sequence: first[0].Sequence,
	}}); err == nil {
		t.Fatal("canceled ambiguous ACK unexpectedly committed")
	}
	second, err := store.UnackedEvents(t.Context(), 1)
	if err != nil || len(second) != 1 || !reflect.DeepEqual(first[0], second[0]) {
		t.Fatalf("poison changed across ACK retry: first=%+v second=%+v err=%v", first, second, err)
	}
	if !first[0].OccurredAt.Equal(deterministicPoisonOccurredAt()) {
		t.Fatalf("poison safe time=%s want=%s", first[0].OccurredAt, deterministicPoisonOccurredAt())
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: first[0].RunID, LeaseEpoch: first[0].LeaseEpoch, Sequence: first[0].Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES ('invalid-time-replace', 'run-invalid-time-replace', 1, 1,
		          'provider.event', '{"ok":true}', 256, 'still-not-a-time')
	`); err != nil {
		t.Fatal(err)
	}
	cursor := adapter.Event{RunID: "run-invalid-time-replace", LeaseEpoch: 1, Sequence: 1}
	replaced, err := store.ReplaceEventWithDiagnostic(t.Context(), cursor, "server_event_constraint")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.ReplaceEventWithDiagnostic(t.Context(), cursor, "server_event_constraint")
	if err != nil || !reflect.DeepEqual(replaced, retried) {
		t.Fatalf("replacement changed across retry: first=%+v second=%+v err=%v", replaced, retried, err)
	}
	pending, err := store.UnackedEvents(t.Context(), 1)
	if err != nil || len(pending) != 1 || !reflect.DeepEqual(replaced, pending[0]) {
		t.Fatalf("persisted deterministic replacement=%+v want=%+v err=%v", pending, replaced, err)
	}
}

func assertPoisonOutcome(t *testing.T, event adapter.Event, originalType, wantType, wantStatus string) {
	t.Helper()
	if event.Type != wantType || event.Payload["code"] != "durable_event_poisoned" ||
		event.Payload["reason"] != "durable_event_poisoned" ||
		event.Payload["original_event_type"] != originalType {
		t.Fatalf("poison outcome=%+v", event)
	}
	if wantStatus != "" && event.Payload["status"] != wantStatus {
		t.Fatalf("poison status=%v want=%s event=%+v", event.Payload["status"], wantStatus, event)
	}
	encodedEventBytes(t, event)
}

func encodedEventBytes(t *testing.T, event adapter.Event) int64 {
	t.Helper()
	encoded, _, err := nodeprotocol.EncodeRunEvent(protocolEvent(event))
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(encoded))
}

func assertEventOutboxUsage(t *testing.T, store *Store, wantCount int64) eventOutboxUsage {
	t.Helper()
	var usage eventOutboxUsage
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT unacked_count, unacked_bytes FROM event_outbox_state WHERE singleton = 1
	`).Scan(&usage.count, &usage.bytes); err != nil {
		t.Fatal(err)
	}
	if usage.count != wantCount || usage.bytes < 0 {
		t.Fatalf("outbox usage=%+v want count=%d", usage, wantCount)
	}
	var actual eventOutboxUsage
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*), COALESCE(SUM(wire_bytes), 0)
		FROM event_log WHERE acked_at IS NULL
	`).Scan(&actual.count, &actual.bytes); err != nil {
		t.Fatal(err)
	}
	if usage != actual {
		t.Fatalf("outbox metadata=%+v actual=%+v", usage, actual)
	}
	return usage
}

func dropEventOutboxMetadata(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `
		DROP TRIGGER IF EXISTS event_outbox_after_insert;
		DROP TRIGGER IF EXISTS event_outbox_after_delete;
		DROP TRIGGER IF EXISTS event_outbox_after_update;
		DROP TABLE IF EXISTS event_outbox_state;
	`); err != nil {
		t.Fatal(err)
	}
}
