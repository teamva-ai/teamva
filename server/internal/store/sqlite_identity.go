package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const sqliteLocalEmail = "local@teamva.invalid"

func (s *SQLite) ensureLocalIdentity(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	userID := newID("usr")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (
			id, email, display_name, status,
			version, created_at, updated_at
		) VALUES (?, ?, '本地用户', 'active', 1, ?, ?)
		ON CONFLICT(email) DO NOTHING
	`, userID, sqliteLocalEmail, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return fmt.Errorf("seed local user: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM users WHERE email = ? AND deleted_at IS NULL
	`, sqliteLocalEmail).Scan(&userID); err != nil {
		return fmt.Errorf("select local user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_preferences (user_id, locale, theme, allow_write, version, created_at, updated_at)
		VALUES (?, ?, ?, 0, 1, ?, ?)
		ON CONFLICT(user_id) DO NOTHING
	`, userID, domain.LocaleZhCN, domain.ThemeSystem, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return fmt.Errorf("seed local preferences: %w", err)
	}
	hostname, _ := os.Hostname()
	if strings.TrimSpace(hostname) == "" {
		hostname = "本机"
	}
	computerID := newID("cmp")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO computers (
			id, user_id, name, platform, architecture, fingerprint, status,
			last_seen_at, node_version, protocol_version, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'online', ?, 'embedded', 4, 1, ?, ?)
		ON CONFLICT DO NOTHING
	`, computerID, userID, hostname, runtime.GOOS, runtime.GOARCH, "local-"+userID,
		sqliteTimeValue(now), sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return fmt.Errorf("seed local computer: %w", err)
	}
	var existingComputerID string
	if err := tx.QueryRowContext(ctx, `
		SELECT computers.id
		FROM computers
		JOIN users owning_user ON owning_user.id = computers.user_id
		WHERE computers.user_id = ? AND computers.fingerprint = ?
		  AND computers.deleted_at IS NULL AND computers.status <> 'revoked'
		  AND owning_user.deleted_at IS NULL
	`, userID, "local-"+userID).Scan(&existingComputerID); err != nil {
		return fmt.Errorf("select local computer: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, computer_id, name, description, theme, version, created_at, updated_at)
		VALUES (?, ?, ?, '', 'hallmark', 1, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, domain.DefaultProjectID(existingComputerID), existingComputerID, domain.DefaultProjectName,
		sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return fmt.Errorf("seed default project: %w", err)
	}
	// Only the protected local identity's exact former default name is updated.
	// Ordinary same-name projects and all other project fields remain untouched.
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET name = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND computer_id = ? AND name = '不使用项目' AND deleted_at IS NULL
	`, domain.DefaultProjectName, sqliteTimeValue(now), domain.DefaultProjectID(existingComputerID), existingComputerID); err != nil {
		return fmt.Errorf("update default project name: %w", err)
	}
	return tx.Commit()
}

func (s *SQLite) LocalIdentity() (domain.User, domain.Computer, error) {
	ctx := context.Background()
	var user domain.User
	var computer domain.Computer
	row := s.db.QueryRowContext(ctx, `
		SELECT users.id, users.version, users.created_at, users.updated_at, users.deleted_at,
		       users.email, users.display_name, users.status,
		       computers.id, computers.version, computers.created_at, computers.updated_at,
		       computers.deleted_at, computers.user_id, computers.name, computers.note,
		       computers.platform, computers.architecture, computers.fingerprint,
		       computers.status, computers.last_seen_at, computers.node_version,
		       computers.protocol_version, computers.computer_use_enabled
		FROM users
		JOIN computers ON computers.user_id = users.id
		WHERE users.email = ? AND users.deleted_at IS NULL AND users.status = 'active'
		  AND computers.deleted_at IS NULL AND computers.status <> 'revoked'
		ORDER BY computers.created_at, computers.id LIMIT 1
	`, sqliteLocalEmail)
	if err := scanSQLiteUserComputer(row, &user, &computer); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, domain.Computer{}, errors.New("local identity is missing")
		}
		return domain.User{}, domain.Computer{}, err
	}
	return user, computer, nil
}

func (s *SQLite) GetUser(ctx context.Context, id string) (domain.User, error) {
	var user domain.User
	err := scanSQLiteUser(s.db.QueryRowContext(ctx, sqliteUserSelect+`
		WHERE users.id = ? AND users.deleted_at IS NULL
	`, id), &user)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.NotFound("用户", id)
	}
	return user, err
}

func (s *SQLite) GetUserPreferences(ctx context.Context, userID string) (domain.UserPreferences, error) {
	now := sqliteTimeValue(time.Now())
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO user_preferences (user_id, locale, theme, allow_write, version, created_at, updated_at)
		SELECT users.id, ?, ?, 0, 1, ?, ? FROM users
		WHERE users.id = ? AND users.deleted_at IS NULL
		ON CONFLICT(user_id) DO NOTHING
	`, domain.LocaleZhCN, domain.ThemeSystem, now, now, userID); err != nil {
		return domain.UserPreferences{}, err
	}
	return readUserPreferences(s.db.QueryRowContext(ctx, sqliteUserPreferencesSelect, appearanceMetadataKey(userID), userID), userID)
}

// app_metadata extends preferences without changing the SQLite baseline. The
// owning user join applies the same soft-deletion scope as user_preferences.
func appearanceMetadataKey(userID string) string { return "user_appearance:" + userID }

const sqliteUserPreferencesSelect = `
	SELECT preferences.user_id, preferences.locale, preferences.locale_explicit, preferences.theme,
	       preferences.allow_write, preferences.version, appearance.value
	FROM user_preferences preferences
	JOIN users owning_user ON owning_user.id = preferences.user_id
	LEFT JOIN app_metadata appearance ON appearance.key = ?
	WHERE preferences.user_id = ? AND owning_user.deleted_at IS NULL
`

func readUserPreferences(row scanner, userID string) (domain.UserPreferences, error) {
	item := domain.UserPreferences{Appearance: domain.Appearance{Palette: "default"}}
	var allowWrite, localeExplicit int
	var appearance sql.NullString
	err := row.Scan(&item.UserID, &item.Locale, &localeExplicit, &item.Theme, &allowWrite, &item.Version, &appearance)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserPreferences{}, domain.NotFound("用户", userID)
	}
	if err != nil {
		return domain.UserPreferences{}, err
	}
	if appearance.Valid {
		// Normalize the retired project palette only when reading stored metadata.
		// Requests and saves still use Appearance's strict current validation. This
		// projection preserves custom colors, metadata bytes and preference version.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(appearance.String), &fields); err != nil {
			return domain.UserPreferences{}, fmt.Errorf("decode appearance metadata: %w", err)
		}
		var palette string
		if err := json.Unmarshal(fields["palette"], &palette); err == nil && palette == "project" {
			fields["palette"] = json.RawMessage(`"default"`)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return domain.UserPreferences{}, fmt.Errorf("encode appearance projection: %w", err)
		}
		if err := json.Unmarshal(encoded, &item.Appearance); err != nil {
			return domain.UserPreferences{}, fmt.Errorf("decode appearance metadata: %w", err)
		}
	}
	item.AllowWrite = allowWrite != 0
	item.LocaleExplicit = localeExplicit != 0
	return item, err
}

func (s *SQLite) UpdateUserPreferences(
	ctx context.Context, userID string, expectedVersion int64, locale, theme string,
	allowWrite, localeExplicit bool,
	appearance *domain.Appearance,
) (domain.UserPreferences, error) {
	var encoded []byte
	if appearance != nil {
		if err := appearance.Validate(); err != nil {
			return domain.UserPreferences{}, err
		}
		var err error
		encoded, err = json.Marshal(appearance)
		if err != nil {
			return domain.UserPreferences{}, err
		}
	}
	if _, err := s.GetUserPreferences(ctx, userID); err != nil {
		return domain.UserPreferences{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE user_preferences
		SET locale = ?, theme = ?, allow_write = ?, locale_explicit = ?,
		    version = version + 1, updated_at = ?
		WHERE user_id = ? AND version = ?
		  AND user_id IN (
		      SELECT scoped_preferences.user_id
		      FROM user_preferences scoped_preferences
		      JOIN users owning_user ON owning_user.id = scoped_preferences.user_id
		      WHERE scoped_preferences.user_id = ? AND owning_user.deleted_at IS NULL
		  )
	`, locale, theme, boolInt(allowWrite), boolInt(localeExplicit), sqliteTimeValue(time.Now()), userID, expectedVersion, userID)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := readUserPreferences(tx.QueryRowContext(ctx, sqliteUserPreferencesSelect, appearanceMetadataKey(userID), userID), userID)
		if getErr != nil {
			return domain.UserPreferences{}, getErr
		}
		return domain.UserPreferences{}, domain.Conflict("version_conflict", "用户设置版本已变化",
			map[string]any{"current_version": current.Version})
	}
	if appearance != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_metadata (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, appearanceMetadataKey(userID), string(encoded), sqliteTimeValue(time.Now())); err != nil {
			return domain.UserPreferences{}, err
		}
	}
	updated, err := readUserPreferences(tx.QueryRowContext(ctx, sqliteUserPreferencesSelect, appearanceMetadataKey(userID), userID), userID)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.UserPreferences{}, err
	}
	return updated, nil
}

// CreateLocalSession issues a session for the pre-provisioned desktop user.
// Only SHA-256 token hashes are persisted; the raw token exists solely in this
// return value so the bootstrap handler can set its HttpOnly cookie.
func (s *SQLite) CreateLocalSession(
	ctx context.Context, userID string, idleTTL time.Duration, meta domain.SessionMeta,
) (domain.User, domain.Session, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil || user.Status != "active" {
		if err == nil {
			err = domain.NotFound("本地用户", userID)
		}
		return domain.User{}, domain.Session{}, err
	}
	token, err := randomToken()
	if err != nil {
		return domain.User{}, domain.Session{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(idleTTL)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, domain.Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if meta.DeviceID != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE web_sessions SET revoked_at = ?, updated_at = ?
			WHERE user_id = ? AND device_id = ? AND revoked_at IS NULL
			  AND julianday(idle_expires_at) <= julianday(?)
		`, sqliteTimeValue(now), sqliteTimeValue(now), userID, meta.DeviceID, sqliteTimeValue(now)); err != nil {
			return domain.User{}, domain.Session{}, err
		}
		var id, createdAtText string
		err = tx.QueryRowContext(ctx, `
			UPDATE web_sessions
			SET token_hash = ?, previous_token_hash = NULL, previous_token_expires_at = NULL,
			    idle_expires_at = ?, last_seen_at = ?, rotated_at = ?, updated_at = ?,
			    ip_hmac = ?, user_agent_hmac = ?
			WHERE user_id = ? AND device_id = ? AND revoked_at IS NULL
			  AND julianday(idle_expires_at) > julianday(?)
			RETURNING id, created_at
		`, hashToken(token), sqliteTimeValue(expiresAt), sqliteTimeValue(now), sqliteTimeValue(now),
			sqliteTimeValue(now), meta.IPHMAC, meta.UserAgentHMAC, userID, meta.DeviceID,
			sqliteTimeValue(now)).Scan(&id, &createdAtText)
		if err == nil {
			createdAt, parseErr := sqliteParseTime(createdAtText)
			if parseErr != nil {
				return domain.User{}, domain.Session{}, parseErr
			}
			if err := tx.Commit(); err != nil {
				return domain.User{}, domain.Session{}, err
			}
			return user, domain.Session{ID: id, UserID: userID, Token: token,
				CSRF: csrfTokenForSession(s.csrfKey, id), IdleExpiresAt: expiresAt, CreatedAt: createdAt}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, domain.Session{}, err
		}
	}
	sessionID := newID("ses")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO web_sessions (
			id, user_id, token_hash, idle_expires_at, created_at, updated_at,
			rotated_at, last_seen_at, device_id, ip_hmac, user_agent_hmac
		) SELECT ?, users.id, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM users WHERE users.id = ? AND users.deleted_at IS NULL AND users.status = 'active'
	`, sessionID, hashToken(token), sqliteTimeValue(expiresAt), sqliteTimeValue(now),
		sqliteTimeValue(now), sqliteTimeValue(now), sqliteTimeValue(now), meta.DeviceID,
		meta.IPHMAC, meta.UserAgentHMAC, userID); err != nil {
		return domain.User{}, domain.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.User{}, domain.Session{}, err
	}
	return user, domain.Session{ID: sessionID, UserID: userID, Token: token,
		CSRF: csrfTokenForSession(s.csrfKey, sessionID), IdleExpiresAt: expiresAt, CreatedAt: now}, nil
}

func (s *SQLite) AuthenticateSession(
	ctx context.Context,
	token string,
	idleTTL time.Duration,
	rotationInterval time.Duration,
	previousTokenTTL time.Duration,
) (domain.SessionAuthentication, error) {
	now := time.Now().UTC()
	candidate, err := randomToken()
	if err != nil {
		return domain.SessionAuthentication{}, err
	}
	tokenHash := hashToken(token)
	candidateHash := hashToken(candidate)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SessionAuthentication{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var info domain.WebSessionInfo
	var userID, createdAt, rotatedAt, lastSeenAt, idleExpiresAt string
	var rotated, remainsCurrent int
	err = tx.QueryRowContext(ctx, `
		UPDATE web_sessions
		SET previous_token_hash = CASE
		        WHEN token_hash = @token_hash
		         AND julianday(rotated_at) <= julianday(@rotation_cutoff)
		        THEN token_hash ELSE previous_token_hash END,
		    previous_token_expires_at = CASE
		        WHEN token_hash = @token_hash
		         AND julianday(rotated_at) <= julianday(@rotation_cutoff)
		        THEN @previous_expires ELSE previous_token_expires_at END,
		    token_hash = CASE
		        WHEN token_hash = @token_hash
		         AND julianday(rotated_at) <= julianday(@rotation_cutoff)
		        THEN @candidate_hash ELSE token_hash END,
		    rotated_at = CASE
		        WHEN token_hash = @token_hash
		         AND julianday(rotated_at) <= julianday(@rotation_cutoff)
		        THEN @now ELSE rotated_at END,
		    last_seen_at = @now,
		    idle_expires_at = @idle_expires,
		    updated_at = @now
		WHERE (token_hash = @token_hash OR
		       (previous_token_hash = @token_hash AND julianday(previous_token_expires_at) > julianday(@now)))
		  AND revoked_at IS NULL AND julianday(idle_expires_at) > julianday(@now)
		  AND EXISTS (
		      SELECT 1 FROM users owning_user
		      WHERE owning_user.id = web_sessions.user_id
		        AND owning_user.status = 'active' AND owning_user.deleted_at IS NULL
		  )
		RETURNING user_id, id, created_at, rotated_at,
		          COALESCE(last_seen_at, created_at), idle_expires_at,
		          device_id,
		          token_hash = @candidate_hash, token_hash = @token_hash
	`,
		sql.Named("token_hash", tokenHash),
		sql.Named("candidate_hash", candidateHash),
		sql.Named("rotation_cutoff", sqliteTimeValue(now.Add(-rotationInterval))),
		sql.Named("previous_expires", sqliteTimeValue(now.Add(previousTokenTTL))),
		sql.Named("idle_expires", sqliteTimeValue(now.Add(idleTTL))),
		sql.Named("now", sqliteTimeValue(now)),
	).Scan(&userID, &info.ID, &createdAt, &rotatedAt, &lastSeenAt, &idleExpiresAt,
		&info.DeviceID, &rotated, &remainsCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionAuthentication{}, domain.Unauthorized("invalid_session", "登录状态无效或已过期")
	}
	if err != nil {
		return domain.SessionAuthentication{}, err
	}
	if err := parseSQLiteSessionTimes(&info, createdAt, rotatedAt, lastSeenAt, idleExpiresAt); err != nil {
		return domain.SessionAuthentication{}, err
	}
	var user domain.User
	if err := scanSQLiteUser(tx.QueryRowContext(ctx, sqliteUserSelect+`
		WHERE users.id = ? AND users.status = 'active' AND users.deleted_at IS NULL
	`, userID), &user); err != nil {
		return domain.SessionAuthentication{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SessionAuthentication{}, err
	}
	match := domain.SessionTokenMatchPrevious
	if rotated != 0 || remainsCurrent != 0 {
		match = domain.SessionTokenMatchCurrent
	}
	if rotated == 0 {
		candidate = ""
	}
	return domain.SessionAuthentication{
		User: user, Session: info, TokenMatch: match, RotatedToken: candidate,
	}, nil
}

func (s *SQLite) RevokeSession(ctx context.Context, token string) error {
	now := sqliteTimeValue(time.Now())
	hash := hashToken(token)
	result, err := s.db.ExecContext(ctx, `
		UPDATE web_sessions SET revoked_at = ?, updated_at = ?
		WHERE (token_hash = ? OR (previous_token_hash = ? AND julianday(previous_token_expires_at) > julianday(?)))
		  AND revoked_at IS NULL AND julianday(idle_expires_at) > julianday(?)
	`, now, now, hash, hash, now, now)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.Unauthorized("invalid_session", "登录状态无效或已过期")
	}
	return nil
}

func (s *SQLite) ListSessions(ctx context.Context, userID, currentSessionID string) ([]domain.WebSessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sessions.id, sessions.created_at, sessions.rotated_at,
		       COALESCE(sessions.last_seen_at, sessions.created_at), sessions.idle_expires_at,
		       sessions.device_id
		FROM web_sessions sessions
		JOIN users owning_user ON owning_user.id = sessions.user_id
		WHERE sessions.user_id = ? AND sessions.revoked_at IS NULL
		  AND julianday(sessions.idle_expires_at) > julianday(?)
		  AND owning_user.deleted_at IS NULL
		ORDER BY sessions.created_at DESC, sessions.id DESC
	`, userID, sqliteTimeValue(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.WebSessionInfo, 0)
	for rows.Next() {
		var item domain.WebSessionInfo
		if err := scanSQLiteSessionInfo(rows, &item); err != nil {
			return nil, err
		}
		item.Current = item.ID == currentSessionID
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) RevokeSessionByID(ctx context.Context, userID, sessionID string) error {
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE web_sessions SET revoked_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL
		  AND id IN (SELECT scoped_session.id FROM web_sessions scoped_session
		             JOIN users owning_user ON owning_user.id = scoped_session.user_id
		             WHERE scoped_session.id = ? AND scoped_session.user_id = ?
		               AND owning_user.deleted_at IS NULL)
	`, now, now, sessionID, userID, sessionID, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.NotFound("会话", sessionID)
	}
	return nil
}

func (s *SQLite) RevokeOtherSessions(ctx context.Context, userID, keepSessionID string) error {
	now := sqliteTimeValue(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE web_sessions SET revoked_at = ?, updated_at = ?
		WHERE user_id = ? AND id <> ? AND revoked_at IS NULL
		  AND user_id IN (SELECT scoped_session.user_id FROM web_sessions scoped_session
		                  JOIN users owning_user ON owning_user.id = scoped_session.user_id
		                  WHERE scoped_session.user_id = ? AND owning_user.deleted_at IS NULL)
	`, now, now, userID, keepSessionID, userID)
	return err
}

func (s *SQLite) VerifySessionCSRF(ctx context.Context, sessionID, csrf string) bool {
	if csrf == "" {
		return false
	}
	_, err := s.GetSessionCSRF(ctx, sessionID)
	return err == nil && constantTimeEqual(csrfTokenForSession(s.csrfKey, sessionID), csrf)
}

func (s *SQLite) GetSessionCSRF(ctx context.Context, sessionID string) (string, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM web_sessions sessions
		JOIN users owning_user ON owning_user.id = sessions.user_id
		WHERE sessions.id = ? AND sessions.revoked_at IS NULL
		  AND julianday(sessions.idle_expires_at) > julianday(?)
		  AND owning_user.status = 'active' AND owning_user.deleted_at IS NULL)
	`, sessionID, sqliteTimeValue(time.Now())).Scan(&active)
	if err != nil {
		return "", err
	}
	if !active {
		return "", domain.Unauthorized("invalid_session", "登录状态无效或已过期")
	}
	return csrfTokenForSession(s.csrfKey, sessionID), nil
}

func (s *SQLite) HMACValue(value string) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

const sqliteUserSelect = `
	SELECT users.id, users.version, users.created_at, users.updated_at, users.deleted_at,
	       users.email, users.display_name, users.status
	FROM users
`

func scanSQLiteUser(row scanner, user *domain.User) error {
	var createdAt, updatedAt string
	var deletedAt sql.NullString
	if err := row.Scan(&user.ID, &user.Version, &createdAt, &updatedAt, &deletedAt,
		&user.Email, &user.DisplayName, &user.Status); err != nil {
		return err
	}
	var err error
	if user.CreatedAt, err = sqliteParseTime(createdAt); err != nil {
		return err
	}
	if user.UpdatedAt, err = sqliteParseTime(updatedAt); err != nil {
		return err
	}
	if user.DeletedAt, err = sqliteParseNullTime(deletedAt); err != nil {
		return err
	}
	return err
}

func scanSQLiteUserComputer(row scanner, user *domain.User, computer *domain.Computer) error {
	var userCreated, userUpdated, computerCreated, computerUpdated string
	var userDeleted sql.NullString
	var computerDeleted, lastSeen sql.NullString
	if err := row.Scan(&user.ID, &user.Version, &userCreated, &userUpdated, &userDeleted,
		&user.Email, &user.DisplayName, &user.Status,
		&computer.ID, &computer.Version, &computerCreated, &computerUpdated, &computerDeleted,
		&computer.UserID, &computer.Name, &computer.Note, &computer.Platform, &computer.Architecture,
		&computer.Fingerprint, &computer.Status, &lastSeen, &computer.NodeVersion,
		&computer.ProtocolVersion, &computer.ComputerUseEnabled); err != nil {
		return err
	}
	var err error
	user.CreatedAt, err = sqliteParseTime(userCreated)
	if err != nil {
		return err
	}
	user.UpdatedAt, err = sqliteParseTime(userUpdated)
	if err != nil {
		return err
	}
	user.DeletedAt, err = sqliteParseNullTime(userDeleted)
	if err != nil {
		return err
	}
	computer.CreatedAt, err = sqliteParseTime(computerCreated)
	if err != nil {
		return err
	}
	computer.UpdatedAt, err = sqliteParseTime(computerUpdated)
	if err != nil {
		return err
	}
	computer.DeletedAt, err = sqliteParseNullTime(computerDeleted)
	if err != nil {
		return err
	}
	computer.LastSeenAt, err = sqliteParseNullTime(lastSeen)
	return err
}

func scanSQLiteSessionInfo(row scanner, info *domain.WebSessionInfo) error {
	var created, rotated, lastSeen, idle string
	if err := row.Scan(&info.ID, &created, &rotated, &lastSeen, &idle,
		&info.DeviceID); err != nil {
		return err
	}
	return parseSQLiteSessionTimes(info, created, rotated, lastSeen, idle)
}

func parseSQLiteSessionTimes(
	info *domain.WebSessionInfo,
	created, rotated, lastSeen, idle string,
) error {
	var err error
	info.CreatedAt, err = sqliteParseTime(created)
	if err != nil {
		return err
	}
	info.RotatedAt, err = sqliteParseTime(rotated)
	if err != nil {
		return err
	}
	last, err := sqliteParseTime(lastSeen)
	if err != nil {
		return err
	}
	info.LastSeenAt = &last
	info.IdleExpiresAt, err = sqliteParseTime(idle)
	if err != nil {
		return err
	}
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
