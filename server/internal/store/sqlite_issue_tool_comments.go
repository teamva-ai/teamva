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

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// RunIssueCommentWrite is deliberately not an IssueComment: callers cannot
// choose the issue, author, run association, or publication status. CommentID
// selects an own-comment correction; otherwise the session summary is upserted.
type RunIssueCommentWrite struct {
	CommentID         string
	ExpectedVersion   int64
	Body              string
	MentionedAgentIDs []string
	WorkItems         []nodeprotocol.WorkPlanItem `json:"work_items,omitempty"`
	AppendNew         bool
	IdempotencyKey    string `json:"-"`
}

type RunIssueCommentResult struct {
	CommentID string
	IssueID   string
	Version   int64
	EditedAt  *time.Time
	Replayed  bool
}

// WriteRunIssueComment checks the live Run and its entire ownership chain in
// the same SQLite transaction as the comment, revision, mentions and request
// receipt. A stop/terminal transition cannot slip between authorization and
// commit. Replaying an already committed request only reads its old receipt.
func (s *SQLite) WriteRunIssueComment(ctx context.Context, userID, computerID, runID string, input RunIssueCommentWrite) (RunIssueCommentResult, error) {
	input.Body = strings.TrimSpace(input.Body)
	if input.Body == "" {
		return RunIssueCommentResult{}, domain.Invalid("评论内容不能为空")
	}
	if len(input.Body) > 1<<20 {
		return RunIssueCommentResult{}, domain.Invalid("评论内容不能超过 1 MiB")
	}
	if input.CommentID != "" && (input.ExpectedVersion < 1 || input.AppendNew || len(input.MentionedAgentIDs) > 0 || len(input.WorkItems) > 0) {
		return RunIssueCommentResult{}, domain.Invalid("评论修正参数无效")
	}
	if len(input.WorkItems) > 0 && len(input.MentionedAgentIDs) > 0 {
		return RunIssueCommentResult{}, domain.Invalid("工作分工不能同时指定普通 @")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return RunIssueCommentResult{}, err
	}
	hash := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(hash[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunIssueCommentResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var scope RunScope
	var status, issueStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT computer.user_id, computer.id, issue.id, agent.id,
		       COALESCE(run.issue_agent_instance_id, ''), COALESCE(run.goal_id, ''),
		       run.purpose, COALESCE(goal.summary_comment_id, ''), run.status, issue.status
		FROM runs run
		JOIN issues issue ON issue.id = run.issue_id AND issue.project_id = run.project_id
		JOIN projects project ON project.id = issue.project_id
		JOIN computers computer ON computer.id = project.computer_id AND computer.id = run.computer_id
		JOIN users owner ON owner.id = computer.user_id
		JOIN agents agent ON agent.id = run.agent_id AND agent.computer_id = computer.id
		LEFT JOIN issue_agent_instances instance ON instance.id = run.issue_agent_instance_id
		LEFT JOIN issue_agent_goals goal ON goal.id = run.goal_id
		WHERE run.id = ? AND computer.user_id = ? AND computer.id = ?
		  AND run.deleted_at IS NULL AND issue.deleted_at IS NULL
		  AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
		  AND owner.deleted_at IS NULL AND agent.deleted_at IS NULL
		  AND computer.status <> 'revoked' AND run.assistant_session_id IS NULL
		  AND run.conversation_node_id IS NULL AND run.purpose <> 'goal_audit'
		  AND (run.issue_agent_instance_id IS NULL OR
		       (instance.issue_id = issue.id AND instance.agent_id = agent.id AND instance.deleted_at IS NULL))
	`, runID, userID, computerID).Scan(&scope.UserID, &scope.ComputerID, &scope.IssueID,
		&scope.AgentID, &scope.InstanceID, &scope.GoalID, &scope.RunPurpose, &scope.GoalCommentID, &status, &issueStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return RunIssueCommentResult{}, domain.NotFound("运行", runID)
	}
	if err != nil {
		return RunIssueCommentResult{}, err
	}
	result := RunIssueCommentResult{IssueID: scope.IssueID}
	if input.IdempotencyKey != "" {
		var previousFingerprint string
		var edited sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT fingerprint, comment_id, comment_version, edited_at
			FROM issue_tool_comment_requests WHERE run_id = ? AND idempotency_key = ?
		`, runID, input.IdempotencyKey).Scan(&previousFingerprint, &result.CommentID, &result.Version, &edited)
		if err == nil {
			if previousFingerprint != fingerprint {
				return RunIssueCommentResult{}, domain.Conflict("idempotency_conflict", "幂等键已用于其他工具请求", nil)
			}
			result.EditedAt, err = sqliteParseNullTime(edited)
			result.Replayed = true
			return result, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return RunIssueCommentResult{}, err
		}
	}
	switch status {
	case domain.RunQueued, domain.RunStarting, domain.RunRunning, domain.RunWaiting:
	default:
		return RunIssueCommentResult{}, domain.Conflict("run_not_active", "运行已停止，不接受评论或委派", nil)
	}
	// Closing an Issue does not cancel an already active Run or discard its
	// final summary, but it must fence new delegations in the same transaction.
	if len(input.MentionedAgentIDs) > 0 || len(input.WorkItems) > 0 {
		if err := requireSQLiteIssueOpen(issueStatus); err != nil {
			return RunIssueCommentResult{}, err
		}
	}
	if err := validateSQLiteCommentMentions(ctx, tx, scope.UserID, scope.ComputerID, scope.AgentID, input.MentionedAgentIDs); err != nil {
		return RunIssueCommentResult{}, err
	}
	if len(input.WorkItems) > 0 {
		if err := validateSQLiteWorkPlanTx(ctx, tx, scope.UserID, scope.IssueID, scope.AgentID, input.WorkItems); err != nil {
			return RunIssueCommentResult{}, err
		}
	}

	var comment domain.IssueComment
	if input.CommentID != "" {
		err = scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+`
			WHERE comments.id = ? AND comments.issue_id = ?
			  AND comments.author_kind = 'agent' AND comments.author_agent_id = ?
			  AND comments.deleted_at IS NULL
		`, input.CommentID, scope.IssueID, scope.AgentID), &comment)
		if errors.Is(err, sql.ErrNoRows) {
			return RunIssueCommentResult{}, domain.Forbidden("not_own_comment", "只能修正当前任务中自己发布的评论")
		}
		if err == nil && comment.Version != input.ExpectedVersion {
			return RunIssueCommentResult{}, versionConflict(comment.Version)
		}
	} else if !input.AppendNew || scope.GoalID != "" {
		// A Goal owns one progress/final summary across all of its work Runs.
		// append_new is only meaningful for ordinary sessions: honoring it here
		// would create working comments that Goal settlement never owns/finishes.
		comment, err = findSQLiteRunSessionComment(ctx, tx, scope, runID)
	}
	if err != nil {
		return RunIssueCommentResult{}, err
	}
	commentStatus := domain.IssueCommentPublished
	if scope.GoalID != "" && scope.RunPurpose != domain.RunPurposeGoalFinalize {
		commentStatus = domain.IssueCommentWorking
	}
	if input.CommentID != "" {
		commentStatus = comment.Status // A correction must not publish goal progress.
	}
	now := sqliteTimeValue(time.Now())
	if comment.ID == "" {
		comment.ID, comment.Version = newID("cmt"), 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO issue_comments (id, issue_id, author_kind, author_agent_id,
			    body, run_id, status, version, created_at, updated_at)
			VALUES (?, ?, 'agent', ?, ?, ?, ?, 1, ?, ?)
		`, comment.ID, scope.IssueID, scope.AgentID, input.Body, runID, commentStatus, now, now)
	} else {
		changed := comment.Body != input.Body
		if changed {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO issue_comment_revisions (id, comment_id, body, created_at) VALUES (?, ?, ?, ?)
			`, newID("cmr"), comment.ID, comment.Body, now); err != nil {
				return RunIssueCommentResult{}, err
			}
			comment.EditedAt, _ = sqliteParseNullTime(sql.NullString{String: now, Valid: true})
		}
		var updated sql.Result
		updated, err = tx.ExecContext(ctx, `
			UPDATE issue_comments SET body = ?, status = ?, version = version + 1,
			    edited_at = CASE WHEN ? THEN ? ELSE edited_at END, updated_at = ?
			WHERE id = ? AND version = ? AND deleted_at IS NULL
		`, input.Body, commentStatus, boolInt(changed), now, now, comment.ID, comment.Version)
		if err == nil {
			rows, rowsErr := updated.RowsAffected()
			if rowsErr != nil {
				return RunIssueCommentResult{}, rowsErr
			}
			if rows != 1 {
				return RunIssueCommentResult{}, versionConflict(comment.Version)
			}
		}
		comment.Version++
	}
	if err != nil {
		return RunIssueCommentResult{}, err
	}
	if err := insertSQLiteCommentMentions(ctx, tx, comment.ID, input.MentionedAgentIDs); err != nil {
		return RunIssueCommentResult{}, err
	}
	if len(input.WorkItems) > 0 {
		if err := attachSQLiteIssueWorkItemsTx(ctx, tx, scope.IssueID, comment.ID, input.WorkItems); err != nil {
			return RunIssueCommentResult{}, err
		}
	}
	result.CommentID, result.Version, result.EditedAt = comment.ID, comment.Version, comment.EditedAt
	if input.IdempotencyKey != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_tool_comment_requests
			    (run_id, idempotency_key, fingerprint, comment_id, comment_version, edited_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, runID, input.IdempotencyKey, fingerprint, comment.ID, comment.Version, nullableSQLiteTime(comment.EditedAt)); err != nil {
			return RunIssueCommentResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RunIssueCommentResult{}, err
	}
	return result, nil
}

// Exact joins, not a recent-N Run scan: an original summary still belongs to
// this instance after arbitrarily many subsequent Runs.
func findSQLiteRunSessionComment(ctx context.Context, tx *sql.Tx, scope RunScope, runID string) (domain.IssueComment, error) {
	var comment domain.IssueComment
	if scope.GoalID != "" {
		// The persisted Goal ID is authoritative, not a comment associated with
		// this continuation Run or an earlier ordinary Run in the same instance.
		// A missing summary is an error; never silently create a replacement.
		err := scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+`
			WHERE comments.id = ? AND comments.issue_id = ?
			  AND comments.author_kind = 'agent' AND comments.author_agent_id = ?
			  AND comments.deleted_at IS NULL
		`, scope.GoalCommentID, scope.IssueID, scope.AgentID), &comment)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.IssueComment{}, domain.Conflict("goal_summary_unavailable", "目标总结评论不可用", nil)
		}
		return comment, err
	}
	err := scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+`
		LEFT JOIN runs comment_run ON comment_run.id = comments.run_id
		WHERE comments.issue_id = ? AND comments.author_kind = 'agent'
		  AND comments.author_agent_id = ? AND comments.deleted_at IS NULL
		  AND (comments.run_id = ? OR (? <> '' AND comments.id = ?) OR
		       (? <> '' AND comment_run.issue_agent_instance_id = ? AND comment_run.issue_id = comments.issue_id))
		ORDER BY CASE WHEN comments.run_id = ? THEN 0 WHEN comments.id = ? THEN 1 ELSE 2 END,
		         comments.created_at DESC, comments.id DESC LIMIT 1
	`, scope.IssueID, scope.AgentID, runID, scope.GoalCommentID, scope.GoalCommentID,
		scope.InstanceID, scope.InstanceID, runID, scope.GoalCommentID), &comment)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueComment{}, nil
	}
	return comment, err
}

func validateSQLiteCommentMentions(ctx context.Context, tx *sql.Tx, userID, computerID, authorAgentID string, targets []string) error {
	seen := make(map[string]bool, len(targets))
	for _, agentID := range targets {
		if agentID == authorAgentID && authorAgentID != "" {
			return domain.Invalid("Agent 不能直接 @ 自己")
		}
		if seen[agentID] {
			return domain.Invalid("不能重复 @ 同一个 Agent")
		}
		seen[agentID] = true
		var valid int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM agents agent
			JOIN computers computer ON computer.id = agent.computer_id
			JOIN users owner ON owner.id = computer.user_id
			WHERE agent.id = ? AND agent.computer_id = ? AND computer.user_id = ?
			  AND agent.status = 'active' AND agent.deleted_at IS NULL
			  AND computer.deleted_at IS NULL AND owner.deleted_at IS NULL
		`, agentID, computerID, userID).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			return domain.Invalid("被 @ 的 Agent 不属于当前项目电脑")
		}
	}
	return nil
}

func insertSQLiteCommentMentions(ctx context.Context, tx *sql.Tx, commentID string, targets []string) error {
	for _, agentID := range targets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_comment_mentions (id, comment_id, target_kind, target_agent_id)
			VALUES (?, ?, 'agent', ?)
		`, newID("men"), commentID, agentID); err != nil {
			return err
		}
	}
	return nil
}
