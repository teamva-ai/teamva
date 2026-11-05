package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func setSQLiteExecutionIssueStatus(t *testing.T, f *sqliteExecutionFixture, status string) {
	t.Helper()
	issue, err := f.repository.UpdateIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, func(issue *domain.Issue) {
		issue.Status = status
	})
	if err != nil {
		t.Fatal(err)
	}
	f.issue = issue
}

func addSQLiteExecutionMention(t *testing.T, f sqliteExecutionFixture) domain.IssueCommentMention {
	t.Helper()
	comment, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "start a new conversation",
	}, []string{f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repository.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range mentions {
		if mention.CommentID == comment.ID {
			return mention
		}
	}
	t.Fatal("created mention missing")
	return domain.IssueCommentMention{}
}

func requireSQLiteExecutionError(t *testing.T, err error, code string) {
	t.Helper()
	var businessError *domain.Error
	if !errors.As(err, &businessError) || businessError.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestSQLiteEndedIssueSkipsMentionAndRequiresExplicitReopen(t *testing.T) {
	for _, status := range []string{domain.IssueClosed, domain.IssueResolved} {
		t.Run(status, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			mention := addSQLiteExecutionMention(t, f)
			setSQLiteExecutionIssueStatus(t, &f, status)
			now := time.Now().UTC().Add(time.Second)
			for _, at := range []time.Time{now, now.Add(24 * time.Hour)} {
				if claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), at, time.Minute); err != nil || ok {
					t.Fatalf("ended Issue was claimed: claim=%+v ok=%v err=%v", claim, ok, err)
				}
			}
			input := []domain.ContentBlock{{Type: "text", Text: "do work"}}
			_, err := f.repository.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.agent.ID, mention.ID, input, "mention:"+mention.ID)
			requireSQLiteExecutionError(t, err, "issue_not_open")
			for index, run := range []domain.Run{
				{AgentID: f.agent.ID, Input: input},
				{AgentID: f.agent.ID, IssueAgentInstanceID: f.instance.ID, Input: input},
				{AgentID: f.agent.ID, ReadOnly: true, Input: input},
			} {
				_, _, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, fmt.Sprintf("manual-%d", index), run)
				requireSQLiteExecutionError(t, err, "issue_not_open")
			}
			_, err = f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
				IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "must not enqueue more work",
			}, []string{f.agent.ID})
			requireSQLiteExecutionError(t, err, "issue_not_open")
			// Discussion remains possible without implicitly reopening the Issue.
			if _, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
				IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "closed discussion",
			}, nil); err != nil {
				t.Fatal(err)
			}
			mentions, err := f.repository.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
			if err != nil || len(mentions) != 1 || mentions[0].ID != mention.ID || mentions[0].ConsumedAt != nil {
				t.Fatalf("pending work discarded or changed: mentions=%+v err=%v", mentions, err)
			}
			var mentionStatus string
			var attempts int
			if err := f.repository.db.QueryRowContext(t.Context(), `SELECT status, attempts FROM issue_comment_mentions WHERE id = ?`, mention.ID).Scan(&mentionStatus, &attempts); err != nil {
				t.Fatal(err)
			}
			if mentionStatus != "pending" || attempts != 0 {
				t.Fatalf("closed work was retried or discarded: status=%s attempts=%d", mentionStatus, attempts)
			}
			var runs, starts, generations, instances int
			if err := f.repository.db.QueryRowContext(t.Context(), `
				SELECT (SELECT count(*) FROM runs),
				       (SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'),
				       (SELECT count(*) FROM issue_agent_generations),
				       (SELECT count(*) FROM issue_agent_instances)
			`).Scan(&runs, &starts, &generations, &instances); err != nil {
				t.Fatal(err)
			}
			if runs != 0 || starts != 0 || generations != 0 || instances != 1 {
				t.Fatalf("rejected execution leaked writes: runs=%d starts=%d generations=%d instances=%d", runs, starts, generations, instances)
			}
			issue, err := f.repository.GetIssue(t.Context(), f.user.ID, f.issue.ID)
			if err != nil || issue.Status != status || issue.Version != f.issue.Version {
				t.Fatalf("dispatch implicitly reopened Issue: %+v %v", issue, err)
			}
			// Explicit user reopening restores the saved queue, not a hidden retry
			// timer or a transport replay of a previously created Run.
			setSQLiteExecutionIssueStatus(t, &f, domain.IssueOpen)
			claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), now, time.Minute)
			if err != nil || !ok || claim.MentionID != mention.ID {
				t.Fatalf("explicit reopen did not restore pending work: %+v %v %v", claim, ok, err)
			}
			if _, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, input, "mention:"+mention.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSQLiteClosedMentionHeadDoesNotStarveOpenIssue(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	blocked := addSQLiteExecutionMention(t, f)
	setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
	healthy := f
	var err error
	healthy.issue, err = f.repository.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: "healthy"})
	if err != nil {
		t.Fatal(err)
	}
	ready := addSQLiteExecutionMention(t, healthy)
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE issue_comment_mentions SET available_at = ? WHERE id = ?`,
		sqliteTimeValue(time.Now().UTC().Add(-time.Hour)), blocked.ID); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
	if err != nil || !ok || claim.MentionID != ready.ID || claim.IssueID != healthy.issue.ID {
		t.Fatalf("closed queue head starved healthy Issue: %+v %v %v", claim, ok, err)
	}
	if _, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "healthy work"}}, "mention:"+ready.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute); err != nil || ok {
		t.Fatalf("closed queue head subsequently claimed: %v %v", ok, err)
	}
}

func TestSQLiteCloseAfterMentionClaimFencesCreationAndLeaseRecovery(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	mention := addSQLiteExecutionMention(t, f)
	now := time.Now().UTC().Add(time.Second)
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claim, ok, err)
	}
	setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
	input := []domain.ContentBlock{{Type: "text", Text: "work"}}
	_, err = f.repository.CreateRunForClaimedMention(t.Context(), claim, input, "mention:"+mention.ID)
	requireSQLiteExecutionError(t, err, "issue_not_open")
	var status, token string
	var consumed any
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT status, claim_token, consumed_at FROM issue_comment_mentions WHERE id = ?`,
		mention.ID).Scan(&status, &token, &consumed); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || token != claim.ClaimToken || consumed != nil {
		t.Fatalf("failed creation consumed or lost claim: status=%s token changed=%v consumed=%v", status, token != claim.ClaimToken, consumed)
	}
	// A process crash before dispatcher retry is also safe: lease recovery must
	// not turn closed work into a runnable claim or silently dead-letter it.
	if _, ok, err := f.repository.ClaimNextIssueMention(t.Context(), now.Add(2*time.Minute), time.Minute); err != nil || ok {
		t.Fatalf("expired claim on closed Issue dispatched: %v %v", ok, err)
	}
	setSQLiteExecutionIssueStatus(t, &f, domain.IssueOpen)
	renewed, ok, err := f.repository.ClaimNextIssueMention(t.Context(), now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok || renewed.ClaimToken == claim.ClaimToken || renewed.MentionID != mention.ID {
		t.Fatalf("reopen/reclaim=%+v %v %v", renewed, ok, err)
	}
	_, err = f.repository.CreateRunForClaimedMention(t.Context(), claim, input, "mention:"+mention.ID)
	requireSQLiteExecutionError(t, err, "mention_claim_lost")
	if _, err := f.repository.CreateRunForClaimedMention(t.Context(), renewed, input, "mention:"+mention.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteClosedIssueRunReplayPreservesExistingResults(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	manual := f.createRun(t, "manual-existing")
	if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "started-before-close", RunID: manual.ID, LeaseEpoch: manual.LeaseEpoch,
		Sequence: 1, Type: "run.started", OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	var err error
	manual, err = f.repository.GetRun(t.Context(), f.user.ID, manual.ID)
	if err != nil || manual.Status != domain.RunRunning {
		t.Fatalf("fixture running Run=%+v %v", manual, err)
	}
	mention := addSQLiteExecutionMention(t, f)
	input := []domain.ContentBlock{{Type: "text", Text: "work"}}
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v %v %v", claim, ok, err)
	}
	delegated, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, input, "mention:"+mention.ID)
	if err != nil {
		t.Fatal(err)
	}
	setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
	for range 3 {
		replay, replayed, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, 1, "manual-existing", domain.Run{})
		if err != nil || !replayed || replay.ID != manual.ID || replay.Status != manual.Status {
			t.Fatalf("manual replay=%+v %v %v", replay, replayed, err)
		}
		for _, claimed := range []bool{false, true} {
			var replay domain.Run
			if claimed {
				replay, err = f.repository.CreateRunForClaimedMention(t.Context(), claim, input, "mention:"+mention.ID)
			} else {
				replay, err = f.repository.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.agent.ID, mention.ID, input, "mention:"+mention.ID)
			}
			if err != nil || replay.ID != delegated.ID || replay.Status != delegated.Status {
				t.Fatalf("mention replay=%+v %v", replay, err)
			}
		}
	}
	runs, err := f.repository.ListRunsByIssue(t.Context(), f.user.ID, f.issue.ID, 100)
	if err != nil || len(runs) != 2 {
		t.Fatalf("replay duplicated runs: %+v %v", runs, err)
	}
	var starts, stops int
	if err := f.repository.db.QueryRowContext(t.Context(), `
		SELECT (SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'),
		       (SELECT count(*) FROM command_outbox WHERE command_name = 'run.stop')
	`).Scan(&starts, &stops); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || stops != 0 {
		t.Fatalf("closing/replay changed execution history: starts=%d stops=%d", starts, stops)
	}
}

func TestSQLiteExplicitReopenCannotBypassPausedOrDeletedInstance(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			run := f.createRun(t, "existing")
			if deleted {
				if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE issue_agent_instances SET deleted_at = ? WHERE id = ?`, sqliteTimeValue(time.Now()), f.instance.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				instance, err := f.repository.GetIssueAgentInstanceByID(t.Context(), f.user.ID, f.instance.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, instance.ID, instance.Version, func(instance *domain.IssueAgentInstance) {
					instance.Status = domain.AgentInstancePaused
				}); err != nil {
					t.Fatal(err)
				}
			}
			setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
			setSQLiteExecutionIssueStatus(t, &f, domain.IssueOpen)
			_, _, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, "new-after-reopen", domain.Run{
				AgentID: f.agent.ID, IssueAgentInstanceID: f.instance.ID, Input: []domain.ContentBlock{{Type: "text", Text: "work"}},
			})
			code := "agent_instance_paused"
			if deleted {
				code = "not_found"
			}
			requireSQLiteExecutionError(t, err, code)
			replay, replayed, err := f.repository.CreateRun(t.Context(), f.user.ID, f.issue.ID, 1, "existing", domain.Run{})
			if err != nil || !replayed || replay.ID != run.ID {
				t.Fatalf("existing replay=%+v %v %v", replay, replayed, err)
			}
		})
	}
}

func TestSQLiteConcurrentIssueCloseNeverCommitsNewMentionRunOnClosedIssue(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	const workers = 16
	claims := make([]IssueMentionClaim, workers)
	for index := range workers {
		addSQLiteExecutionMention(t, f)
		claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim=%+v %v %v", claim, ok, err)
		}
		claims[index] = claim
	}
	// Observation only: this does not guard against or repair an invalid write.
	if _, err := f.repository.db.ExecContext(t.Context(), `
		CREATE TABLE test_run_issue_status (status TEXT NOT NULL);
		CREATE TRIGGER test_observe_issue_run AFTER INSERT ON runs
		BEGIN
		    INSERT INTO test_run_issue_status SELECT status FROM issues WHERE id = NEW.issue_id;
		END;
	`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for _, claim := range claims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "racing work"}}, "mention:"+claim.MentionID)
			results <- err
		}()
	}
	close(start)
	setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			requireSQLiteExecutionError(t, err, "issue_not_open")
		}
	}
	var invalidWrites int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM test_run_issue_status WHERE status <> 'open'`).Scan(&invalidWrites); err != nil {
		t.Fatal(err)
	}
	if invalidWrites != 0 {
		t.Fatalf("runs inserted after close committed: %d", invalidWrites)
	}
	for _, claim := range claims {
		// Same-key replay may return work already created before the close;
		// a fresh key may never create anything after it.
		_, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "late work"}}, "new-key:"+claim.MentionID)
		var businessError *domain.Error
		if !errors.As(err, &businessError) || (businessError.Code != "issue_not_open" && businessError.Code != "mention_consumed") {
			t.Fatalf("post-close fresh creation=%v", err)
		}
	}
}
