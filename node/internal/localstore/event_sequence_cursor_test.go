package localstore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestEventSequenceContinuesAfterACKDeletesAllEvents(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()

	first := appendEventForSequenceTest(t, store, "run-ack", 1, adapter.Event{Type: "run.started"})
	if first.Sequence != 1 {
		t.Fatalf("first sequence=%d, want 1", first.Sequence)
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: "run-ack", LeaseEpoch: 1, Sequence: first.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventLogRows(t, store, "run-ack", 1, 0)

	second := appendEventForSequenceTest(t, store, "run-ack", 1, adapter.Event{Type: "run.completed"})
	if second.Sequence != 2 {
		t.Fatalf("sequence after deleting ACKed outbox=%d, want 2", second.Sequence)
	}
	assertEventSequenceCursor(t, store, "run-ack", 1, 2)
}

func TestEventSequenceSurvivesNonTerminalACKAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openEventSequenceStore(t, path)
	first := appendEventForSequenceTest(t, store, "run-restart", 3, adapter.Event{Type: "provider.event"})
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: first.RunID, LeaseEpoch: first.LeaseEpoch, Sequence: first.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventLogRows(t, store, "run-restart", 3, 0)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openEventSequenceStore(t, path)
	defer store.Close()
	second := appendEventForSequenceTest(t, store, "run-restart", 3, adapter.Event{Type: "provider.event"})
	if second.Sequence != 2 {
		t.Fatalf("sequence after non-terminal ACK and restart=%d, want 2", second.Sequence)
	}
}

func TestEventSequenceCursorSurvivesLegacyACKRetention(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()

	first := appendEventForSequenceTest(t, store, "run-retention", 1, adapter.Event{Type: "run.started"})
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET acked_at = ? WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneDurableState(t.Context(), time.Now().UTC(), defaultDurableRetentionPolicy()); err != nil {
		t.Fatal(err)
	}
	assertEventLogRows(t, store, "run-retention", 1, 0)
	assertEventSequenceCursor(t, store, "run-retention", 1, 1)

	second := appendEventForSequenceTest(t, store, "run-retention", 1, adapter.Event{Type: "run.completed"})
	if second.Sequence != 2 {
		t.Fatalf("sequence after legacy ACK retention=%d, want 2", second.Sequence)
	}
}

func TestLegacyACKedTerminalRetentionCleansSequenceCursor(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	terminal := appendEventForSequenceTest(t, store, "run-legacy-terminal", 1, adapter.Event{
		Type: "run.completed", Payload: map[string]any{"status": "completed"},
	})
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET acked_at = ? WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), terminal.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneDurableState(t.Context(), time.Now().UTC(), defaultDurableRetentionPolicy()); err != nil {
		t.Fatal(err)
	}
	assertEventLogRows(t, store, "run-legacy-terminal", 1, 0)
	var cursors int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors
		WHERE run_id = 'run-legacy-terminal' AND lease_epoch = 1
	`).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("legacy terminal cursor count=%d err=%v", cursors, err)
	}
}

func TestEventSequenceMissingCursorIsRejectedWithoutBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openEventSequenceStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(t.Context(), `
		DROP TABLE event_sequence_cursors;
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES
			('legacy-1', 'run-legacy', 7, 1, 'provider.event', '{}', 256, ?),
			('legacy-2', 'run-legacy', 7, 2, 'run.completed', '{}', 256, ?)
	`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("missing cursor was backfilled")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var rows, cursors int
	if err := raw.QueryRow(`SELECT count(*) FROM event_log`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='event_sequence_cursors'`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || cursors != 0 {
		t.Fatalf("rejected database changed: rows=%d cursors=%d", rows, cursors)
	}
}

func TestEventSequenceCursorsAreIsolatedByRunAndLease(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()

	keys := []struct {
		runID      string
		leaseEpoch int64
	}{
		{runID: "run-a", leaseEpoch: 1},
		{runID: "run-a", leaseEpoch: 2},
		{runID: "run-b", leaseEpoch: 1},
	}
	for _, key := range keys {
		event := appendEventForSequenceTest(t, store, key.runID, key.leaseEpoch, adapter.Event{Type: "run.started"})
		if event.Sequence != 1 {
			t.Fatalf("first sequence for (%s,%d)=%d, want 1", key.runID, key.leaseEpoch, event.Sequence)
		}
	}
	for _, key := range keys {
		event := appendEventForSequenceTest(t, store, key.runID, key.leaseEpoch, adapter.Event{Type: "provider.event"})
		if event.Sequence != 2 {
			t.Fatalf("second sequence for (%s,%d)=%d, want 2", key.runID, key.leaseEpoch, event.Sequence)
		}
	}
}

func TestExplicitEventSequenceAdvancesAndValidatesCursorAtomically(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()

	if event := appendEventForSequenceTest(t, store, "run-explicit", 1, adapter.Event{Type: "run.started"}); event.Sequence != 1 {
		t.Fatalf("first sequence=%d, want 1", event.Sequence)
	}
	if event := appendEventForSequenceTest(t, store, "run-explicit", 1, adapter.Event{
		Sequence: 2, Type: "provider.event",
	}); event.Sequence != 2 {
		t.Fatalf("explicit sequence=%d, want 2", event.Sequence)
	}
	if _, err := store.AppendEvent(t.Context(), "run-explicit", 1, adapter.Event{
		Sequence: 5, Type: "provider.event",
	}); err == nil {
		t.Fatal("jumping explicit sequence was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), "run-explicit", 1, adapter.Event{
		Sequence: 1, Type: "provider.event",
	}); err == nil {
		t.Fatal("stale explicit sequence was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), "run-explicit", 1, adapter.Event{
		Sequence: -1, Type: "provider.event",
	}); err == nil {
		t.Fatal("negative explicit sequence was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), "run-explicit-first", 1, adapter.Event{
		Sequence: 2, Type: "provider.event",
	}); err == nil {
		t.Fatal("first explicit sequence above 1 was accepted")
	}
	if event := appendEventForSequenceTest(t, store, "run-explicit-first", 1, adapter.Event{
		Sequence: 1, Type: "run.started",
	}); event.Sequence != 1 {
		t.Fatalf("first explicit sequence=%d, want 1", event.Sequence)
	}
	if event := appendEventForSequenceTest(t, store, "run-explicit", 1, adapter.Event{Type: "provider.event"}); event.Sequence != 3 {
		t.Fatalf("sequence after rejected explicit jump=%d, want 3", event.Sequence)
	}

	appendEventForSequenceTest(t, store, "run-other", 1, adapter.Event{
		ID: "duplicate-event-id", Type: "provider.event",
	})
	if _, err := store.AppendEvent(t.Context(), "run-explicit", 1, adapter.Event{
		ID: "duplicate-event-id", Sequence: 4, Type: "provider.event",
	}); err == nil {
		t.Fatal("duplicate event ID was accepted")
	}
	next := appendEventForSequenceTest(t, store, "run-explicit", 1, adapter.Event{Type: "provider.event"})
	if next.Sequence != 4 {
		t.Fatalf("failed insert advanced cursor: next sequence=%d, want 4", next.Sequence)
	}
	assertEventSequenceCursor(t, store, "run-explicit", 1, 4)
}

func TestTerminalACKCleansCursorButNonTerminalACKDoesNot(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()

	nonTerminal := appendEventForSequenceTest(t, store, "run-live", 1, adapter.Event{Type: "run.started"})
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: nonTerminal.RunID, LeaseEpoch: nonTerminal.LeaseEpoch, Sequence: nonTerminal.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventSequenceCursor(t, store, "run-live", 1, 1)

	terminal := appendEventForSequenceTest(t, store, "run-done", 1, adapter.Event{
		Type: "run.completed", Payload: map[string]any{"status": "completed"},
	})
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: terminal.RunID, LeaseEpoch: terminal.LeaseEpoch, Sequence: terminal.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	var cursors int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors
		WHERE run_id = 'run-done' AND lease_epoch = 1
	`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if cursors != 0 {
		t.Fatalf("terminal ACK retained %d sequence cursors", cursors)
	}
}

func TestRunErrorACKKeepsCursorUntilNormalizedFinalTerminal(t *testing.T) {
	store := openEventSequenceStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	errorEvent := appendEventForSequenceTest(t, store, "run-error-then-finished", 1, adapter.Event{
		Type: "run.error", Payload: map[string]any{"reason": "provider_failed"},
	})
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: errorEvent.RunID, LeaseEpoch: errorEvent.LeaseEpoch, Sequence: errorEvent.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	assertEventSequenceCursor(t, store, errorEvent.RunID, errorEvent.LeaseEpoch, 1)
	finished := appendEventForSequenceTest(t, store, errorEvent.RunID, errorEvent.LeaseEpoch, adapter.Event{
		Type: "run.finished", Payload: map[string]any{"status": "failed"},
	})
	if finished.Sequence != 2 {
		t.Fatalf("normalized terminal sequence=%d, want 2", finished.Sequence)
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: finished.RunID, LeaseEpoch: finished.LeaseEpoch, Sequence: finished.Sequence,
	}}); err != nil {
		t.Fatal(err)
	}
	var cursors int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_sequence_cursors
		WHERE run_id = 'run-error-then-finished' AND lease_epoch = 1
	`).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("normalized terminal cursor count=%d err=%v", cursors, err)
	}
}

func TestEventSequenceMigrationFailurePreservesLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openEventSequenceStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		DROP TABLE event_sequence_cursors;
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES ('invalid-sequence', 'run-invalid', 1, 0, 'provider.event', '{}', 256, ?)
	`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("invalid legacy event sequence did not refuse startup")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var eventRows, cursorTables int
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_log WHERE id = 'invalid-sequence' AND sequence = 0
	`).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'event_sequence_cursors'
	`).Scan(&cursorTables); err != nil {
		t.Fatal(err)
	}
	if eventRows != 1 || cursorTables != 0 {
		t.Fatalf("failed migration mutated legacy database: events=%d cursor_tables=%d", eventRows, cursorTables)
	}
}

func TestEventSequenceMigrationRejectsExistingHoleAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openEventSequenceStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(t.Context(), `
		DROP TABLE event_sequence_cursors;
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES
			('gap-1', 'run-gap', 1, 1, 'provider.event', '{}', 256, ?),
			('gap-3', 'run-gap', 1, 3, 'run.completed', '{}', 256, ?)
	`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if reopened, err := Open(t.Context(), path); err == nil {
		reopened.Close()
		t.Fatal("legacy event sequence hole was accepted")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var events, cursorTables int
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_log WHERE run_id = 'run-gap'
	`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'event_sequence_cursors'
	`).Scan(&cursorTables); err != nil {
		t.Fatal(err)
	}
	if events != 2 || cursorTables != 0 {
		t.Fatalf("failed gap migration mutated database: events=%d cursor_tables=%d", events, cursorTables)
	}
}

func openEventSequenceStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func appendEventForSequenceTest(
	t *testing.T,
	store *Store,
	runID string,
	leaseEpoch int64,
	event adapter.Event,
) adapter.Event {
	t.Helper()
	appended, err := store.AppendEvent(t.Context(), runID, leaseEpoch, event)
	if err != nil {
		t.Fatal(err)
	}
	return appended
}

func assertEventSequenceCursor(t *testing.T, store *Store, runID string, leaseEpoch, want int64) {
	t.Helper()
	var got int64
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT last_sequence FROM event_sequence_cursors
		WHERE run_id = ? AND lease_epoch = ?
	`, runID, leaseEpoch).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor for (%s,%d)=%d, want %d", runID, leaseEpoch, got, want)
	}
}

func assertEventLogRows(t *testing.T, store *Store, runID string, leaseEpoch int64, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM event_log WHERE run_id = ? AND lease_epoch = ?
	`, runID, leaseEpoch).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("event rows for (%s,%d)=%d, want %d", runID, leaseEpoch, got, want)
	}
}
