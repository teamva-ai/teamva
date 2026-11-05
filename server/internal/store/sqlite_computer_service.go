package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func (s *SQLite) ListComputers(ctx context.Context, userID string) ([]domain.Computer, error) {
	rows, err := s.db.QueryContext(ctx, sqliteComputerSelect+`
		JOIN users owning_user ON owning_user.id = computers.user_id
		WHERE computers.user_id = ? AND computers.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY computers.created_at, computers.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Computer, 0)
	for rows.Next() {
		var item domain.Computer
		if err := scanSQLiteComputer(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) GetComputer(ctx context.Context, userID, id string) (domain.Computer, error) {
	var item domain.Computer
	err := scanSQLiteComputer(s.db.QueryRowContext(ctx, sqliteComputerSelect+`
		JOIN users owning_user ON owning_user.id = computers.user_id
		WHERE computers.id = ? AND computers.user_id = ? AND computers.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Computer{}, domain.NotFound("设备", id)
	}
	return item, err
}

func (s *SQLite) UpdateComputer(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.Computer)) (domain.Computer, error) {
	item, err := s.GetComputer(ctx, userID, id)
	if err != nil {
		return domain.Computer{}, err
	}
	if item.Version != expectedVersion {
		return domain.Computer{}, versionConflict(item.Version)
	}
	update(&item)
	result, err := s.db.ExecContext(ctx, `
		UPDATE computers SET name = ?, note = ?, computer_use_enabled = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND id IN (SELECT scoped_computer.id FROM computers scoped_computer
		             JOIN users owning_user ON owning_user.id = scoped_computer.user_id
		             WHERE scoped_computer.id = ? AND scoped_computer.user_id = ?
		               AND owning_user.deleted_at IS NULL)
	`, item.Name, item.Note, boolInt(item.ComputerUseEnabled), sqliteTimeValue(time.Now()), id, expectedVersion, id, userID)
	if err != nil {
		return domain.Computer{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := s.GetComputer(ctx, userID, id)
		if getErr != nil {
			return domain.Computer{}, getErr
		}
		return domain.Computer{}, versionConflict(current.Version)
	}
	return s.GetComputer(ctx, userID, id)
}

func (s *SQLite) RevokeComputer(ctx context.Context, userID, id string) (domain.Computer, error) {
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE computers SET status = 'revoked', version = version + 1, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		  AND id IN (SELECT scoped_computer.id FROM computers scoped_computer
		             JOIN users owning_user ON owning_user.id = scoped_computer.user_id
		             WHERE scoped_computer.id = ? AND scoped_computer.user_id = ?
		               AND owning_user.deleted_at IS NULL)
	`, now, id, userID, id, userID)
	if err != nil {
		return domain.Computer{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.Computer{}, domain.NotFound("设备", id)
	}
	return s.GetComputer(ctx, userID, id)
}

func (s *SQLite) HeartbeatComputer(ctx context.Context, id string, heartbeat domain.NodeHeartbeat) (domain.Computer, int64, error) {
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE computers SET status = 'online', last_seen_at = ?, node_version = ?,
		       protocol_version = ?, updated_at = ?
		WHERE id = ? AND status <> 'revoked' AND deleted_at IS NULL
	`, now, heartbeat.NodeVersion, heartbeat.ProtocolVersion, now, id)
	if err != nil {
		return domain.Computer{}, 0, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.Computer{}, 0, domain.Unauthorized("computer_revoked", "设备已被停用")
	}
	var userID string
	if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM computers WHERE id = ?`, id).Scan(&userID); err != nil {
		return domain.Computer{}, 0, err
	}
	item, err := s.GetComputer(ctx, userID, id)
	return item, 0, err
}

func (s *SQLite) ListAgentServices(ctx context.Context, userID, computerID string) ([]domain.AgentService, error) {
	rows, err := s.db.QueryContext(ctx, sqliteAgentServiceSelect+`
		JOIN computers owning_computer ON owning_computer.id = services.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE services.computer_id = ? AND owning_computer.user_id = ?
		  AND services.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY services.provider, services.id
	`, computerID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.AgentService, 0)
	for rows.Next() {
		var item domain.AgentService
		if err := scanSQLiteAgentService(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) UpsertAgentService(ctx context.Context, userID string, input domain.AgentService) (domain.AgentService, error) {
	capabilities, err := json.Marshal(input.Capabilities)
	if err != nil {
		return domain.AgentService{}, err
	}
	if input.CapabilityVersion < 1 {
		input.CapabilityVersion = 1
	}
	now := sqliteTimeValue(time.Now())
	lastProbed := nullableSQLiteTime(input.LastProbedAt)
	var item domain.AgentService
	err = scanSQLiteAgentService(s.db.QueryRowContext(ctx, `
		INSERT INTO agent_services (
			id, computer_id, provider, provider_version, login_status, status,
			capability_manifest, capability_version, last_probed_at,
			version, created_at, updated_at
		)
		SELECT ?, computers.id, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?
		FROM computers
		JOIN users owning_user ON owning_user.id = computers.user_id
		WHERE computers.id = ? AND computers.user_id = ? AND computers.status <> 'revoked'
		  AND computers.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ON CONFLICT(computer_id, provider) WHERE deleted_at IS NULL DO UPDATE SET
			provider_version = excluded.provider_version,
			login_status = excluded.login_status,
			status = excluded.status,
			capability_manifest = excluded.capability_manifest,
			capability_version = excluded.capability_version,
			last_probed_at = excluded.last_probed_at,
			version = agent_services.version + 1,
			updated_at = excluded.updated_at
		RETURNING id, version, created_at, updated_at, deleted_at, computer_id, provider,
		          provider_version, login_status, status, capability_manifest,
		          capability_version, last_probed_at
	`, newID("svc"), input.Provider, input.ProviderVersion, input.LoginStatus, input.Status,
		string(capabilities), input.CapabilityVersion, lastProbed, now, now,
		input.ComputerID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentService{}, domain.NotFound("可用设备", input.ComputerID)
	}
	return item, err
}

func (s *SQLite) RequestAgentProbe(ctx context.Context, userID, id string) (domain.AgentService, error) {
	item, err := s.getAgentService(ctx, userID, id)
	if err != nil {
		return domain.AgentService{}, err
	}
	if item.Status == "probing" {
		return item, nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_services SET status = 'probing', version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND computer_id IN (
		      SELECT owning_computer.id FROM computers owning_computer
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, sqliteTimeValue(time.Now()), id, item.Version, userID)
	if err != nil {
		return domain.AgentService{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := s.getAgentService(ctx, userID, id)
		if getErr != nil {
			return domain.AgentService{}, getErr
		}
		return domain.AgentService{}, versionConflict(current.Version)
	}
	return s.getAgentService(ctx, userID, id)
}

func (s *SQLite) getAgentService(ctx context.Context, userID, id string) (domain.AgentService, error) {
	var item domain.AgentService
	err := scanSQLiteAgentService(s.db.QueryRowContext(ctx, sqliteAgentServiceSelect+`
		JOIN computers owning_computer ON owning_computer.id = services.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE services.id = ? AND owning_computer.user_id = ?
		  AND services.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentService{}, domain.NotFound("Agent Service", id)
	}
	return item, err
}

const sqliteComputerSelect = `
	SELECT computers.id, computers.version, computers.created_at, computers.updated_at,
	       computers.deleted_at, computers.user_id, computers.name, computers.note,
	       computers.platform, computers.architecture, computers.fingerprint,
	       computers.status, computers.last_seen_at, computers.node_version,
	       computers.protocol_version, computers.computer_use_enabled
	FROM computers
`

func scanSQLiteComputer(row scanner, item *domain.Computer) error {
	var created, updated string
	var deleted, lastSeen sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.UserID, &item.Name, &item.Note, &item.Platform, &item.Architecture,
		&item.Fingerprint, &item.Status, &lastSeen, &item.NodeVersion, &item.ProtocolVersion,
		&item.ComputerUseEnabled); err != nil {
		return err
	}
	var err error
	item.CreatedAt, err = sqliteParseTime(created)
	if err != nil {
		return err
	}
	item.UpdatedAt, err = sqliteParseTime(updated)
	if err != nil {
		return err
	}
	item.DeletedAt, err = sqliteParseNullTime(deleted)
	if err != nil {
		return err
	}
	item.LastSeenAt, err = sqliteParseNullTime(lastSeen)
	return err
}

const sqliteAgentServiceSelect = `
	SELECT services.id, services.version, services.created_at, services.updated_at,
	       services.deleted_at, services.computer_id, services.provider,
	       services.provider_version, services.login_status, services.status,
	       services.capability_manifest, services.capability_version, services.last_probed_at
	FROM agent_services services
`

func scanSQLiteAgentService(row scanner, item *domain.AgentService) error {
	var created, updated, capabilities string
	var deleted, lastProbed sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ComputerID, &item.Provider, &item.ProviderVersion, &item.LoginStatus,
		&item.Status, &capabilities, &item.CapabilityVersion, &lastProbed); err != nil {
		return err
	}
	var err error
	item.CreatedAt, err = sqliteParseTime(created)
	if err != nil {
		return err
	}
	item.UpdatedAt, err = sqliteParseTime(updated)
	if err != nil {
		return err
	}
	item.DeletedAt, err = sqliteParseNullTime(deleted)
	if err != nil {
		return err
	}
	item.LastProbedAt, err = sqliteParseNullTime(lastProbed)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(capabilities), &item.Capabilities); err != nil {
		return err
	}
	if item.Capabilities == nil {
		item.Capabilities = map[string]any{}
	}
	return nil
}

func nullableSQLiteTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return sqliteTimeValue(*value)
}
