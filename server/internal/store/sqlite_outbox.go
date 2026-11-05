package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	LocalCommandPending    = "pending"
	LocalCommandProcessing = "processing"
	LocalCommandUncertain  = "uncertain"
	LocalCommandCompleted  = "completed"
	LocalCommandFailed     = "failed"
	LocalCommandExpired    = "expired"
)

// Every command that the local dispatcher may claim and run. Directory and
// resource commands share the same durable Runtime and completion fencing.
const queuedLocalCommandNames = `('run.start', 'run.stop', 'run.steer', 'approval.decide', 'filesystem.list', 'resource.register', 'resource.validate')`

// Used both before delivery and at result commit: an older authorization must
// never regain authority over a cancelled approval or a stopping/terminal Run.
const sqliteCurrentApprovalDecision = `EXISTS (
	SELECT 1 FROM approvals approval JOIN runs run ON run.id = approval.run_id
	WHERE approval.run_id = json_extract(command_outbox.payload, '$.run_id')
	  AND approval.provider_approval_ref = json_extract(command_outbox.payload, '$.request_id')
	  AND run.deleted_at IS NULL AND run.status IN ('queued','starting','running','waiting_approval')
	  AND ((approval.status = 'approved' AND json_extract(command_outbox.payload, '$.decision') = 'approved')
	    OR (approval.status IN ('denied','expired') AND json_extract(command_outbox.payload, '$.decision') = 'denied'))
	  AND COALESCE(approval.decision_scope, 'once') = COALESCE(json_extract(command_outbox.payload, '$.scope'), 'once')
)`

// LocalCommand is a durable command envelope. ID is deliberately also the
// runtime MessageID: every delivery attempt therefore hits the runtime's
// durable command deduplication record instead of creating a second command.
type LocalCommand struct {
	ID             string
	ComputerID     string
	Name           string
	IdempotencyKey string
	Payload        json.RawMessage
	Result         json.RawMessage
	Status         string
	Attempts       int
	AvailableAt    time.Time
	ClaimedAt      *time.Time
	LeaseUntil     *time.Time
	ClaimToken     string
	ExpiresAt      time.Time
	CompletedAt    *time.Time
	LastError      string
	CreatedAt      time.Time
}

const sqliteLocalCommandSelect = `
	SELECT id, computer_id, command_name, idempotency_key, payload, result,
	       status, attempts, available_at,
	       claimed_at, lease_until, COALESCE(claim_token, ''), expires_at, completed_at,
	       COALESCE(last_error, ''), created_at
	FROM command_outbox
`

func scanSQLiteLocalCommand(row scanner, item *LocalCommand) error {
	var payload string
	var result, claimed, lease, completed sql.NullString
	var available, expires, created string
	if err := row.Scan(
		&item.ID, &item.ComputerID, &item.Name, &item.IdempotencyKey, &payload, &result,
		&item.Status, &item.Attempts, &available,
		&claimed, &lease, &item.ClaimToken, &expires, &completed, &item.LastError, &created,
	); err != nil {
		return err
	}
	item.Payload = json.RawMessage(payload)
	if result.Valid {
		item.Result = json.RawMessage(result.String)
	}
	var err error
	item.AvailableAt, err = sqliteParseTime(available)
	if err != nil {
		return err
	}
	item.ClaimedAt, err = sqliteParseNullTime(claimed)
	if err != nil {
		return err
	}
	item.LeaseUntil, err = sqliteParseNullTime(lease)
	if err != nil {
		return err
	}
	item.ExpiresAt, err = sqliteParseTime(expires)
	if err != nil {
		return err
	}
	item.CompletedAt, err = sqliteParseNullTime(completed)
	if err != nil {
		return err
	}
	item.CreatedAt, err = sqliteParseTime(created)
	return err
}

func insertSQLiteOutbox(
	ctx context.Context, tx *sql.Tx, computerID, name, idempotencyKey string,
	payload any, expiresAt time.Time,
) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	id := newID("cmd")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO command_outbox (
			id, computer_id, command_name, idempotency_key, payload,
			status, attempts, available_at,
			expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
	`, id, computerID, name, idempotencyKey, string(encoded), sqliteTimeValue(now),
		sqliteTimeValue(expiresAt), sqliteTimeValue(now))
	return id, err
}

// ClaimLocalCommand keeps the write lock only for the conditional claim. The
// potentially long-running runtime Handle call is always performed after this
// transaction commits.
func (s *SQLite) ClaimLocalCommand(
	ctx context.Context, now time.Time, lease time.Duration, excludedIDs ...string,
) (LocalCommand, bool, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LocalCommand{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := expireSQLiteApprovalsTx(ctx, tx, now); err != nil {
		return LocalCommand{}, false, err
	}
	// A stop, terminal event or cancelled/replaced approval invalidates an old
	// authorization before it can be reclaimed. Clearing the token also fences
	// late completions from a worker already handing that command to Provider.
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'failed', completed_at = ?, last_error = 'approval_obsolete',
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name = 'approval.decide' AND status IN ('pending','processing','uncertain')
		  AND NOT `+sqliteCurrentApprovalDecision, sqliteTimeValue(now)); err != nil {
		return LocalCommand{}, false, err
	}

	// A worker that outlived its lease has an unknown delivery outcome. Make it
	// claimable again; the stable MessageID lets runtime return the durable prior
	// result instead of executing a duplicate side effect.
	exclusionSQL, exclusionArgs := sqliteCommandExclusion(excludedIDs)
	expireArgs := []any{sqliteTimeValue(now), sqliteTimeValue(now)}
	expireArgs = append(expireArgs, exclusionArgs...)
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'uncertain', available_at = ?, last_error = 'claim_lease_expired',
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name IN ('run.start', 'run.stop', 'run.steer', 'approval.decide')
		  AND status = 'processing' AND lease_until <= ?
	`+exclusionSQL, expireArgs...); err != nil {
		return LocalCommand{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT command_name, COALESCE(json_extract(payload, '$.run_id'), ''),
		       COALESCE(json_extract(payload, '$.resource_id'), ''),
		       COALESCE(json_extract(payload, '$.validation_version'), 0)
		FROM command_outbox
		WHERE command_name IN `+queuedLocalCommandNames+`
		  AND status IN ('pending', 'uncertain')
		  AND expires_at <= ?
	`, sqliteTimeValue(now))
	if err != nil {
		return LocalCommand{}, false, err
	}
	type expiredExecutionCommand struct {
		name, runID, resourceID string
		validationVersion       int64
	}
	expiredCommands := make([]expiredExecutionCommand, 0)
	for rows.Next() {
		var command expiredExecutionCommand
		if err := rows.Scan(&command.name, &command.runID, &command.resourceID, &command.validationVersion); err != nil {
			_ = rows.Close()
			return LocalCommand{}, false, err
		}
		expiredCommands = append(expiredCommands, command)
	}
	if err := rows.Close(); err != nil {
		return LocalCommand{}, false, err
	}
	// Expiration is terminal and explicit; expired commands are never handed to
	// the runtime with an already invalid envelope.
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = CASE WHEN command_name IN ('filesystem.list','resource.register','resource.validate')
		                  THEN 'expired' ELSE 'failed' END,
		    completed_at = ?, last_error = 'command_expired',
		    payload = CASE
		        WHEN command_name IN ('run.start', 'run.steer') THEN json_remove(payload, '$.attachments')
		        ELSE payload
		    END,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name IN `+queuedLocalCommandNames+`
		  AND status IN ('pending', 'uncertain') AND expires_at <= ?
	`, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return LocalCommand{}, false, err
	}
	for _, command := range expiredCommands {
		if command.name == "resource.validate" {
			if err := setSQLiteResourceValidationTx(ctx, tx, command.resourceID, command.validationVersion, false, false, now); err != nil {
				return LocalCommand{}, false, err
			}
		}
		if command.runID == "" || command.name == "run.steer" {
			continue
		}
		reason := command.name + "_expired"
		if err := finishSQLiteRunTx(ctx, tx, command.runID, domain.RunFailed, reason, now, "run.finished"); err != nil {
			return LocalCommand{}, false, err
		}
	}

	var id, previousStatus string
	selectArgs := []any{sqliteTimeValue(now), sqliteTimeValue(now)}
	selectArgs = append(selectArgs, exclusionArgs...)
	err = tx.QueryRowContext(ctx, `
		SELECT id, status
		FROM command_outbox
		WHERE command_name IN `+queuedLocalCommandNames+`
		  AND status IN ('pending', 'uncertain')
		  AND available_at <= ? AND expires_at > ?
	`+exclusionSQL+`
		ORDER BY available_at, created_at, id
		LIMIT 1
	`, selectArgs...).Scan(&id, &previousStatus)
	if errors.Is(err, sql.ErrNoRows) {
		// Expired commands were just made terminal in this transaction. Commit
		// them even when no command remains to claim, otherwise the rollback
		// would resurrect their pending state.
		if commitErr := tx.Commit(); commitErr != nil {
			return LocalCommand{}, false, commitErr
		}
		return LocalCommand{}, false, nil
	}
	if err != nil {
		return LocalCommand{}, false, err
	}
	claimed := sqliteTimeValue(now)
	leaseUntil := sqliteTimeValue(now.Add(lease))
	claimToken := newID("claim")
	result, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'processing', attempts = attempts + 1,
		    claimed_at = ?, lease_until = ?, claim_token = ?, last_error = NULL
		WHERE id = ? AND status = ? AND available_at <= ? AND expires_at > ?
	`, claimed, leaseUntil, claimToken, id, previousStatus, claimed, claimed)
	if err != nil {
		return LocalCommand{}, false, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		// Another dispatcher won the race. Preserve the expiration transitions
		// from this transaction before returning without a claim.
		if commitErr := tx.Commit(); commitErr != nil {
			return LocalCommand{}, false, commitErr
		}
		return LocalCommand{}, false, nil
	}
	var item LocalCommand
	if err := scanSQLiteLocalCommand(tx.QueryRowContext(ctx, sqliteLocalCommandSelect+` WHERE id = ?`, id), &item); err != nil {
		return LocalCommand{}, false, err
	}
	if item.Name == "run.start" {
		_, err = tx.ExecContext(ctx, `
			UPDATE runs
			SET status = 'starting', started_at = COALESCE(started_at, ?),
			    version = version + 1, updated_at = ?
			WHERE id = json_extract(?, '$.run_id') AND status = 'queued'
		`, claimed, claimed, string(item.Payload))
		if err != nil {
			return LocalCommand{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return LocalCommand{}, false, err
	}
	return item, true, nil
}

func sqliteCommandExclusion(ids []string) (string, []any) {
	values := make([]any, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			values = append(values, id)
		}
	}
	if len(values) == 0 {
		return "", nil
	}
	return " AND id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + ")", values
}

// RenewLocalCommandLease extends a claim only while that exact command is
// still processing and its previous lease has not expired. A zero-row update
// means the dispatcher no longer owns the command and must stop Handle rather
// than allowing concurrent redelivery.
func (s *SQLite) RenewLocalCommandLease(ctx context.Context, id, claimToken string, now time.Time, lease time.Duration) error {
	if lease <= 0 {
		return errors.New("local command lease must be positive")
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_outbox SET lease_until = ?
		WHERE id = ? AND status = 'processing' AND claim_token = ?
		  AND lease_until > ? AND expires_at > ?
	`, sqliteTimeValue(now.Add(lease)), id, claimToken, sqliteTimeValue(now), sqliteTimeValue(now))
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("local command lease is no longer owned")
	}
	return nil
}

func (s *SQLite) CompleteLocalCommand(ctx context.Context, id, claimToken string, result json.RawMessage) error {
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	if !json.Valid(result) {
		return errors.New("invalid local command result JSON")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var item LocalCommand
	if err := scanSQLiteLocalCommand(tx.QueryRowContext(ctx, sqliteLocalCommandSelect+` WHERE id = ?`, id), &item); err != nil {
		return err
	}
	now := time.Now().UTC()
	resultTag, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'completed', result = ?, completed_at = ?, last_error = NULL,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE id = ? AND status = 'processing' AND claim_token = ?
		  AND (command_name <> 'approval.decide' OR `+sqliteCurrentApprovalDecision+`)
	`, string(result), sqliteTimeValue(now), id, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := resultTag.RowsAffected(); rows != 1 {
		return errors.New("local command claim is no longer owned")
	}
	var payload struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(item.Payload, &payload)
	switch item.Name {
	case "resource.validate":
		var request nodeprotocol.ValidateResourcePayload
		var validation nodeprotocol.ValidateResourceResult
		if json.Unmarshal(item.Payload, &request) != nil || json.Unmarshal(result, &validation) != nil ||
			request.ResourceID == "" || request.ValidationVersion <= 0 ||
			validation.ResourceID != request.ResourceID || validation.ValidationVersion != request.ValidationVersion {
			return domain.Invalid("本地资源验证结果与持久化命令不匹配")
		}
		if err := setSQLiteResourceValidationTx(ctx, tx, request.ResourceID, request.ValidationVersion, validation.Valid, validation.GitDetected, now); err != nil {
			return err
		}
	case "run.stop":
		var stopped nodeprotocol.StopRunResult
		if json.Unmarshal(result, &stopped) != nil {
			return domain.Invalid("本地停止结果无效")
		}
		// A graceful native acknowledgement is not the execution's terminal.
		// Keep cancelling so force may take over and compaction can settle first.
		if !stopped.TerminalPending {
			if err := finishSQLiteRunTx(ctx, tx, payload.RunID, domain.RunCancelled, "cancelled_by_user", now, "run.finished"); err != nil {
				return err
			}
		}
	case "approval.decide":
		_, err = tx.ExecContext(ctx, `
			UPDATE runs SET status = 'running', version = version + 1, updated_at = ?
			WHERE id = ? AND status = 'waiting_approval'
			  AND NOT EXISTS (SELECT 1 FROM approvals WHERE run_id = runs.id AND status = 'pending')
			  AND NOT EXISTS (
			      SELECT 1 FROM command_outbox other
			      WHERE other.command_name = 'approval.decide'
			        AND json_extract(other.payload, '$.run_id') = runs.id
			        AND other.status IN ('pending','processing','uncertain')
			  )
		`, sqliteTimeValue(now), payload.RunID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) RetryLocalCommand(ctx context.Context, id, claimToken, lastError string, availableAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'pending', available_at = ?, claimed_at = NULL,
		    lease_until = NULL, claim_token = NULL, last_error = ?
		WHERE id = ? AND status = 'processing' AND claim_token = ?
	`, sqliteTimeValue(availableAt), lastError, id, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return errors.New("local command claim is no longer owned")
	}
	return nil
}

func (s *SQLite) MarkLocalCommandUncertain(ctx context.Context, id, claimToken, lastError string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'uncertain', available_at = ?, last_error = ?,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE id = ? AND status = 'processing' AND claim_token = ?
	`, sqliteTimeValue(time.Now().UTC()), lastError, id, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("local command claim is no longer owned")
	}
	return nil
}

func (s *SQLite) FailLocalCommand(ctx context.Context, id, claimToken, lastError string, result json.RawMessage) error {
	if len(result) > 0 && !json.Valid(result) {
		return errors.New("invalid local command result JSON")
	}
	lastError = nodeprotocol.SanitizeDiagnosticText(lastError)
	var diagnostic map[string]any
	if len(result) > 0 {
		// Do not rely on every caller already having crossed Runtime's safe
		// diagnostic boundary before storing the command result in app.db.
		clean := nodeprotocol.SanitizeDiagnosticPayload(map[string]any{"result": result})["result"]
		var err error
		result, err = json.Marshal(clean)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(result, &diagnostic)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var item LocalCommand
	if err := scanSQLiteLocalCommand(tx.QueryRowContext(ctx, sqliteLocalCommandSelect+` WHERE id = ?`, id), &item); err != nil {
		return err
	}
	now := time.Now().UTC()
	resultTag, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'failed', result = ?, completed_at = ?, last_error = ?,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE id = ? AND status = 'processing' AND claim_token = ?
	`, nullableJSON(result), sqliteTimeValue(now), lastError, id, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := resultTag.RowsAffected(); rows != 1 {
		return errors.New("local command claim is no longer owned")
	}
	if item.Name == "run.start" || item.Name == "run.stop" || item.Name == "approval.decide" {
		var payload struct {
			RunID    string `json:"run_id"`
			Provider string `json:"provider"`
		}
		_ = json.Unmarshal(item.Payload, &payload)
		if diagnostic == nil {
			diagnostic = map[string]any{}
		}
		if payload.Provider != "" {
			diagnostic["provider_id"] = payload.Provider
		}
		if err := finishSQLiteRunTx(ctx, tx, payload.RunID, domain.RunFailed, lastError, now, "run.finished", diagnostic); err != nil {
			return err
		}
	}
	if item.Name == "resource.validate" {
		var request nodeprotocol.ValidateResourcePayload
		if err := json.Unmarshal(item.Payload, &request); err != nil {
			return err
		}
		if err := setSQLiteResourceValidationTx(ctx, tx, request.ResourceID, request.ValidationVersion, false, false, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// A late result must never overwrite a renamed, deleted or revalidated row.
func setSQLiteResourceValidationTx(
	ctx context.Context, tx *sql.Tx, id string, version int64, valid, gitDetected bool, now time.Time,
) error {
	status := "invalid"
	if valid {
		status = "ready"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE project_resources SET status = ?, git_detected = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND status = 'validation_pending' AND deleted_at IS NULL
	`, status, boolInt(valid && gitDetected), sqliteTimeValue(now), id, version)
	return err
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func (s *SQLite) GetLocalCommand(ctx context.Context, id string) (LocalCommand, error) {
	var item LocalCommand
	err := scanSQLiteLocalCommand(s.db.QueryRowContext(ctx, sqliteLocalCommandSelect+` WHERE id = ?`, id), &item)
	return item, err
}
