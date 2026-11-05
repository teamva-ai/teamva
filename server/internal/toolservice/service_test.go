package toolservice

import (
	"context"
	"errors"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

type fakeRepository struct {
	scope       store.RunScope
	scopeErr    error
	issue       domain.Issue
	comments    []domain.IssueComment
	comment     domain.IssueComment
	agents      []domain.Agent
	instances   []domain.IssueAgentInstance
	runs        []domain.Run
	events      map[string][]domain.RunEvent
	created     domain.IssueComment
	createCalls int
	mentioned   []string
	updatedFor  string
	version     int64
}

func (f *fakeRepository) GetRun(_ context.Context, _ string, id string) (domain.Run, error) {
	for _, run := range f.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return domain.Run{}, errors.New("run not found")
}

func (f *fakeRepository) ExecuteWorkReadTool(context.Context, string, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("unexpected assistant query in issue tool test")
}

func (f *fakeRepository) GetRunScope(context.Context, string) (store.RunScope, error) {
	return f.scope, f.scopeErr
}

func (f *fakeRepository) GetIssue(_ context.Context, userID, issueID string) (domain.Issue, error) {
	if userID != f.scope.UserID || issueID != f.scope.IssueID {
		return domain.Issue{}, errors.New("wrong scope")
	}
	return f.issue, nil
}

func (f *fakeRepository) ListIssueComments(_ context.Context, userID, issueID string) ([]domain.IssueComment, error) {
	if userID != f.scope.UserID || issueID != f.scope.IssueID {
		return nil, errors.New("wrong scope")
	}
	return f.comments, nil
}

func (f *fakeRepository) ListIssuePromptComments(ctx context.Context, userID, issueID string) ([]domain.IssueComment, bool, error) {
	comments, err := f.ListIssueComments(ctx, userID, issueID)
	return comments, false, err
}

func (f *fakeRepository) CreateIssueComment(
	_ context.Context, userID string, comment domain.IssueComment, mentioned []string,
) (domain.IssueComment, error) {
	if userID != f.scope.UserID || comment.IssueID != f.scope.IssueID {
		return domain.IssueComment{}, errors.New("wrong scope")
	}
	f.createCalls++
	f.created = comment
	f.mentioned = append([]string(nil), mentioned...)
	comment.ID = "comment-created"
	comment.Version = 1
	return comment, nil
}

func (f *fakeRepository) AddIssueCommentMentions(
	_ context.Context, userID, _ string, _ string, mentioned []string,
) error {
	if userID != f.scope.UserID {
		return errors.New("wrong scope")
	}
	f.mentioned = append([]string(nil), mentioned...)
	return nil
}

func (f *fakeRepository) GetIssueComment(context.Context, string, string) (domain.IssueComment, error) {
	if f.comment.ID == "" {
		return domain.IssueComment{}, errors.New("not found")
	}
	return f.comment, nil
}

func (f *fakeRepository) UpdateIssueComment(
	_ context.Context, userID, commentID string, expectedVersion int64,
	mutate func(*domain.IssueComment) error,
) (domain.IssueComment, error) {
	if userID != f.scope.UserID {
		return domain.IssueComment{}, errors.New("wrong scope")
	}
	updated := f.comment
	if err := mutate(&updated); err != nil {
		return domain.IssueComment{}, err
	}
	updated.Version++
	f.updatedFor = commentID
	f.version = expectedVersion
	f.comment = updated
	return updated, nil
}

func (f *fakeRepository) WriteRunIssueComment(ctx context.Context, userID, computerID, runID string, input store.RunIssueCommentWrite) (store.RunIssueCommentResult, error) {
	if userID != f.scope.UserID || computerID != f.scope.ComputerID {
		return store.RunIssueCommentResult{}, errors.New("wrong scope")
	}
	if input.CommentID != "" && (f.comment.IssueID != f.scope.IssueID ||
		f.comment.AuthorKind != domain.IssueCommentAuthorAgent || f.comment.AuthorAgentID != f.scope.AgentID) {
		return store.RunIssueCommentResult{}, domain.Forbidden("not_own_comment", "只能修正当前任务中自己发布的评论")
	}
	status := domain.IssueCommentPublished
	if f.scope.GoalID != "" && f.scope.RunPurpose != domain.RunPurposeGoalFinalize {
		status = domain.IssueCommentWorking
	}
	var comment domain.IssueComment
	var err error
	if input.CommentID != "" || (!input.AppendNew && len(f.comments) > 0) {
		version := input.ExpectedVersion
		if input.CommentID == "" {
			version = f.comment.Version
		}
		comment, err = f.UpdateIssueComment(ctx, userID, f.comment.ID, version, func(item *domain.IssueComment) error {
			item.Body = input.Body
			if input.CommentID == "" {
				item.Status = status
			}
			return nil
		})
		f.mentioned = append([]string(nil), input.MentionedAgentIDs...)
	} else {
		comment, err = f.CreateIssueComment(ctx, userID, domain.IssueComment{
			IssueID: f.scope.IssueID, AuthorKind: domain.IssueCommentAuthorAgent,
			AuthorAgentID: f.scope.AgentID, Body: input.Body, RunID: runID, Status: status,
		}, input.MentionedAgentIDs)
	}
	return store.RunIssueCommentResult{CommentID: comment.ID, IssueID: comment.IssueID,
		Version: comment.Version, EditedAt: comment.EditedAt}, err
}

func (f *fakeRepository) ListIssueAgentInstances(
	_ context.Context, userID, issueID string,
) ([]domain.IssueAgentInstance, error) {
	if userID != f.scope.UserID || issueID != f.scope.IssueID {
		return nil, errors.New("wrong scope")
	}
	return f.instances, nil
}

func (f *fakeRepository) ListAgents(
	_ context.Context, userID, computerID string,
) ([]domain.Agent, error) {
	if userID != f.scope.UserID || computerID != f.scope.ComputerID {
		return nil, errors.New("wrong scope")
	}
	return f.agents, nil
}

func (f *fakeRepository) GetIssueAgentInstance(
	_ context.Context, userID, issueID, agentID string,
) (domain.IssueAgentInstance, error) {
	if userID != f.scope.UserID || issueID != f.scope.IssueID {
		return domain.IssueAgentInstance{}, errors.New("wrong scope")
	}
	for _, instance := range f.instances {
		if instance.AgentID == agentID {
			return instance, nil
		}
	}
	return domain.IssueAgentInstance{}, errors.New("not found")
}

func (f *fakeRepository) ListRunsByInstance(
	_ context.Context, userID, instanceID string, _ int,
) ([]domain.Run, error) {
	if userID != f.scope.UserID || instanceID == "" {
		return nil, errors.New("wrong scope")
	}
	return f.runs, nil
}

func (f *fakeRepository) GetIssueAgentInstanceByID(_ context.Context, _ string, id string) (domain.IssueAgentInstance, error) {
	for _, instance := range f.instances {
		if instance.ID == id {
			return instance, nil
		}
	}
	return domain.IssueAgentInstance{}, errors.New("not found")
}

func (f *fakeRepository) ListRunsByInstancePage(ctx context.Context, userID, instanceID, _ string, _ int) (domain.Page[domain.Run], error) {
	runs, err := f.ListRunsByInstance(ctx, userID, instanceID, 1)
	return domain.Page[domain.Run]{Items: runs}, err
}

func (f *fakeRepository) ListRunEventPage(ctx context.Context, userID, runID string, after int64, limit, _ int) (domain.RunEventPage, error) {
	events, err := f.ListRunEvents(ctx, userID, runID)
	page := domain.RunEventPage{Items: []domain.RunEvent{}}
	for _, event := range events {
		if event.Sequence > after {
			if len(page.Items) == limit {
				page.Truncated = true
				break
			}
			page.Items = append(page.Items, event)
		}
	}
	return page, err
}

func (f *fakeRepository) ListRunEvents(_ context.Context, userID, runID string) ([]domain.RunEvent, error) {
	if userID != f.scope.UserID {
		return nil, errors.New("wrong scope")
	}
	return f.events[runID], nil
}

func TestExecuteCreatesScopedAgentCommentForBothPayloadTransports(t *testing.T) {
	for name, mentions := range map[string]any{
		"websocket-json": []any{"agent-other"},
		"local-direct":   []string{"agent-other"},
	} {
		t.Run(name, func(t *testing.T) {
			repository := newToolRepository()
			execution := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
				RequestID: "request-1", RunID: "run-1", IssueID: "untrusted-issue",
				AgentID: "untrusted-agent", Payload: map[string]any{
					"body": "  completed  ", "mentioned_agent_ids": mentions,
				}, Tool: "issue_comment_create",
			})
			if !execution.Result.OK || execution.ActivationIssueID != "issue-1" {
				t.Fatalf("execution=%+v", execution)
			}
			if repository.created.IssueID != "issue-1" || repository.created.AuthorAgentID != "agent-1" ||
				repository.created.AuthorKind != domain.IssueCommentAuthorAgent || repository.created.RunID != "run-1" ||
				repository.created.Body != "completed" {
				t.Fatalf("created=%+v", repository.created)
			}
			if len(repository.mentioned) != 1 || repository.mentioned[0] != "agent-other" {
				t.Fatalf("mentioned=%v", repository.mentioned)
			}
		})
	}
}

func TestExecuteReusesLatestSessionCommentAcrossRuns(t *testing.T) {
	repository := newToolRepository()
	repository.comment = domain.IssueComment{
		Versioned: domain.Versioned{ID: "session-comment", Version: 2},
		IssueID:   "issue-1", AuthorKind: domain.IssueCommentAuthorAgent,
		AuthorAgentID: "agent-1", RunID: "run-1", Body: "previous summary",
		Status: domain.IssueCommentPublished,
	}
	repository.comments = []domain.IssueComment{repository.comment}
	// 同一 Instance 的新激活 Run（run-2）不带 append_new 时必须复用旧评论。
	execution := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RequestID: "request-session", RunID: "run-2", Tool: "issue_comment_create",
		Payload: map[string]any{"body": "updated summary", "mentioned_agent_ids": []string{}},
	})
	if !execution.Result.OK || repository.createCalls != 0 {
		t.Fatalf("execution=%+v createCalls=%d", execution, repository.createCalls)
	}
	if repository.updatedFor != "session-comment" || repository.comment.Body != "updated summary" {
		t.Fatalf("comment=%+v", repository.comment)
	}
}

func TestExecuteAppendNewCreatesSecondCommentWhenUserAsks(t *testing.T) {
	repository := newToolRepository()
	repository.comment = domain.IssueComment{
		Versioned: domain.Versioned{ID: "session-comment", Version: 2},
		IssueID:   "issue-1", AuthorKind: domain.IssueCommentAuthorAgent,
		AuthorAgentID: "agent-1", RunID: "run-1", Body: "previous summary",
		Status: domain.IssueCommentPublished,
	}
	repository.comments = []domain.IssueComment{repository.comment}
	// 用户明确要求“再发一条”时 append_new=true 必须真正新建。
	execution := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RequestID: "request-append", RunID: "run-2", Tool: "issue_comment_create",
		Payload: map[string]any{
			"body": "explicit second comment", "mentioned_agent_ids": []string{},
			"append_new": true,
		},
	})
	if !execution.Result.OK || repository.createCalls != 1 {
		t.Fatalf("execution=%+v createCalls=%d", execution, repository.createCalls)
	}
}

func TestExecuteReplacesWorkingReplyInsteadOfAppending(t *testing.T) {
	repository := newToolRepository()
	repository.comment = domain.IssueComment{
		Versioned: domain.Versioned{ID: "working-comment", Version: 1},
		IssueID:   "issue-1", AuthorKind: domain.IssueCommentAuthorAgent,
		AuthorAgentID: "agent-1", RunID: "run-1", Body: "[teamva:working]",
		Status: domain.IssueCommentWorking,
	}
	repository.comments = []domain.IssueComment{repository.comment}
	execution := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RequestID: "request-working", RunID: "run-1", Tool: "issue_comment_create",
		Payload: map[string]any{
			"body": "completed once", "mentioned_agent_ids": []string{"agent-other"},
		},
	})
	if !execution.Result.OK || repository.createCalls != 0 {
		t.Fatalf("execution=%+v createCalls=%d", execution, repository.createCalls)
	}
	if repository.updatedFor != "working-comment" || repository.comment.Body != "completed once" ||
		repository.comment.Status != domain.IssueCommentPublished {
		t.Fatalf("comment=%+v", repository.comment)
	}
	if len(repository.mentioned) != 1 || repository.mentioned[0] != "agent-other" {
		t.Fatalf("mentioned=%v", repository.mentioned)
	}
}

func TestExecuteKeepsGoalProgressWorkingUntilFinalizeRun(t *testing.T) {
	repository := newToolRepository()
	repository.scope.GoalID = "goal-1"
	repository.scope.GoalCommentID = "goal-comment"
	repository.scope.RunPurpose = domain.RunPurposeGoalContinuation
	repository.comment = domain.IssueComment{
		Versioned: domain.Versioned{ID: "goal-comment", Version: 1},
		IssueID:   "issue-1", AuthorKind: domain.IssueCommentAuthorAgent,
		AuthorAgentID: "agent-1", RunID: "first-goal-run", Body: "starting",
		Status: domain.IssueCommentWorking,
	}
	repository.comments = []domain.IssueComment{repository.comment}
	progress := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RequestID: "goal-progress", RunID: "run-1", Tool: "issue_comment_create",
		Payload: map[string]any{"body": "progress", "mentioned_agent_ids": []string{}},
	})
	if !progress.Result.OK || repository.comment.Status != domain.IssueCommentWorking ||
		repository.comment.Body != "progress" {
		t.Fatalf("progress execution=%+v comment=%+v", progress, repository.comment)
	}

	repository.scope.RunPurpose = domain.RunPurposeGoalFinalize
	finalized := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RequestID: "goal-final", RunID: "run-1", Tool: "issue_comment_create",
		Payload: map[string]any{"body": "final summary", "mentioned_agent_ids": []string{}},
	})
	if !finalized.Result.OK || repository.comment.Status != domain.IssueCommentPublished ||
		repository.comment.Body != "final summary" {
		t.Fatalf("final execution=%+v comment=%+v", finalized, repository.comment)
	}
}

func TestExecuteRejectsInvalidRunScopeAndSelfMention(t *testing.T) {
	tests := []struct {
		name       string
		computerID string
		request    nodeprotocol.NodeRequest
		configure  func(*fakeRepository)
		wantCode   string
	}{
		{name: "missing run", computerID: "computer-1", request: nodeprotocol.NodeRequest{Tool: "issue_get"}, wantCode: "missing_run"},
		{name: "unknown run", computerID: "computer-1", request: nodeprotocol.NodeRequest{RunID: "run-1", Tool: "issue_get"}, configure: func(repository *fakeRepository) { repository.scopeErr = errors.New("missing") }, wantCode: "run_not_found"},
		{name: "other computer", computerID: "computer-2", request: nodeprotocol.NodeRequest{RunID: "run-1", Tool: "issue_get"}, wantCode: "computer_mismatch"},
		{name: "no issue", computerID: "computer-1", request: nodeprotocol.NodeRequest{RunID: "run-1", Tool: "issue_get"}, configure: func(repository *fakeRepository) { repository.scope.IssueID = "" }, wantCode: "no_issue_context"},
		{name: "self mention", computerID: "computer-1", request: nodeprotocol.NodeRequest{RunID: "run-1", Tool: "issue_comment_create", Payload: map[string]any{"body": "delegate", "mentioned_agent_ids": []string{"agent-1"}}}, wantCode: "self_mention"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newToolRepository()
			if test.configure != nil {
				test.configure(repository)
			}
			result := New(repository).Execute(t.Context(), test.computerID, test.request).Result
			if result.OK || result.ErrorCode != test.wantCode {
				t.Fatalf("result=%+v want code=%s", result, test.wantCode)
			}
		})
	}
}

func TestExecuteDeduplicatesMutationsAndRejectsKeyReuse(t *testing.T) {
	repository := newToolRepository()
	service := New(repository)
	request := nodeprotocol.NodeRequest{
		RequestID: "request-1", IdempotencyKey: "idem-1",
		RunID: "run-1", Tool: "issue_comment_create",
		Payload: map[string]any{"body": "first", "mentioned_agent_ids": []string{"agent-other"}},
	}
	first := service.Execute(t.Context(), "computer-1", request)
	request.RequestID = "request-retry"
	replayed := service.Execute(t.Context(), "computer-1", request)
	if !first.Result.OK || !replayed.Result.OK || repository.createCalls != 1 {
		t.Fatalf("first=%+v replayed=%+v calls=%d", first, replayed, repository.createCalls)
	}
	if replayed.Result.RequestID != "request-retry" || replayed.ActivationIssueID != "" {
		t.Fatalf("replayed execution=%+v", replayed)
	}
	request.Payload["body"] = "different operation"
	conflict := service.Execute(t.Context(), "computer-1", request).Result
	if conflict.OK || conflict.ErrorCode != "idempotency_conflict" || repository.createCalls != 1 {
		t.Fatalf("conflict=%+v calls=%d", conflict, repository.createCalls)
	}
}

func TestExecuteUpdatesOnlyOwnedAgentComments(t *testing.T) {
	repository := newToolRepository()
	repository.comment = domain.IssueComment{
		Versioned:  domain.Versioned{ID: "comment-1", Version: 2},
		IssueID:    "issue-1",
		AuthorKind: domain.IssueCommentAuthorUser, AuthorAgentID: "agent-1", Body: "before",
	}
	service := New(repository)
	request := nodeprotocol.NodeRequest{
		RunID: "run-1", Tool: "issue_comment_update_own",
		Payload: map[string]any{"comment_id": "comment-1", "expected_version": float64(2), "body": " after "},
	}
	if result := service.Execute(t.Context(), "computer-1", request).Result; result.ErrorCode != "not_own_comment" {
		t.Fatalf("user-authored result=%+v", result)
	}
	repository.comment.AuthorKind = domain.IssueCommentAuthorAgent
	result := service.Execute(t.Context(), "computer-1", request).Result
	if !result.OK || repository.updatedFor != "comment-1" || repository.version != 2 || repository.comment.Body != "after" {
		t.Fatalf("result=%+v repository=%+v", result, repository)
	}
	request.Payload["expected_version"] = "two"
	if result := service.Execute(t.Context(), "computer-1", request).Result; result.ErrorCode != "invalid_version" {
		t.Fatalf("invalid version result=%+v", result)
	}
	request.Payload["expected_version"] = 2.5
	if result := service.Execute(t.Context(), "computer-1", request).Result; result.ErrorCode != "invalid_version" {
		t.Fatalf("fractional version result=%+v", result)
	}
}

func TestExecuteReturnsRedactedIssueAndAgentContext(t *testing.T) {
	repository := newToolRepository()
	now := time.Now().UTC()
	repository.comments = []domain.IssueComment{{
		Versioned:  domain.Versioned{ID: "comment-1", Version: 7, CreatedAt: now},
		AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-1",
		AuthorUserID: "private-user", RunID: "private-run", Body: "public body",
		Status: domain.IssueCommentPublished,
	}}
	repository.instances = []domain.IssueAgentInstance{{
		Versioned: domain.Versioned{ID: "instance-2"}, AgentID: "agent-2",
		IssueID: "issue-1",
		Status:  "running", PendingMentionCount: 3,
	}}
	repository.runs = []domain.Run{{Versioned: domain.Versioned{ID: "run-2"},
		IssueID: "issue-1", AgentID: "agent-2", IssueAgentInstanceID: "instance-2"}}
	repository.events = map[string][]domain.RunEvent{"run-2": {
		{Sequence: 1, Type: "provider.tool", Payload: map[string]any{"tool": "read", "status": "completed", "secret": "hidden"}, OccurredAt: now},
		{Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "hidden reasoning"}, OccurredAt: now},
		{Sequence: 3, Type: "provider.internal", Payload: map[string]any{"secret": "hidden"}, OccurredAt: now},
	}}
	service := New(repository)

	issueResult := service.Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{RunID: "run-1", Tool: "issue_get"}).Result
	comments, ok := issueResult.Data["comments"].([]map[string]any)
	if !issueResult.OK || !ok || len(comments) != 1 {
		t.Fatalf("issue result=%+v", issueResult)
	}
	if _, exposed := comments[0]["author_user_id"]; exposed {
		t.Fatalf("comment exposed author_user_id: %+v", comments[0])
	}
	if comments[0]["run_id"] != "private-run" || comments[0]["version"] != int64(7) ||
		comments[0]["status"] != domain.IssueCommentPublished {
		t.Fatalf("comment revision metadata missing: %+v", comments[0])
	}

	contextResult := service.Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RunID: "run-1", Tool: "issue_agent_context", Payload: map[string]any{"agent_id": "agent-2"},
	}).Result
	items, ok := contextResult.Data["context"].([]map[string]any)
	if !contextResult.OK || !ok || len(items) != 2 {
		t.Fatalf("context result=%+v", contextResult)
	}
	if _, exposed := items[0]["secret"]; exposed {
		t.Fatalf("tool event exposed payload: %+v", items[0])
	}
	if _, exposed := items[1]["text"]; exposed {
		t.Fatalf("output event exposed payload: %+v", items[1])
	}
}

func TestExecuteListsAllComputerAgentsIncludingNonParticipants(t *testing.T) {
	repository := newToolRepository()
	repository.agents = []domain.Agent{
		{Versioned: domain.Versioned{ID: "agent-1"}, Name: "Agent A", Status: domain.AgentActive},
		{Versioned: domain.Versioned{ID: "agent-2"}, Name: "Agent B", Status: domain.AgentActive},
	}
	repository.instances = []domain.IssueAgentInstance{{
		Versioned: domain.Versioned{ID: "instance-1"}, AgentID: "agent-1", Status: domain.AgentInstanceRunning,
	}}

	result := New(repository).Execute(t.Context(), "computer-1", nodeprotocol.NodeRequest{
		RunID: "run-1", Tool: "issue_agents_list",
	}).Result
	items, ok := result.Data["agents"].([]map[string]any)
	if !result.OK || !ok || len(items) != 2 {
		t.Fatalf("result=%+v", result)
	}
	if items[0]["agent_id"] != "agent-1" || items[0]["name"] != "Agent A" ||
		items[0]["is_self"] != true || items[0]["participating"] != true ||
		items[0]["instance_id"] != "instance-1" {
		t.Fatalf("self participant=%+v", items[0])
	}
	if items[1]["agent_id"] != "agent-2" || items[1]["name"] != "Agent B" ||
		items[1]["is_self"] != false || items[1]["participating"] != false {
		t.Fatalf("available non-participant=%+v", items[1])
	}
	if _, exposed := items[1]["instance_id"]; exposed {
		t.Fatalf("non-participant exposed an instance id: %+v", items[1])
	}
}

func newToolRepository() *fakeRepository {
	return &fakeRepository{
		scope: store.RunScope{
			UserID: "user-1", ComputerID: "computer-1", IssueID: "issue-1",
			AgentID: "agent-1", InstanceID: "instance-1",
		},
		issue: domain.Issue{
			Versioned: domain.Versioned{ID: "issue-1"}, Title: "Contract Issue",
			Description: "public description", Status: domain.IssueOpen,
		},
		events: make(map[string][]domain.RunEvent),
		runs:   []domain.Run{{Versioned: domain.Versioned{ID: "run-1"}, IssueAgentInstanceID: "instance-1"}},
		agents: []domain.Agent{{
			Versioned: domain.Versioned{ID: "agent-1"}, Name: "Agent A", Status: domain.AgentActive,
		}},
	}
}

var _ Repository = (*fakeRepository)(nil)
