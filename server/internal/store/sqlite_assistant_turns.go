package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Ownership, replay detection, selected references, history and outbox are read
// and written in one transaction. Replay must precede mutable-state checks: a
// completed request stays replayable even after a referenced object is deleted.
func (s *SQLite) SendAssistantMessage(ctx context.Context, userID, sessionID, key string, request domain.AssistantMessageRequest) (domain.Run, error) {
	request.Body = strings.TrimSpace(request.Body)
	if request.Body == "" || !utf8.ValidString(request.Body) || len(request.Body) > domain.AssistantQuestionBytes || key == "" || len(key) > 128 {
		return domain.Run{}, domain.Invalid("问题或幂等标识无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, err
	}
	defer tx.Rollback()
	session, err := getAssistantSession(ctx, tx, userID, sessionID)
	if err != nil {
		return domain.Run{}, err
	}
	selection, err := normalizeAssistantSelection(userID, session.Scope, request.AssistantSelection)
	if err != nil {
		return domain.Run{}, err
	}
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return domain.Run{}, err
	}
	if run, found, err := replayAssistantTurn(ctx, tx, sessionID, key, request.Body, string(selectionJSON)); err != nil || found {
		return run, err
	}
	if err := requireAssistantCapacity(ctx, tx, sessionID); err != nil {
		return domain.Run{}, err
	}
	references, err := readAssistantReferences(ctx, tx, userID, selection)
	if err != nil {
		return domain.Run{}, err
	}
	history, err := assistantPromptHistory(ctx, tx, sessionID)
	if err != nil {
		return domain.Run{}, err
	}
	provider, err := assistantProvider(ctx, tx, session.AgentID)
	if err != nil {
		return domain.Run{}, err
	}
	prompt, err := buildAssistantPrompt(history, selectionJSON, references, request.Body)
	if err != nil {
		return domain.Run{}, err
	}
	run, err := queueAssistantTurn(ctx, tx, session, provider, key, request.Body, prompt, selectionJSON)
	if err != nil {
		return domain.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, err
	}
	return run, nil
}

func normalizeAssistantSelection(userID string, scope domain.AssistantScope, selection domain.AssistantSelection) (domain.AssistantSelection, error) {
	if err := scope.Validate(); err != nil {
		return selection, err
	}
	if len(selection.SelectedIDs) > 100 || len(selection.InspirationIDs) > 100 {
		return selection, domain.Invalid("选中记录过多")
	}
	if _, _, err := workWhere(selection.Query); err != nil {
		return selection, err
	}
	if scope == domain.AssistantWorkHistory && (len(selection.InspirationIDs) != 0 || selection.InspirationQuery != nil) {
		return selection, domain.Invalid("工作记录会话不接受灵感查询范围")
	}
	if scope == domain.AssistantInspirations && (len(selection.SelectedIDs) != 0 || selection.Query != (domain.WorkQuery{})) {
		return selection, domain.Invalid("灵感会话不接受工作记录查询范围")
	}
	if selection.InspirationQuery != nil {
		if _, _, err := inspirationWhere(userID, *selection.InspirationQuery); err != nil {
			return selection, err
		}
	}
	canonicalIDs := func(ids []string) ([]string, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		copy := slices.Clone(ids)
		for _, id := range copy {
			if id == "" || len(id) > 150 {
				return nil, domain.Invalid("选中记录标识无效")
			}
		}
		slices.Sort(copy)
		return slices.Compact(copy), nil
	}
	var err error
	selection.SelectedIDs, err = canonicalIDs(selection.SelectedIDs)
	if err != nil {
		return selection, err
	}
	selection.InspirationIDs, err = canonicalIDs(selection.InspirationIDs)
	return selection, err
}

func replayAssistantTurn(ctx context.Context, tx *sql.Tx, sessionID, key, body, selection string) (domain.Run, bool, error) {
	var runID, originalBody, originalSelection string
	err := tx.QueryRowContext(ctx, `
		SELECT r.id, m.body, m.query FROM runs r
		JOIN assistant_messages m ON m.run_id = r.id AND m.author_kind = 'user'
		WHERE r.assistant_session_id = ? AND r.idempotency_key = ?`, sessionID, key).Scan(&runID, &originalBody, &originalSelection)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, false, nil
	}
	if err != nil {
		return domain.Run{}, false, err
	}
	if originalBody != body || originalSelection != selection {
		return domain.Run{}, false, domain.Conflict("idempotency_conflict", "幂等键已用于不同的问题或查询范围", nil)
	}
	var run domain.Run
	err = scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, runID), &run)
	return run, true, err
}

func requireAssistantCapacity(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE assistant_session_id = ?
		AND status IN ('queued','starting','running','waiting_approval','cancelling'))`, sessionID).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return domain.Conflict("target_busy", "助手正在回答，请等待或停止", nil)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assistant_messages WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return err
	}
	if count+2 > domain.AssistantMessageLimit {
		return domain.Invalid("此会话已达上限，请新建会话")
	}
	return nil
}

type inspirationReference struct {
	ID        string `json:"id"`
	Version   int64  `json:"version"`
	Title     string `json:"title"`
	ProjectID string `json:"project_id"`
}

func readAssistantReferences(ctx context.Context, tx *sql.Tx, userID string, selection domain.AssistantSelection) ([]inspirationReference, error) {
	for _, id := range selection.SelectedIDs {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_records WHERE id = ? AND user_id = ?)`, id, userID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, domain.NotFound("工作记录", id)
		}
	}
	references := make([]inspirationReference, 0, len(selection.InspirationIDs))
	for _, id := range selection.InspirationIDs {
		var reference inspirationReference
		err := tx.QueryRowContext(ctx, `SELECT id,version,title,COALESCE(project_id,'') FROM inspirations
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&reference.ID, &reference.Version, &reference.Title, &reference.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NotFound("灵感", id)
		}
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, nil
}

func assistantPromptHistory(ctx context.Context, tx *sql.Tx, sessionID string) ([]domain.AssistantMessage, error) {
	var bytes int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(body AS BLOB))),0) FROM (
		SELECT body FROM assistant_messages WHERE session_id = ? ORDER BY created_at DESC,id DESC LIMIT ?
	)`, sessionID, domain.AssistantContextMessages).Scan(&bytes)
	if err != nil {
		return nil, err
	}
	if bytes > domain.AssistantContextBytes {
		return nil, domain.Invalid("会话上下文过大，请新建会话")
	}
	messages, err := listAssistantMessages(ctx, tx, sessionID, 0, domain.AssistantContextMessages, true)
	slices.Reverse(messages)
	return messages, err
}

type assistantRunProvider struct{ ComputerID, ServiceID, ProviderID, Model, RevisionID, SystemPrompt string }

func assistantProvider(ctx context.Context, tx *sql.Tx, agentID string) (assistantRunProvider, error) {
	var provider assistantRunProvider
	err := tx.QueryRowContext(ctx, `
		SELECT a.computer_id, s.id, s.provider, COALESCE(v.model,''), v.id, COALESCE(v.system_prompt,'')
		FROM agents a JOIN agent_revisions v ON v.id = a.current_revision_id AND v.agent_id = a.id
		JOIN agent_services s ON s.id = v.agent_service_id AND s.computer_id = a.computer_id
		WHERE a.id = ? AND a.system_role = 'general' AND a.status = 'active'
		  AND s.status = 'ready' AND s.deleted_at IS NULL AND v.deleted_at IS NULL`, agentID).
		Scan(&provider.ComputerID, &provider.ServiceID, &provider.ProviderID, &provider.Model, &provider.RevisionID, &provider.SystemPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		return provider, domain.Invalid("请先配置并登录 Lumi 的 Provider")
	}
	if err != nil {
		return provider, err
	}
	return provider, requireAssistantProvider(provider.ProviderID)
}

func queueAssistantTurn(ctx context.Context, tx *sql.Tx, session domain.AssistantSession, provider assistantRunProvider, key, body, prompt string, selection []byte) (domain.Run, error) {
	runID := newID("run")
	now := time.Now().UTC()
	stamp := sqliteTimeValue(now)
	input, err := json.Marshal([]domain.ContentBlock{{Type: "text", Text: body}})
	if err != nil {
		return domain.Run{}, err
	}
	snapshot, err := json.Marshal(map[string]string{"agent_revision_id": provider.RevisionID, "work_record_id": domain.WorkOperationID(ctx)})
	if err != nil {
		return domain.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO runs(id,agent_id,computer_id,assistant_session_id,project_id,status,
		 agent_service_id,provider,model,read_only,input,idempotency_key,purpose,snapshot,created_at,updated_at)
		VALUES(?,?,?,?,NULL,'queued',?,?,?,1,?,?,'work_assistant',?,?,?)`,
		runID, session.AgentID, provider.ComputerID, session.ID, provider.ServiceID, provider.ProviderID,
		provider.Model, string(input), key, string(snapshot), stamp, stamp)
	if err != nil {
		return domain.Run{}, err
	}
	for _, message := range []struct{ author, body, at string }{
		{"user", body, stamp}, {"agent", "", sqliteTimeValue(now.Add(time.Microsecond))},
	} {
		_, err := tx.ExecContext(ctx, `INSERT INTO assistant_messages(id,session_id,author_kind,body,run_id,query,created_at)
			VALUES(?,?,?,?,?,?,?)`, newID("wam"), session.ID, message.author, message.body, runID, string(selection), message.at)
		if err != nil {
			return domain.Run{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_sessions SET updated_at = ? WHERE id = ?`, stamp, session.ID); err != nil {
		return domain.Run{}, err
	}
	if err := insertSQLiteRunEvent(ctx, tx, runID, 1, "run.queued", map[string]any{"assistant_session_id": session.ID}, now); err != nil {
		return domain.Run{}, err
	}
	skills, err := sqliteRevisionSkills(ctx, tx, provider.RevisionID)
	if err != nil {
		return domain.Run{}, err
	}
	systemPrompt := assistantSystemPrompt(session.Scope)
	if provider.SystemPrompt != "" {
		systemPrompt += "\n\n" + provider.SystemPrompt
	}
	payload := nodeprotocol.StartRunPayload{RunID: runID, AssistantSessionID: session.ID, AgentID: session.AgentID,
		Provider: provider.ProviderID, Model: provider.Model, ReadOnly: true, LeaseEpoch: 1,
		SystemPrompt: systemPrompt, Skills: skills, Prompt: prompt}
	encoded, err := marshalStartRunPayload(payload)
	if err != nil {
		return domain.Run{}, err
	}
	if _, err := insertSQLiteOutbox(ctx, tx, provider.ComputerID, "run.start", "run.start:"+runID, json.RawMessage(encoded), now.Add(24*time.Hour)); err != nil {
		return domain.Run{}, err
	}
	var run domain.Run
	err = scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, runID), &run)
	return run, err
}
