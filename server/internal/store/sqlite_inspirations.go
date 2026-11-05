package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const inspirationColumns = `id,version,created_at,updated_at,deleted_at,COALESCE(project_id,''),title,markdown,completed,completed_at`

func scanInspiration(row scanner) (domain.Inspiration, error) {
	var n domain.Inspiration
	var created, updated string
	var deleted, completedAt sql.NullString
	if err := row.Scan(&n.ID, &n.Version, &created, &updated, &deleted, &n.ProjectID, &n.Title, &n.Markdown, &n.Completed, &completedAt); err != nil {
		return n, err
	}
	if err := parseSQLiteVersioned(&n.Versioned, created, updated, deleted); err != nil {
		return n, err
	}
	var err error
	n.CompletedAt, err = sqliteParseNullTime(completedAt)
	return n, err
}
func validateInspiration(n domain.Inspiration) error {
	if strings.TrimSpace(n.Title) == "" || utf8.RuneCountInString(n.Title) > 200 || !utf8.ValidString(n.Markdown) || len(n.Markdown) > 262144 {
		return domain.Invalid("灵感标题或 Markdown 正文超过限制")
	}
	return nil
}
func (s *SQLite) GetInspiration(ctx context.Context, userID, id string) (domain.Inspiration, error) {
	n, err := scanInspiration(s.db.QueryRowContext(ctx, `SELECT `+inspirationColumns+` FROM inspirations WHERE id=? AND user_id=? AND deleted_at IS NULL`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.NotFound("灵感", id)
	}
	return n, err
}
func (s *SQLite) CreateInspiration(ctx context.Context, userID, key string, n domain.Inspiration) (domain.Inspiration, error) {
	n.Title = strings.TrimSpace(n.Title)
	requestedTitle := n.Title
	autoTitle := n.Title == ""
	if n.Title == "" {
		n.Title = "新灵感"
	}
	if err := validateInspiration(n); err != nil {
		return n, err
	}
	if key == "" || len(key) > 128 {
		return n, domain.Invalid("创建灵感需要幂等标识")
	}
	// Fingerprint the request, not its automatic display placeholder: explicitly
	// naming a note "新灵感" must not replay an automatic-title creation intent.
	raw, _ := json.Marshal([]string{n.ProjectID, requestedTitle, n.Markdown})
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	var existing, prior string
	var deleted sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,create_fingerprint,deleted_at FROM inspirations WHERE user_id=? AND create_key=?`, userID, key).Scan(&existing, &prior, &deleted)
	if err == nil {
		if prior != fingerprint {
			return n, domain.Conflict("idempotency_conflict", "幂等标识已用于不同灵感", nil)
		}
		if deleted.Valid {
			return n, domain.NotFound("灵感", existing)
		}
		return scanInspiration(tx.QueryRowContext(ctx, `SELECT `+inspirationColumns+` FROM inspirations WHERE id=?`, existing))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return n, err
	}
	stamp := sqliteTimeValue(time.Now())
	id := newID("ins")
	_, err = tx.ExecContext(ctx, `INSERT INTO inspirations(id,user_id,project_id,title,markdown,create_key,create_fingerprint,created_at,updated_at)
SELECT ?,?,NULLIF(?,''),?,?,?,?,?,? WHERE ?='' OR EXISTS(SELECT 1 FROM projects p JOIN computers c ON c.id=p.computer_id WHERE p.id=? AND c.user_id=? AND p.deleted_at IS NULL AND c.deleted_at IS NULL)`, id, userID, n.ProjectID, n.Title, n.Markdown, key, fingerprint, stamp, stamp, n.ProjectID, n.ProjectID, userID)
	if err != nil {
		return n, err
	}
	n, err = scanInspiration(tx.QueryRowContext(ctx, `SELECT `+inspirationColumns+` FROM inspirations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, domain.Invalid("项目不存在或不可访问")
	}
	if err != nil {
		return n, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "inspiration", n.ID); err != nil {
			return n, err
		}
		if strings.TrimSpace(n.Markdown) != "" {
			if err := queueAutoTitleTx(ctx, tx, userID, "inspiration", n.ID, n.Markdown); err != nil {
				return n, err
			}
		}
	}
	return n, tx.Commit()
}
func (s *SQLite) UpdateInspiration(ctx context.Context, userID, id string, version int64, patch domain.InspirationPatch) (domain.Inspiration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Inspiration{}, err
	}
	defer tx.Rollback()
	n, err := scanInspiration(tx.QueryRowContext(ctx, `SELECT `+inspirationColumns+` FROM inspirations WHERE id=? AND user_id=? AND deleted_at IS NULL`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return n, domain.NotFound("灵感", id)
	}
	if err != nil {
		return n, err
	}
	if n.Version != version {
		return n, versionConflict(n.Version)
	}
	original := n
	if patch.ProjectID != nil && *patch.ProjectID != n.ProjectID {
		if *patch.ProjectID != "" {
			var owns int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM projects p JOIN computers c ON c.id=p.computer_id WHERE p.id=? AND c.user_id=? AND p.deleted_at IS NULL AND c.deleted_at IS NULL`, *patch.ProjectID, userID).Scan(&owns); err != nil {
				return n, err
			}
			if owns != 1 {
				return n, domain.Invalid("项目不存在或不可访问")
			}
		}
		n.ProjectID = *patch.ProjectID
	}
	if patch.Title != nil {
		n.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Markdown != nil {
		n.Markdown = *patch.Markdown
	}
	if patch.Completed != nil && *patch.Completed != n.Completed {
		n.Completed = *patch.Completed
		n.CompletedAt = nil
		if n.Completed {
			now := time.Now().UTC()
			n.CompletedAt = &now
		}
	}
	if err = validateInspiration(n); err != nil {
		return n, err
	}
	if patch.Title != nil {
		if err := manualAutoTitleTx(ctx, tx, "inspiration", id); err != nil {
			return n, err
		}
	} else if n.Markdown != original.Markdown && strings.TrimSpace(n.Markdown) != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "inspiration", id, n.Markdown); err != nil {
			return n, err
		}
	}
	if n.Title == original.Title && n.Markdown == original.Markdown && n.ProjectID == original.ProjectID && n.Completed == original.Completed {
		return original, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE inspirations SET project_id=NULLIF(?,''),title=?,markdown=?,completed=?,completed_at=?,version=version+1,updated_at=? WHERE id=? AND user_id=? AND version=? AND deleted_at IS NULL`, n.ProjectID, n.Title, n.Markdown, n.Completed, nullableSQLiteTime(n.CompletedAt), sqliteTimeValue(time.Now()), id, userID, version)
	if err != nil {
		return n, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return n, versionConflict(n.Version)
	}
	n, err = scanInspiration(tx.QueryRowContext(ctx, `SELECT `+inspirationColumns+` FROM inspirations WHERE id=?`, id))
	if err != nil {
		return n, err
	}
	return n, tx.Commit()
}
func (s *SQLite) DeleteInspiration(ctx context.Context, userID, id string, version int64) error {
	n, err := s.GetInspiration(ctx, userID, id)
	if err != nil {
		return err
	}
	if n.Version != version {
		return versionConflict(n.Version)
	}
	stamp := sqliteTimeValue(time.Now())
	r, err := s.db.ExecContext(ctx, `UPDATE inspirations SET deleted_at=?,updated_at=?,version=version+1 WHERE id=? AND user_id=? AND version=? AND deleted_at IS NULL`, stamp, stamp, id, userID, version)
	if err != nil {
		return err
	}
	if affected, _ := r.RowsAffected(); affected != 1 {
		return versionConflict(n.Version)
	}
	return nil
}

func inspirationWhere(userID string, q domain.InspirationQuery) (string, []any, error) {
	if len(q.Search) > 500 || q.Offset < 0 || q.Offset > 1000000 || q.Limit < 0 || q.Limit > 100 {
		return "", nil, domain.Invalid("灵感查询范围无效")
	}
	where := `i.user_id=? AND i.deleted_at IS NULL`
	args := []any{userID}
	if q.ProjectID != nil {
		where += ` AND COALESCE(i.project_id,'')=?`
		args = append(args, *q.ProjectID)
	}
	switch q.Status {
	case "", "all":
	case "open":
		where += ` AND i.completed=0`
	case "completed":
		where += ` AND i.completed=1`
	default:
		return "", nil, domain.Invalid("灵感状态无效")
	}
	if q.Search != "" {
		where += ` AND (instr(lower(i.title),lower(?))>0 OR instr(lower(i.markdown),lower(?))>0 OR i.id=?)`
		args = append(args, q.Search, q.Search, q.Search)
	}
	return where, args, nil
}
func (s *SQLite) ListInspirations(ctx context.Context, userID string, q domain.InspirationQuery) (domain.InspirationPage, error) {
	page := domain.InspirationPage{Items: []domain.InspirationCard{}}
	where, args, err := inspirationWhere(userID, q)
	if err != nil {
		return page, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 40
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM inspirations i WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	columns := strings.Replace(inspirationColumns, "markdown", "substr(markdown,1,360)", 1)
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM inspirations i WHERE `+where+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, append(args, limit, q.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		n, e := scanInspiration(rows)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, domain.InspirationCard{Versioned: n.Versioned, ProjectID: n.ProjectID, Title: n.Title, Preview: n.Markdown, Completed: n.Completed, CompletedAt: n.CompletedAt})
	}
	return page, rows.Err()
}
func (s *SQLite) InspirationGroups(ctx context.Context, userID string, q domain.InspirationQuery) ([]domain.InspirationGroup, error) {
	q.ProjectID = nil
	where, args, err := inspirationWhere(userID, q)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH counts AS (
 SELECT COALESCE(i.project_id,'') AS project_id,count(*) AS total,sum(i.completed) AS completed FROM inspirations i WHERE `+where+` GROUP BY i.project_id
), groups AS (
 SELECT p.id,p.name,p.deleted_at FROM projects p JOIN computers c ON c.id=p.computer_id WHERE c.user_id=? AND c.deleted_at IS NULL AND (p.deleted_at IS NULL OR p.id IN (SELECT project_id FROM counts))
 UNION ALL SELECT '','',NULL
)
SELECT g.id,g.name,g.deleted_at IS NOT NULL,COALESCE(c.total,0),COALESCE(c.completed,0) FROM groups g LEFT JOIN counts c ON c.project_id=g.id ORDER BY g.deleted_at IS NOT NULL,g.id<>'' COLLATE BINARY,g.name,g.id`, append(args, userID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []domain.InspirationGroup{}
	for rows.Next() {
		var g domain.InspirationGroup
		if err := rows.Scan(&g.ProjectID, &g.ProjectName, &g.ProjectDeleted, &g.Total, &g.Completed); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
