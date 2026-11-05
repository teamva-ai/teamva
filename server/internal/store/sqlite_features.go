package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Memory、Policy、ScheduledTask 与 Audit 均直接以 SQLite 为数据源。
// 项目作用域查询必须沿 projects -> computers -> users 校验租户，不能依赖
// 所有功能数据均直接使用规范化 SQLite 表。

const sqliteMemorySelect = `
	SELECT memories.id, memories.current_version, memories.created_at,
	       memories.updated_at, memories.deleted_at, memories.project_id,
	       memories.memory_key, memories.kind, memories.title, memories.content,
	       memories.source_type, COALESCE(memories.source_ref, '')
	FROM project_memory_entries memories
`

const sqliteMemoryOwnership = `
	JOIN projects owning_project ON owning_project.id = memories.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func scanSQLiteMemory(row scanner, item *domain.ProjectMemoryEntry) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(
		&item.ID, &item.Version, &created, &updated, &deleted, &item.ProjectID,
		&item.Key, &item.Kind, &item.Title, &item.Content, &item.SourceType, &item.SourceRef,
	); err != nil {
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
	return err
}

func (s *SQLite) ListMemories(ctx context.Context, userID, projectID string) ([]domain.ProjectMemoryEntry, error) {
	return s.listMemories(ctx, userID, projectID, false)
}

func (s *SQLite) ListArchivedMemories(ctx context.Context, userID, projectID string) ([]domain.ProjectMemoryEntry, error) {
	return s.listMemories(ctx, userID, projectID, true)
}

func (s *SQLite) listMemories(ctx context.Context, userID, projectID string, archived bool) ([]domain.ProjectMemoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, sqliteMemorySelect+sqliteMemoryOwnership+`
		WHERE memories.project_id = ? AND owning_computer.user_id = ?
		  AND (memories.deleted_at IS NOT NULL) = ? AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY julianday(memories.created_at), memories.id
	`, projectID, userID, boolInt(archived))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ProjectMemoryEntry, 0)
	for rows.Next() {
		var item domain.ProjectMemoryEntry
		if err := scanSQLiteMemory(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireSQLiteProjectOwner(ctx, s.db, userID, projectID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLite) CreateMemory(ctx context.Context, userID string, input domain.ProjectMemoryEntry, actor string) (domain.ProjectMemoryEntry, error) {
	autoTitle := strings.TrimSpace(input.Title) == ""
	if autoTitle {
		input.Title = "新记忆"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := sqliteTimeValue(time.Now())
	var item domain.ProjectMemoryEntry
	err = scanSQLiteMemory(tx.QueryRowContext(ctx, `
		INSERT INTO project_memory_entries (
			id, project_id, memory_key, kind, title, content, source_type, source_ref,
			current_version, created_at, updated_at
		)
		SELECT ?, owning_project.id, ?, ?, ?, ?, ?, NULLIF(?, ''), 1, ?, ?
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		RETURNING id, current_version, created_at, updated_at, deleted_at, project_id,
		          memory_key, kind, title, content, source_type, COALESCE(source_ref, '')
	`, newID("mem"), input.Key, input.Kind, input.Title, input.Content, input.SourceType,
		input.SourceRef, now, now, input.ProjectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectMemoryEntry{}, domain.NotFound("项目", input.ProjectID)
	}
	if err != nil {
		return domain.ProjectMemoryEntry{}, mapSQLiteMemoryConflict(err, input.Key)
	}
	if err := insertSQLiteMemoryVersion(ctx, tx, item, actor); err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "memory", item.ID); err != nil {
			return domain.ProjectMemoryEntry{}, err
		}
		if strings.TrimSpace(item.Content) != "" {
			if err := queueAutoTitleTx(ctx, tx, userID, "memory", item.ID, item.Content); err != nil {
				return domain.ProjectMemoryEntry{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	return item, nil
}

func (s *SQLite) GetMemory(ctx context.Context, userID, projectID, id string) (domain.ProjectMemoryEntry, error) {
	var item domain.ProjectMemoryEntry
	err := scanSQLiteMemory(s.db.QueryRowContext(ctx, sqliteMemorySelect+sqliteMemoryOwnership+`
		WHERE memories.id = ? AND memories.project_id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, projectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectMemoryEntry{}, domain.NotFound("项目记忆", id)
	}
	return item, err
}

// titleEdited defaults to true for direct callers that already provide a title.
// HTTP PATCH passes false only when title was omitted, retaining the stored title
// atomically rather than reading and copying a potentially stale title.
func (s *SQLite) UpdateMemory(ctx context.Context, userID, projectID, id string, expectedVersion int64, title, content, actor string, titleEdited ...bool) (domain.ProjectMemoryEntry, error) {
	editTitle := len(titleEdited) == 0 || titleEdited[0]
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var item domain.ProjectMemoryEntry
	err = scanSQLiteMemory(tx.QueryRowContext(ctx, `
		UPDATE project_memory_entries AS memories
		SET title = CASE WHEN ? THEN ? ELSE title END, content = ?, current_version = current_version + 1, updated_at = ?
		WHERE memories.id = ? AND memories.project_id = ? AND memories.current_version = ?
		  AND memories.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = memories.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		RETURNING id, current_version, created_at, updated_at, deleted_at, project_id,
		          memory_key, kind, title, content, source_type, COALESCE(source_ref, '')
	`, editTitle, title, content, sqliteTimeValue(time.Now()), id, projectID, expectedVersion, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		current, lookupErr := getSQLiteMemoryInTx(ctx, tx, userID, projectID, id)
		if lookupErr != nil {
			return domain.ProjectMemoryEntry{}, lookupErr
		}
		if current.DeletedAt != nil {
			return domain.ProjectMemoryEntry{}, domain.NotFound("项目记忆", id)
		}
		return domain.ProjectMemoryEntry{}, versionConflict(current.Version)
	}
	if err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	if err := insertSQLiteMemoryVersion(ctx, tx, item, actor); err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	if editTitle {
		if err := manualAutoTitleTx(ctx, tx, "memory", id); err != nil {
			return domain.ProjectMemoryEntry{}, err
		}
	} else if strings.TrimSpace(item.Content) != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "memory", id, item.Content); err != nil {
			return domain.ProjectMemoryEntry{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	return item, nil
}

func (s *SQLite) ArchiveMemory(ctx context.Context, userID, projectID, id string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE project_memory_entries AS memories
		SET deleted_at = ?, current_version = current_version + 1, updated_at = ?
		WHERE memories.id = ? AND memories.project_id = ? AND memories.current_version = ?
		  AND memories.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = memories.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, now, now, id, projectID, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, lookupErr := getSQLiteMemoryInTx(ctx, tx, userID, projectID, id)
		if lookupErr != nil {
			return lookupErr
		}
		return versionConflict(current.Version)
	}
	if err := cancelAutoTitleTx(ctx, tx, "memory", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) RestoreMemory(ctx context.Context, userID, projectID, id string, expectedVersion int64) (domain.ProjectMemoryEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ProjectMemoryEntry
	err = scanSQLiteMemory(tx.QueryRowContext(ctx, `
		UPDATE project_memory_entries AS memories
		SET deleted_at = NULL, current_version = current_version + 1, updated_at = ?
		WHERE memories.id = ? AND memories.project_id = ? AND memories.current_version = ?
		  AND memories.deleted_at IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = memories.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		RETURNING id, current_version, created_at, updated_at, deleted_at, project_id,
		          memory_key, kind, title, content, source_type, COALESCE(source_ref, '')
	`, sqliteTimeValue(time.Now()), id, projectID, expectedVersion, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		current, lookupErr := getSQLiteMemoryInTx(ctx, tx, userID, projectID, id)
		if lookupErr != nil {
			return domain.ProjectMemoryEntry{}, lookupErr
		}
		return domain.ProjectMemoryEntry{}, versionConflict(current.Version)
	}
	if err != nil {
		return domain.ProjectMemoryEntry{}, mapSQLiteMemoryConflict(err, item.Key)
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectMemoryEntry{}, err
	}
	return item, nil
}

func (s *SQLite) ListMemoryVersions(ctx context.Context, userID, projectID, id string) ([]domain.ProjectMemoryVersion, error) {
	if _, err := s.GetMemory(ctx, userID, projectID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT versions.id, versions.memory_id, versions.version, versions.title,
		       versions.content, versions.actor_type, COALESCE(versions.actor_ref, ''),
		       versions.created_at
		FROM project_memory_versions versions
		JOIN project_memory_entries memories ON memories.id = versions.memory_id
		JOIN projects owning_project ON owning_project.id = memories.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE versions.memory_id = ? AND memories.project_id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY versions.version
	`, id, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ProjectMemoryVersion, 0)
	for rows.Next() {
		var item domain.ProjectMemoryVersion
		var created string
		if err := rows.Scan(&item.ID, &item.MemoryID, &item.Version, &item.Title,
			&item.Content, &item.ActorType, &item.ActorRef, &created); err != nil {
			return nil, err
		}
		item.CreatedAt, err = sqliteParseTime(created)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getSQLiteMemoryInTx(ctx context.Context, tx *sql.Tx, userID, projectID, id string) (domain.ProjectMemoryEntry, error) {
	var item domain.ProjectMemoryEntry
	err := scanSQLiteMemory(tx.QueryRowContext(ctx, sqliteMemorySelect+sqliteMemoryOwnership+`
		WHERE memories.id = ? AND memories.project_id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, projectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectMemoryEntry{}, domain.NotFound("项目记忆", id)
	}
	return item, err
}

func insertSQLiteMemoryVersion(ctx context.Context, tx *sql.Tx, item domain.ProjectMemoryEntry, actor string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO project_memory_versions (
			id, memory_id, version, title, content, actor_type, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, newID("memv"), item.ID, item.Version, item.Title, item.Content, actor,
		sqliteTimeValue(time.Now()))
	return err
}

func mapSQLiteMemoryConflict(err error, key string) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") &&
		strings.Contains(strings.ToLower(err.Error()), "project_memory_entries") {
		return domain.Conflict("memory_key_conflict", "记忆 key 已存在", map[string]any{"key": key})
	}
	return err
}

const sqlitePolicySelect = `
	SELECT policies.id, policies.version, policies.created_at, policies.updated_at,
	       policies.deleted_at, policies.project_id, policies.action_kind,
	       policies.effect, policies.conditions, policies.expires_at, policies.enabled
	FROM policy_rules policies
`

const sqlitePolicyOwnership = `
	JOIN projects owning_project ON owning_project.id = policies.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func scanSQLitePolicy(row scanner, item *domain.PolicyRule) error {
	var created, updated, conditions string
	var deleted, expires sql.NullString
	var enabled int
	if err := row.Scan(
		&item.ID, &item.Version, &created, &updated, &deleted, &item.ProjectID,
		&item.ActionKind, &item.Effect, &conditions, &expires, &enabled,
	); err != nil {
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
	item.ExpiresAt, err = sqliteParseNullTime(expires)
	if err != nil {
		return err
	}
	item.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(conditions), &item.Conditions); err != nil {
		return err
	}
	if item.Conditions == nil {
		item.Conditions = map[string]any{}
	}
	return nil
}

func (s *SQLite) ListPolicyRules(ctx context.Context, userID, projectID string) ([]domain.PolicyRule, error) {
	rows, err := s.db.QueryContext(ctx, sqlitePolicySelect+sqlitePolicyOwnership+`
		WHERE policies.project_id = ? AND owning_computer.user_id = ?
		  AND policies.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY julianday(policies.created_at), policies.id
	`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.PolicyRule, 0)
	for rows.Next() {
		var item domain.PolicyRule
		if err := scanSQLitePolicy(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireSQLiteProjectOwner(ctx, s.db, userID, projectID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLite) CreatePolicyRule(ctx context.Context, userID string, input domain.PolicyRule) (domain.PolicyRule, error) {
	conditions, err := json.Marshal(input.Conditions)
	if err != nil {
		return domain.PolicyRule{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PolicyRule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := sqliteTimeValue(time.Now())
	var item domain.PolicyRule
	err = scanSQLitePolicy(tx.QueryRowContext(ctx, `
		INSERT INTO policy_rules (
			id, project_id, action_kind, effect, conditions, expires_at, enabled,
			version, created_at, updated_at
		)
		SELECT ?, owning_project.id, ?, ?, ?, ?, 1, 1, ?, ?
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, project_id,
		          action_kind, effect, conditions, expires_at, enabled
	`, newID("pol"), input.ActionKind, input.Effect, string(conditions),
		nullableSQLiteTime(input.ExpiresAt), now, now, input.ProjectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PolicyRule{}, domain.NotFound("项目", input.ProjectID)
	}
	if err != nil {
		return domain.PolicyRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PolicyRule{}, err
	}
	return item, nil
}

func (s *SQLite) UpdatePolicyRule(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.PolicyRule)) (domain.PolicyRule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PolicyRule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getSQLitePolicyInTx(ctx, tx, userID, id)
	if err != nil {
		return domain.PolicyRule{}, err
	}
	if current.Version != expectedVersion {
		return domain.PolicyRule{}, versionConflict(current.Version)
	}
	update(&current)
	conditions, err := json.Marshal(current.Conditions)
	if err != nil {
		return domain.PolicyRule{}, err
	}
	var item domain.PolicyRule
	err = scanSQLitePolicy(tx.QueryRowContext(ctx, `
		UPDATE policy_rules AS policies
		SET action_kind = ?, effect = ?, conditions = ?, expires_at = ?, enabled = ?,
		    version = version + 1, updated_at = ?
		WHERE policies.id = ? AND policies.version = ? AND policies.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = policies.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		RETURNING id, version, created_at, updated_at, deleted_at, project_id,
		          action_kind, effect, conditions, expires_at, enabled
	`, current.ActionKind, current.Effect, string(conditions), nullableSQLiteTime(current.ExpiresAt),
		boolInt(current.Enabled), sqliteTimeValue(time.Now()), id, expectedVersion, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		latest, lookupErr := getSQLitePolicyInTx(ctx, tx, userID, id)
		if lookupErr != nil {
			return domain.PolicyRule{}, lookupErr
		}
		return domain.PolicyRule{}, versionConflict(latest.Version)
	}
	if err != nil {
		return domain.PolicyRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PolicyRule{}, err
	}
	return item, nil
}

func (s *SQLite) DeletePolicyRule(ctx context.Context, userID, id string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE policy_rules AS policies
		SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE policies.id = ? AND policies.version = ? AND policies.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = policies.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, now, now, id, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, lookupErr := getSQLitePolicyInTx(ctx, tx, userID, id)
		if lookupErr != nil {
			return lookupErr
		}
		return versionConflict(current.Version)
	}
	return tx.Commit()
}

func getSQLitePolicyInTx(ctx context.Context, tx *sql.Tx, userID, id string) (domain.PolicyRule, error) {
	var item domain.PolicyRule
	err := scanSQLitePolicy(tx.QueryRowContext(ctx, sqlitePolicySelect+sqlitePolicyOwnership+`
		WHERE policies.id = ? AND owning_computer.user_id = ?
		  AND policies.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PolicyRule{}, domain.NotFound("策略", id)
	}
	return item, err
}

const sqliteScheduledTaskSelect = `
	SELECT tasks.id, tasks.version, tasks.created_at, tasks.updated_at, tasks.deleted_at,
	       tasks.project_id,
	       tasks.name, tasks.enabled, tasks.schedule_kind,
	       tasks.schedule_expression, tasks.timezone, tasks.prompt,
	       COALESCE(tasks.agent_id, ''), tasks.concurrency_policy, tasks.misfire_policy,
	       tasks.next_run_at, tasks.dispatch_attempts, tasks.dispatch_retry_at,
	       COALESCE(tasks.last_dispatch_error, ''), COALESCE(schedule_config.value, '')
	FROM scheduled_tasks tasks
	LEFT JOIN app_metadata schedule_config ON schedule_config.key = 'scheduled_task_config:' || tasks.id
`

const sqliteScheduledTaskOwnership = `
	JOIN projects owning_project ON owning_project.id = tasks.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func scanSQLiteScheduledTask(row scanner, item *domain.ScheduledTask) error {
	var created, updated, prompt, profile string
	var deleted, nextRun, dispatchRetry sql.NullString
	var enabled int
	if err := row.Scan(
		&item.ID, &item.Version, &created, &updated, &deleted, &item.ProjectID,
		&item.Name, &enabled, &item.ScheduleKind, &item.ScheduleExpression, &item.Timezone,
		&prompt, &item.AgentID, &item.ConcurrencyPolicy, &item.MisfirePolicy, &nextRun,
		&item.DispatchAttempts, &dispatchRetry, &item.LastDispatchError, &profile,
	); err != nil {
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
	item.NextRunAt, err = sqliteParseNullTime(nextRun)
	if err != nil {
		return err
	}
	item.DispatchRetryAt, err = sqliteParseNullTime(dispatchRetry)
	if err != nil {
		return err
	}
	item.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(prompt), &item.Prompt); err != nil {
		return err
	}
	return hydrateSQLiteScheduleConfig(item, profile)
}

func (s *SQLite) ListScheduledTasks(ctx context.Context, userID, projectID string) ([]domain.ScheduledTask, error) {
	rows, err := s.db.QueryContext(ctx, sqliteScheduledTaskSelect+sqliteScheduledTaskOwnership+`
		WHERE tasks.project_id = ? AND owning_computer.user_id = ?
		  AND tasks.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY julianday(tasks.created_at), tasks.id
	`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledTask, 0)
	for rows.Next() {
		var item domain.ScheduledTask
		if err := scanSQLiteScheduledTask(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireSQLiteProjectOwner(ctx, s.db, userID, projectID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLite) CreateScheduledTask(ctx context.Context, userID string, input domain.ScheduledTask) (domain.ScheduledTask, error) {
	autoTitle := strings.TrimSpace(input.Name) == ""
	if autoTitle {
		input.Name = "新定时任务"
	}
	if input.IssueMode == "" && (input.ScheduleKind == "daily" || input.ScheduleKind == "weekly") {
		input.IssueMode = "new"
	}
	if err := domain.NormalizeScheduledTaskConfig(&input); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := validateSQLiteScheduleCalendar(input); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := validateTextOnlyRunInput(input.Prompt); err != nil {
		return domain.ScheduledTask{}, err
	}
	prompt, err := json.Marshal(input.Prompt)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	input.Enabled = true
	if err := requireSQLiteProjectOwner(ctx, tx, userID, input.ProjectID); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := validateSQLiteScheduleProfile(ctx, tx, userID, input); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := requireSQLiteScheduleAgent(ctx, tx, userID, input.ProjectID, input.AgentID); err != nil {
		return domain.ScheduledTask{}, err
	}
	now := sqliteTimeValue(time.Now())
	kind, expression, profile, err := encodeSQLiteScheduleConfig(input)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	var item domain.ScheduledTask
	err = scanSQLiteScheduledTask(tx.QueryRowContext(ctx, `
		INSERT INTO scheduled_tasks (
			id, project_id, name, enabled, schedule_kind, schedule_expression, timezone,
			prompt, agent_id, concurrency_policy, misfire_policy, next_run_at,
			version, created_at, updated_at
		)
		SELECT ?, owning_project.id, ?, 1, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, 1, ?, ?
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, project_id,
		          name, enabled, schedule_kind, schedule_expression, timezone, prompt,
		          COALESCE(agent_id, ''), concurrency_policy, misfire_policy, next_run_at,
		          dispatch_attempts, dispatch_retry_at, COALESCE(last_dispatch_error, ''), ?
	`, newID("sched"), input.Name, kind, expression,
		input.Timezone, string(prompt), input.AgentID, input.ConcurrencyPolicy,
		input.MisfirePolicy, nullableSQLiteTime(input.NextRunAt), now, now,
		input.ProjectID, userID, profile), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledTask{}, domain.NotFound("项目", input.ProjectID)
	}
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := saveSQLiteScheduleConfig(ctx, tx, item.ID, profile); err != nil {
		return domain.ScheduledTask{}, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "schedule", item.ID); err != nil {
			return domain.ScheduledTask{}, err
		}
		if seed := sqliteScheduleTitleSeed(item.Prompt); seed != "" {
			if err := queueAutoTitleTx(ctx, tx, userID, "schedule", item.ID, seed); err != nil {
				return domain.ScheduledTask{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledTask{}, err
	}
	return item, nil
}

func (s *SQLite) GetScheduledTask(ctx context.Context, userID, id string) (domain.ScheduledTask, error) {
	var item domain.ScheduledTask
	err := scanSQLiteScheduledTask(s.db.QueryRowContext(ctx, sqliteScheduledTaskSelect+sqliteScheduledTaskOwnership+`
		WHERE tasks.id = ? AND owning_computer.user_id = ?
		  AND tasks.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledTask{}, domain.NotFound("计划任务", id)
	}
	return item, err
}

func (s *SQLite) UpdateScheduledTask(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.ScheduledTask)) (domain.ScheduledTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getSQLiteScheduledTaskInTx(ctx, tx, userID, id)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	if current.Version != expectedVersion {
		return domain.ScheduledTask{}, versionConflict(current.Version)
	}
	previousName := current.Name
	update(&current)
	if err := domain.NormalizeScheduledTaskConfig(&current); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := validateSQLiteScheduleCalendar(current); err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := validateTextOnlyRunInput(current.Prompt); err != nil {
		return domain.ScheduledTask{}, err
	}
	// Emergency pause must remain available even after the selected Agent was
	// disabled/deleted. Resume and active edits still require a valid writer.
	if current.Enabled {
		if err := requireSQLiteScheduleAgent(ctx, tx, userID, current.ProjectID, current.AgentID); err != nil {
			return domain.ScheduledTask{}, err
		}
	}
	if err := validateSQLiteScheduleProfile(ctx, tx, userID, current); err != nil {
		return domain.ScheduledTask{}, err
	}
	kind, expression, profile, err := encodeSQLiteScheduleConfig(current)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	prompt, err := json.Marshal(current.Prompt)
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	var item domain.ScheduledTask
	err = scanSQLiteScheduledTask(tx.QueryRowContext(ctx, `
		UPDATE scheduled_tasks AS tasks
		SET name = ?, enabled = ?, schedule_kind = ?, schedule_expression = ?, timezone = ?,
		    prompt = ?, agent_id = NULLIF(?, ''), concurrency_policy = ?, misfire_policy = ?,
		    next_run_at = ?, dispatch_attempts = 0, dispatch_retry_at = NULL,
		    last_dispatch_error = NULL, version = version + 1, updated_at = ?
		WHERE tasks.id = ? AND tasks.version = ? AND tasks.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_project.id = tasks.project_id AND owning_computer.user_id = ?
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		RETURNING id, version, created_at, updated_at, deleted_at, project_id,
		          name, enabled, schedule_kind, schedule_expression, timezone, prompt,
		          COALESCE(agent_id, ''), concurrency_policy, misfire_policy, next_run_at,
		          dispatch_attempts, dispatch_retry_at, COALESCE(last_dispatch_error, ''), ?
	`, current.Name, boolInt(current.Enabled), kind, expression,
		current.Timezone, string(prompt), current.AgentID, current.ConcurrencyPolicy,
		current.MisfirePolicy, nullableSQLiteTime(current.NextRunAt), sqliteTimeValue(time.Now()),
		id, expectedVersion, userID, profile), &item)
	if errors.Is(err, sql.ErrNoRows) {
		latest, lookupErr := getSQLiteScheduledTaskInTx(ctx, tx, userID, id)
		if lookupErr != nil {
			return domain.ScheduledTask{}, lookupErr
		}
		return domain.ScheduledTask{}, versionConflict(latest.Version)
	}
	if err != nil {
		return domain.ScheduledTask{}, err
	}
	if err := saveSQLiteScheduleConfig(ctx, tx, item.ID, profile); err != nil {
		return domain.ScheduledTask{}, err
	}
	if current.TitleEdited || current.Name != previousName {
		if err := manualAutoTitleTx(ctx, tx, "schedule", id); err != nil {
			return domain.ScheduledTask{}, err
		}
	} else if seed := sqliteScheduleTitleSeed(item.Prompt); seed != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "schedule", id, seed); err != nil {
			return domain.ScheduledTask{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledTask{}, err
	}
	return item, nil
}

func (s *SQLite) ListScheduledTaskRuns(ctx context.Context, userID, id string) ([]domain.ScheduledTaskRun, error) {
	if _, err := s.GetScheduledTask(ctx, userID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_runs.id, task_runs.scheduled_task_id, COALESCE(task_runs.run_id, ''),
		       task_runs.trigger_key, task_runs.scheduled_for, task_runs.status,
		       task_runs.created_at, COALESCE(execution_run.issue_id, '')
		FROM scheduled_task_runs task_runs
		LEFT JOIN runs execution_run ON execution_run.id = task_runs.run_id
		JOIN scheduled_tasks tasks ON tasks.id = task_runs.scheduled_task_id
		JOIN projects owning_project ON owning_project.id = tasks.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE task_runs.scheduled_task_id = ? AND owning_computer.user_id = ?
		  AND tasks.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY julianday(task_runs.scheduled_for) DESC, task_runs.id
	`, id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledTaskRun, 0)
	for rows.Next() {
		var item domain.ScheduledTaskRun
		var scheduledFor, created string
		if err := rows.Scan(&item.ID, &item.ScheduledTaskID, &item.RunID, &item.TriggerKey,
			&scheduledFor, &item.Status, &created, &item.IssueID); err != nil {
			return nil, err
		}
		item.ScheduledFor, err = sqliteParseTime(scheduledFor)
		if err != nil {
			return nil, err
		}
		item.CreatedAt, err = sqliteParseTime(created)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getSQLiteScheduledTaskInTx(ctx context.Context, tx *sql.Tx, userID, id string) (domain.ScheduledTask, error) {
	var item domain.ScheduledTask
	err := scanSQLiteScheduledTask(tx.QueryRowContext(ctx, sqliteScheduledTaskSelect+sqliteScheduledTaskOwnership+`
		WHERE tasks.id = ? AND owning_computer.user_id = ?
		  AND tasks.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledTask{}, domain.NotFound("计划任务", id)
	}
	return item, err
}

func requireSQLiteScheduleAgent(ctx context.Context, tx *sql.Tx, userID, projectID, agentID string) error {
	if agentID == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		JOIN agents selected_agent ON selected_agent.id = ?
		JOIN agent_revisions selected_revision ON selected_revision.id = selected_agent.current_revision_id AND selected_revision.agent_id = selected_agent.id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND selected_agent.computer_id = owning_project.computer_id
		  AND selected_agent.status = 'active' AND selected_revision.deleted_at IS NULL
		  AND selected_agent.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, agentID, projectID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.NotFound("Agent", agentID)
	}
	return nil
}

func (s *SQLite) AppendAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	if event.ID == "" {
		event.ID = newID("aud")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	details, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, user_id, project_id, issue_id, run_id, computer_id, actor_type,
			actor_ref, action, outcome, details, trace_id, occurred_at
		) VALUES (
			?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?
		)
	`, event.ID, event.UserID, event.ProjectID, event.IssueID, event.RunID,
		event.ComputerID, event.ActorType, event.ActorRef, event.Action, event.Outcome,
		string(details), event.TraceID, sqliteTimeValue(event.OccurredAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) ListAuditEvents(ctx context.Context, userID, projectID, runID string) ([]domain.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT events.id, COALESCE(events.user_id, ''), COALESCE(events.project_id, ''),
		       COALESCE(events.issue_id, ''), COALESCE(events.run_id, ''),
		       COALESCE(events.computer_id, ''), events.actor_type,
		       COALESCE(events.actor_ref, ''), events.action, events.outcome,
		       events.details, COALESCE(events.trace_id, ''), events.occurred_at
		FROM audit_events events
		LEFT JOIN projects owning_project ON owning_project.id = events.project_id
		LEFT JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		LEFT JOIN computers event_computer ON event_computer.id = events.computer_id
		LEFT JOIN issues event_issue ON event_issue.id = events.issue_id
		LEFT JOIN projects issue_project ON issue_project.id = event_issue.project_id
		LEFT JOIN computers issue_computer ON issue_computer.id = issue_project.computer_id
		LEFT JOIN runs filtered_run ON filtered_run.id = events.run_id
		LEFT JOIN projects run_project ON run_project.id = filtered_run.project_id
		LEFT JOIN computers run_computer ON run_computer.id = run_project.computer_id
		WHERE events.user_id = ?
		  AND (events.project_id IS NULL OR owning_computer.user_id = ?)
		  AND (events.computer_id IS NULL OR event_computer.user_id = ?)
		  AND (events.issue_id IS NULL OR issue_computer.user_id = ?)
		  AND (events.run_id IS NULL OR run_computer.user_id = ?)
		  AND (
		      ? = '' OR events.project_id = ?
		  )
		  AND (
		      ? = '' OR events.run_id = ?
		  )
		ORDER BY julianday(events.occurred_at) DESC, events.id DESC
		LIMIT 500
	`, userID, userID, userID, userID, userID, projectID, projectID, runID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.AuditEvent, 0)
	for rows.Next() {
		var item domain.AuditEvent
		var details, occurred string
		if err := rows.Scan(
			&item.ID, &item.UserID, &item.ProjectID, &item.IssueID, &item.RunID,
			&item.ComputerID, &item.ActorType, &item.ActorRef, &item.Action,
			&item.Outcome, &details, &item.TraceID, &occurred,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(details), &item.Details); err != nil {
			return nil, err
		}
		if item.Details == nil {
			item.Details = map[string]any{}
		}
		item.OccurredAt, err = sqliteParseTime(occurred)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func requireSQLiteProjectOwner(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, projectID string) error {
	var exists int
	if err := queryer.QueryRowContext(ctx, `
		SELECT count(*)
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, projectID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.NotFound("项目", projectID)
	}
	return nil
}
