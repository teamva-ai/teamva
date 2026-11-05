package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// The dismissal flag is stored per user so the derived "empty" state and the
// persisted decision always describe the same owner.
func onboardingMetadataKey(userID string) string { return "onboarding:" + userID }

type onboardingMetadata struct {
	Dismissed bool `json:"dismissed"`
}

func (s *SQLite) Onboarding(ctx context.Context, userID string) (domain.Onboarding, error) {
	dismissed, err := s.onboardingDismissed(ctx, userID)
	if err != nil {
		return domain.Onboarding{}, err
	}
	if dismissed {
		return domain.Onboarding{Dismissed: true}, nil
	}
	needed, err := s.onboardingNeeded(ctx, userID)
	if err != nil {
		return domain.Onboarding{}, err
	}
	return domain.Onboarding{Needed: needed}, nil
}

func (s *SQLite) SetOnboardingDismissed(ctx context.Context, userID string, dismissed bool) error {
	value, err := json.Marshal(onboardingMetadata{Dismissed: dismissed})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_metadata (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, onboardingMetadataKey(userID), string(value), sqliteTimeValue(time.Now()))
	return err
}

func (s *SQLite) onboardingDismissed(ctx context.Context, userID string) (bool, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `
		SELECT value FROM app_metadata WHERE key = ?
	`, onboardingMetadataKey(userID)).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var metadata onboardingMetadata
	if err := json.Unmarshal([]byte(encoded), &metadata); err != nil {
		return false, fmt.Errorf("decode onboarding metadata: %w", err)
	}
	return metadata.Dismissed, nil
}

// A user-created Agent or a configured Lumi proves that the first-run
// configuration is real; the pre-seeded but unconfigured Lumi does not.
func (s *SQLite) onboardingNeeded(ctx context.Context, userID string) (bool, error) {
	var needed bool
	err := s.db.QueryRowContext(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM agents a
			JOIN computers c ON c.id = a.computer_id
			JOIN users u ON u.id = c.user_id
			WHERE c.user_id = ? AND a.deleted_at IS NULL AND a.system_role <> 'general'
			  AND c.deleted_at IS NULL AND u.deleted_at IS NULL
		) AND NOT EXISTS (
			SELECT 1 FROM agents a
			JOIN computers c ON c.id = a.computer_id
			JOIN users u ON u.id = c.user_id
			WHERE c.user_id = ? AND a.deleted_at IS NULL AND a.system_role = 'general'
			  AND a.current_revision_id IS NOT NULL
			  AND c.deleted_at IS NULL AND u.deleted_at IS NULL
		)
	`, userID, userID).Scan(&needed)
	return needed, err
}
