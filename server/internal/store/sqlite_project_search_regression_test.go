package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func insertProjectSearchTestRun(t *testing.T, tx *sql.Tx, f sqliteExecutionFixture, id string, input []domain.ContentBlock, occurred time.Time) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`
		INSERT INTO runs (id, issue_id, issue_agent_instance_id, agent_id, project_id, computer_id,
		                  status, input, idempotency_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'succeeded', ?, ?, ?, ?)
	`, id, f.issue.ID, f.instance.ID, f.agent.ID, f.project.ID, f.computer.ID,
		string(encoded), "search:"+id, sqliteTimeValue(occurred), sqliteTimeValue(occurred))
	if err != nil {
		t.Fatal(err)
	}
}

func insertProjectSearchTestMemory(t *testing.T, tx *sql.Tx, projectID, id, title, content string, occurred time.Time) {
	t.Helper()
	_, err := tx.Exec(`
		INSERT INTO project_memory_entries (id, project_id, memory_key, kind, title, content, source_type, created_at, updated_at)
		VALUES (?, ?, ?, 'fact', ?, ?, 'user', ?, ?)
	`, id, projectID, "search:"+id, title, content, sqliteTimeValue(occurred), sqliteTimeValue(occurred))
	if err != nil {
		t.Fatal(err)
	}
}

func requireProjectSearchHit(t *testing.T, page domain.ProjectSearchPage, runID, role string) domain.ProjectSearchResult {
	t.Helper()
	for _, item := range page.Items {
		if item.RunID == runID && item.Role == role {
			if !utf8.ValidString(item.Excerpt) {
				t.Fatalf("invalid UTF-8 excerpt: %q", item.Excerpt)
			}
			return item
		}
	}
	t.Fatalf("missing run %s role %s: %+v", runID, role, page)
	return domain.ProjectSearchResult{}
}

func TestSQLiteProjectSearchBeyond200RunsAnd1000EventsWithoutLoadingInputJSON(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	const oldest, latest = "search-run-000", "search-run-229"
	for index := 0; index < 230; index++ {
		input := []domain.ContentBlock{{Type: "text", Text: "ordinary work"}}
		if index == 0 {
			input[0].Text = "old-history-marker"
		}
		if index == 229 {
			input = []domain.ContentBlock{
				{Type: "text", Internal: true, Text: "internal-block-secret"},
				{Type: "text", Text: "# 任务（Issue）上下文\n" + strings.Repeat("context-control-secret ", 10_000) +
					"\n\n## 本次指令\n处理 prompt-visible-marker 中文 Ω\n\n请阅读上下文后开始工作"},
				// A deliberately adversarial stored image includes a text field too.
				// No image field, filename, data URL or internal field is searchable.
				{Type: "image", Text: "image-text-secret", Filename: "image-filename-secret", DataURL: "data:image/png;base64," + strings.Repeat("A", 200<<10) + "image-data-secret"},
			}
		}
		insertProjectSearchTestRun(t, tx, f, fmt.Sprintf("search-run-%03d", index), input, base.Add(time.Duration(index)*time.Second))
	}
	if err := insertSQLiteRunEvent(ctx, tx, oldest, 1, "output.snapshot", map[string]any{
		"provider_id": "codex", "id": "old-answer", "text": "old-output-marker",
	}, base); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1_202; index++ {
		text := "片"
		if index == 1_200 {
			text = "末端"
		}
		if index == 1_201 {
			text = "Ω-MARKER"
		}
		if err := insertSQLiteRunEvent(ctx, tx, latest, 1, "output.delta", map[string]any{
			"provider_id": "codex", "item_id": "tail", "text": text,
		}, base.Add(time.Duration(index)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct {
		kind    string
		payload map[string]any
	}{
		{"provider.item", map[string]any{"provider_id": "codex", "item": map[string]any{"type": "reasoning", "text": "reasoning-private-secret"}}},
		{"provider.tool", map[string]any{"provider_id": "codex", "text": "tool-private-secret"}},
		{"provider.event", map[string]any{"provider_id": "codex", "kind": "text", "part_id": "fake", "text": "wrong-owner-secret"}},
		{"output.delta", map[string]any{"provider_id": "codex", "internal": true, "text": "internal-output-secret"}},
		{"output.snapshot", map[string]any{"provider_id": "codex", "id": "image", "text": "data:image/png;base64,image-output-secret"}},
	} {
		if err := insertSQLiteRunEvent(ctx, tx, latest, 1, event.kind, event.payload, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ query, run, role string }{
		{"old-history-marker", oldest, "user"},
		{"old-output-marker", oldest, "assistant"},
		{"prompt-visible-marker", latest, "user"},
		{"末端ω-marker", latest, "assistant"},
	} {
		scan := &projectSearchScan{limits: defaultProjectSearchScanLimits()}
		page, err := f.repository.searchProjectContent(ctx, f.user.ID, f.project.ID, test.query, 50, scan)
		if err != nil {
			t.Fatal(err)
		}
		requireProjectSearchHit(t, page, test.run, test.role)
		if page.Truncated || scan.runRows != 230 || scan.eventRows < 1_202 || scan.bytes > scan.limits.scanBytes {
			t.Fatalf("unexpected scan bounds: page=%+v runs=%d events=%d bytes=%d", page, scan.runRows, scan.eventRows, scan.bytes)
		}
	}
	for _, query := range []string{
		"context-control-secret", "internal-block-secret", "image-text-secret", "image-filename-secret", "image-data-secret",
		"reasoning-private-secret", "tool-private-secret", "wrong-owner-secret", "internal-output-secret", "image-output-secret",
	} {
		page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, query, 50)
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("private query %q: page=%+v err=%v", query, page, err)
		}
	}
}

func TestSQLiteProjectSearchScanBudgetsAreLatestFirstAndExplicit(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 8; index++ {
		insertProjectSearchTestMemory(t, tx, f.project.ID, fmt.Sprintf("budget-memory-%d", index), "budget", "memory-marker", base.Add(time.Duration(index)*time.Second))
		id := fmt.Sprintf("budget-run-%d", index)
		insertProjectSearchTestRun(t, tx, f, id, []domain.ContentBlock{{Type: "text", Text: "run-marker"}}, base.Add(time.Duration(index)*time.Second))
		for event := 0; event < 10; event++ {
			text := "ordinary"
			if event == 0 {
				text = "omitted-old-output"
			}
			if event == 8 {
				text = "latest-"
			}
			if event == 9 {
				text = "tail-marker"
			}
			if err := insertSQLiteRunEvent(ctx, tx, id, 1, "output.delta", map[string]any{
				"provider_id": "codex", "item_id": "answer", "text": text,
			}, base.Add(time.Duration(index*10+event)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	limits := defaultProjectSearchScanLimits()
	limits.batchSize, limits.textRows, limits.runs, limits.runEvents, limits.events = 2, 3, 2, 3, 4
	for _, query := range []string{"memory-marker", "run-marker", "latest-tail-marker", "omitted-old-output", "no-match"} {
		scan := &projectSearchScan{limits: limits}
		page, err := f.repository.searchProjectContent(ctx, f.user.ID, f.project.ID, query, 50, scan)
		if err != nil {
			t.Fatal(err)
		}
		if !page.Truncated || scan.documentRows > 3*limits.textRows || scan.runRows != 2 || scan.eventRows != 4 || scan.bytes > limits.scanBytes {
			t.Fatalf("budget query %q: page=%+v docs=%d runs=%d events=%d bytes=%d", query, page, scan.documentRows, scan.runRows, scan.eventRows, scan.bytes)
		}
		if query == "memory-marker" && (len(page.Items) != 3 || page.Items[0].MemoryID != "budget-memory-7" || page.Items[2].MemoryID != "budget-memory-5") {
			t.Fatalf("memory candidates did not start newest: %+v", page)
		}
		if query == "run-marker" && (len(page.Items) != 2 || page.Items[0].RunID != "budget-run-7" || page.Items[1].RunID != "budget-run-6") {
			t.Fatalf("run candidates did not start newest: %+v", page)
		}
		if query == "latest-tail-marker" {
			requireProjectSearchHit(t, page, "budget-run-7", "assistant")
		}
		if (query == "omitted-old-output" || query == "no-match") && len(page.Items) != 0 {
			t.Fatalf("unexpected incomplete-window hit: %+v", page)
		}
	}
}

func TestSQLiteProjectSearchWireAndResultByteBudgets(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	insertProjectSearchTestMemory(t, tx, f.project.ID, "byte-oversized", "large", strings.Repeat("x", 1<<20)+"hidden-tail-marker", base)
	for index := 0; index < 12; index++ {
		insertProjectSearchTestMemory(t, tx, f.project.ID, fmt.Sprintf("byte-memory-%02d", index), strings.Repeat("名", 100), "retained-marker", base.Add(time.Duration(index)*time.Second))
	}
	insertProjectSearchTestRun(t, tx, f, "byte-run", []domain.ContentBlock{{Type: "text", Text: "work"}}, base)
	for index := 0; index < 8; index++ {
		text := strings.Repeat("旧", 200)
		if index == 7 {
			text = "latest-byte-marker"
		}
		if err := insertSQLiteRunEvent(ctx, tx, "byte-run", 1, "output.delta", map[string]any{
			"provider_id": "codex", "text": text,
		}, base.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	limits := defaultProjectSearchScanLimits()
	limits.batchSize, limits.runBytes, limits.scanBytes, limits.textBytes, limits.resultBytes = 3, 800, 8_000, 6_000, 1_000
	for _, query := range []string{"retained-marker", "latest-byte-marker", "hidden-tail-marker"} {
		scan := &projectSearchScan{limits: limits}
		page, err := f.repository.searchProjectContent(ctx, f.user.ID, f.project.ID, query, 50, scan)
		if err != nil {
			t.Fatal(err)
		}
		if !page.Truncated || scan.results.bytes > limits.resultBytes || scan.bytes > limits.scanBytes {
			t.Fatalf("byte budget query %q: page=%+v scan=%d retained=%d", query, page, scan.bytes, scan.results.bytes)
		}
		if query == "retained-marker" && (len(page.Items) == 0 || len(page.Items) >= 12 || page.Items[0].MemoryID != "byte-memory-11") {
			t.Fatalf("retained results not latest/bounded: %+v", page)
		}
		if query == "latest-byte-marker" {
			requireProjectSearchHit(t, page, "byte-run", "assistant")
		}
		if query == "hidden-tail-marker" && len(page.Items) != 0 {
			t.Fatalf("oversized document bypassed byte guard: %+v", page)
		}
	}
}

func TestSQLiteProjectSearchUnicodeLiteralStableOrderAndCrossProjectRunScope(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	otherUser, computer := insertSQLiteFeatureTenant(t, ctx, f.repository)
	otherProject, err := f.repository.CreateProject(ctx, otherUser, domain.Project{ComputerID: computer, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	otherIssue, err := f.repository.CreateIssue(ctx, otherUser, domain.Issue{ProjectID: otherProject.ID, Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for index := 99; index >= 0; index-- {
		insertProjectSearchTestMemory(t, tx, f.project.ID, fmt.Sprintf("unicode-%03d", index), "中文", "İSTANBUL ÉΛ literal%_ identical-marker", base)
	}
	insertProjectSearchTestMemory(t, tx, f.project.ID, "a-not-wildcard", "plain", "literalXX", base)
	insertProjectSearchTestRun(t, tx, f, "cross-project-run", []domain.ContentBlock{{Type: "text", Text: "cross-project-secret"}}, base)
	if _, err := tx.Exec(`UPDATE runs SET issue_id = ? WHERE id = 'cross-project-run'`, otherIssue.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"中文", "istanbul", "éλ", "%_", "identical-marker"} {
		for repeat := 0; repeat < 2; repeat++ {
			page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, query, 50)
			if err != nil || len(page.Items) != 50 || !page.Truncated {
				t.Fatalf("Unicode/literal query %q: page=%+v err=%v", query, page, err)
			}
			for index, item := range page.Items {
				if item.MemoryID != fmt.Sprintf("unicode-%03d", index) || !utf8.ValidString(item.Excerpt) {
					t.Fatalf("unstable/lossy query %q at %d: %+v", query, index, item)
				}
			}
		}
	}
	page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, "cross-project-secret", 50)
	if err != nil || len(page.Items) != 0 || page.Truncated {
		t.Fatalf("cross-project run leaked: page=%+v err=%v", page, err)
	}
	_, err = f.repository.SearchProjectContent(ctx, otherUser, f.project.ID, "中文", 50)
	assertDomainCode(t, err, "not_found")
}

type projectSearchCancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  atomic.Int64
}

func (c *projectSearchCancelContext) Err() error {
	if c.calls.Add(1) == 200 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSQLiteProjectSearchCancelsDuringCandidateScanAndClosesRows(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index := 0; index < 1_000; index++ {
		insertProjectSearchTestMemory(t, tx, f.project.ID, fmt.Sprintf("cancel-%04d", index), "cancel", strings.Repeat("中", 1_000), time.Now().UTC())
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &projectSearchCancelContext{Context: base, cancel: cancel}
	scan := &projectSearchScan{limits: defaultProjectSearchScanLimits()}
	page, err := f.repository.searchProjectContent(ctx, f.user.ID, f.project.ID, "absent", 50, scan)
	if !errors.Is(err, context.Canceled) || len(page.Items) != 0 || scan.documentRows == 0 || scan.documentRows >= 1_000 {
		t.Fatalf("mid-scan cancel: err=%v docs=%d page=%+v", err, scan.documentRows, page)
	}
	// database/sql has only one connection: an unclosed canceled row stream
	// would hang even a trivial subsequent query.
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var one int
	if err := f.repository.db.QueryRowContext(check, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("connection retained after cancel: one=%d err=%v", one, err)
	}
}

func TestSQLiteProjectSearchOnlyCurrentPublicContentAndNativeSnapshots(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Now().UTC()
	insertProjectSearchTestMemory(t, tx, f.project.ID, "public-memory", "public", "all-public-marker", base)
	insertProjectSearchTestMemory(t, tx, f.project.ID, "archived-memory", "archived", "archived-private-marker", base)
	if _, err := tx.Exec(`UPDATE project_memory_entries SET deleted_at = ? WHERE id = 'archived-memory'`, sqliteTimeValue(base)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issues SET description = 'all-public-marker' WHERE id = ?`, f.issue.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"published", "working", "failed"} {
		body := "all-public-marker " + status + "-comment-marker"
		if _, err := tx.Exec(`
			INSERT INTO issue_comments (id, issue_id, author_kind, author_user_id, body, status)
			VALUES (?, ?, 'user', ?, ?, ?)
		`, "public-status-"+status, f.issue.ID, f.user.ID, body, status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO issues (id, project_id, title, description, created_by_user_id, deleted_at)
		VALUES ('deleted-search-issue', ?, 'deleted', 'deleted-private-marker', ?, ?)
	`, f.project.ID, f.user.ID, sqliteTimeValue(base)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
		INSERT INTO issue_comments (id, issue_id, author_kind, author_user_id, body)
		VALUES ('deleted-issue-comment', 'deleted-search-issue', 'user', ?, 'deleted-private-marker')
	`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	insertProjectSearchTestRun(t, tx, f, "public-native-run", []domain.ContentBlock{{Type: "text", Text: "all-public-marker"}}, base)
	for _, payload := range []map[string]any{
		{"provider_id": "opencode", "kind": "text", "part_id": "visible", "text": "superseded-private-marker"},
		{"provider_id": "opencode", "kind": "reasoning", "part_id": "hidden", "text": "native-reasoning-private-marker"},
		{"provider_id": "opencode", "kind": "tool", "part_id": "tool", "text": "native-tool-private-marker"},
		{"provider_id": "opencode", "kind": "text", "part_id": "visible", "text": "all-public-marker"},
	} {
		if err := insertSQLiteRunEvent(ctx, tx, "public-native-run", 1, "provider.event", payload, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertSQLiteRunEvent(ctx, tx, "public-native-run", 1, "user.steer", map[string]any{
		"internal": false, "text": "all-public-marker", "attachments": []any{map[string]any{"data_url": "data:image/png;base64,steer-image-private-marker"}},
	}, base); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, "all-public-marker", 50)
	if err != nil || page.Truncated || len(page.Items) != 6 {
		t.Fatalf("public content contract: page=%+v err=%v", page, err)
	}
	counts := map[string]int{}
	for _, item := range page.Items {
		counts[item.Kind]++
	}
	if counts[domain.ProjectSearchMemory] != 1 || counts[domain.ProjectSearchIssueDescription] != 1 ||
		counts[domain.ProjectSearchIssueComment] != 1 || counts[domain.ProjectSearchSessionMessage] != 3 {
		t.Fatalf("four public kinds changed: %+v", counts)
	}
	for _, query := range []string{"archived-private-marker", "deleted-private-marker", "working-comment-marker", "failed-comment-marker", "superseded-private-marker", "native-reasoning-private-marker", "native-tool-private-marker", "steer-image-private-marker"} {
		page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, query, 50)
		if err != nil || len(page.Items) != 0 || page.Truncated {
			t.Fatalf("non-visible content %q: page=%+v err=%v", query, page, err)
		}
	}
}

func TestSQLiteProjectSearchOversizedEventIsAnExplicitStreamGap(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	base := time.Now().UTC()
	insertProjectSearchTestRun(t, tx, f, "gap-run", []domain.ContentBlock{{Type: "text", Text: "work"}}, base)
	for _, text := range []string{"left-", strings.Repeat("x", 2_000) + "oversized-secret", "right-marker"} {
		if err := insertSQLiteRunEvent(ctx, tx, "gap-run", 1, "output.delta", map[string]any{
			"provider_id": "codex", "text": text,
		}, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"left-right-marker", "oversized-secret", "right-marker"} {
		limits := defaultProjectSearchScanLimits()
		limits.documentBytes = 1_000
		scan := &projectSearchScan{limits: limits}
		page, err := f.repository.searchProjectContent(ctx, f.user.ID, f.project.ID, query, 50, scan)
		if err != nil || !page.Truncated {
			t.Fatalf("skipped event not reported: page=%+v err=%v", page, err)
		}
		if query == "right-marker" {
			requireProjectSearchHit(t, page, "gap-run", "assistant")
		} else if len(page.Items) != 0 {
			t.Fatalf("oversized event crossed a boundary or leaked: %+v", page)
		}
	}
}

func TestProjectSearchAssistantUsesOfficialProjectionAndLatestReplacement(t *testing.T) {
	a := newProjectSearchAssistant(4096)
	base := time.Now().UTC()
	for index, event := range []domain.RunEvent{
		{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "part", "text": "旧的 streaming-secret"}},
		{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "part_id": "part", "kind": "text", "text": "visible replaced answer"}},
		{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "part_id": "part", "kind": "text", "text": "latest-visible-answer"}},
		{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "part_id": "private", "kind": "reasoning", "text": "reasoning-secret"}},
		{Type: "provider.item", Payload: map[string]any{"provider_id": "opencode", "item": map[string]any{"type": "agentMessage", "text": "wrong-provider-secret"}}},
	} {
		event.ID = fmt.Sprintf("projection-%d", index)
		event.OccurredAt = base.Add(time.Duration(index) * time.Second)
		a.consume(event)
	}
	messages := a.finish("projection-run")
	if len(messages) != 1 || messages[0].text != "latest-visible-answer" || messages[0].anchor != "assistant:part" {
		t.Fatalf("superseded/private projection became searchable: %+v", messages)
	}
}
