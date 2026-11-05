package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func (s *SQLite) ListProjects(ctx context.Context, userID, computerID string) ([]domain.Project, error) {
	if _, err := s.GetComputer(ctx, userID, computerID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteProjectSelect+`
		JOIN computers owning_computer ON owning_computer.id = projects.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE projects.computer_id = ? AND owning_computer.user_id = ?
		  AND projects.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY CASE WHEN projects.id = ? THEN 0 ELSE 1 END, projects.created_at, projects.id
	`, computerID, userID, domain.DefaultProjectID(computerID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Project, 0)
	for rows.Next() {
		var item domain.Project
		if err := scanSQLiteProject(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) CreateProject(ctx context.Context, userID string, input domain.Project) (domain.Project, error) {
	// The legacy physical theme column remains solely to satisfy the unchanged
	// SQLite baseline. It is not part of project reads or business updates.
	now := sqliteTimeValue(time.Now())
	var item domain.Project
	err := scanSQLiteProject(s.db.QueryRowContext(ctx, `
		INSERT INTO projects (id, computer_id, name, description, theme, version, created_at, updated_at)
		SELECT ?, owning_computer.id, ?, ?, 'hallmark', 1, ?, ?
		FROM computers owning_computer
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_computer.id = ? AND owning_computer.user_id = ?
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, computer_id, name, description
	`, newID("proj"), input.Name, input.Description, now, now, input.ComputerID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, domain.NotFound("设备", input.ComputerID)
	}
	return item, err
}

func (s *SQLite) GetProject(ctx context.Context, userID, id string) (domain.Project, error) {
	var item domain.Project
	err := scanSQLiteProject(s.db.QueryRowContext(ctx, sqliteProjectSelect+`
		JOIN computers owning_computer ON owning_computer.id = projects.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE projects.id = ? AND owning_computer.user_id = ?
		  AND projects.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, domain.NotFound("项目", id)
	}
	return item, err
}

func (s *SQLite) UpdateProject(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.Project)) (domain.Project, error) {
	item, err := s.GetProject(ctx, userID, id)
	if err != nil {
		return domain.Project{}, err
	}
	if item.Version != expectedVersion {
		return domain.Project{}, versionConflict(item.Version)
	}
	isDefault := item.IsDefault
	update(&item)
	if isDefault && item.Name != domain.DefaultProjectName {
		return domain.Project{}, domain.Forbidden("protected_project", "默认项目名称不能修改")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE projects SET name = ?, description = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND computer_id IN (
		      SELECT owning_computer.id FROM computers owning_computer
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, item.Name, item.Description, sqliteTimeValue(time.Now()), id, expectedVersion, userID)
	if err != nil {
		return domain.Project{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := s.GetProject(ctx, userID, id)
		if getErr != nil {
			return domain.Project{}, getErr
		}
		return domain.Project{}, versionConflict(current.Version)
	}
	return s.GetProject(ctx, userID, id)
}

func (s *SQLite) DeleteProject(ctx context.Context, userID, id string, expectedVersion int64) error {
	// Resolve ownership before revealing whether a project is protected.
	project, err := s.GetProject(ctx, userID, id)
	if err != nil {
		return err
	}
	if project.IsDefault {
		return domain.Forbidden("protected_project", "默认项目不能删除")
	}
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE projects SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND id IN (
		      SELECT owned_project.id FROM projects owned_project
		      JOIN computers owning_computer ON owning_computer.id = owned_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM runs
		      LEFT JOIN issues ON issues.id = runs.issue_id
		      WHERE (runs.project_id = projects.id OR issues.project_id = projects.id)
		        AND runs.status IN ('queued','starting','running','waiting_approval','cancelling')
		        AND runs.deleted_at IS NULL
		  )
	`, now, now, id, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	item, getErr := s.GetProject(ctx, userID, id)
	if getErr != nil {
		return getErr
	}
	if item.Version != expectedVersion {
		return versionConflict(item.Version)
	}
	return domain.Conflict("project_active", "项目仍有活动运行", nil)
}

func (s *SQLite) ListResources(ctx context.Context, userID, projectID string) ([]domain.ProjectResource, error) {
	if _, err := s.GetProject(ctx, userID, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteResourceSelect+sqliteResourceOwnership+`
		WHERE resources.project_id = ? AND owning_computer.user_id = ?
		  AND resources.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY resources.created_at, resources.id
	`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ProjectResource, 0)
	for rows.Next() {
		var item domain.ProjectResource
		if err := scanSQLiteResource(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) CreateResource(ctx context.Context, userID string, input domain.ProjectResource) (domain.ProjectResource, error) {
	now := sqliteTimeValue(time.Now())
	var item domain.ProjectResource
	err := scanSQLiteResource(s.db.QueryRowContext(ctx, `
		INSERT INTO project_resources (
			id, project_id, kind, name, local_reference, url, status, metadata,
			git_detected, version, created_at, updated_at
		)
		SELECT ?, owning_project.id, ?, ?, NULLIF(?, ''), NULLIF(?, ''),
		       CASE WHEN ? = 'url' THEN 'ready' ELSE 'pending_validation' END, '{}', 0, 1, ?, ?
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, project_id, kind, name,
		          COALESCE(local_reference, ''), COALESCE(url, ''), status, git_detected
	`, newID("res"), input.Kind, input.Name, input.Path, input.URL, input.Kind,
		now, now, input.ProjectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectResource{}, domain.NotFound("项目", input.ProjectID)
	}
	return item, err
}

func (s *SQLite) UpdateResource(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.ProjectResource)) (domain.ProjectResource, error) {
	item, err := s.getResource(ctx, userID, id)
	if err != nil {
		return domain.ProjectResource{}, err
	}
	if item.Version != expectedVersion {
		return domain.ProjectResource{}, versionConflict(item.Version)
	}
	update(&item)
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_resources
		SET name = ?, status = ?, git_detected = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_project.deleted_at IS NULL
		        AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		  )
	`, item.Name, item.Status, boolInt(item.GitDetected), sqliteTimeValue(time.Now()),
		id, expectedVersion, userID)
	if err != nil {
		return domain.ProjectResource{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := s.getResource(ctx, userID, id)
		if getErr != nil {
			return domain.ProjectResource{}, getErr
		}
		return domain.ProjectResource{}, versionConflict(current.Version)
	}
	return s.getResource(ctx, userID, id)
}

func (s *SQLite) DeleteResource(ctx context.Context, userID, id string, expectedVersion int64) error {
	now := sqliteTimeValue(time.Now())
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_resources SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_project.deleted_at IS NULL
		        AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		  )
	`, now, now, id, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	item, getErr := s.getResource(ctx, userID, id)
	if getErr != nil {
		return getErr
	}
	return versionConflict(item.Version)
}

func (s *SQLite) RequestResourceValidation(ctx context.Context, userID, id string, expectedVersion int64) (domain.ProjectResource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectResource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ProjectResource
	err = scanSQLiteResource(tx.QueryRowContext(ctx, sqliteResourceSelect+sqliteResourceOwnership+`
		WHERE resources.id = ? AND owning_computer.user_id = ?
		  AND resources.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectResource{}, domain.NotFound("项目资源", id)
	}
	if err != nil {
		return domain.ProjectResource{}, err
	}
	if item.Version != expectedVersion {
		return domain.ProjectResource{}, versionConflict(item.Version)
	}
	if item.Path == "" {
		return domain.ProjectResource{}, domain.Invalid("URL 资源无需本地 Node 验证")
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE project_resources SET status = 'validation_pending', git_detected = 0, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_project.deleted_at IS NULL
		        AND owning_computer.status <> 'revoked' AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, sqliteTimeValue(now), id, expectedVersion, userID)
	if err != nil {
		return domain.ProjectResource{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ProjectResource{}, &domain.Error{Code: "node_offline", Message: "项目当前没有可用的设备", HTTPStatus: 503, Retryable: true}
	}
	item.Status, item.GitDetected, item.UpdatedAt = "validation_pending", false, now
	item.Version++
	var computerID string
	if err := tx.QueryRowContext(ctx, `SELECT computer_id FROM projects WHERE id = ?`, item.ProjectID).Scan(&computerID); err != nil {
		return domain.ProjectResource{}, err
	}
	if _, err := insertSQLiteOutbox(ctx, tx, computerID, "resource.validate", fmt.Sprintf("resource.validate:%s:%d", id, item.Version),
		nodeprotocol.ValidateResourcePayload{ResourceID: id, Path: item.Path, ValidationVersion: item.Version}, now.Add(time.Minute)); err != nil {
		return domain.ProjectResource{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectResource{}, err
	}
	return item, nil
}

func (s *SQLite) SetResourceGitDetected(ctx context.Context, userID, id string, gitDetected bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_resources SET git_detected = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_project.deleted_at IS NULL
		        AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		  )
	`, boolInt(gitDetected), sqliteTimeValue(time.Now()), id, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.NotFound("资源", id)
	}
	return nil
}

func (s *SQLite) getResource(ctx context.Context, userID, id string) (domain.ProjectResource, error) {
	var item domain.ProjectResource
	err := scanSQLiteResource(s.db.QueryRowContext(ctx, sqliteResourceSelect+sqliteResourceOwnership+`
		WHERE resources.id = ? AND owning_computer.user_id = ?
		  AND resources.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectResource{}, domain.NotFound("项目资源", id)
	}
	return item, err
}

// Directory commands share the same durable dispatcher and fenced completion
// path as runs. Their HTTP handler only admits the command and returns its ID.
func (s *SQLite) CreateDirectoryListCommand(ctx context.Context, userID, computerID, path, query string, showHidden bool) (string, error) {
	payload, err := json.Marshal(map[string]any{"path": path, "query": query, "show_hidden": showHidden})
	if err != nil {
		return "", err
	}
	return s.createLocalCommand(ctx, userID, computerID, "filesystem.list", payload)
}

func (s *SQLite) CreateResourceRegisterCommand(ctx context.Context, userID, computerID, path string) (string, error) {
	payload, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		return "", err
	}
	return s.createLocalCommand(ctx, userID, computerID, "resource.register", payload)
}

func (s *SQLite) createLocalCommand(ctx context.Context, userID, computerID, name string, payload []byte) (string, error) {
	id := newID("cmd")
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO command_outbox (
			id, computer_id, command_name, idempotency_key, payload,
			status, available_at, expires_at, created_at
		)
		SELECT ?, owning_computer.id, ?, ?, ?, 'pending', ?, ?, ?
		FROM computers owning_computer
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_computer.id = ? AND owning_computer.user_id = ?
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, name, name+":"+id, string(payload), sqliteTimeValue(now),
		sqliteTimeValue(now.Add(time.Minute)), sqliteTimeValue(now), computerID, userID)
	if err != nil {
		return "", err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return "", domain.NotFound("设备", computerID)
	}
	return id, nil
}

func (s *SQLite) GetDirectoryListResult(ctx context.Context, userID, computerID, commandID string) (string, []byte, error) {
	var status string
	var result sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT commands.status, commands.result
		FROM command_outbox commands
		JOIN computers owning_computer ON owning_computer.id = commands.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE commands.id = ? AND commands.computer_id = ? AND owning_computer.user_id = ?
		  AND commands.command_name IN ('filesystem.list', 'resource.register')
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, commandID, computerID, userID).Scan(&status, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, domain.NotFound("目录列表", commandID)
	}
	if err != nil {
		return "", nil, err
	}
	if !result.Valid {
		return status, nil, nil
	}
	return status, []byte(result.String), nil
}

const sqliteProjectSelect = `
	SELECT projects.id, projects.version, projects.created_at, projects.updated_at,
	       projects.deleted_at, projects.computer_id, projects.name, projects.description
	FROM projects
`

func scanSQLiteProject(row scanner, item *domain.Project) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ComputerID, &item.Name, &item.Description); err != nil {
		return err
	}
	item.IsDefault = item.ID == domain.DefaultProjectID(item.ComputerID)
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

const sqliteResourceSelect = `
	SELECT resources.id, resources.version, resources.created_at, resources.updated_at,
	       resources.deleted_at, resources.project_id, resources.kind, resources.name,
	       COALESCE(resources.local_reference, ''), COALESCE(resources.url, ''),
	       resources.status, resources.git_detected
	FROM project_resources resources
`

const sqliteResourceOwnership = `
	JOIN projects owning_project ON owning_project.id = resources.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func scanSQLiteResource(row scanner, item *domain.ProjectResource) error {
	var created, updated string
	var deleted sql.NullString
	var gitDetected int
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ProjectID, &item.Kind, &item.Name, &item.Path, &item.URL,
		&item.Status, &gitDetected); err != nil {
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
	item.GitDetected = gitDetected != 0
	return nil
}
