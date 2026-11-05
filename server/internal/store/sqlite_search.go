package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"modernc.org/sqlite"
)

func init() {
	// This pure function is also used for the final Go match. SQLite's built-in
	// lower/LIKE cannot implement our Unicode/literal substring contract. Return
	// only an excerpt from SQL, never entire matched documents or arbitrary JSON.
	sqlite.MustRegisterDeterministicScalarFunction("teamva_search_excerpt", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			text, textOK := args[0].(string)
			query, queryOK := args[1].(string)
			if textOK && queryOK {
				if excerpt, ok := projectSearchMatch(text, query); ok {
					return excerpt, nil
				}
			}
			return nil, nil
		})
}

// Limits bound both work and resident data. Text documents have independent
// source quotas so a large memory collection cannot consume the session quota.
// Anything not examined because of a quota/oversized row sets Truncated, even
// when there are fewer than limit hits. All windows start at the newest end.
type projectSearchLimits struct {
	batchSize, textRows, runs, events, runEvents, inputBlocks  int
	documentBytes, textBytes, runBytes, scanBytes, resultBytes int
}

func defaultProjectSearchScanLimits() projectSearchLimits {
	return projectSearchLimits{
		batchSize: 64, textRows: 8_192, runs: 2_048, events: 32_768, runEvents: 8_192, inputBlocks: 256,
		documentBytes: 1 << 20, textBytes: 8 << 20, runBytes: 4 << 20,
		scanBytes: 96 << 20, resultBytes: 4 << 20,
	}
}

// Kept per request, not global; tests can verify candidate/work bounds without
// mutating production limits or measuring noisy process-wide heap statistics.
type projectSearchScan struct {
	limits                           projectSearchLimits
	results                          projectSearchResults
	documentRows, runRows, eventRows int
	inputRows, bytes                 int
}

func (p *projectSearchScan) reserveBytes(size int) bool {
	if size < 0 || size > p.limits.documentBytes || size > p.limits.scanBytes-p.bytes {
		p.results.truncated = true
		return false
	}
	p.bytes += size
	return true
}

func (s *SQLite) SearchProjectContent(
	ctx context.Context, userID, projectID, query string, limit int,
) (domain.ProjectSearchPage, error) {
	scan := &projectSearchScan{limits: defaultProjectSearchScanLimits()}
	return s.searchProjectContent(ctx, userID, projectID, query, limit, scan)
}

func (s *SQLite) searchProjectContent(
	ctx context.Context, userID, projectID, query string, limit int, scan *projectSearchScan,
) (domain.ProjectSearchPage, error) {
	normalized, limit, err := normalizeProjectSearch(query, limit)
	if err != nil {
		return domain.ProjectSearchPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ProjectSearchPage{}, err
	}
	if err := requireSQLiteProjectOwner(ctx, s.db, userID, projectID); err != nil {
		return domain.ProjectSearchPage{}, err
	}
	scan.results.limit = limit
	scan.results.maxBytes = scan.limits.resultBytes
	for _, source := range projectSearchTextSources() {
		if err := s.searchProjectText(ctx, projectID, normalized, source, scan); err != nil {
			return domain.ProjectSearchPage{}, err
		}
	}
	if err := s.searchProjectRuns(ctx, projectID, normalized, scan); err != nil {
		return domain.ProjectSearchPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ProjectSearchPage{}, err
	}
	return scan.results.page(), nil
}

type projectSearchTextSource struct {
	from, where, id, issueID, title, text, size, updated, kind, prefix string
}

func projectSearchTextSources() []projectSearchTextSource {
	return []projectSearchTextSource{
		{
			from: "project_memory_entries entry", where: "entry.project_id = ? AND entry.deleted_at IS NULL",
			id: "entry.id", issueID: "''", title: "entry.title", text: "entry.title || char(10) || entry.content",
			size: "octet_length(entry.title) + 1 + octet_length(entry.content)", updated: "entry.updated_at",
			kind: domain.ProjectSearchMemory, prefix: "memory:",
		},
		{
			from: "issues issue", where: "issue.project_id = ? AND issue.deleted_at IS NULL",
			id: "issue.id", issueID: "issue.id", title: "issue.title", text: "issue.title || char(10) || issue.description",
			size: "octet_length(issue.title) + 1 + octet_length(issue.description)", updated: "issue.updated_at",
			kind: domain.ProjectSearchIssueDescription, prefix: "issue:",
		},
		{
			from:  "issue_comments comment JOIN issues issue ON issue.id = comment.issue_id",
			where: "issue.project_id = ? AND issue.deleted_at IS NULL AND comment.deleted_at IS NULL AND comment.status = 'published'",
			id:    "comment.id", issueID: "comment.issue_id", title: "issue.title", text: "comment.body",
			size: "octet_length(issue.title) + octet_length(comment.body)", updated: "comment.updated_at",
			kind: domain.ProjectSearchIssueComment, prefix: "comment:",
		},
	}
}

type projectSearchTextCandidate struct {
	ID    string `json:"id"`
	Bytes int    `json:"bytes"`
}

func (s *SQLite) searchProjectText(
	ctx context.Context, projectID, query string, source projectSearchTextSource, scan *projectSearchScan,
) error {
	var cursorTime, cursorID string
	scanned, scannedBytes := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Read only IDs/lengths/timestamps first. Even a huge non-matching body
		// never crosses database/sql or enters a materialized candidate table.
		batchSize := min(scan.limits.batchSize, scan.limits.textRows-scanned+1)
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT %s, %s, %s FROM %s WHERE %s
			AND (? = '' OR %s < ? OR (%s = ? AND %s > ?))
			ORDER BY %s DESC, %s LIMIT ?
		`, source.id, source.updated, source.size, source.from, source.where,
			source.updated, source.updated, source.id, source.updated, source.id),
			projectID, cursorTime, cursorTime, cursorTime, cursorID, batchSize)
		if err != nil {
			return err
		}
		candidates := make([]projectSearchTextCandidate, 0, batchSize)
		count, exhausted := 0, false
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return err
			}
			var id, updated string
			var size int
			if err := rows.Scan(&id, &updated, &size); err != nil {
				rows.Close()
				return err
			}
			count++
			if scanned == scan.limits.textRows {
				scan.results.truncated = true
				exhausted = true
				break
			}
			scanned++
			scan.documentRows++
			cursorTime, cursorID = updated, id
			if size > scan.limits.textBytes-scannedBytes {
				scan.results.truncated = true
				continue
			}
			if scan.reserveBytes(size) {
				scannedBytes += size
				candidates = append(candidates, projectSearchTextCandidate{ID: id, Bytes: size})
			}
		}
		if err := closeProjectSearchRows(rows); err != nil {
			return err
		}
		if len(candidates) > 0 {
			if err := s.searchProjectTextCandidates(ctx, projectID, query, source, candidates, scan); err != nil {
				return err
			}
		}
		if exhausted || count < batchSize {
			return nil
		}
	}
}

func (s *SQLite) searchProjectTextCandidates(
	ctx context.Context, projectID, query string, source projectSearchTextSource, candidates []projectSearchTextCandidate, scan *projectSearchScan,
) error {
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	// The source quota bounds this materialization; it contains only short
	// excerpts and titles, never complete content/description/comment bodies.
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		WITH matches AS MATERIALIZED (
			SELECT %s AS id, %s AS issue_id, %s AS updated_at,
			       CASE WHEN %s <= json_extract(candidate.value, '$.bytes') THEN %s END AS title,
			       CASE WHEN %s <= json_extract(candidate.value, '$.bytes')
			            THEN teamva_search_excerpt(%s, ?) END AS excerpt,
			       %s > json_extract(candidate.value, '$.bytes') AS grew
			FROM json_each(?) candidate CROSS JOIN %s WHERE %s = json_extract(candidate.value, '$.id') AND %s
		)
		SELECT id, issue_id, title, updated_at, excerpt, grew FROM matches WHERE excerpt IS NOT NULL OR grew
	`, source.id, source.issueID, source.updated, source.size, source.title, source.size, source.text,
		source.size, source.from, source.id, source.where), query, string(encoded), projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var id, issueID, updated string
		var title, excerpt sql.NullString
		var grew bool
		if err := rows.Scan(&id, &issueID, &title, &updated, &excerpt, &grew); err != nil {
			return err
		}
		if grew {
			// The document changed between keyset selection and projection. Do
			// not let a concurrent edit bypass this request's byte reservation.
			scan.results.truncated = true
			continue
		}
		occurred, err := sqliteParseTime(updated)
		if err != nil {
			return err
		}
		item := domain.ProjectSearchResult{
			ID: source.prefix + id, Kind: source.kind, ProjectID: projectID,
			IssueID: issueID, Title: title.String, Excerpt: excerpt.String, OccurredAt: occurred,
		}
		switch source.kind {
		case domain.ProjectSearchMemory:
			item.MemoryID = id
		case domain.ProjectSearchIssueComment:
			item.CommentID = id
		}
		scan.results.add(item)
	}
	return rows.Err()
}

type projectSearchRun struct {
	id, issueID, instanceID, updated string
	createdAt                        time.Time
	inputBytes                       int
}

func (s *SQLite) searchProjectRuns(ctx context.Context, projectID, query string, scan *projectSearchScan) error {
	var cursorTime, cursorID string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batchSize := min(scan.limits.batchSize, scan.limits.runs-scan.runRows+1)
		// Do not use sqliteRunSelect: input/snapshot can each contain megabytes.
		// The joined Issue (not just runs.project_id) is an ownership boundary.
		rows, err := s.db.QueryContext(ctx, `
			SELECT runs.id, runs.issue_id, runs.issue_agent_instance_id, runs.created_at, runs.updated_at, octet_length(runs.input)
			FROM runs JOIN issues issue ON issue.id = runs.issue_id AND issue.project_id = runs.project_id
			JOIN issue_agent_instances instance ON instance.id = runs.issue_agent_instance_id AND instance.issue_id = issue.id
			WHERE runs.project_id = ? AND runs.deleted_at IS NULL AND issue.deleted_at IS NULL
			  AND instance.deleted_at IS NULL AND runs.conversation_node_id IS NULL AND runs.assistant_session_id IS NULL
			  AND (? = '' OR runs.updated_at < ? OR (runs.updated_at = ? AND runs.id > ?))
			ORDER BY runs.updated_at DESC, runs.id LIMIT ?
		`, projectID, cursorTime, cursorTime, cursorTime, cursorID, batchSize)
		if err != nil {
			return err
		}
		runs := make([]projectSearchRun, 0, batchSize)
		count, exhausted := 0, false
		for rows.Next() {
			var run projectSearchRun
			var created string
			if err := rows.Scan(&run.id, &run.issueID, &run.instanceID, &created, &run.updated, &run.inputBytes); err != nil {
				rows.Close()
				return err
			}
			count++
			if scan.runRows == scan.limits.runs {
				scan.results.truncated = true
				exhausted = true
				break
			}
			run.createdAt, err = sqliteParseTime(created)
			if err != nil {
				rows.Close()
				return err
			}
			scan.runRows++
			cursorTime, cursorID = run.updated, run.id
			runs = append(runs, run)
		}
		if err := closeProjectSearchRows(rows); err != nil {
			return err
		}
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return err
			}
			var title sql.NullString
			var size int
			err := s.db.QueryRowContext(ctx, `
				SELECT octet_length(title), CASE WHEN octet_length(title) <= ? THEN title END
				FROM issues WHERE id = ? AND project_id = ? AND deleted_at IS NULL
			`, scan.limits.documentBytes, run.issueID, projectID).Scan(&size, &title)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if !title.Valid || !scan.reserveBytes(size) {
				scan.results.truncated = true
				continue
			}
			add := func(text, id, role, anchor string, occurred time.Time) {
				if excerpt, ok := projectSearchMatch(text, query); ok {
					scan.results.add(domain.ProjectSearchResult{
						ID: id, Kind: domain.ProjectSearchSessionMessage, ProjectID: projectID,
						IssueID: run.issueID, IssueAgentInstanceID: run.instanceID, RunID: run.id,
						Role: role, Anchor: anchor, Title: title.String, Excerpt: excerpt, OccurredAt: occurred,
					})
				}
			}
			if err := s.searchProjectRunInput(ctx, run, scan, add); err != nil {
				return err
			}
			if err := s.searchProjectRunEvents(ctx, run.id, scan, add); err != nil {
				return err
			}
		}
		if exhausted || count < batchSize {
			return nil
		}
	}
}

type addProjectSearchMessage func(text, id, role, anchor string, occurred time.Time)

// Normal Run commands already have a smaller wire limit. Guard the JSON parser
// too, so an anomalous stored row cannot allocate an arbitrarily large JSON
// tree inside SQLite before public text fields have been selected.
const maxProjectSearchInputJSONBytes = 16 << 20

func (s *SQLite) searchProjectRunInput(
	ctx context.Context, run projectSearchRun, scan *projectSearchScan, add addProjectSearchMessage,
) error {
	if run.inputBytes > maxProjectSearchInputJSONBytes {
		scan.results.truncated = true
		return nil
	}
	// JSON is inspected only inside SQLite. Return only explicit, public text
	// fields; never Run.Input, image data, internal blocks, or a json_tree match.
	rows, err := s.db.QueryContext(ctx, `
		WITH blocks AS (
			SELECT json_extract(block.value, '$.text') AS text
			FROM runs, json_each(runs.input) block WHERE runs.id = ? AND octet_length(runs.input) <= ?
			AND json_extract(block.value, '$.type') = 'text'
			AND (json_type(block.value, '$.internal') IS NULL OR json_type(block.value, '$.internal') = 'false')
			AND json_type(block.value, '$.text') = 'text'
			ORDER BY CAST(block.key AS INTEGER) LIMIT ?
		)
		SELECT octet_length(text), CASE WHEN octet_length(text) <= ? THEN text END FROM blocks
	`, run.id, maxProjectSearchInputJSONBytes, scan.limits.inputBlocks+1, min(scan.limits.documentBytes, scan.limits.scanBytes-scan.bytes))
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > scan.limits.inputBlocks {
			scan.results.truncated = true
			break
		}
		var size int
		var text sql.NullString
		if err := rows.Scan(&size, &text); err != nil {
			return err
		}
		scan.inputRows++
		if !text.Valid || !scan.reserveBytes(size) {
			scan.results.truncated = true
			// Unknown first public text: do not return some later block as if it
			// were the input the UI selected.
			break
		}
		visible := visibleSearchRunInput([]domain.ContentBlock{{Type: "text", Text: text.String}})
		if visible != "" {
			add(visible, "session-user:"+run.id, "user", "input", run.createdAt)
			break
		}
	}
	return rows.Err()
}

const projectSearchEventTypes = `('user.steer', 'provider.item', 'provider.event', 'output.delta', 'output.snapshot')`

type projectSearchEventCandidate struct {
	Sequence int64 `json:"sequence"`
	Bytes    int   `json:"bytes"`
}

func (s *SQLite) searchProjectRunEvents(
	ctx context.Context, runID string, scan *projectSearchScan, add addProjectSearchMessage,
) error {
	// The existing run/sequence indexes are scanned backwards. First
	// choose a bounded *latest* window by count AND wire bytes, then replay it
	// chronologically through the official provider-owned output projector.
	// Budget the complete envelope, not just payload: large external event IDs
	// must not bypass the memory/work bound either.
	countLimit := min(scan.limits.runEvents, scan.limits.events-scan.eventRows)
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, event_wire_bytes FROM run_events
		WHERE run_id = ? AND event_type IN `+projectSearchEventTypes+`
		ORDER BY sequence DESC LIMIT ?
	`, runID, countLimit+1)
	if err != nil {
		return err
	}
	count, retainedBytes := 0, 0
	candidates := make([]projectSearchEventCandidate, 0, min(countLimit, scan.limits.batchSize))
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return err
		}
		var sequence int64
		var size int
		if err := rows.Scan(&sequence, &size); err != nil {
			rows.Close()
			return err
		}
		if count == countLimit {
			scan.results.truncated = true
			break
		}
		if size <= scan.limits.documentBytes {
			if size > scan.limits.runBytes-retainedBytes || size > scan.limits.scanBytes-scan.bytes {
				scan.results.truncated = true
				break
			}
			if !scan.reserveBytes(size) {
				break
			}
			retainedBytes += size
		} else {
			// Keep its sequence as an explicit stream gap, not its payload.
			scan.results.truncated = true
		}
		count++
		scan.eventRows++
		candidates = append(candidates, projectSearchEventCandidate{Sequence: sequence, Bytes: size})
	}
	if err := closeProjectSearchRows(rows); err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	// Keep the small JSON candidate list as the outer loop. An ordinary JOIN
	// can make SQLite replay json_each once for every historical event (O(n²)).
	rows, err = s.db.QueryContext(ctx, `
		SELECT CASE WHEN octet_length(event.id) <= ? THEN event.id ELSE 'search-gap:' || event.sequence END,
		       event.event_type, event.occurred_at,
		       CASE WHEN event.event_wire_bytes <= ?
		                 AND event.event_wire_bytes <= json_extract(candidate.value, '$.bytes') THEN event.payload_wire END
		FROM json_each(?) candidate CROSS JOIN run_events event INDEXED BY run_events_page_budget
		WHERE event.run_id = ? AND event.sequence = json_extract(candidate.value, '$.sequence')
		ORDER BY event.sequence
	`, scan.limits.documentBytes, scan.limits.documentBytes, string(encoded), runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	assistant := newProjectSearchAssistant(scan.limits.runBytes)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event domain.RunEvent
		var occurred string
		var payload sql.NullString
		if err := rows.Scan(&event.ID, &event.Type, &occurred, &payload); err != nil {
			return err
		}
		event.OccurredAt, err = sqliteParseTime(occurred)
		if err != nil {
			return err
		}
		if !payload.Valid {
			scan.results.truncated = true
			event.Type = "search.gap"
		} else if err := json.Unmarshal([]byte(payload.String), &event.Payload); err != nil {
			return err
		}
		if event.Type == "user.steer" {
			internal, explicit := event.Payload["internal"].(bool)
			text, textOK := event.Payload["text"].(string)
			if explicit && !internal && textOK && strings.TrimSpace(text) != "" {
				add(text, "session-steer:"+event.ID, "user", "steer:"+event.ID, event.OccurredAt)
			}
		}
		assistant.consume(event)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, message := range assistant.finish(runID) {
		if err := ctx.Err(); err != nil {
			return err
		}
		add(message.text, "session-assistant:"+runID+":"+message.anchor, "assistant", message.anchor, message.occurredAt)
	}
	scan.results.truncated = scan.results.truncated || assistant.truncated
	return nil
}

func closeProjectSearchRows(rows *sql.Rows) error {
	return errors.Join(rows.Err(), rows.Close())
}
