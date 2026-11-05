package localstore

import (
	"context"
	"database/sql"
	"errors"
)

const maxEventSequence int64 = 1<<63 - 1

// High-water marks are committed atomically with events, never reconstructed.
func validateEventSequenceCursors(ctx context.Context, tx *sql.Tx) error {
	if err := rejectInvalidEventSequenceMetadata(ctx, tx, "event_log", "sequence"); err != nil {
		return err
	}
	if err := rejectEventSequenceHoles(ctx, tx); err != nil {
		return err
	}
	if err := rejectInvalidEventSequenceMetadata(ctx, tx, "event_sequence_cursors", "last_sequence"); err != nil {
		return err
	}
	var invalid int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		FROM event_log AS event
		LEFT JOIN event_sequence_cursors AS cursor
		  ON cursor.run_id = event.run_id AND cursor.lease_epoch = event.lease_epoch
		WHERE cursor.run_id IS NULL OR cursor.last_sequence < event.sequence
		LIMIT 1
	`).Scan(&invalid)
	if err == nil {
		return errors.New("event sequence cursor is behind event log")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// Existing rows may start above one because an older ACK legitimately removed
// a contiguous prefix. Rows which remain for a stream must nevertheless be
// contiguous and in append (rowid) order; otherwise no cumulative ACK can drain
// them without stepping over a hole.
func rejectEventSequenceHoles(ctx context.Context, tx *sql.Tx) error {
	checks := []string{
		`WITH ordered AS (
			SELECT sequence,
			       LAG(sequence) OVER (
				PARTITION BY run_id, lease_epoch ORDER BY sequence
			   ) AS previous_sequence
			FROM event_log
		)
		SELECT 1 FROM ordered
		WHERE previous_sequence IS NOT NULL AND sequence != previous_sequence + 1
		LIMIT 1`,
		`WITH appended AS (
			SELECT sequence,
			       LAG(sequence) OVER (
				PARTITION BY run_id, lease_epoch ORDER BY rowid
			   ) AS previous_sequence
			FROM event_log
		)
		SELECT 1 FROM appended
		WHERE previous_sequence IS NOT NULL AND sequence != previous_sequence + 1
		LIMIT 1`,
	}
	for _, query := range checks {
		var invalid int
		err := tx.QueryRowContext(ctx, query).Scan(&invalid)
		if err == nil {
			return errors.New("event log contains a durable sequence hole")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func rejectInvalidEventSequenceMetadata(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	sequenceColumn string,
) error {
	var query string
	switch table {
	case "event_log":
		query = `
			SELECT 1 FROM event_log
			WHERE typeof(run_id) != 'text' OR
			      length(trim(run_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			      length(CAST(run_id AS BLOB)) > ? OR
			      typeof(lease_epoch) != 'integer' OR lease_epoch <= 0 OR
			      typeof(sequence) != 'integer' OR sequence <= 0
			LIMIT 1`
	case "event_sequence_cursors":
		query = `
			SELECT 1 FROM event_sequence_cursors
			WHERE typeof(run_id) != 'text' OR
			      length(trim(run_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) = 0 OR
			      length(CAST(run_id AS BLOB)) > ? OR
			      typeof(lease_epoch) != 'integer' OR lease_epoch <= 0 OR
			      typeof(last_sequence) != 'integer' OR last_sequence <= 0
			LIMIT 1`
	default:
		return errors.New("unknown event sequence metadata table")
	}
	var invalid int
	err := tx.QueryRowContext(ctx, query, maxEventRunIDBytes).Scan(&invalid)
	if err == nil {
		return errors.New("invalid " + table + " " + sequenceColumn + " metadata")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// advanceEventSequenceCursor reserves a sequence in the caller's transaction.
// The event insert must commit in that same transaction, otherwise this update
// rolls back and does not leave a gap caused by a failed append.
func advanceEventSequenceCursor(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	leaseEpoch int64,
	requested int64,
) (int64, error) {
	var sequence int64
	if requested == 0 {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO event_sequence_cursors (run_id, lease_epoch, last_sequence)
			VALUES (?, ?, 1)
			ON CONFLICT(run_id, lease_epoch) DO UPDATE SET
				last_sequence = event_sequence_cursors.last_sequence + 1
			WHERE event_sequence_cursors.last_sequence < ?
			RETURNING last_sequence
		`, runID, leaseEpoch, maxEventSequence).Scan(&sequence)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("durable event sequence is exhausted")
		}
		if err != nil {
			return 0, err
		}
		if sequence <= 0 {
			return 0, errors.New("allocated durable event sequence is invalid")
		}
		return sequence, nil
	}
	if requested < 0 {
		return 0, errors.New("explicit durable event sequence must be positive")
	}
	var last int64
	err := tx.QueryRowContext(ctx, `
		SELECT last_sequence FROM event_sequence_cursors
		WHERE run_id = ? AND lease_epoch = ?
	`, runID, leaseEpoch).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		if requested != 1 {
			return 0, errors.New("first explicit durable event sequence must be 1")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_sequence_cursors (run_id, lease_epoch, last_sequence)
			VALUES (?, ?, 1)
		`, runID, leaseEpoch); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if last >= maxEventSequence {
		return 0, errors.New("durable event sequence is exhausted")
	}
	if requested != last+1 {
		return 0, errors.New("explicit durable event sequence must equal the next cursor")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE event_sequence_cursors SET last_sequence = ?
		WHERE run_id = ? AND lease_epoch = ? AND last_sequence = ?
	`, requested, runID, leaseEpoch, last)
	if err != nil {
		return 0, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if updated != 1 {
		return 0, errors.New("explicit durable event sequence was not reserved")
	}
	return requested, nil
}
