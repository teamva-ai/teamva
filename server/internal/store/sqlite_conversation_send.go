package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// A fork owns a bounded, immutable copy of the context that existed at its
// boundary. Reading live ancestors would silently change an existing branch.
type conversationContextMessage struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

const conversationContextBytes = 256 * 1024

func readConversationContext(ctx context.Context, tx *sql.Tx, nodeID string) ([]conversationContextMessage, error) {
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT inherited_messages FROM conversation_nodes WHERE id = ?`, nodeID).Scan(&encoded); err != nil {
		return nil, err
	}
	var messages []conversationContextMessage
	if err := json.Unmarshal([]byte(encoded), &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func conversationPrompt(title string, messages []conversationContextMessage) (string, error) {
	var prompt strings.Builder
	prompt.WriteString("这是 Teamva 图对话中的一个分支节点。请直接回复最新的用户消息，不要调用工具。\n")
	if title != "" {
		prompt.WriteString("节点主题：" + title + "\n")
	}
	prompt.WriteString("\n## 对话历史\n")
	for _, message := range messages {
		role := "用户"
		if message.Author == domain.ConversationAuthorAgent {
			role = "助手"
		}
		prompt.WriteString(role + "：" + message.Body + "\n\n")
		if prompt.Len() > conversationContextBytes {
			return "", domain.Invalid("对话上下文过长，请新建对话并提供必要的摘要")
		}
	}
	return prompt.String(), nil
}

// SendConversationMessage is the only HTTP write path for a conversation turn.
// User input, working reply, execution, status and outbox commit together: a busy
// node, unavailable provider or failed command insert cannot leave a ghost input.
func (s *SQLite) SendConversationMessage(ctx context.Context, userID, nodeID, key, body string, bindings ...domain.ConversationAgentBinding) (domain.ConversationMessage, bool, error) {
	binding := conversationRequestBinding(bindings)
	key, body = strings.TrimSpace(key), strings.TrimSpace(body)
	if key == "" || len(key) > 200 {
		return domain.ConversationMessage{}, false, domain.Invalid("需要有效的幂等键")
	}
	if body == "" || len([]rune(body)) > domain.ConversationMessageLimit {
		return domain.ConversationMessage{}, false, domain.Invalid("消息内容为空或过长")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var node domain.ConversationNode
	err = scanConversationNode(tx.QueryRowContext(ctx,
		conversationNodeSelect+conversationNodeOwnership+` AND n.id = ?`, userID, nodeID), &node)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationMessage{}, false, domain.NotFound("对话节点", nodeID)
	}
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if existing, found, err := getSQLiteConversationRunByIdempotency(ctx, tx, userID, nodeID, key); err != nil {
		return domain.ConversationMessage{}, false, err
	} else if found {
		var message domain.ConversationMessage
		err := scanConversationMessage(tx.QueryRowContext(ctx, conversationMessageSelect+conversationMessageOwnership+`
			AND m.node_id = ? AND m.run_id = ? AND m.author_kind = 'user'
		`, userID, nodeID, existing.ID), &message)
		if err == nil && message.Body != body {
			err = domain.Conflict("idempotency_conflict", "幂等键已用于其他消息", nil)
		}
		if err == nil {
			err = requireConversationRequestBindingTx(ctx, tx, existing.ID, binding)
		}
		return message, true, err
	}
	if err := validateConversationAgentBinding(node, binding); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if node.Locked {
		return domain.ConversationMessage{}, false, domain.Invalid("该节点已创建分支，只能从它的消息继续创建分支")
	}
	if err := requireIdleConversationNodes(ctx, tx, []string{nodeID}); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM conversation_graphs WHERE id = ?`, node.GraphID).Scan(&projectID); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	history, err := readConversationContext(ctx, tx, nodeID)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT author_kind, body FROM conversation_messages
		WHERE node_id = ? AND status = 'published' AND deleted_at IS NULL
		ORDER BY created_at, id
	`, nodeID)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	for rows.Next() {
		var message conversationContextMessage
		if err := rows.Scan(&message.Author, &message.Body); err != nil {
			rows.Close()
			return domain.ConversationMessage{}, false, err
		}
		history = append(history, message)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	history = append(history, conversationContextMessage{Author: domain.ConversationAuthorUser, Body: body})
	prompt, err := conversationPrompt(node.Title, history)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	run, err := createSQLiteExecutionRun(ctx, tx, sqliteRunCreateInput{
		UserID: userID, ProjectID: projectID, ConversationNodeID: nodeID,
		IdempotencyKey: key, AgentRevisionID: node.AgentRevisionID, DisableTools: true,
		Run:           domain.Run{AgentID: node.AgentID, ReadOnly: true, Input: []domain.ContentBlock{{Type: "text", Text: prompt}}},
		QueuedPayload: map[string]any{"conversation_node_id": nodeID},
	})
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if err := saveConversationRequestBindingTx(ctx, tx, run.ID, binding); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	now := time.Now().UTC()
	message := domain.ConversationMessage{
		Versioned: domain.Versioned{ID: newID("cvm"), Version: 1, CreatedAt: now, UpdatedAt: now},
		NodeID:    nodeID, AuthorKind: domain.ConversationAuthorUser, Body: body,
		Status: domain.ConversationMessagePublished, RunID: run.ID,
	}
	for index, item := range []domain.ConversationMessage{message, {
		Versioned: domain.Versioned{ID: newID("cvm")}, AuthorKind: domain.ConversationAuthorAgent,
		Body: "...", Status: domain.ConversationMessageWorking,
	}} {
		// Stable order even when both records are written in the same turn.
		stamp := sqliteTimeValue(now.Add(time.Duration(index) * time.Nanosecond))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_messages (id, node_id, author_kind, body, status, run_id, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, item.ID, nodeID, item.AuthorKind, item.Body, item.Status, run.ID, stamp, stamp); err != nil {
			return domain.ConversationMessage{}, false, err
		}
	}
	// First content and title intent share a transaction, but the source must
	// already exist so its deletion can revoke both graph and node naming.
	if err := queueConversationTitlesTx(ctx, tx, userID, node, body); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_nodes SET status = 'running', version = version + 1, updated_at = ? WHERE id = ?
	`, sqliteTimeValue(now), nodeID); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_graphs SET updated_at = ? WHERE id = ?
	`, sqliteTimeValue(now), node.GraphID); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationMessage{}, false, err
	}
	return message, false, nil
}

func conversationRequestBinding(bindings []domain.ConversationAgentBinding) domain.ConversationAgentBinding {
	if len(bindings) == 0 {
		return domain.ConversationAgentBinding{}
	}
	return bindings[0]
}

func validateConversationAgentBinding(node domain.ConversationNode, binding domain.ConversationAgentBinding) error {
	if binding.ExpectedAgentID != "" && binding.ExpectedAgentID != node.AgentID ||
		binding.ExpectedAgentRevisionID != "" && binding.ExpectedAgentRevisionID != node.AgentRevisionID {
		return domain.Conflict("agent_binding_conflict", "节点 Agent 已被修改，请刷新后重试", nil)
	}
	return nil
}

// Persist optional client expectations in the immutable originating Run, not
// the mutable node. Thus retries remain valid after later binding changes.
func saveConversationRequestBindingTx(ctx context.Context, tx *sql.Tx, runID string, binding domain.ConversationAgentBinding) error {
	encoded, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET snapshot=json_set(snapshot,'$.conversation_request_binding',json(?)) WHERE id=?`, string(encoded), runID)
	return err
}

func requireConversationRequestBindingTx(ctx context.Context, tx *sql.Tx, runID string, binding domain.ConversationAgentBinding) error {
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(snapshot,'$.conversation_request_binding'),'{}') FROM runs WHERE id=?`, runID).Scan(&encoded); err != nil {
		return err
	}
	var saved domain.ConversationAgentBinding
	if err := json.Unmarshal([]byte(encoded), &saved); err != nil {
		return err
	}
	if saved != binding {
		return domain.Conflict("idempotency_conflict", "幂等键已用于其他 Agent 绑定", nil)
	}
	return nil
}

func queueConversationTitlesTx(ctx context.Context, tx *sql.Tx, userID string, node domain.ConversationNode, seed string) error {
	if err := queueAutoTitleTx(ctx, tx, userID, "conversation_node", node.ID, seed); err != nil {
		return err
	}
	return queueAutoTitleTx(ctx, tx, userID, "conversation_graph", node.GraphID, seed)
}

func requireIdleConversationNodes(ctx context.Context, tx *sql.Tx, nodeIDs []string) error {
	for _, id := range nodeIDs {
		if err := requireNoConversationGoalTx(ctx, tx, id); err != nil {
			return err
		}
		var activeID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM runs WHERE conversation_node_id = ? AND deleted_at IS NULL
			  AND status IN ('queued','starting','running','waiting_approval','cancelling') LIMIT 1
		`, id).Scan(&activeID)
		if err == nil {
			return domain.Conflict("target_busy", "对话仍在执行，请先停止运行", map[string]any{"active_run_id": activeID})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
