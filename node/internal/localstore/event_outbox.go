package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	// Normal streaming is backpressured at roughly 256 MiB. The hard edge is
	// deliberately higher so normal deltas never consume terminal capacity.
	maxUnackedEventCount = int64(32_768)
	maxUnackedEventBytes = int64(256 << 20)

	// Keep one maximum-sized priority event for every Run that the two global
	// Provider managers can concurrently own (64 Codex + 64 OpenCode). The
	// aggregate count is duplicated here to avoid a localstore -> runtime import
	// cycle; admission below still grants at most one slot per (run_id, lease).
	reservedPriorityEventCount = int64(128)
	reservedPriorityEventBytes = reservedPriorityEventCount * int64(maxDurableEventBytes)
)

var ErrEventOutboxFull = errors.New("durable event outbox capacity exceeded")

type eventOutboxLimits struct {
	softCount int64
	softBytes int64
	hardCount int64
	hardBytes int64
}

type eventOutboxUsage struct {
	count int64
	bytes int64
}

func defaultEventOutboxLimits() eventOutboxLimits {
	return eventOutboxLimits{
		softCount: maxUnackedEventCount,
		softBytes: maxUnackedEventBytes,
		hardCount: maxUnackedEventCount + reservedPriorityEventCount,
		hardBytes: maxUnackedEventBytes + reservedPriorityEventBytes,
	}
}

func validateEventOutboxLimits(limits eventOutboxLimits) error {
	if limits.softCount < 0 || limits.softBytes < 0 || limits.hardCount <= 0 || limits.hardBytes <= 0 ||
		limits.softCount >= limits.hardCount || limits.softBytes >= limits.hardBytes {
		return errors.New("invalid durable event outbox limits")
	}
	return nil
}

func isTerminalEventType(eventType string) bool {
	switch eventType {
	case "run.completed", "run.finished":
		// Only the normalized terminal outcome may consume reserve capacity.
		// run.error is followed by run.finished and diagnostics can be retried or
		// represented by an in-place poison replacement; admitting either here
		// could starve another concurrently managed Run of its final marker.
		return true
	default:
		return false
	}
}

// Counters and triggers are part of the baseline; mismatch is corruption, not
// a request to recreate metadata or silently repair the database.
func validateEventOutboxMetadata(ctx context.Context, tx *sql.Tx) error {
	if err := validateEventOutboxStateSchema(ctx, tx); err != nil {
		return err
	}
	var invalid int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM event_log
		WHERE typeof(wire_bytes) != 'integer' OR wire_bytes <= 0 OR wire_bytes < length(payload)
		LIMIT 1
	`).Scan(&invalid)
	if err == nil {
		return errors.New("event_log contains invalid wire byte metadata")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return verifyEventOutboxMetadata(ctx, tx)
}

func validateEventOutboxStateSchema(ctx context.Context, tx *sql.Tx) error {
	type column struct {
		typeName string
		notNull  bool
		primary  int
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(event_outbox_state)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := make(map[string]column, 3)
	for rows.Next() {
		var cid, notNull, primary int
		var name, typeName string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primary); err != nil {
			return err
		}
		if cid < 0 || name == "" {
			return errors.New("event_outbox_state has an invalid column")
		}
		columns[name] = column{typeName: strings.ToUpper(strings.TrimSpace(typeName)), notNull: notNull == 1, primary: primary}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expected := map[string]int{"singleton": 1, "unacked_count": 0, "unacked_bytes": 0}
	if len(columns) != len(expected) {
		return errors.New("event_outbox_state has an unexpected column set")
	}
	for name, primary := range expected {
		item, ok := columns[name]
		if !ok || item.typeName != "INTEGER" || !item.notNull || item.primary != primary {
			return fmt.Errorf("event_outbox_state column %s has an unsafe schema", name)
		}
	}
	return nil
}

func verifyEventOutboxMetadata(ctx context.Context, tx *sql.Tx) error {
	var rows, singleton, count, bytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MIN(singleton), 0),
		       COALESCE(MIN(unacked_count), -1), COALESCE(MIN(unacked_bytes), -1)
		FROM event_outbox_state
	`).Scan(&rows, &singleton, &count, &bytes); err != nil {
		return err
	}
	if rows != 1 || singleton != 1 || count < 0 || bytes < 0 {
		return errors.New("event outbox metadata singleton is invalid")
	}
	var actualCount, actualBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(wire_bytes), 0)
		FROM event_log WHERE acked_at IS NULL
	`).Scan(&actualCount, &actualBytes); err != nil {
		return err
	}
	if count != actualCount || bytes != actualBytes {
		return errors.New("event outbox metadata does not match event_log")
	}
	return nil
}

func eventOutboxUsageTx(ctx context.Context, tx *sql.Tx) (eventOutboxUsage, error) {
	var usage eventOutboxUsage
	err := tx.QueryRowContext(ctx, `
		SELECT unacked_count, unacked_bytes
		FROM event_outbox_state WHERE singleton = 1
	`).Scan(&usage.count, &usage.bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return eventOutboxUsage{}, errors.New("event outbox metadata singleton is missing")
	}
	if err != nil {
		return eventOutboxUsage{}, err
	}
	if usage.count < 0 || usage.bytes < 0 {
		return eventOutboxUsage{}, errors.New("event outbox metadata is corrupt")
	}
	return usage, nil
}

func (s *Store) ensureEventOutboxCapacity(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	leaseEpoch int64,
	eventBytes int64,
	terminal bool,
) error {
	if eventBytes <= 0 {
		return errors.New("durable event byte size is invalid")
	}
	limits := s.outboxLimits
	if err := validateEventOutboxLimits(limits); err != nil {
		return err
	}
	usage, err := eventOutboxUsageTx(ctx, tx)
	if err != nil {
		return err
	}
	softFits := usage.count < limits.softCount && eventBytes <= limits.softBytes-usage.bytes
	if softFits {
		return nil
	}
	if !terminal {
		return fmt.Errorf("%w: soft limit reached (events=%d bytes=%d)",
			ErrEventOutboxFull, usage.count, usage.bytes)
	}
	if strings.TrimSpace(runID) == "" || leaseEpoch <= 0 {
		return errors.New("terminal event stream is invalid")
	}
	// A stream which already has an unacknowledged terminal has consumed its
	// one reserve slot. This prevents one noisy/malformed Run from exhausting
	// capacity reserved for the other concurrently managed Runs.
	var hasPendingTerminal int
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM event_log
			WHERE run_id = ? AND lease_epoch = ? AND acked_at IS NULL
			  AND event_type IN ('run.completed', 'run.finished')
		)
	`, runID, leaseEpoch).Scan(&hasPendingTerminal)
	if err != nil {
		return err
	}
	if hasPendingTerminal != 0 {
		return fmt.Errorf("%w: terminal reserve already used (events=%d bytes=%d)",
			ErrEventOutboxFull, usage.count, usage.bytes)
	}
	if usage.count >= limits.hardCount || eventBytes > limits.hardBytes-usage.bytes {
		return fmt.Errorf("%w: hard limit reached (events=%d bytes=%d)",
			ErrEventOutboxFull, usage.count, usage.bytes)
	}
	return nil
}

func (s *Store) ensureEventReplacementCapacity(
	ctx context.Context,
	tx *sql.Tx,
	oldBytes, newBytes int64,
) error {
	if oldBytes <= 0 || newBytes <= 0 {
		return errors.New("durable event replacement byte size is invalid")
	}
	limits := s.outboxLimits
	if err := validateEventOutboxLimits(limits); err != nil {
		return err
	}
	usage, err := eventOutboxUsageTx(ctx, tx)
	if err != nil {
		return err
	}
	if usage.bytes < oldBytes {
		return errors.New("event outbox byte metadata is behind replacement row")
	}
	nextBytes := usage.bytes - oldBytes + newBytes
	// A current outbox may exceed a newly reduced limit. A replacement
	// which does not grow it is still allowed so poison quarantine can help the
	// queue drain; no operation may make an over-limit queue larger.
	if nextBytes > limits.hardBytes && newBytes > oldBytes {
		return fmt.Errorf("%w: hard byte limit reached during replacement", ErrEventOutboxFull)
	}
	return nil
}
