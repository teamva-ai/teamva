package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const conversationGoalSelect = `SELECT cg.id, cg.version, cg.created_at, cg.updated_at,
 cg.node_id, cg.agent_revision_id, cg.objective, cg.status, cg.phase, cg.token_budget,
 cg.tokens_used, cg.work_runs, cg.run_count, cg.max_work_runs,
 COALESCE(cg.current_run_id,''), COALESCE(cg.last_work_run_id,''), cg.note, cg.status_reason
 FROM conversation_goals cg `

func scanConversationGoal(row scanner, goal *domain.ConversationGoal) error {
	var created, updated string
	var budget sql.NullInt64
	if err := row.Scan(&goal.ID, &goal.Version, &created, &updated, &goal.NodeID,
		&goal.AgentRevisionID, &goal.Objective, &goal.Status, &goal.Phase, &budget,
		&goal.TokensUsed, &goal.WorkRuns, &goal.RunCount, &goal.MaxWorkRuns,
		&goal.CurrentRunID, &goal.LastWorkRunID, &goal.Note, &goal.StatusReason); err != nil {
		return err
	}
	if budget.Valid {
		goal.TokenBudget = &budget.Int64
	}
	return parseSQLiteVersioned(&goal.Versioned, created, updated, sql.NullString{})
}

func conversationGoalNodeTx(ctx context.Context, tx *sql.Tx, userID, nodeID string) (domain.ConversationNode, error) {
	var node domain.ConversationNode
	err := scanConversationNode(tx.QueryRowContext(ctx,
		conversationNodeSelect+conversationNodeOwnership+` AND n.id = ?`, userID, nodeID), &node)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.NotFound("对话节点", nodeID)
	}
	return node, err
}

// An unfinished goal reserves its node even between work and audit runs. This
// guard is also used by ordinary sends, branching and subtree deletion.
func requireNoConversationGoalTx(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM conversation_goals WHERE node_id = ?
	 AND status IN ('active','paused','blocked','budget_reached') LIMIT 1`, nodeID).Scan(&id)
	if err == nil {
		return domain.Conflict("goal_already_exists", "请先完成或取消当前节点目标", map[string]any{"goal_id": id})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *SQLite) GetCurrentConversationGoal(ctx context.Context, userID, nodeID string) (*domain.ConversationGoal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := conversationGoalNodeTx(ctx, tx, userID, nodeID); err != nil {
		return nil, err
	}
	var goal domain.ConversationGoal
	err = scanConversationGoal(tx.QueryRowContext(ctx, conversationGoalSelect+`
	 WHERE cg.node_id = ? ORDER BY cg.created_at DESC, cg.id DESC LIMIT 1`, nodeID), &goal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &goal, err
}

func (s *SQLite) CreateConversationGoal(ctx context.Context, userID, nodeID, key, objective string, budget *int64, bindings ...domain.ConversationAgentBinding) (domain.ConversationGoal, bool, error) {
	binding := conversationRequestBinding(bindings)
	objective, err := validateGoalInput(objective, budget)
	if err != nil {
		return domain.ConversationGoal{}, false, err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return domain.ConversationGoal{}, false, domain.Invalid("需要有效的幂等键")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationGoal{}, false, err
	}
	defer tx.Rollback()
	node, err := conversationGoalNodeTx(ctx, tx, userID, nodeID)
	if err != nil {
		return domain.ConversationGoal{}, false, err
	}
	var existing domain.ConversationGoal
	err = scanConversationGoal(tx.QueryRowContext(ctx, conversationGoalSelect+`
	 WHERE cg.node_id = ? AND cg.idempotency_key = ?`, nodeID, key), &existing)
	if err == nil {
		if existing.Objective != objective || !sameConversationGoalBudget(existing.TokenBudget, budget) {
			return existing, false, domain.Conflict("idempotency_conflict", "幂等键已用于其他目标", nil)
		}
		var firstRunID string
		if err := tx.QueryRowContext(ctx, `SELECT run_id FROM conversation_goal_runs WHERE goal_id=? AND ordinal=1`, existing.ID).Scan(&firstRunID); err != nil {
			return existing, false, err
		}
		if err := requireConversationRequestBindingTx(ctx, tx, firstRunID, binding); err != nil {
			return existing, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, false, err
	}
	if err := validateConversationAgentBinding(node, binding); err != nil {
		return existing, false, err
	}
	if node.Locked {
		return existing, false, domain.Invalid("已分支的节点不能启动目标")
	}
	if err := requireIdleConversationNodes(ctx, tx, []string{nodeID}); err != nil {
		return existing, false, err
	}
	now := time.Now().UTC()
	goal := domain.ConversationGoal{
		Versioned: domain.Versioned{ID: newID("cgol"), Version: 1, CreatedAt: now, UpdatedAt: now},
		NodeID:    nodeID, AgentRevisionID: node.AgentRevisionID, Objective: objective,
		Status: domain.GoalActive, Phase: domain.GoalPhaseRunning, TokenBudget: budget,
		MaxWorkRuns: domain.GoalDefaultMaxContinuations + 1,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_goals
	 (id,node_id,agent_revision_id,objective,idempotency_key,status,phase,token_budget,max_work_runs,created_at,updated_at)
	 VALUES (?,?,?,?,?,'active','running',?,?,?,?)`, goal.ID, nodeID, node.AgentRevisionID,
		objective, key, nullableGoalBudget(budget), goal.MaxWorkRuns, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
		return existing, false, err
	}
	if err := queueConversationGoalRunTx(ctx, tx, userID, node, &goal, false, true, now); err != nil {
		return existing, false, err
	}
	if err := saveConversationRequestBindingTx(ctx, tx, goal.CurrentRunID, binding); err != nil {
		return existing, false, err
	}
	if err := queueConversationTitlesTx(ctx, tx, userID, node, objective); err != nil {
		return existing, false, err
	}
	if err := persistConversationGoalTx(ctx, tx, &goal, now); err != nil {
		return existing, false, err
	}
	return goal, false, tx.Commit()
}

func sameConversationGoalBudget(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Read only published conversation messages, not audit output. The bounded
// snapshot is replayed into a fresh native session by ConversationNodeID/run ID.
func conversationGoalHistoryTx(ctx context.Context, tx *sql.Tx, nodeID string) ([]conversationContextMessage, error) {
	history, err := readConversationContext(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT author_kind, body FROM conversation_messages
	 WHERE node_id = ? AND status = 'published' AND deleted_at IS NULL ORDER BY created_at,id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bytes := 0
	for _, message := range history {
		bytes += len(message.Body)
	}
	for rows.Next() {
		var message conversationContextMessage
		if err := rows.Scan(&message.Author, &message.Body); err != nil {
			return nil, err
		}
		bytes += len(message.Body)
		if bytes > conversationContextBytes {
			return nil, domain.Invalid("对话上下文过长，请新建分支并提供必要摘要")
		}
		history = append(history, message)
	}
	return history, rows.Err()
}

func queueConversationGoalRunTx(ctx context.Context, tx *sql.Tx, userID string, node domain.ConversationNode, target *domain.ConversationGoal, audit, initial bool, now time.Time) error {
	copy := *target
	goal := &copy
	// Do not use requireIdleConversationNodes here: this goal already owns the slot.
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE conversation_node_id = ?
	 AND deleted_at IS NULL AND status IN ('queued','starting','running','waiting_approval','cancelling')`, node.ID).Scan(&busy); err != nil {
		return err
	}
	if busy != 0 {
		return domain.Conflict("target_busy", "节点仍在执行或停止中", nil)
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM conversation_graphs WHERE id = ?`, node.GraphID).Scan(&projectID); err != nil {
		return err
	}
	purpose := domain.RunPurposeGoalContinuation
	var prompt string
	var systemPrompt *string
	if audit {
		latest, err := finalAssistantTextSQLiteTx(ctx, tx, goal.LastWorkRunID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(latest) == "" {
			return domain.Invalid("工作轮次没有可供审核的结果")
		}
		prompt = buildGoalAuditPrompt(goal.Objective, latest)
		system := goalAuditSystemPrompt
		systemPrompt = &system
		purpose = domain.RunPurposeGoalAudit
		goal.Phase = domain.GoalPhaseEvaluating
	} else {
		history, err := conversationGoalHistoryTx(ctx, tx, node.ID)
		if err != nil {
			return err
		}
		history = append(history, conversationContextMessage{Author: domain.ConversationAuthorUser, Body: goal.Objective})
		prompt, err = conversationPrompt(node.Title, history)
		if err != nil {
			return err
		}
		prompt += "\n## 节点持续目标\n逐项完成用户目标，不缩小验收标准。沿用图对话的只读权限，不调用 Issue 评论工具。" +
			"每轮直接在节点回复中说明实际进展、证据和剩余工作；不要自行宣称系统已经结束目标。\n<审核备注>" +
			html.EscapeString(goal.Note) + "</审核备注>\n"
		goal.Phase = domain.GoalPhaseRunning
		goal.WorkRuns++
		if initial {
			purpose = domain.RunPurposeWork
		}
	}
	goal.RunCount++
	// A domain error may be returned after Run insertion; the caller persists a blocker.
	if _, err := tx.ExecContext(ctx, `SAVEPOINT conversation_goal_run`); err != nil {
		return err
	}
	run, err := createSQLiteExecutionRun(ctx, tx, sqliteRunCreateInput{
		UserID: userID, ProjectID: projectID, ConversationNodeID: node.ID,
		AgentRevisionID: goal.AgentRevisionID, SystemPromptOverride: systemPrompt, DisableTools: true,
		IdempotencyKey: fmt.Sprintf("conversation-goal:%s:%d", goal.ID, goal.RunCount),
		Run: domain.Run{AgentID: node.AgentID, Purpose: purpose, ReadOnly: true,
			Input: []domain.ContentBlock{{Type: "text", Text: prompt, Internal: audit}}},
		QueuedPayload: map[string]any{"conversation_goal_id": goal.ID, "phase": goal.Phase},
	})
	if err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT conversation_goal_run`); rollbackErr != nil {
			return rollbackErr
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT conversation_goal_run`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_goal_runs(run_id,goal_id,ordinal) VALUES (?,?,?)`, run.ID, goal.ID, goal.RunCount); err != nil {
		return err
	}
	goal.CurrentRunID = run.ID
	if !audit {
		goal.LastWorkRunID = run.ID
		// Internal audit and continuation prompts never become user messages.
		if initial {
			if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_messages
			 (id,node_id,author_kind,body,status,run_id,created_at,updated_at)
			 VALUES (?,?,'user',?,'published',?,?,?)`, newID("cvm"), node.ID, goal.Objective,
				run.ID, sqliteTimeValue(now), sqliteTimeValue(now)); err != nil {
				return err
			}
		}
		stamp := sqliteTimeValue(now.Add(time.Nanosecond))
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_messages
		 (id,node_id,author_kind,body,status,run_id,created_at,updated_at)
		 VALUES (?,?,'agent','...','working',?,?,?)`, newID("cvm"), node.ID, run.ID, stamp, stamp); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversation_nodes SET status='running',version=version+1,updated_at=? WHERE id=?`, sqliteTimeValue(now), node.ID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE conversation_graphs SET updated_at=? WHERE id=?`, sqliteTimeValue(now), node.GraphID)
	}
	if err == nil {
		*target = *goal
	}
	return err
}

func persistConversationGoalTx(ctx context.Context, tx *sql.Tx, goal *domain.ConversationGoal, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE conversation_goals SET status=?,phase=?,tokens_used=?,
	 work_runs=?,run_count=?,current_run_id=NULLIF(?,''),last_work_run_id=NULLIF(?,''),note=?,status_reason=?,
	 version=version+1,updated_at=? WHERE id=?`, goal.Status, goal.Phase, goal.TokensUsed,
		goal.WorkRuns, goal.RunCount, goal.CurrentRunID, goal.LastWorkRunID,
		clampGoalText(goal.Note, 280), goal.StatusReason, sqliteTimeValue(now), goal.ID)
	if err == nil {
		goal.Version++
		goal.UpdatedAt = now
	}
	return err
}

// Count persisted normalized usage for work AND audit. Summing from durable
// events is replay-safe: repeated dispatcher ticks never charge tokens twice.
func refreshConversationGoalTokensTx(ctx context.Context, tx *sql.Tx, goal *domain.ConversationGoal) error {
	return tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(MAX(0,CAST(json_extract(e.payload,'$.total_tokens') AS INTEGER))),0)
	 FROM run_events e JOIN conversation_goal_runs gr ON gr.run_id=e.run_id
	 WHERE gr.goal_id=? AND e.event_type='usage.updated'`, goal.ID).Scan(&goal.TokensUsed)
}

func (s *SQLite) ChangeConversationGoal(ctx context.Context, userID, nodeID, goalID string, version int64, action string) (domain.ConversationGoal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationGoal{}, err
	}
	defer tx.Rollback()
	node, err := conversationGoalNodeTx(ctx, tx, userID, nodeID)
	if err != nil {
		return domain.ConversationGoal{}, err
	}
	var goal domain.ConversationGoal
	err = scanConversationGoal(tx.QueryRowContext(ctx, conversationGoalSelect+` WHERE cg.id=? AND cg.node_id=?`, goalID, nodeID), &goal)
	if errors.Is(err, sql.ErrNoRows) {
		return goal, domain.NotFound("节点目标", goalID)
	}
	if err != nil {
		return goal, err
	}
	if goal.Version != version {
		return goal, versionConflict(goal.Version)
	}
	if err := refreshConversationGoalTokensTx(ctx, tx, &goal); err != nil {
		return goal, err
	}
	now := time.Now().UTC()
	if goal.Status == domain.GoalCompleted || goal.Status == domain.GoalCancelled {
		return goal, domain.Conflict("goal_not_active", "目标已经结束", nil)
	}
	switch action {
	case "pause", "cancel":
		goal.Status = domain.GoalPaused
		if action == "cancel" {
			goal.Status, goal.Phase = domain.GoalCancelled, domain.GoalPhaseSettled
		}
		goal.StatusReason = "user_" + action
		if err := requestSQLiteGoalRunStopTx(ctx, tx, goal.CurrentRunID, "conversation_goal_"+action, now); err != nil {
			return goal, err
		}
	case "resume":
		if goal.Status == domain.GoalActive || node.Locked {
			return goal, domain.Conflict("goal_not_resumable", "当前目标不能恢复", nil)
		}
		if goal.TokenBudget != nil && goal.TokensUsed >= *goal.TokenBudget {
			return goal, domain.Conflict("goal_budget_reached", "目标预算或轮数上限已用尽，请取消后创建新目标", nil)
		}
		if goal.CurrentRunID != "" {
			var status string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, goal.CurrentRunID).Scan(&status); err != nil {
				return goal, err
			}
			if !domain.IsRunTerminal(status) {
				return goal, domain.Conflict("target_busy", "请等待当前运行停止后再恢复", nil)
			}
			if status != domain.RunSucceeded {
				goal.CurrentRunID = ""
			}
		}
		// A paused successful run can still be audited. Other stopped/blocked
		// phases restart work rather than accepting stale audit output.
		if goal.Status != domain.GoalPaused {
			goal.CurrentRunID = ""
		}
		if goal.CurrentRunID == "" && goal.WorkRuns >= goal.MaxWorkRuns {
			return goal, domain.Conflict("goal_budget_reached", "目标轮数上限已用尽，请取消后创建新目标", nil)
		}
		goal.Status, goal.StatusReason = domain.GoalActive, ""
	default:
		return goal, domain.Invalid("不支持的目标操作")
	}
	if err := persistConversationGoalTx(ctx, tx, &goal, now); err != nil {
		return goal, err
	}
	return goal, tx.Commit()
}

// This is polled by the same durable dispatcher as Issue goals. Every step
// (decision, next Run and outbox) commits together, so restart and replay are safe.
func (s *SQLite) AdvanceConversationGoals(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM conversation_goals
	 WHERE status = 'active' ORDER BY updated_at,id`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	count := 0
	for _, id := range ids {
		changed, err := s.advanceConversationGoal(ctx, id, now)
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}

func (s *SQLite) advanceConversationGoal(ctx context.Context, id string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var goal domain.ConversationGoal
	if err := scanConversationGoal(tx.QueryRowContext(ctx, conversationGoalSelect+` WHERE cg.id=?`, id), &goal); err != nil {
		return false, err
	}
	if goal.Status == domain.GoalCancelled || goal.Status == domain.GoalCompleted {
		return false, nil
	}
	previousTokens := goal.TokensUsed
	if err := refreshConversationGoalTokensTx(ctx, tx, &goal); err != nil {
		return false, err
	}
	if goal.Status != domain.GoalActive {
		if previousTokens == goal.TokensUsed {
			return false, nil
		}
		if err := persistConversationGoalTx(ctx, tx, &goal, now); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	var userID string
	if err := tx.QueryRowContext(ctx, `SELECT c.user_id FROM conversation_nodes n
	 JOIN conversation_graphs g ON g.id=n.graph_id JOIN computers c ON c.id=g.computer_id WHERE n.id=?`, goal.NodeID).Scan(&userID); err != nil {
		return false, err
	}
	node, nodeErr := conversationGoalNodeTx(ctx, tx, userID, goal.NodeID)
	var current domain.Run
	if goal.CurrentRunID != "" {
		if err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id=?`, goal.CurrentRunID), &current); err != nil {
			return false, err
		}
	}
	settle := func(status, reason string) {
		goal.Status, goal.Phase, goal.StatusReason = status, domain.GoalPhaseSettled, reason
	}
	switch {
	case nodeErr != nil || node.Locked:
		if nodeErr != nil {
			var de *domain.Error
			if !errors.As(nodeErr, &de) {
				return false, nodeErr
			}
		}
		settle(domain.GoalCancelled, "node_unavailable")
	case goal.TokenBudget != nil && goal.TokensUsed >= *goal.TokenBudget:
		settle(domain.GoalBudgetReached, "token_budget_reached")
	case current.ID != "" && !domain.IsRunTerminal(current.Status):
		if previousTokens == goal.TokensUsed {
			return false, nil
		}
	case current.ID != "" && current.Status != domain.RunSucceeded:
		if current.Status == domain.RunCancelled {
			settle(domain.GoalPaused, "run_cancelled")
		} else {
			settle(domain.GoalBlocked, "run_failed")
		}
	case current.ID != "" && current.Purpose != domain.RunPurposeGoalAudit:
		err = queueConversationGoalRunTx(ctx, tx, userID, node, &goal, true, false, now)
	case current.ID != "":
		text, readErr := finalAssistantTextSQLiteTx(ctx, tx, current.ID)
		if readErr != nil {
			return false, readErr
		}
		var verdict struct {
			Verdict string `json:"verdict"`
			Note    string `json:"note"`
		}
		// Audits must be exactly one JSON object, not prose with a quoted sample.
		if json.Unmarshal([]byte(strings.TrimSpace(text)), &verdict) != nil {
			settle(domain.GoalBlocked, "invalid_audit")
		} else {
			goal.Note = clampGoalText(verdict.Note, 280)
			switch verdict.Verdict {
			case "complete":
				settle(domain.GoalCompleted, "verified")
			case "blocked":
				settle(domain.GoalBlocked, "needs_user")
			case "continue":
				goal.CurrentRunID = ""
			default:
				settle(domain.GoalBlocked, "invalid_audit")
			}
		}
	}
	if err != nil {
		// Provider unavailability/context limits are visible blockers, not an
		// endless failing dispatcher tick. Database errors must still roll back.
		var de *domain.Error
		if !errors.As(err, &de) {
			return false, err
		}
		settle(domain.GoalBlocked, de.Code)
	}
	if goal.Status == domain.GoalActive && goal.CurrentRunID == "" {
		if goal.WorkRuns >= goal.MaxWorkRuns {
			settle(domain.GoalBlocked, "work_limit_reached")
		} else if err := queueConversationGoalRunTx(ctx, tx, userID, node, &goal, false, false, now); err != nil {
			var de *domain.Error
			if !errors.As(err, &de) {
				return false, err
			}
			settle(domain.GoalBlocked, de.Code)
		}
	}
	if goal.Status != domain.GoalActive {
		if err := requestSQLiteGoalRunStopTx(ctx, tx, current.ID, "conversation_goal_settled", now); err != nil {
			return false, err
		}
	}
	if err := persistConversationGoalTx(ctx, tx, &goal, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
