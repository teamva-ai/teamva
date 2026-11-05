package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const workColumns = `id,kind,action,actor_type,actor_id,object_type,object_id,object_version,title,project_id,run_id,parent_id,request_id,client_id,page_id,entry_type,sequence,outcome,occurred_at,received_at,COALESCE(finished_at,''),refs,before_values,after_values,error_code`

func (s *SQLite) RecoverWorkOperations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_records SET outcome='unknown',error_code='process_interrupted',finished_at=? WHERE kind='operation' AND outcome='pending'`, sqliteTimeValue(time.Now()))
	return err
}

func scanWorkRecord(row scanner) (domain.WorkRecord, error) {
	var r domain.WorkRecord
	var refs, before, after string
	err := row.Scan(&r.ID, &r.Kind, &r.Action, &r.ActorType, &r.ActorID, &r.ObjectType, &r.ObjectID, &r.ObjectVersion, &r.Title, &r.ProjectID, &r.RunID, &r.ParentID, &r.RequestID, &r.ClientID, &r.PageID, &r.EntryType, &r.Sequence, &r.Outcome, &r.OccurredAt, &r.ReceivedAt, &r.FinishedAt, &refs, &before, &after, &r.ErrorCode)
	if err != nil {
		return r, err
	}
	for _, pair := range []struct {
		raw    string
		target *map[string]any
	}{{refs, &r.Refs}, {before, &r.Before}, {after, &r.After}} {
		if err := json.Unmarshal([]byte(pair.raw), pair.target); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *SQLite) QueryWorkRecords(ctx context.Context, userID string, q domain.WorkQuery) (domain.WorkPage, error) {
	where, args, err := workWhere(q)
	if err != nil {
		return domain.WorkPage{}, err
	}
	args = append([]any{userID}, args...)
	page := domain.WorkPage{Items: []domain.WorkRecord{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM work_records WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	args = append(args, limit, q.Offset)
	rows, err := tx.QueryContext(ctx, "SELECT "+workColumns+" FROM work_records WHERE "+where+" ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanWorkRecord(rows)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, r)
	}
	return page, rows.Err()
}

func (s *SQLite) GetWorkRecord(ctx context.Context, userID, id string) (domain.WorkRecord, error) {
	r, err := scanWorkRecord(s.db.QueryRowContext(ctx, "SELECT "+workColumns+" FROM work_records WHERE user_id=? AND id=?", userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.NotFound("工作记录", id)
	}
	return r, err
}

func (s *SQLite) WorkRecordChildren(ctx context.Context, userID, id string) ([]domain.WorkRecord, error) {
	r, err := s.GetWorkRecord(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+workColumns+` FROM work_records WHERE user_id=? AND id<>? AND (parent_id=? OR (?<>'' AND run_id=? AND kind='change')) ORDER BY occurred_at,id LIMIT 200`, userID, id, id, r.RunID, r.RunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.WorkRecord{}
	for rows.Next() {
		item, e := scanWorkRecord(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Work titles are display metadata, not a second business-field constraint.
// A long object title must not make that object impossible to edit or delete.
func boundedWorkTitle(title string) string {
	const maxBytes = 1000
	if len(title) <= maxBytes {
		return title
	}
	title = title[:maxBytes-len("…")]
	for !utf8.ValidString(title) {
		title = title[:len(title)-1]
	}
	return title + "…"
}

func (s *SQLite) AppendWorkRecord(ctx context.Context, userID string, r domain.WorkRecord) error {
	r.Title = boundedWorkTitle(r.Title)
	if r.ID == "" || len(r.ID) > 150 || len(r.ClientID) > 100 || len(r.PageID) > 100 || len(r.RequestID) > 150 || len(r.Action) > 150 {
		return domain.Invalid("工作记录标识或字段无效")
	}
	if r.OccurredAt == "" {
		r.OccurredAt = sqliteTimeValue(time.Now())
	} else {
		t, err := time.Parse(time.RFC3339Nano, r.OccurredAt)
		if err != nil {
			return domain.Invalid("操作时间无效")
		}
		r.OccurredAt = sqliteTimeValue(t)
	}
	if r.Refs == nil {
		r.Refs = map[string]any{}
	}
	if r.Before == nil {
		r.Before = map[string]any{}
	}
	if r.After == nil {
		r.After = map[string]any{}
	}
	refs, _ := json.Marshal(r.Refs)
	before, _ := json.Marshal(r.Before)
	after, _ := json.Marshal(r.After)
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_records(id,user_id,kind,action,actor_type,actor_id,object_type,object_id,object_version,title,project_id,run_id,parent_id,request_id,client_id,page_id,entry_type,sequence,outcome,occurred_at,refs,before_values,after_values,error_code)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, r.ID, userID, r.Kind, r.Action, r.ActorType, r.ActorID, r.ObjectType, r.ObjectID, r.ObjectVersion, r.Title, r.ProjectID, r.RunID, r.ParentID, r.RequestID, r.ClientID, r.PageID, r.EntryType, r.Sequence, r.Outcome, r.OccurredAt, string(refs), string(before), string(after), r.ErrorCode)
	return err
}

// The mutation's authoritative change rows were committed by SQLite triggers
// in the business transaction. Enrichment associates them with the originating
// HTTP action; a process crash can lose enrichment, never the change itself.
func (s *SQLite) FinishWorkOperation(ctx context.Context, userID string, r domain.WorkRecord) error {
	r.Title = boundedWorkTitle(r.Title)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	refs, _ := json.Marshal(r.Refs)
	now := sqliteTimeValue(time.Now())
	_, err = tx.ExecContext(ctx, `UPDATE work_records SET outcome=?,finished_at=?,object_type=?,object_id=?,object_version=?,title=?,project_id=?,run_id=?,refs=json_patch(refs,?),error_code=? WHERE id=? AND user_id=?`, r.Outcome, now, r.ObjectType, r.ObjectID, r.ObjectVersion, r.Title, r.ProjectID, r.RunID, string(refs), r.ErrorCode, r.ID, userID)
	if err != nil {
		return err
	}
	if r.ObjectID != "" && r.Outcome == "succeeded" {
		// The HTTP request can finish before or after a fast Run. Preserve its
		// durable execution outcome rather than overwriting it with HTTP 202.
		if r.RunID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE work_records SET outcome=(SELECT status FROM runs WHERE id=?),finished_at=(SELECT finished_at FROM runs WHERE id=?) WHERE id=? AND id=(SELECT json_extract(snapshot,'$.work_record_id') FROM runs WHERE id=?)`, r.RunID, r.RunID, r.ID, r.RunID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE work_records SET parent_id=?,request_id=?,client_id=?,page_id=?,entry_type=?,actor_type=CASE WHEN object_id=? THEN 'user' ELSE actor_type END WHERE user_id=? AND kind='change' AND parent_id='' AND received_at>=(SELECT received_at FROM work_records WHERE id=?) AND ((object_id=? AND (?=0 OR object_version<=?)) OR (?<>'' AND run_id=?))`, r.ID, r.RequestID, r.ClientID, r.PageID, r.EntryType, r.ObjectID, userID, r.ID, r.ObjectID, r.ObjectVersion, r.ObjectVersion, r.RunID, r.RunID)
		if err != nil {
			return err
		}
		// Copy the canonical transaction snapshot, never a request body.
		_, err = tx.ExecContext(ctx, `UPDATE work_records SET parent_id=?,actor_type='user',client_id=?,page_id=?,entry_type=?,request_id=? WHERE user_id=? AND kind='change' AND parent_id='' AND object_type='node' AND object_id IN (SELECT value FROM json_each(?,'$.created_node_ids'))`, r.ID, r.ClientID, r.PageID, r.EntryType, r.RequestID, userID, string(refs))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE work_records SET before_values=COALESCE((SELECT before_values FROM work_records WHERE parent_id=? ORDER BY received_at,id LIMIT 1),'{}'),after_values=COALESCE((SELECT after_values FROM work_records WHERE parent_id=? AND object_id=? ORDER BY object_version DESC LIMIT 1),'{}') WHERE id=?`, r.ID, r.ID, r.ObjectID, r.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE work_records SET
 title=CASE WHEN title='' THEN COALESCE((SELECT title FROM work_records WHERE parent_id=? AND object_id=? ORDER BY object_version DESC LIMIT 1),'') ELSE title END,
 project_id=CASE WHEN project_id='' THEN COALESCE((SELECT project_id FROM work_records WHERE parent_id=? AND object_id=? ORDER BY object_version DESC LIMIT 1),'') ELSE project_id END,
 refs=json_patch(refs,COALESCE((SELECT refs FROM work_records WHERE parent_id=? AND object_id=? ORDER BY object_version DESC LIMIT 1),'{}')) WHERE id=?`, r.ID, r.ObjectID, r.ID, r.ObjectID, r.ID, r.ObjectID, r.ID)
		if err != nil {
			return err
		}
		if r.Action == "node.update" {
			_, err = tx.ExecContext(ctx, `DELETE FROM work_records WHERE id=? AND NOT EXISTS(SELECT 1 FROM work_records WHERE parent_id=?)`, r.ID, r.ID)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *SQLite) ClearWorkRecords(ctx context.Context, userID string, q domain.WorkQuery) (int64, error) {
	q.Offset = 0
	q.Limit = 0
	where, args, err := workWhere(q)
	if err != nil {
		return 0, err
	}
	args = append([]any{userID}, args...)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff, err := time.Parse(time.RFC3339Nano, q.Until)
	if err != nil {
		return 0, domain.Invalid("清除需要明确截止时间")
	}
	args = append(args, userID, sqliteTimeValue(cutoff))
	result, err := tx.ExecContext(ctx, `WITH RECURSIVE selected(id,run_id) AS (
 SELECT id,run_id FROM work_records WHERE `+where+`
 UNION SELECT w.id,w.run_id FROM work_records w JOIN selected p ON w.parent_id=p.id OR (p.run_id<>'' AND w.run_id=p.run_id AND w.kind='change') WHERE w.user_id=? AND w.occurred_at<=?
) DELETE FROM work_records WHERE id IN (SELECT id FROM selected)`, args...)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,user_id,actor_type,actor_ref,action,outcome,details,occurred_at) VALUES(?,?,'user',?,'work_records.cleared','succeeded',?,?)`, newID("aud"), userID, userID, fmt.Sprintf(`{"count":%d}`, count), sqliteTimeValue(time.Now()))
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *SQLite) SaveWorkFilter(ctx context.Context, userID string, f domain.WorkFilter) (domain.WorkFilter, error) {
	if _, _, err := workWhere(f.Query); err != nil {
		return f, err
	}
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len([]rune(f.Name)) > 100 {
		return f, domain.Invalid("筛选名称无效")
	}
	f.Query.Offset = 0
	if f.ID == "" {
		f.ID = newID("wfl")
	}
	raw, _ := json.Marshal(f.Query)
	result, err := s.db.ExecContext(ctx, `INSERT INTO work_record_filters(id,user_id,name,query,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,query=excluded.query,updated_at=excluded.updated_at WHERE work_record_filters.user_id=excluded.user_id`, f.ID, userID, f.Name, string(raw), sqliteTimeValue(time.Now()))
	if err != nil {
		return f, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return f, domain.NotFound("筛选", f.ID)
	}
	return f, nil
}
func (s *SQLite) ListWorkFilters(ctx context.Context, userID string) ([]domain.WorkFilter, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,query FROM work_record_filters WHERE user_id=? ORDER BY updated_at DESC,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.WorkFilter{}
	for rows.Next() {
		var f domain.WorkFilter
		var raw string
		if err := rows.Scan(&f.ID, &f.Name, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &f.Query); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}
func (s *SQLite) DeleteWorkFilter(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM work_record_filters WHERE id=? AND user_id=?`, id, userID)
	return err
}

type WorkLink struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Deleted bool   `json:"deleted"`
}

func (s *SQLite) WorkRecordLinks(ctx context.Context, userID string, r domain.WorkRecord) ([]WorkLink, error) {
	items := []WorkLink{}
	seen := map[string]bool{}
	add := func(kind, id string) {
		if id == "" || seen[kind+id] {
			return
		}
		seen[kind+id] = true
		link := s.ResolveWorkLink(ctx, userID, kind, id)
		items = append(items, link)
	}
	add(r.ObjectType, r.ObjectID)
	add("project", r.ProjectID)
	add("run", r.RunID)
	for field, kind := range map[string]string{"project_id": "project", "agent_id": "agent", "graph_id": "graph", "node_id": "node", "parent_id": "node", "fork_message_id": "message", "issue_id": "issue", "message_id": "message", "comment_id": "comment", "agent_revision_id": "agent_revision", "assistant_session_id": "assistant"} {
		if id, ok := r.Refs[field].(string); ok {
			add(kind, id)
		}
	}
	for _, values := range []map[string]any{r.Before, r.After} {
		if state, ok := values["state"].(map[string]any); ok {
			for field, kind := range map[string]string{"project_id": "project", "issue_id": "issue", "graph_id": "graph", "node_id": "node"} {
				if id, ok := state[field].(string); ok {
					add(kind, id)
				}
			}
		}
	}
	return items, nil
}

func (s *SQLite) ResolveWorkLink(ctx context.Context, userID, kind, id string) WorkLink {
	l := WorkLink{Kind: kind, ID: id, Deleted: true}
	var project, graph, node string
	switch kind {
	case "inspiration":
		if n, err := s.GetInspiration(ctx, userID, id); err == nil {
			l.Title = n.Title
			l.URL = "/inspirations?note=" + id
		}
	case "resource", "memory", "schedule":
		tables := map[string]string{"resource": "project_resources", "memory": "project_memory_entries", "schedule": "scheduled_tasks"}
		titles := map[string]string{"resource": "name", "memory": "title", "schedule": "name"}
		routes := map[string]string{"resource": "/resources?resource=", "memory": "/memories?memory=", "schedule": "/schedules?task="}
		query := `SELECT o.` + titles[kind] + `,o.project_id FROM ` + tables[kind] + ` o JOIN projects p ON p.id=o.project_id JOIN computers c ON c.id=p.computer_id WHERE o.id=? AND c.user_id=? AND o.deleted_at IS NULL AND p.deleted_at IS NULL AND c.deleted_at IS NULL`
		if err := s.db.QueryRowContext(ctx, query, id, userID).Scan(&l.Title, &project); err == nil {
			l.URL = routes[kind] + id
		}
	case "assistant_message":
		var session string
		if err := s.db.QueryRowContext(ctx, `SELECT m.session_id FROM assistant_messages m JOIN assistant_sessions s ON s.id=m.session_id WHERE m.id=? AND s.user_id=?`, id, userID).Scan(&session); err == nil {
			l.Title = id
			l.URL = "/work-records?session=" + session
		}
	case "project":
		if v, e := s.GetProject(ctx, userID, id); e == nil {
			l.Title = v.Name
			project = id
			l.URL = "/workbench"
		}
	case "agent":
		if v, e := s.GetAgent(ctx, userID, id); e == nil {
			l.Title = v.Name
			l.URL = "/agents?agent=" + id
		}
	case "issue":
		if v, e := s.GetIssue(ctx, userID, id); e == nil {
			l.Title = v.Title
			project = v.ProjectID
			l.URL = "/issues?issue=" + id
		}
	case "graph":
		if v, e := s.GetConversationGraph(ctx, userID, id); e == nil {
			l.Title = v.Title
			project = v.ProjectID
			graph = id
		}
	case "node":
		if v, e := s.GetConversationNode(ctx, userID, id); e == nil {
			l.Title = v.Title
			node = id
			graph = v.GraphID
			if g, e := s.GetConversationGraph(ctx, userID, graph); e == nil {
				project = g.ProjectID
			}
		}
	case "message":
		var n string
		if e := s.db.QueryRowContext(ctx, `SELECT node_id FROM conversation_messages WHERE id=? AND deleted_at IS NULL`, id).Scan(&n); e == nil {
			parent := s.ResolveWorkLink(ctx, userID, "node", n)
			if !parent.Deleted {
				l.Title = id
				l.URL = parent.URL + "&message=" + id
			}
		}
	case "comment":
		if v, e := s.GetIssueComment(ctx, userID, id); e == nil {
			parent := s.ResolveWorkLink(ctx, userID, "issue", v.IssueID)
			if !parent.Deleted {
				l.Title = id
				l.URL = parent.URL + "&comment=" + id
			}
		}
	case "run":
		if v, e := s.GetRun(ctx, userID, id); e == nil {
			l.Title = id
			if v.IssueID != "" {
				l = s.ResolveWorkLink(ctx, userID, "issue", v.IssueID)
				l.Kind = kind
				l.ID = id
				l.URL += "&run=" + id
			} else if v.ConversationNodeID != "" {
				l = s.ResolveWorkLink(ctx, userID, "node", v.ConversationNodeID)
				l.Kind = kind
				l.ID = id
				l.URL += "&run=" + id
			} else {
				l.URL = "/work-records?run=" + id
			}
		}
	case "assistant":
		var found, scope string
		if e := s.db.QueryRowContext(ctx, `SELECT title,scope FROM assistant_sessions WHERE id=? AND user_id=?`, id, userID).Scan(&found, &scope); e == nil {
			l.Title = found
			l.URL = "/work-records?session=" + id
			if scope == "inspirations" {
				l.URL = "/inspirations?session=" + id
			}
		}
	case "agent_revision":
		if v, e := s.GetAgentRevision(ctx, userID, id); e == nil {
			l = s.ResolveWorkLink(ctx, userID, "agent", v.AgentID)
			l.Kind = kind
			l.ID = id
		}
	}
	if graph != "" {
		l.URL = "/conversations?graph=" + graph
		if node != "" {
			l.URL += "&node=" + node
		}
	}
	if project != "" && l.URL != "" {
		sep := "?"
		if strings.Contains(l.URL, "?") {
			sep = "&"
		}
		l.URL += sep + "project=" + project
	}
	l.Deleted = l.URL == ""
	return l
}
