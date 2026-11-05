package store

import (
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func claimQueueControlMention(t *testing.T, f sqliteExecutionFixture) IssueMentionClaim {
	t.Helper()
	_, err := f.repository.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: f.issue.ID, AuthorKind: "user", Body: "ordinary queued activation"}, []string{f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := f.repository.ClaimNextIssueMention(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	return claim
}

func TestIssueWorkItemsMentionQueueClearFencesUnmentionedWorkAcrossRecovery(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	items := []nodeprotocol.WorkPlanItem{workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a")}
	comment := createWorkPlan(t, f, items...)
	run := consumeWork(t, f)
	claim := claimQueueControlMention(t, f)
	if err := f.repository.DeadLetterIssueMention(t.Context(), claim, "queue_cleared"); err != nil {
		t.Fatal(err)
	}
	if item := workComment(t, f, comment.ID).WorkItems[1]; item.Status != "cancelled" || item.MentionID != "" {
		t.Fatalf("hidden waiting work survived queue clear: %+v", item)
	}
	current, err := f.repository.GetRun(t.Context(), f.user.ID, run.ID)
	if err != nil || current.Status != domain.RunQueued {
		t.Fatal("clearing pending work stopped an already admitted Run")
	}
	finishWork(t, f, run, "published after queue clear", "completed")
	advanceWork(t, f, 0)
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
	replay, err := s.CreateIssueWorkComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: f.issue.ID, AuthorKind: "user", Body: "confirmed discussion"}, items, "confirmed")
	if err != nil || replay.ID != comment.ID || replay.WorkItems[1].Status != "cancelled" {
		t.Fatalf("receipt replay revived cleared work: %+v %v", replay, err)
	}
	for range 2 {
		if err := s.ClearIssueWorkItems(t.Context(), f.user.ID, f.issue.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.ClaimNextIssueMention(t.Context(), time.Now().Add(24*time.Hour), time.Minute); err != nil || ok {
		t.Fatalf("queue clear did not survive recovery: %v %v", ok, err)
	}
	fresh, err := s.CreateIssueWorkComment(t.Context(), f.user.ID, domain.IssueComment{IssueID: f.issue.ID, AuthorKind: "user", Body: "new confirmed work"}, []nodeprotocol.WorkPlanItem{workPlanItem(f.agent.ID, "fresh")}, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeadLetterIssueMention(t.Context(), claim, "queue_cleared"); err == nil {
		t.Fatal("old clear claim retained authority over new work")
	}
	if workComment(t, f, fresh.ID).WorkItems[0].Status != "ready" {
		t.Fatal("stale clearing worker cancelled newly confirmed work")
	}
}

func TestIssueWorkItemsMentionQueueClearRollsBackAndDoesNotWidenOrdinaryErrors(t *testing.T) {
	for _, reason := range []string{"queue_cleared", "invalid_argument"} {
		t.Run(reason, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			comment := createWorkPlan(t, f, workPlanItem(f.agent.ID, "a"), workPlanItem(f.agent.ID, "b", "a"))
			run := consumeWork(t, f)
			claim := claimQueueControlMention(t, f)
			if reason == "queue_cleared" {
				if _, err := f.repository.db.Exec(`CREATE TRIGGER fail_queue_work BEFORE UPDATE ON app_metadata WHEN NEW.key GLOB 'issue_work:v1:*' BEGIN SELECT RAISE(ABORT,'test queue clear failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := f.repository.DeadLetterLocalIssueMention(t.Context(), claim.MentionID, claim.ClaimToken, reason); err == nil {
					t.Fatal("metadata failure partially cleared mention")
				}
				var status, token string
				if err := f.repository.db.QueryRow(`SELECT status,claim_token FROM issue_comment_mentions WHERE id=?`, claim.MentionID).Scan(&status, &token); err != nil || status != "processing" || token != claim.ClaimToken {
					t.Fatal("failed clear lost original claim")
				}
				if item := workComment(t, f, comment.ID).WorkItems[1]; item.Status != "waiting_dependencies" {
					t.Fatal("failed clear persisted cancellation")
				}
				if _, err := f.repository.db.Exec(`DROP TRIGGER fail_queue_work`); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.repository.DeadLetterLocalIssueMention(t.Context(), claim.MentionID, claim.ClaimToken, reason); err != nil {
				t.Fatal(err)
			}
			finishWork(t, f, run, "published", "completed")
			want := 1
			if reason == "queue_cleared" {
				want = 0
			}
			advanceWork(t, f, want)
			if item := workComment(t, f, comment.ID).WorkItems[1]; (reason == "queue_cleared") != (item.Status == "cancelled") {
				t.Fatalf("wrong clear/error cancellation scope: %+v", item)
			}
		})
	}
}
