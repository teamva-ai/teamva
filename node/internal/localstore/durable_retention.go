package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxRetainedInboxCommands = 10_000
	maxRetainedInboxBytes    = 64 << 20
	inboxRetentionAge        = 30 * 24 * time.Hour

	maxRetainedSessions     = 10_000
	maxRetainedSessionBytes = 32 << 20
	sessionRetentionAge     = 180 * 24 * time.Hour

	durablePruneWriteInterval = 64
	durablePruneMinInterval   = time.Minute
	durablePruneBatchSize     = 256
	durablePruneTimeout       = 15 * time.Second
)

type durableRetentionPolicy struct {
	inboxCount    int
	inboxBytes    int64
	inboxAge      time.Duration
	sessionCount  int
	sessionBytes  int64
	sessionAge    time.Duration
	writeInterval int
	minInterval   time.Duration
}

func defaultDurableRetentionPolicy() durableRetentionPolicy {
	return durableRetentionPolicy{
		inboxCount: maxRetainedInboxCommands, inboxBytes: maxRetainedInboxBytes, inboxAge: inboxRetentionAge,
		sessionCount: maxRetainedSessions, sessionBytes: maxRetainedSessionBytes, sessionAge: sessionRetentionAge,
		writeInterval: durablePruneWriteInterval, minInterval: durablePruneMinInterval,
	}
}

type durableUsage struct {
	inboxRows         int64
	inboxBytes        int64
	nativeRows        int64
	nativeBytes       int64
	conversationRows  int64
	conversationBytes int64
}

type durableUsageDelta struct {
	inboxRows         int64
	inboxBytes        int64
	nativeRows        int64
	nativeBytes       int64
	conversationRows  int64
	conversationBytes int64
}

type durableRetentionState struct {
	policy             durableRetentionPolicy
	usage              durableUsage
	writes             int
	lastPrune          time.Time
	maintenancePending bool
	maintenanceHook    func() error
}

func validateDurableRetentionPolicy(policy durableRetentionPolicy) error {
	if policy.inboxCount < 0 || policy.inboxBytes < 0 || policy.inboxAge < 0 ||
		policy.sessionCount < 0 || policy.sessionBytes < 0 || policy.sessionAge < 0 ||
		policy.writeInterval < 0 || policy.minInterval < 0 {
		return errors.New("invalid durable retention policy")
	}
	return nil
}

func (s *Store) initializeDurableRetention(ctx context.Context, now time.Time) error {
	s.retention.policy = defaultDurableRetentionPolicy()
	usage, err := s.loadDurableUsage(ctx)
	if err != nil {
		return err
	}
	s.retention.usage = usage
	s.retention.lastPrune = now
	s.retention.writes = 0
	s.retention.maintenancePending = false
	return nil
}

// noteDurableWriteLocked is called while Store.durableMu is held and only
// after the corresponding SQLite write committed. Cheap in-memory upper-bound
// accounting triggers pruning exactly at a hard count/byte edge; otherwise
// pruning is amortized by write count or elapsed time.
func (s *Store) noteDurableWriteLocked(delta durableUsageDelta) error {
	return s.noteDurableActivityLocked(delta, true)
}

// noteDurableActivityLocked lets unrelated outbox/claim activity apply elapsed
// age retention without making high-volume event streaming hit the write-count
// cadence. Only writes that change retained inbox/session usage advance it.
func (s *Store) noteDurableActivityLocked(delta durableUsageDelta, countWrite bool) error {
	state := &s.retention
	state.usage.inboxRows += delta.inboxRows
	state.usage.inboxBytes += delta.inboxBytes
	state.usage.nativeRows += delta.nativeRows
	state.usage.nativeBytes += delta.nativeBytes
	state.usage.conversationRows += delta.conversationRows
	state.usage.conversationBytes += delta.conversationBytes
	if countWrite {
		state.writes++
	}
	// A previous maintenance attempt may have committed part of its own work
	// before a later read failed. Recalibrate before retrying so stale in-memory
	// counters cannot drive an unsafe follow-up prune.
	if state.maintenancePending {
		recalibrateContext, cancel := context.WithTimeout(context.Background(), durablePruneTimeout)
		usage, err := s.loadDurableUsage(recalibrateContext)
		cancel()
		if err != nil {
			return s.deferDurableMaintenanceLocked()
		}
		state.usage = usage
	}
	policy := state.policy
	if err := validateDurableRetentionPolicy(policy); err != nil {
		return s.deferDurableMaintenanceLocked()
	}
	hardLimit := state.usage.inboxRows > int64(policy.inboxCount) ||
		state.usage.inboxBytes > policy.inboxBytes ||
		state.usage.nativeRows > int64(policy.sessionCount) ||
		state.usage.nativeBytes > policy.sessionBytes ||
		state.usage.conversationRows > int64(policy.sessionCount) ||
		state.usage.conversationBytes > policy.sessionBytes
	writeDue := policy.writeInterval > 0 && state.writes >= policy.writeInterval
	timeDue := policy.minInterval > 0 && time.Since(state.lastPrune) >= policy.minInterval
	if !state.maintenancePending && !hardLimit && !writeDue && !timeDue {
		return nil
	}
	if state.maintenanceHook != nil {
		if err := state.maintenanceHook(); err != nil {
			return s.deferDurableMaintenanceLocked()
		}
	}
	pruneContext, cancel := context.WithTimeout(context.Background(), durablePruneTimeout)
	defer cancel()
	if hardLimit && !writeDue && !timeDue {
		usage, err := s.pruneDurableHardLimits(pruneContext, state.usage, policy)
		if err != nil {
			return s.deferDurableMaintenanceLocked()
		}
		state.usage = usage
		state.maintenancePending = false
		return nil
	}
	now := time.Now().UTC()
	if err := s.pruneDurableState(pruneContext, now, policy); err != nil {
		return s.deferDurableMaintenanceLocked()
	}
	usage, err := s.loadDurableUsage(pruneContext)
	if err != nil {
		return s.deferDurableMaintenanceLocked()
	}
	state.usage = usage
	state.writes = 0
	state.lastPrune = now
	state.maintenancePending = false
	return nil
}

// Retention is independent maintenance. Callers reach this helper only after
// their primary SQLite write committed, so a failure is retained for a forced
// retry and deliberately not surfaced as failure of that write.
func (s *Store) deferDurableMaintenanceLocked() error {
	s.retention.maintenancePending = true
	return nil
}

// pruneDurableHardLimits is the steady-state fast path. Once a table reaches a
// hard bound, each subsequent write removes only the minimum oldest SQL prefix
// instead of rescanning every retained row. The periodic path still recalibrates
// counters and applies age retention.
func (s *Store) pruneDurableHardLimits(
	ctx context.Context, usage durableUsage, policy durableRetentionPolicy,
) (durableUsage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return durableUsage{}, err
	}
	defer tx.Rollback()
	if err := trimOldestDurableRows(ctx, tx, `
		SELECT rowid, length(CAST(message_id AS BLOB)) + length(CAST(name AS BLOB)) + length(payload) +
		       COALESCE(length(result), 0) + COALESCE(length(CAST(error_code AS BLOB)), 0)
		FROM inbox_commands WHERE status IN ('completed', 'failed')
		ORDER BY finished_at, message_id LIMIT ?
	`, `DELETE FROM inbox_commands WHERE status IN ('completed', 'failed') AND rowid IN (%s)`,
		&usage.inboxRows, &usage.inboxBytes, int64(policy.inboxCount), policy.inboxBytes); err != nil {
		return durableUsage{}, err
	}
	if err := trimOldestDurableRows(ctx, tx, `
		SELECT rowid, length(CAST(run_id AS BLOB)) + length(CAST(provider AS BLOB)) +
		       length(CAST(native_session_id AS BLOB))
		FROM native_sessions ORDER BY updated_at, run_id LIMIT ?
	`, `DELETE FROM native_sessions WHERE rowid IN (%s)`,
		&usage.nativeRows, &usage.nativeBytes, int64(policy.sessionCount), policy.sessionBytes); err != nil {
		return durableUsage{}, err
	}
	if err := trimOldestDurableRows(ctx, tx, `
		SELECT rowid, length(CAST(conversation_id AS BLOB)) + length(CAST(provider AS BLOB)) +
		       length(CAST(native_session_id AS BLOB))
		FROM conversation_sessions ORDER BY updated_at, conversation_id, provider LIMIT ?
	`, `DELETE FROM conversation_sessions WHERE rowid IN (%s)`,
		&usage.conversationRows, &usage.conversationBytes,
		int64(policy.sessionCount), policy.sessionBytes); err != nil {
		return durableUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return durableUsage{}, err
	}
	return usage, nil
}

func trimOldestDurableRows(
	ctx context.Context, tx *sql.Tx, selectQuery, deleteFormat string,
	rowsCount, bytesCount *int64, maxRows, maxBytes int64,
) error {
	for *rowsCount > maxRows || *bytesCount > maxBytes {
		rows, err := tx.QueryContext(ctx, selectQuery, durablePruneBatchSize)
		if err != nil {
			return err
		}
		type retainedRow struct {
			rowID int64
			size  int64
		}
		items := make([]retainedRow, 0, durablePruneBatchSize)
		for rows.Next() {
			var item retainedRow
			if err := rows.Scan(&item.rowID, &item.size); err != nil {
				rows.Close()
				return err
			}
			if item.rowID <= 0 || item.size < 0 {
				rows.Close()
				return errors.New("corrupt durable hard-limit metadata")
			}
			items = append(items, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(items) == 0 {
			return errors.New("durable hard-limit counters do not match SQLite")
		}
		removeCount := 0
		removedBytes := int64(0)
		for _, item := range items {
			removeCount++
			removedBytes += item.size
			if *rowsCount-int64(removeCount) <= maxRows && *bytesCount-removedBytes <= maxBytes {
				break
			}
		}
		arguments := make([]any, removeCount)
		placeholders := make([]string, removeCount)
		for index := 0; index < removeCount; index++ {
			arguments[index] = items[index].rowID
			placeholders[index] = "?"
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(deleteFormat, strings.Join(placeholders, ",")), arguments...)
		if err != nil {
			return err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed != int64(removeCount) {
			return errors.New("durable hard-limit delete was not atomic")
		}
		*rowsCount -= removed
		*bytesCount -= removedBytes
	}
	return nil
}

func (s *Store) loadDurableUsage(ctx context.Context) (durableUsage, error) {
	var usage durableUsage
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(
			length(CAST(message_id AS BLOB)) + length(CAST(name AS BLOB)) + length(payload) +
			COALESCE(length(result), 0) + COALESCE(length(CAST(error_code AS BLOB)), 0)
		), 0)
		FROM inbox_commands WHERE status IN ('completed', 'failed')
	`).Scan(&usage.inboxRows, &usage.inboxBytes); err != nil {
		return durableUsage{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(CAST(run_id AS BLOB)) + length(CAST(provider AS BLOB)) +
			length(CAST(native_session_id AS BLOB))), 0)
		FROM native_sessions
	`).Scan(&usage.nativeRows, &usage.nativeBytes); err != nil {
		return durableUsage{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(CAST(conversation_id AS BLOB)) + length(CAST(provider AS BLOB)) +
			length(CAST(native_session_id AS BLOB))), 0)
		FROM conversation_sessions
	`).Scan(&usage.conversationRows, &usage.conversationBytes); err != nil {
		return durableUsage{}, err
	}
	if usage.inboxRows < 0 || usage.inboxBytes < 0 || usage.nativeRows < 0 || usage.nativeBytes < 0 ||
		usage.conversationRows < 0 || usage.conversationBytes < 0 {
		return durableUsage{}, errors.New("corrupt durable usage metadata")
	}
	return usage, nil
}

// pruneDurableState uses bounded SQL batches and keyset scans. It never loads
// all keys/rows into Go memory and never sorts a complete table on every write.
func (s *Store) pruneDurableState(ctx context.Context, now time.Time, policy durableRetentionPolicy) error {
	if err := validateDurableRetentionPolicy(policy); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateRetentionMetadata(ctx, tx); err != nil {
		return err
	}
	// Older databases retained ACKed rows. Reclaim a cursor only when its exact
	// high-water row is a normal final terminal and no unacknowledged row remains
	// for that stream. Provider run.error is followed by normalized run.finished,
	// so it deliberately keeps the cursor. A live stream whose non-terminal
	// outbox is temporarily empty keeps its cursor as well.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM event_sequence_cursors AS cursor
		WHERE EXISTS (
			SELECT 1 FROM event_log AS terminal
			WHERE terminal.run_id = cursor.run_id
			  AND terminal.lease_epoch = cursor.lease_epoch
			  AND terminal.sequence = cursor.last_sequence
			  AND terminal.acked_at IS NOT NULL
			  AND terminal.event_type IN ('run.completed', 'run.finished')
		)
		AND NOT EXISTS (
			SELECT 1 FROM event_log AS pending
			WHERE pending.run_id = cursor.run_id
			  AND pending.lease_epoch = cursor.lease_epoch
			  AND pending.acked_at IS NULL
		)
	`); err != nil {
		return err
	}
	if err := deleteInBatches(ctx, tx, `
		DELETE FROM event_log WHERE rowid IN (
			SELECT rowid FROM event_log WHERE acked_at IS NOT NULL LIMIT ?
		)
	`, durablePruneBatchSize); err != nil {
		return err
	}
	if err := deleteInBatches(ctx, tx, `
		DELETE FROM inbox_commands WHERE rowid IN (
			SELECT rowid FROM inbox_commands
			WHERE status IN ('completed', 'failed')
			  AND julianday(finished_at) < julianday(?)
			ORDER BY finished_at, message_id LIMIT ?
		)
	`, now.Add(-policy.inboxAge).Format(time.RFC3339Nano), durablePruneBatchSize); err != nil {
		return err
	}
	if err := pruneInboxBoundary(ctx, tx, policy.inboxCount, policy.inboxBytes); err != nil {
		return err
	}
	for _, table := range []string{"native_sessions", "conversation_sessions"} {
		if err := deleteSessionAgeBatches(ctx, tx, table,
			now.Add(-policy.sessionAge).Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if err := pruneNativeSessionBoundary(ctx, tx, policy.sessionCount, policy.sessionBytes); err != nil {
		return err
	}
	if err := pruneConversationSessionBoundary(ctx, tx, policy.sessionCount, policy.sessionBytes); err != nil {
		return err
	}
	return tx.Commit()
}

func validateRetentionMetadata(ctx context.Context, tx *sql.Tx) error {
	checks := []string{
		`SELECT 1 FROM inbox_commands WHERE
			status NOT IN ('processing', 'completed', 'failed', 'uncertain') OR
			typeof(message_id) != 'text' OR
			length(trim(message_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(name) != 'text' OR
			length(trim(name, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(received_at) != 'text' OR julianday(received_at) IS NULL OR
			(status IN ('completed', 'failed') AND (
				finished_at IS NULL OR typeof(finished_at) != 'text' OR
				length(CAST(finished_at AS BLOB)) = 0 OR length(CAST(finished_at AS BLOB)) > 64 OR
				julianday(finished_at) IS NULL
			)) LIMIT 1`,
		`SELECT 1 FROM native_sessions WHERE
			typeof(run_id) != 'text' OR
			length(trim(run_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(provider) != 'text' OR
			length(trim(provider, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(native_session_id) != 'text' OR
			length(trim(native_session_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			julianday(updated_at) IS NULL LIMIT 1`,
		`SELECT 1 FROM conversation_sessions WHERE
			typeof(conversation_id) != 'text' OR
			length(trim(conversation_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(provider) != 'text' OR
			length(trim(provider, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			typeof(native_session_id) != 'text' OR
			length(trim(native_session_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			julianday(updated_at) IS NULL LIMIT 1`,
	}
	for _, query := range checks {
		var marker int
		err := tx.QueryRowContext(ctx, query).Scan(&marker)
		if err == nil {
			return errors.New("corrupt durable retention metadata")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT finished_at FROM inbox_commands
		WHERE status IN ('completed', 'failed')
	`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return err
		}
		// SQLite accepts timestamps (including normalized impossible dates) that
		// Go's durable decoder rejects. Apply the exact decoder contract here.
		if _, err := time.Parse(time.RFC3339Nano, encoded); err != nil {
			rows.Close()
			return errors.New("corrupt terminal command finished_at")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return nil
}

func deleteInBatches(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed == 0 {
			return nil
		}
	}
}

func deleteSessionAgeBatches(ctx context.Context, tx *sql.Tx, table, cutoff string) error {
	switch table {
	case "native_sessions":
		return deleteInBatches(ctx, tx, `
			DELETE FROM native_sessions WHERE rowid IN (
				SELECT rowid FROM native_sessions WHERE julianday(updated_at) < julianday(?)
				ORDER BY updated_at, run_id LIMIT ?
			)
		`, cutoff, durablePruneBatchSize)
	case "conversation_sessions":
		return deleteInBatches(ctx, tx, `
			DELETE FROM conversation_sessions WHERE rowid IN (
				SELECT rowid FROM conversation_sessions WHERE julianday(updated_at) < julianday(?)
				ORDER BY updated_at, conversation_id, provider LIMIT ?
			)
		`, cutoff, durablePruneBatchSize)
	default:
		return errors.New("unknown session retention table")
	}
}

func pruneInboxBoundary(ctx context.Context, tx *sql.Tx, maxCount int, maxBytes int64) error {
	var cursorTime, cursorID string
	hasCursor := false
	count := 0
	bytes := int64(0)
	for {
		query := `
			SELECT finished_at, message_id,
			       length(CAST(message_id AS BLOB)) + length(CAST(name AS BLOB)) + length(payload) +
			       COALESCE(length(result), 0) + COALESCE(length(CAST(error_code AS BLOB)), 0)
			FROM inbox_commands
			WHERE status IN ('completed', 'failed')`
		args := make([]any, 0, 3)
		if hasCursor {
			query += ` AND (finished_at < ? OR (finished_at = ? AND message_id < ?))`
			args = append(args, cursorTime, cursorTime, cursorID)
		}
		query += ` ORDER BY finished_at DESC, message_id DESC LIMIT ?`
		args = append(args, durablePruneBatchSize)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		read := 0
		for rows.Next() {
			var timestamp, id string
			var size int64
			if err := rows.Scan(&timestamp, &id, &size); err != nil {
				rows.Close()
				return err
			}
			read++
			if size < 0 {
				rows.Close()
				return errors.New("corrupt inbox retention size")
			}
			if count >= maxCount || size > maxBytes-bytes {
				rows.Close()
				return deleteInBatches(ctx, tx, `
					DELETE FROM inbox_commands WHERE rowid IN (
						SELECT rowid FROM inbox_commands
						WHERE status IN ('completed', 'failed')
						  AND (finished_at < ? OR (finished_at = ? AND message_id <= ?))
						ORDER BY finished_at, message_id LIMIT ?
					)
				`, timestamp, timestamp, id, durablePruneBatchSize)
			}
			count++
			bytes += size
			cursorTime, cursorID = timestamp, id
			hasCursor = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if read < durablePruneBatchSize {
			return nil
		}
	}
}

func pruneNativeSessionBoundary(ctx context.Context, tx *sql.Tx, maxCount int, maxBytes int64) error {
	var cursorTime, cursorID string
	hasCursor := false
	count := 0
	bytes := int64(0)
	for {
		query := `SELECT updated_at, run_id, length(CAST(run_id AS BLOB)) +
			length(CAST(provider AS BLOB)) + length(CAST(native_session_id AS BLOB))
			FROM native_sessions WHERE 1 = 1`
		args := make([]any, 0, 3)
		if hasCursor {
			query += ` AND (updated_at < ? OR (updated_at = ? AND run_id < ?))`
			args = append(args, cursorTime, cursorTime, cursorID)
		}
		query += ` ORDER BY updated_at DESC, run_id DESC LIMIT ?`
		args = append(args, durablePruneBatchSize)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		read := 0
		for rows.Next() {
			var timestamp, id string
			var size int64
			if err := rows.Scan(&timestamp, &id, &size); err != nil {
				rows.Close()
				return err
			}
			read++
			if size < 0 {
				rows.Close()
				return errors.New("corrupt native session retention size")
			}
			if count >= maxCount || size > maxBytes-bytes {
				rows.Close()
				return deleteInBatches(ctx, tx, `
					DELETE FROM native_sessions WHERE rowid IN (
						SELECT rowid FROM native_sessions
						WHERE updated_at < ? OR (updated_at = ? AND run_id <= ?)
						ORDER BY updated_at, run_id LIMIT ?
					)
				`, timestamp, timestamp, id, durablePruneBatchSize)
			}
			count++
			bytes += size
			cursorTime, cursorID = timestamp, id
			hasCursor = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if read < durablePruneBatchSize {
			return nil
		}
	}
}

func pruneConversationSessionBoundary(ctx context.Context, tx *sql.Tx, maxCount int, maxBytes int64) error {
	var cursorTime, cursorID, cursorProvider string
	hasCursor := false
	count := 0
	bytes := int64(0)
	for {
		query := `SELECT updated_at, conversation_id, provider,
			length(CAST(conversation_id AS BLOB)) + length(CAST(provider AS BLOB)) +
			length(CAST(native_session_id AS BLOB))
			FROM conversation_sessions WHERE 1 = 1`
		args := make([]any, 0, 6)
		if hasCursor {
			query += ` AND (updated_at < ? OR (updated_at = ? AND
				(conversation_id < ? OR (conversation_id = ? AND provider < ?))))`
			args = append(args, cursorTime, cursorTime, cursorID, cursorID, cursorProvider)
		}
		query += ` ORDER BY updated_at DESC, conversation_id DESC, provider DESC LIMIT ?`
		args = append(args, durablePruneBatchSize)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		read := 0
		for rows.Next() {
			var timestamp, id, provider string
			var size int64
			if err := rows.Scan(&timestamp, &id, &provider, &size); err != nil {
				rows.Close()
				return err
			}
			read++
			if size < 0 {
				rows.Close()
				return errors.New("corrupt conversation session retention size")
			}
			if count >= maxCount || size > maxBytes-bytes {
				rows.Close()
				return deleteInBatches(ctx, tx, `
					DELETE FROM conversation_sessions WHERE rowid IN (
						SELECT rowid FROM conversation_sessions
						WHERE updated_at < ? OR (updated_at = ? AND
							(conversation_id < ? OR (conversation_id = ? AND provider <= ?)))
						ORDER BY updated_at, conversation_id, provider LIMIT ?
					)
				`, timestamp, timestamp, id, id, provider, durablePruneBatchSize)
			}
			count++
			bytes += size
			cursorTime, cursorID, cursorProvider = timestamp, id, provider
			hasCursor = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if read < durablePruneBatchSize {
			return nil
		}
	}
}

func (usage durableUsage) String() string {
	return fmt.Sprintf("inbox=%d/%d native=%d/%d conversation=%d/%d",
		usage.inboxRows, usage.inboxBytes, usage.nativeRows, usage.nativeBytes,
		usage.conversationRows, usage.conversationBytes)
}

func (s *Store) quickCheck(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("check node sqlite integrity: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		count++
		if result != "ok" {
			return errors.New("node sqlite integrity check failed")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("node sqlite integrity check returned no result")
	}
	return nil
}
