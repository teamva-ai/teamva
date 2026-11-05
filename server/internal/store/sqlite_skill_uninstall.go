package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// UninstallComputerSkillPackageWithArtifact validates ownership, CAS and all
// live bindings while holding SQLite's write lock, then removes the artifact
// before committing the soft deletion. removeArtifact must be idempotent and
// MUST NOT re-enter the control repository: its sole connection is held here.
// Without a reversible artifact API this is deliberately fail-safe, not a
// distributed transaction: any failure retains the installation record for a
// retry, even if files were partly or completely removed already.
func (s *SQLite) UninstallComputerSkillPackageWithArtifact(
	ctx context.Context, userID, computerID, id string, expectedVersion int64,
	removeArtifact func(context.Context, string) error,
) error {
	if removeArtifact == nil {
		return domain.Invalid("Skill 卸载需要本机制品删除能力")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// BeginTx is deferred. Acquire the write lock BEFORE reading bindings, also
	// against other SQLite connections/processes (not just this DB's pool). A
	// concurrent binding either commits first and is rejected below, or cannot
	// publish until after this transaction, when the installation is gone.
	if _, err := tx.ExecContext(ctx, `
		UPDATE computer_skill_packages SET version = version
		WHERE id = ? AND computer_id = ? AND deleted_at IS NULL
		  AND computer_id IN (
		    SELECT computers.id FROM computers JOIN users ON users.id = computers.user_id
		    WHERE computers.user_id = ? AND computers.deleted_at IS NULL AND users.deleted_at IS NULL
		  )
	`, id, computerID, userID); err != nil {
		return err
	}
	var packageID, slug string
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `
		SELECT packages.id, packages.slug, installed.version
		FROM computer_skill_packages installed
		JOIN computers ON computers.id = installed.computer_id
		JOIN users ON users.id = computers.user_id
		JOIN skill_package_versions versions ON versions.id = installed.skill_package_version_id
		JOIN skill_packages packages ON packages.id = versions.skill_package_id
		WHERE installed.id = ? AND installed.computer_id = ? AND computers.user_id = ?
		  AND installed.deleted_at IS NULL AND computers.deleted_at IS NULL AND users.deleted_at IS NULL
	`, id, computerID, userID).Scan(&packageID, &slug, &currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("本机 Skill", id)
	}
	if err != nil {
		return err
	}
	if currentVersion != expectedVersion {
		return versionConflict(currentVersion)
	}
	if !validSkillUninstallSlug(slug) {
		return domain.Invalid("Skill 目录标识无效，无法安全卸载")
	}
	var used bool
	// A slug names the whole artifact, not one immutable version. Protect older
	// versions still pinned by active runs as well as current Agent bindings.
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM agent_revision_skill_packages bindings
		  JOIN skill_package_versions versions ON versions.id = bindings.skill_package_version_id
		  WHERE versions.skill_package_id = ? AND (
		    EXISTS (SELECT 1 FROM agents WHERE agents.current_revision_id = bindings.agent_revision_id
		      AND agents.computer_id = ? AND agents.deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM runs
		      WHERE runs.computer_id = ? AND runs.deleted_at IS NULL
		        AND runs.status IN ('queued','starting','running','waiting_approval','cancelling')
		        AND bindings.agent_revision_id IN (
		          json_extract(runs.snapshot, '$.agent_revision_id'),
		          (SELECT agent_revision_id FROM issue_agent_generations WHERE id = runs.issue_agent_generation_id),
		          (SELECT agent_revision_id FROM issue_agent_instances WHERE id = runs.issue_agent_instance_id)
		        ))
		  )
		)
	`, packageID, computerID, computerID).Scan(&used); err != nil {
		return err
	}
	if used {
		return domain.Conflict("skill_in_use", "该技能正在被 Agent 或活动运行使用。请先在 Agent 配置中解除所有绑定，并停止相关运行，再重试卸载。", nil)
	}
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE computer_skill_packages
		SET deleted_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND version = ? AND deleted_at IS NULL
	`, now, now, id, expectedVersion)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return versionConflict(currentVersion)
	}
	if err := removeArtifact(ctx, slug); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		// RemoveAll is idempotent, so a retry can finalize a retained record even
		// if the artifact no longer exists. Do not hide the uncertain file state
		// behind an ordinary internal error, or include SQL/runtime payloads.
		return &domain.Error{
			Code: "skill_uninstall_incomplete", Message: "Skill 文件已移除，但安装记录提交失败；请刷新后重试卸载，如需继续使用请重新安装",
			HTTPStatus: 503, Retryable: true,
			Details: map[string]any{"installation_record_retained": true, "artifact_state": "removed", "retry_action": "refresh_and_uninstall"},
		}
	}
	return nil
}

// Only a bounded ASCII leaf name may reach the runtime's path join. In
// particular '.', '..', separators, spaces and hidden/path-like names fail
// closed. Never sanitize unsafe input into a different directory name.
func validSkillUninstallSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 128 {
		return false
	}
	for index, char := range slug {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '-' || char == '_') {
			continue
		}
		return false
	}
	return true
}
