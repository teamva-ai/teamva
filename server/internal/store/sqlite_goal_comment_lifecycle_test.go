package store

import (
	"errors"
	"sync"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func assertSQLiteGoalSummary(t *testing.T, f sqliteExecutionFixture, goal domain.IssueAgentGoal, status, body string) domain.IssueComment {
	t.Helper()
	comment, err := f.repository.GetIssueComment(t.Context(), f.user.ID, goal.SummaryCommentID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_comments WHERE issue_id = ? AND deleted_at IS NULL`, goal.IssueID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || comment.Status != status || comment.Body != body {
		t.Fatalf("summary lifecycle: comments=%d status=%s want=%s body_matches=%t", count, comment.Status, status, comment.Body == body)
	}
	return comment
}

func writeSQLiteGoalSummary(t *testing.T, f sqliteExecutionFixture, run domain.Run, goal domain.IssueAgentGoal, body string, appendNew bool) RunIssueCommentResult {
	t.Helper()
	result, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID, RunIssueCommentWrite{
		Body: body, AppendNew: appendNew, IdempotencyKey: "summary-" + body,
	})
	if err != nil || result.CommentID != goal.SummaryCommentID {
		t.Fatalf("write must reuse Goal summary: comment=%s want=%s err=%v", result.CommentID, goal.SummaryCommentID, err)
	}
	return result
}

func sqliteGoalCommentContinuation(t *testing.T, f sqliteExecutionFixture, work domain.Run, goal domain.IssueAgentGoal) domain.Run {
	t.Helper()
	finishSQLiteIsolationRun(t, f, work, "model output is not a public comment")
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, goal.ID)
	finishSQLiteIsolationRun(t, f, audit, `{"verdict":"continue","note":"remaining"}`)
	advanceSQLiteIsolationGoals(t, f, false)
	return sqliteIsolationCurrentRun(t, f, goal.ID)
}

func TestSQLiteGoalCommentLifecycleReusesSummaryThroughContinuationAndFinalize(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "[teamva:goal-working]")
	writeSQLiteGoalSummary(t, f, work, goal, "work progress", true)
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "work progress")
	continuation := sqliteGoalCommentContinuation(t, f, work, goal)
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "work progress")
	writeSQLiteGoalSummary(t, f, continuation, goal, "continued progress", true)
	writeSQLiteGoalSummary(t, f, continuation, goal, "continued progress updated", false)
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "continued progress updated")
	finishSQLiteIsolationRun(t, f, continuation, "completion claim alone does not publish")
	advanceSQLiteIsolationGoals(t, f, false)
	audit := sqliteIsolationCurrentRun(t, f, goal.ID)
	if audit.Purpose != domain.RunPurposeGoalAudit {
		t.Fatalf("purpose=%s, want audit", audit.Purpose)
	}
	before := snapshotSQLiteGoalResumeState(t, f.repository)
	if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, audit.ID,
		RunIssueCommentWrite{Body: "audit must not publish", AppendNew: true}); err == nil {
		t.Fatal("independent audit wrote a public comment")
	}
	if after := snapshotSQLiteGoalResumeState(t, f.repository); before != after {
		t.Fatal("rejected audit mutated Goal/comment state")
	}
	finishSQLiteIsolationRun(t, f, audit, `{"verdict":"complete","note":"verified"}`)
	advanceSQLiteIsolationGoals(t, f, false)
	finalize := sqliteIsolationCurrentRun(t, f, goal.ID)
	if finalize.Purpose != domain.RunPurposeGoalFinalize {
		t.Fatalf("purpose=%s, want finalize", finalize.Purpose)
	}
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "continued progress updated")
	writeSQLiteGoalSummary(t, f, finalize, goal, "explicit tool final summary", true)
	comment := assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentPublished, "explicit tool final summary")
	if comment.RunID != work.ID {
		t.Fatalf("summary lost its original association: run=%s want=%s", comment.RunID, work.ID)
	}
	finishSQLiteIsolationRun(t, f, finalize, "different model text must not replace summary")
	advanceSQLiteIsolationGoals(t, f, false)
	if current := sqliteIsolationGoal(t, f, goal.ID); current.Status != domain.GoalCompleted {
		t.Fatalf("goal status=%s, want completed", current.Status)
	}
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentPublished, "explicit tool final summary")
	if changed := advanceSQLiteIsolationGoals(t, f, false); changed != 0 {
		t.Fatalf("completed Goal advanced again: %d", changed)
	}
}

func TestSQLiteGoalCommentLifecycleBudgetCancelFailureAndPause(t *testing.T) {
	for _, mode := range []string{"budget", "cancel", "failure", "pause"} {
		t.Run(mode, func(t *testing.T) {
			budget := int64(1_000)
			f, work, goal := createSQLiteGoalFixture(t, &budget)
			t.Cleanup(func() { _ = f.repository.Close() })
			writeSQLiteGoalSummary(t, f, work, goal, "initial progress", false)
			continuation := sqliteGoalCommentContinuation(t, f, work, goal)
			writeSQLiteGoalSummary(t, f, continuation, goal, "latest progress", true)
			current := sqliteIsolationGoal(t, f, goal.ID)
			wantGoal, wantComment := domain.GoalBlocked, domain.IssueCommentFailed
			switch mode {
			case "budget":
				applySQLiteGoalEvents(t, f.repository, continuation,
					nodeprotocol.RunEvent{Type: "usage.updated", Payload: map[string]any{"total_tokens": budget}},
					nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
				)
				wantGoal = domain.GoalBudgetReached
			case "cancel", "pause":
				var err error
				if mode == "cancel" {
					_, err = f.repository.CancelIssueAgentGoal(t.Context(), f.user.ID, goal.ID, current.Version)
					wantGoal = domain.GoalCancelled
				} else {
					_, err = f.repository.PauseIssueAgentGoal(t.Context(), f.user.ID, goal.ID, current.Version)
					wantGoal, wantComment = domain.GoalPaused, domain.IssueCommentWorking
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID,
					RunIssueCommentWrite{Body: "late write", AppendNew: true}); err == nil {
					t.Fatal("stopping run accepted a new public comment")
				}
				applySQLiteGoalEvents(t, f.repository, continuation,
					nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}})
			default:
				applySQLiteGoalEvents(t, f.repository, continuation,
					nodeprotocol.RunEvent{Type: "run.error", Payload: map[string]any{"status": "failed"}})
			}
			advanceSQLiteIsolationGoals(t, f, false)
			current = sqliteIsolationGoal(t, f, goal.ID)
			if current.Status != wantGoal {
				t.Fatalf("goal status=%s, want=%s", current.Status, wantGoal)
			}
			assertSQLiteGoalSummary(t, f, goal, wantComment, "latest progress")
			if mode == "pause" || mode == "failure" {
				if _, err := f.repository.ResumeIssueAgentGoal(t.Context(), f.user.ID, goal.ID, current.Version); err != nil {
					t.Fatal(err)
				}
				advanceSQLiteIsolationGoals(t, f, false)
				resumed := sqliteIsolationCurrentRun(t, f, goal.ID)
				writeSQLiteGoalSummary(t, f, resumed, goal, "resumed progress", true)
				assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "resumed progress")
			}
		})
	}
}

func TestSQLiteGoalCommentLifecycleFinalizeWithoutToolDoesNotPublish(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	writeSQLiteGoalSummary(t, f, work, goal, "progress only", true)
	finishSQLiteIsolationRun(t, f, work, "done")
	advanceSQLiteIsolationGoals(t, f, false)
	finishSQLiteIsolationRun(t, f, sqliteIsolationCurrentRun(t, f, goal.ID), `{"verdict":"complete","note":"verified"}`)
	advanceSQLiteIsolationGoals(t, f, false)
	for range 2 {
		finalize := sqliteIsolationCurrentRun(t, f, goal.ID)
		assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, "progress only")
		finishSQLiteIsolationRun(t, f, finalize, "model claims summary was published, but never called the tool")
		advanceSQLiteIsolationGoals(t, f, false)
	}
	if current := sqliteIsolationGoal(t, f, goal.ID); current.Status != domain.GoalBlocked {
		t.Fatalf("goal status=%s, want blocked", current.Status)
	}
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentFailed, "progress only")
}

func TestSQLiteGoalCommentLifecycleConcurrentIdempotentWriteAndTerminalReplay(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	continuation := sqliteGoalCommentContinuation(t, f, work, goal)
	input := RunIssueCommentWrite{Body: "concurrent progress", AppendNew: true, IdempotencyKey: "same-progress"}
	type result struct {
		comment RunIssueCommentResult
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			comment, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID, input)
			results <- result{comment, err}
		}()
	}
	wg.Wait()
	close(results)
	fresh := 0
	for result := range results {
		if result.err != nil || result.comment.CommentID != goal.SummaryCommentID || result.comment.Version != 2 {
			t.Fatalf("concurrent write comment=%s version=%d err=%v", result.comment.CommentID, result.comment.Version, result.err)
		}
		if !result.comment.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh mutations=%d, want 1", fresh)
	}
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, input.Body)
	finishSQLiteIsolationRun(t, f, continuation, "finished")
	before := snapshotSQLiteGoalResumeState(t, f.repository)
	replayed, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID, input)
	if err != nil || !replayed.Replayed || replayed.CommentID != goal.SummaryCommentID {
		t.Fatalf("terminal replay comment=%s replayed=%t err=%v", replayed.CommentID, replayed.Replayed, err)
	}
	input.Body = "different request"
	_, err = f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID, input)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "idempotency_conflict" {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
		t.Fatal("terminal replay changed persisted state")
	}
}

func TestSQLiteGoalCommentLifecycleReceiptFailureRollsBackSummary(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	continuation := sqliteGoalCommentContinuation(t, f, work, goal)
	if _, err := f.repository.db.ExecContext(t.Context(), `CREATE TRIGGER goal_comment_receipt_failure
		BEFORE INSERT ON issue_tool_comment_requests BEGIN SELECT RAISE(ABORT, 'fixture receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := snapshotSQLiteGoalResumeState(t, f.repository)
	input := RunIssueCommentWrite{Body: "transactional progress", AppendNew: true, IdempotencyKey: "receipt-retry"}
	if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID, input); err == nil {
		t.Fatal("receipt failure unexpectedly succeeded")
	}
	if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
		t.Fatal("receipt failure left a changed or extra comment")
	}
	var revisions, receipts int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_comment_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_tool_comment_requests`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || receipts != 0 {
		t.Fatalf("failed transaction left revisions=%d receipts=%d", revisions, receipts)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER goal_comment_receipt_failure`); err != nil {
		t.Fatal(err)
	}
	writeSQLiteGoalSummary(t, f, continuation, goal, input.Body, input.AppendNew)
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentWorking, input.Body)
}

func TestSQLiteGoalCommentLifecycleMissingSummaryNeverFallsBackOrCreates(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	continuation := sqliteGoalCommentContinuation(t, f, work, goal)
	// A non-summary comment associated with this exact Run must not shadow
	// the Goal-owned summary. This also represents the old append_new defect.
	other, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: goal.IssueID, AuthorKind: domain.IssueCommentAuthorAgent,
		AuthorAgentID: f.agent.ID, RunID: continuation.ID, Body: "separate old comment",
		Status: domain.IssueCommentPublished,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeSQLiteGoalSummary(t, f, continuation, goal, "authoritative summary", false)
	unchanged, err := f.repository.GetIssueComment(t.Context(), f.user.ID, other.ID)
	if err != nil || unchanged.Version != other.Version || unchanged.Body != other.Body || unchanged.Status != other.Status {
		t.Fatalf("Goal write changed a different comment: err=%v", err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE issue_comments SET deleted_at = updated_at WHERE id = ?`, goal.SummaryCommentID); err != nil {
		t.Fatal(err)
	}
	before := snapshotSQLiteGoalResumeState(t, f.repository)
	for _, appendNew := range []bool{false, true} {
		_, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID,
			RunIssueCommentWrite{Body: "must not replace missing summary", AppendNew: appendNew})
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != "goal_summary_unavailable" {
			t.Fatalf("missing summary error=%v", err)
		}
	}
	if after := snapshotSQLiteGoalResumeState(t, f.repository); after != before {
		t.Fatal("missing summary rejection changed rows")
	}
}

func TestSQLiteGoalCommentLifecycleConcurrentCancelNeverLeavesWorkingComment(t *testing.T) {
	f, work, goal := createSQLiteGoalFixture(t, nil)
	t.Cleanup(func() { _ = f.repository.Close() })
	continuation := sqliteGoalCommentContinuation(t, f, work, goal)
	current := sqliteIsolationGoal(t, f, goal.ID)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, continuation.ID,
			RunIssueCommentWrite{Body: "racing progress", AppendNew: true, IdempotencyKey: "racing"})
		var de *domain.Error
		if errors.As(err, &de) && de.Code == "run_not_active" {
			err = nil // Cancel won the transaction; this write must be refused.
		}
		results <- err
	}()
	go func() {
		<-start
		_, err := f.repository.CancelIssueAgentGoal(t.Context(), f.user.ID, goal.ID, current.Version)
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	finishSQLiteIsolationRun(t, f, continuation, "late terminal output")
	advanceSQLiteIsolationGoals(t, f, false)
	comment, err := f.repository.GetIssueComment(t.Context(), f.user.ID, goal.SummaryCommentID)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteGoalSummary(t, f, goal, domain.IssueCommentFailed, comment.Body)
}

func TestSQLiteGoalCommentLifecycleDoesNotChangeOrdinaryAppendNew(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	run := f.createRun(t, "ordinary-summary")
	first, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID,
		RunIssueCommentWrite{Body: "ordinary summary"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID,
		RunIssueCommentWrite{Body: "explicit additional comment", AppendNew: true})
	if err != nil || first.CommentID == second.CommentID {
		t.Fatalf("ordinary append_new lost its behavior: err=%v", err)
	}
	for _, id := range []string{first.CommentID, second.CommentID} {
		comment, err := f.repository.GetIssueComment(t.Context(), f.user.ID, id)
		if err != nil || comment.Status != domain.IssueCommentPublished {
			t.Fatalf("ordinary public comment status=%s err=%v", comment.Status, err)
		}
	}
}

func TestSQLiteGoalCommentLifecycleConversationHasNoPublicIssueComment(t *testing.T) {
	f, graph := createConversationFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID,
		"conversation-comment-scope", "conversation only")
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.GetRun(t.Context(), f.user.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID,
		RunIssueCommentWrite{Body: "must not become an Issue comment", AppendNew: true}); err == nil {
		t.Fatal("branch conversation wrote a public Issue comment")
	}
	finishSQLiteIsolationRun(t, f, run, "conversation reply")
	var comments int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM issue_comments`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if comments != 0 {
		t.Fatalf("branch conversation created %d public comments", comments)
	}
}
