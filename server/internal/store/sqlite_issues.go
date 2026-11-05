package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Issue 相关实体全部通过 issues -> projects -> computers -> users 确定租户，
// 所有关系均直接从 SQLite 读取。

const sqliteIssueSelect = `
	SELECT issues.id, issues.version, issues.created_at, issues.updated_at,
	       issues.deleted_at, issues.project_id, issues.title, issues.description,
	       issues.status, issues.worktree_hint, issues.created_by_user_id
	FROM issues
`

const sqliteIssueOwnership = `
	JOIN projects owning_project ON owning_project.id = issues.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func (s *SQLite) ListIssues(ctx context.Context, userID, projectID string) ([]domain.Issue, error) {
	if _, err := s.GetProject(ctx, userID, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteIssueSelect+sqliteIssueOwnership+`
		WHERE issues.project_id = ? AND owning_computer.user_id = ?
		  AND issues.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY issues.created_at, issues.id
	`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Issue, 0)
	for rows.Next() {
		var item domain.Issue
		if err := scanSQLiteIssue(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) ListIssuesPage(ctx context.Context, userID, projectID string, q IssueListQuery) (IssuePage, error) {
	page := IssuePage{Page: domain.Page[domain.Issue]{Items: []domain.Issue{}}}
	if err := q.validate(); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := requireSQLiteProjectOwner(ctx, tx, userID, projectID); err != nil {
		return page, err
	}
	where := ` WHERE issues.project_id = ? AND issues.deleted_at IS NULL`
	args := []any{projectID}
	if q.Status != "" {
		where += ` AND issues.status = ?`
		args = append(args, q.Status)
	}
	if search := strings.ToLower(strings.TrimSpace(q.Search)); search != "" {
		where += ` AND teamva_issue_title_contains(issues.title, ?)`
		args = append(args, search)
	}
	items, pagination, err := assistantHistoryRows(ctx, tx,
		assistantHistoryRequest{Offset: q.Offset, Limit: &q.Limit},
		`SELECT count(*) FROM issues`+where,
		sqliteIssueSelect+where+` ORDER BY issues.updated_at DESC, issues.id DESC`, args,
		func(row scanner) (domain.Issue, error) {
			var item domain.Issue
			err := scanSQLiteIssue(row, &item)
			return item, err
		})
	if err != nil {
		return page, err
	}
	page.Items, page.Total = items, pagination.Total
	return page, nil
}

func (s *SQLite) GetIssue(ctx context.Context, userID, id string) (domain.Issue, error) {
	var item domain.Issue
	err := scanSQLiteIssue(s.db.QueryRowContext(ctx, sqliteIssueSelect+sqliteIssueOwnership+`
		WHERE issues.id = ? AND owning_computer.user_id = ?
		  AND issues.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Issue{}, domain.NotFound("任务", id)
	}
	return item, err
}

func (s *SQLite) CreateIssue(ctx context.Context, userID string, input domain.Issue) (domain.Issue, error) {
	autoTitle := strings.TrimSpace(input.Title) == ""
	if autoTitle {
		input.Title = "新任务"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Issue{}, err
	}
	defer func() { _ = tx.Rollback() }()
	status := input.Status
	if status == "" {
		status = domain.IssueOpen
	}
	now := sqliteTimeValue(time.Now())
	var item domain.Issue
	err = scanSQLiteIssue(tx.QueryRowContext(ctx, `
		INSERT INTO issues (
			id, project_id, title, description, status, worktree_hint,
			created_by_user_id, version, created_at, updated_at
		)
		SELECT ?, owning_project.id, ?, ?, ?, ?, owning_user.id, 1, ?, ?
		FROM projects owning_project
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_project.id = ? AND owning_computer.user_id = ?
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at, project_id,
		          title, description, status, worktree_hint, created_by_user_id
	`, newID("iss"), input.Title, input.Description, status, boolInt(input.WorktreeHint),
		now, now, input.ProjectID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Issue{}, domain.NotFound("项目", input.ProjectID)
	}
	if err != nil {
		return domain.Issue{}, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "issue", item.ID); err != nil {
			return domain.Issue{}, err
		}
		if strings.TrimSpace(input.Description) != "" {
			if err := queueAutoTitleTx(ctx, tx, userID, "issue", item.ID, input.Description); err != nil {
				return domain.Issue{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Issue{}, err
	}
	return item, nil
}

func (s *SQLite) UpdateIssue(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.Issue)) (domain.Issue, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Issue{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.Issue
	err = scanSQLiteIssue(tx.QueryRowContext(ctx, sqliteIssueSelect+sqliteIssueOwnership+`
		WHERE issues.id = ? AND owning_computer.user_id = ?
		  AND issues.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Issue{}, domain.NotFound("任务", id)
	}
	if err != nil {
		return domain.Issue{}, err
	}
	if item.Version != expectedVersion {
		return domain.Issue{}, versionConflict(item.Version)
	}
	previousTitle, previousDescription := item.Title, item.Description
	update(&item)
	if item.TitleEdited || item.Title != previousTitle {
		if err := manualAutoTitleTx(ctx, tx, "issue", id); err != nil {
			return domain.Issue{}, err
		}
	} else if item.Description != previousDescription && strings.TrimSpace(item.Description) != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "issue", id, item.Description); err != nil {
			return domain.Issue{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET title = ?, description = ?, status = ?, worktree_hint = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id
		      FROM projects owning_project
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_project.deleted_at IS NULL
		        AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		  )
	`, item.Title, item.Description, item.Status, boolInt(item.WorktreeHint),
		sqliteTimeValue(time.Now()), id, expectedVersion, userID)
	if err != nil {
		return domain.Issue{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		var currentVersion int64
		lookupErr := tx.QueryRowContext(ctx, `
			SELECT issues.version
			FROM issues
			JOIN projects project ON project.id = issues.project_id
			JOIN computers computer ON computer.id = project.computer_id
			WHERE issues.id = ? AND computer.user_id = ? AND issues.deleted_at IS NULL
		`, id, userID).Scan(&currentVersion)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return domain.Issue{}, domain.NotFound("任务", id)
		}
		if lookupErr != nil {
			return domain.Issue{}, lookupErr
		}
		return domain.Issue{}, versionConflict(currentVersion)
	}
	if err := scanSQLiteIssue(tx.QueryRowContext(ctx, sqliteIssueSelect+` WHERE issues.id = ?`, id), &item); err != nil {
		return domain.Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Issue{}, err
	}
	return item, nil
}

func (s *SQLite) DeleteIssue(ctx context.Context, userID, id string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND project_id IN (
		      SELECT owning_project.id
		      FROM projects owning_project
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
		if _, err := tx.ExecContext(ctx, `UPDATE app_metadata SET value=json_set(value,'$.cancelled',json('true')) WHERE key GLOB ?`, issueWorkPrefix+id+":*"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_metadata WHERE key GLOB ?`, issueWorkPendingPrefix+id+":*"); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := tx.Rollback(); err != nil {
		return err
	}
	current, getErr := s.GetIssue(ctx, userID, id)
	if getErr != nil {
		return getErr
	}
	return versionConflict(current.Version)
}

// --- Issue comments / mentions / revisions ---

const sqliteIssueCommentSelect = `
	SELECT comments.id, comments.version, comments.created_at, comments.updated_at,
	       comments.deleted_at, comments.issue_id, comments.author_kind,
	       COALESCE(comments.author_user_id, ''), COALESCE(comments.author_agent_id, ''),
	       comments.body, COALESCE(comments.run_id, ''), comments.status, comments.edited_at
	FROM issue_comments comments
`

const sqliteIssueCommentOwnership = `
	JOIN issues owning_issue ON owning_issue.id = comments.issue_id
	JOIN projects owning_project ON owning_project.id = owning_issue.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func (s *SQLite) ListIssueComments(ctx context.Context, userID, issueID string) ([]domain.IssueComment, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireSQLiteIssueOwner(ctx, tx, userID, issueID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, sqliteIssueCommentSelect+sqliteIssueCommentOwnership+`
		WHERE comments.issue_id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY comments.created_at, comments.id
	`, issueID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.IssueComment, 0)
	for rows.Next() {
		var item domain.IssueComment
		if err := scanSQLiteIssueComment(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for index := range items {
		if err := hydrateSQLiteIssueWorkItems(ctx, tx, &items[index]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLite) GetIssueComment(ctx context.Context, userID, id string) (domain.IssueComment, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.IssueComment{}, err
	}
	defer tx.Rollback()
	var item domain.IssueComment
	err = scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+sqliteIssueCommentOwnership+`
		WHERE comments.id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, domain.NotFound("评论", id)
	}
	if err == nil {
		err = hydrateSQLiteIssueWorkItems(ctx, tx, &item)
	}
	return item, err
}

// CreateIssueComment 将评论和所有结构化 mentions 放在同一事务中。任一作者、
// run 或 mention 校验失败时，不会留下半条评论。
func (s *SQLite) CreateIssueComment(ctx context.Context, userID string, input domain.IssueComment, mentionedAgentIDs []string) (domain.IssueComment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IssueComment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, err := createSQLiteIssueCommentTx(ctx, tx, userID, input, mentionedAgentIDs)
	if err != nil {
		return domain.IssueComment{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.IssueComment{}, err
	}
	return item, nil
}

func createSQLiteIssueCommentTx(ctx context.Context, tx *sql.Tx, userID string, input domain.IssueComment, mentionedAgentIDs []string) (domain.IssueComment, error) {
	var projectComputerID, issueStatus string
	err := tx.QueryRowContext(ctx, `
		SELECT owning_project.computer_id, owning_issue.status
		FROM issues owning_issue
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_issue.id = ? AND owning_computer.user_id = ?
		  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, input.IssueID, userID).Scan(&projectComputerID, &issueStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, domain.NotFound("任务", input.IssueID)
	}
	if err != nil {
		return domain.IssueComment{}, err
	}
	if len(mentionedAgentIDs) > 0 {
		if err := requireSQLiteIssueOpen(issueStatus); err != nil {
			return domain.IssueComment{}, err
		}
	}

	authorUserID, authorAgentID := "", ""
	switch input.AuthorKind {
	case domain.IssueCommentAuthorUser:
		authorUserID = userID
	case domain.IssueCommentAuthorAgent:
		if input.AuthorAgentID == "" {
			return domain.IssueComment{}, domain.Invalid("Agent 评论缺少作者")
		}
		authorAgentID = input.AuthorAgentID
		var instanceExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM issue_agent_instances instances
			JOIN issues owning_issue ON owning_issue.id = instances.issue_id
			JOIN projects owning_project ON owning_project.id = owning_issue.project_id
			JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
			JOIN users owning_user ON owning_user.id = owning_computer.user_id
			WHERE instances.issue_id = ? AND instances.agent_id = ?
			  AND owning_computer.user_id = ? AND instances.deleted_at IS NULL
			  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
			  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		`, input.IssueID, authorAgentID, userID).Scan(&instanceExists); err != nil {
			return domain.IssueComment{}, err
		}
		if instanceExists == 0 {
			return domain.IssueComment{}, domain.Invalid("Agent 实例不存在")
		}
	default:
		return domain.IssueComment{}, domain.Invalid("评论作者类型无效")
	}

	if input.RunID != "" {
		var runExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM runs
			JOIN issues owning_issue ON owning_issue.id = runs.issue_id
			JOIN projects owning_project ON owning_project.id = owning_issue.project_id
			JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
			JOIN users owning_user ON owning_user.id = owning_computer.user_id
			WHERE runs.id = ? AND runs.issue_id = ? AND owning_computer.user_id = ?
			  AND runs.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
			  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
			  AND owning_user.deleted_at IS NULL
		`, input.RunID, input.IssueID, userID).Scan(&runExists); err != nil {
			return domain.IssueComment{}, err
		}
		if runExists == 0 {
			return domain.IssueComment{}, domain.Invalid("评论关联的运行不存在")
		}
	}

	seen := make(map[string]struct{}, len(mentionedAgentIDs))
	for _, agentID := range mentionedAgentIDs {
		if _, duplicate := seen[agentID]; duplicate {
			return domain.IssueComment{}, domain.Invalid("不能重复 @ 同一个 Agent")
		}
		seen[agentID] = struct{}{}
		if authorAgentID != "" && authorAgentID == agentID {
			return domain.IssueComment{}, domain.Invalid("Agent 不能直接 @ 自己")
		}
		var valid int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM agents
			JOIN computers owning_computer ON owning_computer.id = agents.computer_id
			JOIN users owning_user ON owning_user.id = owning_computer.user_id
			WHERE agents.id = ? AND agents.computer_id = ?
			  AND owning_computer.user_id = ? AND agents.status = 'active'
			  AND agents.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
			  AND owning_user.deleted_at IS NULL
		`, agentID, projectComputerID, userID).Scan(&valid); err != nil {
			return domain.IssueComment{}, err
		}
		if valid == 0 {
			return domain.IssueComment{}, domain.Invalid("被 @ 的 Agent 不属于当前项目电脑")
		}
	}

	now := sqliteTimeValue(time.Now())
	if input.Status == "" {
		input.Status = domain.IssueCommentPublished
	}
	var item domain.IssueComment
	err = scanSQLiteIssueComment(tx.QueryRowContext(ctx, `
		INSERT INTO issue_comments (
			id, issue_id, author_kind, author_user_id, author_agent_id,
			body, run_id, status, version, created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, 1, ?, ?)
		RETURNING id, version, created_at, updated_at, deleted_at, issue_id,
		          author_kind, COALESCE(author_user_id, ''), COALESCE(author_agent_id, ''),
		          body, COALESCE(run_id, ''), status, edited_at
	`, newID("cmt"), input.IssueID, input.AuthorKind, authorUserID, authorAgentID,
		input.Body, input.RunID, input.Status, now, now), &item)
	if err != nil {
		return domain.IssueComment{}, err
	}
	for _, agentID := range mentionedAgentIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_comment_mentions (
				id, comment_id, target_kind, target_agent_id
			) VALUES (?, ?, 'agent', ?)
		`, newID("men"), item.ID, agentID); err != nil {
			return domain.IssueComment{}, err
		}
	}
	if item.AuthorKind == domain.IssueCommentAuthorUser && item.Status == domain.IssueCommentPublished && strings.TrimSpace(item.Body) != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "issue", item.IssueID, item.Body); err != nil {
			return domain.IssueComment{}, err
		}
	}
	return item, nil
}

// AddIssueCommentMentions attaches new activations for direct server workflows.
// Agent tools use WriteRunIssueComment instead, so publication, Run fencing and
// these activations cannot be committed independently.
func (s *SQLite) AddIssueCommentMentions(ctx context.Context, userID, commentID, authorAgentID string, mentionedAgentIDs []string) error {
	if len(mentionedAgentIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var computerID, issueStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT project.computer_id, issue.status
		FROM issue_comments comment
		JOIN issues issue ON issue.id = comment.issue_id
		JOIN projects project ON project.id = issue.project_id
		JOIN computers computer ON computer.id = project.computer_id
		WHERE comment.id = ? AND comment.author_kind = 'agent'
		  AND comment.author_agent_id = ? AND computer.user_id = ?
		  AND comment.deleted_at IS NULL AND issue.deleted_at IS NULL
		  AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
	`, commentID, authorAgentID, userID).Scan(&computerID, &issueStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("评论", commentID)
	}
	if err != nil {
		return err
	}
	if err := requireSQLiteIssueOpen(issueStatus); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(mentionedAgentIDs))
	for _, agentID := range mentionedAgentIDs {
		if agentID == authorAgentID {
			return domain.Invalid("Agent 不能直接 @ 自己")
		}
		if _, exists := seen[agentID]; exists {
			continue
		}
		seen[agentID] = struct{}{}
		var valid int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM agents
			WHERE id = ? AND computer_id = ? AND status = 'active' AND deleted_at IS NULL
		`, agentID, computerID).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			return domain.Invalid("被 @ 的 Agent 不属于当前项目电脑")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_comment_mentions (
				id, comment_id, target_kind, target_agent_id
			) VALUES (?, ?, 'agent', ?)
		`, newID("men"), commentID, agentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) UpdateIssueComment(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.IssueComment) error) (domain.IssueComment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IssueComment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var item domain.IssueComment
	err = scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+sqliteIssueCommentOwnership+`
		WHERE comments.id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, domain.NotFound("评论", id)
	}
	if err != nil {
		return domain.IssueComment{}, err
	}
	if item.Version != expectedVersion {
		return domain.IssueComment{}, versionConflict(item.Version)
	}
	previousBody := item.Body
	if err := update(&item); err != nil {
		return domain.IssueComment{}, err
	}
	bodyChanged := item.Body != previousBody
	now := sqliteTimeValue(time.Now())
	if bodyChanged {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_comment_revisions (id, comment_id, body, created_at)
			VALUES (?, ?, ?, ?)
		`, newID("cmr"), item.ID, previousBody, now); err != nil {
			return domain.IssueComment{}, err
		}
	}
	var updated domain.IssueComment
	err = scanSQLiteIssueComment(tx.QueryRowContext(ctx, `
		UPDATE issue_comments
		SET body = ?, status = ?, edited_at = CASE WHEN ? THEN ? ELSE edited_at END,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND issue_id IN (
		      SELECT owning_issue.id
		      FROM issues owning_issue
		      JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_issue.deleted_at IS NULL
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
		RETURNING id, version, created_at, updated_at, deleted_at, issue_id,
		          author_kind, COALESCE(author_user_id, ''), COALESCE(author_agent_id, ''),
		          body, COALESCE(run_id, ''), status, edited_at
	`, item.Body, item.Status, boolInt(bodyChanged), now, now, id, expectedVersion, userID), &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, versionConflict(item.Version)
	}
	if err != nil {
		return domain.IssueComment{}, err
	}
	if err := hydrateSQLiteIssueWorkItems(ctx, tx, &updated); err != nil {
		return domain.IssueComment{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.IssueComment{}, err
	}
	return updated, nil
}

// DeleteIssueComment 软删除评论：用户评论与 Agent 评论都可以删除。删除后
// 评论不再出现在列表、修订定位与工具结果里，未派发的 @ 同步失效，避免评论
// 删除后仍然触发 Agent 运行。
func (s *SQLite) DeleteIssueComment(ctx context.Context, userID, id string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE issue_comments
		SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND issue_id IN (
		      SELECT owning_issue.id
		      FROM issues owning_issue
		      JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_issue.deleted_at IS NULL
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, now, now, id, expectedVersion, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		var current int64
		err := tx.QueryRowContext(ctx, `
			SELECT comments.version
			FROM issue_comments comments
			JOIN issues owning_issue ON owning_issue.id = comments.issue_id
			JOIN projects owning_project ON owning_project.id = owning_issue.project_id
			JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
			JOIN users owning_user ON owning_user.id = owning_computer.user_id
			WHERE comments.id = ? AND owning_computer.user_id = ?
			  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
			  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
			  AND owning_user.deleted_at IS NULL
		`, id, userID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NotFound("评论", id)
		}
		if err != nil {
			return err
		}
		return versionConflict(current)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET status = 'dead_letter', last_error = 'mention_target_deleted',
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE comment_id = ? AND target_kind = 'agent' AND status IN ('pending', 'processing')
	`, id); err != nil {
		return err
	}
	if err := cancelSQLiteCommentWorkItemsTx(ctx, tx, id, true); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) ListIssueCommentMentions(ctx context.Context, userID, issueID string) ([]domain.IssueCommentMention, error) {
	if _, err := s.GetIssue(ctx, userID, issueID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT mentions.id, mentions.comment_id, mentions.target_kind,
		       COALESCE(mentions.target_agent_id, ''), mentions.consumed_at
		FROM issue_comment_mentions mentions
		JOIN issue_comments comments ON comments.id = mentions.comment_id
		JOIN issues owning_issue ON owning_issue.id = comments.issue_id
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE comments.issue_id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY mentions.id
	`, issueID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.IssueCommentMention, 0)
	for rows.Next() {
		var item domain.IssueCommentMention
		var consumed sql.NullString
		if err := rows.Scan(&item.ID, &item.CommentID, &item.TargetKind,
			&item.TargetAgentID, &consumed); err != nil {
			return nil, err
		}
		item.ConsumedAt, err = sqliteParseNullTime(consumed)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) ConsumeIssueCommentMention(ctx context.Context, userID, issueID, mentionID string) error {
	if _, err := s.GetIssue(ctx, userID, issueID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET consumed_at = ?, status = 'completed', claimed_at = NULL,
		    lease_until = NULL, claim_token = NULL, last_error = NULL
		WHERE id = ? AND consumed_at IS NULL
		  AND comment_id IN (
		      SELECT comments.id
		      FROM issue_comments comments
		      JOIN issues owning_issue ON owning_issue.id = comments.issue_id
		      JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE comments.issue_id = ? AND owning_computer.user_id = ?
		        AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, sqliteTimeValue(time.Now()), mentionID, issueID, userID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM issue_comment_mentions mentions
		JOIN issue_comments comments ON comments.id = mentions.comment_id
		JOIN issues owning_issue ON owning_issue.id = comments.issue_id
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE mentions.id = ? AND comments.issue_id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, mentionID, issueID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 { // 已消费，保持幂等。
		return nil
	}
	return domain.NotFound("提及", mentionID)
}

func (s *SQLite) ListIssueCommentRevisions(ctx context.Context, userID, commentID string) ([]domain.IssueCommentRevision, error) {
	if _, err := s.GetIssueComment(ctx, userID, commentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT revisions.id, revisions.comment_id, revisions.body, revisions.created_at
		FROM issue_comment_revisions revisions
		JOIN issue_comments comments ON comments.id = revisions.comment_id
		JOIN issues owning_issue ON owning_issue.id = comments.issue_id
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE revisions.comment_id = ? AND owning_computer.user_id = ?
		  AND comments.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY revisions.created_at, revisions.id
	`, commentID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.IssueCommentRevision, 0)
	for rows.Next() {
		var item domain.IssueCommentRevision
		var created string
		if err := rows.Scan(&item.ID, &item.CommentID, &item.Body, &created); err != nil {
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

// --- Issue Agent instances / generations ---

const sqliteIssueAgentInstanceSelect = `
	SELECT instances.id, instances.version, instances.created_at, instances.updated_at,
	       instances.deleted_at, instances.issue_id, instances.agent_id,
	       instances.agent_revision_id, instances.status, instances.last_activity_at,
	       instances.pending_mention_count
	FROM issue_agent_instances instances
`

const sqliteIssueAgentInstanceOwnership = `
	JOIN issues owning_issue ON owning_issue.id = instances.issue_id
	JOIN projects owning_project ON owning_project.id = owning_issue.project_id
	JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
	JOIN users owning_user ON owning_user.id = owning_computer.user_id
`

func (s *SQLite) ListIssueAgentInstances(ctx context.Context, userID, issueID string) ([]domain.IssueAgentInstance, error) {
	if _, err := s.GetIssue(ctx, userID, issueID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteIssueAgentInstanceSelect+sqliteIssueAgentInstanceOwnership+`
		WHERE instances.issue_id = ? AND owning_computer.user_id = ?
		  AND instances.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY instances.created_at, instances.id
	`, issueID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.IssueAgentInstance, 0)
	for rows.Next() {
		var item domain.IssueAgentInstance
		if err := scanSQLiteIssueAgentInstance(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) GetIssueAgentInstance(ctx context.Context, userID, issueID, agentID string) (domain.IssueAgentInstance, error) {
	if _, err := s.GetIssue(ctx, userID, issueID); err != nil {
		return domain.IssueAgentInstance{}, err
	}
	var item domain.IssueAgentInstance
	err := scanSQLiteIssueAgentInstance(s.db.QueryRowContext(ctx, sqliteIssueAgentInstanceSelect+sqliteIssueAgentInstanceOwnership+`
		WHERE instances.issue_id = ? AND instances.agent_id = ?
		  AND owning_computer.user_id = ? AND instances.deleted_at IS NULL
		  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		ORDER BY instances.created_at DESC, instances.id DESC
		LIMIT 1
	`, issueID, agentID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentInstance{}, domain.NotFound("Agent 实例", agentID)
	}
	return item, err
}

func (s *SQLite) GetIssueAgentInstanceByID(ctx context.Context, userID, id string) (domain.IssueAgentInstance, error) {
	return s.getIssueAgentInstanceByID(ctx, userID, id)
}

// EnsureIssueAgentInstance returns the latest conversation for direct server
// workflows or creates the first one. Structured @ mentions always create a
// new conversation instead.
func (s *SQLite) EnsureIssueAgentInstance(ctx context.Context, userID, issueID, agentID string) (domain.IssueAgentInstance, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IssueAgentInstance{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var projectComputerID string
	err = tx.QueryRowContext(ctx, `
		SELECT owning_project.computer_id
		FROM issues owning_issue
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_issue.id = ? AND owning_computer.user_id = ?
		  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, issueID, userID).Scan(&projectComputerID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentInstance{}, false, domain.NotFound("任务", issueID)
	}
	if err != nil {
		return domain.IssueAgentInstance{}, false, err
	}

	var currentRevisionID string
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(agents.current_revision_id, '')
		FROM agents
		JOIN computers owning_computer ON owning_computer.id = agents.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE agents.id = ? AND agents.computer_id = ? AND owning_computer.user_id = ?
		  AND agents.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, agentID, projectComputerID, userID).Scan(&currentRevisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentInstance{}, false, domain.NotFound("Agent", agentID)
	}
	if err != nil {
		return domain.IssueAgentInstance{}, false, err
	}
	if currentRevisionID == "" {
		return domain.IssueAgentInstance{}, false, domain.Invalid("Agent 尚未生成配置修订")
	}

	var existing domain.IssueAgentInstance
	err = scanSQLiteIssueAgentInstance(tx.QueryRowContext(ctx, sqliteIssueAgentInstanceSelect+`
		WHERE instances.issue_id = ? AND instances.agent_id = ?
		  AND instances.deleted_at IS NULL
		ORDER BY instances.created_at DESC, instances.id DESC
		LIMIT 1
	`, issueID, agentID), &existing)
	if err == nil {
		if existing.AgentRevisionID != currentRevisionID {
			now := sqliteTimeValue(time.Now())
			if _, err := tx.ExecContext(ctx, `
				UPDATE issue_agent_instances
				SET agent_revision_id = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND deleted_at IS NULL
			`, currentRevisionID, now, existing.ID); err != nil {
				return domain.IssueAgentInstance{}, false, err
			}
			existing.AgentRevisionID = currentRevisionID
			existing.Version++
			existing.UpdatedAt, _ = sqliteParseTime(now)
		}
		if err := tx.Commit(); err != nil {
			return domain.IssueAgentInstance{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentInstance{}, false, err
	}

	now := sqliteTimeValue(time.Now())
	instanceID := newID("iai")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO issue_agent_instances (
			id, issue_id, agent_id, agent_revision_id, status,
			version, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'idle', 1, ?, ?)
	`, instanceID, issueID, agentID, currentRevisionID, now, now)
	if err != nil {
		return domain.IssueAgentInstance{}, false, err
	}
	var item domain.IssueAgentInstance
	err = scanSQLiteIssueAgentInstance(tx.QueryRowContext(ctx, sqliteIssueAgentInstanceSelect+`
		WHERE instances.id = ? AND instances.deleted_at IS NULL
	`, instanceID), &item)
	if err != nil {
		return domain.IssueAgentInstance{}, false, err
	}
	if item.AgentRevisionID != currentRevisionID {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_agent_instances
			SET agent_revision_id = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL
		`, currentRevisionID, now, item.ID); err != nil {
			return domain.IssueAgentInstance{}, false, err
		}
		item.AgentRevisionID = currentRevisionID
		item.Version++
		item.UpdatedAt, _ = sqliteParseTime(now)
	}
	if err := tx.Commit(); err != nil {
		return domain.IssueAgentInstance{}, false, err
	}
	return item, true, nil
}

func (s *SQLite) UpdateIssueAgentInstance(ctx context.Context, userID, id string, expectedVersion int64, update func(*domain.IssueAgentInstance)) (domain.IssueAgentInstance, error) {
	item, err := s.getIssueAgentInstanceByID(ctx, userID, id)
	if err != nil {
		return domain.IssueAgentInstance{}, err
	}
	if item.Version != expectedVersion {
		return domain.IssueAgentInstance{}, versionConflict(item.Version)
	}
	update(&item)
	result, err := s.db.ExecContext(ctx, `
		UPDATE issue_agent_instances
		SET status = ?, last_activity_at = ?, pending_mention_count = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
		  AND issue_id IN (
		      SELECT owning_issue.id
		      FROM issues owning_issue
		      JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		      JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		      JOIN users owning_user ON owning_user.id = owning_computer.user_id
		      WHERE owning_computer.user_id = ? AND owning_issue.deleted_at IS NULL
		        AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		        AND owning_user.deleted_at IS NULL
		  )
	`, item.Status, nullableSQLiteTime(item.LastActivityAt), item.PendingMentionCount,
		sqliteTimeValue(time.Now()), id, expectedVersion, userID)
	if err != nil {
		return domain.IssueAgentInstance{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		current, getErr := s.getIssueAgentInstanceByID(ctx, userID, id)
		if getErr != nil {
			return domain.IssueAgentInstance{}, getErr
		}
		return domain.IssueAgentInstance{}, versionConflict(current.Version)
	}
	return s.getIssueAgentInstanceByID(ctx, userID, id)
}

func (s *SQLite) getIssueAgentInstanceByID(ctx context.Context, userID, id string) (domain.IssueAgentInstance, error) {
	var item domain.IssueAgentInstance
	err := scanSQLiteIssueAgentInstance(s.db.QueryRowContext(ctx, sqliteIssueAgentInstanceSelect+sqliteIssueAgentInstanceOwnership+`
		WHERE instances.id = ? AND owning_computer.user_id = ?
		  AND instances.deleted_at IS NULL AND owning_issue.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentInstance{}, domain.NotFound("Agent 实例", id)
	}
	return item, err
}

func (s *SQLite) CreateIssueAgentGeneration(ctx context.Context, userID string, input domain.IssueAgentGeneration) (domain.IssueAgentGeneration, error) {
	now := sqliteTimeValue(time.Now())
	var item domain.IssueAgentGeneration
	err := scanSQLiteIssueAgentGeneration(s.db.QueryRowContext(ctx, `
		INSERT INTO issue_agent_generations (
			id, issue_agent_instance_id, agent_revision_id, native_session_key,
			version, created_at, updated_at
		)
		SELECT ?, instances.id, revisions.id, NULLIF(?, ''), 1, ?, ?
		FROM issue_agent_instances instances
		JOIN agent_revisions revisions
		  ON revisions.id = ? AND revisions.agent_id = instances.agent_id
		JOIN issues owning_issue ON owning_issue.id = instances.issue_id
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE instances.id = ? AND owning_computer.user_id = ?
		  AND instances.deleted_at IS NULL AND revisions.deleted_at IS NULL
		  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		RETURNING id, version, created_at, updated_at, deleted_at,
		          issue_agent_instance_id, agent_revision_id,
		          COALESCE(native_session_key, '')
	`, newID("iag"), input.NativeSessionKey, now, now, input.AgentRevisionID,
		input.IssueAgentInstanceID, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentGeneration{}, domain.NotFound("Agent 实例", input.IssueAgentInstanceID)
	}
	return item, err
}

func (s *SQLite) GetIssueAgentGeneration(ctx context.Context, userID, id string) (domain.IssueAgentGeneration, error) {
	var item domain.IssueAgentGeneration
	err := scanSQLiteIssueAgentGeneration(s.db.QueryRowContext(ctx, `
		SELECT generations.id, generations.version, generations.created_at,
		       generations.updated_at, generations.deleted_at,
		       generations.issue_agent_instance_id, generations.agent_revision_id,
		       COALESCE(generations.native_session_key, '')
		FROM issue_agent_generations generations
		JOIN issue_agent_instances instances ON instances.id = generations.issue_agent_instance_id
		JOIN issues owning_issue ON owning_issue.id = instances.issue_id
		JOIN projects owning_project ON owning_project.id = owning_issue.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE generations.id = ? AND owning_computer.user_id = ?
		  AND generations.deleted_at IS NULL AND instances.deleted_at IS NULL
		  AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueAgentGeneration{}, domain.NotFound("Agent 世代", id)
	}
	return item, err
}

func scanSQLiteIssue(row scanner, item *domain.Issue) error {
	var created, updated string
	var deleted sql.NullString
	var worktreeHint int
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ProjectID, &item.Title, &item.Description, &item.Status,
		&worktreeHint, &item.CreatedByUserID); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	item.WorktreeHint = worktreeHint != 0
	return nil
}

func scanSQLiteIssueComment(row scanner, item *domain.IssueComment) error {
	var created, updated string
	var deleted, edited sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.IssueID, &item.AuthorKind, &item.AuthorUserID, &item.AuthorAgentID,
		&item.Body, &item.RunID, &item.Status, &edited); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	var err error
	item.EditedAt, err = sqliteParseNullTime(edited)
	return err
}

func scanSQLiteIssueAgentInstance(row scanner, item *domain.IssueAgentInstance) error {
	var created, updated string
	var deleted, lastActivity sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.IssueID, &item.AgentID, &item.AgentRevisionID, &item.Status,
		&lastActivity, &item.PendingMentionCount); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	var err error
	item.LastActivityAt, err = sqliteParseNullTime(lastActivity)
	return err
}

func scanSQLiteIssueAgentGeneration(row scanner, item *domain.IssueAgentGeneration) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.IssueAgentInstanceID, &item.AgentRevisionID, &item.NativeSessionKey); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}
