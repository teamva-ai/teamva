package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func decodeAssistantTool(payload map[string]any, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Invalid("查询参数无效")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Invalid("查询参数不符合契约")
	}
	return nil
}

// Owner comes exclusively from the persisted Run. The dispatcher exposes this
// read-only tool contract; it does not accept SQL, filesystem paths or user IDs.
func (s *SQLite) ExecuteWorkReadTool(ctx context.Context, userID, tool string, payload map[string]any) (map[string]any, error) {
	switch tool {
	case "inspiration_list":
		var query domain.InspirationQuery
		if err := decodeAssistantTool(payload, &query); err != nil {
			return nil, err
		}
		page, err := s.ListInspirations(ctx, userID, query)
		return map[string]any{"items": page.Items, "total": page.Total}, err
	case "work_records_query":
		var query domain.WorkQuery
		if err := decodeAssistantTool(payload, &query); err != nil {
			return nil, err
		}
		page, err := s.QueryWorkRecords(ctx, userID, query)
		return map[string]any{"items": page.Items, "total": page.Total}, err
	case "inspiration_get", "work_record_get":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeAssistantTool(payload, &request); err != nil {
			return nil, err
		}
		if tool == "inspiration_get" {
			note, err := s.GetInspiration(ctx, userID, request.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"inspiration": note, "url": "/inspirations?note=" + note.ID}, nil
		}
		record, err := s.GetWorkRecord(ctx, userID, request.ID)
		if err != nil {
			return nil, err
		}
		children, err := s.WorkRecordChildren(ctx, userID, request.ID)
		if err != nil {
			return nil, err
		}
		links, err := s.WorkRecordLinks(ctx, userID, record)
		return map[string]any{"record": record, "children": children, "links": links}, err
	case "work_history_read":
		var request assistantHistoryRequest
		if err := decodeAssistantTool(payload, &request); err != nil {
			return nil, err
		}
		if err := request.validate(); err != nil {
			return nil, err
		}
		return s.readAssistantHistory(ctx, userID, request)
	default:
		return nil, domain.Forbidden("read_only_tool", "助手仅允许查询工具")
	}
}

type assistantHistoryRequest struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Offset int    `json:"offset"`
	Limit  *int   `json:"limit"`
}

func (r *assistantHistoryRequest) validate() error {
	if r.ID == "" || len(r.ID) > 150 {
		return domain.Invalid("历史对象标识无效")
	}
	if r.Limit == nil {
		limit := 20
		r.Limit = &limit
	}
	if r.Offset < 0 || r.Offset > 100000 || *r.Limit < 1 || *r.Limit > 50 {
		return domain.Invalid("历史分页范围无效")
	}
	switch r.Kind {
	case "issue", "node", "message", "comment", "assistant":
		return nil
	default:
		return domain.Invalid("历史对象类型无效")
	}
}

type assistantHistoryPage struct {
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset"`
}

// LIMIT/OFFSET are applied by SQLite, not by slicing a fully materialized body
// list. Count and content share the same read transaction and owner snapshot.
func assistantHistoryRows[T any](ctx context.Context, tx *sql.Tx, request assistantHistoryRequest, countSQL, selectSQL string, args []any, scan func(scanner) (T, error)) ([]T, assistantHistoryPage, error) {
	var page assistantHistoryPage
	if err := tx.QueryRowContext(ctx, countSQL, args...).Scan(&page.Total); err != nil {
		return nil, page, err
	}
	page.NextOffset = min(request.Offset+*request.Limit, page.Total)
	page.HasMore = page.NextOffset < page.Total
	rows, err := tx.QueryContext(ctx, selectSQL+` LIMIT ? OFFSET ?`, append(args, *request.Limit, request.Offset)...)
	if err != nil {
		return nil, page, err
	}
	defer rows.Close()
	items := make([]T, 0, *request.Limit)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, page, err
		}
		items = append(items, item)
	}
	return items, page, rows.Err()
}

func (s *SQLite) readAssistantHistory(ctx context.Context, userID string, request assistantHistoryRequest) (map[string]any, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	switch request.Kind {
	case "issue":
		return readAssistantIssue(ctx, tx, userID, request)
	case "node":
		return readAssistantNode(ctx, tx, userID, request)
	case "assistant":
		session, err := getAssistantSession(ctx, tx, userID, request.ID)
		if err != nil {
			return nil, err
		}
		messages, page, err := assistantHistoryRows(ctx, tx, request,
			`SELECT count(*) FROM assistant_messages WHERE session_id = ?`,
			assistantMessageSelect+` WHERE m.session_id = ? ORDER BY m.created_at,m.id`,
			[]any{session.ID}, scanAssistantMessage)
		url := "/work-records?session=" + session.ID
		if session.Scope == domain.AssistantInspirations {
			url = "/inspirations?session=" + session.ID
		}
		return map[string]any{"link": WorkLink{Kind: "assistant", ID: session.ID, Title: session.Title, URL: url}, "messages": messages, "page": page}, err
	case "message":
		var message domain.ConversationMessage
		err := scanConversationMessage(tx.QueryRowContext(ctx, conversationMessageSelect+conversationMessageOwnership+` AND m.id = ?`, userID, request.ID), &message)
		if errors.Is(err, sql.ErrNoRows) {
			return unavailableAssistantHistory(request), nil
		}
		if err != nil {
			return nil, err
		}
		link, err := assistantNodeLink(ctx, tx, userID, message.NodeID)
		if err != nil {
			return nil, err
		}
		link.Kind, link.ID = "message", message.ID
		link.URL += "&message=" + message.ID
		return map[string]any{"link": link, "message": message}, nil
	case "comment":
		var comment domain.IssueComment
		err := scanSQLiteIssueComment(tx.QueryRowContext(ctx, sqliteIssueCommentSelect+sqliteIssueCommentOwnership+`
			WHERE comments.id = ? AND owning_computer.user_id = ? AND comments.deleted_at IS NULL
			AND owning_issue.deleted_at IS NULL AND owning_project.deleted_at IS NULL
			AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL`, request.ID, userID), &comment)
		if errors.Is(err, sql.ErrNoRows) {
			return unavailableAssistantHistory(request), nil
		}
		if err != nil {
			return nil, err
		}
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id = ?`, comment.IssueID).Scan(&projectID); err != nil {
			return nil, err
		}
		link := WorkLink{Kind: "comment", ID: comment.ID, Title: comment.ID,
			URL: "/issues?issue=" + comment.IssueID + "&project=" + projectID + "&comment=" + comment.ID}
		return map[string]any{"link": link, "comment": comment}, nil
	}
	return nil, domain.Invalid("历史对象类型无效")
}

func unavailableAssistantHistory(request assistantHistoryRequest) map[string]any {
	return map[string]any{"link": WorkLink{Kind: request.Kind, ID: request.ID, Deleted: true}, "unavailable": true}
}

func readAssistantIssue(ctx context.Context, tx *sql.Tx, userID string, request assistantHistoryRequest) (map[string]any, error) {
	var issue domain.Issue
	err := scanSQLiteIssue(tx.QueryRowContext(ctx, sqliteIssueSelect+sqliteIssueOwnership+`
		WHERE issues.id = ? AND owning_computer.user_id = ? AND issues.deleted_at IS NULL
		AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL`, request.ID, userID), &issue)
	if errors.Is(err, sql.ErrNoRows) {
		return unavailableAssistantHistory(request), nil
	}
	if err != nil {
		return nil, err
	}
	comments, page, err := assistantHistoryRows(ctx, tx, request,
		`SELECT count(*) FROM issue_comments WHERE issue_id = ? AND deleted_at IS NULL`,
		sqliteIssueCommentSelect+` WHERE comments.issue_id = ? AND comments.deleted_at IS NULL ORDER BY comments.created_at,comments.id`,
		[]any{issue.ID}, func(row scanner) (domain.IssueComment, error) {
			var comment domain.IssueComment
			err := scanSQLiteIssueComment(row, &comment)
			return comment, err
		})
	link := WorkLink{Kind: "issue", ID: issue.ID, Title: issue.Title, URL: "/issues?issue=" + issue.ID + "&project=" + issue.ProjectID}
	return map[string]any{"link": link, "issue": issue, "comments": comments, "page": page}, err
}

func assistantNodeLink(ctx context.Context, tx *sql.Tx, userID, nodeID string) (WorkLink, error) {
	link := WorkLink{Kind: "node", ID: nodeID}
	var graphID, projectID string
	err := tx.QueryRowContext(ctx, `SELECT n.title,n.graph_id,g.project_id FROM conversation_nodes n
		JOIN conversation_graphs g ON g.id=n.graph_id JOIN projects p ON p.id=g.project_id
		JOIN computers c ON c.id=g.computer_id JOIN users u ON u.id=c.user_id`+
		conversationNodeOwnership+` AND n.id = ?`, userID, nodeID).Scan(&link.Title, &graphID, &projectID)
	if err != nil {
		return link, err
	}
	link.URL = "/conversations?graph=" + graphID + "&node=" + nodeID + "&project=" + projectID
	return link, nil
}

func readAssistantNode(ctx context.Context, tx *sql.Tx, userID string, request assistantHistoryRequest) (map[string]any, error) {
	link, err := assistantNodeLink(ctx, tx, userID, request.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return unavailableAssistantHistory(request), nil
	}
	if err != nil {
		return nil, err
	}
	messages, page, err := assistantHistoryRows(ctx, tx, request,
		`SELECT count(*) FROM conversation_messages WHERE node_id = ? AND deleted_at IS NULL`,
		conversationMessageSelect+` WHERE m.node_id = ? AND m.deleted_at IS NULL AND n.deleted_at IS NULL AND g.deleted_at IS NULL AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL ORDER BY m.created_at,m.id`,
		[]any{request.ID}, func(row scanner) (domain.ConversationMessage, error) {
			var message domain.ConversationMessage
			err := scanConversationMessage(row, &message)
			return message, err
		})
	return map[string]any{"link": link, "messages": messages, "page": page}, err
}
