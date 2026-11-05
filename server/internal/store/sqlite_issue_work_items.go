package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const issueWorkPrefix = "issue_work:v1:"
const issueWorkPendingPrefix = "issue_work_pending:v1:"

const sqliteIssueWorkMarkersQuery = `SELECT count(*) FROM issue_comment_mentions WHERE comment_id=? AND target_kind='agent' AND id GLOB 'wmen_*'`

type sqliteIssueWorkBatch struct {
	Schema    int                    `json:"schema"`
	IssueID   string                 `json:"issue_id"`
	CommentID string                 `json:"comment_id"`
	Cancelled bool                   `json:"cancelled,omitempty"`
	Items     []domain.IssueWorkItem `json:"items"`
}

func issueWorkKey(issueID, commentID string) string {
	return issueWorkPrefix + issueID + ":" + commentID
}

func readSQLiteIssueWorkBatch(ctx context.Context, q sqliteQueryer, issueID, commentID string) (sqliteIssueWorkBatch, bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key=?`, issueWorkKey(issueID, commentID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sqliteIssueWorkBatch{}, false, nil
	}
	if err != nil {
		return sqliteIssueWorkBatch{}, false, err
	}
	var batch sqliteIssueWorkBatch
	if json.Unmarshal([]byte(raw), &batch) != nil || batch.Schema != 1 || batch.IssueID != issueID || batch.CommentID != commentID {
		return batch, false, domain.Invalid("工作分工元数据损坏，拒绝执行")
	}
	items := make([]nodeprotocol.WorkPlanItem, len(batch.Items))
	mentions := make(map[string]bool, len(batch.Items))
	for i, item := range batch.Items {
		// Persisted batches always have explicit dependencies and a mention for roots.
		if item.After == nil || (len(item.After) == 0 && item.MentionID == "") {
			return batch, false, domain.Invalid("工作分工依赖或执行关联缺失，拒绝执行")
		}
		items[i] = item.WorkPlanItem
		if item.MentionID != "" && (!strings.HasPrefix(item.MentionID, "wmen_") || mentions[item.MentionID]) || item.RunID != "" && (item.MentionID == "" || item.IssueAgentInstanceID == "") {
			return batch, false, domain.Invalid("工作分工执行关联损坏，拒绝执行")
		}
		mentions[item.MentionID] = true
	}
	if err := nodeprotocol.ValidateWorkPlanItems(items, nil); err != nil {
		return batch, false, domain.Invalid("工作分工元数据损坏，拒绝执行")
	}
	return batch, true, nil
}

func saveSQLiteIssueWorkBatchTx(ctx context.Context, tx *sql.Tx, batch sqliteIssueWorkBatch) error {
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_metadata(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		issueWorkKey(batch.IssueID, batch.CommentID), string(raw), sqliteTimeValue(time.Now()))
	if err != nil {
		return err
	}
	pendingKey := issueWorkPendingPrefix + batch.IssueID + ":" + batch.CommentID
	if !batch.Cancelled && slices.ContainsFunc(batch.Items, func(item domain.IssueWorkItem) bool { return item.MentionID == "" }) {
		_, err = tx.ExecContext(ctx, `INSERT INTO app_metadata(key,value) VALUES(?,'{}') ON CONFLICT(key) DO NOTHING`, pendingKey)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM app_metadata WHERE key=?`, pendingKey)
	}
	return err
}

// Check once, read-only at startup; never silently rebuild a lost scheduling index.
func checkSQLiteIssueWorkIndex(ctx context.Context, db *sql.DB) error {
	var invalid bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM app_metadata batch WHERE batch.key GLOB 'issue_work:v1:*'
		AND (COALESCE(json_extract(batch.value,'$.cancelled'),0)=0 AND EXISTS(
			SELECT 1 FROM json_each(batch.value,'$.items') item WHERE COALESCE(json_extract(item.value,'$.mention_id'),'')=''))
		!= EXISTS(SELECT 1 FROM app_metadata pending WHERE pending.key='issue_work_pending:v1:'||substr(batch.key,?))
	) OR EXISTS(
		SELECT 1 FROM app_metadata pending WHERE pending.key GLOB 'issue_work_pending:v1:*'
		AND NOT EXISTS(SELECT 1 FROM app_metadata batch WHERE batch.key='issue_work:v1:'||substr(pending.key,?))
	)`, len(issueWorkPrefix)+1, len(issueWorkPendingPrefix)+1).Scan(&invalid)
	if err != nil {
		return fmt.Errorf("verify work scheduling index: %w", err)
	}
	if invalid {
		return errors.New("工作分工调度索引缺失或不一致；保留数据库并拒绝启动")
	}
	return nil
}

func validateSQLiteWorkPlanTx(ctx context.Context, tx *sql.Tx, userID, issueID, authorAgentID string, items []nodeprotocol.WorkPlanItem) error {
	rows, err := tx.QueryContext(ctx, `SELECT agent.id,agent.name FROM agents agent
		JOIN issues issue ON issue.id=? JOIN projects project ON project.id=issue.project_id
		JOIN computers computer ON computer.id=project.computer_id JOIN users owner ON owner.id=computer.user_id
		JOIN agent_revisions revision ON revision.id=agent.current_revision_id AND revision.agent_id=agent.id
		WHERE computer.user_id=? AND agent.computer_id=computer.id AND agent.status='active'
		AND agent.deleted_at IS NULL AND revision.deleted_at IS NULL AND issue.deleted_at IS NULL
		AND project.deleted_at IS NULL AND computer.deleted_at IS NULL AND owner.deleted_at IS NULL`, issueID, userID)
	if err != nil {
		return err
	}
	agents := []nodeprotocol.WorkPlanAgent{}
	for rows.Next() {
		var agent nodeprotocol.WorkPlanAgent
		if err := rows.Scan(&agent.ID, &agent.Name); err != nil {
			_ = rows.Close()
			return err
		}
		agents = append(agents, agent)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if err := nodeprotocol.ValidateWorkPlanItems(items, agents); err != nil {
		return domain.Invalid("工作分工无效：" + err.Error())
	}
	for _, item := range items {
		if authorAgentID != "" && item.AgentID == authorAgentID {
			return domain.Invalid("Agent 不能直接 @ 自己")
		}
	}
	return nil
}

// A batch is confirmed once. Editing its discussion/summary never rewrites tasks.
func attachSQLiteIssueWorkItemsTx(ctx context.Context, tx *sql.Tx, issueID, commentID string, items []nodeprotocol.WorkPlanItem) error {
	batch, exists, err := readSQLiteIssueWorkBatch(ctx, tx, issueID, commentID)
	if err != nil {
		return err
	}
	if exists {
		if len(items) != len(batch.Items) {
			return domain.Conflict("work_plan_immutable", "已确认的工作分工不能修改", nil)
		}
		for i, item := range items {
			prior := batch.Items[i].WorkPlanItem
			if item.Key != prior.Key || item.AgentID != prior.AgentID || item.Title != prior.Title || item.Instruction != prior.Instruction || !slices.Equal(item.After, prior.After) {
				return domain.Conflict("work_plan_immutable", "已确认的工作分工不能修改", nil)
			}
		}
		return nil
	}
	var markers int
	if err := tx.QueryRowContext(ctx, sqliteIssueWorkMarkersQuery, commentID).Scan(&markers); err != nil {
		return err
	}
	if markers != 0 {
		return domain.Invalid("工作分工元数据缺失，不能重新创建")
	}
	batch = sqliteIssueWorkBatch{Schema: 1, IssueID: issueID, CommentID: commentID}
	for _, item := range items {
		if item.After == nil {
			item.After = []string{}
		}
		work := domain.IssueWorkItem{WorkPlanItem: item, Status: "waiting_dependencies"}
		if len(item.After) == 0 {
			work.MentionID = newID("wmen")
			work.Status = "ready"
			if err := insertSQLiteWorkMentionTx(ctx, tx, commentID, work); err != nil {
				return err
			}
		}
		batch.Items = append(batch.Items, work)
	}
	return saveSQLiteIssueWorkBatchTx(ctx, tx, batch)
}

func insertSQLiteWorkMentionTx(ctx context.Context, tx *sql.Tx, commentID string, item domain.IssueWorkItem) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO issue_comment_mentions(id,comment_id,target_kind,target_agent_id) VALUES(?,?,'agent',?)`, item.MentionID, commentID, item.AgentID)
	return err
}

func (s *SQLite) CreateIssueWorkComment(ctx context.Context, userID string, input domain.IssueComment, items []nodeprotocol.WorkPlanItem, idempotencyKey string) (domain.IssueComment, error) {
	input.Body = strings.TrimSpace(input.Body)
	if input.AuthorKind != domain.IssueCommentAuthorUser || input.AuthorAgentID != "" || input.RunID != "" || input.Body == "" || len(input.Body) > 1<<20 || strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 {
		return domain.IssueComment{}, domain.Invalid("工作分工评论或幂等键无效")
	}
	raw, err := json.Marshal(struct {
		Body  string
		Items []nodeprotocol.WorkPlanItem
	}{input.Body, items})
	if err != nil {
		return domain.IssueComment{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	key := fmt.Sprintf("issue_work_request:v1:%s:%x", input.IssueID, sha256.Sum256([]byte(idempotencyKey)))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IssueComment{}, err
	}
	defer tx.Rollback()
	// First statement takes the writer lock across independent SQLite handles.
	result, err := tx.ExecContext(ctx, `INSERT INTO app_metadata(key,value) VALUES(?,'{}') ON CONFLICT(key) DO NOTHING`, key)
	if err != nil {
		return domain.IssueComment{}, err
	}
	if err := requireSQLiteIssueOwner(ctx, tx, userID, input.IssueID); err != nil {
		return domain.IssueComment{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return domain.IssueComment{}, err
	}
	if inserted == 0 {
		var prior struct{ Fingerprint, CommentID string }
		var receipt string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key=?`, key).Scan(&receipt); err != nil {
			return domain.IssueComment{}, err
		}
		if json.Unmarshal([]byte(receipt), &prior) != nil || prior.CommentID == "" || prior.Fingerprint == "" {
			return domain.IssueComment{}, domain.Invalid("工作分工幂等回执损坏")
		}
		if prior.Fingerprint != fingerprint {
			return domain.IssueComment{}, domain.Conflict("idempotency_conflict", "幂等键已用于其他工作分工", nil)
		}
		var comment domain.IssueComment
		if err := scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+` WHERE comments.id=? AND comments.issue_id=? AND comments.deleted_at IS NULL`, prior.CommentID, input.IssueID), &comment); errors.Is(err, sql.ErrNoRows) {
			return domain.IssueComment{}, domain.NotFound("评论", prior.CommentID)
		} else if err != nil {
			return domain.IssueComment{}, err
		}
		if err := hydrateSQLiteIssueWorkItems(ctx, tx, &comment); err != nil {
			return domain.IssueComment{}, err
		}
		return comment, nil
	}
	if err := validateSQLiteWorkPlanTx(ctx, tx, userID, input.IssueID, "", items); err != nil {
		return domain.IssueComment{}, err
	}
	var issueStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM issues WHERE id=?`, input.IssueID).Scan(&issueStatus); err != nil {
		return domain.IssueComment{}, err
	}
	if err := requireSQLiteIssueOpen(issueStatus); err != nil {
		return domain.IssueComment{}, err
	}
	input.Status = domain.IssueCommentPublished
	comment, err := createSQLiteIssueCommentTx(ctx, tx, userID, input, nil)
	if err != nil {
		return domain.IssueComment{}, err
	}
	if err := attachSQLiteIssueWorkItemsTx(ctx, tx, comment.IssueID, comment.ID, items); err != nil {
		return domain.IssueComment{}, err
	}
	receipt, _ := json.Marshal(struct{ Fingerprint, CommentID string }{fingerprint, comment.ID})
	if _, err := tx.ExecContext(ctx, `UPDATE app_metadata SET value=? WHERE key=?`, string(receipt), key); err != nil {
		return domain.IssueComment{}, err
	}
	if err := hydrateSQLiteIssueWorkItems(ctx, tx, &comment); err != nil {
		return domain.IssueComment{}, err
	}
	return comment, tx.Commit()
}

func hydrateSQLiteIssueWorkItems(ctx context.Context, q sqliteQueryer, comment *domain.IssueComment) error {
	batch, exists, err := readSQLiteIssueWorkBatch(ctx, q, comment.IssueID, comment.ID)
	if err != nil {
		return err
	}
	if !exists {
		var markers int
		if err := q.QueryRowContext(ctx, sqliteIssueWorkMarkersQuery, comment.ID).Scan(&markers); err != nil {
			return err
		}
		if markers != 0 {
			return domain.Invalid("工作分工元数据缺失，拒绝执行")
		}
		return nil
	}
	comment.WorkItems, err = projectSQLiteIssueWorkItems(ctx, q, batch)
	return err
}

func projectSQLiteIssueWorkItems(ctx context.Context, q sqliteQueryer, batch sqliteIssueWorkBatch) ([]domain.IssueWorkItem, error) {
	items := append([]domain.IssueWorkItem(nil), batch.Items...)
	var issueStatus string
	var paused int
	if err := q.QueryRowContext(ctx, `SELECT status,(SELECT count(*) FROM issue_agent_instances WHERE issue_id=issues.id AND status='paused' AND deleted_at IS NULL) FROM issues WHERE id=?`, batch.IssueID).Scan(&issueStatus, &paused); err != nil {
		return nil, err
	}
	byKey := make(map[string]int, len(items))
	for i := range items {
		item := &items[i]
		byKey[item.Key] = i
		item.ResultCommentID, item.StatusReason, item.BlockedBy = "", "", nil
		item.Status = "ready"
		if item.MentionID != "" {
			var status string
			if err := q.QueryRowContext(ctx, `SELECT status FROM issue_comment_mentions WHERE id=? AND comment_id=? AND target_agent_id=?`, item.MentionID, batch.CommentID, item.AgentID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
				return nil, domain.Invalid("工作分工 mention 关联缺失，拒绝执行")
			} else if err != nil {
				return nil, err
			}
			if status == "completed" && item.RunID == "" {
				return nil, domain.Invalid("工作分工已消费但缺少 Run 关联，拒绝执行")
			}
			if status == "dead_letter" {
				item.Status, item.StatusReason = "cancelled", "queue_cleared"
			}
		}
		if item.RunID != "" {
			if err := q.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=? AND issue_id=? AND agent_id=? AND issue_agent_instance_id=? AND deleted_at IS NULL`, item.RunID, batch.IssueID, item.AgentID, item.IssueAgentInstanceID).Scan(&item.Status); errors.Is(err, sql.ErrNoRows) {
				return nil, domain.Invalid("工作分工 Run 关联缺失，拒绝执行")
			} else if err != nil {
				return nil, err
			}
			err := q.QueryRowContext(ctx, `SELECT id FROM issue_comments WHERE run_id=? AND issue_id=? AND author_kind='agent' AND author_agent_id=? AND status='published' AND deleted_at IS NULL ORDER BY created_at DESC,id DESC LIMIT 1`, item.RunID, batch.IssueID, item.AgentID).Scan(&item.ResultCommentID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if item.Status == domain.RunSucceeded && item.ResultCommentID == "" {
				item.Status, item.StatusReason = "waiting_result", "result_not_published"
			}
		} else if batch.Cancelled {
			item.Status, item.StatusReason = "cancelled", "queue_cleared"
		}
	}
	visited := make(map[string]bool, len(items))
	var dependencies func(int)
	dependencies = func(index int) {
		item := &items[index]
		if visited[item.Key] || item.RunID != "" || item.Status == "cancelled" {
			return
		}
		visited[item.Key] = true
		blocked := false
		for _, key := range item.After {
			dep := byKey[key]
			dependencies(dep)
			if items[dep].Status != domain.RunSucceeded || items[dep].ResultCommentID == "" {
				item.BlockedBy = append(item.BlockedBy, key)
				item.Status = "waiting_dependencies"
				if domain.IsRunTerminal(items[dep].Status) || items[dep].Status == domain.RunCancelling || items[dep].Status == "blocked" {
					blocked = true
				}
			}
		}
		if blocked {
			item.Status, item.StatusReason = "blocked", "dependency_failed"
		}
		if issueStatus != domain.IssueOpen {
			item.StatusReason = "issue_not_open"
		} else if paused > 0 {
			item.StatusReason = "issue_paused"
		}
	}
	for i := range items {
		dependencies(i)
	}
	return items, nil
}

// This runs inside mention consumption, so readiness, fresh instance, Run,
// working reply, durable outbox and the work-item binding commit together.
func prepareSQLiteWorkMentionTx(ctx context.Context, tx *sql.Tx, issueID, commentID, mentionID string, input []domain.ContentBlock) (*sqliteIssueWorkBatch, int, []domain.ContentBlock, error) {
	if !strings.HasPrefix(mentionID, "wmen_") {
		return nil, 0, input, nil
	}
	batch, exists, err := readSQLiteIssueWorkBatch(ctx, tx, issueID, commentID)
	if err != nil {
		return nil, 0, nil, err
	}
	if !exists {
		return nil, 0, nil, domain.Invalid("工作分工元数据缺失，拒绝执行")
	}
	projected, err := projectSQLiteIssueWorkItems(ctx, tx, batch)
	if err != nil {
		return nil, 0, nil, err
	}
	for i, item := range projected {
		if item.MentionID != mentionID {
			continue
		}
		if item.RunID != "" || item.Status == "cancelled" || batch.Cancelled {
			return nil, 0, nil, domain.Conflict("work_item_cancelled", "工作分工已取消或执行", nil)
		}
		if item.Status != "ready" || item.StatusReason != "" {
			return nil, 0, nil, domain.Conflict("work_item_blocked", "工作分工依赖未完成，或任务已暂停/结束", nil)
		}
		var instruction strings.Builder
		instruction.WriteString("\n\n## 本次已确认的工作分工（只执行本项）\nkey: " + item.Key + "\n标题：" + item.Title + "\n具体指令：\n" + item.Instruction)
		for _, key := range item.After {
			for _, dep := range projected {
				if dep.Key != key {
					continue
				}
				var body string
				// shortcut: cap each upstream excerpt at 16K runes; issue_get retrieves full results.
				if err := tx.QueryRowContext(ctx, `SELECT substr(body,1,16384) FROM issue_comments WHERE id=? AND status='published' AND deleted_at IS NULL`, dep.ResultCommentID).Scan(&body); err != nil {
					return nil, 0, nil, err
				}
				instruction.WriteString("\n\n### 上游 " + key + " 的已发布结果\ncomment_id: " + dep.ResultCommentID + "\n" + body)
			}
		}
		instruction.WriteString("\n\n上游结果采用有界摘录；需要全文时用 issue_get 的 comment_id 精确读取。分工指令不随讨论正文编辑改变。")
		input = append(append([]domain.ContentBlock(nil), input...), domain.ContentBlock{Type: "text", Text: instruction.String(), Internal: true})
		return &batch, i, input, nil
	}
	return nil, 0, nil, domain.Invalid("工作分工 mention 不属于已确认任务，拒绝执行")
}

func (s *SQLite) AdvanceIssueWorkItems(ctx context.Context, _ time.Time) (int, error) {
	// The transactionally maintained key index avoids parsing completed history on every tick.
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM app_metadata WHERE key GLOB 'issue_work_pending:v1:*' ORDER BY key`)
	if err != nil {
		return 0, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		keys = append(keys, issueWorkPrefix+strings.TrimPrefix(key, issueWorkPendingPrefix))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	advanced := 0
	var failures []error
	for _, key := range keys {
		count, err := s.advanceSQLiteIssueWorkBatch(ctx, key)
		advanced += count
		if err != nil {
			failures = append(failures, errors.New("工作分工推进失败，保持原队列不变"))
		}
	}
	return advanced, errors.Join(failures...)
}

func (s *SQLite) advanceSQLiteIssueWorkBatch(ctx context.Context, key string) (int, error) {
	parts := strings.SplitN(strings.TrimPrefix(key, issueWorkPrefix), ":", 2)
	if len(parts) != 2 {
		return 0, domain.Invalid("工作分工元数据键无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE app_metadata SET updated_at=updated_at WHERE key=?`, key); err != nil {
		return 0, err
	}
	batch, exists, err := readSQLiteIssueWorkBatch(ctx, tx, parts[0], parts[1])
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, domain.Invalid("工作分工元数据缺失，拒绝执行")
	}
	if batch.Cancelled {
		return 0, nil
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM issue_comments comment
		JOIN issues issue ON issue.id=comment.issue_id JOIN projects project ON project.id=issue.project_id
		JOIN computers computer ON computer.id=project.computer_id JOIN users owner ON owner.id=computer.user_id
		WHERE comment.id=? AND issue.id=? AND comment.deleted_at IS NULL AND issue.deleted_at IS NULL
		AND issue.status='open' AND project.deleted_at IS NULL AND computer.deleted_at IS NULL AND owner.deleted_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM issue_agent_instances WHERE issue_id=issue.id AND status='paused' AND deleted_at IS NULL)`, batch.CommentID, batch.IssueID).Scan(&live); err != nil || live == 0 {
		return 0, err
	}
	items, err := projectSQLiteIssueWorkItems(ctx, tx, batch)
	if err != nil {
		return 0, err
	}
	count := 0
	for i, item := range items {
		if item.MentionID != "" || item.Status != "ready" {
			continue
		}
		batch.Items[i].MentionID = newID("wmen")
		if err := insertSQLiteWorkMentionTx(ctx, tx, batch.CommentID, batch.Items[i]); err != nil {
			return 0, err
		}
		count++
	}
	if count > 0 {
		if err := saveSQLiteIssueWorkBatchTx(ctx, tx, batch); err != nil {
			return 0, err
		}
	}
	return count, tx.Commit()
}

func cancelSQLiteCommentWorkItemsTx(ctx context.Context, tx *sql.Tx, commentID string, stopActive bool) error {
	var issueID string
	if err := tx.QueryRowContext(ctx, `SELECT issue_id FROM issue_comments WHERE id=?`, commentID).Scan(&issueID); err != nil {
		return err
	}
	batch, exists, err := readSQLiteIssueWorkBatch(ctx, tx, issueID, commentID)
	if err != nil || !exists {
		return err
	}
	batch.Cancelled = true
	for _, item := range batch.Items {
		if stopActive && item.RunID != "" {
			if err := requestSQLiteGoalRunStopTx(ctx, tx, item.RunID, "graceful", time.Now().UTC()); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_comment_mentions SET status='dead_letter',last_error='queue_cleared',claimed_at=NULL,lease_until=NULL,claim_token=NULL
		WHERE comment_id=? AND target_kind='agent' AND id GLOB 'wmen_*' AND status IN ('pending','processing')`, commentID); err != nil {
		return err
	}
	return saveSQLiteIssueWorkBatchTx(ctx, tx, batch)
}

// Clearing waiting work also cancels dependency-only items without mentions;
// merely deleting pending mention rows would resurrect them on the next tick.
func (s *SQLite) ClearIssueWorkItems(ctx context.Context, userID, issueID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE app_metadata SET updated_at=updated_at WHERE key=?`, issueWorkKey(issueID, "")); err != nil {
		return err
	}
	if err := clearSQLiteIssueWorkItemsTx(ctx, tx, userID, issueID); err != nil {
		return err
	}
	return tx.Commit()
}

func clearSQLiteIssueWorkItemsTx(ctx context.Context, tx *sql.Tx, userID, issueID string) error {
	if err := requireSQLiteIssueOwner(ctx, tx, userID, issueID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM issue_comments WHERE issue_id=? AND deleted_at IS NULL`, issueID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		if err := cancelSQLiteCommentWorkItemsTx(ctx, tx, id, false); err != nil {
			return err
		}
	}
	return nil
}
