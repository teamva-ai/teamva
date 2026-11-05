package toolservice

import (
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestSQLiteToolWorkItemsUseAtomicSummaryAndKeepSelfMentionFence(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "work-plan-source")
	items := []nodeprotocol.WorkPlanItem{
		{Key: "first", AgentID: f.b.ID, Title: "first", Instruction: "first task"},
		{Key: "again", AgentID: f.b.ID, Title: "again", After: []string{"first"}},
	}
	request := commentRequest(run, "work-plan", "confirmed plan")
	request.Payload["work_items"] = items
	result := New(f.s).Execute(t.Context(), f.computer.ID, request)
	if !result.Result.OK || result.ActivationIssueID != f.issue.ID {
		t.Fatalf("plan write=%+v", result)
	}
	commentID := result.Result.Data["comment_id"].(string)
	comment, err := f.s.GetIssueComment(t.Context(), f.user.ID, commentID)
	if err != nil || len(comment.WorkItems) != 2 || len(f.mentions(t)) != 1 || comment.Status != domain.IssueCommentPublished {
		t.Fatalf("comment=%+v err=%v", comment, err)
	}
	replay := New(f.s).Execute(t.Context(), f.computer.ID, request)
	if !replay.Result.OK || replay.ActivationIssueID != "" || len(f.mentions(t)) != 1 {
		t.Fatalf("replay duplicated plan=%+v", replay)
	}
	// Ordinary summary correction still upserts the same comment, not its tasks.
	updated := f.publish(t, run, "summary-update", "reworded summary")
	if updated.Result.Data["comment_id"] != commentID {
		t.Fatal("work plan broke single-summary upsert")
	}
	comment, err = f.s.GetIssueComment(t.Context(), f.user.ID, commentID)
	if err != nil || len(comment.WorkItems) != 2 || comment.WorkItems[0].Instruction != "first task" {
		t.Fatalf("summary update changed confirmed tasks: %+v %v", comment, err)
	}
	mutated := append([]nodeprotocol.WorkPlanItem(nil), items...)
	mutated[0].Instruction = "changed after confirmation"
	changed := commentRequest(run, "mutated-plan", "must roll back summary")
	changed.Payload["work_items"] = mutated
	if got := New(f.s).Execute(t.Context(), f.computer.ID, changed); got.Result.OK || got.Result.ErrorCode != "work_plan_immutable" {
		t.Fatalf("confirmed plan mutated=%+v", got)
	}
	if got := f.comment(t, commentID); got.Body != "reworded summary" {
		t.Fatal("rejected plan edit partially updated summary")
	}
	self := commentRequest(run, "self-plan", "forbidden")
	self.Payload["work_items"] = []nodeprotocol.WorkPlanItem{{Key: "self", AgentID: f.a.ID, Title: "self"}}
	if got := New(f.s).Execute(t.Context(), f.computer.ID, self); got.Result.ErrorCode != "self_mention" {
		t.Fatalf("tool self protection lost=%+v", got)
	}
	// The persistence boundary independently rejects self even without ToolService.
	if _, err := f.s.WriteRunIssueComment(t.Context(), f.user.ID, f.computer.ID, run.ID,
		store.RunIssueCommentWrite{Body: "forbidden", WorkItems: []nodeprotocol.WorkPlanItem{{Key: "self", AgentID: f.a.ID, Title: "self"}}}); err == nil {
		t.Fatal("direct store bypassed self protection")
	}
	for name, value := range map[string]any{"empty": []any{}, "not-array": "invalid", "unknown-field": []map[string]any{{"key": "x", "agent_id": f.b.ID, "title": "x", "unexpected": true}}} {
		bad := commentRequest(run, name, "invalid")
		bad.Payload["work_items"] = value
		if got := New(f.s).Execute(t.Context(), f.computer.ID, bad); got.Result.OK {
			t.Fatalf("malformed work_items accepted: %s", name)
		}
	}
	mixed := commentRequest(run, "mixed", "invalid", f.b.ID)
	mixed.Payload["work_items"] = items
	if got := New(f.s).Execute(t.Context(), f.computer.ID, mixed); got.Result.OK {
		t.Fatal("work_items and ordinary mentions mixed")
	}
	if _, err := f.s.StopRun(t.Context(), f.user.ID, run.ID, "force"); err != nil {
		t.Fatal(err)
	}
	late := commentRequest(run, "late-plan", "late")
	late.Payload["work_items"] = items
	if got := New(f.s).Execute(t.Context(), f.computer.ID, late); got.Result.ErrorCode != "run_not_active" {
		t.Fatalf("stopped source delegated=%+v", got)
	}
}

func TestSQLiteToolWorkPlanRollsBackWithSummaryAndClosedIssue(t *testing.T) {
	f := newSQLiteToolFixture(t)
	run := f.run(t, f.a, f.issue, "source")
	first := f.publish(t, run, "initial", "initial")
	commentID := first.Result.Data["comment_id"].(string)
	request := commentRequest(run, "plan", "plan summary")
	request.Payload["work_items"] = []nodeprotocol.WorkPlanItem{{Key: "b", AgentID: f.b.ID, Title: "B task"}}
	if _, err := f.db.Exec(`CREATE TRIGGER fail_tool_work_plan BEFORE INSERT ON app_metadata WHEN NEW.key GLOB 'issue_work:v1:*' BEGIN SELECT RAISE(ABORT,'test plan error'); END`); err != nil {
		t.Fatal(err)
	}
	if result := New(f.s).Execute(t.Context(), f.computer.ID, request); result.Result.OK {
		t.Fatal("metadata failure succeeded")
	}
	if got := f.comment(t, commentID); got.Body != "initial" || got.Version != 1 || len(f.mentions(t)) != 0 {
		t.Fatal("work plan failure left partial summary/delegation")
	}
	if _, err := f.db.Exec(`DROP TRIGGER fail_tool_work_plan`); err != nil {
		t.Fatal(err)
	}
	issue, err := f.s.UpdateIssue(t.Context(), f.user.ID, f.issue.ID, f.issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueClosed })
	if err != nil {
		t.Fatal(err)
	}
	if result := New(f.s).Execute(t.Context(), f.computer.ID, request); result.Result.OK || result.Result.ErrorCode != "issue_not_open" {
		t.Fatalf("closed issue delegated=%+v", result)
	}
	if _, err := f.s.UpdateIssue(t.Context(), f.user.ID, issue.ID, issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueOpen }); err != nil {
		t.Fatal(err)
	}
	if result := New(f.s).Execute(t.Context(), f.computer.ID, request); !result.Result.OK {
		t.Fatal(result)
	}
	claim, ok, err := f.s.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "work"}}, "mention:"+claim.MentionID); err != nil {
		t.Fatal(err)
	}
}
