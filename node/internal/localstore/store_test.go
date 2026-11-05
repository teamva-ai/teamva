package localstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestInboxAndEventLogSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.db")
	ctx := t.Context()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	command := Command{MessageID: "cmd_1", Name: "run.start", Payload: json.RawMessage(`{"run_id":"run_1"}`)}
	_, claimed, err := store.ClaimCommand(ctx, command)
	if err != nil || !claimed {
		t.Fatalf("claim first command: claimed=%v err=%v", claimed, err)
	}
	if err := store.CompleteCommand(ctx, command.MessageID, map[string]any{"accepted": true}); err != nil {
		t.Fatalf("complete command: %v", err)
	}
	record, claimed, err := store.ClaimCommand(ctx, command)
	if err != nil || claimed || record.Status != "completed" {
		t.Fatalf("duplicate must replay: claimed=%v status=%s err=%v", claimed, record.Status, err)
	}
	first, err := store.AppendEvent(ctx, "run_1", 1, adapter.Event{
		Type: "run.started", Payload: map[string]any{"provider": "codex"},
	})
	if err != nil || first.Sequence != 1 {
		t.Fatalf("append first event: %+v err=%v", first, err)
	}
	second, err := store.AppendEvent(ctx, "run_1", 1, adapter.Event{
		Type: "output.delta", Payload: map[string]any{"text": "ok"},
	})
	if err != nil || second.Sequence != 2 {
		t.Fatalf("append second event: %+v err=%v", second, err)
	}
	if err := store.AckEventsThrough(ctx, []EventCursor{{
		RunID: first.RunID, LeaseEpoch: first.LeaseEpoch, Sequence: first.Sequence,
	}}); err != nil {
		t.Fatalf("ack event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	record, err = store.GetCommand(ctx, command.MessageID)
	if err != nil || record.Status != "completed" {
		t.Fatalf("persisted command: status=%s err=%v", record.Status, err)
	}
	events, err := store.UnackedEvents(ctx, 10)
	if err != nil || len(events) != 1 || events[0].ID != second.ID {
		t.Fatalf("persisted unacked events: %+v err=%v", events, err)
	}
}

func TestDurableEventRejectsOversizeAndQuarantinesCorruptPayload(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AppendEvent(t.Context(), "run-large", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"text": strings.Repeat("x", maxDurableEventBytes)},
	}); err == nil {
		t.Fatal("oversized durable event was accepted")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES ('poison-1', 'run-poison', 1, 1, 'provider.event', X'ff', 1, ?)
	`, now); err != nil {
		t.Fatal(err)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("poison events=%+v err=%v", events, err)
	}
	if events[0].Type != "node.diagnostic" || events[0].Payload["code"] != "durable_event_poisoned" ||
		events[0].Payload["original_event_type"] != "provider.event" {
		t.Fatalf("poison event was not converted to a diagnostic: %+v", events[0])
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: "run-poison", LeaseEpoch: 1, Sequence: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_log WHERE run_id = 'run-poison'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("ACK retained %d physical poison rows", rows)
	}
}

func TestSQLitePathReservedCharactersCannotAlterDSN(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime?mode=memory#fragment.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{MessageID: "reserved-path-command", Name: "probe", Payload: json.RawMessage(`{}`)}
	if _, claimed, err := store.ClaimCommand(ctx, command); err != nil || !claimed {
		t.Fatalf("claim command: claimed=%t err=%v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("escaped SQLite path was not created as a file: info=%v err=%v", info, err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if record, err := store.GetCommand(ctx, command.MessageID); err != nil || record.MessageID != command.MessageID {
		t.Fatalf("reserved-path command did not survive reopen: record=%+v err=%v", record, err)
	}
}

func TestOpenRejectsSymlinkDatabasePath(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "runtime.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if store, err := Open(t.Context(), link); err == nil {
		store.Close()
		t.Fatal("symlink database path was accepted")
	}
	payload, err := os.ReadFile(target)
	if err != nil || string(payload) != "sentinel" {
		t.Fatalf("rejected symlink database target was mutated: payload=%q err=%v", payload, err)
	}
}

func TestProcessingCommandBecomesUncertainAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.db")
	ctx := t.Context()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{MessageID: "cmd_uncertain", Name: "run.start", Payload: json.RawMessage(`{}`)}
	if _, claimed, err := store.ClaimCommand(ctx, command); err != nil || !claimed {
		t.Fatalf("claim command: %v", err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, claimed, err := store.ClaimCommand(ctx, command)
	if err != nil || claimed || record.Status != "uncertain" ||
		record.ErrorCode != "node_restarted_during_command" {
		t.Fatalf("unexpected recovered command: %+v claimed=%v err=%v", record, claimed, err)
	}
}

func TestDurableRetentionBoundsTerminalInboxSessionsAndLegacyACKRows(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < 4; index++ {
		id := fmt.Sprintf("command-%d", index)
		if _, claimed, err := store.ClaimCommand(t.Context(), Command{
			MessageID: id, Name: "probe", Payload: json.RawMessage(`{}`),
		}); err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%t err=%v", id, claimed, err)
		}
		if err := store.CompleteCommand(t.Context(), id, map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().UTC().Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		if _, err := store.db.ExecContext(t.Context(), `UPDATE inbox_commands SET finished_at = ? WHERE message_id = ?`, stamp, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "processing-protected", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("claim processing command: claimed=%t err=%v", claimed, err)
	}
	for index := 0; index < 3; index++ {
		if err := store.SaveNativeSession(t.Context(), fmt.Sprintf("run-%d", index), "fake", fmt.Sprintf("native-%d", index)); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveConversationSession(t.Context(), fmt.Sprintf("conversation-%d", index), "fake", fmt.Sprintf("native-%d", index)); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().UTC().Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		if _, err := store.db.ExecContext(t.Context(), `UPDATE native_sessions SET updated_at = ? WHERE run_id = ?`, stamp, fmt.Sprintf("run-%d", index)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(t.Context(), `UPDATE conversation_sessions SET updated_at = ? WHERE conversation_id = ?`, stamp, fmt.Sprintf("conversation-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	event, err := store.AppendEvent(t.Context(), "run-acked", 1, adapter.Event{Type: "run.started", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE event_log SET acked_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), event.ID); err != nil {
		t.Fatal(err)
	}
	policy := durableRetentionPolicy{
		inboxCount: 2, inboxBytes: 1 << 20, inboxAge: 24 * time.Hour,
		sessionCount: 2, sessionBytes: 1 << 20, sessionAge: 24 * time.Hour,
	}
	if err := store.pruneDurableState(t.Context(), time.Now().UTC(), policy); err != nil {
		t.Fatal(err)
	}
	var inbox, processing, nativeSessions, conversations, ackedRows int
	queries := []struct {
		query string
		value *int
	}{
		{`SELECT COUNT(*) FROM inbox_commands`, &inbox},
		{`SELECT COUNT(*) FROM inbox_commands WHERE status = 'processing'`, &processing},
		{`SELECT COUNT(*) FROM native_sessions`, &nativeSessions},
		{`SELECT COUNT(*) FROM conversation_sessions`, &conversations},
		{`SELECT COUNT(*) FROM event_log WHERE acked_at IS NOT NULL`, &ackedRows},
	}
	for _, query := range queries {
		if err := store.db.QueryRowContext(t.Context(), query.query).Scan(query.value); err != nil {
			t.Fatal(err)
		}
	}
	if inbox != 3 || processing != 1 || nativeSessions != 2 || conversations != 2 || ackedRows != 0 {
		t.Fatalf("retention inbox=%d processing=%d native=%d conversations=%d acked=%d",
			inbox, processing, nativeSessions, conversations, ackedRows)
	}
}

func TestInboxCountRetentionUsesFinishedAtAndKeepsJustCompletedReplay(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second)
	for _, item := range []struct {
		id       string
		finished time.Time
	}{
		{id: "finished-oldest", finished: now.Add(-2 * time.Hour)},
		{id: "finished-middle", finished: now.Add(-time.Hour)},
	} {
		completeInboxCommandForRetentionTest(t, store, item.id)
		if _, err := store.db.ExecContext(t.Context(), `
			UPDATE inbox_commands SET finished_at = ? WHERE message_id = ?
		`, item.finished.Format(time.RFC3339Nano), item.id); err != nil {
			t.Fatal(err)
		}
	}
	longRunning := Command{MessageID: "long-running-just-finished", Name: "probe", Payload: json.RawMessage(`{}`)}
	if _, claimed, err := store.ClaimCommand(t.Context(), longRunning); err != nil || !claimed {
		t.Fatalf("claim long-running command: claimed=%t err=%v", claimed, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE inbox_commands SET received_at = ? WHERE message_id = ?
	`, now.Add(-90*24*time.Hour).Format(time.RFC3339Nano), longRunning.MessageID); err != nil {
		t.Fatal(err)
	}
	store.durableMu.Lock()
	store.retention.policy = durableRetentionPolicy{
		inboxCount: 2, inboxBytes: 1 << 20, inboxAge: 365 * 24 * time.Hour,
		sessionCount: maxRetainedSessions, sessionBytes: maxRetainedSessionBytes, sessionAge: sessionRetentionAge,
		writeInterval: 10_000, minInterval: time.Hour,
	}
	store.durableMu.Unlock()
	if err := store.CompleteCommand(t.Context(), longRunning.MessageID, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	var terminalRows, oldestRows int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE status IN ('completed', 'failed')
	`).Scan(&terminalRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE message_id = 'finished-oldest'
	`).Scan(&oldestRows); err != nil {
		t.Fatal(err)
	}
	if terminalRows != 2 || oldestRows != 0 {
		t.Fatalf("finished_at count boundary retained terminals=%d oldest=%d", terminalRows, oldestRows)
	}
	replayed, claimed, err := store.ClaimCommand(t.Context(), longRunning)
	if err != nil || claimed || replayed.Status != "completed" || replayed.FinishedAt == nil ||
		!replayed.FinishedAt.After(replayed.ReceivedAt) {
		t.Fatalf("just-completed command was not replayable: record=%+v claimed=%t err=%v", replayed, claimed, err)
	}
	var indexSQL string
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'inbox_commands_retention'
	`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "finished_at") || strings.Contains(indexSQL, "received_at") {
		t.Fatalf("inbox retention index is not based on finished_at: %s", indexSQL)
	}
}

func TestInboxAgeRetentionUsesFinishedAtWithExclusiveOldBoundary(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-time.Hour)
	for _, item := range []struct {
		id       string
		finished time.Time
	}{
		{id: "age-before-boundary", finished: cutoff.Add(-time.Second)},
		{id: "age-at-boundary", finished: cutoff},
		{id: "age-fresh", finished: now},
	} {
		completeInboxCommandForRetentionTest(t, store, item.id)
		if _, err := store.db.ExecContext(t.Context(), `
			UPDATE inbox_commands SET received_at = ?, finished_at = ? WHERE message_id = ?
		`, now.Add(-90*24*time.Hour).Format(time.RFC3339Nano),
			item.finished.Format(time.RFC3339Nano), item.id); err != nil {
			t.Fatal(err)
		}
	}
	policy := durableRetentionPolicy{
		inboxCount: 10, inboxBytes: 1 << 20, inboxAge: time.Hour,
		sessionCount: maxRetainedSessions, sessionBytes: maxRetainedSessionBytes, sessionAge: sessionRetentionAge,
	}
	if err := store.pruneDurableState(t.Context(), now, policy); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		want int
	}{
		{id: "age-before-boundary", want: 0},
		{id: "age-at-boundary", want: 1},
		{id: "age-fresh", want: 1},
	} {
		var rows int
		if err := store.db.QueryRowContext(t.Context(), `
			SELECT COUNT(*) FROM inbox_commands WHERE message_id = ?
		`, item.id).Scan(&rows); err != nil || rows != item.want {
			t.Fatalf("age boundary %s rows=%d want=%d err=%v", item.id, rows, item.want, err)
		}
	}
	replayed, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "age-at-boundary", Name: "probe", Payload: json.RawMessage(`{}`),
	})
	if err != nil || claimed || replayed.Status != "completed" {
		t.Fatalf("boundary command was reclaimed: record=%+v claimed=%t err=%v", replayed, claimed, err)
	}
}

func completeInboxCommandForRetentionTest(t *testing.T, store *Store, id string) {
	t.Helper()
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: id, Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("claim %s: claimed=%t err=%v", id, claimed, err)
	}
	if err := store.CompleteCommand(t.Context(), id, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete %s: %v", id, err)
	}
}

func TestProductionWritesKeepDurableRetentionBoundedWithoutExplicitPrune(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.durableMu.Lock()
	store.retention.policy = durableRetentionPolicy{
		inboxCount: 16, inboxBytes: 8 << 10, inboxAge: 24 * time.Hour,
		sessionCount: 8, sessionBytes: 8 << 10, sessionAge: 24 * time.Hour,
		writeInterval: 10_000, minInterval: time.Hour,
	}
	store.durableMu.Unlock()
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("bounded-command-%03d", index)
		if _, claimed, err := store.ClaimCommand(t.Context(), Command{
			MessageID: id, Name: "probe", Payload: json.RawMessage(`{"value":"bounded"}`),
		}); err != nil || !claimed {
			t.Fatalf("claim %d: claimed=%t err=%v", index, claimed, err)
		}
		if err := store.CompleteCommand(t.Context(), id, map[string]any{"ok": true}); err != nil {
			t.Fatalf("complete %d: %v", index, err)
		}
		var rows int
		var bytes int64
		if err := store.db.QueryRowContext(t.Context(), `
			SELECT COUNT(*), COALESCE(SUM(length(message_id) + length(name) + length(payload) +
			       COALESCE(length(result), 0) + COALESCE(length(error_code), 0)), 0)
			FROM inbox_commands WHERE status != 'processing'
		`).Scan(&rows, &bytes); err != nil {
			t.Fatal(err)
		}
		if rows > 16 || bytes > 8<<10 {
			t.Fatalf("runtime inbox retention escaped bound after write %d: rows=%d bytes=%d", index, rows, bytes)
		}
	}
	store.durableMu.Lock()
	amortizedWrites := store.retention.writes
	store.durableMu.Unlock()
	if amortizedWrites != 100 {
		t.Fatalf("hard-bound writes unexpectedly ran full retention: writes=%d", amortizedWrites)
	}
	for index := 0; index < 50; index++ {
		if err := store.SaveNativeSession(t.Context(), fmt.Sprintf("bounded-run-%03d", index),
			"fake", fmt.Sprintf("native-%03d", index)); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveConversationSession(t.Context(), fmt.Sprintf("bounded-conversation-%03d", index),
			"fake", fmt.Sprintf("native-%03d", index)); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"native_sessions", "conversation_sessions"} {
			var rows int
			if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows > 8 {
				t.Fatalf("runtime %s retention escaped bound after write %d: rows=%d", table, index, rows)
			}
		}
	}
	for index := 0; index < 100; index++ {
		if err := store.SaveNativeSession(t.Context(), "bounded-run-049", "本地",
			fmt.Sprintf("会话-%03d", index)); err != nil {
			t.Fatalf("upsert native session %d: %v", index, err)
		}
		if err := store.SaveConversationSession(t.Context(), "bounded-conversation-049", "fake",
			fmt.Sprintf("会话-%03d", index)); err != nil {
			t.Fatalf("upsert conversation session %d: %v", index, err)
		}
	}
	for _, table := range []string{"native_sessions", "conversation_sessions"} {
		var rows int
		if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil || rows != 8 {
			t.Fatalf("session upserts corrupted %s retention count: rows=%d err=%v", table, rows, err)
		}
	}
	oldCommand := "bounded-command-099"
	oldTimestamp := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE inbox_commands SET finished_at = ? WHERE message_id = ?
	`, oldTimestamp, oldCommand); err != nil {
		t.Fatal(err)
	}
	store.durableMu.Lock()
	store.retention.policy.inboxAge = time.Hour
	store.retention.lastPrune = time.Now().UTC().Add(-2 * time.Hour)
	store.durableMu.Unlock()
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "age-prune-trigger", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("age prune trigger: claimed=%t err=%v", claimed, err)
	}
	var oldRows int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE message_id = ?
	`, oldCommand).Scan(&oldRows); err != nil || oldRows != 0 {
		t.Fatalf("production write did not prune aged terminal row: rows=%d err=%v", oldRows, err)
	}
}

func TestUnackedEventsPrefiltersCumulativeWireBytesBeforePayloadScan(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := strings.Repeat("x", 256<<10)
	for index := 0; index < nodeprotocol.MaxRunEventsPerBatch; index++ {
		if _, err := store.AppendEvent(t.Context(), "run-wire-prefix", 1, adapter.Event{
			Type: "provider.event", Payload: map[string]any{"text": payload},
		}); err != nil {
			t.Fatalf("append event %d: %v", index, err)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	events, err := store.UnackedEvents(t.Context(), nodeprotocol.MaxRunEventsPerBatch)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || len(events) >= nodeprotocol.MaxRunEventsPerBatch {
		t.Fatalf("wire prefix event count=%d", len(events))
	}
	wire := make([]nodeprotocol.RunEvent, 0, len(events))
	for _, event := range events {
		wire = append(wire, nodeprotocol.RunEvent{
			ID: event.ID, RunID: event.RunID, LeaseEpoch: event.LeaseEpoch, Sequence: event.Sequence,
			Type: event.Type, Payload: event.Payload, OccurredAt: event.OccurredAt,
		})
	}
	encoded, err := json.Marshal(nodeprotocol.RunEventBatch{Events: wire})
	if err != nil || len(encoded) > maxUnackedWireJSONBytes {
		t.Fatalf("bounded wire bytes=%d err=%v", len(encoded), err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	// json.Decoder must buffer each value to apply UseNumber and perform the
	// second trailing-data decode; keep that work within a fixed wire-budget
	// multiple rather than reverting to lossy json.Unmarshal numbers.
	if allocated > 8*uint64(maxUnackedWireJSONBytes) {
		t.Fatalf("UnackedEvents allocated %d bytes for an %d-byte prefix", allocated, len(encoded))
	}
}

func TestUnackedEvents128NearLimitMetadataHasBoundedAllocation(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, occurred_at, wire_bytes
		) VALUES (?, 'run-near-limit-metadata', 1, ?, 'provider.event', '{"ok":true}', ?, ?)
	`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index := 0; index < nodeprotocol.MaxRunEventsPerBatch; index++ {
		if _, err := statement.ExecContext(t.Context(), fmt.Sprintf("near-limit-%03d", index),
			index+1, now, nodeprotocol.MaxRunEventBytes-1024); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	events, err := store.UnackedEvents(t.Context(), nodeprotocol.MaxRunEventsPerBatch)
	runtime.ReadMemStats(&after)
	if err != nil || len(events) != 1 {
		t.Fatalf("near-limit metadata prefix=%d err=%v", len(events), err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(maxUnackedWireJSONBytes) {
		t.Fatalf("near-limit metadata scan allocated %d bytes", allocated)
	}
}

func TestOpenPrunesLargeLegacyDurableStateWithBoundedAllocation(t *testing.T) {
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
	_, err = raw.ExecContext(t.Context(), `
		WITH digits(value) AS (VALUES(0),(1),(2),(3),(4),(5),(6),(7),(8),(9)),
		numbers(value) AS (
			SELECT a.value + 10*b.value + 100*c.value + 1000*d.value + 10000*e.value
			FROM digits a, digits b, digits c, digits d, digits e
			LIMIT 30000
		)
		INSERT INTO inbox_commands (
			message_id, name, payload, status, result, error_code, received_at, finished_at
		)
		SELECT printf('legacy-%05d', value), 'probe', '{}', 'completed', '{"ok":true}', NULL, ?, ?
		FROM numbers
	`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if closeErr := raw.Close(); err != nil || closeErr != nil {
		t.Fatalf("seed large legacy state: err=%v close=%v", err, closeErr)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	store, err = Open(t.Context(), path)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE status != 'processing'
	`).Scan(&rows); err != nil || rows != maxRetainedInboxCommands {
		t.Fatalf("legacy rows after startup prune=%d err=%v", rows, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 128<<20 {
		t.Fatalf("large legacy startup allocated %d bytes", allocated)
	}
}

func TestEventWireBytesRejectsOldSchemaAndPreservesOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEvent(t.Context(), "run-wire-migration", 1, adapter.Event{
		Type: "run.completed", Payload: map[string]any{"status": "completed"},
	})
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
	if _, err := raw.ExecContext(t.Context(), `
		DROP TRIGGER IF EXISTS event_outbox_after_insert;
		DROP TRIGGER IF EXISTS event_outbox_after_delete;
		DROP TRIGGER IF EXISTS event_outbox_after_update;
		DROP TABLE event_outbox_state;
		ALTER TABLE event_log DROP COLUMN wire_bytes;
	`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err == nil {
		store.Close()
		t.Fatal("old event schema must not be migrated automatically")
	}
	raw, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_log WHERE id=?`, event.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected schema lost its outbox: count=%d err=%v", count, err)
	}
}

func TestEventWireBytesPersistsExactCurrentSize(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	newEvent, err := store.AppendEvent(t.Context(), "run-wire-exact", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"text": "exact-wire-size"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := nodeprotocol.EncodeRunEvent(protocolEvent(newEvent))
	if err != nil {
		t.Fatal(err)
	}
	var wireBytes int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT wire_bytes FROM event_log WHERE id = ?
	`, newEvent.ID).Scan(&wireBytes); err != nil || wireBytes != len(encoded) {
		t.Fatalf("new exact wire_bytes=%d want=%d err=%v", wireBytes, len(encoded), err)
	}
}

func TestOversizedTerminalPoisonDoesNotBlockFollowingEvent(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(), `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, occurred_at, wire_bytes
		) VALUES
			('terminal-poison', 'run-terminal-poison', 1, 1, 'run.completed', '{}', ?, ?),
			('terminal-neighbor', 'run-terminal-poison', 1, 2, 'provider.event', '{"ok":true}', ?, 256)
	`, now, maxUnackedWireJSONBytes+1, now); err != nil {
		t.Fatal(err)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("terminal poison prefix=%+v err=%v", events, err)
	}
	if events[0].Type != "run.finished" || events[0].Payload["status"] != "failed" ||
		events[0].Payload["code"] != "durable_event_poisoned" ||
		events[1].ID != "terminal-neighbor" {
		t.Fatalf("terminal poison blocked or swallowed neighbor: %+v", events)
	}
}

func TestOpenRejectsCorruptTerminalFinishedAtWithoutClearingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "corrupt-retention", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("claim: claimed=%t err=%v", claimed, err)
	}
	if err := store.CompleteCommand(t.Context(), "corrupt-retention", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE inbox_commands SET finished_at = 'not-a-timestamp' WHERE message_id = 'corrupt-retention'
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path); err == nil {
		_ = reopened.Close()
		t.Fatal("corrupt inbox retention metadata did not refuse startup")
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var timestamp string
	if err := raw.QueryRowContext(t.Context(), `
		SELECT finished_at FROM inbox_commands WHERE message_id = 'corrupt-retention'
	`).Scan(&timestamp); err != nil || timestamp != "not-a-timestamp" {
		t.Fatalf("corrupt row was deleted or changed: timestamp=%q err=%v", timestamp, err)
	}
}

func TestUnackedEventsAlwaysReturnsRunSequencePrefix(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.AppendEvent(ctx, "run-prefix", 1, adapter.Event{
		Sequence: 1, Type: "output.delta", Payload: map[string]any{"text": "first"},
		OccurredAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, "run-prefix", 1, adapter.Event{
		Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "second"},
		OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.UnackedEvents(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("cumulative ACK batch skipped a sequence prefix: %+v", events)
	}
}

func TestLocalResourcePathIsCanonicalAndStable(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := t.TempDir()
	first, err := store.RegisterResource(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterResource(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveResource(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first != want || second != want || resolved != want {
		t.Fatalf("resource path is not canonical/stable: first=%q second=%q resolved=%q want=%q", first, second, resolved, want)
	}
}

func TestRunGitSnapshotsAreCompressedAndSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n" + strings.Repeat("+bounded history\n", 4_000)
	snapshot := nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo-stable", Label: "Code", RootDisplay: "Code", Phase: "start",
		CapturedAt: time.Now().UTC(),
		Areas:      []nodeprotocol.GitRunPatchArea{{Area: "unstaged", Patch: patch, Paths: []string{"a.txt"}}},
		Untracked:  []nodeprotocol.GitRunUntrackedEntry{}, Warnings: []nodeprotocol.GitRepositoryWarning{},
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-persist", snapshot); err != nil {
		t.Fatal(err)
	}
	var compressedBytes, uncompressedBytes int
	if err := store.db.QueryRowContext(ctx, `
		SELECT compressed_bytes, uncompressed_bytes FROM run_git_snapshots
		WHERE run_id = ? AND phase = 'start'
	`, "run-persist").Scan(&compressedBytes, &uncompressedBytes); err != nil {
		t.Fatal(err)
	}
	if compressedBytes >= uncompressedBytes || uncompressedBytes < len(patch) {
		t.Fatalf("snapshot was not usefully compressed: compressed=%d raw=%d", compressedBytes, uncompressedBytes)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshots, err := store.GetRunGitSnapshots(ctx, "run-persist")
	if err != nil || len(snapshots) != 2 || snapshots[0].Areas[0].Patch != patch || snapshots[0].ContentHash == "" ||
		snapshots[1].Phase != "end" || len(snapshots[1].Warnings) != 1 ||
		snapshots[1].Warnings[0].Code != "snapshot_incomplete" {
		t.Fatalf("persisted snapshots=%+v err=%v", snapshots, err)
	}
}

func TestRunGitSnapshotsPersistAllRepositoryPhases(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	for index := 0; index < nodeprotocol.MaxGitRepositoriesPerRun; index++ {
		repositoryID := fmt.Sprintf("repository-%03d", index)
		for phaseIndex, phase := range []string{"start", "end"} {
			if err := store.SaveRunGitSnapshot(ctx, "run-multi-repository", nodeprotocol.GitRunWorkspaceSnapshot{
				RepositoryID: repositoryID, Phase: phase,
				CapturedAt: base.Add(time.Duration(index*2+phaseIndex) * time.Nanosecond),
				Areas:      []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{},
			}); err != nil {
				t.Fatalf("save repository %d phase %s: %v", index, phase, err)
			}
		}
	}
	snapshots, err := store.GetRunGitSnapshots(ctx, "run-multi-repository")
	if err != nil || len(snapshots) != maxRunGitSnapshotsPerRun {
		t.Fatalf("multi-repository snapshots=%d err=%v", len(snapshots), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshots, err = store.GetRunGitSnapshots(ctx, "run-multi-repository")
	if err != nil || len(snapshots) != maxRunGitSnapshotsPerRun {
		t.Fatalf("restarted multi-repository snapshots=%d err=%v", len(snapshots), err)
	}
	for index, snapshot := range snapshots {
		wantPhase := "start"
		wantRepository := fmt.Sprintf("repository-%03d", index)
		if index >= nodeprotocol.MaxGitRepositoriesPerRun {
			wantPhase = "end"
			wantRepository = fmt.Sprintf("repository-%03d", index-nodeprotocol.MaxGitRepositoriesPerRun)
		}
		if snapshot.Phase != wantPhase || snapshot.RepositoryID != wantRepository {
			t.Fatalf("snapshot[%d]=%s/%s want=%s/%s", index, snapshot.Phase,
				snapshot.RepositoryID, wantPhase, wantRepository)
		}
	}
	// An exact-key refresh remains legal at the bound, but a 101st repository
	// (and therefore any possible 201st phase row) is rejected atomically.
	if err := store.SaveRunGitSnapshot(ctx, "run-multi-repository", snapshots[0]); err != nil {
		t.Fatalf("bounded snapshot refresh failed: %v", err)
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-multi-repository", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository-overflow", Phase: "start", CapturedAt: base.Add(time.Second),
		Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err == nil {
		t.Fatal("snapshot store accepted a repository beyond the per-run bound")
	}
}

func TestRunGitSnapshotOpenRejectsPerRunRepositoryOverflow(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	for index := 0; index <= nodeprotocol.MaxGitRepositoriesPerRun; index++ {
		snapshot := nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: fmt.Sprintf("repository-corrupt-%03d", index), Phase: "start",
			CapturedAt: base.Add(time.Duration(index) * time.Nanosecond),
			Areas:      []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
			Warnings: []nodeprotocol.GitRepositoryWarning{},
		}
		snapshot.ContentHash = snapshotContentHash("run-corrupt-repositories", snapshot)
		uncompressedBytes, compressed, encodeErr := encodeRunGitSnapshot(ctx, snapshot)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, insertErr := store.db.ExecContext(ctx, `
			INSERT INTO run_git_snapshots (
				run_id, phase, repository_id, captured_at_ns, content_hash,
				uncompressed_bytes, compressed_bytes, truncated, payload
			) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
		`, "run-corrupt-repositories", snapshot.Phase, snapshot.RepositoryID,
			snapshot.CapturedAt.UnixNano(), snapshot.ContentHash,
			uncompressedBytes, len(compressed), compressed); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("runtime store opened a per-run repository overflow")
	}
	var rows int
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM run_git_snapshots WHERE run_id = 'run-corrupt-repositories'
	`).Scan(&rows); err != nil || rows != nodeprotocol.MaxGitRepositoriesPerRun+1 {
		t.Fatalf("overflow database was modified: rows=%d err=%v", rows, err)
	}
}

func TestRunGitSnapshotRejectsOldTimestampSchemaWithoutRewriting(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Date(2026, time.August, 11, 9, 8, 7, 654321987, time.UTC)
	if err := store.SaveRunGitSnapshot(ctx, "run-legacy-time", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo-legacy", Label: "Code", RootDisplay: "Code", Phase: "start",
		CapturedAt: capturedAt, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	rewriteRunGitSnapshotsWithLegacyTimestamp(t, ctx, store, capturedAt.Format(time.RFC3339Nano))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("old timestamp schema must be rejected, not migrated")
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var retained string
	if err := raw.QueryRowContext(ctx, `SELECT captured_at FROM run_git_snapshots WHERE run_id='run-legacy-time'`).Scan(&retained); err != nil || retained != capturedAt.Format(time.RFC3339Nano) {
		t.Fatalf("old data was modified: err=%v", err)
	}
}

func TestRunGitSnapshotRejectsInvalidOldSchemaWithoutMutation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Date(2026, time.August, 11, 9, 8, 7, 654321987, time.UTC)
	if err := store.SaveRunGitSnapshot(ctx, "run-invalid-legacy-time", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo-legacy", Label: "Code", RootDisplay: "Code", Phase: "start",
		CapturedAt: capturedAt, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	rewriteRunGitSnapshotsWithLegacyTimestamp(t, ctx, store, "not-a-timestamp")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("invalid legacy timestamp did not refuse startup")
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var storedTimestamp string
	if err := raw.QueryRowContext(ctx, `
		SELECT captured_at FROM run_git_snapshots WHERE run_id = 'run-invalid-legacy-time'
	`).Scan(&storedTimestamp); err != nil || storedTimestamp != "not-a-timestamp" {
		t.Fatalf("failed migration mutated legacy row: timestamp=%q err=%v", storedTimestamp, err)
	}
	var migrationObjects int
	if err := raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE name = 'run_git_snapshots_legacy_ns'
	`).Scan(&migrationObjects); err != nil || migrationObjects != 0 {
		t.Fatalf("failed migration left temporary schema objects: count=%d err=%v", migrationObjects, err)
	}
}

func rewriteRunGitSnapshotsWithLegacyTimestamp(t *testing.T, ctx context.Context, store *Store, capturedAt string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`ALTER TABLE run_git_snapshots RENAME TO run_git_snapshots_integer_time`,
		`CREATE TABLE run_git_snapshots (
			run_id TEXT NOT NULL,
			phase TEXT NOT NULL CHECK(phase IN ('start', 'end')),
			repository_id TEXT NOT NULL,
			captured_at TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			uncompressed_bytes INTEGER NOT NULL CHECK(uncompressed_bytes >= 0),
			compressed_bytes INTEGER NOT NULL CHECK(compressed_bytes >= 0),
			truncated INTEGER NOT NULL DEFAULT 0 CHECK(truncated IN (0, 1)),
			payload BLOB NOT NULL,
			PRIMARY KEY(run_id, phase)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_git_snapshots (
			run_id, phase, repository_id, captured_at, content_hash,
			uncompressed_bytes, compressed_bytes, truncated, payload
		)
		SELECT run_id, phase, repository_id, ?, content_hash,
		       uncompressed_bytes, compressed_bytes, truncated, payload
		FROM run_git_snapshots_integer_time
	`, capturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE run_git_snapshots_integer_time`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRunGitSnapshotRetentionDeletesWholeRuns(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for index := 0; index < maxRunGitSnapshotRuns+5; index++ {
		runID := fmt.Sprintf("run-retention-%03d", index)
		for _, phase := range []string{"start", "end"} {
			if err := store.SaveRunGitSnapshot(ctx, runID, nodeprotocol.GitRunWorkspaceSnapshot{
				RepositoryID: "repo", Label: "Code", RootDisplay: "Code", Phase: phase,
				CapturedAt: now.Add(time.Duration(index) * time.Second),
				Areas:      []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT run_id, COUNT(*) FROM run_git_snapshots GROUP BY run_id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	runs := 0
	for rows.Next() {
		var runID string
		var phases int
		if err := rows.Scan(&runID, &phases); err != nil {
			t.Fatal(err)
		}
		if phases != 2 {
			t.Fatalf("retention left partial run %q with %d phases", runID, phases)
		}
		runs++
	}
	if runs != maxRunGitSnapshotRuns {
		t.Fatalf("retained %d runs, want %d", runs, maxRunGitSnapshotRuns)
	}

	for _, phase := range []string{"start", "end"} {
		if err := store.SaveRunGitSnapshot(ctx, "run-expired", nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: "repo", Label: "Code", RootDisplay: "Code", Phase: phase,
			CapturedAt: now.Add(-runGitSnapshotMaxAge - time.Hour),
			Areas:      []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
			Warnings: []nodeprotocol.GitRepositoryWarning{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var expired int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_git_snapshots WHERE run_id = 'run-expired'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatal("expired run snapshot was retained")
	}
}

func TestRunGitSnapshotRetentionProtectsActiveRunsWithHardBound(t *testing.T) {
	if maxActiveRunGitSnapshotRuns != 128 {
		t.Fatalf("active snapshot count bound=%d want=128", maxActiveRunGitSnapshotRuns)
	}
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := time.Now().UTC().Add(-runGitSnapshotMaxAge - time.Hour)
	if err := store.SaveRunGitSnapshot(ctx, "run-active-old", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo", Phase: "start", CapturedAt: old,
		Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxActiveRunGitSnapshotRuns-1; index++ {
		if err := store.SaveRunGitSnapshot(ctx, fmt.Sprintf("run-active-%03d", index), nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: "repo", Phase: "start", CapturedAt: time.Now().UTC(),
			Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
			Warnings: []nodeprotocol.GitRepositoryWarning{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-active-overflow", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo", Phase: "start", CapturedAt: time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err == nil {
		t.Fatal("active run retention hard bound was not enforced")
	}
	var oldCount, overflowCount, total int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_git_snapshots WHERE run_id = 'run-active-old'`).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_git_snapshots WHERE run_id = 'run-active-overflow'`).Scan(&overflowCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_git_snapshots`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if oldCount != 1 || overflowCount != 0 || total != maxActiveRunGitSnapshotRuns {
		t.Fatalf("active retention old=%d overflow=%d total=%d", oldCount, overflowCount, total)
	}
}

func TestRestartMarks128StartOnlySnapshotsIncompleteAndAllowsNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < maxActiveRunGitSnapshotRuns; index++ {
		if err := store.SaveRunGitSnapshot(t.Context(), fmt.Sprintf("interrupted-%02d", index),
			nodeprotocol.GitRunWorkspaceSnapshot{
				RepositoryID: "repo", Phase: "start", CapturedAt: now.Add(time.Duration(index) * time.Nanosecond),
				Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
				Warnings: []nodeprotocol.GitRepositoryWarning{},
			}); err != nil {
			t.Fatalf("save interrupted start %d: %v", index, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < maxActiveRunGitSnapshotRuns; index++ {
		snapshots, err := store.GetRunGitSnapshots(t.Context(), fmt.Sprintf("interrupted-%02d", index))
		if err != nil || len(snapshots) != 2 || snapshots[0].Phase != "start" || snapshots[1].Phase != "end" ||
			len(snapshots[1].Warnings) != 1 || snapshots[1].Warnings[0].Code != "snapshot_incomplete" {
			t.Fatalf("recovered snapshot %d=%+v err=%v", index, snapshots, err)
		}
	}
	if err := store.SaveRunGitSnapshot(t.Context(), "run-after-restart", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo", Phase: "start", CapturedAt: time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatalf("new start after interrupted-run recovery: %v", err)
	}
}

func TestOpenValidatesCorruptTerminalBeforeRecoveringStartOnlySnapshotsOrPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		runID string
		phase string
	}{
		{runID: "corrupt-old-terminal", phase: "end"},
		{runID: "healthy-start-only", phase: "start"},
	} {
		if err := store.SaveRunGitSnapshot(t.Context(), item.runID, nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: "repo", Phase: item.phase, CapturedAt: time.Now().UTC(),
			Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
			Warnings: []nodeprotocol.GitRepositoryWarning{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	corruptTimestamp := time.Now().UTC().Add(-runGitSnapshotMaxAge - time.Hour).UnixNano()
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE run_git_snapshots SET captured_at_ns = ?
		WHERE run_id = 'corrupt-old-terminal' AND phase = 'end'
	`, corruptTimestamp); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path); err == nil {
		_ = reopened.Close()
		t.Fatal("corrupt terminal snapshot was pruned instead of refusing startup")
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var syntheticEnds, corruptRows int
	var retainedTimestamp int64
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM run_git_snapshots
		WHERE run_id = 'healthy-start-only' AND phase = 'end'
	`).Scan(&syntheticEnds); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*), COALESCE(MIN(captured_at_ns), 0)
		FROM run_git_snapshots WHERE run_id = 'corrupt-old-terminal' AND phase = 'end'
	`).Scan(&corruptRows, &retainedTimestamp); err != nil {
		t.Fatal(err)
	}
	if syntheticEnds != 0 || corruptRows != 1 || retainedTimestamp != corruptTimestamp {
		t.Fatalf("failed Open mutated snapshots: synthetic_end=%d corrupt_rows=%d timestamp=%d want=%d",
			syntheticEnds, corruptRows, retainedTimestamp, corruptTimestamp)
	}
}

func TestRunGitSnapshotRejectsCorruptionAndCompressionBomb(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().UnixNano()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO run_git_snapshots (
			run_id, phase, repository_id, captured_at_ns, content_hash,
			uncompressed_bytes, compressed_bytes, truncated, payload
		) VALUES ('run-corrupt', 'start', 'repo', ?, 'bad', 4, 4, 0, X'01020304')
	`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRunGitSnapshots(ctx, "run-corrupt"); err == nil {
		t.Fatal("corrupt compressed payload was accepted")
	}

	var bomb bytes.Buffer
	writer := gzip.NewWriter(&bomb)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), maxRunGitSnapshotUncompressed+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO run_git_snapshots (
			run_id, phase, repository_id, captured_at_ns, content_hash,
			uncompressed_bytes, compressed_bytes, truncated, payload
		) VALUES (?, 'start', 'repo', ?, 'bad', ?, ?, 0, ?)
	`, "run-bomb", now, maxRunGitSnapshotUncompressed, bomb.Len(), bomb.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRunGitSnapshots(ctx, "run-bomb"); err == nil {
		t.Fatal("gzip expansion beyond 20 MiB was accepted")
	}
}

func TestRunGitSnapshotHashBindsRunAndChecksDuplicatedColumns(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository", Phase: "start", CapturedAt: time.Now().UTC(),
		Areas:     []nodeprotocol.GitRunPatchArea{{Area: "unstaged", Paths: []string{"file.txt"}}},
		Untracked: []nodeprotocol.GitRunUntrackedEntry{}, Warnings: []nodeprotocol.GitRepositoryWarning{},
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-source", snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO run_git_snapshots (
			run_id, phase, repository_id, captured_at_ns, content_hash,
			uncompressed_bytes, compressed_bytes, truncated, payload
		)
		SELECT 'run-copy', phase, repository_id, captured_at_ns, content_hash,
		       uncompressed_bytes, compressed_bytes, truncated, payload
		FROM run_git_snapshots WHERE run_id = 'run-source'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRunGitSnapshots(ctx, "run-copy"); err == nil {
		t.Fatal("snapshot row copied to another run passed content hash validation")
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE run_git_snapshots SET captured_at_ns = ?, truncated = 1
		WHERE run_id = 'run-source'
	`, time.Now().UTC().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRunGitSnapshots(ctx, "run-source"); err == nil {
		t.Fatal("mismatched captured_at_ns/truncated duplicated columns were accepted")
	}
}

func TestOpenRejectsInvalidRunGitRetentionTimestampWithoutDeletingDatabase(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-poisoned", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository", Phase: "end", CapturedAt: time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{}, Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE run_git_snapshots SET captured_at_ns = 'not-a-time' WHERE run_id = 'run-poisoned'
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		_ = reopened.Close()
		t.Fatal("Store.Open accepted an invalid retention timestamp")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("runtime database was removed after validation failure: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var capturedAt string
	if err := raw.QueryRowContext(ctx, `
		SELECT captured_at_ns FROM run_git_snapshots WHERE run_id = 'run-poisoned'
	`).Scan(&capturedAt); err != nil {
		t.Fatal(err)
	}
	if capturedAt != "not-a-time" {
		t.Fatalf("poisoned row was mutated or deleted: captured_at_ns=%q", capturedAt)
	}
}

func TestOpenRejectsCorruptRunGitSnapshotPayloadWithoutDeletingDatabase(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-corrupt-open", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository", Phase: "end", CapturedAt: time.Now().UTC(),
		Areas:     []nodeprotocol.GitRunPatchArea{{Area: "unstaged", Patch: "diff --git a/a b/a\n"}},
		Untracked: []nodeprotocol.GitRunUntrackedEntry{}, Warnings: []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := store.db.QueryRowContext(ctx, `
		SELECT payload FROM run_git_snapshots WHERE run_id = 'run-corrupt-open'
	`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) < 8 {
		t.Fatalf("unexpected compressed payload length %d", len(payload))
	}
	payload[len(payload)/2] ^= 0xff
	if _, err := store.db.ExecContext(ctx, `
		UPDATE run_git_snapshots SET payload = ? WHERE run_id = 'run-corrupt-open'
	`, payload); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		_ = reopened.Close()
		t.Fatal("Store.Open accepted a corrupt bounded gzip payload")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("runtime database was removed after payload validation failure: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var retained []byte
	if err := raw.QueryRowContext(ctx, `SELECT payload FROM run_git_snapshots WHERE run_id = 'run-corrupt-open'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, payload) {
		t.Fatal("corrupt payload was mutated during failed Open")
	}
}

func TestLocalStoreRecoversDurableStateAfterSIGKILL(t *testing.T) {
	if os.Getenv("TEAMVA_LOCALSTORE_CRASH_HELPER") == "1" {
		runLocalStoreCrashHelper(t)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL recovery test requires a Unix process model")
	}
	path := filepath.Join(t.TempDir(), "runtime.db")
	command := exec.Command(os.Args[0], "-test.run=^TestLocalStoreRecoversDurableStateAfterSIGKILL$")
	command.Env = append(os.Environ(),
		"TEAMVA_LOCALSTORE_CRASH_HELPER=1",
		"TEAMVA_LOCALSTORE_CRASH_PATH="+path,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "CRASH-READY" {
				ready <- scanner.Text()
				return
			}
		}
		ready <- ""
	}()
	select {
	case marker := <-ready:
		if marker == "" {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal("crash helper exited before persisting state")
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("crash helper did not become ready")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}

	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.GetCommand(t.Context(), "cmd-crash")
	if err != nil || record.Status != "uncertain" || record.ErrorCode != "node_restarted_during_command" {
		t.Fatalf("recovered command=%+v err=%v", record, err)
	}
	_, persistedIdentity, found, err := store.lookupCommand(t.Context(), "cmd-crash")
	if err != nil || !found || strings.Contains(string(persistedIdentity), "data:image") ||
		!strings.Contains(string(persistedIdentity), "capture.png") || !strings.Contains(string(persistedIdentity), "payload_fingerprint") {
		t.Fatalf("recovered command identity was not redacted: found=%t payload=%s err=%v", found, persistedIdentity, err)
	}
	sessionID, found, err := store.GetConversationSession(t.Context(), "generation-crash", "opencode")
	if err != nil || !found || sessionID != "native-session-crash" {
		t.Fatalf("recovered conversation session=%q found=%t err=%v", sessionID, found, err)
	}
	var provider, nativeSessionID string
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT provider, native_session_id FROM native_sessions WHERE run_id = ?
	`, "run-crash").Scan(&provider, &nativeSessionID); err != nil {
		t.Fatal(err)
	}
	if provider != "opencode" || nativeSessionID != "native-session-crash" {
		t.Fatalf("recovered native session provider=%q id=%q", provider, nativeSessionID)
	}
	events, err := store.UnackedEvents(t.Context(), 10)
	if err != nil || len(events) != 1 || events[0].RunID != "run-crash" || events[0].Type != "run.started" {
		t.Fatalf("recovered events=%+v err=%v", events, err)
	}
}

func runLocalStoreCrashHelper(t *testing.T) {
	path := os.Getenv("TEAMVA_LOCALSTORE_CRASH_PATH")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimCommand(context.Background(), Command{
		MessageID: "cmd-crash", Name: "run.start", Payload: json.RawMessage(`{
			"protocol_version":6,
			"payload":{"run_id":"run-crash","attachments":[{
				"mime_type":"image/png","filename":"capture.png",
				"data_url":"data:image/png;base64,secret","data_sha256":"sha256:test-data"
			}]},
			"payload_fingerprint":"sha256:test"
		}`),
	}); err != nil || !claimed {
		t.Fatalf("claim crash command: claimed=%t err=%v", claimed, err)
	}
	if err := store.SaveNativeSession(context.Background(), "run-crash", "opencode", "native-session-crash"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConversationSession(context.Background(), "generation-crash", "opencode", "native-session-crash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(context.Background(), "run-crash", 1, adapter.Event{Type: "run.started"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "CRASH-READY"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Minute)
}
