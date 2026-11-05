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

const assistantSessionSelect = `
	SELECT s.id, s.agent_id, s.title, s.created_at, s.updated_at, s.scope
	FROM assistant_sessions s
	JOIN agents a ON a.id = s.agent_id
	JOIN computers c ON c.id = a.computer_id AND c.user_id = s.user_id
	JOIN users u ON u.id = s.user_id
	WHERE s.user_id = ? AND a.system_role = 'general'
	  AND a.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL`

func scanAssistantSession(row scanner) (domain.AssistantSession, error) {
	var session domain.AssistantSession
	err := row.Scan(&session.ID, &session.AgentID, &session.Title, &session.CreatedAt, &session.UpdatedAt, &session.Scope)
	return session, err
}

func getAssistantSession(ctx context.Context, query sqliteQueryer, userID, id string) (domain.AssistantSession, error) {
	session, err := scanAssistantSession(query.QueryRowContext(ctx, assistantSessionSelect+` AND s.id = ?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return session, domain.NotFound("助手会话", id)
	}
	return session, err
}

func (s *SQLite) ListAssistantSessions(ctx context.Context, userID string, scope domain.AssistantScope) ([]domain.AssistantSession, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, assistantSessionSelect+` AND s.scope = ? ORDER BY s.updated_at DESC, s.id DESC LIMIT 200`, userID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []domain.AssistantSession{}
	for rows.Next() {
		session, err := scanAssistantSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *SQLite) CreateAssistantSession(ctx context.Context, userID, title string, scope domain.AssistantScope) (domain.AssistantSession, error) {
	if err := scope.Validate(); err != nil {
		return domain.AssistantSession{}, err
	}
	agent, err := s.EnsureGeneralAgent(ctx, userID)
	if err != nil {
		return domain.AssistantSession{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "工作回顾"
		if scope == domain.AssistantInspirations {
			title = "灵感对话"
		}
	}
	if len([]rune(title)) > 200 {
		return domain.AssistantSession{}, domain.Invalid("会话标题过长")
	}
	stamp := sqliteTimeValue(time.Now())
	session := domain.AssistantSession{ID: newID("was"), AgentID: agent.ID, Title: title, Scope: scope, CreatedAt: stamp, UpdatedAt: stamp}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO assistant_sessions(id, user_id, agent_id, title, created_at, updated_at, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, session.ID, userID, agent.ID, title, stamp, stamp, scope)
	return session, err
}

const assistantMessageSelect = `
	SELECT m.id, m.session_id, m.author_kind, m.body, m.run_id, m.query, m.created_at, r.status
	FROM assistant_messages m JOIN runs r ON r.id = m.run_id`

func scanAssistantMessage(row scanner) (domain.AssistantMessage, error) {
	var message domain.AssistantMessage
	var selection string
	err := row.Scan(&message.ID, &message.SessionID, &message.AuthorKind, &message.Body,
		&message.RunID, &selection, &message.CreatedAt, &message.Status)
	message.Query = json.RawMessage(selection)
	return message, err
}

func listAssistantMessages(ctx context.Context, query sqliteQueryer, sessionID string, offset, limit int, descending bool) ([]domain.AssistantMessage, error) {
	order := "ASC"
	if descending {
		order = "DESC"
	}
	rows, err := query.QueryContext(ctx, assistantMessageSelect+`
		WHERE m.session_id = ? ORDER BY m.created_at `+order+`, m.id `+order+` LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []domain.AssistantMessage{}
	for rows.Next() {
		message, err := scanAssistantMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *SQLite) AssistantMessages(ctx context.Context, userID, sessionID string) ([]domain.AssistantMessage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := getAssistantSession(ctx, tx, userID, sessionID); err != nil {
		return nil, err
	}
	return listAssistantMessages(ctx, tx, sessionID, 0, domain.AssistantMessageLimit, false)
}
