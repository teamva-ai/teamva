package toolservice

import (
	"fmt"
	"sync"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteClosedIssueAllowsLateSummaryButFencesNewDelegation(t *testing.T) {
	for _, status := range []string{domain.IssueClosed, domain.IssueResolved} {
		t.Run(status, func(t *testing.T) {
			f := newSQLiteToolFixture(t)
			run := f.run(t, f.a, f.issue, "active-before-close")
			before := f.publish(t, run, "before-close", "delegate before close", f.b.ID)
			commentID := before.Result.Data["comment_id"].(string)
			issue, err := f.s.UpdateIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, func(issue *domain.Issue) { issue.Status = status })
			if err != nil {
				t.Fatal(err)
			}
			replay := New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(run, "before-close", "delegate before close", f.b.ID))
			if !replay.Result.OK || replay.ActivationIssueID != "" || len(f.mentions(t)) != 1 {
				t.Fatalf("closed receipt replay duplicated delegation: %+v", replay)
			}
			request := commentRequest(run, "late-delegation", "must roll back new body", f.b.ID)
			for _, appendNew := range []bool{false, true} {
				request.Payload["append_new"] = appendNew
				result := New(f.s).Execute(t.Context(), f.computer.ID, request)
				if result.Result.OK || result.Result.ErrorCode != "issue_not_open" || result.ActivationIssueID != "" {
					t.Fatalf("late delegation=%+v", result)
				}
			}
			comment := f.comment(t, commentID)
			revisions, err := f.s.ListIssueCommentRevisions(t.Context(), f.user.ID, commentID)
			if err != nil || comment.Body != "delegate before close" || comment.Version != 1 || len(revisions) != 0 || len(f.mentions(t)) != 1 {
				t.Fatalf("rejected delegation partially wrote: comment=%+v revisions=%+v err=%v", comment, revisions, err)
			}
			// The legacy server workflow must not provide a second delegation path.
			if err := f.s.AddIssueCommentMentions(t.Context(), f.user.ID, commentID, f.a.ID, []string{f.b.ID}); err == nil {
				t.Fatal("direct workflow delegated on ended Issue")
			}
			late := f.publish(t, run, "late-summary", "completed work before closure")
			if late.ActivationIssueID != "" || late.Result.Data["comment_id"] != commentID || len(f.mentions(t)) != 1 {
				t.Fatalf("late summary=%+v", late)
			}
			current, err := f.s.GetRun(t.Context(), f.user.ID, run.ID)
			if err != nil || current.Status != run.Status {
				t.Fatalf("Issue closure fabricated a Run cancellation: %+v %v", current, err)
			}
			issue, err = f.s.UpdateIssue(t.Context(), f.user.ID, issue.ID, issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueOpen })
			if err != nil {
				t.Fatal(err)
			}
			// The refused request never committed a receipt; explicit reopening
			// allows that same request to succeed exactly once.
			request.Payload["append_new"] = false
			if result := New(f.s).Execute(t.Context(), f.computer.ID, request); !result.Result.OK || result.ActivationIssueID != issue.ID || len(f.mentions(t)) != 2 {
				t.Fatalf("explicit reopen failed to restore delegation: %+v", result)
			}
		})
	}
}

func TestSQLiteConcurrentIssueCloseFencesDelegationWithoutDiscardingSummaries(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "close-write-race")
	f.publish(t, run, "initial", "initial summary")
	// Observation trigger only: the transaction, not this trigger, must reject
	// a delegation if Issue closure linearized first.
	if _, err := f.db.ExecContext(t.Context(), `
		CREATE TABLE test_delegation_issue_status (status TEXT NOT NULL);
		CREATE TRIGGER test_observe_delegation AFTER INSERT ON issue_comment_mentions
		BEGIN
		    INSERT INTO test_delegation_issue_status
		    SELECT issue.status FROM issues issue JOIN issue_comments comment ON comment.issue_id = issue.id
		    WHERE comment.id = NEW.comment_id;
		END;
	`); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	start := make(chan struct{})
	results := make(chan Execution, workers)
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(run, fmt.Sprintf("close-race-%d", index), "summary and delegation", f.b.ID))
		}()
	}
	close(start)
	if _, err := f.s.UpdateIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueClosed }); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.Result.OK {
			successes++
		} else if result.Result.ErrorCode != "issue_not_open" {
			t.Fatalf("unexpected close-race result: %+v", result)
		}
	}
	var invalidWrites int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM test_delegation_issue_status WHERE status <> 'open'`).Scan(&invalidWrites); err != nil {
		t.Fatal(err)
	}
	if invalidWrites != 0 || len(f.mentions(t)) != successes {
		t.Fatalf("close fence violated: invalidWrites=%d mentions=%d successes=%d", invalidWrites, len(f.mentions(t)), successes)
	}
	f.publish(t, run, "after-close-summary", "summary survives closure without new delegation")
}
