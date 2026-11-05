package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteIssueRunPagesIncludeLatestAndCompleteHistory(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	for index := 0; index < 205; index++ {
		id := fmt.Sprintf("history-run-%03d", index)
		stamp := sqliteTimeValue(time.Date(2026, 1, 1, 0, index, 0, 0, time.UTC))
		_, err := f.repository.db.ExecContext(t.Context(), `INSERT INTO runs
			(id,issue_id,issue_agent_instance_id,agent_id,project_id,status,input,idempotency_key,created_at,updated_at)
			VALUES(?,?,?,?,?,'succeeded','[]',?,?,?)`,
			id, f.issue.ID, f.instance.ID, f.agent.ID, f.project.ID, id, stamp, stamp)
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		page, err := f.repository.ListRunsByIssuePage(t.Context(), f.user.ID, f.issue.ID, cursor, 17)
		if err != nil {
			t.Fatal(err)
		}
		if pageNumber == 0 && (len(page.Items) != 17 || page.Items[0].ID != "history-run-204") {
			t.Fatal("first page did not contain the newest run")
		}
		for _, run := range page.Items {
			if seen[run.ID] {
				t.Fatal("cursor replayed a run")
			}
			seen[run.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if pageNumber >= 20 {
			t.Fatal("pagination did not progress")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 205 {
		t.Fatalf("history truncated to %d runs", len(seen))
	}
	if _, err := f.repository.ListRunsByIssuePage(t.Context(), "other-user", f.issue.ID, "", 17); err == nil {
		t.Fatal("cross-user history visible")
	}
	if _, err := f.repository.ListRunsByIssuePage(t.Context(), f.user.ID, f.issue.ID, "invalid-cursor", 17); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestSQLiteIssuePromptHistoryIsBoundedAndKeepsRecentPublished(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	for index := 0; index < 105; index++ {
		body := strings.Repeat("界", 5000)
		if index == 104 {
			body = "latest-important-instruction"
		}
		_, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
			IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: body,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	comments, truncated, err := f.repository.ListIssuePromptComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(comments) != 100 || comments[len(comments)-1].Body != "latest-important-instruction" {
		t.Fatalf("recent history missing: items=%d truncated=%t", len(comments), truncated)
	}
	for _, comment := range comments {
		if !utf8.ValidString(comment.Body) || len([]rune(comment.Body)) > 4097 || comment.Status != "published" {
			t.Fatal("body budget or public status violated")
		}
	}
	if _, _, err := f.repository.ListIssuePromptComments(t.Context(), "other-user", f.issue.ID); err == nil {
		t.Fatal("history ownership not checked")
	}
}
