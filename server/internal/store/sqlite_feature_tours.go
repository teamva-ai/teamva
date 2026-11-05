package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func featureToursMetadataKey(userID string) string { return "feature_tours:" + userID }

const sqliteFeatureToursSelect = `
	SELECT tours.value FROM users
	LEFT JOIN app_metadata tours ON tours.key = ?
	WHERE users.id = ? AND users.deleted_at IS NULL
`

func (s *SQLite) GetFeatureTours(ctx context.Context, userID string) (domain.FeatureTours, error) {
	return readFeatureTours(s.db.QueryRowContext(ctx, sqliteFeatureToursSelect, featureToursMetadataKey(userID), userID), userID)
}

func readFeatureTours(row scanner, userID string) (domain.FeatureTours, error) {
	var encoded sql.NullString
	if err := row.Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.FeatureTours{}, domain.NotFound("用户", userID)
		}
		return domain.FeatureTours{}, err
	}
	view := domain.FeatureTours{Seen: []string{}}
	if !encoded.Valid {
		return view, nil
	}
	// Only known true flags are valid. In particular null, false, unknown keys,
	// duplicate keys and trailing JSON must not be silently repaired by a mark.
	decoder := json.NewDecoder(strings.NewReader(encoded.String))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return domain.FeatureTours{}, fmt.Errorf("invalid feature tours metadata object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		id, ok := key.(string)
		if err != nil || !ok || !domain.IsFeatureTourID(id) || seen[id] {
			return domain.FeatureTours{}, fmt.Errorf("invalid feature tours metadata key")
		}
		value, err := decoder.Token()
		if err != nil || value != true {
			return domain.FeatureTours{}, fmt.Errorf("invalid feature tours metadata flag")
		}
		seen[id] = true
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return domain.FeatureTours{}, fmt.Errorf("invalid feature tours metadata object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return domain.FeatureTours{}, fmt.Errorf("invalid trailing feature tours metadata")
	}
	for _, id := range domain.FeatureTourIDs() {
		if seen[id] {
			view.Seen = append(view.Seen, id)
		}
	}
	return view, nil
}

func (s *SQLite) MarkFeatureTourSeen(ctx context.Context, userID, tourID string) (domain.FeatureTours, error) {
	if !domain.IsFeatureTourID(tourID) {
		return domain.FeatureTours{}, domain.Invalid("未知的栏目教学")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.FeatureTours{}, err
	}
	defer func() { _ = tx.Rollback() }()
	key := featureToursMetadataKey(userID)
	// This is the first transaction statement: obtain SQLite's writer lock
	// before reading metadata, also across independent repository connections.
	// Existing metadata (including its timestamp) is never overwritten here.
	// An absent/deleted user cannot create a metadata row.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO app_metadata (key, value, updated_at)
		SELECT ?, '{}', ? FROM users WHERE id = ? AND deleted_at IS NULL
		ON CONFLICT(key) DO NOTHING
	`, key, sqliteTimeValue(time.Now()), userID); err != nil {
		return domain.FeatureTours{}, err
	}
	current, err := readFeatureTours(tx.QueryRowContext(ctx, sqliteFeatureToursSelect, key, userID), userID)
	if err != nil {
		return domain.FeatureTours{}, err
	}
	if !slices.Contains(current.Seen, tourID) {
		// Merge one flag atomically, never replace a snapshot of the whole set.
		if _, err := tx.ExecContext(ctx, `
			UPDATE app_metadata SET value = json_set(value, ?, json('true')), updated_at = ?
			WHERE key = ?
		`, "$."+tourID, sqliteTimeValue(time.Now()), key); err != nil {
			return domain.FeatureTours{}, err
		}
		current, err = readFeatureTours(tx.QueryRowContext(ctx, sqliteFeatureToursSelect, key, userID), userID)
		if err != nil {
			return domain.FeatureTours{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.FeatureTours{}, err
	}
	return current, nil
}
