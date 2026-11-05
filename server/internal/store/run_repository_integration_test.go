package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteRunIdempotencyIsIssueAndOwnerScoped(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	first := f.createRun(t, "shared-key")
	issue, err := f.repository.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: "other issue"})
	if err != nil {
		t.Fatal(err)
	}
	second, replayed, err := f.repository.CreateRun(t.Context(), f.user.ID, issue.ID, issue.Version, "shared-key", domain.Run{
		AgentID: f.agent.ID, Input: []domain.ContentBlock{{Type: "text", Text: "work"}},
	})
	if err != nil || replayed || second.ID == first.ID {
		t.Fatalf("issue scope: replay=%v err=%v", replayed, err)
	}
	other := seedSQLiteContractUser(t, f.repository, "other-idem@example.test")
	_, found, err := f.repository.GetRunByIssueIdempotency(t.Context(), other.ID, f.issue.ID, "shared-key")
	if found || !isNotFoundDomainError(err) {
		t.Fatalf("owner scope: found=%v err=%v", found, err)
	}
}

func TestSQLiteInstanceRunPaginationRetrievesMoreThan100(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	tx, err := f.repository.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	for index := 0; index < 237; index++ {
		createdAt := sqliteTimeValue(base.Add(-time.Duration(index/3) * time.Second))
		_, err = tx.ExecContext(t.Context(), `
			INSERT INTO runs (id, issue_id, issue_agent_instance_id, agent_id, project_id,
			status, input, idempotency_key, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'succeeded', '[]', ?, 1, ?, ?)
		`, fmt.Sprintf("run-page-%03d", index), f.issue.ID, f.instance.ID, f.agent.ID,
			f.project.ID, fmt.Sprintf("page-%03d", index), createdAt, createdAt)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	var previous domain.Run
	cursor := ""
	for {
		page, err := f.repository.ListRunsByInstancePage(t.Context(), f.user.ID, f.instance.ID, cursor, 37)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Items {
			if seen[run.ID] {
				t.Fatalf("duplicate run %s", run.ID)
			}
			if previous.ID != "" && (run.CreatedAt.After(previous.CreatedAt) || run.CreatedAt.Equal(previous.CreatedAt) && run.ID >= previous.ID) {
				t.Fatal("unstable pagination order")
			}
			seen[run.ID] = true
			previous = run
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 237 {
		t.Fatalf("retrieved %d runs, want 237", len(seen))
	}
}

func isNotFoundDomainError(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Code == "not_found"
}
