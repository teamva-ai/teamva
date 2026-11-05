package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// A deliberately invalid calendar fences every profiled task, including once
// and cron, against silently falling back to legacy standalone execution when
// metadata is missing. No SQL baseline or existing rows need to change.
const sqliteScheduleProfileExpression = "@teamva:scheduled-task-config:v1"

type sqliteScheduleConfig struct {
	Version    int      `json:"version"`
	Kind       string   `json:"schedule_kind"`
	Expression string   `json:"schedule_expression,omitempty"`
	Times      []string `json:"times,omitempty"`
	Weekdays   []int    `json:"weekdays,omitempty"`
	IssueMode  string   `json:"issue_mode,omitempty"`
	IssueID    string   `json:"issue_id,omitempty"`
}

func encodeSQLiteScheduleConfig(task domain.ScheduledTask) (kind, expression, profile string, err error) {
	if task.IssueMode == "" && task.ScheduleKind != "daily" && task.ScheduleKind != "weekly" {
		return task.ScheduleKind, task.ScheduleExpression, "", nil
	}
	config := sqliteScheduleConfig{1, task.ScheduleKind, task.ScheduleExpression, task.Times, task.Weekdays, task.IssueMode, task.IssueID}
	value, err := json.Marshal(config)
	return "cron", sqliteScheduleProfileExpression, string(value), err
}

func hydrateSQLiteScheduleConfig(task *domain.ScheduledTask, profile string) error {
	if profile == "" {
		if task.ScheduleExpression == sqliteScheduleProfileExpression {
			return domain.Invalid("计划任务配置缺失，拒绝执行")
		}
		return nil
	}
	var config sqliteScheduleConfig
	if json.Unmarshal([]byte(profile), &config) != nil || config.Version != 1 || (config.IssueMode != "new" && config.IssueMode != "existing") || task.ScheduleExpression != sqliteScheduleProfileExpression {
		return domain.Invalid("计划任务配置损坏，拒绝执行")
	}
	task.ScheduleKind, task.ScheduleExpression = config.Kind, config.Expression
	task.Times, task.Weekdays, task.IssueMode, task.IssueID = config.Times, config.Weekdays, config.IssueMode, config.IssueID
	if err := domain.NormalizeScheduledTaskConfig(task); err != nil {
		return err
	}
	return validateSQLiteScheduleCalendar(*task)
}

func saveSQLiteScheduleConfig(ctx context.Context, tx *sql.Tx, id, profile string) error {
	if profile == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO app_metadata(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		"scheduled_task_config:"+id, profile, sqliteTimeValue(time.Now()))
	return err
}

func validateSQLiteScheduleProfile(ctx context.Context, tx *sql.Tx, userID string, task domain.ScheduledTask) error {
	if strings.TrimSpace(task.Name) == "" {
		return domain.Invalid("计划任务名称不能为空")
	}
	if task.IssueMode == "" {
		return nil
	} // Explicit compatibility for old repository callers.
	if strings.TrimSpace(task.AgentID) == "" {
		return domain.Invalid("计划任务必须选择 Agent")
	}
	var body strings.Builder
	for _, block := range task.Prompt {
		body.WriteString(block.Text)
	}
	if strings.TrimSpace(body.String()) == "" {
		return domain.Invalid("计划任务提示词不能为空")
	}
	if task.IssueMode == "existing" && task.Enabled {
		return requireSQLiteScheduleIssue(ctx, tx, task.ProjectID, task.IssueID)
	}
	return nil
}

// Paused instances represent the existing issue/session emergency stop. Never
// create a new session to bypass that stop in an existing target issue.
func requireSQLiteScheduleIssue(ctx context.Context, tx *sql.Tx, projectID, issueID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM issues WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, issueID, projectID).Scan(&status)
	if err == sql.ErrNoRows {
		return domain.NotFound("本项目任务", issueID)
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return domain.Invalid("计划任务目标 Issue 已结束")
	}
	var paused int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM issue_agent_instances WHERE issue_id = ? AND status = 'paused' AND deleted_at IS NULL`, issueID).Scan(&paused); err != nil {
		return err
	}
	if paused > 0 {
		return domain.Invalid("计划任务目标 Issue 存在已暂停的会话")
	}
	return nil
}

func createSQLiteScheduledIssueRunTx(ctx context.Context, tx *sql.Tx, task domain.ScheduledTask, triggerKey string) (domain.Run, error) {
	var userID string
	if err := tx.QueryRowContext(ctx, `SELECT computer.user_id FROM projects project
		JOIN computers computer ON computer.id = project.computer_id
		JOIN users owner ON owner.id = computer.user_id
		WHERE project.id = ? AND project.deleted_at IS NULL AND computer.deleted_at IS NULL AND owner.deleted_at IS NULL`, task.ProjectID).Scan(&userID); err == sql.ErrNoRows {
		return domain.Run{}, domain.NotFound("项目", task.ProjectID)
	} else if err != nil {
		return domain.Run{}, err
	}
	if err := requireSQLiteScheduleAgent(ctx, tx, userID, task.ProjectID, task.AgentID); err != nil {
		return domain.Run{}, err
	}
	if err := validateSQLiteScheduleProfile(ctx, tx, userID, task); err != nil {
		return domain.Run{}, err
	}
	issueID := task.IssueID
	var body strings.Builder
	for _, block := range task.Prompt {
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString(block.Text)
	}
	now := sqliteTimeValue(time.Now())
	if task.IssueMode == "new" {
		issueID = newID("iss")
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, project_id, title, description, status, created_by_user_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'open', ?, ?, ?)`, issueID, task.ProjectID, "新任务", body.String(), userID, now, now); err != nil {
			return domain.Run{}, err
		}
		if err := initAutoTitleTx(ctx, tx, userID, "issue", issueID); err != nil {
			return domain.Run{}, err
		}
	} else if task.IssueMode != "existing" {
		return domain.Run{}, domain.Invalid("无效 Issue 模式")
	}
	// Persist the user's real scheduled instruction, never an invented Agent reply.
	commentID, mentionID := newID("cmt"), newID("men")
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_comments(id, issue_id, author_kind, author_user_id, body, status, created_at, updated_at)
		VALUES (?, ?, 'user', ?, ?, 'published', ?, ?)`, commentID, issueID, userID, body.String(), now, now); err != nil {
		return domain.Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_comment_mentions(id, comment_id, target_kind, target_agent_id)
		VALUES (?, ?, 'agent', ?)`, mentionID, commentID, task.AgentID); err != nil {
		return domain.Run{}, err
	}
	if seed := sqliteScheduleTitleSeed(task.Prompt); seed != "" {
		if err := queueAutoTitleTx(ctx, tx, userID, "issue", issueID, seed); err != nil {
			return domain.Run{}, err
		}
	}
	input, err := sqliteScheduledMentionInput(ctx, tx, issueID, commentID, body.String())
	if err != nil {
		return domain.Run{}, err
	}
	return createSQLiteRunForMentionTx(ctx, tx, userID, issueID, task.AgentID, mentionID, "", input, "schedule:"+task.ID+":"+triggerKey)
}

// Use the user's saved prompt, never the generated Issue/run context. Internal
// is a public per-block display flag, not evidence that the user text is fake.
func sqliteScheduleTitleSeed(prompt []domain.ContentBlock) string {
	var seed strings.Builder
	for _, block := range prompt {
		if block.Type != "text" {
			continue
		}
		if seed.Len() > 0 {
			seed.WriteString("\n\n")
		}
		seed.WriteString(block.Text)
	}
	return seed.String()
}

func sqliteScheduledMentionInput(ctx context.Context, tx *sql.Tx, issueID, commentID, instruction string) ([]domain.ContentBlock, error) {
	var title, description string
	if err := tx.QueryRowContext(ctx, `SELECT title, description FROM issues WHERE id = ?`, issueID).Scan(&title, &description); err != nil {
		return nil, err
	}
	var prompt strings.Builder
	prompt.WriteString("# 任务（Issue）上下文\n\n## 标题\n" + title + "\n\n## 描述\n" + description + "\n\n## 评论历史\n")
	rows, err := tx.QueryContext(ctx, `SELECT id, body FROM (
		SELECT id, substr(body, 1, 8192) body, created_at FROM issue_comments
		WHERE issue_id = ? AND deleted_at IS NULL AND status = 'published' AND id <> ?
		ORDER BY created_at DESC, id DESC LIMIT 16) ORDER BY created_at, id`, issueID, commentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return nil, err
		}
		prompt.WriteString("\n[comment_id=" + id + "] " + text)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	prompt.WriteString("\n\n（历史采用有界最近窗口；使用 issue_get 的 comment_id 参数读取全文。）\n\n## 触发本次委派的评论\ncomment_id: " + commentID + "\n" + instruction)
	prompt.WriteString("\n\n本轮结束前必须使用 issue_comment_create 工具向本任务发送本轮结果；向其他 Agent 委派时，委派说明即为本轮结果。每个 Agent 会话只维护一条总结评论；只有用户明确要求再发一条时才使用 append_new=true。")
	prompt.WriteString(IssueAsyncDelegationPrompt)
	return []domain.ContentBlock{{Type: "text", Text: prompt.String()}}, nil
}
