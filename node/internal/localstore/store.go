package localstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	_ "modernc.org/sqlite"
)

type Store struct {
	db           *sql.DB
	databasePath string
	durableMu    sync.Mutex
	retention    durableRetentionState
	inboxLimits  inboxAdmissionLimits
	outboxLimits eventOutboxLimits
}

type Command struct {
	MessageID string
	Name      string
	Payload   json.RawMessage
}

type CommandRecord struct {
	MessageID  string
	Name       string
	Status     string
	Result     json.RawMessage
	ErrorCode  string
	ReceivedAt time.Time
	FinishedAt *time.Time
}

type EventCursor struct {
	RunID      string
	LeaseEpoch int64
	Sequence   int64
}

const (
	// A single durable event uses the protocol's encoded event/payload limits.
	// Batches may contain up to 128 such events, but are split by their exact
	// encoded frame payload before transport.
	maxDurableEventBytes        = nodeprotocol.MaxRunEventBytes
	maxDurableEventPayloadBytes = nodeprotocol.MaxRunEventPayloadBytes
	maxEventIDBytes             = 2 << 10
	maxEventRunIDBytes          = 1024
	maxEventTypeBytes           = 256
	maxEventTimeBytes           = 64
	maxSessionRunIDBytes        = 1024
	maxSessionKeyBytes          = 16 << 10
	maxSessionIDBytes           = 4 << 10
	maxProviderBytes            = 64
	maxCommandIDBytes           = 4 << 10
	maxCommandNameBytes         = 256
	maxCommandErrorCodeBytes    = 256
	maxUnackedWireJSONBytes     = 8 << 20
	runEventBatchJSONOverhead   = 64
	maxPoisonPayloadBytes       = 4 << 10
	commandResultTooLargeCode   = "command_result_too_large"
	commandResultTooLargeJSON   = `{"error":"command result exceeds transport size limit"}`
	commandFailureTooLargeJSON  = `{"error":"command failure detail exceeds transport size limit"}`
)

var ErrCommandConflict = errors.New("durable command message ID conflicts with persisted command")

// sqliteFileURL keeps Windows drive letters in the URI path, never the authority.
func sqliteFileURL(path string) *url.URL {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return &url.URL{Scheme: "file", Path: slashed}
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("node sqlite path is required")
	}
	absolutePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("resolve node sqlite path: %w", err)
	}
	if existing, statErr := os.Lstat(absolutePath); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return nil, errors.New("node sqlite path is not a safe regular file")
		}
		location := sqliteFileURL(absolutePath)
		location.RawQuery = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
		readOnly, err := sql.Open("sqlite", location.String())
		if err != nil {
			return nil, err
		}
		probe := &Store{db: readOnly}
		checkErr := probe.quickCheck(ctx)
		if checkErr == nil {
			checkErr = runtimeBaseline.Check(ctx, readOnly)
		}
		_ = readOnly.Close()
		if checkErr != nil {
			return nil, checkErr
		}
		current, err := os.Lstat(absolutePath)
		if err != nil || !os.SameFile(existing, current) {
			return nil, errors.New("runtime database changed during read-only validation")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect node sqlite path: %w", statErr)
	}
	databaseURL := sqliteFileURL(absolutePath).String()
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "journal_mode(WAL)")
	parameters.Add("_pragma", "foreign_keys(1)")
	db, err := sql.Open("sqlite", databaseURL+"?"+parameters.Encode())
	if err != nil {
		return nil, fmt.Errorf("open node sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping node sqlite: %w", err)
	}
	created, statErr := os.Lstat(absolutePath)
	if statErr != nil || created.Mode()&os.ModeSymlink != 0 || !created.Mode().IsRegular() {
		db.Close()
		return nil, errors.New("opened node sqlite path is not a safe regular file")
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure node sqlite permissions: %w", err)
	}
	store := &Store{
		db: db, databasePath: absolutePath, inboxLimits: defaultInboxAdmissionLimits(), outboxLimits: defaultEventOutboxLimits(),
	}
	if err := store.quickCheck(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := runtimeBaseline.Initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.recoverCurrentStore(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// Recovery and retention both write run_git_snapshots. Validate every
	// existing bounded payload before either path can synthesize or prune rows.
	if err := store.validateRunGitSnapshots(ctx); err != nil {
		db.Close()
		return nil, err
	}
	recoveryContext, cancelRecovery := context.WithTimeout(context.Background(), 30*time.Second)
	if err := store.completeInterruptedRunGitSnapshots(recoveryContext, time.Now().UTC()); err != nil {
		cancelRecovery()
		db.Close()
		return nil, fmt.Errorf("complete interrupted run Git snapshots: %w", err)
	}
	cancelRecovery()
	if err := store.validateRunGitSnapshots(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.pruneDurableState(ctx, time.Now().UTC(), defaultDurableRetentionPolicy()); err != nil {
		db.Close()
		return nil, fmt.Errorf("prune node durable state: %w", err)
	}
	if err := store.initializeDurableRetention(ctx, time.Now().UTC()); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize node durable retention: %w", err)
	}
	return store, nil
}

// ResourceAuthorizationDirectory is private local state, separate from SQL
// baselines, retention-managed command/session rows, and user repositories.
// The Git service creates it only for an explicit resource validation approval.
func (s *Store) ResourceAuthorizationDirectory() string {
	if s == nil || s.databasePath == "" {
		return "" // A store without a durable location cannot anchor authorization.
	}
	return filepath.Join(filepath.Dir(s.databasePath), "resource-authorizations")
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) recoverCurrentStore(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin runtime recovery: %w", err)
	}
	defer tx.Rollback()
	// Validate current data before recovery or retention writes. No schema
	// updates and no metadata backfills are performed on reopen.
	if err := validateRunGitSnapshotsQuery(ctx, tx); err != nil {
		return err
	}
	if err := validateEventWireBytes(ctx, tx); err != nil {
		return fmt.Errorf("validate event wire bytes: %w", err)
	}
	if err := validateEventOutboxMetadata(ctx, tx); err != nil {
		return fmt.Errorf("validate event outbox metadata: %w", err)
	}
	if err := validateEventSequenceCursors(ctx, tx); err != nil {
		return fmt.Errorf("validate event sequence cursors: %w", err)
	}
	postStatements := []string{
		`UPDATE inbox_commands
			SET payload = CAST(json_remove(CAST(payload AS TEXT),
				'$.payload.attachments[0].data_url', '$.payload.attachments[1].data_url',
				'$.payload.attachments[2].data_url', '$.payload.attachments[3].data_url') AS BLOB)
			WHERE status = 'processing'
			  AND name IN ('run.start', 'run.steer')
			  AND json_valid(payload)
			  AND (json_type(CAST(payload AS TEXT), '$.payload.attachments[0].data_url') IS NOT NULL
			    OR json_type(CAST(payload AS TEXT), '$.payload.attachments[1].data_url') IS NOT NULL
			    OR json_type(CAST(payload AS TEXT), '$.payload.attachments[2].data_url') IS NOT NULL
			    OR json_type(CAST(payload AS TEXT), '$.payload.attachments[3].data_url') IS NOT NULL)`,
		`UPDATE inbox_commands SET status = 'uncertain', error_code = 'node_restarted_during_command',
			finished_at = COALESCE(finished_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			WHERE status = 'processing'`,
	}
	for _, statement := range postStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("recover current runtime: %w", err)
		}
	}
	if err := validateRetentionMetadata(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runtime recovery: %w", err)
	}
	return nil
}

func (s *Store) RegisterResource(_ context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("local resource path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve resource path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat resource path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("local resource must be a directory")
	}
	return canonical, nil
}

func (s *Store) ResolveResource(ctx context.Context, id string) (string, error) {
	if !filepath.IsAbs(id) {
		return "", errors.New("local resource must be an absolute directory path")
	}
	return s.RegisterResource(ctx, id)
}

func (s *Store) ClaimCommand(ctx context.Context, command Command) (CommandRecord, bool, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(command.MessageID) == "" || len(command.MessageID) > maxCommandIDBytes {
		return CommandRecord{}, false, errors.New("command metadata is invalid")
	}
	record, storedPayload, found, err := s.lookupCommand(ctx, command.MessageID)
	if err != nil {
		return CommandRecord{}, false, err
	}
	if found {
		if record.Name != command.Name || !bytes.Equal(storedPayload, command.Payload) {
			return CommandRecord{}, false, ErrCommandConflict
		}
		if err := s.noteDurableActivityLocked(durableUsageDelta{}, false); err != nil {
			return CommandRecord{}, false, err
		}
		return record, false, nil
	}
	if strings.TrimSpace(command.Name) == "" || len(command.Name) > maxCommandNameBytes {
		return CommandRecord{}, false, errors.New("command metadata is invalid")
	}
	if err := s.ensureInboxAdmission(ctx, command); err != nil {
		return CommandRecord{}, false, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO inbox_commands (
			message_id, name, payload, status, received_at
		) VALUES (?, ?, ?, 'processing', ?)
	`, command.MessageID, command.Name, []byte(command.Payload), now.Format(time.RFC3339Nano))
	if err != nil {
		return CommandRecord{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return CommandRecord{}, false, err
	}
	record, storedPayload, found, err = s.lookupCommand(ctx, command.MessageID)
	if err != nil {
		return record, affected == 1, err
	}
	if !found {
		return CommandRecord{}, false, errors.New("claimed command was not persisted")
	}
	if record.Name != command.Name || !bytes.Equal(storedPayload, command.Payload) {
		return CommandRecord{}, false, ErrCommandConflict
	}
	if err := s.noteDurableActivityLocked(durableUsageDelta{}, false); err != nil {
		return CommandRecord{}, false, err
	}
	return record, affected == 1, nil
}

func (s *Store) CompleteCommand(ctx context.Context, messageID string, result any) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(messageID) == "" || len(messageID) > maxCommandIDBytes {
		return errors.New("command message ID is invalid")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	status := "completed"
	code := ""
	if err := validateCommandResultFrame(messageID, status, payload, code); err != nil {
		// Transition directly from processing to a bounded failure. There is no
		// intermediate completed write for retention or replay to observe.
		status = "failed"
		code = commandResultTooLargeCode
		payload = []byte(commandResultTooLargeJSON)
		if err := validateCommandResultFrame(messageID, status, payload, code); err != nil {
			return fmt.Errorf("encode bounded command result failure: %w", err)
		}
	}
	return s.persistCommandTerminalLocked(ctx, "complete", messageID, status, code, payload)
}

func (s *Store) FailCommand(ctx context.Context, messageID, code string, result any) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(messageID) == "" || len(messageID) > maxCommandIDBytes ||
		strings.TrimSpace(code) == "" || len(code) > maxCommandErrorCodeBytes {
		return errors.New("failed command metadata is invalid")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := validateCommandResultFrame(messageID, "failed", payload, code); err != nil {
		payload = []byte(commandFailureTooLargeJSON)
		if err := validateCommandResultFrame(messageID, "failed", payload, code); err != nil {
			return fmt.Errorf("encode bounded command failure result: %w", err)
		}
	}
	return s.persistCommandTerminalLocked(ctx, "fail", messageID, "failed", code, payload)
}

func validateCommandResultFrame(messageID, status string, result []byte, errorCode string) error {
	_, err := nodeprotocol.EncodeCommandResult(nodeprotocol.CommandResult{
		CommandID: messageID,
		Status:    status,
		Result:    json.RawMessage(result),
		ErrorCode: errorCode,
	})
	return err
}

func (s *Store) persistCommandTerminalLocked(
	ctx context.Context,
	operation, messageID, status, errorCode string,
	result []byte,
) error {
	var nullableErrorCode any
	if errorCode != "" {
		nullableErrorCode = errorCode
	}
	var retainedBytes int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE inbox_commands
		SET status = ?, result = ?, error_code = ?, finished_at = ?
		WHERE message_id = ? AND status = 'processing'
		RETURNING length(CAST(message_id AS BLOB)) + length(CAST(name AS BLOB)) + length(payload) +
		          COALESCE(length(result), 0) + COALESCE(length(CAST(error_code AS BLOB)), 0)
	`, status, result, nullableErrorCode, time.Now().UTC().Format(time.RFC3339Nano), messageID).Scan(&retainedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s command: command is not processing", operation)
	}
	if err != nil {
		return err
	}
	return s.noteDurableWriteLocked(durableUsageDelta{inboxRows: 1, inboxBytes: retainedBytes})
}

func (s *Store) GetCommand(ctx context.Context, messageID string) (CommandRecord, error) {
	if strings.TrimSpace(messageID) == "" || len(messageID) > maxCommandIDBytes {
		return CommandRecord{}, errors.New("command message ID is invalid")
	}
	record, _, found, err := s.lookupCommand(ctx, messageID)
	if err != nil {
		return CommandRecord{}, err
	}
	if !found {
		return CommandRecord{}, fmt.Errorf("command %s not found", messageID)
	}
	return record, nil
}

func (s *Store) lookupCommand(ctx context.Context, messageID string) (CommandRecord, []byte, bool, error) {
	var record CommandRecord
	var commandPayload, resultPayload []byte
	var received string
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT message_id, name, payload, status, COALESCE(result, X''), COALESCE(error_code, ''),
		       received_at, finished_at
		FROM inbox_commands WHERE message_id = ?
	`, messageID).Scan(
		&record.MessageID, &record.Name, &commandPayload, &record.Status, &resultPayload, &record.ErrorCode,
		&received, &finished,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, nil, false, nil
	}
	if err != nil {
		return CommandRecord{}, nil, false, err
	}
	record.Result = json.RawMessage(resultPayload)
	record.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return CommandRecord{}, nil, false, err
	}
	if finished.Valid {
		value, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return CommandRecord{}, nil, false, err
		}
		record.FinishedAt = &value
	}
	return record, commandPayload, true, nil
}

func (s *Store) AppendEvent(ctx context.Context, runID string, leaseEpoch int64, event adapter.Event) (adapter.Event, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(runID) == "" || len(runID) > maxEventRunIDBytes || leaseEpoch <= 0 || event.Sequence < 0 ||
		strings.TrimSpace(event.Type) == "" || len(event.Type) > maxEventTypeBytes ||
		len(event.ID) > maxEventIDBytes || (event.ID != "" && strings.TrimSpace(event.ID) == "") {
		return adapter.Event{}, errors.New("durable event metadata is invalid")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return adapter.Event{}, err
	}
	defer tx.Rollback()
	event.Sequence, err = advanceEventSequenceCursor(ctx, tx, runID, leaseEpoch, event.Sequence)
	if err != nil {
		return adapter.Event{}, err
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("evt_%s_%d_%d", runID, leaseEpoch, event.Sequence)
	}
	if len(event.ID) > maxEventIDBytes {
		return adapter.Event{}, errors.New("durable event ID exceeds limit")
	}
	event.RunID = runID
	event.LeaseEpoch = leaseEpoch
	wireEvent := nodeprotocol.RunEvent{
		ID: event.ID, RunID: runID, LeaseEpoch: leaseEpoch, Sequence: event.Sequence,
		Type: event.Type, Payload: event.Payload, OccurredAt: event.OccurredAt,
	}
	encodedEvent, payload, encodeErr := nodeprotocol.EncodeRunEvent(wireEvent)
	if encodeErr != nil {
		return adapter.Event{}, encodeErr
	}
	if err := s.ensureEventOutboxCapacity(ctx, tx, runID, leaseEpoch,
		int64(len(encodedEvent)), isTerminalEventType(event.Type)); err != nil {
		return adapter.Event{}, err
	}
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO event_log (
			id, run_id, lease_epoch, sequence, event_type, payload, wire_bytes, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, lease_epoch, sequence) DO NOTHING
	`, event.ID, runID, leaseEpoch, event.Sequence, event.Type, payload, len(encodedEvent),
		event.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		return adapter.Event{}, err
	}
	rowsAffected, err := inserted.RowsAffected()
	if err != nil {
		return adapter.Event{}, err
	}
	if rowsAffected != 1 {
		return adapter.Event{}, errors.New("durable event sequence already exists")
	}
	if err := tx.Commit(); err != nil {
		return adapter.Event{}, err
	}
	if err := s.noteDurableActivityLocked(durableUsageDelta{}, false); err != nil {
		return adapter.Event{}, err
	}
	return event, nil
}

func (s *Store) UnackedEvents(ctx context.Context, limit int) ([]adapter.Event, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if limit <= 0 || limit > nodeprotocol.MaxRunEventsPerBatch {
		return nil, fmt.Errorf("unacked event limit must be between 1 and %d", nodeprotocol.MaxRunEventsPerBatch)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT rowid, length(CAST(id AS BLOB)), substr(id, 1, ?),
		       length(CAST(run_id AS BLOB)), substr(run_id, 1, ?),
		       lease_epoch, sequence, length(CAST(event_type AS BLOB)), substr(event_type, 1, ?),
		       length(payload), wire_bytes, length(CAST(occurred_at AS BLOB)), substr(occurred_at, 1, ?)
		FROM event_log WHERE acked_at IS NULL
		ORDER BY rowid LIMIT ?
	`, maxEventIDBytes+1, maxEventRunIDBytes+1, maxEventTypeBytes+1, maxEventTimeBytes+1, limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		rowID        int64
		event        adapter.Event
		payload      []byte
		payloadBytes int64
		wireBytes    int64
		poisonReason string
	}
	candidates := make([]candidate, 0, limit)
	cumulative := int64(runEventBatchJSONOverhead)
	type streamKey struct {
		runID      string
		leaseEpoch int64
	}
	lastSequence := make(map[streamKey]int64)
	for rows.Next() {
		var item candidate
		var occurred string
		var idBytes, runIDBytes, typeBytes, occurredBytes int64
		if err := rows.Scan(&item.rowID, &idBytes, &item.event.ID, &runIDBytes, &item.event.RunID,
			&item.event.LeaseEpoch, &item.event.Sequence, &typeBytes, &item.event.Type,
			&item.payloadBytes, &item.wireBytes, &occurredBytes, &occurred); err != nil {
			rows.Close()
			return nil, err
		}
		if runIDBytes != int64(len(item.event.RunID)) || runIDBytes <= 0 || runIDBytes > maxEventRunIDBytes ||
			strings.TrimSpace(item.event.RunID) == "" ||
			item.event.LeaseEpoch <= 0 || item.event.Sequence <= 0 {
			rows.Close()
			return nil, errors.New("corrupt durable event cursor metadata")
		}
		key := streamKey{runID: item.event.RunID, leaseEpoch: item.event.LeaseEpoch}
		if previous, ok := lastSequence[key]; ok && item.event.Sequence != previous+1 {
			rows.Close()
			return nil, errors.New("durable event row order does not preserve a sequence prefix")
		}
		lastSequence[key] = item.event.Sequence
		if idBytes != int64(len(item.event.ID)) || idBytes <= 0 || idBytes > maxEventIDBytes ||
			strings.TrimSpace(item.event.ID) == "" {
			item.poisonReason = "invalid_event_id"
		}
		if typeBytes != int64(len(item.event.Type)) || typeBytes <= 0 || typeBytes > maxEventTypeBytes ||
			strings.TrimSpace(item.event.Type) == "" {
			if item.poisonReason == "" {
				item.poisonReason = "invalid_event_type"
			}
		}
		item.event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if occurredBytes != int64(len(occurred)) || occurredBytes <= 0 || occurredBytes > maxEventTimeBytes || err != nil {
			if item.poisonReason == "" {
				item.poisonReason = "invalid_occurred_at"
			}
			item.event.OccurredAt = time.Time{}
		}
		if item.payloadBytes < 0 || item.payloadBytes > maxDurableEventPayloadBytes || item.wireBytes <= 0 ||
			item.wireBytes > nodeprotocol.MaxRunEventBytes {
			if item.poisonReason == "" {
				item.poisonReason = "oversized_payload"
			}
		}
		conservativeBytes := item.payloadBytes + 6*(idBytes+runIDBytes+typeBytes+occurredBytes) + 512
		if conservativeBytes > item.wireBytes {
			item.wireBytes = conservativeBytes
		}
		accountedBytes := item.wireBytes + 1
		if item.poisonReason != "" {
			item.event = poisonReplacementEvent(item.event, item.poisonReason)
			encoded, _, encodeErr := nodeprotocol.EncodeRunEvent(protocolEvent(item.event))
			if encodeErr != nil {
				rows.Close()
				return nil, encodeErr
			}
			accountedBytes = int64(len(encoded) + 1)
		}
		if cumulative+accountedBytes > maxUnackedWireJSONBytes {
			if len(candidates) > 0 {
				break
			}
			item.poisonReason = "wire_budget_exceeded"
			item.event = poisonReplacementEvent(item.event, item.poisonReason)
			encoded, _, encodeErr := nodeprotocol.EncodeRunEvent(protocolEvent(item.event))
			if encodeErr != nil || int64(len(encoded))+cumulative > maxUnackedWireJSONBytes {
				rows.Close()
				return nil, errors.New("durable poison diagnostic exceeds wire budget")
			}
			accountedBytes = int64(len(encoded) + 1)
		}
		cumulative += accountedBytes
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Load only the wire-bounded prefix, reject malformed/trailing JSON before
	// decoding, then stream every valid payload through one UseNumber decoder.
	// A decoder cannot be reset safely after EOF; one concatenated JSON stream
	// both preserves integers and reuses its bounded token buffer.
	payloadReaders := make([]io.Reader, 0, len(candidates)*2)
	for index := range candidates {
		item := &candidates[index]
		if item.poisonReason == "" {
			var payload []byte
			var storedBytes int64
			if err := s.db.QueryRowContext(ctx, `
				SELECT length(payload), substr(payload, 1, ?) FROM event_log
				WHERE rowid = ? AND acked_at IS NULL
			`, maxDurableEventPayloadBytes+1, item.rowID).Scan(&storedBytes, &payload); err != nil {
				return nil, err
			}
			if storedBytes != int64(len(payload)) || storedBytes > maxDurableEventPayloadBytes || !json.Valid(payload) {
				item.poisonReason = "invalid_payload"
				item.event = poisonReplacementEvent(item.event, item.poisonReason)
				continue
			}
			item.payload = payload
			payloadReaders = append(payloadReaders, bytes.NewReader(payload), strings.NewReader("\n"))
		}
	}
	decoder := json.NewDecoder(io.MultiReader(payloadReaders...))
	decoder.UseNumber()
	for index := range candidates {
		item := &candidates[index]
		if item.payload == nil {
			continue
		}
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode validated durable event payload: %w", err)
		}
		switch value := decoded.(type) {
		case map[string]any:
			item.event.Payload = value
		case nil:
			item.event.Payload = nil
		default:
			item.event = poisonReplacementEvent(item.event, "invalid_payload")
		}
		item.payload = nil
		if err := validatePersistedDiagnostic(item.event); err != nil {
			return nil, err
		}
	}

	events := make([]adapter.Event, 0, len(candidates))
	actualBytes := int64(runEventBatchJSONOverhead)
	for _, item := range candidates {
		encoded, _, encodeErr := nodeprotocol.EncodeRunEvent(protocolEvent(item.event))
		if encodeErr != nil || actualBytes+int64(len(encoded)+1) > maxUnackedWireJSONBytes {
			item.event = poisonReplacementEvent(item.event, "wire_budget_exceeded")
			encoded, _, encodeErr = nodeprotocol.EncodeRunEvent(protocolEvent(item.event))
			if encodeErr != nil || actualBytes+int64(len(encoded)+1) > maxUnackedWireJSONBytes {
				return nil, errors.New("durable event cannot fit bounded diagnostic batch")
			}
		}
		actualBytes += int64(len(encoded) + 1)
		events = append(events, item.event)
	}
	return events, nil
}

// Reject an unsupported diagnostic shape; never reinterpret or upgrade it.
func validatePersistedDiagnostic(event adapter.Event) error {
	if event.Payload["code"] != "durable_event_poisoned" || event.Payload["reason"] != "durable_event_poisoned" {
		return nil
	}
	if event.Type == "node.diagnostic" || (event.Type == "run.finished" && event.Payload["status"] == "failed") {
		return nil
	}
	return errors.New("unsupported durable diagnostic format")
}

func protocolEvent(event adapter.Event) nodeprotocol.RunEvent {
	return nodeprotocol.RunEvent{
		ID: event.ID, RunID: event.RunID, LeaseEpoch: event.LeaseEpoch, Sequence: event.Sequence,
		Type: event.Type, Payload: event.Payload, OccurredAt: event.OccurredAt,
	}
}

func poisonReplacementEvent(event adapter.Event, poisonReason string) adapter.Event {
	originalType := event.Type
	event.ID = poisonEventID(event.RunID, event.LeaseEpoch, event.Sequence)
	poisonReason = strings.TrimSpace(poisonReason)
	if poisonReason == "" {
		poisonReason = "unknown"
	}
	if len(poisonReason) > maxEventTypeBytes {
		poisonReason = strings.ToValidUTF8(poisonReason[:maxEventTypeBytes], "")
	}
	event.Payload = map[string]any{
		"code": "durable_event_poisoned", "reason": "durable_event_poisoned",
		"poison_reason": poisonReason, "original_event_type": originalType,
	}
	switch originalType {
	case "run.completed", "run.finished", "run.error":
		// No terminal outcome is trustworthy once its payload is poisoned.
		// Emit a bounded failed terminal so the server can still advance the Run
		// without manufacturing a successful completion.
		event.Type = "run.finished"
		event.Payload["status"] = "failed"
	default:
		event.Type = "node.diagnostic"
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = deterministicPoisonOccurredAt()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	return event
}

func poisonEventID(runID string, leaseEpoch, sequence int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", runID, leaseEpoch, sequence)))
	return fmt.Sprintf("evt_poison_%x", digest[:16])
}

func deterministicPoisonOccurredAt() time.Time {
	return time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// ReplaceEventWithDiagnostic atomically quarantines one server-rejected outbox
// row while preserving its run/lease/sequence cursor. Non-terminals become a
// diagnostic; terminal rows remain a bounded terminal outcome. No neighboring
// event is acknowledged or removed.
func (s *Store) ReplaceEventWithDiagnostic(ctx context.Context, event adapter.Event, reason string) (adapter.Event, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(event.RunID) == "" || len(event.RunID) > maxEventRunIDBytes ||
		event.LeaseEpoch <= 0 || event.Sequence <= 0 {
		return adapter.Event{}, errors.New("poison event cursor is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return adapter.Event{}, err
	}
	defer tx.Rollback()
	var storedID, originalType, occurredAt string
	var storedPayload []byte
	var storedIDBytes, originalTypeBytes, occurredAtBytes, storedPayloadBytes, oldWireBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT length(CAST(id AS BLOB)), substr(id, 1, ?),
		       length(CAST(event_type AS BLOB)), substr(event_type, 1, ?),
		       length(CAST(occurred_at AS BLOB)), substr(occurred_at, 1, ?),
		       length(payload), substr(payload, 1, ?), wire_bytes
		FROM event_log
		WHERE run_id = ? AND lease_epoch = ? AND sequence = ? AND acked_at IS NULL
	`, maxEventIDBytes+1, maxEventTypeBytes+1, maxEventTimeBytes+1, maxPoisonPayloadBytes+1,
		event.RunID, event.LeaseEpoch, event.Sequence).Scan(
		&storedIDBytes, &storedID, &originalTypeBytes, &originalType,
		&occurredAtBytes, &occurredAt, &storedPayloadBytes, &storedPayload, &oldWireBytes,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return adapter.Event{}, errors.New("poison event is no longer pending")
		}
		return adapter.Event{}, err
	}
	if persisted, ok := persistedPoisonEvent(event, storedIDBytes, storedID,
		originalTypeBytes, originalType, occurredAtBytes, occurredAt,
		storedPayloadBytes, storedPayload, oldWireBytes); ok {
		return persisted, validatePersistedDiagnostic(persisted)
	}
	if originalTypeBytes != int64(len(originalType)) || originalTypeBytes <= 0 || originalTypeBytes > maxEventTypeBytes {
		originalType = "unknown"
	}
	event.Type = originalType
	if occurredAtBytes == int64(len(occurredAt)) && occurredAtBytes > 0 && occurredAtBytes <= maxEventTimeBytes {
		parsed, parseErr := time.Parse(time.RFC3339Nano, occurredAt)
		if parseErr == nil {
			event.OccurredAt = parsed.UTC()
		} else {
			event.OccurredAt = time.Time{}
		}
	} else {
		event.OccurredAt = time.Time{}
	}
	diagnostic := poisonReplacementEvent(event, reason)
	encoded, payload, err := nodeprotocol.EncodeRunEvent(protocolEvent(diagnostic))
	if err != nil {
		return adapter.Event{}, err
	}
	if err := s.ensureEventReplacementCapacity(ctx, tx, oldWireBytes, int64(len(encoded))); err != nil {
		return adapter.Event{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE event_log
		SET id = ?, event_type = ?, payload = ?, wire_bytes = ?, occurred_at = ?
		WHERE run_id = ? AND lease_epoch = ? AND sequence = ? AND acked_at IS NULL
	`, diagnostic.ID, diagnostic.Type, payload, len(encoded), diagnostic.OccurredAt.Format(time.RFC3339Nano),
		diagnostic.RunID, diagnostic.LeaseEpoch, diagnostic.Sequence)
	if err != nil {
		return adapter.Event{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return adapter.Event{}, err
	}
	if updated != 1 {
		return adapter.Event{}, errors.New("poison event is no longer pending")
	}
	if err := tx.Commit(); err != nil {
		return adapter.Event{}, err
	}
	return diagnostic, nil
}

func persistedPoisonEvent(
	event adapter.Event,
	storedIDBytes int64,
	storedID string,
	typeBytes int64,
	eventType string,
	occurredAtBytes int64,
	occurredAt string,
	payloadBytes int64,
	payload []byte,
	wireBytes int64,
) (adapter.Event, bool) {
	if storedIDBytes != int64(len(storedID)) || storedID != poisonEventID(event.RunID, event.LeaseEpoch, event.Sequence) ||
		typeBytes != int64(len(eventType)) || typeBytes <= 0 || typeBytes > maxEventTypeBytes ||
		occurredAtBytes != int64(len(occurredAt)) || occurredAtBytes <= 0 || occurredAtBytes > maxEventTimeBytes ||
		payloadBytes != int64(len(payload)) || payloadBytes <= 0 || payloadBytes > maxPoisonPayloadBytes ||
		wireBytes <= 0 || wireBytes > maxDurableEventBytes {
		return adapter.Event{}, false
	}
	switch eventType {
	case "node.diagnostic", "run.finished":
	default:
		return adapter.Event{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil || occurredAt != parsed.UTC().Format(time.RFC3339Nano) {
		return adapter.Event{}, false
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil ||
		decoded["code"] != "durable_event_poisoned" || decoded["reason"] != "durable_event_poisoned" {
		return adapter.Event{}, false
	}
	if poisonReason, ok := decoded["poison_reason"].(string); !ok || strings.TrimSpace(poisonReason) == "" {
		return adapter.Event{}, false
	}
	if originalType, ok := decoded["original_event_type"].(string); !ok || originalType == "" {
		return adapter.Event{}, false
	}
	persisted := adapter.Event{
		ID: storedID, RunID: event.RunID, LeaseEpoch: event.LeaseEpoch, Sequence: event.Sequence,
		Type: eventType, Payload: decoded, OccurredAt: parsed.UTC(),
	}
	encoded, canonicalPayload, err := nodeprotocol.EncodeRunEvent(protocolEvent(persisted))
	if err != nil || int64(len(encoded)) != wireBytes || !bytes.Equal(canonicalPayload, payload) {
		return adapter.Event{}, false
	}
	return persisted, true
}

func (s *Store) AckEventsThrough(
	ctx context.Context,
	cursors []EventCursor,
) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if len(cursors) == 0 {
		return errors.New("acknowledgement cursors are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type cursorKey struct {
		runID      string
		leaseEpoch int64
	}
	seen := make(map[cursorKey]struct{}, len(cursors))
	for _, cursor := range cursors {
		if strings.TrimSpace(cursor.RunID) == "" || len(cursor.RunID) > maxEventRunIDBytes ||
			cursor.LeaseEpoch <= 0 || cursor.Sequence <= 0 {
			return errors.New("acknowledgement cursor is invalid")
		}
		key := cursorKey{runID: cursor.RunID, leaseEpoch: cursor.LeaseEpoch}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("acknowledgement contains duplicate stream cursors")
		}
		seen[key] = struct{}{}
		var hasFinalTerminal int
		// A provider run.error is priority but not the local stream's final
		// marker: Runtime appends a normalized run.finished after it. Cleaning
		// the cursor on run.error could make that follow-up reuse sequence 1.
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM event_log
				WHERE run_id = ? AND lease_epoch = ? AND sequence <= ?
				  AND acked_at IS NULL AND event_type IN ('run.completed', 'run.finished')
			)
		`, cursor.RunID, cursor.LeaseEpoch, cursor.Sequence).Scan(&hasFinalTerminal); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM event_log
			WHERE run_id = ? AND lease_epoch = ? AND sequence <= ?
		`, cursor.RunID, cursor.LeaseEpoch, cursor.Sequence); err != nil {
			return err
		}
		if hasFinalTerminal == 1 {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM event_sequence_cursors
				WHERE run_id = ? AND lease_epoch = ? AND last_sequence <= ?
				  AND NOT EXISTS (
					SELECT 1 FROM event_log
					WHERE run_id = ? AND lease_epoch = ?
				  )
			`, cursor.RunID, cursor.LeaseEpoch, cursor.Sequence,
				cursor.RunID, cursor.LeaseEpoch); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.noteDurableActivityLocked(durableUsageDelta{}, false)
}

func (s *Store) SaveNativeSession(ctx context.Context, runID, provider, nativeSessionID string) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(runID) == "" || len(runID) > maxSessionRunIDBytes ||
		strings.TrimSpace(provider) == "" || len(provider) > maxProviderBytes ||
		strings.TrimSpace(nativeSessionID) == "" || len(nativeSessionID) > maxSessionIDBytes {
		return errors.New("native session metadata is invalid")
	}
	var previousBytes int64
	rowsDelta := int64(0)
	lookupErr := s.db.QueryRowContext(ctx, `
		SELECT length(CAST(run_id AS BLOB)) + length(CAST(provider AS BLOB)) +
		       length(CAST(native_session_id AS BLOB))
		FROM native_sessions WHERE run_id = ?
	`, runID).Scan(&previousBytes)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		rowsDelta = 1
		previousBytes = 0
	} else if lookupErr != nil {
		return lookupErr
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO native_sessions (run_id, provider, native_session_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			provider = excluded.provider,
			native_session_id = excluded.native_session_id,
			updated_at = excluded.updated_at
	`, runID, provider, nativeSessionID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	retainedBytes := int64(len(runID) + len(provider) + len(nativeSessionID))
	return s.noteDurableWriteLocked(durableUsageDelta{
		nativeRows: rowsDelta, nativeBytes: retainedBytes - previousBytes,
	})
}

func (s *Store) SaveConversationSession(
	ctx context.Context,
	conversationID, provider, nativeSessionID string,
) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if strings.TrimSpace(conversationID) == "" || len(conversationID) > maxSessionKeyBytes ||
		strings.TrimSpace(provider) == "" || len(provider) > maxProviderBytes ||
		strings.TrimSpace(nativeSessionID) == "" || len(nativeSessionID) > maxSessionIDBytes {
		return errors.New("conversation session metadata is invalid")
	}
	var previousBytes int64
	rowsDelta := int64(0)
	lookupErr := s.db.QueryRowContext(ctx, `
		SELECT length(CAST(conversation_id AS BLOB)) + length(CAST(provider AS BLOB)) +
		       length(CAST(native_session_id AS BLOB))
		FROM conversation_sessions WHERE conversation_id = ? AND provider = ?
	`, conversationID, provider).Scan(&previousBytes)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		rowsDelta = 1
		previousBytes = 0
	} else if lookupErr != nil {
		return lookupErr
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_sessions (
			conversation_id, provider, native_session_id, updated_at
		)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(conversation_id, provider) DO UPDATE SET
			native_session_id = excluded.native_session_id,
			updated_at = excluded.updated_at
	`, conversationID, provider, nativeSessionID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	retainedBytes := int64(len(conversationID) + len(provider) + len(nativeSessionID))
	return s.noteDurableWriteLocked(durableUsageDelta{
		conversationRows: rowsDelta, conversationBytes: retainedBytes - previousBytes,
	})
}

func (s *Store) GetConversationSession(
	ctx context.Context,
	conversationID, provider string,
) (string, bool, error) {
	if strings.TrimSpace(conversationID) == "" || len(conversationID) > maxSessionKeyBytes ||
		strings.TrimSpace(provider) == "" || len(provider) > maxProviderBytes {
		return "", false, errors.New("conversation session lookup is invalid")
	}
	var nativeSessionID string
	err := s.db.QueryRowContext(ctx, `
		SELECT native_session_id
		FROM conversation_sessions
		WHERE conversation_id = ? AND provider = ?
	`, conversationID, provider).Scan(&nativeSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return nativeSessionID, err == nil, err
}
