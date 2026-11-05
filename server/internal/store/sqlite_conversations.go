package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Bounded single snapshot for graph detail. The values are deliberately
// generous for real conversations while keeping one browser response sane.
var (
	maxConversationNodes        = 500
	maxConversationMessages     = 5000
	maxConversationMessageBytes = 8 << 20
)

const conversationGraphSelect = `
	SELECT g.id, g.version, g.created_at, g.updated_at, g.deleted_at,
	       g.project_id, g.computer_id, g.title,
	       (SELECT COUNT(*) FROM conversation_nodes n
	         WHERE n.graph_id = g.id AND n.deleted_at IS NULL)
	FROM conversation_graphs g
	JOIN projects p ON p.id = g.project_id
	JOIN computers c ON c.id = g.computer_id
	JOIN users u ON u.id = c.user_id
`

const conversationGraphOwnership = `
	WHERE c.user_id = ? AND g.deleted_at IS NULL
	  AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL
`

const conversationNodeSelect = `
	SELECT n.id, n.version, n.created_at, n.updated_at, n.deleted_at,
	       n.graph_id, COALESCE(n.parent_id, ''), COALESCE(n.fork_message_id, ''),
	       n.agent_id, COALESCE(n.agent_revision_id, ''), n.title, n.status,
	       n.position_x, n.position_y, n.width, n.height, n.locked, COALESCE(n.issue_id, ''),
	       n.inherited_messages,
	       (SELECT COUNT(*) FROM conversation_messages m
	         WHERE m.node_id = n.id AND m.deleted_at IS NULL),
	       COALESCE((SELECT m.body FROM conversation_messages m
	         WHERE m.node_id = n.id AND m.deleted_at IS NULL
	         ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '')
	FROM conversation_nodes n
	JOIN conversation_graphs g ON g.id = n.graph_id
	JOIN projects p ON p.id = g.project_id
	JOIN computers c ON c.id = g.computer_id
	JOIN users u ON u.id = c.user_id
`

const conversationNodeOwnership = `
	WHERE c.user_id = ? AND n.deleted_at IS NULL AND g.deleted_at IS NULL
	  AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL
`

const conversationMessageSelect = `
	SELECT m.id, m.version, m.created_at, m.updated_at, m.deleted_at,
	       m.node_id, m.author_kind, m.body, m.status, COALESCE(m.run_id, '')
	FROM conversation_messages m
	JOIN conversation_nodes n ON n.id = m.node_id
	JOIN conversation_graphs g ON g.id = n.graph_id
	JOIN projects p ON p.id = g.project_id
	JOIN computers c ON c.id = g.computer_id
	JOIN users u ON u.id = c.user_id
`

const conversationMessageOwnership = `
	WHERE c.user_id = ? AND m.deleted_at IS NULL AND n.deleted_at IS NULL
	  AND g.deleted_at IS NULL AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL
`

func scanConversationGraph(row scanner, item *domain.ConversationGraph) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.ProjectID, &item.ComputerID, &item.Title, &item.NodeCount); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func scanConversationNode(row scanner, item *domain.ConversationNode) error {
	var created, updated string
	var deleted sql.NullString
	var locked int
	var inherited string
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.GraphID, &item.ParentID, &item.ForkMessageID, &item.AgentID,
		&item.AgentRevisionID, &item.Title, &item.Status, &item.PositionX,
		&item.PositionY, &item.Width, &item.Height, &locked, &item.IssueID,
		&inherited, &item.MessageCount, &item.Preview); err != nil {
		return err
	}
	item.Locked = locked != 0
	if strings.TrimSpace(inherited) != "" {
		if err := json.Unmarshal([]byte(inherited), &item.InheritedMessages); err != nil {
			return fmt.Errorf("decode conversation node inherited messages: %w", err)
		}
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func scanConversationMessage(row scanner, item *domain.ConversationMessage) error {
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&item.ID, &item.Version, &created, &updated, &deleted,
		&item.NodeID, &item.AuthorKind, &item.Body, &item.Status, &item.RunID); err != nil {
		return err
	}
	return parseSQLiteVersioned(&item.Versioned, created, updated, deleted)
}

func (s *SQLite) ListConversationGraphs(ctx context.Context, userID, projectID string) ([]domain.ConversationGraph, error) {
	if _, err := s.GetProject(ctx, userID, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, conversationGraphSelect+`
		WHERE g.project_id = ? AND c.user_id = ? AND g.deleted_at IS NULL
		  AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL
		ORDER BY g.updated_at DESC, g.id
	`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ConversationGraph, 0)
	for rows.Next() {
		var item domain.ConversationGraph
		if err := scanConversationGraph(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) CreateConversationGraph(ctx context.Context, userID string, graph domain.ConversationGraph, root domain.ConversationNode) (domain.ConversationGraphDetail, error) {
	title := strings.TrimSpace(graph.Title)
	autoTitle := title == ""
	if title == "" {
		title = "新对话"
	}
	if len([]rune(title)) > domain.ConversationTitleLimit {
		return domain.ConversationGraphDetail{}, domain.Invalid("图对话标题过长")
	}
	if len([]rune(strings.TrimSpace(root.Title))) > domain.ConversationTitleLimit {
		return domain.ConversationGraphDetail{}, domain.Invalid("节点标题过长")
	}
	now := sqliteTimeValue(time.Now())
	graphID := newID("cvg")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var insertedID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO conversation_graphs (id, project_id, computer_id, title, version, created_at, updated_at)
		SELECT ?, p.id, p.computer_id, ?, 1, ?, ?
		FROM projects p
		JOIN computers c ON c.id = p.computer_id
		JOIN users u ON u.id = c.user_id
		WHERE p.id = ? AND c.user_id = ? AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL
		RETURNING id
	`, graphID, title, now, now, graph.ProjectID, userID).Scan(&insertedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ConversationGraphDetail{}, domain.NotFound("项目", graph.ProjectID)
		}
		return domain.ConversationGraphDetail{}, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "conversation_graph", graphID); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
	}
	if strings.TrimSpace(root.AgentID) != "" {
		var computerID string
		if err := tx.QueryRowContext(ctx, `SELECT computer_id FROM conversation_graphs WHERE id=?`, graphID).Scan(&computerID); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
		if _, err := createConversationRootTx(ctx, tx, userID, graphID, computerID, root.AgentID, root.Title, now); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	return s.GetConversationGraphDetail(ctx, userID, graphID)
}

// Resolve the Agent and its current revision under the same transaction as the
// binding change. Never read current_revision_id before acquiring the writer.
func conversationAgentRevisionTx(ctx context.Context, tx *sql.Tx, userID, computerID, agentID string) (string, error) {
	var ownerComputer, status, revisionID string
	err := tx.QueryRowContext(ctx, `SELECT a.computer_id,a.status,COALESCE(a.current_revision_id,'')
	 FROM agents a JOIN computers c ON c.id=a.computer_id JOIN users u ON u.id=c.user_id
	 WHERE a.id=? AND c.user_id=? AND a.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL`, agentID, userID).Scan(&ownerComputer, &status, &revisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.NotFound("Agent", agentID)
	}
	if err != nil {
		return "", err
	}
	if ownerComputer != computerID {
		return "", domain.Invalid("Agent 与项目必须位于同一电脑")
	}
	if status != domain.AgentActive || revisionID == "" {
		return "", domain.Invalid("请选择已启用的 Agent")
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_revisions WHERE id=? AND agent_id=? AND deleted_at IS NULL)`, revisionID, agentID).Scan(&valid); err != nil {
		return "", err
	}
	if !valid {
		return "", domain.Invalid("Agent 配置版本不可用")
	}
	return revisionID, nil
}

func createConversationRootTx(ctx context.Context, tx *sql.Tx, userID, graphID, computerID, agentID, title, now string) (string, error) {
	revisionID, err := conversationAgentRevisionTx(ctx, tx, userID, computerID, agentID)
	if err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	autoTitle := title == ""
	if autoTitle {
		title = "新节点"
	}
	id := newID("cvn")
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_nodes
	 (id,graph_id,agent_id,agent_revision_id,title,status,position_x,position_y,version,created_at,updated_at)
	 VALUES (?,?,?,?,?,'idle',0,0,1,?,?)`, id, graphID, agentID, revisionID, title, now, now); err != nil {
		return "", err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "conversation_node", id); err != nil {
			return "", err
		}
	}
	return id, nil
}

// Initialization is idempotent for the same root Agent, even when the first
// response was lost. A stale version can never initialize an empty graph.
func (s *SQLite) InitializeConversationGraphRoot(ctx context.Context, userID, graphID, agentID string, expectedVersion int64) (domain.ConversationGraphDetail, error) {
	if strings.TrimSpace(agentID) == "" || expectedVersion < 1 {
		return domain.ConversationGraphDetail{}, domain.Invalid("需要 Agent 和有效版本")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	defer tx.Rollback()
	var graph domain.ConversationGraph
	err = scanConversationGraph(tx.QueryRowContext(ctx, conversationGraphSelect+conversationGraphOwnership+` AND g.id=?`, userID, graphID), &graph)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationGraphDetail{}, domain.NotFound("图对话", graphID)
	}
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	var rootAgent string
	err = tx.QueryRowContext(ctx, `SELECT agent_id FROM conversation_nodes WHERE graph_id=? AND parent_id IS NULL AND deleted_at IS NULL ORDER BY created_at,id LIMIT 1`, graphID).Scan(&rootAgent)
	if err == nil {
		if rootAgent != agentID {
			return domain.ConversationGraphDetail{}, domain.Conflict("root_already_exists", "图对话已选择根节点 Agent", nil)
		}
	} else {
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.ConversationGraphDetail{}, err
		}
		if graph.Version != expectedVersion {
			return domain.ConversationGraphDetail{}, domain.Conflict("version_conflict", "图对话已被修改", nil)
		}
		now := sqliteTimeValue(time.Now())
		if _, err := createConversationRootTx(ctx, tx, userID, graphID, graph.ComputerID, agentID, "", now); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_graphs SET version=version+1,updated_at=? WHERE id=?`, now, graphID); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	return s.GetConversationGraphDetail(ctx, userID, graphID)
}

func (s *SQLite) UpdateConversationGraphTitle(ctx context.Context, userID, graphID string, expectedVersion int64, title string) (domain.ConversationGraph, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > domain.ConversationTitleLimit {
		return domain.ConversationGraph{}, domain.Invalid("图对话标题为空或过长")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationGraph{}, err
	}
	defer tx.Rollback()
	var graph domain.ConversationGraph
	err = scanConversationGraph(tx.QueryRowContext(ctx, conversationGraphSelect+conversationGraphOwnership+` AND g.id=?`, userID, graphID), &graph)
	if errors.Is(err, sql.ErrNoRows) {
		return graph, domain.NotFound("图对话", graphID)
	}
	if err != nil {
		return graph, err
	}
	if graph.Version != expectedVersion {
		return graph, domain.Conflict("version_conflict", "图对话已被修改", nil)
	}
	if err := manualAutoTitleTx(ctx, tx, "conversation_graph", graphID); err != nil {
		return graph, err
	}
	if graph.Title != title {
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_graphs SET title=?,version=version+1,updated_at=? WHERE id=? AND version=?`, title, sqliteTimeValue(time.Now()), graphID, expectedVersion); err != nil {
			return graph, err
		}
	}
	if err := tx.Commit(); err != nil {
		return graph, err
	}
	return s.GetConversationGraph(ctx, userID, graphID)
}

func (s *SQLite) GetConversationGraph(ctx context.Context, userID, graphID string) (domain.ConversationGraph, error) {
	var item domain.ConversationGraph
	err := scanConversationGraph(s.db.QueryRowContext(ctx,
		conversationGraphSelect+conversationGraphOwnership+` AND g.id = ?`, userID, graphID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationGraph{}, domain.NotFound("图对话", graphID)
	}
	return item, err
}

func (s *SQLite) GetConversationGraphDetail(ctx context.Context, userID, graphID string) (domain.ConversationGraphDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var graph domain.ConversationGraph
	err = scanConversationGraph(tx.QueryRowContext(ctx,
		conversationGraphSelect+conversationGraphOwnership+` AND g.id = ?`, userID, graphID), &graph)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationGraphDetail{}, domain.NotFound("图对话", graphID)
	}
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	// A single browser snapshot must stay bounded. Exceeding the budget fails
	// explicitly instead of silently dropping nodes or materializing a huge JSON.
	var nodeCount, messageCount, messageBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM conversation_nodes WHERE graph_id = ? AND deleted_at IS NULL
	`, graphID).Scan(&nodeCount); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(body)), 0) FROM conversation_messages
		WHERE node_id IN (SELECT id FROM conversation_nodes WHERE graph_id = ? AND deleted_at IS NULL)
		  AND deleted_at IS NULL
	`, graphID).Scan(&messageCount, &messageBytes); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	if nodeCount > int64(maxConversationNodes) || messageCount > int64(maxConversationMessages) ||
		messageBytes > int64(maxConversationMessageBytes) {
		return domain.ConversationGraphDetail{}, &domain.Error{
			Code: "conversation_too_large", Message: "图对话规模超过本地单次读取上限，请新建图或删除不再需要的分支",
			HTTPStatus: http.StatusRequestEntityTooLarge, Retryable: false,
		}
	}
	nodeRows, err := tx.QueryContext(ctx, conversationNodeSelect+conversationNodeOwnership+`
		AND n.graph_id = ? ORDER BY n.created_at, n.id
	`, userID, graphID)
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	defer nodeRows.Close()
	nodes := make([]domain.ConversationNode, 0)
	for nodeRows.Next() {
		var item domain.ConversationNode
		if err := scanConversationNode(nodeRows, &item); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
		nodes = append(nodes, item)
	}
	if err := nodeRows.Err(); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	messageRows, err := tx.QueryContext(ctx, conversationMessageSelect+conversationMessageOwnership+`
		  AND n.graph_id = ?
		ORDER BY m.created_at, m.id
	`, userID, graphID)
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	defer messageRows.Close()
	messages := make([]domain.ConversationMessage, 0)
	for messageRows.Next() {
		var item domain.ConversationMessage
		if err := scanConversationMessage(messageRows, &item); err != nil {
			return domain.ConversationGraphDetail{}, err
		}
		messages = append(messages, item)
	}
	if err := messageRows.Err(); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	goalRows, err := tx.QueryContext(ctx, conversationGoalSelect+`
	 JOIN conversation_nodes n ON n.id=cg.node_id WHERE n.graph_id=? AND n.deleted_at IS NULL
	 AND NOT EXISTS (SELECT 1 FROM conversation_goals newer WHERE newer.node_id=cg.node_id
	   AND (newer.created_at>cg.created_at OR (newer.created_at=cg.created_at AND newer.id>cg.id)))`, graphID)
	if err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	goals := make([]domain.ConversationGoal, 0)
	for goalRows.Next() {
		var goal domain.ConversationGoal
		if err := scanConversationGoal(goalRows, &goal); err != nil {
			goalRows.Close()
			return domain.ConversationGraphDetail{}, err
		}
		goals = append(goals, goal)
	}
	goalErr := goalRows.Err()
	goalRows.Close()
	if goalErr != nil {
		return domain.ConversationGraphDetail{}, goalErr
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationGraphDetail{}, err
	}
	return domain.ConversationGraphDetail{Graph: graph, Nodes: nodes, Messages: messages, Goals: goals}, nil
}

func (s *SQLite) DeleteConversationGraph(ctx context.Context, userID, graphID string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var graph domain.ConversationGraph
	err = scanConversationGraph(tx.QueryRowContext(ctx,
		conversationGraphSelect+conversationGraphOwnership+` AND g.id = ?`, userID, graphID), &graph)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("图对话", graphID)
	}
	if err != nil {
		return err
	}
	if graph.Version != expectedVersion {
		return domain.Conflict("version_conflict", "图对话已被修改", nil)
	}
	if err := deleteConversationGraphTx(ctx, tx, graphID); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteConversationGraphTx(ctx context.Context, tx *sql.Tx, graphID string) error {
	now := sqliteTimeValue(time.Now())
	var hasGoal bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_goals cg
	 JOIN conversation_nodes n ON n.id=cg.node_id WHERE n.graph_id=? AND n.deleted_at IS NULL
	 AND cg.status IN ('active','paused','blocked','budget_reached'))`, graphID).Scan(&hasGoal); err != nil {
		return err
	}
	if hasGoal {
		return domain.Conflict("goal_already_exists", "请先完成或取消图中的目标", nil)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM runs r JOIN conversation_nodes n ON n.id = r.conversation_node_id
		WHERE n.graph_id = ? AND r.deleted_at IS NULL
		  AND r.status IN ('queued','starting','running','waiting_approval','cancelling'))
	`, graphID).Scan(&active); err != nil {
		return err
	}
	if active {
		return domain.Conflict("target_busy", "对话仍在执行，请先停止运行", nil)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_messages SET deleted_at = ?, updated_at = ?
		WHERE deleted_at IS NULL AND node_id IN (SELECT id FROM conversation_nodes WHERE graph_id = ?)
	`, now, now, graphID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_nodes SET deleted_at = ?, updated_at = ?, version = version + 1
		WHERE deleted_at IS NULL AND graph_id = ?
	`, now, now, graphID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_graphs SET deleted_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, graphID); err != nil {
		return err
	}
	return nil
}

func (s *SQLite) GetConversationNode(ctx context.Context, userID, nodeID string) (domain.ConversationNode, error) {
	var item domain.ConversationNode
	err := scanConversationNode(s.db.QueryRowContext(ctx,
		conversationNodeSelect+conversationNodeOwnership+` AND n.id = ?`, userID, nodeID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationNode{}, domain.NotFound("对话节点", nodeID)
	}
	return item, err
}

func (s *SQLite) UpdateConversationNode(ctx context.Context, userID, nodeID string, expectedVersion int64, mutate func(*domain.ConversationNode)) (domain.ConversationNode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	defer tx.Rollback()
	item, err := conversationGoalNodeTx(ctx, tx, userID, nodeID)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	if item.Version != expectedVersion {
		return domain.ConversationNode{}, domain.Conflict("version_conflict", "对话节点已被修改", nil)
	}
	original := item
	mutate(&item)
	item.AgentID = strings.TrimSpace(item.AgentID)
	item.Title = strings.TrimSpace(item.Title)
	if len([]rune(item.Title)) > domain.ConversationTitleLimit {
		return domain.ConversationNode{}, domain.Invalid("节点标题过长")
	}
	if math.IsNaN(item.PositionX) || math.IsNaN(item.PositionY) || math.IsInf(item.PositionX, 0) || math.IsInf(item.PositionY, 0) ||
		math.Abs(item.PositionX) > 1_000_000 || math.Abs(item.PositionY) > 1_000_000 {
		return domain.ConversationNode{}, domain.Invalid("节点坐标超出范围")
	}
	if math.IsNaN(item.Width) || math.IsNaN(item.Height) || item.Width < 300 || item.Width > 1000 {
		return domain.ConversationNode{}, domain.Invalid("节点宽度需要在 300 到 1000 之间")
	}
	if math.IsNaN(item.Height) || item.Height < 260 || item.Height > 1600 {
		return domain.ConversationNode{}, domain.Invalid("节点高度需要在 260 到 1600 之间")
	}
	status := item.Status
	switch status {
	case domain.ConversationNodeIdle, domain.ConversationNodeRunning, domain.ConversationNodePaused, domain.ConversationNodeFailed:
	default:
		return domain.ConversationNode{}, domain.Invalid("不支持的节点状态")
	}
	// Binding and admission use the same transactional snapshot as send/Goal.
	// History remains attached to its original Runs; only future work changes.
	item.AgentRevisionID = original.AgentRevisionID
	if item.AgentID != original.AgentID {
		if item.AgentID == "" {
			return domain.ConversationNode{}, domain.Invalid("请选择 Agent")
		}
		if original.Locked {
			return domain.ConversationNode{}, domain.Conflict("node_locked", "历史节点不能更换 Agent", nil)
		}
		if err := requireIdleConversationNodes(ctx, tx, []string{nodeID}); err != nil {
			return domain.ConversationNode{}, err
		}
		var computerID string
		if err := tx.QueryRowContext(ctx, `SELECT computer_id FROM conversation_graphs WHERE id=?`, item.GraphID).Scan(&computerID); err != nil {
			return domain.ConversationNode{}, err
		}
		revisionID, err := conversationAgentRevisionTx(ctx, tx, userID, computerID, item.AgentID)
		if err != nil {
			return domain.ConversationNode{}, err
		}
		item.AgentRevisionID = revisionID
	}
	if item.TitleEdited || item.Title != original.Title {
		if err := manualAutoTitleTx(ctx, tx, "conversation_node", nodeID); err != nil {
			return domain.ConversationNode{}, err
		}
	}
	if item.Title == original.Title && item.AgentID == original.AgentID && item.Status == original.Status &&
		item.PositionX == original.PositionX && item.PositionY == original.PositionY && item.Width == original.Width &&
		item.Height == original.Height && item.IssueID == original.IssueID {
		if err := tx.Commit(); err != nil {
			return domain.ConversationNode{}, err
		}
		original.TitleEdited = false
		return original, nil
	}
	now := sqliteTimeValue(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE conversation_nodes
		SET title = ?, status = ?, position_x = ?, position_y = ?,
		    width = ?, height = ?, issue_id = NULLIF(?, ''),
		    agent_id = ?, agent_revision_id = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND deleted_at IS NULL
	`, item.Title, status, item.PositionX, item.PositionY, item.Width, item.Height,
		item.IssueID, item.AgentID, item.AgentRevisionID, now, nodeID, expectedVersion)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return domain.ConversationNode{}, err
		}
		return domain.ConversationNode{}, domain.Conflict("version_conflict", "对话节点已被修改", nil)
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationNode{}, err
	}
	return s.GetConversationNode(ctx, userID, nodeID)
}

// DeleteConversationNode soft-deletes a node and its whole subtree. Deleting a
// root node removes the entire graph so no orphaned tree remains.
func (s *SQLite) DeleteConversationNode(ctx context.Context, userID, nodeID string, expectedVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ConversationNode
	err = scanConversationNode(tx.QueryRowContext(ctx,
		conversationNodeSelect+conversationNodeOwnership+` AND n.id = ?`, userID, nodeID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("对话节点", nodeID)
	}
	if err != nil {
		return err
	}
	if item.Version != expectedVersion {
		return domain.Conflict("version_conflict", "对话节点已被修改", nil)
	}
	if item.ParentID == "" {
		if err := deleteConversationGraphTx(ctx, tx, item.GraphID); err != nil {
			return err
		}
		return tx.Commit()
	}
	now := sqliteTimeValue(time.Now())
	var active bool
	if err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT ? UNION ALL
			SELECT n.id FROM conversation_nodes n JOIN subtree s ON n.parent_id = s.id WHERE n.deleted_at IS NULL
		)
		SELECT EXISTS(SELECT 1 FROM runs WHERE conversation_node_id IN (SELECT id FROM subtree)
		  AND deleted_at IS NULL AND status IN ('queued','starting','running','waiting_approval','cancelling'))
	`, nodeID).Scan(&active); err != nil {
		return err
	}
	if active {
		return domain.Conflict("target_busy", "对话仍在执行，请先停止运行", nil)
	}
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT ?
			UNION ALL
			SELECT n.id FROM conversation_nodes n JOIN subtree s ON n.parent_id = s.id
			WHERE n.deleted_at IS NULL
		)
		UPDATE conversation_messages SET deleted_at = ?, updated_at = ?
		WHERE deleted_at IS NULL AND node_id IN (SELECT id FROM subtree)
	`, nodeID, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT ?
			UNION ALL
			SELECT n.id FROM conversation_nodes n JOIN subtree s ON n.parent_id = s.id
			WHERE n.deleted_at IS NULL
		)
		UPDATE conversation_nodes SET deleted_at = ?, updated_at = ?, version = version + 1
		WHERE deleted_at IS NULL AND id IN (SELECT id FROM subtree)
	`, nodeID, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_graphs SET version = version + 1, updated_at = ? WHERE id = ?`, now, item.GraphID); err != nil {
		return err
	}
	return tx.Commit()
}

// ForkConversationNode splits a node at one published Agent message.
//
// 语义（与产品约定一致）：
//  1. 新增冻结父副本，只复制截至分叉点的消息和原有继承上下文；
//  2. 原节点成为可写的完整延续分支，保留全部消息、Run 关联及继承上下文；
//  3. 新增可写分支，使用截至分叉点的继承快照，本地消息从空开始；
//  4. 三者沿用源节点尺寸，原有后代仅整体平移，不改变其内容或锁定状态。
func (s *SQLite) ForkConversationNode(ctx context.Context, userID, nodeID, messageID string, child domain.ConversationNode) (domain.ConversationNode, error) {
	child.Title = strings.TrimSpace(child.Title)
	autoTitle := child.Title == ""
	if autoTitle {
		child.Title = "新分支"
	}
	if len([]rune(child.Title)) > domain.ConversationTitleLimit {
		return domain.ConversationNode{}, domain.Invalid("节点标题过长")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var source domain.ConversationNode
	// Re-read under the write transaction so a simultaneous send/fork/delete
	// cannot invalidate the boundary while we copy its history.
	err = scanConversationNode(tx.QueryRowContext(ctx,
		conversationNodeSelect+conversationNodeOwnership+` AND n.id = ?`, userID, nodeID), &source)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationNode{}, domain.NotFound("对话节点", nodeID)
	}
	if err != nil {
		return domain.ConversationNode{}, err
	}
	if child.AgentID == "" {
		child.AgentID = source.AgentID
	}
	var computerID string
	if err := tx.QueryRowContext(ctx, `SELECT computer_id FROM conversation_graphs WHERE id=?`, source.GraphID).Scan(&computerID); err != nil {
		return domain.ConversationNode{}, err
	}
	if _, err := conversationAgentRevisionTx(ctx, tx, userID, computerID, child.AgentID); err != nil {
		return domain.ConversationNode{}, err
	}
	if err := requireIdleConversationNodes(ctx, tx, []string{nodeID}); err != nil {
		return domain.ConversationNode{}, err
	}
	inherited, err := readConversationContext(ctx, tx, nodeID)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	parentContext, err := json.Marshal(inherited)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	var forkAt string
	err = tx.QueryRowContext(ctx, `
		SELECT m.created_at FROM conversation_messages m
		JOIN conversation_nodes n ON n.id = m.node_id
		JOIN conversation_graphs g ON g.id = n.graph_id
		JOIN computers c ON c.id = g.computer_id
		WHERE m.id = ? AND m.node_id = ? AND c.user_id = ? AND m.deleted_at IS NULL
		  AND n.deleted_at IS NULL AND g.deleted_at IS NULL AND c.deleted_at IS NULL
		  AND m.author_kind = 'agent' AND m.status = 'published'
	`, messageID, nodeID, userID).Scan(&forkAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationNode{}, domain.Invalid("只能基于已发布的 Agent 消息分叉")
	}
	if err != nil {
		return domain.ConversationNode{}, err
	}

	// 需要复制到新父节点的消息（分叉点及之前）。
	type historyMessage struct {
		AuthorKind string
		Body       string
		Status     string
		RunID      string
		CreatedAt  string
		UpdatedAt  string
		Version    int64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT m.author_kind, m.body, m.status, COALESCE(m.run_id, ''), m.created_at, m.updated_at, m.version
		FROM conversation_messages m
		WHERE m.node_id = ? AND m.deleted_at IS NULL
		  AND (m.created_at < ? OR (m.created_at = ? AND m.id <= ?))
		ORDER BY m.created_at, m.id
	`, nodeID, forkAt, forkAt, messageID)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	history := make([]historyMessage, 0)
	for rows.Next() {
		var item historyMessage
		if err := rows.Scan(&item.AuthorKind, &item.Body, &item.Status, &item.RunID, &item.CreatedAt, &item.UpdatedAt, &item.Version); err != nil {
			rows.Close()
			return domain.ConversationNode{}, err
		}
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ConversationNode{}, err
	}
	rows.Close()
	for _, item := range history {
		if item.Status == domain.ConversationMessagePublished {
			inherited = append(inherited, conversationContextMessage{Author: item.AuthorKind, Body: item.Body})
		}
	}
	if _, err := conversationPrompt(child.Title, inherited); err != nil {
		return domain.ConversationNode{}, err
	}
	childContext, err := json.Marshal(inherited)
	if err != nil {
		return domain.ConversationNode{}, err
	}
	layout, err := layoutConversationFork(ctx, tx, source)
	if err != nil {
		return domain.ConversationNode{}, err
	}

	now := sqliteTimeValue(time.Now())
	forkNodeID := newID("cvn")
	childID := child.ID
	if childID == "" {
		childID = newID("cvn")
	}
	// 新父节点：克隆原节点元数据，位置沿用原节点。它保存分叉点之前的
	// 历史，属于已锁定节点：不能再发送消息，只能继续创建分支。
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_nodes (
			id, graph_id, parent_id, fork_message_id, agent_id, agent_revision_id,
			title, status, position_x, position_y, width, height, locked,
			inherited_messages, version, created_at, updated_at
		) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''),
		          ?, ?, ?, ?, ?, ?, 1, ?, 1, ?, ?)
	`, forkNodeID, source.GraphID, source.ParentID, source.ForkMessageID,
		source.AgentID, source.AgentRevisionID, source.Title, domain.ConversationNodeIdle,
		source.PositionX, source.PositionY, source.Width, source.Height,
		string(parentContext), now, now); err != nil {
		return domain.ConversationNode{}, err
	}
	for _, item := range history {
		var runID any
		if item.RunID != "" {
			runID = item.RunID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_messages (id, node_id, author_kind, body, status, run_id, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, newID("cvm"), forkNodeID, item.AuthorKind, item.Body, item.Status, runID, item.Version, item.CreatedAt, item.UpdatedAt); err != nil {
			return domain.ConversationNode{}, err
		}
	}
	// 在改变 source 的 parent 链之前，整体平移其存量后代。保留人工布局、
	// 消息和后代之间的 parent 链，不重排同图的其他分支或已删除节点。
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT ? UNION
			SELECT n.id FROM conversation_nodes n JOIN subtree s ON n.parent_id = s.id
			WHERE n.graph_id = ? AND n.deleted_at IS NULL
		)
		UPDATE conversation_nodes
		SET position_x = position_x + ?, position_y = position_y + ?,
		    version = version + 1, updated_at = ?
		WHERE id IN (SELECT id FROM subtree) AND id <> ? AND graph_id = ? AND deleted_at IS NULL
	`, nodeID, source.GraphID, layout.offsetX, layout.offsetY, now, nodeID, source.GraphID); err != nil {
		return domain.ConversationNode{}, err
	}

	// 原节点作为完整延续分支。即使源原本已冻结，也只解锁源本身，
	// 不解锁或重挂它的旧后代；原始消息和历史 Run 完全不改写。
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_nodes
		SET parent_id = ?, fork_message_id = ?, locked = 0,
		    position_x = ?, position_y = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, forkNodeID, messageID, source.PositionX+layout.offsetX, source.PositionY+layout.offsetY, now, nodeID); err != nil {
		return domain.ConversationNode{}, err
	}
	// 新分叉节点：空消息，挂在父节点下方，可以继续发送消息。
	var insertedID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO conversation_nodes (
			id, graph_id, parent_id, fork_message_id, agent_id, agent_revision_id,
			title, status, position_x, position_y, width, height, locked,
			inherited_messages, version, created_at, updated_at
		)
		SELECT ?, ?, ?, ?, a.id, CASE WHEN a.id = ? THEN ? ELSE a.current_revision_id END,
		       ?, 'idle', ?, ?, ?, ?, 0, ?, 1, ?, ?
		FROM agents a
		JOIN computers c ON c.id = a.computer_id
		WHERE a.id = ? AND c.user_id = ? AND a.status = 'active' AND a.deleted_at IS NULL AND c.deleted_at IS NULL
		  AND a.computer_id = (SELECT computer_id FROM conversation_graphs WHERE id = ?)
		RETURNING id
	`, childID, source.GraphID, forkNodeID, messageID, source.AgentID, source.AgentRevisionID, child.Title,
		layout.childX, layout.childY, source.Width, source.Height, string(childContext), now, now, child.AgentID, userID, source.GraphID).Scan(&insertedID); err != nil {
		return domain.ConversationNode{}, err
	}
	if autoTitle {
		if err := initAutoTitleTx(ctx, tx, userID, "conversation_node", childID); err != nil {
			return domain.ConversationNode{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_graphs SET updated_at = ?, version = version + 1 WHERE id = ? AND deleted_at IS NULL
	`, now, source.GraphID); err != nil {
		return domain.ConversationNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationNode{}, err
	}
	return s.GetConversationNode(ctx, userID, childID)
}

type conversationForkLayout struct {
	offsetX, offsetY float64
	childX, childY   float64
}

// layoutConversationFork keeps the history copy at source's position and moves
// the existing subtree rigidly to the upper right, with the new branch below.
// Only this subtree and the new branch move; other trees retain their geometry.
// Read all rectangles in the same transaction as the subsequent writes so a
// concurrent move/fork cannot leave us laying out against a stale snapshot.
func layoutConversationFork(ctx context.Context, tx *sql.Tx, source domain.ConversationNode) (conversationForkLayout, error) {
	const gap = 80.0
	type rectangle struct{ x, y, width, height float64 }
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT ? UNION
			SELECT n.id FROM conversation_nodes n JOIN subtree s ON n.parent_id = s.id
			WHERE n.graph_id = ? AND n.deleted_at IS NULL
		)
		SELECT position_x, position_y, width, height, id IN (SELECT id FROM subtree)
		FROM conversation_nodes WHERE graph_id = ? AND deleted_at IS NULL
		ORDER BY id
	`, source.ID, source.GraphID, source.GraphID)
	if err != nil {
		return conversationForkLayout{}, err
	}
	var moving, fixed []rectangle
	for rows.Next() {
		var r rectangle
		var inSubtree bool
		if err := rows.Scan(&r.x, &r.y, &r.width, &r.height, &inSubtree); err != nil {
			rows.Close()
			return conversationForkLayout{}, err
		}
		if inSubtree {
			moving = append(moving, r)
		} else {
			fixed = append(fixed, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return conversationForkLayout{}, err
	}

	dx, rowGap := source.Width+gap, (source.Height+gap)/2
	for _, r := range moving {
		// A manually moved descendant can sit left of source. Keep the entire
		// translated subtree to the right of the new, stationary history copy.
		dx = math.Max(dx, source.PositionX+source.Width+gap-r.x)
		// Both branches inherit source's persisted border box. Leave space for
		// every descendant crossing that column, including manually resized cards.
		if r.x < source.PositionX+source.Width+gap && r.x+r.width+gap > source.PositionX {
			rowGap = math.Max(rowGap, (r.y-source.PositionY+r.height+gap)/2)
		}
	}
	for i := range moving {
		moving[i].y -= rowGap
	}
	moving = append(moving, rectangle{source.PositionX, source.PositionY + rowGap, source.Width, source.Height})

	// For vertically intersecting cards, each fixed rectangle forbids an open
	// interval of horizontal translations. Merge by scanning sorted intervals:
	// the first legal dx avoids collisions without rearranging unrelated nodes.
	type interval struct{ start, end float64 }
	var forbidden []interval
	for _, m := range moving {
		for _, f := range fixed {
			if m.y < f.y+f.height+gap && m.y+m.height+gap > f.y {
				forbidden = append(forbidden, interval{f.x - m.x - m.width - gap, f.x + f.width + gap - m.x})
			}
		}
	}
	sort.Slice(forbidden, func(i, j int) bool { return forbidden[i].start < forbidden[j].start })
	for _, interval := range forbidden {
		if dx <= interval.start {
			break
		}
		if dx < interval.end {
			dx = interval.end
		}
	}
	for _, r := range moving {
		if math.Abs(r.x+dx) > 1_000_000 || math.Abs(r.y) > 1_000_000 {
			return conversationForkLayout{}, domain.Invalid("分叉后节点坐标超出范围，请先将分支移回画布中心")
		}
	}
	return conversationForkLayout{offsetX: dx, offsetY: -rowGap,
		childX: source.PositionX + dx, childY: source.PositionY + rowGap}, nil
}

func (s *SQLite) ListConversationMessages(ctx context.Context, userID, nodeID string) ([]domain.ConversationMessage, error) {
	if _, err := s.GetConversationNode(ctx, userID, nodeID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, conversationMessageSelect+conversationMessageOwnership+`
		AND m.node_id = ? ORDER BY m.created_at, m.id
	`, userID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ConversationMessage, 0)
	for rows.Next() {
		var item domain.ConversationMessage
		if err := scanConversationMessage(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) CreateConversationMessage(ctx context.Context, userID, nodeID string, input domain.ConversationMessage) (domain.ConversationMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	defer tx.Rollback()
	node, err := conversationGoalNodeTx(ctx, tx, userID, nodeID)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return domain.ConversationMessage{}, domain.Invalid("消息内容不能为空")
	}
	if len([]rune(body)) > domain.ConversationMessageLimit {
		return domain.ConversationMessage{}, domain.Invalid("消息内容过长")
	}
	authorKind := input.AuthorKind
	if authorKind == "" {
		authorKind = domain.ConversationAuthorUser
	}
	if authorKind != domain.ConversationAuthorUser && authorKind != domain.ConversationAuthorAgent {
		return domain.ConversationMessage{}, domain.Invalid("不支持的消息作者")
	}
	status := input.Status
	if status == "" {
		status = domain.ConversationMessagePublished
	}
	now := sqliteTimeValue(time.Now())
	id := input.ID
	if id == "" {
		id = newID("cvm")
	}
	var runID any
	if input.RunID != "" {
		runID = input.RunID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_messages (id, node_id, author_kind, body, status, run_id, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
	`, id, nodeID, authorKind, body, status, runID, now, now); err != nil {
		return domain.ConversationMessage{}, err
	}
	if authorKind == domain.ConversationAuthorUser {
		if err := queueConversationTitlesTx(ctx, tx, userID, node, body); err != nil {
			return domain.ConversationMessage{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_graphs SET updated_at = ?
		WHERE id = (SELECT graph_id FROM conversation_nodes WHERE id = ?) AND deleted_at IS NULL
	`, now, nodeID); err != nil {
		return domain.ConversationMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationMessage{}, err
	}
	return s.getConversationMessage(ctx, userID, id)
}

func (s *SQLite) getConversationMessage(ctx context.Context, userID, messageID string) (domain.ConversationMessage, error) {
	var item domain.ConversationMessage
	err := scanConversationMessage(s.db.QueryRowContext(ctx,
		conversationMessageSelect+conversationMessageOwnership+` AND m.id = ?`, userID, messageID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationMessage{}, domain.NotFound("消息", messageID)
	}
	return item, err
}

func getSQLiteConversationRunByIdempotency(ctx context.Context, tx *sql.Tx, userID, nodeID, idempotencyKey string) (domain.Run, bool, error) {
	var item domain.Run
	err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+`
		JOIN conversation_nodes node ON node.id = runs.conversation_node_id
		JOIN conversation_graphs graph ON graph.id = node.graph_id
		JOIN computers computer ON computer.id = graph.computer_id
		WHERE runs.conversation_node_id = ? AND runs.idempotency_key = ?
		  AND computer.user_id = ? AND runs.deleted_at IS NULL
	`, nodeID, idempotencyKey, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, false, nil
	}
	return item, err == nil, err
}
