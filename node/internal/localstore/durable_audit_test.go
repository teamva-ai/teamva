package localstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestRestartedUncertainCommandsSurviveAllRetentionBoundaries(t *testing.T) {
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
	want := maxRetainedInboxCommands + 5
	_, err = raw.ExecContext(t.Context(), `
		WITH digits(value) AS (VALUES(0),(1),(2),(3),(4),(5),(6),(7),(8),(9)),
		numbers(value) AS (
			SELECT a.value + 10*b.value + 100*c.value + 1000*d.value + 10000*e.value
			FROM digits a, digits b, digits c, digits d, digits e
			LIMIT ?
		)
		INSERT INTO inbox_commands (
			message_id, name, payload, status, received_at
		)
		SELECT printf('crashed-%05d', value), 'run.start', '{}', 'processing', ?
		FROM numbers
	`, want, time.Now().UTC().Add(-2*inboxRetentionAge).Format(time.RFC3339Nano))
	if closeErr := raw.Close(); err != nil || closeErr != nil {
		t.Fatalf("seed crashed commands: err=%v close=%v", err, closeErr)
	}

	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var uncertain int
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE status = 'uncertain'
	`).Scan(&uncertain); err != nil || uncertain != want {
		t.Fatalf("uncertain rows=%d want=%d err=%v", uncertain, want, err)
	}
	zeroPolicy := durableRetentionPolicy{
		inboxCount: 0, inboxBytes: 0, inboxAge: 0,
		sessionCount: maxRetainedSessions, sessionBytes: maxRetainedSessionBytes, sessionAge: sessionRetentionAge,
	}
	if err := store.pruneDurableState(t.Context(), time.Now().UTC(), zeroPolicy); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM inbox_commands WHERE status = 'uncertain'
	`).Scan(&uncertain); err != nil || uncertain != want {
		t.Fatalf("retention deleted uncertain rows=%d want=%d err=%v", uncertain, want, err)
	}
	record, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "crashed-00000", Name: "run.start", Payload: json.RawMessage(`{}`),
	})
	if err != nil || claimed || record.Status != "uncertain" || record.ErrorCode != "node_restarted_during_command" {
		t.Fatalf("uncertain command was reclaimed: record=%+v claimed=%t err=%v", record, claimed, err)
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "new-after-uncertain-overflow", Name: "run.start", Payload: json.RawMessage(`{}`),
	}); !errors.Is(err, ErrInboxAdmissionFull) || claimed {
		t.Fatalf("over-cap uncertain inbox admitted a distinct ID: claimed=%t err=%v", claimed, err)
	}
}

func TestProcessingAndUncertainInboxAdmissionHasCountAndByteBoundaries(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		store.inboxLimits = inboxAdmissionLimits{count: 3, bytes: 1 << 20}
		commands := []Command{
			{MessageID: "admission-uncertain", Name: "probe", Payload: json.RawMessage(`{}`)},
			{MessageID: "admission-processing", Name: "probe", Payload: json.RawMessage(`{}`)},
			{MessageID: "admission-third", Name: "probe", Payload: json.RawMessage(`{}`)},
		}
		for _, command := range commands {
			if _, claimed, err := store.ClaimCommand(t.Context(), command); err != nil || !claimed {
				t.Fatalf("claim %s: claimed=%t err=%v", command.MessageID, claimed, err)
			}
		}
		if _, err := store.db.ExecContext(t.Context(), `
			UPDATE inbox_commands
			SET status = 'uncertain', error_code = 'uncertain_fixture', finished_at = ?
			WHERE message_id = 'admission-uncertain'
		`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		overflow := Command{MessageID: "admission-overflow", Name: "probe", Payload: json.RawMessage(`{}`)}
		if _, claimed, err := store.ClaimCommand(t.Context(), overflow); !errors.Is(err, ErrInboxAdmissionFull) || claimed {
			t.Fatalf("combined count overflow: claimed=%t err=%v", claimed, err)
		}
		for _, command := range commands[:2] {
			replayed, claimed, err := store.ClaimCommand(t.Context(), command)
			if err != nil || claimed || replayed.MessageID != command.MessageID {
				t.Fatalf("existing command %s was not replayable: record=%+v claimed=%t err=%v",
					command.MessageID, replayed, claimed, err)
			}
		}
		if err := store.CompleteCommand(t.Context(), "admission-processing", map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
		if _, claimed, err := store.ClaimCommand(t.Context(), overflow); err != nil || !claimed {
			t.Fatalf("terminal transition did not release admission: claimed=%t err=%v", claimed, err)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		first := Command{MessageID: "byte-first", Name: "probe", Payload: json.RawMessage(`{"text":"first"}`)}
		second := Command{MessageID: "byte-second", Name: "probe", Payload: json.RawMessage(`{"text":"second"}`)}
		boundary := int64(len(first.MessageID) + len(first.Name) + len(first.Payload) +
			len(second.MessageID) + len(second.Name) + len(second.Payload))
		store.inboxLimits = inboxAdmissionLimits{count: 10, bytes: boundary}
		for _, command := range []Command{first, second} {
			if _, claimed, err := store.ClaimCommand(t.Context(), command); err != nil || !claimed {
				t.Fatalf("exact byte boundary claim %s: claimed=%t err=%v", command.MessageID, claimed, err)
			}
		}
		if _, claimed, err := store.ClaimCommand(t.Context(), Command{
			MessageID: "byte-overflow", Name: "probe", Payload: json.RawMessage(`{}`),
		}); !errors.Is(err, ErrInboxAdmissionFull) || claimed {
			t.Fatalf("byte overflow admitted: claimed=%t err=%v", claimed, err)
		}
		replayed, claimed, err := store.ClaimCommand(t.Context(), second)
		if err != nil || claimed || replayed.Status != "processing" {
			t.Fatalf("byte-bound existing ID was not replayable: record=%+v claimed=%t err=%v", replayed, claimed, err)
		}
	})
}

func TestCommittedWritesSucceedWhenIndependentRetentionMaintenanceFails(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	injected := errors.New("injected retention maintenance failure")
	attempts := 0
	store.durableMu.Lock()
	store.retention.maintenancePending = true
	store.retention.maintenanceHook = func() error {
		attempts++
		return injected
	}
	store.durableMu.Unlock()

	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "maintenance-complete", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("claim despite maintenance failure: claimed=%t err=%v", claimed, err)
	}
	if err := store.CompleteCommand(t.Context(), "maintenance-complete", map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete despite maintenance failure: %v", err)
	}
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "maintenance-fail", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("claim failed command: claimed=%t err=%v", claimed, err)
	}
	if err := store.FailCommand(t.Context(), "maintenance-fail", "injected_command_failure", map[string]any{"ok": false}); err != nil {
		t.Fatalf("fail despite maintenance failure: %v", err)
	}
	if err := store.SaveNativeSession(t.Context(), "run-maintenance", "fake", "native-maintenance"); err != nil {
		t.Fatalf("save native session despite maintenance failure: %v", err)
	}
	if err := store.SaveConversationSession(t.Context(), "conversation-maintenance", "fake", "native-maintenance"); err != nil {
		t.Fatalf("save conversation session despite maintenance failure: %v", err)
	}
	event, err := store.AppendEvent(t.Context(), "run-maintenance", 1, adapter.Event{Type: "run.started"})
	if err != nil {
		t.Fatalf("append despite maintenance failure: %v", err)
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{
		RunID: event.RunID, LeaseEpoch: event.LeaseEpoch, Sequence: event.Sequence,
	}}); err != nil {
		t.Fatalf("ACK despite maintenance failure: %v", err)
	}
	if attempts < 8 {
		t.Fatalf("maintenance attempts=%d, want a retry after every durable activity", attempts)
	}
	for _, command := range []struct {
		id     string
		status string
	}{
		{id: "maintenance-complete", status: "completed"},
		{id: "maintenance-fail", status: "failed"},
	} {
		record, err := store.GetCommand(t.Context(), command.id)
		if err != nil || record.Status != command.status {
			t.Fatalf("committed command %s=%+v err=%v", command.id, record, err)
		}
	}
	if session, found, err := store.GetConversationSession(t.Context(), "conversation-maintenance", "fake"); err != nil || !found || session != "native-maintenance" {
		t.Fatalf("committed conversation session=%q found=%t err=%v", session, found, err)
	}
	assertEventOutboxUsage(t, store, 0)

	store.durableMu.Lock()
	store.retention.maintenanceHook = nil
	store.durableMu.Unlock()
	if _, claimed, err := store.ClaimCommand(t.Context(), Command{
		MessageID: "maintenance-retry", Name: "probe", Payload: json.RawMessage(`{}`),
	}); err != nil || !claimed {
		t.Fatalf("maintenance retry trigger: claimed=%t err=%v", claimed, err)
	}
	store.durableMu.Lock()
	pending := store.retention.maintenancePending
	store.durableMu.Unlock()
	if pending {
		t.Fatal("successful later maintenance did not clear the retry marker")
	}
}

func TestBlankDurableMetadataIsRejectedConsistently(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.ClaimCommand(t.Context(), Command{MessageID: "   ", Name: "probe"}); err == nil {
		t.Fatal("blank command ID was accepted")
	}
	if _, _, err := store.ClaimCommand(t.Context(), Command{MessageID: "command", Name: "\t"}); err == nil {
		t.Fatal("blank command type was accepted")
	}
	if err := store.CompleteCommand(t.Context(), " ", nil); err == nil {
		t.Fatal("blank completion ID was accepted")
	}
	if err := store.FailCommand(t.Context(), "command", " ", nil); err == nil {
		t.Fatal("blank failure code was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), " ", 1, adapter.Event{Type: "provider.event"}); err == nil {
		t.Fatal("blank event run ID was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), "run", 1, adapter.Event{Type: "\n"}); err == nil {
		t.Fatal("blank event type was accepted")
	}
	if _, err := store.AppendEvent(t.Context(), "run", 1, adapter.Event{ID: "  ", Type: "provider.event"}); err == nil {
		t.Fatal("blank event ID was accepted")
	}
	if err := store.AckEventsThrough(t.Context(), []EventCursor{{RunID: "\t", LeaseEpoch: 1, Sequence: 1}}); err == nil {
		t.Fatal("blank ACK run ID was accepted")
	}
	if err := store.SaveNativeSession(t.Context(), "run", " ", "native"); err == nil {
		t.Fatal("blank native provider was accepted")
	}
	if err := store.SaveConversationSession(t.Context(), " ", "fake", "native"); err == nil {
		t.Fatal("blank conversation ID was accepted")
	}
	if _, _, err := store.GetConversationSession(t.Context(), "conversation", " "); err == nil {
		t.Fatal("blank conversation provider lookup was accepted")
	}
	if _, err := store.RegisterResource(t.Context(), " "); err == nil {
		t.Fatal("blank resource path was accepted")
	}
	if _, err := store.ResolveResource(t.Context(), " "); err == nil {
		t.Fatal("blank resource ID was accepted")
	}
	if err := store.SaveRunGitSnapshot(t.Context(), " ", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: "repo", Phase: "start",
	}); err == nil {
		t.Fatal("blank snapshot run ID was accepted")
	}
	if err := store.SaveRunGitSnapshot(t.Context(), "run", nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: " ", Phase: "start",
	}); err == nil {
		t.Fatal("blank snapshot repository ID was accepted")
	}
}
