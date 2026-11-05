package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// providerExecutableKey stores one Provider's user-configured CLI path. Empty
// values are deleted instead of persisted so clearing the setting restores the
// default resolution (environment, bundled runtime, then PATH).
func providerExecutableKey(provider string) string {
	return "provider_executable:" + provider
}

func (s *SQLite) ProviderExecutablePaths(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value FROM app_metadata WHERE key LIKE 'provider_executable:%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]string)
	for rows.Next() {
		var key, encoded string
		if err := rows.Scan(&key, &encoded); err != nil {
			return nil, err
		}
		provider := strings.TrimPrefix(key, "provider_executable:")
		if provider == "" {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			return nil, fmt.Errorf("decode provider executable metadata %s: %w", provider, err)
		}
		if value != "" {
			paths[provider] = value
		}
	}
	return paths, rows.Err()
}

func (s *SQLite) ProviderExecutablePath(ctx context.Context, provider string) (string, error) {
	if provider == "" {
		return "", errors.New("provider is required")
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `
		SELECT value FROM app_metadata WHERE key = ?
	`, providerExecutableKey(provider)).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return "", fmt.Errorf("decode provider executable metadata: %w", err)
	}
	return value, nil
}

func (s *SQLite) SetProviderExecutablePath(ctx context.Context, provider, path string) error {
	if provider == "" {
		return errors.New("provider is required")
	}
	if path == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM app_metadata WHERE key = ?`, providerExecutableKey(provider))
		return err
	}
	value, err := json.Marshal(path)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_metadata (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, providerExecutableKey(provider), string(value), sqliteTimeValue(time.Now()))
	return err
}
