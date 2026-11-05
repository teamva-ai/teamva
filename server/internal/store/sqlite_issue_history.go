package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// FindInstanceSummaryComment uses the authoritative Run association, not a
// recent-N list. An original published summary remains revisable indefinitely.
func (s *SQLite) FindInstanceSummaryComment(ctx context.Context, userID, instanceID string) (domain.IssueComment, bool, error) {
	instance, err := s.GetIssueAgentInstanceByID(ctx, userID, instanceID)
	if err != nil {
		return domain.IssueComment{}, false, err
	}
	var item domain.IssueComment
	err = scanSQLiteIssueComment(s.db.QueryRowContext(ctx, sqliteIssueCommentSelect+`
		JOIN runs comment_run ON comment_run.id = comments.run_id AND comment_run.issue_id = comments.issue_id
		WHERE comments.issue_id = ? AND comments.author_kind = 'agent' AND comments.author_agent_id = ?
		  AND comment_run.issue_agent_instance_id = ? AND comments.status = 'published'
		  AND comments.deleted_at IS NULL
		ORDER BY comments.created_at DESC, comments.id DESC LIMIT 1
	`, instance.IssueID, instance.AgentID, instance.ID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, false, nil
	}
	return item, err == nil, err
}

// ListIssuePromptComments loads a bounded recent window, not the entire Issue
// transcript. SQL clips individual bodies before they enter Go memory.
func (s *SQLite) ListIssuePromptComments(ctx context.Context, userID, issueID string) ([]domain.IssueComment, bool, error) {
	const maxComments, maxBodyRunes = 100, 4096
	if err := requireSQLiteIssueOwner(ctx, s.db, userID, issueID); err != nil {
		return nil, false, err
	}
	selection := strings.Replace(sqliteIssueCommentSelect, "comments.body,", "substr(comments.body, 1, 4097),", 1)
	rows, err := s.db.QueryContext(ctx, selection+`
		WHERE comments.issue_id = ? AND comments.deleted_at IS NULL AND comments.status = 'published'
		ORDER BY comments.created_at DESC, comments.id DESC LIMIT ?
	`, issueID, maxComments+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]domain.IssueComment, 0, maxComments)
	truncated := false
	for rows.Next() {
		if len(items) == maxComments {
			truncated = true
			break
		}
		var item domain.IssueComment
		if err := scanSQLiteIssueComment(rows, &item); err != nil {
			return nil, false, err
		}
		body := []rune(item.Body)
		if len(body) > maxBodyRunes {
			item.Body = string(body[:maxBodyRunes]) + "…"
			truncated = true
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, truncated, nil
}

// ListRunsByIssuePage returns newest-first keyset pages, allowing old history
// to remain accessible without hiding current runs behind the oldest 200.
func (s *SQLite) ListRunsByIssuePage(ctx context.Context, userID, issueID, cursor string, limit int) (domain.Page[domain.Run], error) {
	if limit < 1 || limit > 200 {
		return domain.Page[domain.Run]{}, domain.Invalid("limit 必须在 1 到 200 之间")
	}
	if err := requireSQLiteIssueOwner(ctx, s.db, userID, issueID); err != nil {
		return domain.Page[domain.Run]{}, err
	}
	cursorTime, cursorID, err := decodeRunPageCursor(cursor)
	if err != nil {
		return domain.Page[domain.Run]{}, err
	}
	query := sqliteRunSelect + ` WHERE runs.issue_id = ? AND runs.deleted_at IS NULL`
	args := []any{issueID}
	if cursorID != "" {
		query += ` AND (runs.created_at < ? OR (runs.created_at = ? AND runs.id < ?))`
		stamp := sqliteTimeValue(cursorTime)
		args = append(args, stamp, stamp, cursorID)
	}
	query += ` ORDER BY runs.created_at DESC, runs.id DESC LIMIT ?`
	args = append(args, limit+1)
	items, err := s.listSQLiteRuns(ctx, query, args...)
	if err != nil {
		return domain.Page[domain.Run]{}, err
	}
	return finishRunPage(items, limit), nil
}

type IssueRunEventHead struct {
	RunID    string
	Sequence int64
}

// Event subscribers need only identities and indexed sequence heads, not historical prompts or payloads.
func (s *SQLite) ListIssueRunEventHeadsPage(ctx context.Context, userID, issueID, cursor string, limit int) (domain.Page[IssueRunEventHead], error) {
	page := domain.Page[IssueRunEventHead]{Items: []IssueRunEventHead{}}
	if limit < 1 || limit > 200 {
		return page, domain.Invalid("limit 必须在 1 到 200 之间")
	}
	if err := requireSQLiteIssueOwner(ctx, s.db, userID, issueID); err != nil {
		return page, err
	}
	cursorTime, cursorID, err := decodeRunPageCursor(cursor)
	if err != nil {
		return page, err
	}
	query := `SELECT id, created_at, COALESCE((SELECT max(sequence) FROM run_events WHERE run_id=runs.id),0)
		FROM runs WHERE issue_id = ? AND deleted_at IS NULL`
	args := []any{issueID}
	if cursorID != "" {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		stamp := sqliteTimeValue(cursorTime)
		args = append(args, stamp, stamp, cursorID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastID, lastStamp string
	for rows.Next() {
		if len(page.Items) == limit {
			stamp, err := time.Parse(time.RFC3339Nano, lastStamp)
			if err != nil {
				return page, err
			}
			page.NextCursor = encodeRunPageCursor(domain.Run{Versioned: domain.Versioned{ID: lastID, CreatedAt: stamp}})
			break
		}
		var sequence int64
		if err := rows.Scan(&lastID, &lastStamp, &sequence); err != nil {
			return page, err
		}
		page.Items = append(page.Items, IssueRunEventHead{RunID: lastID, Sequence: sequence})
	}
	return page, rows.Err()
}
