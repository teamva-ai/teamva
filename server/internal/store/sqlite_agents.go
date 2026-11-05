package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Agent、修订和 Skill 均以 SQLite 为唯一数据源。所有租户查询都通过
// users -> computers 收敛作用域，SQLite 是唯一事实源。

const sqliteAgentSelect = `
	SELECT agents.id, agents.version, agents.created_at, agents.updated_at,
	       agents.deleted_at, agents.computer_id, COALESCE(agents.agent_service_id,''),
	       COALESCE(agents.model, ''), agents.name, agents.note, agents.status,
	       COALESCE(agents.current_revision_id, ''), agents.system_role
	FROM agents
`

const sqliteAgentOwnership = `
	JOIN computers owning_computer ON owning_computer.id = agents.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

type sqliteAgentQueries interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *SQLite) ListAgents(ctx context.Context, userID, computerID string) ([]domain.Agent, error) {
	if _, err := s.GetComputer(ctx, userID, computerID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteAgentSelect+sqliteAgentOwnership+`
		WHERE agents.computer_id = ? AND owning_computer.user_id = ?
		  AND agents.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY agents.created_at, agents.id
	`, computerID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Agent, 0)
	for rows.Next() {
		var item domain.Agent
		if err := scanSQLiteAgent(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) GetAgent(ctx context.Context, userID, id string) (domain.Agent, error) {
	return getSQLiteAgent(ctx, s.db, userID, id)
}

func getSQLiteAgent(ctx context.Context, query sqliteAgentQueries, userID, id string) (domain.Agent, error) {
	var item domain.Agent
	err := scanSQLiteAgent(query.QueryRowContext(ctx, sqliteAgentSelect+sqliteAgentOwnership+`
		WHERE agents.id = ? AND owning_computer.user_id = ?
		  AND agents.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, domain.NotFound("Agent", id)
	}
	return item, err
}

func (s *SQLite) CreateAgent(ctx context.Context, userID string, input domain.Agent) (domain.Agent, error) {
	return createSQLiteAgent(ctx, s.db, userID, input)
}

func createSQLiteAgent(ctx context.Context, query sqliteAgentQueries, userID string, input domain.Agent) (domain.Agent, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]rune(input.Name)) > 200 {
		return domain.Agent{}, domain.Invalid("Agent 名称为空或过长")
	}
	status := input.Status
	if status == "" {
		status = domain.AgentActive
	}
	now := sqliteTimeValue(time.Now())
	var item domain.Agent
	err := scanSQLiteAgent(query.QueryRowContext(ctx, `
		INSERT INTO agents (
			id, computer_id, agent_service_id, model, name, note, status,
			version, created_at, updated_at
		)
		SELECT ?, owning_computer.id, services.id, NULLIF(?, ''), ?, ?, ?, 1, ?, ?
		FROM computers owning_computer
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		JOIN agent_services services
		  ON services.id = ? AND services.computer_id = owning_computer.id
		WHERE owning_computer.id = ? AND owning_computer.user_id = ?
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		  AND services.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, computer_id,
		          agent_service_id, COALESCE(model, ''), name, note, status,
		          COALESCE(current_revision_id, ''), system_role
	`, newID("agt"), input.Model, input.Name, input.Note, status, now, now,
		input.AgentServiceID, input.ComputerID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		var owns bool
		if ownerErr := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM computers WHERE id=? AND user_id=? AND deleted_at IS NULL)`, input.ComputerID, userID).Scan(&owns); ownerErr != nil {
			return domain.Agent{}, ownerErr
		}
		if !owns {
			return domain.Agent{}, domain.NotFound("设备", input.ComputerID)
		}
		return domain.Agent{}, domain.Invalid("Agent 服务不存在")
	}
	return item, err
}

func (s *SQLite) UpdateAgent(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.Agent)) (domain.Agent, error) {
	return updateSQLiteAgent(ctx, s.db, userID, id, expectedVersion, update)
}

func updateSQLiteAgent(ctx context.Context, query sqliteAgentQueries, userID, id string, expectedVersion int64, update func(*domain.Agent)) (domain.Agent, error) {
	item, err := getSQLiteAgent(ctx, query, userID, id)
	if err != nil {
		return domain.Agent{}, err
	}
	if item.Version != expectedVersion {
		return domain.Agent{}, versionConflict(item.Version)
	}
	previousService := item.AgentServiceID
	update(&item)
	if item.AgentServiceID != previousService {
		var exists int
		err := query.QueryRowContext(ctx, `SELECT 1 FROM agent_services WHERE id=? AND computer_id=? AND deleted_at IS NULL`, item.AgentServiceID, item.ComputerID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Agent{}, domain.Invalid("Agent 服务不存在")
		}
		if err != nil {
			return domain.Agent{}, err
		}
	}
	// The built-in Agent is the always-available global assistant. The schema
	// trigger has the same invariant; keep the friendly error here.
	if item.SystemRole == "general" && item.Status != domain.AgentActive {
		return domain.Agent{}, domain.Forbidden("protected_agent", "Lumi 不能禁用")
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len([]rune(item.Name)) > 200 || (item.Status != domain.AgentActive && item.Status != domain.AgentDisabled) {
		return domain.Agent{}, domain.Invalid("Agent 名称或状态无效")
	}
	result, err := query.ExecContext(ctx, `
		UPDATE agents
		SET agent_service_id = NULLIF(?, ''), model = NULLIF(?, ''), name = ?, note = ?, status = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND computer_id IN (
		      SELECT owning_computer.id
		      FROM computers owning_computer
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, item.AgentServiceID, item.Model, item.Name, item.Note, item.Status, sqliteTimeValue(time.Now()),
		id, expectedVersion, userID)
	if err != nil {
		return domain.Agent{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := getSQLiteAgent(ctx, query, userID, id)
		if getErr != nil {
			return domain.Agent{}, getErr
		}
		return domain.Agent{}, versionConflict(current.Version)
	}
	return getSQLiteAgent(ctx, query, userID, id)
}

func (s *SQLite) DeleteAgent(ctx context.Context, userID, id string, expectedVersion int64) error {
	if item, err := s.GetAgent(ctx, userID, id); err != nil {
		return err
	} else if item.SystemRole == "general" {
		return domain.Forbidden("protected_agent", "Lumi 不能删除")
	}
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND computer_id IN (
		      SELECT owning_computer.id
		      FROM computers owning_computer
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		  AND NOT EXISTS (SELECT 1 FROM runs WHERE agent_id = agents.id AND deleted_at IS NULL
		      AND status IN ('queued','starting','running','waiting_approval','cancelling'))
		  AND NOT EXISTS (SELECT 1 FROM scheduled_tasks WHERE agent_id = agents.id AND deleted_at IS NULL AND enabled = 1)
		  AND NOT EXISTS (SELECT 1 FROM issue_comment_mentions WHERE target_agent_id = agents.id
		      AND status IN ('pending','processing') AND consumed_at IS NULL)
	`, now, now, id, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	current, getErr := s.GetAgent(ctx, userID, id)
	if getErr != nil {
		return getErr
	}
	if current.Version != expectedVersion {
		return versionConflict(current.Version)
	}
	return domain.Conflict("agent_in_use", "Agent 仍有活动运行、待执行提及或启用的计划任务，请先停止、清空或暂停它们", nil)
}

// CreateAgentRevision 在一个事务中创建不可变修订、固化 Skill 绑定并推进
// current_revision_id。agents/current_revision 与 revisions 的延迟 FK 在提交前完整满足。
func (s *SQLite) CreateAgentRevision(ctx context.Context, userID string, input domain.AgentRevision, skillVersionIDs []string) (domain.AgentRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentRevision{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, err := createSQLiteAgentRevisionTx(ctx, tx, userID, input, skillVersionIDs, true)
	if err != nil {
		return domain.AgentRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentRevision{}, err
	}
	return item, nil
}

// advanceVersion is false when the enclosing configuration operation already
// owns the Agent version change. The revision and owner still commit together.
func createSQLiteAgentRevisionTx(ctx context.Context, tx *sql.Tx, userID string, input domain.AgentRevision, skillVersionIDs []string, advanceVersion bool) (domain.AgentRevision, error) {
	if len([]rune(input.SystemPrompt)) > 65536 || len(skillVersionIDs) > 64 {
		return domain.AgentRevision{}, domain.Invalid("Agent 配置超过大小上限")
	}
	var agent domain.Agent
	err := scanSQLiteAgent(tx.QueryRowContext(ctx, sqliteAgentSelect+sqliteAgentOwnership+`
		WHERE agents.id = ? AND owning_computer.user_id = ?
		  AND agents.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, input.AgentID, userID), &agent)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRevision{}, domain.NotFound("Agent", input.AgentID)
	}
	if err != nil {
		return domain.AgentRevision{}, err
	}

	// 修订使用的服务必须仍属于 Agent 所在电脑。
	var serviceExists int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_services services
		JOIN computers owning_computer ON owning_computer.id = services.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE services.id = ? AND services.computer_id = ?
		  AND owning_computer.user_id = ? AND services.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, input.AgentServiceID, agent.ComputerID, userID).Scan(&serviceExists); err != nil {
		return domain.AgentRevision{}, err
	}
	if serviceExists == 0 {
		return domain.AgentRevision{}, domain.Invalid("Agent 服务不存在")
	}

	seen := make(map[string]struct{}, len(skillVersionIDs))
	for _, skillVersionID := range skillVersionIDs {
		if _, duplicate := seen[skillVersionID]; duplicate {
			return domain.AgentRevision{}, domain.Invalid("不能重复绑定同一个 Skill")
		}
		seen[skillVersionID] = struct{}{}
		var installed int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM computer_skill_packages
			WHERE computer_id = ? AND skill_package_version_id = ? AND deleted_at IS NULL
		`, agent.ComputerID, skillVersionID).Scan(&installed); err != nil {
			return domain.AgentRevision{}, err
		}
		if installed != 1 {
			return domain.AgentRevision{}, domain.Invalid("Agent 只能绑定本机已安装的 Skill")
		}
	}

	now := sqliteTimeValue(time.Now())
	revisionID := newID("arev")
	var item domain.AgentRevision
	err = scanSQLiteAgentRevision(tx.QueryRowContext(ctx, `
		INSERT INTO agent_revisions (
			id, agent_id, agent_service_id, model, system_prompt,
			version, created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 1, ?, ?)
		RETURNING id, version, created_at, updated_at, deleted_at, agent_id,
		          agent_service_id, COALESCE(model, ''), COALESCE(system_prompt, '')
	`, revisionID, agent.ID, input.AgentServiceID, input.Model, input.SystemPrompt,
		now, now), &item)
	if err != nil {
		return domain.AgentRevision{}, err
	}
	for position, skillVersionID := range skillVersionIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_revision_skill_packages (
				id, agent_revision_id, skill_package_version_id, position
			) VALUES (?, ?, ?, ?)
		`, newID("arsk"), item.ID, skillVersionID, position); err != nil {
			return domain.AgentRevision{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET current_revision_id = ?, model = NULLIF(?, ''), version = version + ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, item.ID, input.Model, boolInt(advanceVersion), now, agent.ID)
	if err != nil {
		return domain.AgentRevision{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return domain.AgentRevision{}, domain.NotFound("Agent", agent.ID)
	}
	return item, nil
}

const sqliteAgentRevisionSelect = `
	SELECT revisions.id, revisions.version, revisions.created_at, revisions.updated_at,
	       revisions.deleted_at, revisions.agent_id, revisions.agent_service_id,
	       COALESCE(revisions.model, ''), COALESCE(revisions.system_prompt, '')
	FROM agent_revisions revisions
`

func (s *SQLite) GetAgentRevision(ctx context.Context, userID, id string) (domain.AgentRevision, error) {
	var item domain.AgentRevision
	err := scanSQLiteAgentRevision(s.db.QueryRowContext(ctx, sqliteAgentRevisionSelect+`
		JOIN agents owning_agent ON owning_agent.id = revisions.agent_id
		JOIN computers owning_computer ON owning_computer.id = owning_agent.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE revisions.id = ? AND owning_computer.user_id = ?
		  AND revisions.deleted_at IS NULL AND owning_agent.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRevision{}, domain.NotFound("Agent 修订", id)
	}
	return item, err
}

func (s *SQLite) ListAgentRevisionSkillPackages(ctx context.Context, userID, revisionID string) ([]domain.AgentRevisionSkillPackage, error) {
	if _, err := s.GetAgentRevision(ctx, userID, revisionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT bindings.id, bindings.agent_revision_id,
		       bindings.skill_package_version_id, bindings.position
		FROM agent_revision_skill_packages bindings
		JOIN agent_revisions revisions ON revisions.id = bindings.agent_revision_id
		JOIN agents owning_agent ON owning_agent.id = revisions.agent_id
		JOIN computers owning_computer ON owning_computer.id = owning_agent.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE bindings.agent_revision_id = ? AND owning_computer.user_id = ?
		  AND revisions.deleted_at IS NULL AND owning_agent.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY bindings.position, bindings.id
	`, revisionID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.AgentRevisionSkillPackage, 0)
	for rows.Next() {
		var item domain.AgentRevisionSkillPackage
		if err := rows.Scan(&item.ID, &item.AgentRevisionID,
			&item.SkillPackageVersionID, &item.Position); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) ListSkillPackages(ctx context.Context, _ string) ([]domain.SkillPackage, error) {
	rows, err := s.db.QueryContext(ctx, sqliteSkillPackageSelect+`
		WHERE packages.deleted_at IS NULL
		ORDER BY packages.created_at, packages.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SkillPackage, 0)
	for rows.Next() {
		var item domain.SkillPackage
		if err := scanSQLiteSkillPackage(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) GetSkillPackage(ctx context.Context, _ string, id string) (domain.SkillPackage, error) {
	var item domain.SkillPackage
	err := scanSQLiteSkillPackage(s.db.QueryRowContext(ctx, sqliteSkillPackageSelect+`
		WHERE packages.id = ? AND packages.deleted_at IS NULL
	`, id), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SkillPackage{}, domain.NotFound("Skill", id)
	}
	return item, err
}

func (s *SQLite) UpsertSkillPackage(ctx context.Context, input domain.SkillPackage) (domain.SkillPackage, error) {
	if input.ID == "" {
		input.ID = newID("skp")
	}
	now := sqliteTimeValue(time.Now())
	var item domain.SkillPackage
	err := scanSQLiteSkillPackage(s.db.QueryRowContext(ctx, `
		INSERT INTO skill_packages (
			id, slug, name, description, source_url, license, builtin,
			version, created_at, updated_at
		) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 1, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			source_url = excluded.source_url,
			license = excluded.license,
			builtin = excluded.builtin,
			updated_at = excluded.updated_at
		RETURNING id, version, created_at, updated_at, deleted_at, slug, name,
		          description, COALESCE(source_url, ''), COALESCE(license, ''), builtin
	`, input.ID, input.Slug, input.Name, input.Description, input.SourceURL,
		input.License, boolInt(input.Builtin), now, now), &item)
	return item, err
}

func (s *SQLite) GetSkillPackageVersion(ctx context.Context, _ string, id string) (domain.SkillPackageVersion, error) {
	var item domain.SkillPackageVersion
	var created, updated string
	var deleted sql.NullString
	var manifestJSON, executableJSON, providerJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT versions.id, versions.version, versions.created_at, versions.updated_at,
		       versions.deleted_at, versions.skill_package_id, versions.commit_sha,
		       versions.content_digest, versions.manifest, versions.file_count,
		       versions.size_bytes, versions.executable_files, versions.provider_install
		FROM skill_package_versions versions
		JOIN skill_packages packages ON packages.id = versions.skill_package_id
		WHERE versions.id = ?
	`, id).Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.SkillPackageID, &item.CommitSHA, &item.ContentDigest, &manifestJSON,
		&item.FileCount, &item.SizeBytes, &executableJSON, &providerJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SkillPackageVersion{}, domain.NotFound("Skill 版本", id)
	}
	if err != nil {
		return domain.SkillPackageVersion{}, err
	}
	if item.CreatedAt, err = sqliteParseTime(created); err != nil {
		return domain.SkillPackageVersion{}, err
	}
	if item.UpdatedAt, err = sqliteParseTime(updated); err != nil {
		return domain.SkillPackageVersion{}, err
	}
	if item.DeletedAt, err = sqliteParseNullTime(deleted); err != nil {
		return domain.SkillPackageVersion{}, err
	}
	if err := json.Unmarshal([]byte(manifestJSON), &item.Manifest); err != nil {
		return domain.SkillPackageVersion{}, fmt.Errorf("decode Skill manifest: %w", err)
	}
	if err := json.Unmarshal([]byte(executableJSON), &item.ExecutableFiles); err != nil {
		return domain.SkillPackageVersion{}, fmt.Errorf("decode Skill executable files: %w", err)
	}
	if err := json.Unmarshal([]byte(providerJSON), &item.ProviderInstall); err != nil {
		return domain.SkillPackageVersion{}, fmt.Errorf("decode Skill provider install: %w", err)
	}
	return item, nil
}

const sqliteSkillPackageSelect = `
	SELECT packages.id, packages.version, packages.created_at, packages.updated_at,
	       packages.deleted_at, packages.slug, packages.name, packages.description,
	       COALESCE(packages.source_url, ''), COALESCE(packages.license, ''), packages.builtin
	FROM skill_packages packages
`

func scanSQLiteAgent(row scanner, item *domain.Agent) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ComputerID, &item.AgentServiceID, &item.Model, &item.Name, &item.Note,
		&item.Status, &item.CurrentRevisionID, &item.SystemRole); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func scanSQLiteAgentRevision(row scanner, item *domain.AgentRevision) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.AgentID, &item.AgentServiceID, &item.Model, &item.SystemPrompt); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func scanSQLiteSkillPackage(row scanner, item *domain.SkillPackage) error {
	var created, updated string
	var deleted sql.NullString
	var builtin int
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.Slug, &item.Name, &item.Description, &item.SourceURL, &item.License,
		&builtin); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	item.Builtin = builtin != 0
	return nil
}

func (s *SQLite) ListComputerSkillPackages(ctx context.Context, userID, computerID string) ([]domain.ComputerSkillPackage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT installed.id, installed.version, installed.created_at, installed.updated_at,
		       installed.deleted_at, installed.computer_id, versions.skill_package_id,
		       installed.skill_package_version_id
		FROM computer_skill_packages installed
		JOIN computers ON computers.id = installed.computer_id
		JOIN skill_package_versions versions ON versions.id = installed.skill_package_version_id
		WHERE installed.computer_id = ? AND computers.user_id = ?
		  AND installed.deleted_at IS NULL AND computers.deleted_at IS NULL
		ORDER BY installed.created_at, installed.id
	`, computerID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ComputerSkillPackage, 0)
	for rows.Next() {
		var item domain.ComputerSkillPackage
		if err := scanSQLiteComputerSkillPackage(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// InstallComputerSkillPackageVersion records a verified on-disk artifact.
func (s *SQLite) InstallComputerSkillPackageVersion(
	ctx context.Context, userID, computerID, packageID string, artifact domain.SkillPackageVersion,
) (domain.ComputerSkillPackage, error) {
	if artifact.CommitSHA == "" || artifact.ContentDigest == "" || artifact.FileCount <= 0 || artifact.Manifest == nil {
		return domain.ComputerSkillPackage{}, domain.Invalid("Skill 安装制品不完整")
	}
	if artifact.ExecutableFiles == nil {
		artifact.ExecutableFiles = []string{}
	}
	if artifact.ProviderInstall == nil {
		artifact.ProviderInstall = map[string]any{}
	}
	manifestJSON, err := json.Marshal(artifact.Manifest)
	if err != nil {
		return domain.ComputerSkillPackage{}, domain.Invalid("Skill manifest 无效")
	}
	executableJSON, err := json.Marshal(artifact.ExecutableFiles)
	if err != nil {
		return domain.ComputerSkillPackage{}, domain.Invalid("Skill executable_files 无效")
	}
	providerJSON, err := json.Marshal(artifact.ProviderInstall)
	if err != nil {
		return domain.ComputerSkillPackage{}, domain.Invalid("Skill provider_install 无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ComputerSkillPackage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var owns int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM computers WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, computerID, userID).Scan(&owns); err != nil {
		return domain.ComputerSkillPackage{}, err
	}
	if owns != 1 {
		return domain.ComputerSkillPackage{}, domain.NotFound("计算机", computerID)
	}
	var current domain.ComputerSkillPackage
	var currentCommit, currentDigest string
	err = scanSQLiteComputerSkillPackage(tx.QueryRowContext(ctx, `
		SELECT installed.id, installed.version, installed.created_at, installed.updated_at,
		       installed.deleted_at, installed.computer_id, versions.skill_package_id,
		       installed.skill_package_version_id
		FROM computer_skill_packages installed
		JOIN skill_package_versions versions ON versions.id = installed.skill_package_version_id
		WHERE installed.computer_id = ? AND versions.skill_package_id = ?
		  AND installed.deleted_at IS NULL AND versions.deleted_at IS NULL
		ORDER BY installed.created_at DESC LIMIT 1
	`, computerID, packageID), &current)
	if err == nil {
		if err := tx.QueryRowContext(ctx, `
			SELECT commit_sha, content_digest FROM skill_package_versions WHERE id = ?
		`, current.SkillPackageVersionID).Scan(&currentCommit, &currentDigest); err != nil {
			return domain.ComputerSkillPackage{}, err
		}
		if currentCommit == artifact.CommitSHA && currentDigest == artifact.ContentDigest {
			return current, tx.Commit()
		}
		return domain.ComputerSkillPackage{}, domain.Conflict("skill_version_conflict", "本机已安装另一个 Skill 版本，请先解除 Agent 绑定并卸载", nil)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ComputerSkillPackage{}, err
	}

	// Uninstall soft-deletes only the installation. Immutable package versions
	// remain referenced by historical Agent revisions/runs and are globally
	// unique per package+commit. Reuse verified content instead of reinserting
	// that version (or overwriting its historical manifest).
	var versionID, retainedDigest string
	var versionDeleted sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT versions.id, versions.content_digest, versions.deleted_at
		FROM skill_package_versions versions JOIN skill_packages packages ON packages.id = versions.skill_package_id
		WHERE packages.id = ? AND packages.deleted_at IS NULL AND versions.commit_sha = ?
	`, packageID, artifact.CommitSHA).Scan(&versionID, &retainedDigest, &versionDeleted)
	now := sqliteTimeValue(time.Now())
	if err == nil {
		if versionDeleted.Valid || retainedDigest != artifact.ContentDigest {
			return domain.ComputerSkillPackage{}, domain.Conflict("skill_version_conflict", "同一 Skill 提交的制品校验不一致或版本已不可用，无法安全重新安装", nil)
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		versionID = newID("skv")
		_, err = tx.ExecContext(ctx, `
			INSERT INTO skill_package_versions (
				id, skill_package_id, commit_sha, content_digest, manifest,
				file_count, size_bytes, executable_files, provider_install,
				version, created_at, updated_at
			) SELECT ?, id, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?
			  FROM skill_packages WHERE id = ? AND deleted_at IS NULL
		`, versionID, artifact.CommitSHA, artifact.ContentDigest, string(manifestJSON), artifact.FileCount,
			artifact.SizeBytes, string(executableJSON), string(providerJSON), now, now, packageID)
		if err != nil {
			return domain.ComputerSkillPackage{}, err
		}
	} else {
		return domain.ComputerSkillPackage{}, err
	}
	var item domain.ComputerSkillPackage
	err = scanSQLiteComputerSkillPackage(tx.QueryRowContext(ctx, `
		INSERT INTO computer_skill_packages (
			id, computer_id, skill_package_version_id, version, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, 1, ?, ?, NULL)
		ON CONFLICT (computer_id, skill_package_version_id) DO UPDATE
		SET deleted_at = NULL, updated_at = excluded.updated_at, version = computer_skill_packages.version + 1
		WHERE computer_skill_packages.deleted_at IS NOT NULL
		RETURNING id, version, created_at, updated_at, deleted_at, computer_id,
		          (SELECT skill_package_id FROM skill_package_versions WHERE id = skill_package_version_id),
		          skill_package_version_id
	`, newID("csk"), computerID, versionID, now, now), &item)
	if err != nil {
		return domain.ComputerSkillPackage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ComputerSkillPackage{}, err
	}
	return item, nil
}

func (s *SQLite) UninstallComputerSkillPackage(ctx context.Context, userID, computerID, id string, expectedVersion int64) error {
	// Database-only repository callers have no filesystem side effect. HTTP must
	// use the artifact-aware method so the record cannot disappear on a failed
	// filesystem deletion.
	return s.UninstallComputerSkillPackageWithArtifact(ctx, userID, computerID, id, expectedVersion,
		func(context.Context, string) error { return nil })
}

func (s *SQLite) ValidateComputerSkillPackages(ctx context.Context, userID, computerID string, versionIDs []string) error {
	var owns int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM computers WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, computerID, userID).Scan(&owns); err != nil {
		return err
	}
	if owns != 1 {
		return domain.NotFound("计算机", computerID)
	}
	for _, versionID := range versionIDs {
		var installed int
		if err := s.db.QueryRowContext(ctx, `
			SELECT count(*) FROM computer_skill_packages
			WHERE computer_id = ? AND skill_package_version_id = ? AND deleted_at IS NULL
		`, computerID, versionID).Scan(&installed); err != nil {
			return err
		}
		if installed != 1 {
			return domain.Invalid("Agent 只能绑定已安装到本机的 Skills")
		}
	}
	return nil
}

func scanSQLiteComputerSkillPackage(row scanner, item *domain.ComputerSkillPackage) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ComputerID, &item.SkillPackageID, &item.SkillPackageVersionID); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func parseSQLiteVersioned(item *domain.Versioned, created, updated string, deleted sql.NullString) error {
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
