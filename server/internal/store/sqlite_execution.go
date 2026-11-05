package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// ApplyLocalEvents imports only a contiguous runtime sequence for each run.
// The transaction commits all event and projection updates before cursors are
// returned to the dispatcher, so ACK can never get ahead of durable state.
func (s *SQLite) ApplyLocalEvents(ctx context.Context, events []nodeprotocol.RunEvent) ([]nodeprotocol.RunEventCursor, error) {
	if len(events) > nodeprotocol.MaxRunEventsPerBatch {
		return nil, &domain.Error{
			Code: "run_event_batch_too_large", Message: "Run event 批次超过协议大小上限",
			HTTPStatus: 413, Retryable: false,
			Details: map[string]any{"max_events": nodeprotocol.MaxRunEventsPerBatch},
		}
	}
	events = append([]nodeprotocol.RunEvent(nil), events...)
	for index := range events {
		events[index].Payload = nodeprotocol.RedactInlineImageDataURLs(events[index].Payload)
	}
	encodedBatch, err := json.Marshal(nodeprotocol.RunEventBatch{Events: events})
	if err != nil {
		return nil, err
	}
	if len(encodedBatch) > nodeprotocol.MaxRunEventBatchBytes {
		return nil, &domain.Error{
			Code: "run_event_batch_too_large", Message: "Run event 批次超过协议大小上限",
			HTTPStatus: 413, Retryable: false,
			Details: map[string]any{
				"max_events":        nodeprotocol.MaxRunEventsPerBatch,
				"max_payload_bytes": nodeprotocol.MaxRunEventBatchBytes,
			},
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	type eventStream struct {
		runID string
		lease int64
	}
	grouped := make(map[eventStream][]nodeprotocol.RunEvent)
	for _, event := range events {
		if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.RunID) == "" ||
			strings.TrimSpace(event.Type) == "" || event.LeaseEpoch <= 0 || event.Sequence <= 0 ||
			event.OccurredAt.IsZero() {
			return nil, &domain.Error{
				Code: "invalid_run_event", Message: "Run event 元数据无效",
				HTTPStatus: 400, Retryable: false,
			}
		}
		if err := nodeprotocol.ValidateRunEventSize(event); err != nil {
			if errors.Is(err, nodeprotocol.ErrRunEventTooLarge) {
				return nil, &domain.Error{
					Code: "run_event_too_large", Message: "单个 Run event 超过协议大小上限",
					HTTPStatus: 413, Retryable: false,
					Details: map[string]any{
						"max_event_bytes":   nodeprotocol.MaxRunEventBytes,
						"max_payload_bytes": nodeprotocol.MaxRunEventPayloadBytes,
					},
				}
			}
			return nil, err
		}
		key := eventStream{runID: event.RunID, lease: event.LeaseEpoch}
		grouped[key] = append(grouped[key], event)
	}
	streams := make([]eventStream, 0, len(grouped))
	for stream := range grouped {
		streams = append(streams, stream)
	}
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].runID == streams[j].runID {
			return streams[i].lease < streams[j].lease
		}
		return streams[i].runID < streams[j].runID
	})
	cursors := make([]nodeprotocol.RunEventCursor, 0, len(streams))
	for _, stream := range streams {
		runID := stream.runID
		incoming := grouped[stream]
		sort.SliceStable(incoming, func(i, j int) bool { return incoming[i].Sequence < incoming[j].Sequence })
		var run domain.Run
		if err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ? AND runs.deleted_at IS NULL`, runID), &run); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The control DB may legitimately no longer contain a run whose
				// runtime.db still has unacknowledged rows. ACK the contiguous part
				// of the stream from its first available sequence so the orphan can
				// be garbage-collected instead of blocking every later event.
				if cursor := contiguousRuntimeCursor(incoming); cursor > 0 {
					cursors = append(cursors, nodeprotocol.RunEventCursor{RunID: runID, LeaseEpoch: stream.lease, Sequence: cursor})
				}
				continue
			}
			return nil, err
		}
		if stream.lease != run.LeaseEpoch {
			// As with orphans, a stale lease may start above sequence one because
			// an earlier prefix was already ACKed before the lease changed.
			cursor := contiguousRuntimeCursor(incoming)
			if cursor > 0 {
				cursors = append(cursors, nodeprotocol.RunEventCursor{RunID: runID, LeaseEpoch: stream.lease, Sequence: cursor})
			}
			continue
		}
		var persistedCursor int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(runtime_sequence), 0)
			FROM run_events
			WHERE run_id = ? AND lease_epoch = ? AND runtime_sequence IS NOT NULL
		`, runID, run.LeaseEpoch).Scan(&persistedCursor); err != nil {
			return nil, err
		}
		var batchAckCursor int64
		for _, event := range incoming {
			if event.LeaseEpoch != run.LeaseEpoch || event.Sequence <= 0 {
				break
			}
			// ACK is cumulative on the runtime. Once this batch has validated its
			// first row, do not cross a local sequence that was absent from the
			// batch merely because the server already has a later durable cursor.
			if batchAckCursor > 0 && event.Sequence != batchAckCursor+1 {
				break
			}
			occurred := event.OccurredAt.UTC()
			payload := event.Payload
			if event.Type == "run.error" || event.Type == "run.finished" || event.Type == "run.completed" || event.Type == "context.compaction" || payload["willRetry"] == true || payload["kind"] == "error" {
				payload = nodeprotocol.NormalizeDiagnosticPayload(payload)
			}
			if payload == nil {
				payload = map[string]any{}
			}
			event.Payload = payload
			encodedPayload, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			if event.Sequence <= persistedCursor {
				var existingID, existingType, existingPayload, existingOccurredAt string
				err := tx.QueryRowContext(ctx, `
					SELECT id, event_type, payload_wire, occurred_at FROM run_events
					WHERE run_id = ? AND lease_epoch = ? AND runtime_sequence = ?
				`, runID, event.LeaseEpoch, event.Sequence).Scan(
					&existingID, &existingType, &existingPayload, &existingOccurredAt,
				)
				contentMatches := err == nil && existingID == event.ID && existingType == event.Type &&
					bytes.Equal([]byte(existingPayload), encodedPayload) &&
					existingOccurredAt == sqliteTimeValue(occurred)
				if !contentMatches {
					if errors.Is(err, sql.ErrNoRows) {
						return nil, fmt.Errorf("runtime sequence %d for run %s conflicts with imported cursor", event.Sequence, runID)
					}
					if err == nil {
						return nil, fmt.Errorf("runtime event content conflicts at sequence %d for run %s", event.Sequence, runID)
					}
					return nil, err
				}
				batchAckCursor = event.Sequence
				continue
			}
			if event.Sequence != persistedCursor+1 {
				break
			}
			var serverSequence int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?`, runID).Scan(&serverSequence); err != nil {
				return nil, err
			}
			persistedEvent := event
			persistedEvent.LeaseEpoch = run.LeaseEpoch
			persistedEvent.Sequence = serverSequence
			persistedEvent.Payload = payload
			persistedEvent.OccurredAt = occurred
			wire, err := EncodeRunEventWire(persistedEvent)
			if err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO run_events (
					id, run_id, lease_epoch, sequence, runtime_sequence,
					event_type, payload, payload_wire, event_wire_prefix,
					event_wire_suffix, payload_wire_bytes, event_wire_bytes,
					occurred_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO NOTHING
			`, event.ID, runID, run.LeaseEpoch, serverSequence, event.Sequence,
				event.Type, string(wire.Payload), string(wire.Payload),
				string(wire.Prefix), string(wire.Suffix), wire.PayloadBytes,
				wire.EventBytes, sqliteTimeValue(occurred))
			if err != nil {
				return nil, MapRunEventWriteError(err)
			}
			if rows, _ := result.RowsAffected(); rows != 1 {
				return nil, fmt.Errorf("runtime event %s conflicts with an existing event", event.ID)
			}
			if err := applySQLiteRunEventTx(ctx, tx, &run, event, occurred); err != nil {
				return nil, err
			}
			persistedCursor = event.Sequence
			batchAckCursor = event.Sequence
		}
		if batchAckCursor > 0 {
			cursors = append(cursors, nodeprotocol.RunEventCursor{RunID: runID, LeaseEpoch: run.LeaseEpoch, Sequence: batchAckCursor})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cursors, nil
}

func contiguousRuntimeCursor(events []nodeprotocol.RunEvent) int64 {
	if len(events) == 0 || events[0].Sequence <= 0 {
		return 0
	}
	cursor := events[0].Sequence
	lease := events[0].LeaseEpoch
	for _, event := range events[1:] {
		if event.LeaseEpoch != lease || event.Sequence != cursor+1 {
			break
		}
		cursor = event.Sequence
	}
	return cursor
}

func applySQLiteRunEventTx(ctx context.Context, tx *sql.Tx, run *domain.Run, event nodeprotocol.RunEvent, occurred time.Time) error {
	wasTerminal := domain.IsRunTerminal(run.Status)
	if !wasTerminal {
		switch event.Type {
		case "run.started":
			if run.Status != domain.RunCancelling && run.Status != domain.RunWaiting {
				run.Status = domain.RunRunning
			}
			if run.StartedAt == nil {
				run.StartedAt = &occurred
			}
		case "approval.requested":
			if run.Status == domain.RunCancelling {
				break
			}
			pending, err := createSQLiteApprovalFromEventTx(ctx, tx, *run, event, occurred)
			if err != nil {
				return err
			}
			if pending {
				run.Status = domain.RunWaiting
			}
		case "run.completed", "run.finished", "run.error":
			if event.Payload["willRetry"] == true {
				break
			}
			run.Status, run.TerminalReason = sqliteRunTerminalStatus(run.Status, event.Payload)
			run.FinishedAt = &occurred
		}
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, started_at = ?, finished_at = ?, terminal_reason = NULLIF(?, ''),
		    version = version + 1, updated_at = ? WHERE id = ?
	`, run.Status, nullableSQLiteTime(run.StartedAt), nullableSQLiteTime(run.FinishedAt),
		run.TerminalReason, sqliteTimeValue(occurred), run.ID)
	if err != nil {
		return err
	}
	run.Version++
	run.UpdatedAt = occurred
	if !wasTerminal && domain.IsRunTerminal(run.Status) {
		return finishSQLiteRunRelationsTx(ctx, tx, *run, occurred)
	}
	return nil
}

// Project provider terminal payloads into control-plane Run state.
func sqliteRunTerminalStatus(current string, payload map[string]any) (string, string) {
	if current == domain.RunCancelling {
		return domain.RunCancelled, "interrupted"
	}
	status := catalog.TerminalStatus(payload)
	switch status {
	case "completed", "succeeded":
		return domain.RunSucceeded, ""
	case "interrupted", "cancelled":
		return domain.RunCancelled, "interrupted"
	default:
		reason, _ := payload["reason"].(string)
		if reason == "" {
			reason = "provider_failed"
		}
		return domain.RunFailed, reason
	}
}

func finishSQLiteRunRelationsTx(ctx context.Context, tx *sql.Tx, run domain.Run, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE work_records SET outcome=?,finished_at=? WHERE id=(SELECT json_extract(snapshot,'$.work_record_id') FROM runs WHERE id=?)`, run.Status, sqliteTimeValue(now), run.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_records SET outcome=? WHERE kind='change' AND object_type='run' AND object_id=? AND object_version=(SELECT version FROM runs WHERE id=?)`, run.Status, run.ID, run.ID); err != nil {
		return err
	}
	if run.AssistantSessionID != "" {
		text, err := finalAssistantTextSQLiteTx(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE assistant_messages SET body=? WHERE run_id=? AND author_kind='agent'`, text, run.ID); err != nil {
			return err
		}
	}
	if run.ConversationNodeID != "" {
		if err := finishSQLiteConversationRunTx(ctx, tx, run, now); err != nil {
			return err
		}
	}
	// Issue comments are public tool writes, never inferred from model output.
	// An unfulfilled placeholder remains visibly failed even if the Run succeeded.
	if run.GoalID == "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_comments
			SET status = 'failed', version = version + 1, updated_at = ?
			WHERE run_id = ? AND status = 'working' AND deleted_at IS NULL
		`, sqliteTimeValue(now), run.ID); err != nil {
			return err
		}
	}
	// A pending approval cannot outlive its run. Keep this in the same
	// transaction as every terminal run transition so no decision can race in
	// between terminal state and approval cancellation.
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'cancelled', version = version + 1, updated_at = ?
		WHERE run_id = ? AND status = 'pending'
	`, sqliteTimeValue(now), run.ID); err != nil {
		return err
	}
	// Runtime control commands are obsolete once the run is terminal. run.start
	// is deliberately excluded: an in-flight start may be the operation that
	// produced the terminal event and is allowed to record its durable result.
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'failed', completed_at = ?, last_error = 'run_terminal',
		    payload = CASE
		        WHEN command_name = 'run.steer' THEN json_remove(payload, '$.attachments')
		        ELSE payload
		    END,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name IN ('run.stop', 'run.steer', 'approval.decide')
		  AND status IN ('pending', 'processing', 'uncertain')
		  AND json_extract(payload, '$.run_id') = ?
	`, sqliteTimeValue(now), run.ID); err != nil {
		return err
	}
	if run.IssueAgentInstanceID != "" {
		if run.GoalID != "" {
			// A public instance pause can precede the Goal dispatch tick. Its
			// durable intent must survive even a terminal event arriving first.
			if _, err := tx.ExecContext(ctx, `
				UPDATE issue_agent_instances
				SET status = CASE
				        WHEN EXISTS (SELECT 1 FROM issue_agent_goals goal WHERE goal.id = ? AND goal.status = 'active') THEN 'running'
				        WHEN EXISTS (SELECT 1 FROM issue_agent_goals goal WHERE goal.id = ? AND goal.status = 'paused') THEN 'paused'
				        ELSE 'idle'
				    END,
				    last_activity_at = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND deleted_at IS NULL AND status <> 'paused'
			`, run.GoalID, run.GoalID, sqliteTimeValue(now), sqliteTimeValue(now), run.IssueAgentInstanceID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE issue_agent_instances
			SET status = 'idle', last_activity_at = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL AND status <> 'paused'
			  AND NOT EXISTS (
			      SELECT 1 FROM runs active_run
			      WHERE active_run.issue_agent_instance_id = issue_agent_instances.id
			        AND active_run.id <> ? AND active_run.deleted_at IS NULL
			        AND active_run.status IN ('queued','starting','running','waiting_approval','cancelling')
			  )
		`, sqliteTimeValue(now), sqliteTimeValue(now), run.IssueAgentInstanceID, run.ID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE scheduled_task_runs SET status = ? WHERE run_id = ?`, run.Status, run.ID)
	return err
}

func finalAssistantTextSQLiteTx(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH keyed AS MATERIALIZED (
			SELECT id, sequence, payload_wire_bytes
			FROM run_events INDEXED BY run_events_page_budget
			WHERE run_id = ? AND event_type IN ('provider.item', 'provider.event', 'output.delta', 'output.snapshot')
			ORDER BY sequence DESC
			LIMIT ?
		), budgeted AS MATERIALIZED (
			SELECT id, sequence,
			       row_number() OVER (ORDER BY sequence DESC) AS ordinal,
			       sum(payload_wire_bytes) OVER (ORDER BY sequence DESC) AS cumulative_bytes
			FROM keyed
		), selected AS MATERIALIZED (
			SELECT id, sequence
			FROM budgeted
			WHERE ordinal <= ? AND cumulative_bytes <= ?
		)
		SELECT event.event_type, event.payload_wire
		FROM selected
		CROSS JOIN run_events event ON event.id = selected.id
		ORDER BY selected.sequence DESC
	`, runID, MaxTerminalEventScanCount+1, MaxTerminalEventScanCount,
		MaxRunEventPagePayloadBytes)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	events := make([]domain.RunEvent, 0, min(MaxTerminalEventScanCount, 256))
	for rows.Next() {
		var event domain.RunEvent
		var payload string
		if err := rows.Scan(&event.Type, &payload); err != nil {
			return "", err
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return "", err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return FinalAssistantText(events), nil
}

func finishSQLiteRunTx(ctx context.Context, tx *sql.Tx, runID, status, reason string, now time.Time, eventType string, diagnostics ...map[string]any) error {
	var run domain.Run
	err := scanSQLiteRun(tx.QueryRowContext(ctx, sqliteRunSelect+` WHERE runs.id = ?`, runID), &run)
	if errors.Is(err, sql.ErrNoRows) || domain.IsRunTerminal(run.Status) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status == domain.RunCancelling && status == domain.RunFailed {
		// Match native terminal projection: a preparation failure racing an
		// accepted user stop must not turn cancellation back into failure.
		status, reason = domain.RunCancelled, "interrupted"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, terminal_reason = ?, finished_at = ?,
		    version = version + 1, updated_at = ? WHERE id = ?
	`, status, reason, sqliteTimeValue(now), sqliteTimeValue(now), runID); err != nil {
		return err
	}
	payload := map[string]any{"status": status, "reason": reason}
	if len(diagnostics) > 0 {
		// Preserve safe command failure detail only when there is no already-
		// committed native terminal. Status/reason remain the control-plane's.
		for _, key := range []string{"message", "error", "detail", "details", "additionalDetails", "responseBody", "provider_id"} {
			if value, ok := diagnostics[0][key]; ok {
				payload[key] = value
			}
		}
		payload = nodeprotocol.NormalizeDiagnosticPayload(payload)
	}
	if err := insertSQLiteRunEvent(ctx, tx, runID, run.LeaseEpoch, eventType, payload, now); err != nil {
		return err
	}
	run.Status, run.TerminalReason, run.FinishedAt = status, reason, &now
	return finishSQLiteRunRelationsTx(ctx, tx, run, now)
}

func (s *SQLite) FailLocalRun(ctx context.Context, runID, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := finishSQLiteRunTx(ctx, tx, runID, domain.RunFailed, reason, time.Now().UTC(), "run.finished"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) CancelLocalRun(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := finishSQLiteRunTx(ctx, tx, runID, domain.RunCancelled, "cancelled_by_user", time.Now().UTC(), "run.finished"); err != nil {
		return err
	}
	return tx.Commit()
}

const sqliteApprovalSelect = `
	SELECT approvals.id, approvals.version, approvals.created_at, approvals.updated_at,
	       NULL, approvals.project_id, COALESCE(approvals.issue_id, ''), approvals.run_id,
	       approvals.status, approvals.action_kind, approvals.action_digest,
	       approvals.normalized_action, approvals.risk, approvals.expires_at,
	       COALESCE(approvals.decision_scope, ''), approvals.decided_at,
	       COALESCE(approvals.provider_approval_ref, '')
	FROM approvals
`

func scanSQLiteApproval(row scanner, item *domain.Approval) error {
	var created, updated, action, expires string
	var deleted, decided sql.NullString
	if err := row.Scan(
		&item.ID, &item.Version, &created, &updated, &deleted, &item.ProjectID,
		&item.IssueID, &item.RunID, &item.Status, &item.ActionKind, &item.ActionDigest,
		&action, &item.Risk, &expires, &item.DecisionScope, &decided,
		&item.ProviderApprovalRef,
	); err != nil {
		return err
	}
	if err := parseSQLiteVersioned(&item.Versioned, created, updated, deleted); err != nil {
		return err
	}
	var err error
	item.ExpiresAt, err = sqliteParseTime(expires)
	if err != nil {
		return err
	}
	item.DecidedAt, err = sqliteParseNullTime(decided)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(action), &item.NormalizedAction)
}

func (s *SQLite) ListApprovals(ctx context.Context, userID, status string) ([]domain.Approval, error) {
	if _, err := s.ExpireLocalApprovals(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqliteApprovalSelect+`
		JOIN runs owning_run ON owning_run.id = approvals.run_id
		JOIN projects owning_project ON owning_project.id = owning_run.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE owning_computer.user_id = ? AND (? = '' OR approvals.status = ?)
		  AND owning_run.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
		ORDER BY approvals.created_at, approvals.id
	`, userID, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Approval, 0)
	for rows.Next() {
		var item domain.Approval
		if err := scanSQLiteApproval(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) CreateApproval(ctx context.Context, input domain.Approval) (domain.Approval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var status, projectID string
	var issueID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT status, project_id, issue_id FROM runs
		WHERE id = ? AND deleted_at IS NULL
	`, input.RunID).Scan(&status, &projectID, &issueID); errors.Is(err, sql.ErrNoRows) || domain.IsRunTerminal(status) || status == domain.RunCancelling {
		return domain.Approval{}, domain.NotFound("活动运行", input.RunID)
	} else if err != nil {
		return domain.Approval{}, err
	}
	// Project and issue scope are authoritative properties of the run. Never
	// trust a caller-supplied copy that could cross-link an approval.
	input.ProjectID = projectID
	input.IssueID = ""
	if issueID.Valid {
		input.IssueID = issueID.String
	}
	action, err := json.Marshal(input.NormalizedAction)
	if err != nil {
		return domain.Approval{}, err
	}
	now := time.Now().UTC()
	var item domain.Approval
	err = scanSQLiteApproval(tx.QueryRowContext(ctx, `
		INSERT INTO approvals (
			id, project_id, issue_id, run_id, provider_approval_ref, status,
			action_kind, action_digest, normalized_action, risk, expires_at,
			version, created_at, updated_at
		) VALUES (?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), 'pending', ?, ?, ?, ?, ?, 1, ?, ?)
		RETURNING id, version, created_at, updated_at, NULL, project_id,
		          COALESCE(issue_id, ''), run_id, status, action_kind, action_digest,
		          normalized_action, risk, expires_at, COALESCE(decision_scope, ''),
		          decided_at, COALESCE(provider_approval_ref, '')
	`, newID("apr"), input.ProjectID, input.IssueID, input.RunID, input.ProviderApprovalRef,
		input.ActionKind, input.ActionDigest, string(action), input.Risk,
		sqliteTimeValue(input.ExpiresAt), sqliteTimeValue(now), sqliteTimeValue(now)), &item)
	if err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return item, nil
}

func (s *SQLite) GetApproval(ctx context.Context, userID, id string) (domain.Approval, error) {
	if _, err := s.ExpireLocalApprovals(ctx, time.Now().UTC()); err != nil {
		return domain.Approval{}, err
	}
	var item domain.Approval
	err := scanSQLiteApproval(s.db.QueryRowContext(ctx, sqliteApprovalSelect+`
		JOIN runs owning_run ON owning_run.id = approvals.run_id
		JOIN projects owning_project ON owning_project.id = owning_run.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE approvals.id = ? AND owning_computer.user_id = ?
		  AND owning_run.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, domain.NotFound("审批", id)
	}
	return item, err
}

func (s *SQLite) DecideApproval(ctx context.Context, userID, id string, expectedVersion int64, digest, decision, scope string) (domain.Approval, error) {
	if decision != "approved" && decision != "denied" {
		return domain.Approval{}, domain.Invalid("decision 必须为 approved 或 denied")
	}
	if scope == "" {
		scope = "once"
	}
	if scope != "once" && scope != "session" {
		return domain.Approval{}, domain.Invalid("scope 必须为 once 或 session")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current domain.Approval
	err = scanSQLiteApproval(tx.QueryRowContext(ctx, sqliteApprovalSelect+`
		JOIN runs owning_run ON owning_run.id = approvals.run_id
		JOIN projects owning_project ON owning_project.id = owning_run.project_id
		JOIN computers owning_computer ON owning_computer.id = owning_project.computer_id
		JOIN users owning_user ON owning_user.id = owning_computer.user_id
		WHERE approvals.id = ? AND owning_computer.user_id = ?
		  AND owning_run.deleted_at IS NULL
		  AND owning_project.deleted_at IS NULL AND owning_computer.deleted_at IS NULL
		  AND owning_user.deleted_at IS NULL
	`, id, userID), &current)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, domain.NotFound("审批", id)
	}
	if err != nil {
		return domain.Approval{}, err
	}
	var runStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ? AND deleted_at IS NULL`, current.RunID).Scan(&runStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Approval{}, domain.Conflict("approval_expired", "审批关联运行已结束", nil)
		}
		return domain.Approval{}, err
	}
	if domain.IsRunTerminal(runStatus) || runStatus == domain.RunCancelling {
		if _, err := expireSQLiteApprovalsTx(ctx, tx, time.Now().UTC()); err != nil {
			return domain.Approval{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.Approval{}, err
		}
		return domain.Approval{}, domain.Conflict("approval_expired", "审批关联运行已结束", nil)
	}
	now := time.Now().UTC()
	if current.Status != "pending" || !current.ExpiresAt.After(now) {
		// Commit expiration AND the native denial outbox even though this user
		// decision returns 409. Rolling back here would leave Provider waiting.
		if _, err := expireSQLiteApprovalsTx(ctx, tx, now); err != nil {
			return domain.Approval{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.Approval{}, err
		}
		return domain.Approval{}, domain.Conflict("approval_expired", "审批已失效", nil)
	}
	if current.Version != expectedVersion {
		return domain.Approval{}, versionConflict(current.Version)
	}
	if current.ActionDigest != digest {
		return domain.Approval{}, domain.Conflict("approval_action_changed", "动作摘要已变化", nil)
	}
	var item domain.Approval
	err = scanSQLiteApproval(tx.QueryRowContext(ctx, `
		UPDATE approvals SET status = ?, decision_scope = ?, decided_at = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND status = 'pending' AND expires_at > ?
		  AND EXISTS (
		      SELECT 1 FROM runs
		      WHERE runs.id = approvals.run_id AND runs.deleted_at IS NULL
		        AND runs.status IN ('queued','starting','running','waiting_approval')
		  )
		RETURNING id, version, created_at, updated_at, NULL, project_id,
		          COALESCE(issue_id, ''), run_id, status, action_kind, action_digest,
		          normalized_action, risk, expires_at, COALESCE(decision_scope, ''),
		          decided_at, COALESCE(provider_approval_ref, '')
	`, decision, scope, sqliteTimeValue(now), sqliteTimeValue(now), id,
		expectedVersion, sqliteTimeValue(now)), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, domain.Conflict("approval_expired", "审批已失效", nil)
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if err := enqueueSQLiteApprovalDecisionTx(ctx, tx, item, decision, scope, now); err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return item, nil
}

func createSQLiteApprovalFromEventTx(ctx context.Context, tx *sql.Tx, run domain.Run, event nodeprotocol.RunEvent, now time.Time) (bool, error) {
	requestID, _ := event.Payload["request_id"].(string)
	if requestID == "" {
		return false, nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM approvals WHERE run_id = ? AND provider_approval_ref = ?`, run.ID, requestID).Scan(&exists); err != nil || exists > 0 {
		return false, err
	}
	normalized, _ := event.Payload["normalized_action"].(map[string]any)
	if normalized == nil {
		normalized = map[string]any{}
	}
	action, err := json.Marshal(normalized)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(action)
	actionKind, _ := event.Payload["action_kind"].(string)
	if actionKind == "" {
		actionKind = "provider.action"
	}
	risk, _ := event.Payload["risk"].(string)
	if risk == "" {
		risk = "high"
	}
	approvalID := newID("apr")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO approvals (
			id, project_id, issue_id, run_id, provider_approval_ref, status,
			action_kind, action_digest, normalized_action, risk, expires_at,
			decision_scope, decided_at, version, created_at, updated_at
		) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
	`, approvalID, run.ProjectID, run.IssueID, run.ID, requestID, "pending", actionKind,
		"sha256:"+hex.EncodeToString(digest[:]), string(action), risk,
		sqliteTimeValue(now.Add(10*time.Minute)), nil, nil,
		sqliteTimeValue(now), sqliteTimeValue(now))
	if err != nil {
		return false, err
	}
	return true, nil
}

func enqueueSQLiteApprovalDecisionTx(ctx context.Context, tx *sql.Tx, approval domain.Approval, decision, scope string, now time.Time) error {
	var computerID, provider, status string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(computer_id, ''), COALESCE(provider, ''), status
		FROM runs WHERE id = ? AND deleted_at IS NULL
	`, approval.RunID).Scan(&computerID, &provider, &status); err != nil {
		return err
	}
	if domain.IsRunTerminal(status) || status == domain.RunCancelling {
		return domain.Conflict("approval_expired", "审批关联运行已结束", nil)
	}
	if computerID == "" || provider == "" || approval.ProviderApprovalRef == "" {
		return domain.Conflict("approval_not_dispatchable", "审批未绑定活动 Provider 请求", nil)
	}
	payload := nodeprotocol.ApprovalDecisionPayload{RunID: approval.RunID, Provider: provider,
		RequestID: approval.ProviderApprovalRef, Decision: decision, Scope: scope}
	_, err := insertSQLiteOutbox(ctx, tx, computerID, "approval.decide",
		"approval.decide:"+approval.ID+":"+decision, payload, now.Add(10*time.Minute))
	if isSQLiteUnique(err) {
		return nil
	}
	return err
}

// ExpireLocalApprovals durably closes deadlines without ever approving a tool.
// ClaimLocalCommand also runs this in its own transaction, so recovery and an
// idle UI do not depend on somebody opening the approval list.
func (s *SQLite) ExpireLocalApprovals(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	count, err := expireSQLiteApprovalsTx(ctx, tx, now.UTC())
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func expireSQLiteApprovalsTx(ctx context.Context, tx *sql.Tx, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, sqliteApprovalSelect+`
		JOIN runs owning_run ON owning_run.id = approvals.run_id
		WHERE approvals.status = 'pending' AND (
		    approvals.expires_at <= ? OR owning_run.deleted_at IS NOT NULL OR
		    owning_run.status IN ('cancelling','succeeded','failed','cancelled','lost')
		) ORDER BY approvals.expires_at, approvals.id
	`, sqliteTimeValue(now))
	if err != nil {
		return 0, err
	}
	items := make([]domain.Approval, 0)
	for rows.Next() {
		var item domain.Approval
		if err := scanSQLiteApproval(rows, &item); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, item := range items {
		var runStatus string
		var deleted sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT status, deleted_at FROM runs WHERE id = ?`, item.RunID).Scan(&runStatus, &deleted); err != nil {
			return 0, err
		}
		status := "expired"
		if domain.IsRunTerminal(runStatus) || runStatus == domain.RunCancelling || deleted.Valid {
			status = "cancelled"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals SET status = ?, decision_scope = 'once', decided_at = ?,
			    version = version + 1, updated_at = ?
			WHERE id = ? AND status = 'pending'
		`, status, sqliteTimeValue(now), sqliteTimeValue(now), item.ID); err != nil {
			return 0, err
		}
		if status == "expired" && item.ProviderApprovalRef != "" {
			if err := enqueueSQLiteApprovalDecisionTx(ctx, tx, item, "denied", "once", now); err != nil {
				return 0, err
			}
		}
	}
	return len(items), nil
}

func (s *SQLite) TriggerScheduledTask(ctx context.Context, id, triggerKey string, scheduledFor time.Time) (domain.ScheduledTaskRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getSQLiteScheduledRunTx(ctx, tx, id, triggerKey); err != nil {
		return domain.ScheduledTaskRun{}, false, err
	} else if found {
		return existing, true, nil
	}
	task, err := getSQLiteScheduledTaskInternalTx(ctx, tx, id)
	if err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	if !task.Enabled {
		return domain.ScheduledTaskRun{}, false, domain.Conflict("scheduled_task_paused", "计划任务已暂停", nil)
	}
	if err := validateSQLiteScheduleCalendar(task); err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	item, err := triggerSQLiteScheduleTx(ctx, tx, task, triggerKey, scheduledFor.UTC())
	if err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	return item, false, nil
}

func validateSQLiteScheduleCalendar(task domain.ScheduledTask) error {
	// A stored once occurrence may already be due (or disabled after running).
	// Validate its syntax/timezone without changing that existing misfire policy.
	after := time.Now().UTC()
	if task.ScheduleKind == "once" {
		value, err := time.Parse(time.RFC3339, task.ScheduleExpression)
		if err != nil {
			return domain.Invalid("once 表达式必须是 RFC3339 时间")
		}
		after = value.Add(-time.Second)
	}
	_, err := domain.NextScheduledTaskTime(task, after)
	return err
}

func triggerSQLiteScheduleTx(ctx context.Context, tx *sql.Tx, task domain.ScheduledTask, triggerKey string, scheduledFor time.Time) (domain.ScheduledTaskRun, error) {
	if task.AgentID == "" {
		return domain.ScheduledTaskRun{}, domain.Invalid("计划任务未指定 Agent")
	}
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT computer.user_id FROM projects project JOIN computers computer ON computer.id = project.computer_id WHERE project.id = ?`, task.ProjectID).Scan(&ownerID); err != nil {
		return domain.ScheduledTaskRun{}, err
	}
	if err := requireSQLiteScheduleAgent(ctx, tx, ownerID, task.ProjectID, task.AgentID); err != nil {
		return domain.ScheduledTaskRun{}, err
	}
	if task.ConcurrencyPolicy == "forbid" {
		var active int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM scheduled_task_runs task_run
			JOIN runs run ON run.id = task_run.run_id
			WHERE task_run.scheduled_task_id = ?
			  AND run.status IN ('queued','starting','running','waiting_approval','cancelling')
		`, task.ID).Scan(&active); err != nil {
			return domain.ScheduledTaskRun{}, err
		}
		if active > 0 {
			return domain.ScheduledTaskRun{}, domain.Conflict("schedule_concurrency_forbidden", "该计划任务已有活动运行", nil)
		}
	}
	var run domain.Run
	var err error
	if task.IssueMode != "" {
		run, err = createSQLiteScheduledIssueRunTx(ctx, tx, task, triggerKey)
	} else {
		run, err = createSQLiteExecutionRun(ctx, tx, sqliteRunCreateInput{
			ProjectID: task.ProjectID, IdempotencyKey: "schedule:" + task.ID + ":" + triggerKey,
			Run: domain.Run{
				ProjectID: task.ProjectID, AgentID: task.AgentID, Input: task.Prompt,
			},
			QueuedPayload: map[string]any{"source": "schedule", "scheduled_task_id": task.ID,
				"trigger_key": triggerKey, "scheduled_for": scheduledFor},
		})
	}
	if err != nil {
		return domain.ScheduledTaskRun{}, err
	}
	now := time.Now().UTC()
	item := domain.ScheduledTaskRun{ID: newID("str"), ScheduledTaskID: task.ID,
		RunID: run.ID, IssueID: run.IssueID, TriggerKey: triggerKey, ScheduledFor: scheduledFor,
		Status: "running", CreatedAt: now}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO scheduled_task_runs (
			id, scheduled_task_id, run_id, trigger_key, scheduled_for, status, created_at
		) VALUES (?, ?, ?, ?, ?, 'running', ?)
	`, item.ID, item.ScheduledTaskID, item.RunID, item.TriggerKey,
		sqliteTimeValue(item.ScheduledFor), sqliteTimeValue(now))
	return item, err
}

func (s *SQLite) TriggerDueLocalSchedules(ctx context.Context, now time.Time) (int, error) {
	now = now.UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM scheduled_tasks
		WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at IS NOT NULL AND next_run_at <= ?
		  AND (dispatch_retry_at IS NULL OR dispatch_retry_at <= ?)
		ORDER BY next_run_at, id
	`, sqliteTimeValue(now), sqliteTimeValue(now))
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	handled := 0
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return handled, err
		}
		task, err := getSQLiteScheduledTaskInternalTx(ctx, tx, id)
		if err != nil {
			_ = tx.Rollback()
			var domainErr *domain.Error
			if errors.As(err, &domainErr) && domainErr.Code == "not_found" {
				continue
			}
			if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, isPermanentSQLiteQueueError(err), now); markErr != nil {
				return handled, errors.Join(err, markErr)
			}
			handled++
			continue
		}
		if !task.Enabled || task.NextRunAt == nil || task.NextRunAt.After(now) {
			_ = tx.Rollback()
			continue
		}
		scheduledFor := task.NextRunAt.UTC()
		triggerKey := "calendar:" + scheduledFor.Format(time.RFC3339Nano)
		// Validate/advance before materializing any occurrence. In particular a
		// zero Next from an impossible cron is never treated as run_once misfire.
		// Both the advance and occurrence still commit (or roll back) together.
		if err := advanceSQLiteScheduleTx(ctx, tx, task, scheduledFor, now); err != nil {
			_ = tx.Rollback()
			if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, true, now); markErr != nil {
				return handled, errors.Join(err, markErr)
			}
			handled++
			continue
		}
		if _, found, err := getSQLiteScheduledRunTx(ctx, tx, id, triggerKey); err != nil {
			_ = tx.Rollback()
			if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, false, now); markErr != nil {
				return handled, errors.Join(err, markErr)
			}
			handled++
			continue
		} else if !found {
			misfire := task.MisfirePolicy == "skip" && now.Sub(scheduledFor) > time.Minute
			if misfire {
				if err := insertSQLiteSkippedScheduleTx(ctx, tx, task.ID, triggerKey, scheduledFor, "skipped_misfire", now); err != nil {
					_ = tx.Rollback()
					if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, false, now); markErr != nil {
						return handled, errors.Join(err, markErr)
					}
					handled++
					continue
				}
			} else if _, err := triggerSQLiteScheduleTx(ctx, tx, task, triggerKey, scheduledFor); err != nil {
				var domainErr *domain.Error
				if errors.As(err, &domainErr) && domainErr.Code == "schedule_concurrency_forbidden" {
					if err := insertSQLiteSkippedScheduleTx(ctx, tx, task.ID, triggerKey, scheduledFor, "skipped_concurrency", now); err != nil {
						_ = tx.Rollback()
						if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, false, now); markErr != nil {
							return handled, errors.Join(err, markErr)
						}
						handled++
						continue
					}
				} else {
					_ = tx.Rollback()
					if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, isPermanentSQLiteQueueError(err), now); markErr != nil {
						return handled, errors.Join(err, markErr)
					}
					handled++
					continue
				}
			}
		}
		if err := tx.Commit(); err != nil {
			if markErr := s.recordSQLiteScheduleDispatchError(ctx, id, err, false, now); markErr != nil {
				return handled, errors.Join(err, markErr)
			}
			handled++
			continue
		}
		handled++
	}
	return handled, nil
}

func isPermanentSQLiteQueueError(err error) bool {
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		return false
	}
	return !domainErr.Retryable && (domainErr.Code == "invalid_argument" || domainErr.Code == "not_found" || domainErr.HTTPStatus == 422)
}

func (s *SQLite) recordSQLiteScheduleDispatchError(ctx context.Context, id string, cause error, permanent bool, now time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var attempts int
	if err := s.db.QueryRowContext(ctx, `
		SELECT dispatch_attempts FROM scheduled_tasks WHERE id = ? AND deleted_at IS NULL
	`, id).Scan(&attempts); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	attempts++
	failureCode := ScheduleDispatchFailureCode(cause)
	if permanent {
		_, err := s.db.ExecContext(ctx, `
			UPDATE scheduled_tasks
			SET enabled = 0, dispatch_attempts = ?, dispatch_retry_at = NULL,
			    last_dispatch_error = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND enabled = 1 AND deleted_at IS NULL
		`, attempts, failureCode, sqliteTimeValue(now), id)
		return err
	}
	delay := time.Second
	for i := 1; i < attempts && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_tasks
		SET dispatch_attempts = ?, dispatch_retry_at = ?, last_dispatch_error = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND enabled = 1 AND deleted_at IS NULL
	`, attempts, sqliteTimeValue(now.Add(delay)), failureCode, sqliteTimeValue(now), id)
	return err
}

func insertSQLiteSkippedScheduleTx(ctx context.Context, tx *sql.Tx, taskID, triggerKey string, scheduledFor time.Time, status string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO scheduled_task_runs (
			id, scheduled_task_id, run_id, trigger_key, scheduled_for, status, created_at
		) VALUES (?, ?, NULL, ?, ?, ?, ?)
	`, newID("str"), taskID, triggerKey, sqliteTimeValue(scheduledFor), status, sqliteTimeValue(now))
	return err
}

func advanceSQLiteScheduleTx(ctx context.Context, tx *sql.Tx, task domain.ScheduledTask, scheduledFor, now time.Time) error {
	// Legacy once rows have no profile hydration validation. Validate before
	// disabling/advancing too, so malformed calendars cannot materialize a Run.
	if err := validateSQLiteScheduleCalendar(task); err != nil {
		return err
	}
	var next any
	enabled := task.Enabled
	if task.ScheduleKind == "once" {
		enabled = false
	} else {
		base := scheduledFor
		if now.Sub(scheduledFor) > time.Minute {
			base = now
		}
		nextTime, err := domain.NextScheduledTaskTime(task, base)
		if err != nil {
			return err
		}
		next = sqliteTimeValue(nextTime)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE scheduled_tasks SET enabled = ?, next_run_at = ?, dispatch_attempts = 0,
		    dispatch_retry_at = NULL, last_dispatch_error = NULL,
		    version = version + 1, updated_at = ?
		WHERE id = ?
	`, boolInt(enabled), next, sqliteTimeValue(now), task.ID)
	return err
}

func getSQLiteScheduledTaskInternalTx(ctx context.Context, tx *sql.Tx, id string) (domain.ScheduledTask, error) {
	var item domain.ScheduledTask
	err := scanSQLiteScheduledTask(tx.QueryRowContext(ctx, sqliteScheduledTaskSelect+`
		WHERE tasks.id = ? AND tasks.deleted_at IS NULL
	`, id), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledTask{}, domain.NotFound("计划任务", id)
	}
	return item, err
}

func getSQLiteScheduledRunTx(ctx context.Context, tx *sql.Tx, id, triggerKey string) (domain.ScheduledTaskRun, bool, error) {
	var item domain.ScheduledTaskRun
	var scheduled, created string
	err := tx.QueryRowContext(ctx, `
		SELECT task_run.id, task_run.scheduled_task_id, COALESCE(task_run.run_id, ''), task_run.trigger_key,
		       task_run.scheduled_for, task_run.status, task_run.created_at, COALESCE(run.issue_id, '')
		FROM scheduled_task_runs task_run LEFT JOIN runs run ON run.id = task_run.run_id
		WHERE task_run.scheduled_task_id = ? AND task_run.trigger_key = ?
	`, id, triggerKey).Scan(&item.ID, &item.ScheduledTaskID, &item.RunID,
		&item.TriggerKey, &scheduled, &item.Status, &created, &item.IssueID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledTaskRun{}, false, nil
	}
	if err != nil {
		return domain.ScheduledTaskRun{}, false, err
	}
	item.ScheduledFor, err = sqliteParseTime(scheduled)
	if err == nil {
		item.CreatedAt, err = sqliteParseTime(created)
	}
	return item, err == nil, err
}

// RecoverLocalExecution is called after the dispatcher has first imported all
// runtime events left unacknowledged by the previous process. Commands whose
// delivery outcome was unknown become retryable, while in-memory executions
// that still cannot be proven terminal are closed as interrupted.
func (s *SQLite) RecoverLocalExecution(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if _, err := expireSQLiteApprovalsTx(ctx, tx, now); err != nil {
		return err
	}
	// flushEvents runs before recovery. A terminal projection therefore proves
	// the runtime outcome even if the previous dispatcher died before updating
	// its command row. Do not replay obsolete stop/approval commands.
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'cancelled', version = version + 1, updated_at = ?
		WHERE status = 'pending' AND run_id IN (
		    SELECT id FROM runs WHERE status IN ('succeeded','failed','cancelled','lost')
		)
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'completed', result = COALESCE(result, '{}'), completed_at = ?,
		    payload = json_remove(payload, '$.attachments'),
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL, last_error = NULL
		WHERE command_name = 'run.start' AND status IN ('pending','processing','uncertain')
		  AND json_extract(payload, '$.run_id') IN (
		      SELECT id FROM runs WHERE status IN ('succeeded','failed','cancelled','lost')
		  )
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'failed', completed_at = ?, last_error = 'run_terminal',
		    payload = CASE
		        WHEN command_name = 'run.steer' THEN json_remove(payload, '$.attachments')
		        ELSE payload
		    END,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name IN ('run.stop','run.steer','approval.decide')
		  AND status IN ('pending','processing','uncertain')
		  AND json_extract(payload, '$.run_id') IN (
		      SELECT id FROM runs WHERE status IN ('succeeded','failed','cancelled','lost')
		  )
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, sqliteRunSelect+`
		WHERE runs.status IN ('starting','running','waiting_approval','cancelling')
		  AND runs.deleted_at IS NULL ORDER BY runs.created_at, runs.id
	`)
	if err != nil {
		return err
	}
	active := make([]domain.Run, 0)
	for rows.Next() {
		var run domain.Run
		if err := scanSQLiteRun(rows, &run); err != nil {
			_ = rows.Close()
			return err
		}
		active = append(active, run)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, run := range active {
		if err := finishSQLiteRunTx(ctx, tx, run.ID, domain.RunFailed, "interrupted", now, "run.finished"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE command_outbox
			SET status = 'failed', completed_at = ?, last_error = 'run_interrupted',
			    payload = CASE
			        WHEN command_name IN ('run.start','run.steer') THEN json_remove(payload, '$.attachments')
			        ELSE payload
			    END,
			    claimed_at = NULL, lease_until = NULL, claim_token = NULL
			WHERE command_name IN ('run.start','run.stop','run.steer','approval.decide')
			  AND status IN ('pending','processing','uncertain')
			  AND json_extract(payload, '$.run_id') = ?
		`, sqliteTimeValue(now), run.ID); err != nil {
			return err
		}
	}
	// A queued run.start is the sole recovery case that is safe and useful to
	// replay: it has not established an in-memory execution yet and its stable
	// MessageID is deduplicated by runtime.db. All other unknown delivery states
	// are made terminal explicitly instead of being retried speculatively.
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'pending', available_at = ?, claimed_at = NULL, lease_until = NULL,
		    claim_token = NULL,
		    last_error = 'dispatcher_restarted'
		WHERE command_name = 'run.start' AND status IN ('processing','uncertain')
		  AND json_extract(payload, '$.run_id') IN (
		      SELECT id FROM runs WHERE status = 'queued' AND deleted_at IS NULL
		  )
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	// Local I/O commands also have a durable Runtime record. Recover its result
	// through the same ID; uncertain Runtime work becomes an explicit failure.
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox SET status = 'uncertain', available_at = ?, claimed_at = NULL,
		    lease_until = NULL, claim_token = NULL, last_error = 'dispatcher_restarted'
		WHERE command_name IN ('filesystem.list','resource.register','resource.validate') AND status = 'processing'
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_outbox
		SET status = 'failed', completed_at = ?, last_error = 'recovery_not_replayable',
		    payload = CASE
		        WHEN command_name IN ('run.start','run.steer') THEN json_remove(payload, '$.attachments')
		        ELSE payload
		    END,
		    claimed_at = NULL, lease_until = NULL, claim_token = NULL
		WHERE command_name IN ('run.start','run.stop','run.steer','approval.decide')
		  AND status IN ('processing','uncertain')
	`, sqliteTimeValue(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// finishSQLiteConversationRunTx 收尾图对话节点的运行：把最终助手文本写回
// working 占位消息，并在没有其他活跃 Run 时把节点恢复为 idle。
func finishSQLiteConversationRunTx(ctx context.Context, tx *sql.Tx, run domain.Run, now time.Time) error {
	// Include late terminal usage even when the user already cancelled/paused.
	var goal domain.ConversationGoal
	goalErr := scanConversationGoal(tx.QueryRowContext(ctx, conversationGoalSelect+`
	 JOIN conversation_goal_runs gr ON gr.goal_id=cg.id WHERE gr.run_id=?`, run.ID), &goal)
	if goalErr == nil {
		if err := refreshConversationGoalTokensTx(ctx, tx, &goal); err != nil {
			return err
		}
		if err := persistConversationGoalTx(ctx, tx, &goal, now); err != nil {
			return err
		}
	} else if !errors.Is(goalErr, sql.ErrNoRows) {
		return goalErr
	}
	nodeStatus := domain.ConversationNodeFailed
	if run.Status == domain.RunSucceeded {
		text, err := finalAssistantTextSQLiteTx(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if text != "" {
			nodeStatus = domain.ConversationNodeIdle
			if _, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages
				SET body = ?, status = 'published', version = version + 1, updated_at = ?
				WHERE run_id = ? AND status = 'working' AND deleted_at IS NULL
			`, text, sqliteTimeValue(now), run.ID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE conversation_messages
			SET status = 'failed', version = version + 1, updated_at = ?
			WHERE run_id = ? AND status = 'working' AND deleted_at IS NULL
		`, sqliteTimeValue(now), run.ID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_messages
		SET status = 'failed', version = version + 1, updated_at = ?
		WHERE run_id = ? AND status = 'working' AND deleted_at IS NULL
	`, sqliteTimeValue(now), run.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE conversation_nodes
		SET status = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM runs active_run
		      WHERE active_run.conversation_node_id = conversation_nodes.id
		        AND active_run.id <> ? AND active_run.deleted_at IS NULL
		        AND active_run.status IN ('queued','starting','running','waiting_approval','cancelling')
		  )
	`, nodeStatus, sqliteTimeValue(now), run.ConversationNodeID, run.ID)
	return err
}
