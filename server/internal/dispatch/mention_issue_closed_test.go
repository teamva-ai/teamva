package dispatch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

// Close through the public repository operation immediately after a successful
// claim, deterministically exercising the claim-to-creation race in dispatcher.
type closeIssueAfterClaimStore struct {
	*store.SQLite
	closed      bool
	retries     int
	deadLetters int
}

func (s *closeIssueAfterClaimStore) ClaimNextIssueMention(ctx context.Context, now time.Time, lease time.Duration) (store.IssueMentionClaim, bool, error) {
	claim, ok, err := s.SQLite.ClaimNextIssueMention(ctx, now, lease)
	if err != nil || !ok || s.closed {
		return claim, ok, err
	}
	issue, err := s.SQLite.GetIssue(ctx, claim.UserID, claim.IssueID)
	if err == nil {
		_, err = s.SQLite.UpdateIssue(ctx, claim.UserID, issue.ID, issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueClosed })
	}
	s.closed = err == nil
	return claim, ok && err == nil, err
}

func (s *closeIssueAfterClaimStore) RetryLocalIssueMention(ctx context.Context, mentionID, claimToken, lastError string, availableAt time.Time) error {
	s.retries++
	return s.SQLite.RetryLocalIssueMention(ctx, mentionID, claimToken, lastError, availableAt)
}

func (s *closeIssueAfterClaimStore) DeadLetterLocalIssueMention(ctx context.Context, mentionID, claimToken, lastError string) error {
	s.deadLetters++
	return s.SQLite.DeadLetterLocalIssueMention(ctx, mentionID, claimToken, lastError)
}

func TestLocalDispatcherPreservesMentionWhenIssueClosesAfterClaim(t *testing.T) {
	repository, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := repository.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: provider.ID, Name: "fixture", Status: domain.AgentActive,
	}, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "closure fixture"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: "close after claim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "delegate after reopen only",
	}, []string{agent.ID}); err != nil {
		t.Fatal(err)
	}
	wrapped := &closeIssueAfterClaimStore{SQLite: repository}
	local := &Local{repository: wrapped}
	if worked, err := local.processMention(t.Context()); err != nil || !worked {
		t.Fatalf("processMention=%v %v", worked, err)
	}
	if wrapped.retries != 1 || wrapped.deadLetters != 0 {
		t.Fatalf("close-after-claim must preserve queue: retries=%d deadLetters=%d", wrapped.retries, wrapped.deadLetters)
	}
	runs, err := repository.ListRunsByIssue(t.Context(), user.ID, issue.ID, 100)
	if err != nil || len(runs) != 0 {
		t.Fatalf("closed Issue created Run: %+v %v", runs, err)
	}
	if worked, err := local.processMention(t.Context()); err != nil || worked {
		t.Fatalf("closed Issue was reprocessed: %v %v", worked, err)
	}
	mentions, err := repository.ListIssueCommentMentions(t.Context(), user.ID, issue.ID)
	if err != nil || len(mentions) != 1 || mentions[0].ConsumedAt != nil {
		t.Fatalf("pending mention was discarded: %+v %v", mentions, err)
	}
	issue, err = repository.GetIssue(t.Context(), user.ID, issue.ID)
	if err != nil || issue.Status != domain.IssueClosed {
		t.Fatalf("Issue auto-reopened: %+v %v", issue, err)
	}
	if _, err := repository.UpdateIssue(t.Context(), user.ID, issue.ID, issue.Version, func(issue *domain.Issue) { issue.Status = domain.IssueOpen }); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repository.ClaimNextIssueMention(t.Context(), time.Now().UTC().Add(time.Second), time.Minute)
	if err != nil || !ok || claim.MentionID != mentions[0].ID {
		t.Fatalf("explicit reopen lost pending mention: %+v %v %v", claim, ok, err)
	}
	if _, err := repository.CreateRunForClaimedMention(t.Context(), claim, []domain.ContentBlock{{Type: "text", Text: "explicitly reopened"}}, "mention:"+claim.MentionID); err != nil {
		t.Fatal(err)
	}
}
