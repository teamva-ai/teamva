package store

import (
	"context"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteProjectSearchCoversVisibleContentAndExcludesPrivateEvents(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteExecutionFixture(t)
	defer func() { _ = f.repository.Close() }()

	if _, err := f.repository.UpdateIssue(ctx, f.user.ID, f.issue.ID, f.issue.Version, func(issue *domain.Issue) {
		issue.Description = "描述里的 recovery-marker"
	}); err != nil {
		t.Fatal(err)
	}
	comment, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: f.user.ID, Body: "评论里的 comment-marker",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := f.repository.CreateMemory(ctx, f.user.ID, domain.ProjectMemoryEntry{
		ProjectID: f.project.ID, Key: "search", Kind: "fact",
		Title: "检索记忆", Content: "记忆里的 memory-marker", SourceType: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := f.repository.GetIssue(ctx, f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The comment write updates Issue activity/version while the explicit Issue
	// patch above created the description; always use the latest optimistic lock.
	issue, err = f.repository.GetIssue(ctx, f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := f.repository.CreateRun(ctx, f.user.ID, f.issue.ID, issue.Version, "search-run", domain.Run{
		IssueAgentInstanceID: f.instance.ID, AgentID: f.agent.ID,
		Input: []domain.ContentBlock{{Type: "text", Text: "# 任务（Issue）上下文\n\n## 本次指令\n请处理 user-marker\n\n请阅读上下文后开始工作"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []nodeprotocol.RunEvent{
		{ID: "evt-search-start", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1, Type: "run.started", Payload: map[string]any{}, OccurredAt: now},
		{ID: "evt-search-delta", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "assistant-marker"}, OccurredAt: now.Add(time.Millisecond)},
		{ID: "evt-search-assistant-one", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 3, Type: "provider.item", Payload: map[string]any{"item": map[string]any{"id": "assistant-one", "type": "agentMessage", "text": "assistant-marker"}}, OccurredAt: now.Add(2 * time.Millisecond)},
		{ID: "evt-search-reasoning", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 4, Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "reasoning", "text": "private-marker"}}, OccurredAt: now.Add(3 * time.Millisecond)},
		{ID: "evt-search-tool", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 5, Type: "provider.tool", Payload: map[string]any{"output": "tool-marker"}, OccurredAt: now.Add(4 * time.Millisecond)},
		{ID: "evt-search-internal", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 6, Type: "user.steer", Payload: map[string]any{"text": "internal-marker", "internal": true}, OccurredAt: now.Add(5 * time.Millisecond)},
		{ID: "evt-search-steer", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 7, Type: "user.steer", Payload: map[string]any{"text": "followup-marker", "internal": false}, OccurredAt: now.Add(6 * time.Millisecond)},
		{ID: "evt-search-delta-two", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 8, Type: "output.delta", Payload: map[string]any{"text": "older-turn-marker"}, OccurredAt: now.Add(7 * time.Millisecond)},
		{ID: "evt-search-assistant-two", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 9, Type: "provider.item", Payload: map[string]any{"item": map[string]any{"id": "assistant-two", "type": "agentMessage", "text": "older-turn-marker"}}, OccurredAt: now.Add(8 * time.Millisecond)},
		{ID: "evt-search-image-echo", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 10, Type: "output.delta", Payload: map[string]any{"text": "data:image/png;base64,search-secret"}, OccurredAt: now.Add(9 * time.Millisecond)},
	}
	for index := range events {
		events[index].Payload["provider_id"] = "codex"
	}
	if _, err := f.repository.ApplyLocalEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	assertHit := func(query, kind, targetID string) {
		t.Helper()
		page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, query, 30)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Kind == kind && (item.MemoryID == targetID || item.CommentID == targetID || item.RunID == targetID || item.IssueID == targetID) {
				return
			}
		}
		t.Fatalf("query %q did not return %s target %s: %+v", query, kind, targetID, page.Items)
	}
	assertHit("memory-marker", domain.ProjectSearchMemory, memory.ID)
	assertHit("recovery-marker", domain.ProjectSearchIssueDescription, f.issue.ID)
	assertHit("comment-marker", domain.ProjectSearchIssueComment, comment.ID)
	assertHit("user-marker", domain.ProjectSearchSessionMessage, run.ID)
	assertHit("assistant-marker", domain.ProjectSearchSessionMessage, run.ID)
	assertHit("older-turn-marker", domain.ProjectSearchSessionMessage, run.ID)
	assertHit("followup-marker", domain.ProjectSearchSessionMessage, run.ID)

	for _, query := range []string{"private-marker", "tool-marker", "internal-marker", "search-secret"} {
		page, err := f.repository.SearchProjectContent(ctx, f.user.ID, f.project.ID, query, 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("private query %q leaked search hits: %+v", query, page.Items)
		}
	}
}

func TestSQLiteProjectSearchIsTenantScopedAndBounded(t *testing.T) {
	ctx := context.Background()
	repository, user, _, project := newSQLiteFeatureProject(t, ctx)
	defer func() { _ = repository.Close() }()
	for index := 0; index < 4; index++ {
		if _, err := repository.CreateMemory(ctx, user.ID, domain.ProjectMemoryEntry{
			ProjectID: project.ID, Key: newID("key"), Kind: "fact",
			Title: "limit marker", Content: "limit marker", SourceType: "user",
		}, "user"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.SearchProjectContent(ctx, user.ID, project.ID, "limit marker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.Truncated {
		t.Fatalf("bounded search = %+v, want 2 truncated results", page)
	}
	otherUserID, _ := insertSQLiteFeatureTenant(t, ctx, repository)
	_, err = repository.SearchProjectContent(ctx, otherUserID, project.ID, "limit marker", 30)
	assertDomainCode(t, err, "not_found")
}
