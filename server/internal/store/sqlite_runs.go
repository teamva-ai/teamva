package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const sqliteRunSelect = `
	SELECT runs.id, runs.version, runs.created_at, runs.updated_at, runs.deleted_at,
	       COALESCE(runs.issue_id, ''), COALESCE(runs.issue_agent_instance_id, ''),
	       COALESCE(runs.issue_agent_generation_id, ''), COALESCE(runs.agent_id, ''),
	       COALESCE(runs.project_id,''), runs.status,
	       COALESCE(runs.agent_service_id, ''),
	       COALESCE(runs.model, ''), runs.read_only, runs.lease_epoch, runs.input,
	       runs.started_at, runs.finished_at, COALESCE(runs.terminal_reason, ''),
	       COALESCE(runs.goal_id, ''), COALESCE(runs.purpose, 'work'),
	       COALESCE(runs.conversation_node_id, ''), COALESCE(runs.assistant_session_id,'')
	FROM runs
`

// Soft-deleting a collaboration container must also hide its historical Run
// endpoints. Global assistant Runs deliberately have neither of these parents.
const sqliteRunLiveParents = `
	AND (runs.issue_id IS NULL OR EXISTS (
		SELECT 1 FROM issues parent_issue
		WHERE parent_issue.id = runs.issue_id AND parent_issue.deleted_at IS NULL
	))
	AND (runs.conversation_node_id IS NULL OR EXISTS (
		SELECT 1 FROM conversation_nodes parent_node
		JOIN conversation_graphs parent_graph ON parent_graph.id = parent_node.graph_id
		WHERE parent_node.id = runs.conversation_node_id
		  AND parent_node.deleted_at IS NULL AND parent_graph.deleted_at IS NULL
	))
`

type runRequestFingerprintKey struct{}

// WithRunRequestFingerprint binds HTTP admission to its canonical user request,
// not the generated prompt (which includes mutable Issue history). The digest
// lives in the existing private Run snapshot, never in the public API.
func WithRunRequestFingerprint(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, runRequestFingerprintKey{}, fingerprint)
}

func sqliteRunRequestFingerprint(ctx context.Context) string {
	value, _ := ctx.Value(runRequestFingerprintKey{}).(string)
	return value
}

func checkSQLiteRunRequestFingerprint(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string) error {
	want := sqliteRunRequestFingerprint(ctx)
	if want == "" {
		return nil
	}
	var saved string
	if err := queryer.QueryRowContext(ctx, `SELECT COALESCE(json_extract(snapshot, '$.request_fingerprint'), '') FROM runs WHERE id = ?`, runID).Scan(&saved); err != nil {
		return err
	}
	// Internal admission has no HTTP request identity. An HTTP caller must not
	// claim it as a verified replay simply by reusing that internal key. Without
	// an original fingerprint there is no safe equivalence check; fail closed.
	if saved != want {
		return domain.Conflict("idempotency_conflict", "幂等键已用于不同的运行请求", nil)
	}
	return nil
}

func scanSQLiteRun(row scanner, item *domain.Run) error {
	var created, updated, input string
	var deleted, started, finished sql.NullString
	var readOnly int
	if err := row.Scan(
		&item.ID, &item.Version, &created, &updated, &deleted, &item.IssueID,
		&item.IssueAgentInstanceID, &item.IssueAgentGenerationID, &item.AgentID,
		&item.ProjectID, &item.Status, &item.AgentServiceID, &item.Model, &readOnly,
		&item.LeaseEpoch, &input, &started, &finished, &item.TerminalReason,
		&item.GoalID, &item.Purpose, &item.ConversationNodeID, &item.AssistantSessionID,
	); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	var err error
	item.StartedAt, err = sqliteParseNullTime(started)
	if err != nil {
		return err
	}
	item.FinishedAt, err = sqliteParseNullTime(finished)
	if err != nil {
		return err
	}
	item.ReadOnly = readOnly != 0
	if err := json.Unmarshal([]byte(input), &item.Input); err != nil {
		return err
	}
	item.Input = publicRunInput(item.Input)
	return nil
}

func (s *SQLite) ListRunEvents(ctx context.Context, userID, runID string) ([]domain.RunEvent, error) {
	page, err := s.ListRunEventPage(ctx, userID, runID, 0, 1000, MaxRunEventPagePayloadBytes)
	return page.Items, err
}

func (s *SQLite) ListRunEventsAfter(ctx context.Context, userID, runID string, after int64, limit int) ([]domain.RunEvent, error) {
	page, err := s.ListRunEventPage(ctx, userID, runID, after, limit, MaxRunEventPagePayloadBytes)
	return page.Items, err
}

func (s *SQLite) ListRunEventPage(
	ctx context.Context, userID, runID string, after int64, limit, maxPayloadBytes int,
) (domain.RunEventPage, error) {
	if err := normalizeRunEventPageRequest(after, limit, maxPayloadBytes); err != nil {
		return domain.RunEventPage{}, err
	}
	if err := requireSQLiteRun(ctx, s.db, userID, runID); err != nil {
		return domain.RunEventPage{}, err
	}
	return listSQLiteRunEventPage(ctx, s.db, runID, after, limit, maxPayloadBytes)
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const sqliteRunEventPageQuery = `
	WITH keyed AS MATERIALIZED (
		SELECT id, sequence, payload_wire_bytes
		FROM run_events INDEXED BY run_events_page_budget
		WHERE run_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT ?
	), budgeted AS MATERIALIZED (
		SELECT id, sequence,
		       row_number() OVER (ORDER BY sequence) AS ordinal,
		       sum(payload_wire_bytes) OVER (ORDER BY sequence) AS cumulative_bytes
		FROM keyed
	), selected AS MATERIALIZED (
		SELECT id, sequence
		FROM budgeted
		WHERE ordinal <= ? AND cumulative_bytes <= ?
	)
	SELECT event.id, event.run_id, event.sequence, event.event_type,
	       event.payload_wire, event.occurred_at,
	       (SELECT count(*) FROM budgeted) > (SELECT count(*) FROM selected)
	FROM selected
	CROSS JOIN run_events event ON event.id = selected.id
	ORDER BY selected.sequence
`

func listSQLiteRunEventPage(
	ctx context.Context, queryer sqliteQueryer, runID string, after int64, limit, maxPayloadBytes int,
) (domain.RunEventPage, error) {
	var firstPayloadBytes int64
	err := queryer.QueryRowContext(ctx, `
		SELECT payload_wire_bytes
		FROM run_events INDEXED BY run_events_page_budget
		WHERE run_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT 1
	`, runID, after).Scan(&firstPayloadBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return finishRunEventPage(nil, false), nil
	}
	if err != nil {
		return domain.RunEventPage{}, err
	}
	if firstPayloadBytes > int64(maxPayloadBytes) {
		return domain.RunEventPage{}, runEventPageItemTooLarge(maxPayloadBytes)
	}
	rows, err := queryer.QueryContext(ctx, sqliteRunEventPageQuery,
		runID, after, limit+1, limit, maxPayloadBytes)
	if err != nil {
		return domain.RunEventPage{}, err
	}
	defer rows.Close()
	items := make([]domain.RunEvent, 0, limit)
	truncated := false
	for rows.Next() {
		var item domain.RunEvent
		var payload, occurred string
		var truncatedValue int
		if err := rows.Scan(
			&item.ID, &item.RunID, &item.Sequence, &item.Type,
			&payload, &occurred, &truncatedValue,
		); err != nil {
			return domain.RunEventPage{}, err
		}
		truncated = truncatedValue != 0
		if err := json.Unmarshal([]byte(payload), &item.Payload); err != nil {
			return domain.RunEventPage{}, err
		}
		item.OccurredAt, err = sqliteParseTime(occurred)
		if err != nil {
			return domain.RunEventPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.RunEventPage{}, err
	}
	return finishRunEventPage(items, truncated), nil
}

func requireSQLiteRun(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, runID string) error {
	var exists int
	if err := queryer.QueryRowContext(ctx, `
		SELECT count(*) FROM runs
		LEFT JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = COALESCE(owning_project.computer_id,runs.computer_id)
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.id = ? AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`+sqliteRunLiveParents, runID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.NotFound("运行", runID)
	}
	return nil
}

func (s *SQLite) CreateRun(ctx context.Context, userID, issueID string, expectedVersion int64, idempotencyKey string, input domain.Run) (domain.Run, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.Run{}, false, domain.Invalid("缺少幂等键")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getSQLiteRunByIdempotency(ctx, tx, userID, issueID, "", idempotencyKey); err != nil {
		return domain.Run{}, false, err
	} else if found {
		return existing, true, nil
	}
	item, err := createSQLiteExecutionRun(ctx, tx, sqliteRunCreateInput{
		UserID: userID, IssueID: issueID, ExpectedIssueVersion: expectedVersion,
		CheckIssueVersion: true, IdempotencyKey: idempotencyKey, Run: input,
		UseCurrentAgentRevision: true,
		QueuedPayload:           map[string]any{"issue_id": issueID},
	})
	if err != nil {
		return domain.Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, false, err
	}
	return item, false, nil
}

func (s *SQLite) GetRunByIssueIdempotency(
	ctx context.Context, userID, issueID, idempotencyKey string,
) (domain.Run, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.Run{}, false, domain.Invalid("缺少幂等键")
	}
	if err := requireSQLiteIssueOwner(ctx, s.db, userID, issueID); err != nil {
		return domain.Run{}, false, err
	}
	var item domain.Run
	err := scanSQLiteRun(s.db.QueryRowContext(ctx, sqliteRunSelect+`
		JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.issue_id = ? AND runs.idempotency_key = ?
		  AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, issueID, idempotencyKey, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, false, nil
	}
	if err == nil {
		err = checkSQLiteRunRequestFingerprint(ctx, s.db, item.ID)
	}
	return item, err == nil, err
}

func (s *SQLite) GetRun(ctx context.Context, userID, id string) (domain.Run, error) {
	var item domain.Run
	err := scanSQLiteRun(s.db.QueryRowContext(ctx, sqliteRunSelect+`
		LEFT JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = COALESCE(owning_project.computer_id,runs.computer_id)
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.id = ? AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`+sqliteRunLiveParents, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.NotFound("运行", id)
	}
	return item, err
}

func (s *SQLite) GetRunScope(ctx context.Context, runID string) (RunScope, error) {
	var scope RunScope
	err := s.db.QueryRowContext(ctx, `
		SELECT owning_computer.user_id, COALESCE(runs.computer_id, ''),
		       COALESCE(runs.issue_id, ''), COALESCE(runs.agent_id, ''),
		       COALESCE(runs.issue_agent_instance_id, ''), COALESCE(runs.goal_id, ''),
		       COALESCE(runs.purpose, 'work'), COALESCE(goal.summary_comment_id, ''),
		       COALESCE(runs.conversation_node_id, ''), COALESCE(runs.assistant_session_id,'')
		FROM runs
		LEFT JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = COALESCE(owning_project.computer_id,runs.computer_id)
		LEFT JOIN issue_agent_goals goal ON goal.id = runs.goal_id
		WHERE runs.id = ? AND runs.deleted_at IS NULL
	`, runID).Scan(&scope.UserID, &scope.ComputerID, &scope.IssueID, &scope.AgentID,
		&scope.InstanceID, &scope.GoalID, &scope.RunPurpose, &scope.GoalCommentID, &scope.ConversationNodeID, &scope.AssistantSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunScope{}, domain.NotFound("运行", runID)
	}
	return scope, err
}

func (s *SQLite) ListRunsByInstance(ctx context.Context, userID, instanceID string, limit int) ([]domain.Run, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	page, err := s.ListRunsByInstancePage(ctx, userID, instanceID, "", limit)
	return page.Items, err
}

func (s *SQLite) ListRunsByInstancePage(
	ctx context.Context, userID, instanceID, cursor string, limit int,
) (domain.Page[domain.Run], error) {
	if limit < 1 || limit > 100 {
		return domain.Page[domain.Run]{}, domain.Invalid("limit 必须在 1 到 100 之间")
	}
	if _, err := s.GetIssueAgentInstanceByID(ctx, userID, instanceID); err != nil {
		return domain.Page[domain.Run]{}, err
	}
	cursorTime, cursorID, err := decodeRunPageCursor(cursor)
	if err != nil {
		return domain.Page[domain.Run]{}, err
	}
	query := sqliteRunSelect + `
		JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.issue_agent_instance_id = ? AND owning_computer.user_id = ?
		  AND runs.deleted_at IS NULL AND owning_project.deleted_at IS NULL
		  AND owning_computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
	`
	args := []any{instanceID, userID}
	if cursorID != "" {
		query += ` AND (runs.created_at < ? OR (runs.created_at = ? AND runs.id < ?))`
		cursorValue := sqliteTimeValue(cursorTime)
		args = append(args, cursorValue, cursorValue, cursorID)
	}
	query += ` ORDER BY runs.created_at DESC, runs.id DESC LIMIT ?`
	args = append(args, limit+1)
	items, err := s.listSQLiteRuns(ctx, query, args...)
	if err != nil {
		return domain.Page[domain.Run]{}, err
	}
	return finishRunPage(items, limit), nil
}

// ListRunsByIssue returns the latest N Runs, newest first. It never silently
// returns the oldest slice when an Issue has more historical Runs than limit.
func (s *SQLite) ListRunsByIssue(ctx context.Context, userID, issueID string, limit int) ([]domain.Run, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	if err := requireSQLiteIssueOwner(ctx, s.db, userID, issueID); err != nil {
		return nil, err
	}
	return s.listSQLiteRuns(ctx, sqliteRunSelect+`
		JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.issue_id = ? AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY runs.created_at DESC, runs.id DESC LIMIT ?
	`, issueID, userID, limit)
}

func (s *SQLite) listSQLiteRuns(ctx context.Context, query string, args ...any) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Run, 0)
	for rows.Next() {
		var item domain.Run
		if err := scanSQLiteRun(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func requireSQLiteIssueOwner(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, issueID string) error {
	var exists int
	if err := queryer.QueryRowContext(ctx, `
		SELECT count(*) FROM issues
		JOIN projects owning_project ON owning_project.id = issues.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE issues.id = ? AND owning_computer.user_id = ? AND issues.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, issueID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.NotFound("任务", issueID)
	}
	return nil
}

func (s *SQLite) StopRun(ctx context.Context, userID, id, mode string) (domain.Run, error) {
	if mode != "graceful" && mode != "force" {
		return domain.Run{}, domain.Invalid("mode 必须为 graceful 或 force")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireSQLiteRun(ctx, tx, userID, id); err != nil {
		return domain.Run{}, err
	}
	var item domain.Run
	err = scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+`
		LEFT JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = COALESCE(owning_project.computer_id, runs.computer_id)
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE runs.id = ? AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.NotFound("运行", id)
	}
	if err != nil {
		return domain.Run{}, err
	}
	if domain.IsRunTerminal(item.Status) {
		return item, nil
	}
	now := time.Now().UTC()
	if item.GoalID != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_agent_goals
			SET status = 'paused', status_reason = 'paused after stop',
			    version = version + 1, updated_at = ?
			WHERE id = ? AND status = 'active'
		`, sqliteTimeValue(now), item.GoalID); err != nil {
			return domain.Run{}, err
		}
		if item.IssueAgentInstanceID != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE issue_agent_instances SET status = 'paused', version = version + 1, updated_at = ?
				WHERE id = ? AND deleted_at IS NULL
			`, sqliteTimeValue(now), item.IssueAgentInstanceID); err != nil {
				return domain.Run{}, err
			}
		}
	}
	if err := requestSQLiteGoalRunStopTx(ctx, tx, item.ID, mode, now); err != nil {
		return domain.Run{}, err
	}
	if err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, id), &item); err != nil {
		return domain.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, err
	}
	return item, nil
}

func (s *SQLite) SteerRun(ctx context.Context, userID, id, idempotencyKey string, input []domain.ContentBlock, internal bool) (domain.Run, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.Run{}, domain.Invalid("缺少幂等键")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireSQLiteRun(ctx, tx, userID, id); err != nil {
		return domain.Run{}, err
	}
	var item domain.Run
	err = scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+`
		LEFT JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = COALESCE(owning_project.computer_id, runs.computer_id)
		WHERE runs.id = ? AND owning_computer.user_id = ? AND runs.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.NotFound("运行", id)
	}
	if err != nil {
		return domain.Run{}, err
	}
	prompt := contentBlockText(input)
	attachments, err := validateRunInput(input)
	if err != nil {
		return domain.Run{}, err
	}
	if prompt == "" && len(attachments) == 0 {
		return domain.Run{}, domain.Invalid("干预内容不能为空")
	}
	var computerID, provider string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(computer_id, ''), COALESCE(provider, '') FROM runs WHERE id = ?`, id).Scan(&computerID, &provider); err != nil {
		return domain.Run{}, err
	}
	if computerID == "" || provider == "" {
		return domain.Run{}, domain.Conflict("run_not_steerable", "运行尚未连接到本机 Provider", nil)
	}
	payload := nodeprotocol.SteerRunPayload{RunID: id, LeaseEpoch: item.LeaseEpoch, Provider: provider, Prompt: prompt, Attachments: attachments}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Run{}, err
	}
	fingerprintInput, err := json.Marshal(struct {
		Payload  nodeprotocol.SteerRunPayload
		Internal bool
	}{payload, internal})
	if err != nil {
		return domain.Run{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintInput))
	commandKey := fmt.Sprintf("run.steer:%s:%d:%s", id, item.LeaseEpoch, idempotencyKey)
	var originalPayload string
	var originalInternal bool
	var originalFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT command.payload, COALESCE((SELECT json_extract(event.payload, '$.internal')
		FROM run_events event WHERE event.run_id = ? AND event.event_type = 'user.steer'
		AND json_extract(event.payload, '$.command_id') = command.id LIMIT 1), 0),
		COALESCE((SELECT json_extract(event.payload, '$.request_fingerprint')
		FROM run_events event WHERE event.run_id = ? AND event.event_type = 'user.steer'
		AND json_extract(event.payload, '$.command_id') = command.id LIMIT 1), '')
		FROM command_outbox command WHERE command.idempotency_key = ?
	`, id, id, commandKey).Scan(&originalPayload, &originalInternal, &originalFingerprint)
	if err == nil {
		conflict := originalFingerprint != fingerprint
		if originalFingerprint == "" {
			conflict = originalPayload != string(encoded) || originalInternal != internal
		}
		if conflict {
			return domain.Run{}, domain.Conflict("idempotency_conflict", "幂等键已用于不同的干预内容", nil)
		}
		return item, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, err
	}
	if item.Status != domain.RunStarting && item.Status != domain.RunRunning && item.Status != domain.RunWaiting {
		return domain.Run{}, domain.Conflict("run_not_steerable", "当前运行不接受干预", map[string]any{"status": item.Status})
	}
	if item.ConversationNodeID != "" {
		if err := requireNoConversationGoalTx(ctx, tx, item.ConversationNodeID); err != nil {
			return domain.Run{}, err
		}
	}
	now := time.Now().UTC()
	commandID, err := insertSQLiteOutbox(ctx, tx, computerID, "run.steer", commandKey, payload,
		now.Add(10*time.Minute))
	if err != nil {
		return domain.Run{}, err
	}
	attachmentMetadata := runAttachmentMetadata(attachments)
	if err := insertSQLiteRunEvent(ctx, tx, id, item.LeaseEpoch, "user.steer", map[string]any{
		"text": prompt, "attachments": attachmentMetadata, "internal": internal, "command_id": commandID,
		"request_fingerprint": fingerprint,
	}, now); err != nil {
		return domain.Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET version = version + 1, updated_at = ? WHERE id = ?`, sqliteTimeValue(now), id); err != nil {
		return domain.Run{}, err
	}
	if err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, id), &item); err != nil {
		return domain.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, err
	}
	return item, nil
}

// ClaimNextIssueMention atomically leases one available mention. Expired
// leases are fenced by replacing their token before another worker receives
// the row, so a slow prior worker cannot consume the new owner's claim.
func (s *SQLite) ClaimNextIssueMention(ctx context.Context, now time.Time, lease time.Duration) (IssueMentionClaim, bool, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueMentionClaim{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	nowValue := sqliteTimeValue(now)
	// Soft-deleted ownership chains can never become runnable. Dead-letter them
	// instead of leaving invisible rows at the head of the queue forever.
	if _, err := tx.ExecContext(ctx, `
		UPDATE issue_comment_mentions AS mention
		SET status = 'dead_letter', last_error = 'mention_target_deleted',
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE mention.target_kind = 'agent' AND mention.status IN ('pending','processing')
		  AND NOT EXISTS (
		      SELECT 1 FROM issue_comments comment
		      JOIN issues issue ON issue.id = comment.issue_id
		      JOIN projects project ON project.id = issue.project_id
		      JOIN computers computer ON computer.id = project.computer_id
		      JOIN users owning_user ON owning_user.id = computer.user_id
		      JOIN agents agent ON agent.id = mention.target_agent_id
		      WHERE comment.id = mention.comment_id AND comment.deleted_at IS NULL
		        AND issue.deleted_at IS NULL AND project.deleted_at IS NULL
		        AND computer.deleted_at IS NULL AND owning_user.deleted_at IS NULL
		        AND agent.deleted_at IS NULL
		  )
	`); err != nil {
		return IssueMentionClaim{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET status = 'pending', available_at = ?, last_error = 'mention_lease_expired',
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE target_kind = 'agent' AND status = 'processing' AND lease_until <= ?
	`, nowValue, nowValue); err != nil {
		return IssueMentionClaim{}, false, err
	}
	var claim IssueMentionClaim
	err = tx.QueryRowContext(ctx, `
		SELECT issue.id, computer.user_id, mention.target_agent_id, mention.id, comment.id,
		       mention.attempts
		FROM issue_comment_mentions mention
		JOIN issue_comments comment ON comment.id = mention.comment_id
		JOIN issues issue ON issue.id = comment.issue_id
		JOIN projects project ON project.id = issue.project_id
		JOIN computers computer ON computer.id = project.computer_id
		JOIN users owning_user ON owning_user.id = computer.user_id
		JOIN agents agent ON agent.id = mention.target_agent_id
		WHERE mention.target_kind = 'agent' AND mention.status = 'pending'
		  AND mention.available_at <= ? AND mention.consumed_at IS NULL
		  AND comment.deleted_at IS NULL AND issue.deleted_at IS NULL
		  AND issue.status = 'open'
		  AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL AND agent.deleted_at IS NULL
		ORDER BY mention.available_at, comment.created_at, mention.id LIMIT 1
	`, nowValue).Scan(&claim.IssueID, &claim.UserID, &claim.AgentID, &claim.MentionID,
		&claim.CommentID, &claim.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return IssueMentionClaim{}, false, nil
	}
	if err != nil {
		return IssueMentionClaim{}, false, err
	}
	claim.ClaimToken = newID("mclaim")
	result, err := tx.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET status = 'processing', attempts = attempts + 1, claimed_at = ?,
		    lease_until = ?, claim_token = ?, last_error = NULL
		WHERE id = ? AND status = 'pending' AND available_at <= ?
	`, nowValue, sqliteTimeValue(now.Add(lease)), claim.ClaimToken, claim.MentionID, nowValue)
	if err != nil {
		return IssueMentionClaim{}, false, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return IssueMentionClaim{}, false, nil
	}
	claim.Attempts++
	if err := tx.Commit(); err != nil {
		return IssueMentionClaim{}, false, err
	}
	return claim, true, nil
}

func (s *SQLite) CreateRunForMention(
	ctx context.Context, userID, issueID, agentID, mentionID string,
	input []domain.ContentBlock, idempotencyKey string,
) (domain.Run, error) {
	return s.createRunForMention(ctx, userID, issueID, agentID, mentionID, "", input, idempotencyKey)
}

func (s *SQLite) CreateRunForClaimedMention(
	ctx context.Context, claim IssueMentionClaim, input []domain.ContentBlock, idempotencyKey string,
) (domain.Run, error) {
	return s.createRunForMention(ctx, claim.UserID, claim.IssueID, claim.AgentID,
		claim.MentionID, claim.ClaimToken, input, idempotencyKey)
}

func (s *SQLite) createRunForMention(
	ctx context.Context, userID, issueID, agentID, mentionID, claimToken string,
	input []domain.ContentBlock, idempotencyKey string,
) (domain.Run, error) {
	if err := validateTextOnlyRunInput(input); err != nil {
		return domain.Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, err := createSQLiteRunForMentionTx(ctx, tx, userID, issueID, agentID, mentionID, claimToken, input, idempotencyKey)
	if err != nil {
		return domain.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, err
	}
	return item, nil
}

// Used by both ordinary mention dispatch and atomic scheduled activations.
func createSQLiteRunForMentionTx(ctx context.Context, tx *sql.Tx, userID, issueID, agentID, mentionID, claimToken string, input []domain.ContentBlock, idempotencyKey string) (domain.Run, error) {
	if existing, found, err := getSQLiteRunByIdempotency(ctx, tx, userID, issueID, "", idempotencyKey); err != nil {
		return domain.Run{}, err
	} else if found {
		return existing, nil
	}
	var issueVersion int64
	var consumed sql.NullString
	var mentionStatus, currentClaimToken, issueStatus, commentID string
	err := tx.QueryRowContext(ctx, `
		SELECT issue.version, mention.consumed_at, mention.status, COALESCE(mention.claim_token, ''), issue.status, comment.id
		FROM issue_comment_mentions mention
		JOIN issue_comments comment ON comment.id = mention.comment_id
		JOIN issues issue ON issue.id = comment.issue_id
		JOIN projects project ON project.id = issue.project_id
		JOIN computers computer ON computer.id = project.computer_id
		WHERE mention.id = ? AND issue.id = ? AND mention.target_kind = 'agent'
		  AND mention.target_agent_id = ? AND computer.user_id = ?
		  AND comment.deleted_at IS NULL AND issue.deleted_at IS NULL
		  AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
	`, mentionID, issueID, agentID, userID).Scan(
		&issueVersion, &consumed, &mentionStatus, &currentClaimToken, &issueStatus, &commentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.NotFound("Agent mention", mentionID)
	}
	if err != nil {
		return domain.Run{}, err
	}
	if consumed.Valid {
		return domain.Run{}, domain.Conflict("mention_consumed", "该 mention 已消费", nil)
	}
	if claimToken != "" {
		if mentionStatus != "processing" || currentClaimToken != claimToken {
			return domain.Run{}, domain.Conflict("mention_claim_lost", "mention 认领已失效", nil)
		}
	} else if mentionStatus != "pending" && mentionStatus != "processing" {
		return domain.Run{}, domain.Conflict("mention_not_pending", "mention 不可消费", nil)
	}
	// Recheck after leasing: the user may have closed the Issue between the
	// claim and this transaction. Do not consume its durable pending work.
	if err := requireSQLiteIssueOpen(issueStatus); err != nil {
		return domain.Run{}, err
	}
	batch, workIndex, preparedInput, err := prepareSQLiteWorkMentionTx(ctx, tx, issueID, commentID, mentionID, input)
	if err != nil {
		return domain.Run{}, err
	}
	input = preparedInput
	instanceID, err := createSQLiteMentionInstance(ctx, tx, userID, issueID, agentID)
	if err != nil {
		return domain.Run{}, err
	}
	workingCommentID := newID("cmt")
	input = withWorkingReplyID(input, workingCommentID)
	consumeQuery := `
		UPDATE issue_comment_mentions
		SET consumed_at = ?, status = 'completed', claimed_at = NULL,
		    lease_until = NULL, claim_token = NULL, last_error = NULL
		WHERE id = ? AND consumed_at IS NULL`
	consumeArgs := []any{sqliteTimeValue(time.Now()), mentionID}
	if claimToken != "" {
		consumeQuery += ` AND status = 'processing' AND claim_token = ?`
		consumeArgs = append(consumeArgs, claimToken)
	} else {
		consumeQuery += ` AND status IN ('pending','processing')`
	}
	result, err := tx.ExecContext(ctx, consumeQuery, consumeArgs...)
	if err != nil {
		return domain.Run{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return domain.Run{}, domain.Conflict("mention_consumed", "该 mention 已消费", nil)
	}
	item, err := createSQLiteExecutionRun(ctx, tx, sqliteRunCreateInput{
		UserID: userID, IssueID: issueID, ExpectedIssueVersion: issueVersion,
		CheckIssueVersion: true, IdempotencyKey: idempotencyKey,
		Run: domain.Run{
			IssueAgentInstanceID: instanceID, AgentID: agentID,
			Input: input, ReadOnly: false,
		},
		QueuedPayload: map[string]any{"source": "mention", "mention_id": mentionID},
	})
	if err != nil {
		return domain.Run{}, err
	}
	now := sqliteTimeValue(time.Now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_comments (
			id, issue_id, author_kind, author_agent_id, body, run_id, status,
			version, created_at, updated_at
		) VALUES (?, ?, 'agent', ?, '[teamva:working]', ?, 'working', 1, ?, ?)
	`, workingCommentID, issueID, agentID, item.ID, now, now); err != nil {
		return domain.Run{}, err
	}
	if batch != nil {
		batch.Items[workIndex].RunID = item.ID
		batch.Items[workIndex].IssueAgentInstanceID = instanceID
		if err := saveSQLiteIssueWorkBatchTx(ctx, tx, *batch); err != nil {
			return domain.Run{}, err
		}
	}
	return item, nil
}

func (s *SQLite) RetryLocalIssueMention(ctx context.Context, mentionID, claimToken, lastError string, availableAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET status = 'pending', available_at = ?, last_error = ?,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE id = ? AND status = 'processing' AND claim_token = ?
	`, sqliteTimeValue(availableAt), lastError, mentionID, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("local mention claim is no longer owned")
	}
	return nil
}

func (s *SQLite) RetryIssueMention(
	ctx context.Context, claim IssueMentionClaim, lastError string, availableAt time.Time,
) error {
	return s.RetryLocalIssueMention(ctx, claim.MentionID, claim.ClaimToken, lastError, availableAt)
}

func (s *SQLite) DeadLetterLocalIssueMention(ctx context.Context, mentionID, claimToken, lastError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE issue_comment_mentions
		SET status = 'dead_letter', last_error = ?, claimed_at = NULL,
		    lease_until = NULL, claim_token = NULL
		WHERE id = ? AND status = 'processing' AND claim_token = ?
	`, lastError, mentionID, claimToken)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("local mention claim is no longer owned")
	}
	if lastError == "queue_cleared" {
		var userID, issueID string
		if err := tx.QueryRowContext(ctx, `SELECT computer.user_id,comment.issue_id
			FROM issue_comment_mentions mention JOIN issue_comments comment ON comment.id=mention.comment_id
			JOIN issues issue ON issue.id=comment.issue_id JOIN projects project ON project.id=issue.project_id
			JOIN computers computer ON computer.id=project.computer_id WHERE mention.id=?`, mentionID).Scan(&userID, &issueID); err != nil {
			return err
		}
		// An explicit queue clear also fences work still waiting without mentions.
		if err := clearSQLiteIssueWorkItemsTx(ctx, tx, userID, issueID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) DeadLetterIssueMention(
	ctx context.Context, claim IssueMentionClaim, lastError string,
) error {
	return s.DeadLetterLocalIssueMention(ctx, claim.MentionID, claim.ClaimToken, lastError)
}

// createSQLiteMentionInstance creates one fresh conversation for exactly one
// structured @ mention. It intentionally never looks up an older (issue,agent)
// instance: follow-up messages carry an explicit instance ID instead.
func createSQLiteMentionInstance(ctx context.Context, tx *sql.Tx, userID, issueID, agentID string) (string, error) {
	var revisionID string
	err := tx.QueryRowContext(ctx, `
		SELECT agent.current_revision_id
		FROM agents agent
		JOIN issues issue ON issue.id = ?
		JOIN projects project ON project.id = issue.project_id AND project.computer_id = agent.computer_id
		JOIN computers computer ON computer.id = project.computer_id
		WHERE agent.id = ? AND computer.user_id = ? AND agent.status = 'active'
		  AND agent.deleted_at IS NULL AND agent.current_revision_id IS NOT NULL
		  AND issue.deleted_at IS NULL AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
	`, issueID, agentID, userID).Scan(&revisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.NotFound("Agent", agentID)
	}
	if err != nil {
		return "", err
	}
	instanceID := newID("iai")
	now := sqliteTimeValue(time.Now())
	_, err = tx.ExecContext(ctx, `
		INSERT INTO issue_agent_instances (
			id, issue_id, agent_id, agent_revision_id, status, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'idle', 1, ?, ?)
	`, instanceID, issueID, agentID, revisionID, now, now)
	return instanceID, err
}

type sqliteRunCreateInput struct {
	UserID               string
	IssueID              string
	ProjectID            string
	ExpectedIssueVersion int64
	CheckIssueVersion    bool
	IdempotencyKey       string
	ConversationNodeID   string
	Run                  domain.Run
	QueuedPayload        map[string]any
	AgentRevisionID      string
	// Ordinary follow-ups refresh only the explicitly selected idle instance.
	// Goals and other explicitly frozen revisions must never follow Agent edits.
	UseCurrentAgentRevision bool
	SystemPromptOverride    *string
	DisableTools            bool
}

func requireSQLiteIssueOpen(status string) error {
	if status != domain.IssueOpen {
		return domain.Conflict("issue_not_open", "任务已结束，请先重新打开任务再继续执行或委派", map[string]any{"status": status})
	}
	return nil
}

func createSQLiteExecutionRun(ctx context.Context, tx *sql.Tx, request sqliteRunCreateInput) (domain.Run, error) {
	input := request.Run
	refreshInstanceRevision := request.UseCurrentAgentRevision && request.AgentRevisionID == "" && input.GoalID == ""
	projectID := request.ProjectID
	worktreeHint := false
	if request.IssueID != "" {
		var version int64
		var hint int
		var status string
		err := tx.QueryRowContext(ctx, `
			SELECT issue.project_id, issue.version, issue.worktree_hint, issue.status
			FROM issues issue
			JOIN projects project ON project.id = issue.project_id
			JOIN computers computer ON computer.id = project.computer_id
			WHERE issue.id = ? AND (? = '' OR computer.user_id = ?)
			  AND issue.deleted_at IS NULL AND project.deleted_at IS NULL AND computer.deleted_at IS NULL
		`, request.IssueID, request.UserID, request.UserID).Scan(
			&projectID, &version, &hint, &status,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, domain.NotFound("任务", request.IssueID)
		}
		if err != nil {
			return domain.Run{}, err
		}
		if request.CheckIssueVersion && version != request.ExpectedIssueVersion {
			return domain.Run{}, versionConflict(version)
		}
		if err := requireSQLiteIssueOpen(status); err != nil {
			return domain.Run{}, err
		}
		worktreeHint = hint != 0
	}
	if projectID == "" {
		return domain.Run{}, domain.Invalid("运行缺少项目")
	}
	if strings.TrimSpace(input.AgentID) == "" {
		return domain.Run{}, &domain.Error{Code: "agent_required", Message: "运行未指定 Agent", HTTPStatus: 422}
	}
	revisionID := request.AgentRevisionID
	if input.IssueAgentInstanceID != "" {
		var instanceIssue, instanceAgent, instanceRevisionID, status string
		err := tx.QueryRowContext(ctx, `
			SELECT issue_id, agent_id, agent_revision_id, status
			FROM issue_agent_instances WHERE id = ? AND deleted_at IS NULL
		`, input.IssueAgentInstanceID).Scan(&instanceIssue, &instanceAgent, &instanceRevisionID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, domain.NotFound("Agent 实例", input.IssueAgentInstanceID)
		}
		if err != nil {
			return domain.Run{}, err
		}
		if instanceIssue != request.IssueID || instanceAgent != input.AgentID {
			return domain.Run{}, domain.NotFound("Agent 实例", input.IssueAgentInstanceID)
		}
		if status == domain.AgentInstancePaused {
			return domain.Run{}, domain.Conflict("agent_instance_paused", "Agent 实例已暂停", nil)
		}
		if input.GoalID == "" {
			var goalID string
			err = tx.QueryRowContext(ctx, `
				SELECT id FROM issue_agent_goals
				WHERE issue_agent_instance_id = ? AND deleted_at IS NULL
				  AND status IN ('active','paused','blocked','budget_reached')
				LIMIT 1
			`, input.IssueAgentInstanceID).Scan(&goalID)
			if err == nil {
				return domain.Run{}, domain.Conflict("goal_already_exists", "请先继续或取消当前会话目标", map[string]any{"goal_id": goalID})
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return domain.Run{}, err
			}
		}
		var activeID string
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM runs WHERE issue_agent_instance_id = ? AND deleted_at IS NULL
			  AND status IN ('queued','starting','running','waiting_approval','cancelling')
			ORDER BY created_at LIMIT 1
		`, input.IssueAgentInstanceID).Scan(&activeID)
		if err == nil {
			return domain.Run{}, domain.Conflict("target_busy", "该 Agent 在此任务中正在执行", map[string]any{"active_run_id": activeID})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, err
		}
		if revisionID == "" {
			revisionID = instanceRevisionID
			if refreshInstanceRevision {
				if err := tx.QueryRowContext(ctx, `
					SELECT COALESCE(current_revision_id, '') FROM agents
					WHERE id = ? AND deleted_at IS NULL
				`, input.AgentID).Scan(&revisionID); errors.Is(err, sql.ErrNoRows) {
					return domain.Run{}, domain.NotFound("Agent", input.AgentID)
				} else if err != nil {
					return domain.Run{}, err
				}
			}
		}
		if input.IssueAgentGenerationID == "" {
			input.IssueAgentGenerationID = newID("iag")
			now := sqliteTimeValue(time.Now())
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO issue_agent_generations (
					id, issue_agent_instance_id, agent_revision_id, version, created_at, updated_at
				) VALUES (?, ?, ?, 1, ?, ?)
			`, input.IssueAgentGenerationID, input.IssueAgentInstanceID, revisionID, now, now); err != nil {
				return domain.Run{}, err
			}
		} else {
			var generationRevision string
			err := tx.QueryRowContext(ctx, `
				SELECT agent_revision_id FROM issue_agent_generations
				WHERE id = ? AND issue_agent_instance_id = ? AND deleted_at IS NULL
			`, input.IssueAgentGenerationID, input.IssueAgentInstanceID).Scan(&generationRevision)
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Run{}, domain.NotFound("Agent 世代", input.IssueAgentGenerationID)
			}
			if err != nil {
				return domain.Run{}, err
			}
			if generationRevision != revisionID {
				return domain.Run{}, domain.NotFound("Agent 世代", input.IssueAgentGenerationID)
			}
		}
	}

	if revisionID != "" {
		var belongs int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM agent_revisions
			WHERE id = ? AND agent_id = ? AND deleted_at IS NULL
		`, revisionID, input.AgentID).Scan(&belongs); err != nil {
			return domain.Run{}, err
		}
		if belongs == 0 {
			return domain.Run{}, domain.NotFound("Agent 修订", revisionID)
		}
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(current_revision_id, '') FROM agents WHERE id = ?`, input.AgentID).Scan(&revisionID); err != nil {
			return domain.Run{}, err
		}
	}
	var serviceID, provider, computerID, model, systemPrompt string
	var computerUseEnabled bool
	err := tx.QueryRowContext(ctx, `
		SELECT service.id, service.provider, service.computer_id,
		       COALESCE(revision.model, ''), COALESCE(revision.system_prompt, ''),
		       computer.computer_use_enabled
		FROM agents agent
		JOIN agent_revisions revision ON revision.id = ? AND revision.agent_id = agent.id
		JOIN agent_services service ON service.id = revision.agent_service_id
		JOIN projects project ON project.id = ? AND project.computer_id = service.computer_id
		JOIN computers computer ON computer.id = service.computer_id
		WHERE agent.id = ? AND agent.status = 'active' AND agent.deleted_at IS NULL
		  AND revision.deleted_at IS NULL AND service.status = 'ready' AND service.deleted_at IS NULL
		  AND project.deleted_at IS NULL AND computer.deleted_at IS NULL AND computer.status <> 'revoked'
	`, revisionID, projectID, input.AgentID).Scan(&serviceID, &provider, &computerID, &model, &systemPrompt, &computerUseEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, &domain.Error{Code: "runtime_unavailable", Message: "Agent Adapter 当前不可用", HTTPStatus: 503, Retryable: true}
	}
	if err != nil {
		return domain.Run{}, err
	}
	if request.SystemPromptOverride != nil {
		systemPrompt = *request.SystemPromptOverride
	}
	gitResources, err := sqliteRunGitResources(ctx, tx, projectID)
	if err != nil {
		return domain.Run{}, err
	}
	skills, err := sqliteRevisionSkills(ctx, tx, revisionID)
	if err != nil {
		return domain.Run{}, err
	}
	attachments, err := validateRunInput(input.Input)
	if err != nil {
		return domain.Run{}, err
	}
	if request.IssueID != "" && !input.ReadOnly && strings.TrimSpace(contentBlockText(input.Input)) == "" && len(attachments) == 0 {
		return domain.Run{}, domain.Invalid("运行输入不能为空")
	}
	inputJSON, err := json.Marshal(publicRunInput(input.Input))
	if err != nil {
		return domain.Run{}, err
	}
	runID := newID("run")
	purpose := input.Purpose
	if purpose == "" {
		purpose = domain.RunPurposeWork
	}
	now := time.Now().UTC()
	snapshotValues := map[string]string{"agent_revision_id": revisionID, "work_record_id": domain.WorkOperationID(ctx)}
	if fingerprint := sqliteRunRequestFingerprint(ctx); fingerprint != "" {
		snapshotValues["request_fingerprint"] = fingerprint
	}
	snapshot, _ := json.Marshal(snapshotValues)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO runs (
			id, issue_id, issue_agent_instance_id, issue_agent_generation_id, agent_id,
			project_id, computer_id, status, agent_service_id, provider, model, read_only,
			lease_epoch, input, idempotency_key, goal_id, purpose, conversation_node_id, snapshot,
			version, created_at, updated_at
		) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, 'queued',
		          ?, ?, NULLIF(?, ''), ?, 1, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, 1, ?, ?)
	`, runID, request.IssueID, input.IssueAgentInstanceID, input.IssueAgentGenerationID,
		input.AgentID, projectID, computerID, serviceID, provider, model, boolInt(input.ReadOnly),
		string(inputJSON), request.IdempotencyKey, input.GoalID, purpose, request.ConversationNodeID, string(snapshot),
		sqliteTimeValue(now), sqliteTimeValue(now))
	if err != nil {
		return domain.Run{}, err
	}
	if request.QueuedPayload == nil {
		request.QueuedPayload = map[string]any{}
	}
	if err := insertSQLiteRunEvent(ctx, tx, runID, 1, "run.queued", request.QueuedPayload, now); err != nil {
		return domain.Run{}, err
	}
	var prompt strings.Builder
	for _, block := range input.Input {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			if prompt.Len() > 0 {
				prompt.WriteString("\n")
			}
			prompt.WriteString(block.Text)
		}
	}
	payload := nodeprotocol.StartRunPayload{
		RunID: runID, IssueID: request.IssueID, ProjectID: projectID, AgentID: input.AgentID,
		IssueAgentInstanceID:   input.IssueAgentInstanceID,
		IssueAgentGenerationID: input.IssueAgentGenerationID,
		ConversationNodeID:     request.ConversationNodeID,
		LeaseEpoch:             1, Provider: provider,
		Prompt: prompt.String(), Attachments: attachments,
		Model: model, SystemPrompt: systemPrompt, Skills: skills,
		WorktreeHint: worktreeHint, ReadOnly: input.ReadOnly, DisableTools: request.DisableTools,
		ComputerUse: computerUseEnabled && !request.DisableTools,
	}
	payload.GitResources = gitResources
	encodedStartPayload, err := marshalStartRunPayload(payload)
	if err != nil {
		return domain.Run{}, err
	}
	if _, err := insertSQLiteOutbox(ctx, tx, computerID, "run.start", "run.start:"+runID,
		json.RawMessage(encodedStartPayload), now.Add(24*time.Hour)); err != nil {
		return domain.Run{}, err
	}
	if input.IssueAgentInstanceID != "" {
		// Commit the new configuration pointer together with the Run, generation
		// and outbox, never on a replay or a failed/busy/paused creation attempt.
		// Frozen Goal execution may legitimately differ from this mutable pointer.
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_agent_instances
			SET status = 'running', last_activity_at = ?, version = version + 1, updated_at = ?,
			    agent_revision_id = CASE WHEN ? THEN ? ELSE agent_revision_id END
			WHERE id = ?
		`, sqliteTimeValue(now), sqliteTimeValue(now), refreshInstanceRevision, revisionID, input.IssueAgentInstanceID); err != nil {
			return domain.Run{}, err
		}
	}
	var item domain.Run
	if err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, runID), &item); err != nil {
		return domain.Run{}, err
	}
	return item, nil
}

func sqliteRunGitResources(ctx context.Context, tx *sql.Tx, projectID string) ([]nodeprotocol.GitResourceRequest, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, COALESCE(local_reference, '')
		FROM project_resources
		WHERE project_id = ? AND kind = 'code_directory' AND status = 'ready'
		  AND git_detected = 1 AND deleted_at IS NULL
		ORDER BY created_at, id
		LIMIT ?
	`, projectID, nodeprotocol.MaxGitResourcesPerRun+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]nodeprotocol.GitResourceRequest, 0)
	for rows.Next() {
		var item nodeprotocol.GitResourceRequest
		if err := rows.Scan(&item.ResourceID, &item.Name, &item.Path); err != nil {
			return nil, err
		}
		if isAbsoluteGitResourcePath(item.Path) {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := validateRunGitResources(items); err != nil {
		return nil, err
	}
	return items, nil
}

func sqliteRevisionSkills(ctx context.Context, tx *sql.Tx, revisionID string) ([]nodeprotocol.SkillPayload, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT package.slug, package.name, package.description,
		       COALESCE(package.source_url, ''), version.content_digest, version.manifest
		FROM agent_revision_skill_packages binding
		JOIN skill_package_versions version ON version.id = binding.skill_package_version_id
		JOIN skill_packages package ON package.id = version.skill_package_id
		WHERE binding.agent_revision_id = ? AND version.deleted_at IS NULL AND package.deleted_at IS NULL
		ORDER BY binding.position
	`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]nodeprotocol.SkillPayload, 0)
	for rows.Next() {
		var item nodeprotocol.SkillPayload
		var manifestJSON string
		if err := rows.Scan(&item.Slug, &item.Name, &item.Description, &item.SourceURL,
			&item.ContentDigest, &manifestJSON); err != nil {
			return nil, err
		}
		var manifest map[string]any
		if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getSQLiteRunByIdempotency(ctx context.Context, tx *sql.Tx, userID, issueID, projectID, key string) (domain.Run, bool, error) {
	var item domain.Run
	query := sqliteRunSelect + `
		JOIN projects owning_project ON owning_project.id = runs.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		WHERE runs.idempotency_key = ? AND (? = '' OR owning_computer.user_id = ?)
	`
	args := []any{key, userID, userID}
	if issueID != "" {
		query += ` AND runs.issue_id = ?`
		args = append(args, issueID)
	} else {
		query += ` AND runs.issue_id IS NULL AND runs.project_id = ?`
		args = append(args, projectID)
	}
	err := scanSQLiteRun(tx.QueryRowContext(ctx, query, args...), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, false, nil
	}
	if err == nil {
		err = checkSQLiteRunRequestFingerprint(ctx, tx, item.ID)
	}
	return item, err == nil, err
}

func insertSQLiteRunEvent(ctx context.Context, tx *sql.Tx, runID string, leaseEpoch int64, eventType string, payload map[string]any, occurred time.Time) error {
	if payload == nil {
		payload = map[string]any{}
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?
	`, runID).Scan(&sequence); err != nil {
		return err
	}
	event := nodeprotocol.RunEvent{
		ID: newID("evt"), RunID: runID, LeaseEpoch: leaseEpoch, Sequence: sequence,
		Type: eventType, Payload: payload, OccurredAt: occurred.UTC(),
	}
	wire, err := EncodeRunEventWire(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO run_events (
			id, run_id, lease_epoch, sequence, event_type, payload,
			payload_wire, event_wire_prefix, event_wire_suffix,
			payload_wire_bytes, event_wire_bytes, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, runID, leaseEpoch, sequence, eventType, string(wire.Payload),
		string(wire.Payload), string(wire.Prefix), string(wire.Suffix),
		wire.PayloadBytes, wire.EventBytes, sqliteTimeValue(event.OccurredAt))
	return MapRunEventWriteError(err)
}

func isSQLiteUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

// LocalRun is an internal dispatcher lookup; browser handlers use GetRun with
// explicit ownership. Provider selection is already persisted in the outbox.
func (s *SQLite) LocalRun(ctx context.Context, runID string) (domain.Run, error) {
	var item domain.Run
	err := scanSQLiteRun(s.db.QueryRowContext(ctx, sqliteRunSelect+`
		WHERE runs.id = ?
	`, runID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.NotFound("运行", runID)
	}
	if err != nil {
		return domain.Run{}, err
	}
	return item, nil
}
