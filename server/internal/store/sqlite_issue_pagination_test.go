package store

import (
	"fmt"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteIssuePagesBoundRowsAndSearchWholeProject(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	ctx := t.Context()
	p, err := f.repository.CreateProject(ctx, f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "paged issues"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.db.ExecContext(ctx, `
		WITH RECURSIVE numbers(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM numbers WHERE n<259)
		INSERT INTO issues(id, project_id, title, status, created_by_user_id, created_at, updated_at)
		SELECT printf('page-%03d',n), ?, printf('Task %03d',n), CASE WHEN n%2=0 THEN 'open' ELSE 'resolved' END,
		       ?, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z' FROM numbers`, p.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE issues SET updated_at='2026-01-03T00:00:00Z' WHERE id='page-003'`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for offset := 0; offset < 300; offset += 100 {
		page, err := f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 100, Offset: offset})
		want := min(100, 260-offset)
		if err != nil || page.Total != 260 || len(page.Items) != want {
			t.Fatalf("offset %d: total=%d items=%d err=%v", offset, page.Total, len(page.Items), err)
		}
		if offset == 0 && (page.Items[0].ID != "page-003" || page.Items[1].ID != "page-259") {
			t.Fatalf("latest/tie order: %+v", page.Items[:2])
		}
		for _, issue := range page.Items {
			if seen[issue.ID] {
				t.Fatalf("duplicate issue %s across pages", issue.ID)
			}
			seen[issue.ID] = true
		}
	}
	if len(seen) != 260 {
		t.Fatalf("missing issues: %d", len(seen))
	}
	all, err := f.repository.ListIssues(ctx, f.user.ID, p.ID)
	if err != nil || len(all) != 260 || all[0].ID != "page-000" {
		t.Fatalf("legacy ListIssues changed: items=%d err=%v", len(all), err)
	}
	page, err := f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 2, Search: " task 00 ", Status: "resolved"})
	if err != nil || page.Total != 5 || len(page.Items) != 2 || page.Items[0].ID != "page-003" {
		t.Fatalf("whole-project filters: %+v err=%v", page, err)
	}
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE issues SET title='ÄPFEL 100%_中' WHERE id='page-000'`); err != nil {
		t.Fatal(err)
	}
	for _, search := range []string{"äpfel", "100%_", "中"} {
		page, err := f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 100, Search: search})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "page-000" {
			t.Fatalf("literal Unicode search %q: %+v err=%v", search, page, err)
		}
	}
	// A malformed row outside LIMIT must never be scanned/materialized by the page reader.
	if _, err := f.repository.db.ExecContext(ctx, `UPDATE issues SET created_at='invalid' WHERE id='page-000'`); err != nil {
		t.Fatal(err)
	}
	page, err = f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 100})
	if err != nil || len(page.Items) != 100 || page.Total != 260 {
		t.Fatalf("page scanned outside SQL LIMIT: items=%d total=%d err=%v", len(page.Items), page.Total, err)
	}
	if _, err := f.repository.ListIssues(ctx, f.user.ID, p.ID); err == nil {
		t.Fatal("fixture must make an unbounded row scan fail")
	}
}

func TestSQLiteIssuePageValidationAndOwnership(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	ctx := t.Context()
	p, err := f.repository.CreateProject(ctx, f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "private issues"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := f.repository.CreateIssue(ctx, f.user.ID, domain.Issue{ProjectID: p.ID, Title: "private"})
	if err != nil {
		t.Fatal(err)
	}
	for i, q := range []IssueListQuery{{Limit: 0}, {Limit: 101}, {Limit: 1, Offset: -1}, {Limit: 1, Offset: 1_000_001}, {Limit: 1, Status: "unknown"}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, q)
			assertDomainCode(t, err, "invalid_argument")
		})
	}
	otherUser, _ := seedSQLiteOtherIdentity(t, f.repository, "issue-page-other")
	_, err = f.repository.ListIssuesPage(ctx, otherUser, p.ID, IssueListQuery{Limit: 1})
	assertDomainCode(t, err, "not_found")
	page, err := f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 1, Offset: 100})
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatalf("empty trailing page: %+v err=%v", page, err)
	}
	if err := f.repository.DeleteIssue(ctx, f.user.ID, issue.ID, issue.Version); err != nil {
		t.Fatal(err)
	}
	page, err = f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 1})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("deleted issue in page: %+v err=%v", page, err)
	}
	if err := f.repository.DeleteProject(ctx, f.user.ID, p.ID, p.Version); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.ListIssuesPage(ctx, f.user.ID, p.ID, IssueListQuery{Limit: 1})
	assertDomainCode(t, err, "not_found")
}
