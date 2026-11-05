package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteIssueCommentInstanceGenerationContract(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	repository := fixture.repository

	project, err := repository.CreateProject(ctx, fixture.user.ID, domain.Project{
		ComputerID: fixture.computer.ID, Name: "Issue project",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: fixture.service.ID,
		Name: "Issue Agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repository.CreateAgentRevision(ctx, fixture.user.ID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: fixture.service.ID, SystemPrompt: "work",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, fixture.user.ID, domain.Issue{
		ProjectID: project.ID, Title: "Durable issue", Description: "before restart",
		WorktreeHint: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Direct server workflows reuse the latest conversation explicitly.
	// Structured @ mentions are tested separately and always create a new one.
	instance, created, err := repository.EnsureIssueAgentInstance(ctx, fixture.user.ID, issue.ID, agent.ID)
	if err != nil || !created || instance.AgentRevisionID != revision.ID {
		t.Fatalf("initial ensure failed: created=%t instance=%+v err=%v", created, instance, err)
	}
	again, created, err := repository.EnsureIssueAgentInstance(ctx, fixture.user.ID, issue.ID, agent.ID)
	if err != nil || created || again.ID != instance.ID {
		t.Fatalf("sequential ensure did not reuse latest conversation: created=%t instance=%+v err=%v", created, again, err)
	}

	comment, err := repository.CreateIssueComment(ctx, fixture.user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: fixture.user.ID, Body: "@Issue Agent please work",
	}, []string{agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := repository.ListIssueCommentMentions(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 1 || mentions[0].CommentID != comment.ID || mentions[0].TargetAgentID != agent.ID {
		t.Fatalf("unexpected mentions: %+v", mentions)
	}

	// 一条合法 mention 后跟一条非法 mention 时，评论和 mentions 都不能部分落库。
	otherUserID, otherComputerID := seedSQLiteOtherIdentity(t, repository, "comment-other")
	otherService, err := repository.UpsertAgentService(ctx, otherUserID, domain.AgentService{
		ComputerID: otherComputerID, Provider: "other", LoginStatus: "authenticated",
		Status: "ready", Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := repository.CreateAgent(ctx, otherUserID, domain.Agent{
		ComputerID: otherComputerID, AgentServiceID: otherService.ID,
		Name: "Foreign Agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateIssueComment(ctx, fixture.user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: fixture.user.ID, Body: "must rollback",
	}, []string{agent.ID, otherAgent.ID})
	assertDomainCode(t, err, "invalid_argument")
	comments, err := repository.ListIssueComments(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	mentions, err = repository.ListIssueCommentMentions(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || len(mentions) != 1 {
		t.Fatalf("failed comment transaction leaked rows: comments=%+v mentions=%+v", comments, mentions)
	}

	// 修订插入先发生；空正文触发 UPDATE CHECK 失败后，修订也必须回滚。
	if _, err := repository.UpdateIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version,
		func(item *domain.IssueComment) error { item.Body = ""; return nil }); err == nil {
		t.Fatal("empty comment update unexpectedly succeeded")
	}
	revisions, err := repository.ListIssueCommentRevisions(ctx, fixture.user.ID, comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentComment, err := repository.GetIssueComment(ctx, fixture.user.ID, comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 0 || currentComment.Body != comment.Body || currentComment.Version != comment.Version {
		t.Fatalf("failed comment revision leaked state: revisions=%+v comment=%+v", revisions, currentComment)
	}
	updatedComment, err := repository.UpdateIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version,
		func(item *domain.IssueComment) error { item.Body = "edited"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if updatedComment.EditedAt == nil || updatedComment.Version != comment.Version+1 {
		t.Fatalf("comment edit metadata is wrong: %+v", updatedComment)
	}
	_, err = repository.UpdateIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version,
		func(item *domain.IssueComment) error { item.Body = "stale"; return nil })
	assertDomainCode(t, err, "version_conflict")
	revisions, err = repository.ListIssueCommentRevisions(ctx, fixture.user.ID, comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Body != comment.Body {
		t.Fatalf("unexpected comment history: %+v", revisions)
	}

	if err := repository.ConsumeIssueCommentMention(ctx, fixture.user.ID, issue.ID, mentions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConsumeIssueCommentMention(ctx, fixture.user.ID, issue.ID, mentions[0].ID); err != nil {
		t.Fatalf("mention consumption is not idempotent: %v", err)
	}
	mentions, err = repository.ListIssueCommentMentions(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mentions[0].ConsumedAt == nil {
		t.Fatal("mention consumption was not persisted")
	}

	lastActivity := updatedComment.UpdatedAt.Add(10)
	updatedInstance, err := repository.UpdateIssueAgentInstance(ctx, fixture.user.ID, instance.ID, instance.Version,
		func(item *domain.IssueAgentInstance) {
			item.Status = domain.AgentInstancePaused
			item.LastActivityAt = &lastActivity
			item.PendingMentionCount = 2
		})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateIssueAgentInstance(ctx, fixture.user.ID, instance.ID, instance.Version,
		func(item *domain.IssueAgentInstance) { item.Status = domain.AgentInstanceRunning })
	assertDomainCode(t, err, "version_conflict")
	generation, err := repository.CreateIssueAgentGeneration(ctx, fixture.user.ID, domain.IssueAgentGeneration{
		IssueAgentInstanceID: instance.ID, AgentRevisionID: revision.ID,
		NativeSessionKey: "native-session",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = OpenSQLite(ctx, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	restoredIssue, err := repository.GetIssue(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredComments, err := repository.ListIssueComments(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredInstance, err := repository.GetIssueAgentInstance(ctx, fixture.user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredGeneration, err := repository.GetIssueAgentGeneration(ctx, fixture.user.ID, generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredIssue.Title != issue.Title ||
		len(restoredComments) != 1 || restoredComments[0].Body != "edited" ||
		restoredInstance.Version != updatedInstance.Version || restoredInstance.Status != domain.AgentInstancePaused ||
		restoredGeneration.NativeSessionKey != generation.NativeSessionKey {
		t.Fatalf("Issue graph changed after restart: issue=%+v comments=%+v instance=%+v generation=%+v",
			restoredIssue, restoredComments, restoredInstance, restoredGeneration)
	}
}

func TestSQLiteConcurrentIssueCommentLoad(t *testing.T) {
	fixture := newSQLiteAgentTestFixture(t)
	project, err := fixture.repository.CreateProject(t.Context(), fixture.user.ID, domain.Project{
		ComputerID: fixture.computer.ID, Name: "Concurrent comment load",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := fixture.repository.CreateIssue(t.Context(), fixture.user.ID, domain.Issue{
		ProjectID: project.ID, Title: "Concurrent comments", Status: domain.IssueOpen,
		CreatedByUserID: fixture.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	const (
		workers           = 16
		commentsPerWorker = 25
	)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	started := time.Now()
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range commentsPerWorker {
				_, createErr := fixture.repository.CreateIssueComment(ctx, fixture.user.ID, domain.IssueComment{
					IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
					AuthorUserID: fixture.user.ID,
					Body:         fmt.Sprintf("worker-%02d-comment-%02d", worker, index),
				}, nil)
				if createErr != nil {
					errors <- createErr
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	comments, err := fixture.repository.ListIssueComments(t.Context(), fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := workers * commentsPerWorker
	if len(comments) != want {
		t.Fatalf("comments=%d want=%d", len(comments), want)
	}
	seenBodies := make(map[string]struct{}, want)
	for _, comment := range comments {
		seenBodies[comment.Body] = struct{}{}
	}
	if len(seenBodies) != want {
		t.Fatalf("unique comment bodies=%d want=%d", len(seenBodies), want)
	}
	duration := time.Since(started)
	t.Logf("persisted %d concurrent comments in %s (%.0f comments/s)", want, duration, float64(want)/duration.Seconds())
}

func TestSQLiteIssueTenantIsolationAndOptimisticLock(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	defer func() { _ = fixture.repository.Close() }()
	repository := fixture.repository

	ownerProject, err := repository.CreateProject(ctx, fixture.user.ID, domain.Project{
		ComputerID: fixture.computer.ID, Name: "Owner project",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, fixture.user.ID, domain.Issue{
		ProjectID: ownerProject.ID, Title: "Owner issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateIssue(ctx, fixture.user.ID, issue.ID, issue.Version,
		func(item *domain.Issue) { item.Status = domain.IssueResolved })
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateIssue(ctx, fixture.user.ID, issue.ID, issue.Version,
		func(item *domain.Issue) { item.Title = "stale" })
	assertDomainCode(t, err, "version_conflict")
	if err := repository.DeleteIssue(ctx, fixture.user.ID, issue.ID, issue.Version); err == nil {
		t.Fatal("stale Issue delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "version_conflict")
	}

	otherUserID, otherComputerID := seedSQLiteOtherIdentity(t, repository, "issue-other")
	otherProject, err := repository.CreateProject(ctx, otherUserID, domain.Project{
		ComputerID: otherComputerID, Name: "Other project",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherIssue, err := repository.CreateIssue(ctx, otherUserID, domain.Issue{
		ProjectID: otherProject.ID, Title: "Other issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherComment, err := repository.CreateIssueComment(ctx, otherUserID, domain.IssueComment{
		IssueID: otherIssue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: otherUserID, Body: "private",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = repository.ListIssues(ctx, fixture.user.ID, otherProject.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetIssue(ctx, fixture.user.ID, otherIssue.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.ListIssueComments(ctx, fixture.user.ID, otherIssue.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.GetIssueComment(ctx, fixture.user.ID, otherComment.ID)
	assertDomainCode(t, err, "not_found")
	if err := repository.ConsumeIssueCommentMention(ctx, fixture.user.ID, otherIssue.ID, "unknown"); err == nil {
		t.Fatal("cross-tenant mention consume unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "not_found")
	}
	if err := repository.DeleteIssue(ctx, fixture.user.ID, otherIssue.ID, otherIssue.Version); err == nil {
		t.Fatal("cross-tenant Issue delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "not_found")
	}
	if updated.Version != issue.Version+1 {
		t.Fatalf("unexpected Issue version: %+v", updated)
	}
}

func TestSQLiteIssueCommentDeleteRevokesPendingMentions(t *testing.T) {
	ctx := context.Background()
	fixture := newSQLiteAgentTestFixture(t)
	defer func() { _ = fixture.repository.Close() }()
	repository := fixture.repository

	project, err := repository.CreateProject(ctx, fixture.user.ID, domain.Project{
		ComputerID: fixture.computer.ID, Name: "Delete comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(ctx, fixture.user.ID, domain.Agent{
		ComputerID: fixture.computer.ID, AgentServiceID: fixture.service.ID,
		Name: "Delete Agent", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(ctx, fixture.user.ID, domain.Issue{
		ProjectID: project.ID, Title: "Delete issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := repository.CreateIssueComment(ctx, fixture.user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: fixture.user.ID, Body: "@Delete Agent 干活",
	}, []string{agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	assertDomainCode(t, repository.DeleteIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version+1), "version_conflict")
	if err := repository.DeleteIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version); err != nil {
		t.Fatal(err)
	}
	assertDomainCode(t, repository.DeleteIssueComment(ctx, fixture.user.ID, comment.ID, comment.Version+1), "not_found")
	_, err = repository.GetIssueComment(ctx, fixture.user.ID, comment.ID)
	assertDomainCode(t, err, "not_found")
	comments, err := repository.ListIssueComments(ctx, fixture.user.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("deleted comment still listed: %+v", comments)
	}

	var status, lastError string
	if err := repository.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(last_error, '') FROM issue_comment_mentions WHERE comment_id = ?
	`, comment.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "dead_letter" || lastError != "mention_target_deleted" {
		t.Fatalf("deleted comment mention = %s/%s", status, lastError)
	}
	if claim, ok, err := repository.ClaimNextIssueMention(ctx, time.Now().UTC(), time.Minute); err != nil || ok {
		t.Fatalf("deleted comment mention was claimed: %+v ok=%t err=%v", claim, ok, err)
	}
}
