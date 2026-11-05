package localstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestClaimCommandRequiresExactPersistedNameAndPayloadBytes(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := Command{
		MessageID: "exact-command",
		Name:      "run.start",
		Payload:   json.RawMessage(`{"first":1,"second":2}`),
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), command); err != nil || !claimed {
		t.Fatalf("initial claim: claimed=%t err=%v", claimed, err)
	}
	if err := store.CompleteCommand(t.Context(), command.MessageID, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}

	conflicts := []Command{
		{MessageID: command.MessageID, Name: "run.stop", Payload: command.Payload},
		{MessageID: command.MessageID, Name: "\t", Payload: command.Payload},
		// This JSON is semantically equivalent, but its original bytes differ.
		{MessageID: command.MessageID, Name: command.Name, Payload: json.RawMessage(`{"second":2,"first":1}`)},
		{MessageID: command.MessageID, Name: command.Name, Payload: json.RawMessage(`{ "first": 1, "second": 2 }`)},
	}
	for index, conflict := range conflicts {
		record, claimed, err := store.ClaimCommand(t.Context(), conflict)
		if !errors.Is(err, ErrCommandConflict) || claimed || record.MessageID != "" ||
			record.Name != "" || record.Status != "" || len(record.Result) != 0 {
			t.Fatalf("conflict %d returned old result: record=%+v claimed=%t err=%v", index, record, claimed, err)
		}
	}
	replayed, claimed, err := store.ClaimCommand(t.Context(), command)
	if err != nil || claimed || replayed.Status != "completed" {
		t.Fatalf("exact replay: record=%+v claimed=%t err=%v", replayed, claimed, err)
	}
}

func TestCommandTerminalResultsAlwaysFitCommandResultFrame(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	completedCommand := Command{
		MessageID: strings.Repeat("c", maxCommandIDBytes),
		Name:      "large.complete",
		Payload:   json.RawMessage(`{}`),
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), completedCommand); err != nil || !claimed {
		t.Fatalf("claim oversized completion: claimed=%t err=%v", claimed, err)
	}
	completedResult := map[string]any{
		"data": strings.Repeat("x", nodeprotocol.MaxCommandResultBytes-(2<<10)),
	}
	encodedResult, err := json.Marshal(completedResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedResult) >= nodeprotocol.MaxCommandResultBytes {
		t.Fatalf("fixture result alone is not below frame limit: %d", len(encodedResult))
	}
	if _, err := nodeprotocol.EncodeCommandResult(nodeprotocol.CommandResult{
		CommandID: completedCommand.MessageID,
		Status:    "completed",
		Result:    encodedResult,
	}); err == nil {
		t.Fatal("fixture full command.result unexpectedly fits")
	}
	if err := store.CompleteCommand(t.Context(), completedCommand.MessageID, completedResult); err != nil {
		t.Fatal(err)
	}
	completedRecord, err := store.GetCommand(t.Context(), completedCommand.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if completedRecord.Status != "failed" || completedRecord.ErrorCode != commandResultTooLargeCode {
		t.Fatalf("oversized completion was not bounded failure: %+v", completedRecord)
	}
	assertCommandResultRecordFits(t, completedRecord)
	replayed, claimed, err := store.ClaimCommand(t.Context(), completedCommand)
	if err != nil || claimed || replayed.Status != "failed" || replayed.ErrorCode != commandResultTooLargeCode {
		t.Fatalf("oversized completion was reclaimed: record=%+v claimed=%t err=%v", replayed, claimed, err)
	}

	failedCommand := Command{MessageID: "large-failure", Name: "large.fail", Payload: json.RawMessage(`{}`)}
	if _, claimed, err := store.ClaimCommand(t.Context(), failedCommand); err != nil || !claimed {
		t.Fatalf("claim oversized failure: claimed=%t err=%v", claimed, err)
	}
	if err := store.FailCommand(t.Context(), failedCommand.MessageID, "provider_failed", map[string]any{
		"error": strings.Repeat("y", nodeprotocol.MaxCommandResultBytes),
	}); err != nil {
		t.Fatal(err)
	}
	failedRecord, err := store.GetCommand(t.Context(), failedCommand.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if failedRecord.Status != "failed" || failedRecord.ErrorCode != "provider_failed" ||
		!strings.Contains(string(failedRecord.Result), "transport size limit") {
		t.Fatalf("oversized failure detail was not bounded: %+v", failedRecord)
	}
	assertCommandResultRecordFits(t, failedRecord)
	if replayed, claimed, err := store.ClaimCommand(t.Context(), failedCommand); err != nil || claimed ||
		replayed.Status != "failed" || replayed.ErrorCode != "provider_failed" {
		t.Fatalf("oversized failure was reclaimed: record=%+v claimed=%t err=%v", replayed, claimed, err)
	}

	var rows, processing int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*), SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END)
		FROM inbox_commands WHERE message_id IN (?, ?)
	`, completedCommand.MessageID, failedCommand.MessageID).Scan(&rows, &processing); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || processing != 0 {
		t.Fatalf("terminal transition lost dedupe rows: rows=%d processing=%d", rows, processing)
	}
}

func assertCommandResultRecordFits(t *testing.T, record CommandRecord) {
	t.Helper()
	if _, err := nodeprotocol.EncodeCommandResult(nodeprotocol.CommandResult{
		CommandID: record.MessageID,
		Status:    record.Status,
		Result:    record.Result,
		ErrorCode: record.ErrorCode,
	}); err != nil {
		t.Fatalf("persisted command.result cannot be encoded: %v", err)
	}
}

func TestUnackedEventsPreservesJSONNumbersAndRejectsTrailingData(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const exactInteger = "900719925474099312345678901234567890"
	numberEvent, err := store.AppendEvent(t.Context(), "run-number", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"value": json.Number(exactInteger)},
	})
	if err != nil {
		t.Fatal(err)
	}
	trailingEvent, err := store.AppendEvent(t.Context(), "run-trailing", 1, adapter.Event{
		Type: "provider.event", Payload: map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET payload = ?, wire_bytes = 512 WHERE id = ?
	`, []byte(`{"ok":true} {"trailing":true}`), trailingEvent.ID); err != nil {
		t.Fatal(err)
	}

	events, err := store.UnackedEvents(t.Context(), 2)
	if err != nil || len(events) != 2 {
		t.Fatalf("unacked events=%+v err=%v", events, err)
	}
	value, ok := events[0].Payload["value"].(json.Number)
	if events[0].ID != numberEvent.ID || !ok || value.String() != exactInteger {
		t.Fatalf("large integer changed type/value: event=%+v type=%T", events[0], events[0].Payload["value"])
	}
	if events[1].Type != "node.diagnostic" || events[1].Payload["poison_reason"] != "invalid_payload" ||
		events[1].Payload["original_event_type"] != "provider.event" {
		t.Fatalf("trailing JSON was not rejected: %+v", events[1])
	}
}

func TestUnackedEventsDecodesEachValidPayloadIndependently(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, event := range []adapter.Event{
		{Type: "run.started", Payload: map[string]any{}},
		{Type: "run.finished", Payload: map[string]any{"status": "completed"}},
	} {
		if _, err := store.AppendEvent(t.Context(), "run-two-valid-payloads", 1, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.UnackedEvents(t.Context(), 2)
	if err != nil || len(events) != 2 || events[0].Type != "run.started" ||
		events[1].Type != "run.finished" || events[1].Payload["status"] != "completed" {
		t.Fatalf("independent payload decode events=%+v err=%v", events, err)
	}
}

func TestUnsupportedDiagnosticIsRejectedWithoutReinterpretation(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event, err := store.AppendEvent(t.Context(), "run-old-poison", 1, adapter.Event{
		Type: "run.completed", Payload: map[string]any{"status": "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := event
	legacy.ID = poisonEventID(event.RunID, event.LeaseEpoch, event.Sequence)
	legacy.Payload = map[string]any{
		"code":                "durable_event_poisoned",
		"reason":              "durable_event_poisoned",
		"poison_reason":       "invalid_payload",
		"original_event_type": "run.completed",
		"status":              "completed",
	}
	encoded, payload, err := nodeprotocol.EncodeRunEvent(protocolEvent(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
		UPDATE event_log SET id = ?, payload = ?, wire_bytes = ? WHERE id = ?
	`, legacy.ID, payload, len(encoded), event.ID); err != nil {
		t.Fatal(err)
	}
	events, err := store.UnackedEvents(t.Context(), 1)
	if err == nil || len(events) != 0 {
		t.Fatalf("unsupported diagnostic was replayed: events=%+v err=%v", events, err)
	}
	var stored []byte
	if err := store.db.QueryRow(`SELECT payload FROM event_log WHERE id=?`, legacy.ID).Scan(&stored); err != nil || string(stored) != string(payload) {
		t.Fatalf("rejected diagnostic changed: %v", err)
	}
}

func TestRunGitSnapshotUnicodeMetadataAndNanosecondsSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	runID := "运行-α-🚀"
	repositoryID := "仓库-β-代码"
	capturedAt := time.Date(2026, time.August, 11, 13, 14, 15, 987654321, time.UTC)
	if err := store.SaveRunGitSnapshot(ctx, runID, nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: repositoryID,
		Phase:        "start",
		CapturedAt:   capturedAt,
		Areas:        []nodeprotocol.GitRunPatchArea{},
		Untracked:    []nodeprotocol.GitRunUntrackedEntry{},
		Warnings:     []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetRunGitSnapshots(ctx, runID)
	if err != nil || len(current) != 1 || current[0].RepositoryID != repositoryID {
		t.Fatalf("current Unicode snapshot=%+v err=%v", current, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.GetRunGitSnapshots(ctx, runID)
	if err != nil || len(restored) != 2 || restored[0].RepositoryID != repositoryID ||
		!restored[0].CapturedAt.Equal(capturedAt) {
		t.Fatalf("restored Unicode snapshot=%+v err=%v", restored, err)
	}
}

func TestSaveRunGitSnapshotRejectsZeroAndYear3000BeforeWriting(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tests := []struct {
		runID      string
		capturedAt time.Time
	}{
		{runID: "run-zero-time", capturedAt: time.Time{}},
		{runID: "run-year-3000", capturedAt: time.Date(3000, time.January, 2, 3, 4, 5, 6, time.UTC)},
	}
	for _, test := range tests {
		err := store.SaveRunGitSnapshot(t.Context(), test.runID, nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: "repository",
			Phase:        "start",
			CapturedAt:   test.capturedAt,
			Areas:        []nodeprotocol.GitRunPatchArea{},
			Untracked:    []nodeprotocol.GitRunUntrackedEntry{},
			Warnings:     []nodeprotocol.GitRepositoryWarning{},
		})
		if err == nil {
			t.Fatalf("captured_at %s was accepted", test.capturedAt)
		}
		var rows int
		if queryErr := store.db.QueryRowContext(t.Context(), `
			SELECT COUNT(*) FROM run_git_snapshots WHERE run_id = ?
		`, test.runID).Scan(&rows); queryErr != nil || rows != 0 {
			t.Fatalf("rejected snapshot %q was written: rows=%d err=%v", test.runID, rows, queryErr)
		}
	}
}

func TestLegacyRunGitSnapshotMigrationRejectsYear3000WithoutMutation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRunGitSnapshot(ctx, "run-legacy-year-3000", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repository",
		Phase:        "start",
		CapturedAt:   time.Now().UTC(),
		Areas:        []nodeprotocol.GitRunPatchArea{},
		Untracked:    []nodeprotocol.GitRunUntrackedEntry{},
		Warnings:     []nodeprotocol.GitRepositoryWarning{},
	}); err != nil {
		t.Fatal(err)
	}
	year3000 := time.Date(3000, time.January, 2, 3, 4, 5, 6, time.UTC).Format(time.RFC3339Nano)
	rewriteRunGitSnapshotsWithLegacyTimestamp(t, ctx, store, year3000)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		_ = reopened.Close()
		t.Fatal("legacy year-3000 timestamp was accepted")
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var retained string
	if err := raw.QueryRowContext(ctx, `
		SELECT captured_at FROM run_git_snapshots WHERE run_id = 'run-legacy-year-3000'
	`).Scan(&retained); err != nil || retained != year3000 {
		t.Fatalf("failed legacy migration mutated timestamp=%q err=%v", retained, err)
	}
}
