package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func workPlanItem(agent, key string, after ...string) nodeprotocol.WorkPlanItem {
	return nodeprotocol.WorkPlanItem{Key: key, AgentID: agent, Title: "分工 " + key, Instruction: "instruction-" + key, After: after}
}

func createWorkPlan(t *testing.T, f sqliteExecutionFixture, items ...nodeprotocol.WorkPlanItem) domain.IssueComment {
	t.Helper()
	comment, err := f.repository.CreateIssueWorkComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: "user", Body: "confirmed discussion",
	}, items, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	return comment
}

func workComment(t *testing.T, f sqliteExecutionFixture, id string) domain.IssueComment {
	t.Helper()
	comment, err := f.repository.GetIssueComment(t.Context(), f.user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return comment
}

func consumeWork(t *testing.T, f sqliteExecutionFixture) domain.Run {
	t.Helper()
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claim, ok, err)
	}
	run, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "bounded ordinary context"}}, "mention:"+claim.MentionID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func finishWork(t *testing.T, f sqliteExecutionFixture, run domain.Run, summary, terminal string) {
	t.Helper()
	if summary != "" {
		if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID, RunIssueCommentWrite{Body: summary, IdempotencyKey: "summary"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "terminal-" + run.ID, RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "run.completed", Payload: map[string]any{"status": terminal}, OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
}

func advanceWork(t *testing.T, f sqliteExecutionFixture, want int) {
	t.Helper()
	got, err := f.repository.AdvanceIssueWorkItems(t.Context(), time.Now().UTC())
	if err != nil || got != want {
		t.Fatalf("advance=%d want=%d err=%v", got, want, err)
	}
}

func TestIssueWorkItemsSevenItemsSixParallelAndFiveDependencies(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	items := make([]nodeprotocol.WorkPlanItem, 0, 7)
	for i := range 6 {
		items = append(items, workPlanItem(f.agent.ID, fmt.Sprintf("root-%d", i)))
	}
	items = append(items, workPlanItem(f.agent.ID, "join", "root-0", "root-1", "root-2", "root-3", "root-4"))
	comment := createWorkPlan(t, f, items...)
	if len(comment.WorkItems) != 7 || comment.WorkItems[6].Status != "waiting_dependencies" || len(comment.WorkItems[6].BlockedBy) != 5 {
		t.Fatalf("initial projection=%+v", comment.WorkItems)
	}
	mentions, _ := f.repository.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if len(mentions) != 6 {
		t.Fatalf("ready=%d want=6", len(mentions))
	}
	runs := make(map[string]domain.Run)
	instances := make(map[string]bool)
	for range 6 {
		run := consumeWork(t, f)
		if instances[run.IssueAgentInstanceID] || run.IssueAgentInstanceID == f.instance.ID {
			t.Fatal("repeated Agent reused a conversation")
		}
		instances[run.IssueAgentInstanceID] = true
		for _, item := range workComment(t, f, comment.ID).WorkItems {
			if item.RunID == run.ID {
				runs[item.Key] = run
				if !strings.Contains(contentBlockText(run.Input), item.Instruction) {
					t.Fatal("own instruction missing")
				}
			}
		}
	}
	if len(runs) != 6 {
		t.Fatalf("root bindings=%v", runs)
	}
	for i := range 4 {
		key := fmt.Sprintf("root-%d", i)
		finishWork(t, f, runs[key], "published-result-"+key, "completed")
	}
	advanceWork(t, f, 0)
	finishWork(t, f, runs["root-4"], "published-result-root-4", "completed")
	advanceWork(t, f, 1)
	advanceWork(t, f, 0)
	join := consumeWork(t, f)
	prompt := contentBlockText(join.Input)
	if !strings.Contains(prompt, "instruction-join") || strings.Contains(prompt, "published-result-root-5") {
		t.Fatalf("wrong own/upstream context: %s", prompt)
	}
	for i := range 5 {
		if !strings.Contains(prompt, fmt.Sprintf("published-result-root-%d", i)) {
			t.Fatal("upstream published result omitted")
		}
	}
	latest := workComment(t, f, comment.ID)
	if latest.WorkItems[6].RunID != join.ID || latest.WorkItems[6].IssueAgentInstanceID != join.IssueAgentInstanceID || latest.WorkItems[5].Status != domain.RunQueued {
		t.Fatalf("execution projection=%+v", latest.WorkItems)
	}
	// Editing the discussion must not rewrite confirmed instructions.
	_, err := f.repository.UpdateIssueComment(t.Context(), f.user.ID, comment.ID, comment.Version, func(c *domain.IssueComment) error { c.Body = "new discussion"; return nil })
	if err != nil || workComment(t, f, comment.ID).WorkItems[6].Instruction != "instruction-join" {
		t.Fatalf("discussion edited the plan: %v", err)
	}
}

func TestIssueWorkItemsDamagedDependenciesCannotBecomeReady(t *testing.T) {
	for name, mutation := range map[string]string{
		"missing after":     `json_remove(value,'$.items[1].after')`,
		"null after":        `json_set(value,'$.items[1].after',NULL)`,
		"empty after":       `json_set(value,'$.items[1].after',json('[]'))`,
		"lost root mention": `json_set(value,'$.items[0].mention_id','')`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "root"), workPlanItem(f.agent.ID, "dependent", "root"))
			claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
			if err != nil || !ok {
				t.Fatal("initial work not claimable")
			}
			key := issueWorkKey(f.issue.ID, comment.ID)
			if _, err := f.repository.db.Exec(`UPDATE app_metadata SET value=`+mutation+` WHERE key=?`, key); err != nil {
				t.Fatal(err)
			}
			var corrupted, retained string
			if err := f.repository.db.QueryRow(`SELECT value FROM app_metadata WHERE key=?`, key).Scan(&corrupted); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.GetIssueComment(t.Context(), f.user.ID, comment.ID); err == nil {
				t.Fatal("damaged work was projected as executable")
			}
			if count, err := f.repository.AdvanceIssueWorkItems(t.Context(), time.Now().UTC()); err == nil || count != 0 {
				t.Fatalf("damaged dependency advanced: count=%d err=%v", count, err)
			}
			if _, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "not allowed"}}, "corrupt-work"); err == nil {
				t.Fatal("claimed work bypassed damaged dependency metadata")
			}
			var mentions, runs int
			if err := f.repository.db.QueryRow(`SELECT (SELECT count(*) FROM issue_comment_mentions),(SELECT count(*) FROM runs)`).Scan(&mentions, &runs); err != nil || mentions != 1 || runs != 0 {
				t.Fatalf("damaged work created execution: mentions=%d runs=%d err=%v", mentions, runs, err)
			}
			if err := f.repository.db.QueryRow(`SELECT value FROM app_metadata WHERE key=?`, key).Scan(&retained); err != nil || corrupted != retained {
				t.Fatal("damaged metadata was silently repaired or overwritten")
			}
		})
	}
}

func TestIssueWorkItemsMarkerLookupUsesCommentScopedIndex(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	rows, err := f.repository.db.Query(`EXPLAIN QUERY PLAN `+sqliteIssueWorkMarkersQuery, "comment")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if err := rows.Err(); err != nil || !strings.Contains(plan.String(), "comment_id=?") {
		t.Fatalf("marker lookup scans global work mentions instead of the comment: plan=%s err=%v", plan.String(), err)
	}
}

func TestIssueWorkItemsRequireSuccessfulRunAndExactPublishedSummary(t *testing.T) {
	for _, test := range []struct {
		status, summary string
		ready           bool
	}{
		{"completed", "", false}, {"completed", "published", true},
		{"failed", "published", false}, {"cancelled", "published", false},
	} {
		t.Run(test.status+"/"+test.summary, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
			run := consumeWork(t, f)
			finishWork(t, f, run, test.summary, test.status)
			want := 0
			if test.ready {
				want = 1
			}
			advanceWork(t, f, want)
			view := workComment(t, f, comment.ID)
			if !test.ready && (view.WorkItems[1].MentionID != "" || len(view.WorkItems[1].BlockedBy) != 1) {
				t.Fatalf("failed/missing result released child: %+v", view.WorkItems)
			}
			if test.status == "completed" && test.summary == "" && view.WorkItems[0].StatusReason != "result_not_published" {
				t.Fatal("successful model output was mistaken for public result")
			}
			if test.status == "completed" && test.summary == "" && view.WorkItems[0].Status != "waiting_result" {
				t.Fatal("missing publication must be visibly waiting_result")
			}
			if test.status != "completed" && view.WorkItems[1].Status != "blocked" {
				t.Fatal("failed/cancelled dependency must visibly block waiting work")
			}
		})
	}
	t.Run("published before failure and accepted stop", func(t *testing.T) {
		f := newSQLiteExecutionFixture(t)
		defer f.repository.Close()
		comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
		run := consumeWork(t, f)
		if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID, RunIssueCommentWrite{Body: "early result"}); err != nil {
			t.Fatal(err)
		}
		advanceWork(t, f, 0)
		if _, err := f.repository.StopRun(t.Context(), f.user.ID, run.ID, "force"); err != nil {
			t.Fatal(err)
		}
		finishWork(t, f, run, "", "completed")
		advanceWork(t, f, 0)
		if workComment(t, f, comment.ID).WorkItems[0].Status != domain.RunCancelled {
			t.Fatal("late success beat accepted stop")
		}
	})
	t.Run("deleted published result", func(t *testing.T) {
		f := newSQLiteExecutionFixture(t)
		defer f.repository.Close()
		comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
		finishWork(t, f, consumeWork(t, f), "published", "completed")
		result := workComment(t, f, workComment(t, f, comment.ID).WorkItems[0].ResultCommentID)
		if err := f.repository.DeleteIssueComment(t.Context(), f.user.ID, result.ID, result.Version); err != nil {
			t.Fatal(err)
		}
		advanceWork(t, f, 0)
	})
}

func TestIssueWorkItemsConcurrentIdempotencyAndAtomicFailures(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	other, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	items := []nodeprotocol.WorkPlanItem{workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a")}
	input := domain.IssueComment{IssueID: f.issue.ID, AuthorKind: "user", Body: "confirmed"}
	results := make(chan domain.IssueComment, 12)
	errors := make(chan error, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := f.repository
			if i%2 != 0 {
				repo = other
			}
			c, err := repo.CreateIssueWorkComment(t.Context(), f.user.ID, input, items, "same-key")
			results <- c
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for c := range results {
		if id == "" {
			id = c.ID
		}
		if c.ID != id || len(c.WorkItems) != 2 {
			t.Fatal("duplicate/missing receipt projection")
		}
	}
	input.Body = "different"
	_, err = f.repository.CreateIssueWorkComment(t.Context(), f.user.ID, input, items, "same-key")
	requireSQLiteExecutionError(t, err, "idempotency_conflict")
	var count int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM issue_comments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("comments=%d err=%v", count, err)
	}
	if _, err := f.repository.db.Exec(`CREATE TRIGGER fail_work_binding BEFORE UPDATE ON app_metadata WHEN NEW.key GLOB 'issue_work:v1:*' BEGIN SELECT RAISE(ABORT,'test binding failure'); END`); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	_, err = f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID)
	if err == nil {
		t.Fatal("binding failure accepted")
	}
	var runs, instances, outbox int
	if err := f.repository.db.QueryRow(`SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM issue_agent_instances),(SELECT count(*) FROM command_outbox)`).Scan(&runs, &instances, &outbox); err != nil || runs != 0 || instances != 1 || outbox != 0 {
		t.Fatalf("partial binding: %d/%d/%d %v", runs, instances, outbox, err)
	}
	if _, err := f.repository.db.Exec(`DROP TRIGGER fail_work_binding; CREATE TRIGGER fail_work_create BEFORE INSERT ON app_metadata WHEN NEW.key GLOB 'issue_work:v1:*' BEGIN SELECT RAISE(ABORT,'test plan failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.CreateIssueWorkComment(t.Context(), f.user.ID, input, items, "failed-key")
	if err == nil {
		t.Fatal("plan write failure accepted")
	}
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM issue_comments`).Scan(&count); err != nil || count != 1 {
		t.Fatal("partial work comment")
	}
	if _, err := f.repository.db.Exec(`DROP TRIGGER fail_work_create`); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("mention replay duplicated run")
	}
}

func TestIssueWorkItemsInvalidGraphAndForeignAgentLeaveNoRows(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	otherUser, otherComputer := seedSQLiteOtherIdentity(t, f.repository, "work-foreign")
	service, err := f.repository.UpsertAgentService(t.Context(), otherUser, domain.AgentService{ComputerID: otherComputer, Provider: "foreign", Status: "ready", LoginStatus: "authenticated", Capabilities: map[string]any{}, CapabilityVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.repository.CreateConfiguredAgent(t.Context(), otherUser, domain.Agent{ComputerID: otherComputer, AgentServiceID: service.ID, Name: "foreign", Status: domain.AgentActive}, "foreign", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, items := range map[string][]nodeprotocol.WorkPlanItem{
		"empty":              nil,
		"duplicate":          {workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "a")},
		"missing dependency": {workPlanItem(f.agent.ID, "a", "missing")},
		"self dependency":    {workPlanItem(f.agent.ID, "a", "a")},
		"cycle":              {workPlanItem(f.agent.ID, "a", "b"), workPlanItem(f.agent.ID, "b", "a")},
		"duplicate edge":     {workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a", "a")},
		"foreign agent":      {workPlanItem("foreign-agent", "a")},
		"other tenant":       {workPlanItem(foreign.ID, "a")},
		"empty title":        {{Key: "a", AgentID: f.agent.ID}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.repository.CreateIssueWorkComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: f.issue.ID, AuthorKind: "user", Body: "invalid"}, items, name)
			if err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	var comments, mentions, metadata int
	if err := f.repository.db.QueryRow(`SELECT (SELECT count(*) FROM issue_comments),(SELECT count(*) FROM issue_comment_mentions),(SELECT count(*) FROM app_metadata WHERE key GLOB 'issue_work*')`).Scan(&comments, &mentions, &metadata); err != nil || comments+mentions+metadata != 0 {
		t.Fatalf("invalid request leaked data %d/%d/%d %v", comments, mentions, metadata, err)
	}
}

func TestIssueWorkItemsMetadataAndInstructionsNeverEnterWorkHistory(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	item := workPlanItem(f.agent.ID, "a")
	item.Instruction = "private-work-instruction-marker"
	comment := createWorkPlan(t, f, item)
	finishWork(t, f, consumeWork(t, f), "private-work-result-marker", "completed")
	page, err := f.repository.QueryWorkRecords(t.Context(), f.user.ID, domain.WorkQuery{IncludeChanges: true, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{item.Instruction, "private-work-result-marker", comment.Body, "\"work_items\""} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("work instruction/metadata copied into work history")
		}
	}
}

func TestIssueWorkItemsPauseCloseClearDeleteAndReopen(t *testing.T) {
	for _, gate := range []string{"paused", "closed"} {
		t.Run(gate, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
			claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
			if err != nil || !ok {
				t.Fatal(err)
			}
			if gate == "paused" {
				f.instance, err = f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, f.instance.ID, f.instance.Version, func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused })
				if err != nil {
					t.Fatal(err)
				}
			} else {
				setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
			}
			_, err = f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID)
			if err == nil {
				t.Fatal("new instance bypassed emergency gate")
			}
			advanceWork(t, f, 0)
			if gate == "paused" {
				_, err = f.repository.UpdateIssueAgentInstance(t.Context(), f.user.ID, f.instance.ID, f.instance.Version, func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstanceIdle })
				if err != nil {
					t.Fatal(err)
				}
			} else {
				setSQLiteExecutionIssueStatus(t, &f, domain.IssueOpen)
			}
			run, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID)
			if err != nil {
				t.Fatal(err)
			}
			finishWork(t, f, run, "result", "completed")
			advanceWork(t, f, 1)
			if workComment(t, f, comment.ID).WorkItems[1].MentionID == "" {
				t.Fatal("explicit reopening lost queue")
			}
		})
	}
	for _, stop := range []string{"clear", "delete", "dead_letter"} {
		t.Run(stop, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
			if stop == "dead_letter" {
				claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
				if err != nil || !ok {
					t.Fatal(err)
				}
				if err := f.repository.DeadLetterIssueMention(t.Context(), claim, "queue_cleared"); err != nil {
					t.Fatal(err)
				}
			} else {
				run := consumeWork(t, f)
				if stop == "clear" {
					if err := f.repository.ClearIssueWorkItems(t.Context(), f.user.ID, f.issue.ID); err != nil {
						t.Fatal(err)
					}
					finishWork(t, f, run, "result", "completed")
				} else {
					if err := f.repository.DeleteIssueComment(t.Context(), f.user.ID, comment.ID, comment.Version); err != nil {
						t.Fatal(err)
					}
					current, _ := f.repository.GetRun(t.Context(), f.user.ID, run.ID)
					if current.Status != domain.RunCancelling {
						t.Fatal("deleting work comment did not stop active Run")
					}
				}
			}
			setSQLiteExecutionIssueStatus(t, &f, domain.IssueClosed)
			setSQLiteExecutionIssueStatus(t, &f, domain.IssueOpen)
			advanceWork(t, f, 0)
			if _, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(24*time.Hour), time.Minute); err != nil || ok {
				t.Fatalf("cleared work resurrected %v %v", ok, err)
			}
		})
	}
}

func TestIssueWorkItemsReopenAndMissingMetadataFailClosed(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	finishWork(t, f, consumeWork(t, f), "persisted result", "completed")
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repository = reopened
	defer reopened.Close()
	if err := reopened.RecoverLocalExecution(t.Context()); err != nil {
		t.Fatal(err)
	}
	advanceWork(t, f, 1)
	claim, ok, err := reopened.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := reopened.db.Exec(`DELETE FROM app_metadata WHERE key=?`, issueWorkKey(f.issue.ID, comment.ID)); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "must not downgrade"}}, "mention:"+claim.MentionID)
	if err == nil {
		t.Fatal("metadata loss silently became ordinary mention")
	}
	if _, err := reopened.GetIssueComment(t.Context(), f.user.ID, comment.ID); err == nil {
		t.Fatal("metadata loss invisible to UI")
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("metadata loss started another run %d %v", count, err)
	}
}

func TestIssueWorkItemsCorruptMetadataCannotBecomeOrdinaryMention(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	const corrupt = `{"schema":2,"items":[{}]}`
	if _, err := f.repository.db.Exec(`UPDATE app_metadata SET value=? WHERE key=?`, corrupt, issueWorkKey(f.issue.ID, comment.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "ordinary context must not run"}}, "mention:"+claim.MentionID); err == nil {
		t.Fatal("corrupt metadata dispatched")
	}
	if _, err := f.repository.AdvanceIssueWorkItems(t.Context(), time.Now()); err == nil {
		t.Fatal("corrupt batch silently ignored/repaired")
	}
	var raw string
	if err := f.repository.db.QueryRow(`SELECT value FROM app_metadata WHERE key=?`, issueWorkKey(f.issue.ID, comment.ID)).Scan(&raw); err != nil || raw != corrupt {
		t.Fatal("corrupt metadata overwritten")
	}
	var runs int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("corrupt batch created Run")
	}
}

func TestIssueWorkItemsInterruptedRunRecoveryBlocksDependents(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	run := consumeWork(t, f)
	if _, err := f.repository.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID, RunIssueCommentWrite{Body: "published before interrupted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{ID: "started", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.started", OccurredAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repository = s
	defer s.Close()
	if err := s.RecoverLocalExecution(t.Context()); err != nil {
		t.Fatal(err)
	}
	advanceWork(t, f, 0)
	view := workComment(t, f, comment.ID)
	if view.WorkItems[0].Status != domain.RunFailed || view.WorkItems[1].Status != "blocked" || view.WorkItems[1].MentionID != "" {
		t.Fatalf("interrupted work released child: %+v", view.WorkItems)
	}
}

func TestIssueWorkItemsConcurrentAdvancementReleasesOneMention(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
	finishWork(t, f, consumeWork(t, f), "published", "completed")
	other, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	counts := make(chan int, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := f.repository
			if i%2 != 0 {
				s = other
			}
			count, err := s.AdvanceIssueWorkItems(t.Context(), time.Now())
			if err != nil {
				t.Error(err)
			}
			counts <- count
		}()
	}
	wg.Wait()
	close(counts)
	total := 0
	for count := range counts {
		total += count
	}
	if total != 1 {
		t.Fatalf("released %d mentions, want one", total)
	}
	mentions, err := f.repository.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 2 || workComment(t, f, comment.ID).WorkItems[1].MentionID == "" {
		t.Fatal("concurrent advancement lost/duplicated binding")
	}
}
