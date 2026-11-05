package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// NetworkAccessConfig is local-only metadata. PasswordHash is never included in
// API responses, diagnostics, runtime.json, logs or provider environments.
type NetworkAccessConfig struct {
	Enabled        bool                  `json:"enabled"`
	Port           int                   `json:"port"` // shared local/LAN service port, selected at startup
	PasswordHash   string                `json:"password_hash"`
	TrustedDevices []TrustedRemoteDevice `json:"trusted_devices,omitempty"`
}

// Only a digest of the random browser credential is stored, never the cookie
// value. Trust has no expiration; only changing the password revokes it.
// Bundling this with network settings makes password revocation atomic.
type TrustedRemoteDevice struct {
	TokenHash string `json:"token_hash"`
	Host      string `json:"host"`
}

const networkAccessMetadataKey = "network_access"

func (s *SQLite) NetworkAccess(ctx context.Context) (NetworkAccessConfig, error) {
	var encodedJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT value FROM app_metadata WHERE key = ?
	`, networkAccessMetadataKey).Scan(&encodedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return NetworkAccessConfig{}, nil
	}
	if err != nil {
		return NetworkAccessConfig{}, err
	}
	var config NetworkAccessConfig
	if err := json.Unmarshal([]byte(encodedJSON), &config); err != nil {
		return config, fmt.Errorf("decode network access metadata: %w", err)
	}
	return config, nil
}

func (s *SQLite) SetNetworkAccess(ctx context.Context, config NetworkAccessConfig) error {
	value, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_metadata (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, networkAccessMetadataKey, string(value), sqliteTimeValue(time.Now()))
	return err
}
