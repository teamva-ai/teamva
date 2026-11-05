package localstore

import (
	"context"
	"errors"
	"fmt"
)

const (
	// Processing and uncertain rows cannot be retention-pruned because they are
	// the durable idempotency record for a command whose outcome is not safely
	// repeatable. Bound admission instead, using the same conservative scale as
	// terminal inbox retention.
	maxAdmittedInboxCommands = int64(10_000)
	maxAdmittedInboxBytes    = int64(64 << 20)
)

var ErrInboxAdmissionFull = errors.New("durable command inbox admission capacity exceeded")

type inboxAdmissionLimits struct {
	count int64
	bytes int64
}

func defaultInboxAdmissionLimits() inboxAdmissionLimits {
	return inboxAdmissionLimits{count: maxAdmittedInboxCommands, bytes: maxAdmittedInboxBytes}
}

func validateInboxAdmissionLimits(limits inboxAdmissionLimits) error {
	if limits.count <= 0 || limits.count == int64(^uint64(0)>>1) || limits.bytes <= 0 {
		return errors.New("invalid durable command inbox admission limits")
	}
	return nil
}

// ensureInboxAdmission runs under Store.durableMu. The inner LIMIT bounds work
// even if external writes caused the current database to exceed its cap;
// an over-cap database remains readable but cannot admit another distinct ID.
func (s *Store) ensureInboxAdmission(ctx context.Context, command Command) error {
	limits := s.inboxLimits
	if err := validateInboxAdmissionLimits(limits); err != nil {
		return err
	}
	candidateBytes := int64(len(command.MessageID) + len(command.Name) + len(command.Payload))
	if candidateBytes <= 0 || candidateBytes > limits.bytes {
		return fmt.Errorf("%w: command bytes exceed limit", ErrInboxAdmissionFull)
	}
	var count, retainedBytes int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(retained_bytes), 0)
		FROM (
			SELECT MIN(
				length(CAST(message_id AS BLOB)) + length(CAST(name AS BLOB)) + length(payload) +
				COALESCE(length(result), 0) + COALESCE(length(CAST(error_code AS BLOB)), 0),
				?
			) AS retained_bytes
			FROM inbox_commands
			WHERE status IN ('processing', 'uncertain')
			LIMIT ?
		)
	`, limits.bytes, limits.count+1).Scan(&count, &retainedBytes); err != nil {
		return err
	}
	if count < 0 || retainedBytes < 0 {
		return errors.New("durable command inbox admission metadata is corrupt")
	}
	if count >= limits.count || retainedBytes >= limits.bytes || candidateBytes > limits.bytes-retainedBytes {
		return fmt.Errorf("%w: commands=%d bytes=%d", ErrInboxAdmissionFull, count, retainedBytes)
	}
	return nil
}
