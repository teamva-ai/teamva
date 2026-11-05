package toolservice

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

type sqliteToolFixture struct {
	s        *store.SQLite
	db       *sql.DB
	user     domain.User
	computer domain.Computer
	provider domain.AgentService
	issue    domain.Issue
	a, b     domain.Agent
	path     string
}

func newSQLiteToolFixture(t *testing.T) *sqliteToolFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := store.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user, computer, err := s.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := s.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "tool tests"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: "tool tests"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &sqliteToolFixture{s: s, db: db, user: user, computer: computer, provider: provider, issue: issue, path: path}
	f.a, f.b = f.agent(t, "A"), f.agent(t, "B")
	return f
}

func TestSQLiteIssueGetUsesBoundedWindowAndExactScopedCommentReads(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "bounded-read")
	body := strings.Repeat("文", 5000)
	comment, err := f.s.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: "user", Body: body,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := New(f.s)
	request := nodeprotocol.NodeRequest{RunID: run.ID, Tool: "issue_get"}
	window := service.Execute(t.Context(), f.computer.ID, request)
	if !window.Result.OK || window.Result.Data["truncated"] != true {
		t.Fatalf("window=%+v", window.Result)
	}
	request.Payload = map[string]any{"comment_id": comment.ID}
	exact := service.Execute(t.Context(), f.computer.ID, request)
	if !exact.Result.OK {
		t.Fatalf("exact read=%+v", exact.Result)
	}
	comments := exact.Result.Data["comments"].([]map[string]any)
	if len(comments) != 1 || comments[0]["body"] != body || exact.Result.Data["truncated"] != false {
		t.Fatal("exact comment was clipped")
	}
	other, err := f.s.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: f.issue.ProjectID, Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.s.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: other.ID, AuthorKind: "user", Body: "other-task-content"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Payload["comment_id"] = foreign.ID
	if result := service.Execute(t.Context(), f.computer.ID, request); result.Result.OK || result.Result.ErrorCode != "comment_not_found" {
		t.Fatalf("cross-Issue comment read accepted: %+v", result.Result)
	}
	invalid := commentRequest(run, "invalid-append", "not published")
	invalid.Payload["append_new"] = "true"
	if result := service.Execute(t.Context(), f.computer.ID, invalid); result.Result.OK || result.Result.ErrorCode != "invalid_argument" {
		t.Fatalf("non-boolean append_new accepted: %+v", result.Result)
	}
	oversized := commentRequest(run, "oversized-body", strings.Repeat("x", (1<<20)+1))
	if result := service.Execute(t.Context(), f.computer.ID, oversized); result.Result.OK || result.Result.ErrorCode != "invalid_argument" {
		t.Fatalf("oversized body accepted: %+v", result.Result)
	}
}

func (f *sqliteToolFixture) agent(t *testing.T, name string) domain.Agent {
	t.Helper()
	agent, err := f.s.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.provider.ID, Name: name, Status: domain.AgentActive,
	}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func (f *sqliteToolFixture) run(t *testing.T, agent domain.Agent, issue domain.Issue, key string) domain.Run {
	t.Helper()
	instance, _, err := f.s.EnsureIssueAgentInstance(t.Context(), f.user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := f.s.CreateRun(t.Context(), f.user.ID, issue.ID, issue.Version, key, domain.Run{
		AgentID: agent.ID, IssueAgentInstanceID: instance.ID,
		Input: []domain.ContentBlock{{Type: "text", Text: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func commentRequest(run domain.Run, key, body string, targets ...string) nodeprotocol.NodeRequest {
	return nodeprotocol.NodeRequest{RunID: run.ID, RequestID: "req-" + key, IdempotencyKey: key,
		Tool: "issue_comment_create", Payload: map[string]any{"body": body, "mentioned_agent_ids": targets}}
}

func (f *sqliteToolFixture) publish(t *testing.T, run domain.Run, key, body string, targets ...string) Execution {
	t.Helper()
	result := New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(run, key, body, targets...))
	if !result.Result.OK {
		t.Fatalf("publish failed: %+v", result.Result)
	}
	return result
}

func (f *sqliteToolFixture) comment(t *testing.T, id string) domain.IssueComment {
	t.Helper()
	comment, err := f.s.GetIssueComment(t.Context(), f.user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return comment
}

func (f *sqliteToolFixture) mentions(t *testing.T) []domain.IssueCommentMention {
	t.Helper()
	items, err := f.s.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (f *sqliteToolFixture) consume(t *testing.T, mention domain.IssueCommentMention) domain.Run {
	t.Helper()
	run, err := f.s.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, mention.TargetAgentID,
		mention.ID, []domain.ContentBlock{{Type: "text", Text: "delegated test"}}, "mention:"+mention.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestSQLiteToolCommentsFenceCurrentIssueAndRun(t *testing.T) {
	f := newSQLiteToolFixture(t)
	runA := f.run(t, f.a, f.issue, "current")
	project, err := f.s.CreateProject(t.Context(), f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "other project"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: project.ID, Title: "other issue"})
	if err != nil {
		t.Fatal(err)
	}
	runOther := f.run(t, f.a, other, "other")
	foreign := f.publish(t, runOther, "foreign", "must not change")
	foreignID := foreign.Result.Data["comment_id"].(string)
	request := nodeprotocol.NodeRequest{RunID: runA.ID, Tool: "issue_comment_update_own", Payload: map[string]any{
		"comment_id": foreignID, "expected_version": int64(1), "body": "cross-project write",
	}}
	result := New(f.s).Execute(t.Context(), f.computer.ID, request)
	if result.Result.OK || result.Result.ErrorCode != "not_own_comment" {
		t.Fatalf("cross-issue accepted: %+v", result)
	}
	if f.comment(t, foreignID).Body != "must not change" {
		t.Fatal("foreign comment changed")
	}
	request = commentRequest(runA, "scoped", "scoped summary")
	request.IssueID, request.AgentID = other.ID, f.b.ID
	result = New(f.s).Execute(t.Context(), f.computer.ID, request)
	if !result.Result.OK {
		t.Fatalf("valid publish denied: %+v", result)
	}
	comment := f.comment(t, result.Result.Data["comment_id"].(string))
	if comment.IssueID != f.issue.ID || comment.AuthorAgentID != f.a.ID || comment.RunID != runA.ID {
		t.Fatalf("untrusted scope used: %+v", comment)
	}
	correction := nodeprotocol.NodeRequest{RunID: runA.ID, Tool: "issue_comment_update_own", Payload: map[string]any{
		"comment_id": comment.ID, "expected_version": comment.Version, "body": "corrected own summary",
	}}
	if corrected := New(f.s).Execute(t.Context(), f.computer.ID, correction); !corrected.Result.OK || corrected.Result.Data["version"] != int64(2) {
		t.Fatalf("valid correction failed: %+v", corrected)
	}
	if stale := New(f.s).Execute(t.Context(), f.computer.ID, correction); stale.Result.ErrorCode != "version_conflict" {
		t.Fatalf("stale correction accepted: %+v", stale)
	}
	request.IdempotencyKey = "wrong-device"
	if New(f.s).Execute(t.Context(), "other-computer", request).Result.OK {
		t.Fatal("wrong device wrote")
	}
	// Even a persisted but inconsistent Run/instance relation cannot grant write access.
	if _, err := f.db.ExecContext(t.Context(), `UPDATE runs SET agent_id = ? WHERE id = ?`, f.b.ID, runA.ID); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "inconsistent-agent"
	if New(f.s).Execute(t.Context(), f.computer.ID, request).Result.OK {
		t.Fatal("inconsistent Run owner wrote")
	}
}

func TestSQLiteToolCommentsRejectTerminalAndCancellingRuns(t *testing.T) {
	for _, status := range []string{domain.RunCancelling, domain.RunCancelled, domain.RunSucceeded, domain.RunFailed, domain.RunLost} {
		t.Run(status, func(t *testing.T) {
			f := newSQLiteToolFixture(t)
			run := f.run(t, f.a, f.issue, "run")
			published := f.publish(t, run, "before", "before")
			id := published.Result.Data["comment_id"].(string)
			if _, err := f.db.ExecContext(t.Context(), `UPDATE runs SET status = ? WHERE id = ?`, status, run.ID); err != nil {
				t.Fatal(err)
			}
			for _, request := range []nodeprotocol.NodeRequest{
				commentRequest(run, "late-summary", "late", f.b.ID),
				{RunID: run.ID, Tool: "issue_comment_update_own", Payload: map[string]any{
					"comment_id": id, "expected_version": int64(1), "body": "late edit",
				}},
			} {
				result := New(f.s).Execute(t.Context(), f.computer.ID, request)
				if result.Result.OK || result.Result.ErrorCode != "run_not_active" {
					t.Fatalf("late write: %+v", result)
				}
			}
			if got := f.comment(t, id); got.Body != "before" || got.Version != 1 {
				t.Fatalf("late edit persisted: %+v", got)
			}
			if len(f.mentions(t)) != 0 {
				t.Fatal("late delegation persisted")
			}
			// A receipt replay is allowed after cancellation but must not add any write.
			if replay := New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(run, "before", "before")); !replay.Result.OK || replay.ActivationIssueID != "" {
				t.Fatalf("receipt replay: %+v", replay)
			}
		})
	}
}

type cancelAfterScopeRepository struct{ *store.SQLite }

func (r cancelAfterScopeRepository) GetRunScope(ctx context.Context, id string) (store.RunScope, error) {
	scope, err := r.SQLite.GetRunScope(ctx, id)
	if err == nil {
		err = r.CancelLocalRun(ctx, id)
	}
	return scope, err
}

func TestSQLiteToolCommentsRecheckRunInsideWriteTransaction(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "late")
	result := New(cancelAfterScopeRepository{f.s}).Execute(t.Context(), f.computer.ID, commentRequest(run, "late", "late", f.b.ID))
	if result.Result.OK || result.Result.ErrorCode != "run_not_active" {
		t.Fatalf("cancelled between scope and write: %+v", result)
	}
	comments, err := f.s.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 0 || len(f.mentions(t)) != 0 {
		t.Fatalf("late write persisted: %+v %v", comments, err)
	}
}

func TestSQLiteToolConcurrentStopNeverCommitsInactiveCommentWrite(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "concurrent-stop")
	f.publish(t, run, "initial", "initial summary")
	// This trigger only observes the status at the write's linearization point;
	// unlike a protective trigger it does not reject or fix a stale write.
	if _, err := f.db.ExecContext(t.Context(), `
		CREATE TABLE test_tool_write_status (status TEXT NOT NULL);
		CREATE TRIGGER test_observe_tool_write AFTER UPDATE OF body ON issue_comments
		BEGIN
		    INSERT INTO test_tool_write_status SELECT status FROM runs WHERE id = NEW.run_id;
		END;
	`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan Execution, 32)
	var wg sync.WaitGroup
	for index := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- New(f.s).Execute(t.Context(), f.computer.ID,
				commentRequest(run, fmt.Sprintf("stop-race-%d", index), "concurrent summary", f.b.ID))
		}()
	}
	close(start)
	if err := f.s.CancelLocalRun(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.Result.OK {
			successes++
		} else if result.Result.ErrorCode != "run_not_active" {
			t.Fatalf("unexpected cancellation result: %+v", result)
		}
	}
	var inactiveWrites int
	if err := f.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM test_tool_write_status
		WHERE status NOT IN ('queued','starting','running','waiting_approval')
	`).Scan(&inactiveWrites); err != nil {
		t.Fatal(err)
	}
	if inactiveWrites != 0 || len(f.mentions(t)) != successes {
		t.Fatalf("stop fence violated: inactive writes=%d successes=%d mentions=%d", inactiveWrites, successes, len(f.mentions(t)))
	}
	for index := range 8 {
		if result := New(f.s).Execute(t.Context(), f.computer.ID,
			commentRequest(run, fmt.Sprintf("after-cancel-%d", index), "late summary", f.b.ID)); result.Result.OK {
			t.Fatal("write after committed cancellation succeeded")
		}
	}
}

func TestSQLiteToolSummaryAndDelegationsRollbackTogether(t *testing.T) {
	f := newSQLiteToolFixture(t)
	_, err := f.s.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "start A",
	}, []string{f.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	run := f.consume(t, f.mentions(t)[0])
	comments, err := f.s.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments=%+v err=%v", comments, err)
	}
	working := comments[1]
	assertUnchanged := func() {
		t.Helper()
		got := f.comment(t, working.ID)
		revisions, err := f.s.ListIssueCommentRevisions(t.Context(), f.user.ID, working.ID)
		if err != nil || got.Body != working.Body || got.Status != domain.IssueCommentWorking || got.Version != 1 || len(revisions) != 0 || len(f.mentions(t)) != 1 {
			t.Fatalf("partial transaction: comment=%+v revisions=%+v err=%v", got, revisions, err)
		}
	}
	for _, targets := range [][]string{{f.b.ID, "missing-agent"}, {f.b.ID, f.a.ID}, {f.b.ID, f.b.ID}} {
		result := New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(run, "invalid-"+strings.Join(targets, ":"), "must roll back", targets...))
		if result.Result.OK {
			t.Fatal("invalid delegation succeeded")
		}
		assertUnchanged()
	}
	if _, err := f.db.ExecContext(t.Context(), `CREATE TRIGGER test_mention_failure BEFORE INSERT ON issue_comment_mentions BEGIN SELECT RAISE(ABORT, 'test mention failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := commentRequest(run, "transaction-retry", "published once", f.b.ID)
	if New(f.s).Execute(t.Context(), f.computer.ID, request).Result.OK {
		t.Fatal("injected failure succeeded")
	}
	assertUnchanged()
	// Failed append_new must not leak a second summary either.
	appendRequest := commentRequest(run, "failed-append", "second summary", f.b.ID)
	appendRequest.Payload["append_new"] = true
	if New(f.s).Execute(t.Context(), f.computer.ID, appendRequest).Result.OK {
		t.Fatal("injected append succeeded")
	}
	comments, _ = f.s.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if len(comments) != 2 {
		t.Fatalf("failed append leaked: %+v", comments)
	}
	if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER test_mention_failure`); err != nil {
		t.Fatal(err)
	}
	result := New(f.s).Execute(t.Context(), f.computer.ID, request)
	if !result.Result.OK || result.Result.Data["comment_id"] != working.ID {
		t.Fatalf("retry: %+v", result)
	}
	got := f.comment(t, working.ID)
	if got.Body != "published once" || got.Status != domain.IssueCommentPublished || got.Version != 2 || len(f.mentions(t)) != 2 {
		t.Fatalf("publication: %+v", got)
	}
}

func TestSQLiteToolDelegationIsDurableConcurrentAndRepeatable(t *testing.T) {
	f := newSQLiteToolFixture(t)
	runA := f.run(t, f.a, f.issue, "A")
	request := commentRequest(runA, "first", "delegate B", f.b.ID)
	const workers = 16
	results := make(chan Execution, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Independent services bypass the process-local idempotency cache.
			results <- New(f.s).Execute(t.Context(), f.computer.ID, request)
		}()
	}
	wg.Wait()
	close(results)
	id, wakes := "", 0
	for result := range results {
		if !result.Result.OK {
			t.Fatalf("concurrent write: %+v", result)
		}
		if id == "" {
			id = result.Result.Data["comment_id"].(string)
		}
		if id != result.Result.Data["comment_id"] || result.Result.Data["version"] != int64(1) {
			t.Fatalf("non-idempotent result: %+v", result)
		}
		if result.ActivationIssueID != "" {
			wakes++
		}
	}
	if wakes != 1 || len(f.mentions(t)) != 1 {
		t.Fatalf("wakes=%d mentions=%+v", wakes, f.mentions(t))
	}
	request.Payload["body"] = "conflicting body"
	if result := New(f.s).Execute(t.Context(), f.computer.ID, request); result.Result.ErrorCode != "idempotency_conflict" {
		t.Fatalf("key reuse=%+v", result)
	}
	firstMention := f.mentions(t)[0]
	runB := f.consume(t, firstMention)
	// Same mention, any number of transport retries, still exactly one Run.
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replayed, err := f.s.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.b.ID, firstMention.ID,
				[]domain.ContentBlock{{Type: "text", Text: "test"}}, "mention:"+firstMention.ID)
			if err != nil || replayed.ID != runB.ID {
				t.Errorf("mention retry: %+v %v", replayed, err)
			}
		}()
	}
	wg.Wait()
	if _, err := f.s.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.b.ID, firstMention.ID,
		[]domain.ContentBlock{{Type: "text", Text: "test"}}, "different-consumption-key"); err == nil {
		t.Fatal("mention consumed twice with another key")
	}
	// A still runs; B's second activation must be a different mention and instance.
	second := f.publish(t, runA, "second", "delegate B again", f.b.ID)
	if second.Result.Data["comment_id"] != id || second.Result.Data["version"] != int64(2) {
		t.Fatalf("summary not reused: %+v", second)
	}
	mentions := f.mentions(t)
	if len(mentions) != 2 {
		t.Fatalf("repeated target swallowed: %+v", mentions)
	}
	var secondMention domain.IssueCommentMention
	for _, mention := range mentions {
		if mention.ID != firstMention.ID {
			secondMention = mention
		}
	}
	secondB := f.consume(t, secondMention)
	if secondB.ID == runB.ID || secondB.IssueAgentInstanceID == runB.IssueAgentInstanceID {
		t.Fatal("repeated delegation reused consumed identity")
	}
	// A -> B -> A remains legal for many rounds, even while old instances run.
	current := runB
	seenInstances := map[string]bool{runA.IssueAgentInstanceID: true, runB.IssueAgentInstanceID: true, secondB.IssueAgentInstanceID: true}
	for round := range 8 {
		target := f.a.ID
		if current.AgentID == f.a.ID {
			target = f.b.ID
		}
		f.publish(t, current, fmt.Sprintf("cycle-%d", round), "continue cycle", target)
		pending := []domain.IssueCommentMention{}
		for _, mention := range f.mentions(t) {
			if mention.ConsumedAt == nil {
				pending = append(pending, mention)
			}
		}
		if len(pending) != 1 {
			t.Fatalf("round %d pending=%+v", round, pending)
		}
		current = f.consume(t, pending[0])
		if seenInstances[current.IssueAgentInstanceID] {
			t.Fatal("cycle reused an instance")
		}
		seenInstances[current.IssueAgentInstanceID] = true
	}
	// Serial within an instance is preserved, independent instances stay parallel.
	_, _, busyErr := f.s.CreateRun(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, "busy-instance", domain.Run{
		AgentID: current.AgentID, IssueAgentInstanceID: current.IssueAgentInstanceID, Input: []domain.ContentBlock{{Type: "text", Text: "busy"}},
	})
	if businessError, ok := busyErr.(*domain.Error); !ok || businessError.Code != "target_busy" {
		t.Fatalf("busy instance guard changed: %v", busyErr)
	}
	if _, err := f.s.StopRun(t.Context(), f.user.ID, current.ID, "force"); err != nil {
		t.Fatal(err)
	}
	if result := New(f.s).Execute(t.Context(), f.computer.ID, commentRequest(current, "after-stop", "late", f.b.ID)); result.Result.OK {
		t.Fatal("emergency stop admitted late delegation")
	}
}

func TestSQLiteToolMentionRetryLeaseAndStop(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "source")
	f.publish(t, run, "pending", "delegate", f.b.ID)
	now := time.Now().UTC().Add(time.Second)
	claim, ok, err := f.s.ClaimNextIssueMention(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claim, ok, err)
	}
	if err := f.s.RetryIssueMention(t.Context(), claim, "target_busy", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.s.ClaimNextIssueMention(t.Context(), now, time.Minute); err != nil || ok {
		t.Fatalf("busy backoff skipped: %v %v", ok, err)
	}
	if _, err := f.s.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "test"}}, "mention:"+claim.MentionID); err == nil {
		t.Fatal("stale lease consumed")
	}
	newClaim, ok, err := f.s.ClaimNextIssueMention(t.Context(), now.Add(time.Minute), time.Minute)
	if err != nil || !ok || newClaim.ClaimToken == claim.ClaimToken {
		t.Fatalf("retry claim=%+v ok=%v err=%v", newClaim, ok, err)
	}
	// Queue emergency clearing/deleting invalidates even an already leased mention.
	comment := f.comment(t, newClaim.CommentID)
	if err := f.s.DeleteIssueComment(t.Context(), f.user.ID, comment.ID, comment.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRunForClaimedMention(t.Context(), newClaim, []domain.ContentBlock{{Type: "text", Text: "test"}}, "mention:"+claim.MentionID); err == nil {
		t.Fatal("deleted mention consumed")
	}
	if _, ok, err := f.s.ClaimNextIssueMention(t.Context(), now.Add(2*time.Minute), time.Minute); err != nil || ok {
		t.Fatalf("cleared mention requeued: %v %v", ok, err)
	}
}

func TestSQLiteToolSessionSummarySurvivesMoreThan200Runs(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "run-0")
	firstID := run.ID
	summary := f.publish(t, run, "summary-0", "initial summary")
	commentID := summary.Result.Data["comment_id"].(string)
	for index := 1; index <= 220; index++ {
		if err := f.s.CancelLocalRun(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
		run = f.run(t, f.a, f.issue, fmt.Sprintf("run-%d", index))
	}
	updated := f.publish(t, run, "summary-220", "same session summary")
	if updated.Result.Data["comment_id"] != commentID || updated.Result.Data["version"] != int64(2) {
		t.Fatalf("summary lost after window: %+v", updated)
	}
	comments, err := f.s.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("duplicated summary: %+v %v", comments, err)
	}
	runs, err := f.s.ListRunsByIssue(t.Context(), f.user.ID, f.issue.ID, 200)
	if err != nil || len(runs) != 200 || runs[0].ID != run.ID {
		t.Fatalf("recent runs=%d err=%v", len(runs), err)
	}
	for index, candidate := range runs {
		if candidate.ID == firstID {
			t.Fatal("oldest run occupied recent page")
		}
		if index > 0 && candidate.CreatedAt.After(runs[index-1].CreatedAt) {
			t.Fatal("runs not newest-first")
		}
	}
	seen, cursor := map[string]bool{}, ""
	for {
		page, err := f.s.ListRunsByInstancePage(t.Context(), f.user.ID, run.IssueAgentInstanceID, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range page.Items {
			if seen[candidate.ID] {
				t.Fatal("duplicate run page")
			}
			seen[candidate.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 221 {
		t.Fatalf("run pages lost history: %d", len(seen))
	}
}

func TestSQLiteToolGoalSummaryRemainsWorkingUntilFinalize(t *testing.T) {
	f := newSQLiteToolFixture(t)
	instance, _, err := f.s.EnsureIssueAgentInstance(t.Context(), f.user.ID, f.issue.ID, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, goal, _, err := f.s.CreateIssueAgentGoalRun(t.Context(), f.user.ID, f.issue.ID, instance.ID,
		f.issue.Version, "goal-create", "test goal", nil, domain.Run{
			AgentID: f.a.ID, IssueAgentInstanceID: instance.ID, Input: []domain.ContentBlock{{Type: "text", Text: "test goal"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	progress := f.publish(t, run, "goal-progress", "working progress")
	if progress.Result.Data["comment_id"] != goal.SummaryCommentID {
		t.Fatalf("goal summary not reused: %+v", progress)
	}
	comment := f.comment(t, goal.SummaryCommentID)
	if comment.Status != domain.IssueCommentWorking {
		t.Fatalf("progress published early: %+v", comment)
	}
	corrected := New(f.s).Execute(t.Context(), f.computer.ID, nodeprotocol.NodeRequest{
		RunID: run.ID, Tool: "issue_comment_update_own", IdempotencyKey: "goal-correction", Payload: map[string]any{
			"comment_id": comment.ID, "expected_version": comment.Version, "body": "corrected progress",
		},
	})
	if !corrected.Result.OK || f.comment(t, comment.ID).Status != domain.IssueCommentWorking {
		t.Fatalf("correction published goal progress: %+v", corrected)
	}
	complete := func(run domain.Run, answer string) {
		t.Helper()
		events := []nodeprotocol.RunEvent{
			{Type: "run.started"},
			{Type: "output.delta", Payload: map[string]any{"text": answer, "provider_id": "codex"}},
			{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
		}
		for index := range events {
			events[index].ID, events[index].RunID = fmt.Sprintf("%s-goal-%d", run.ID, index), run.ID
			events[index].LeaseEpoch, events[index].Sequence = run.LeaseEpoch, int64(index+1)
			events[index].OccurredAt = time.Now().UTC()
		}
		if _, err := f.s.ApplyLocalEvents(t.Context(), events); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.AdvanceLocalGoals(t.Context(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	complete(run, "work finished")
	current, _, err := f.s.GetCurrentIssueAgentGoal(t.Context(), f.user.ID, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := f.s.GetRun(t.Context(), f.user.ID, current.CurrentRunID)
	if err != nil || audit.Purpose != domain.RunPurposeGoalAudit {
		t.Fatalf("audit run=%+v err=%v", audit, err)
	}
	complete(audit, `{"verdict":"complete","note":"test evidence"}`)
	current, _, err = f.s.GetCurrentIssueAgentGoal(t.Context(), f.user.ID, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalize, err := f.s.GetRun(t.Context(), f.user.ID, current.CurrentRunID)
	if err != nil || finalize.Purpose != domain.RunPurposeGoalFinalize {
		t.Fatalf("finalize run=%+v err=%v", finalize, err)
	}
	final := f.publish(t, finalize, "goal-final", "public final summary")
	comment = f.comment(t, goal.SummaryCommentID)
	if final.Result.Data["comment_id"] != goal.SummaryCommentID || comment.Status != domain.IssueCommentPublished || comment.Body != "public final summary" {
		t.Fatalf("goal final summary failed: %+v comment=%+v", final, comment)
	}
}

func (f *sqliteToolFixture) events(t *testing.T, run domain.Run, count int) {
	t.Helper()
	events := make([]nodeprotocol.RunEvent, count)
	for index := range count {
		payload := map[string]any{"tool": "read", "status": "done", "secret": "never expose"}
		if index == 0 {
			payload["tool"] = map[string]any{"secret": "nested secret"}
			payload["status"] = []string{"nested secret"}
		}
		events[index] = nodeprotocol.RunEvent{ID: fmt.Sprintf("%s-event-%d", run.ID, index), RunID: run.ID,
			LeaseEpoch: 1, Sequence: int64(index + 1), Type: "provider.tool", Payload: payload, OccurredAt: time.Now().UTC()}
	}
	for start := 0; start < len(events); start += 128 {
		end := min(start+128, len(events))
		if _, err := f.s.ApplyLocalEvents(t.Context(), events[start:end]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteToolCommentIdempotencySurvivesRepositoryReopen(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "reopen")
	request := commentRequest(run, "persisted-receipt", "summary", f.b.ID)
	first := New(f.s).Execute(t.Context(), f.computer.ID, request)
	if !first.Result.OK {
		t.Fatalf("first request failed: %+v", first)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.s = reopened
	replayed := New(reopened).Execute(t.Context(), f.computer.ID, request)
	if !replayed.Result.OK || replayed.ActivationIssueID != "" ||
		replayed.Result.Data["comment_id"] != first.Result.Data["comment_id"] ||
		replayed.Result.Data["version"] != first.Result.Data["version"] || len(f.mentions(t)) != 1 {
		t.Fatalf("receipt lost across reopen: first=%+v replay=%+v", first, replayed)
	}
}

func TestSQLiteToolContextPayloadBudgetAndLabelBounds(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "payload-budget")
	for index := range 3 {
		_, err := f.s.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
			ID: fmt.Sprintf("large-%d", index), RunID: run.ID, LeaseEpoch: 1, Sequence: int64(index + 1),
			Type: "provider.tool", OccurredAt: time.Now().UTC(), Payload: map[string]any{
				"text": strings.Repeat("private payload", store.MaxRunEventPagePayloadBytes/20),
				"tool": strings.Repeat("界", 300), "status": "done",
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	request := nodeprotocol.NodeRequest{RunID: run.ID, Tool: "issue_agent_context", Payload: map[string]any{"agent_id": f.a.ID}}
	seen, cursor := 0, ""
	for page := 0; page < 5; page++ {
		request.Payload["cursor"] = cursor
		result := New(f.s).Execute(t.Context(), f.computer.ID, request).Result
		if !result.OK {
			t.Fatalf("large payload context failed: %+v", result)
		}
		items := result.Data["context"].([]map[string]any)
		if len(items) > 1 {
			t.Fatalf("raw payload page exceeded total byte budget: %d", len(items))
		}
		for _, item := range items {
			if len(item["tool"].(string)) > 128 {
				t.Fatal("unbounded provider tool label")
			}
			seen++
		}
		encoded, _ := json.Marshal(result.Data)
		if len(encoded) > 2048 || strings.Contains(string(encoded), "private payload") {
			t.Fatalf("large/private context response: %d bytes", len(encoded))
		}
		cursor = result.Data["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if seen != 3 || cursor != "" {
		t.Fatalf("payload-budget pagination lost events: seen=%d cursor=%q", seen, cursor)
	}
}

func TestSQLiteToolContextPaginationIsBoundedPinnedAndRedacted(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "context-0")
	for index := range 12 {
		count := 1
		if index == 11 {
			count = 300
		}
		f.events(t, run, count)
		if index != 11 {
			if err := f.s.CancelLocalRun(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			run = f.run(t, f.a, f.issue, fmt.Sprintf("context-%d", index+1))
		}
	}
	request := nodeprotocol.NodeRequest{RunID: run.ID, Tool: "issue_agent_context", Payload: map[string]any{"agent_id": f.a.ID}}
	seen, cursor, pages := map[string]bool{}, "", 0
	for {
		request.Payload["cursor"] = cursor
		result := New(f.s).Execute(t.Context(), f.computer.ID, request).Result
		if !result.OK {
			t.Fatalf("page failed: %+v", result)
		}
		if result.Data["instance_id"] != run.IssueAgentInstanceID {
			t.Fatalf("cursor switched instance: %+v", result)
		}
		items := result.Data["context"].([]map[string]any)
		if len(items) > maxContextEvents {
			t.Fatalf("unbounded context: %d", len(items))
		}
		encoded, _ := json.Marshal(result.Data)
		if strings.Contains(string(encoded), "never expose") || strings.Contains(string(encoded), "nested secret") {
			t.Fatalf("unredacted context: %s", encoded)
		}
		for _, item := range items {
			key := fmt.Sprintf("%s:%v", item["run_id"], item["seq"])
			if seen[key] {
				t.Fatalf("duplicate context item %s", key)
			}
			seen[key] = true
		}
		pages++
		next := result.Data["next_cursor"].(string)
		if result.Data["truncated"] != (next != "") || (next != "" && next == cursor) {
			t.Fatal("cursor did not advance")
		}
		if pages == 1 {
			// A fresh mention changes the latest instance, not this traversal.
			_, err := f.s.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: f.issue.ID,
				AuthorKind: domain.IssueCommentAuthorUser, Body: "new context instance"}, []string{f.a.ID})
			if err != nil {
				t.Fatal(err)
			}
			f.consume(t, f.mentions(t)[0])
		}
		cursor = next
		if cursor == "" {
			break
		}
		if pages > 40 {
			t.Fatal("context cursor loop")
		}
	}
	// 300 latest tools + 11 older tools + 11 cancellation metadata events.
	if len(seen) != 322 || pages < 14 {
		t.Fatalf("lost context history: events=%d pages=%d", len(seen), pages)
	}
	for _, invalid := range []any{123, "bad-cursor", base64.RawURLEncoding.EncodeToString([]byte(`{"v":0,"instance_id":"bad"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"instance_id":"` + run.IssueAgentInstanceID + `"}`))} {
		request.Payload["cursor"] = invalid
		if New(f.s).Execute(t.Context(), f.computer.ID, request).Result.OK {
			t.Fatalf("invalid cursor accepted: %v", invalid)
		}
	}
	forged, _ := json.Marshal(contextCursor{Version: 1, InstanceID: run.IssueAgentInstanceID, RunID: "foreign-run"})
	request.Payload["cursor"] = base64.RawURLEncoding.EncodeToString(forged)
	if New(f.s).Execute(t.Context(), f.computer.ID, request).Result.OK {
		t.Fatal("forged run cursor accepted")
	}
}
