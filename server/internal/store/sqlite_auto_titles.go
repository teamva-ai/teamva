package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Title ownership is independent of an entity's optimistic version. Moving a
// node or saving its body must not revoke the right to apply its first title.
// Missing metadata means an existing/manual title, never eligibility to rename.
const autoTitlePrefix = "auto_title:v1:"

type autoTitleState struct {
	Schema      int       `json:"schema"`
	UserID      string    `json:"user_id"`
	Kind        string    `json:"kind"`
	EntityID    string    `json:"entity_id"`
	Mode        string    `json:"mode"`
	Epoch       int64     `json:"epoch"`
	Seed        string    `json:"seed,omitempty"`
	QueuedAt    time.Time `json:"queued_at,omitempty"`
	NextAt      time.Time `json:"next_at,omitempty"`
	Attempt     int       `json:"attempt"`
	LeaseToken  string    `json:"lease_token,omitempty"`
	LeaseUntil  time.Time `json:"lease_until,omitempty"`
	LumiAgentID string    `json:"lumi_agent_id,omitempty"`
	RevisionID  string    `json:"revision_id,omitempty"`
	SourceKind  string    `json:"source_kind,omitempty"`
	SourceID    string    `json:"source_id,omitempty"`
	Failure     string    `json:"failure,omitempty"`
}

type AutoTitleClaim struct {
	ID, LeaseToken, UserID, Kind, EntityID string
	Request                                nodeprotocol.TitleRequest
	Epoch                                  int64
}

func autoTitleKey(kind, id string) string { return autoTitlePrefix + kind + ":" + id }

func validAutoTitleKind(kind string) bool {
	switch kind {
	case "issue", "conversation_graph", "conversation_node", "inspiration", "memory", "schedule":
		return true
	}
	return false
}

func saveAutoTitleTx(ctx context.Context, tx *sql.Tx, state autoTitleState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_metadata(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		autoTitleKey(state.Kind, state.EntityID), string(raw), sqliteTimeValue(time.Now()))
	return err
}

func readAutoTitleTx(ctx context.Context, tx *sql.Tx, kind, id string) (autoTitleState, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key=?`, autoTitleKey(kind, id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return autoTitleState{}, false, nil
	}
	if err != nil {
		return autoTitleState{}, false, err
	}
	var state autoTitleState
	if json.Unmarshal([]byte(raw), &state) != nil || state.Schema != 1 || state.Kind != kind || state.EntityID != id || state.UserID == "" || state.Epoch < 1 || !validAutoTitleKind(kind) {
		return state, false, errors.New("invalid automatic title state")
	}
	switch state.Mode {
	case "awaiting", "queued", "running", "generated", "manual", "failed", "cancelled":
		return state, true, nil
	}
	return state, false, errors.New("invalid automatic title mode")
}

func initAutoTitleTx(ctx context.Context, tx *sql.Tx, userID, kind, id string) error {
	if !validAutoTitleKind(kind) || userID == "" || id == "" {
		return errors.New("invalid automatic title target")
	}
	// Never reset a title epoch when a create request is replayed.
	if _, exists, err := readAutoTitleTx(ctx, tx, kind, id); err != nil || exists {
		return err
	}
	return saveAutoTitleTx(ctx, tx, autoTitleState{Schema: 1, UserID: userID, Kind: kind, EntityID: id, Mode: "awaiting", Epoch: 1})
}

func queueAutoTitleTx(ctx context.Context, tx *sql.Tx, userID, kind, id, seed string) error {
	originalSeed := seed
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil
	}
	state, exists, err := readAutoTitleTx(ctx, tx, kind, id)
	if err != nil || !exists {
		return err
	}
	if state.Mode != "awaiting" || state.UserID != userID {
		return nil
	}
	// Only the bounded first-content seed is retained while pending. It is
	// erased on every terminal transition, never copied to logs or work records.
	seed = boundedAutoTitleSeed(seed)
	if seed == "" {
		state.Mode = "failed"
		state.Failure = "sensitive_or_invalid_seed"
	} else {
		// Keep the real source identity where first content is a message. A
		// deleted source must not be resurrected through a pending title seed.
		var sourceQuery, sourceKind string
		switch kind {
		case "issue":
			sourceKind = "issue_comment"
			sourceQuery = `SELECT id FROM issue_comments WHERE issue_id=? AND author_kind='user' AND body=? AND deleted_at IS NULL ORDER BY created_at,id LIMIT 1`
		case "conversation_node":
			sourceKind = "conversation_message"
			sourceQuery = `SELECT id FROM conversation_messages WHERE node_id=? AND author_kind='user' AND body=? AND deleted_at IS NULL ORDER BY created_at,id LIMIT 1`
		case "conversation_graph":
			sourceKind = "conversation_message"
			sourceQuery = `SELECT m.id FROM conversation_messages m JOIN conversation_nodes n ON n.id=m.node_id WHERE n.graph_id=? AND m.author_kind='user' AND m.body=? AND m.deleted_at IS NULL ORDER BY m.created_at,m.id LIMIT 1`
		}
		if sourceQuery != "" {
			err := tx.QueryRowContext(ctx, sourceQuery, id, originalSeed).Scan(&state.SourceID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				state.SourceKind = sourceKind
			}
		}
		state.Mode = "queued"
		state.Seed = seed
		state.QueuedAt = time.Now().UTC()
		state.NextAt = state.QueuedAt
	}
	return saveAutoTitleTx(ctx, tx, state)
}

func boundedAutoTitleSeed(seed string) string {
	if !utf8.ValidString(seed) {
		return ""
	}
	// Conservative skip, not a promise that arbitrary secrets can be detected.
	lower := strings.ToLower(seed)
	for _, marker := range []string{"-----begin private key", "-----begin rsa private key", "bearer ", "api_key=", "api_key:", "api key:", "access_token=", "password=", "密码：", "密码:"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	runes := []rune(seed)
	if len(runes) > 2048 {
		runes = runes[:2048]
	}
	return string(runes)
}

func manualAutoTitleTx(ctx context.Context, tx *sql.Tx, kind, id string) error {
	state, exists, err := readAutoTitleTx(ctx, tx, kind, id)
	if err != nil || !exists {
		return err
	}
	state.Epoch++
	state.Mode = "manual"
	state.Seed = ""
	state.LeaseToken = ""
	state.LeaseUntil = time.Time{}
	return saveAutoTitleTx(ctx, tx, state)
}

func cancelAutoTitleTx(ctx context.Context, tx *sql.Tx, kind, id string) error {
	state, exists, err := readAutoTitleTx(ctx, tx, kind, id)
	if err != nil || !exists {
		return err
	}
	if state.Mode != "awaiting" && state.Mode != "queued" && state.Mode != "running" {
		return nil
	}
	state.Epoch++
	state.Mode = "cancelled"
	state.Seed = ""
	state.LeaseToken = ""
	state.LeaseUntil = time.Time{}
	return saveAutoTitleTx(ctx, tx, state)
}

// A claim is a durable, fenced attempt. A crash may cause a new model call
// after lease expiry; only the current token/epoch can ever apply a result.
// There are at most two attempts and a thirty-minute total pending lifetime.
func (s *SQLite) ClaimAutoTitle(ctx context.Context, now time.Time, lease time.Duration) (AutoTitleClaim, bool, error) {
	if lease <= 0 {
		return AutoTitleClaim{}, false, errors.New("invalid automatic title lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AutoTitleClaim{}, false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT key FROM app_metadata WHERE key GLOB 'auto_title:v1:*'
		AND json_extract(value,'$.mode') IN ('queued','running') ORDER BY updated_at,key LIMIT 64`)
	if err != nil {
		return AutoTitleClaim{}, false, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return AutoTitleClaim{}, false, err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return AutoTitleClaim{}, false, err
	}
	for _, key := range keys {
		parts := strings.SplitN(strings.TrimPrefix(key, autoTitlePrefix), ":", 2)
		if len(parts) != 2 {
			return AutoTitleClaim{}, false, errors.New("invalid automatic title key")
		}
		state, _, err := readAutoTitleTx(ctx, tx, parts[0], parts[1])
		if err != nil {
			return AutoTitleClaim{}, false, err
		}
		if state.Mode == "running" && now.Before(state.LeaseUntil) || state.Mode == "queued" && now.Before(state.NextAt) {
			continue
		}
		alive, err := autoTitleTargetAliveTx(ctx, tx, state)
		if err != nil {
			return AutoTitleClaim{}, false, err
		}
		if !alive || state.Attempt >= 2 || state.QueuedAt.IsZero() || now.Sub(state.QueuedAt) > 30*time.Minute {
			state.Mode = "cancelled"
			state.Failure = "target_or_attempt_expired"
			state.Seed = ""
			state.LeaseToken = ""
			if err = saveAutoTitleTx(ctx, tx, state); err != nil {
				return AutoTitleClaim{}, false, err
			}
			continue
		}
		var provider, model, prompt, agentID, revisionID string
		err = tx.QueryRowContext(ctx, `SELECT a.id,v.id,s.provider,COALESCE(v.model,''),COALESCE(v.system_prompt,'')
			FROM agents a JOIN computers c ON c.id=a.computer_id JOIN users u ON u.id=c.user_id
			JOIN agent_revisions v ON v.agent_id=a.id AND v.id=CASE WHEN ?='' THEN a.current_revision_id ELSE ? END
			JOIN agent_services s ON s.id=v.agent_service_id AND s.computer_id=a.computer_id
			WHERE c.user_id=? AND a.system_role='general' AND a.status='active' AND a.deleted_at IS NULL
			AND c.deleted_at IS NULL AND u.deleted_at IS NULL AND v.deleted_at IS NULL AND s.deleted_at IS NULL AND s.status='ready'`,
			state.RevisionID, state.RevisionID, state.UserID).Scan(&agentID, &revisionID, &provider, &model, &prompt)
		if errors.Is(err, sql.ErrNoRows) {
			state.Mode = "failed"
			state.Failure = "lumi_unavailable"
			state.Seed = ""
			state.LeaseToken = ""
			if err = saveAutoTitleTx(ctx, tx, state); err != nil {
				return AutoTitleClaim{}, false, err
			}
			continue
		}
		if err != nil {
			return AutoTitleClaim{}, false, err
		}
		state.Attempt++
		state.Mode = "running"
		state.LeaseToken = newID("title_attempt")
		state.LeaseUntil = now.Add(lease)
		state.LumiAgentID = agentID
		state.RevisionID = revisionID
		if err = saveAutoTitleTx(ctx, tx, state); err != nil {
			return AutoTitleClaim{}, false, err
		}
		claim := AutoTitleClaim{ID: key, LeaseToken: state.LeaseToken, UserID: state.UserID, Kind: state.Kind, EntityID: state.EntityID, Epoch: state.Epoch,
			Request: nodeprotocol.TitleRequest{ExecutionID: state.LeaseToken, Provider: provider, Model: model, SystemPrompt: prompt, Seed: state.Seed}}
		if err = tx.Commit(); err != nil {
			return AutoTitleClaim{}, false, err
		}
		return claim, true, nil
	}
	return AutoTitleClaim{}, false, tx.Commit()
}

func autoTitleClaimMatches(state autoTitleState, claim AutoTitleClaim) bool {
	return state.Mode == "running" && state.UserID == claim.UserID && state.Kind == claim.Kind && state.EntityID == claim.EntityID &&
		state.LeaseToken == claim.LeaseToken && state.Epoch == claim.Epoch && claim.ID == autoTitleKey(claim.Kind, claim.EntityID)
}

func (s *SQLite) AutoTitleClaimActive(ctx context.Context, claim AutoTitleClaim) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	state, exists, err := readAutoTitleTx(ctx, tx, claim.Kind, claim.EntityID)
	if err != nil || !exists || !autoTitleClaimMatches(state, claim) || !time.Now().Before(state.LeaseUntil) {
		return false, err
	}
	alive, err := autoTitleTargetAliveTx(ctx, tx, state)
	if err != nil || alive {
		return alive, err
	}
	// A cancellation is a durable observation, not a transient predicate that
	// can become true again if an archived/restored target reappears.
	if err := cancelAutoTitleTx(ctx, tx, claim.Kind, claim.EntityID); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func validAutoTitle(title string) bool {
	if title != strings.TrimSpace(title) || title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 60 {
		return false
	}
	for _, r := range title {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return !strings.ContainsAny(title, "<>`\r\n") && !strings.Contains(title, "](") && !strings.Contains(title, "![")
}

func (s *SQLite) CompleteAutoTitle(ctx context.Context, claim AutoTitleClaim, title string) error {
	if !validAutoTitle(title) {
		return errors.New("invalid generated title")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, exists, err := readAutoTitleTx(ctx, tx, claim.Kind, claim.EntityID)
	if err != nil || !exists {
		return err
	}
	if !autoTitleClaimMatches(state, claim) || !time.Now().Before(state.LeaseUntil) {
		return nil
	}
	alive, err := autoTitleTargetAliveTx(ctx, tx, state)
	if err != nil {
		return err
	}
	if !alive {
		state.Mode = "cancelled"
	} else {
		if err = applyAutoTitleTx(ctx, tx, state, title); err != nil {
			return err
		}
		state.Mode = "generated"
	}
	state.Seed = ""
	state.LeaseToken = ""
	state.LeaseUntil = time.Time{}
	if err = saveAutoTitleTx(ctx, tx, state); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) FailAutoTitle(ctx context.Context, claim AutoTitleClaim, code string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, exists, err := readAutoTitleTx(ctx, tx, claim.Kind, claim.EntityID)
	if err != nil || !exists {
		return err
	}
	if !autoTitleClaimMatches(state, claim) {
		return nil
	}
	// Errors are categorical only. Provider errors may contain input or secrets.
	state.Failure = "generation_failed"
	state.LeaseToken = ""
	state.LeaseUntil = time.Time{}
	if state.Attempt < 2 {
		state.Mode = "queued"
		state.NextAt = time.Now().UTC().Add(5 * time.Second)
	} else {
		state.Mode = "failed"
		state.Seed = ""
	}
	if err = saveAutoTitleTx(ctx, tx, state); err != nil {
		return err
	}
	return tx.Commit()
}

func autoTitleTargetAliveTx(ctx context.Context, tx *sql.Tx, state autoTitleState) (bool, error) {
	if state.SourceID != "" {
		var sourceQuery string
		switch state.SourceKind {
		case "issue_comment":
			sourceQuery = `SELECT EXISTS(SELECT 1 FROM issue_comments WHERE id=? AND deleted_at IS NULL AND author_kind='user')`
		case "conversation_message":
			sourceQuery = `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE id=? AND deleted_at IS NULL AND author_kind='user')`
		default:
			return false, errors.New("invalid automatic title source")
		}
		var sourceAlive bool
		if err := tx.QueryRowContext(ctx, sourceQuery, state.SourceID).Scan(&sourceAlive); err != nil || !sourceAlive {
			return false, err
		}
	}
	var query string
	switch state.Kind {
	case "inspiration":
		query = `SELECT EXISTS(SELECT 1 FROM inspirations e JOIN users u ON u.id=e.user_id
			LEFT JOIN projects p ON p.id=e.project_id LEFT JOIN computers c ON c.id=p.computer_id
			WHERE e.id=? AND e.user_id=? AND e.deleted_at IS NULL AND u.deleted_at IS NULL
			AND (e.project_id IS NULL OR (p.deleted_at IS NULL AND c.deleted_at IS NULL AND c.user_id=e.user_id)))`
	case "conversation_node":
		query = `SELECT EXISTS(SELECT 1 FROM conversation_nodes e JOIN conversation_graphs g ON g.id=e.graph_id
			JOIN projects p ON p.id=g.project_id JOIN computers c ON c.id=p.computer_id JOIN users u ON u.id=c.user_id
			WHERE e.id=? AND c.user_id=? AND e.deleted_at IS NULL AND g.deleted_at IS NULL AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL)`
	default:
		table := map[string]string{"issue": "issues", "conversation_graph": "conversation_graphs", "memory": "project_memory_entries", "schedule": "scheduled_tasks"}[state.Kind]
		if table == "" {
			return false, errors.New("invalid automatic title target")
		}
		query = `SELECT EXISTS(SELECT 1 FROM ` + table + ` e JOIN projects p ON p.id=e.project_id
			JOIN computers c ON c.id=p.computer_id JOIN users u ON u.id=c.user_id WHERE e.id=? AND c.user_id=?
			AND e.deleted_at IS NULL AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND u.deleted_at IS NULL)`
	}
	var exists bool
	err := tx.QueryRowContext(ctx, query, state.EntityID, state.UserID).Scan(&exists)
	return exists, err
}

func applyAutoTitleTx(ctx context.Context, tx *sql.Tx, state autoTitleState, title string) error {
	var query string
	switch state.Kind {
	case "issue":
		query = `UPDATE issues SET title=?,version=version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	case "conversation_graph":
		query = `UPDATE conversation_graphs SET title=?,version=version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	case "conversation_node":
		query = `UPDATE conversation_nodes SET title=?,version=version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	case "inspiration":
		query = `UPDATE inspirations SET title=?,version=version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	case "schedule":
		query = `UPDATE scheduled_tasks SET name=?,version=version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	case "memory":
		query = `UPDATE project_memory_entries SET title=?,current_version=current_version+1,updated_at=? WHERE id=? AND deleted_at IS NULL`
	default:
		return errors.New("invalid automatic title target")
	}
	result, err := tx.ExecContext(ctx, query, title, sqliteTimeValue(time.Now()), state.EntityID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("automatic title target disappeared")
	}
	if state.Kind == "memory" {
		var item domain.ProjectMemoryEntry
		if err := scanSQLiteMemory(tx.QueryRowContext(ctx, sqliteMemorySelect+` WHERE memories.id=?`, state.EntityID), &item); err != nil {
			return err
		}
		// Automatic title changes are genuine revisions, not hidden history edits.
		return insertSQLiteMemoryVersion(ctx, tx, item, "agent")
	}
	return nil
}
