package toolservice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

// Repository is the narrow control-plane contract required by Agent tools.
// The in-process desktop/browser dispatcher uses
// this service so authorization and response redaction cannot drift.
type Repository interface {
	GetRun(context.Context, string, string) (domain.Run, error)
	ExecuteWorkReadTool(context.Context, string, string, map[string]any) (map[string]any, error)
	GetRunScope(context.Context, string) (store.RunScope, error)
	GetIssue(context.Context, string, string) (domain.Issue, error)
	ListIssuePromptComments(context.Context, string, string) ([]domain.IssueComment, bool, error)
	GetIssueComment(context.Context, string, string) (domain.IssueComment, error)
	WriteRunIssueComment(context.Context, string, string, string, store.RunIssueCommentWrite) (store.RunIssueCommentResult, error)
	ListAgents(context.Context, string, string) ([]domain.Agent, error)
	ListIssueAgentInstances(context.Context, string, string) ([]domain.IssueAgentInstance, error)
	GetIssueAgentInstance(context.Context, string, string, string) (domain.IssueAgentInstance, error)
	GetIssueAgentInstanceByID(context.Context, string, string) (domain.IssueAgentInstance, error)
	ListRunsByInstancePage(context.Context, string, string, string, int) (domain.Page[domain.Run], error)
	ListRunEventPage(context.Context, string, string, int64, int, int) (domain.RunEventPage, error)
}

// Execution includes the protocol response and an optional issue wake-up.
// Persistence creates durable mentions; adapters use ActivationIssueID only to
// wake their dispatcher immediately after the transaction commits.
type Execution struct {
	Result            nodeprotocol.NodeRequestResult
	ActivationIssueID string
}

type Service struct {
	repository Repository
	mu         sync.Mutex
	requests   map[string]*requestExecution
}

type requestExecution struct {
	fingerprint [sha256.Size]byte
	done        chan struct{}
	execution   Execution
	completedAt time.Time
}

func New(repository Repository) *Service {
	return &Service{repository: repository, requests: make(map[string]*requestExecution)}
}

// Execute authorizes a tool from its persisted run scope. IDs supplied by the
// Agent payload are never used to choose the user, computer, issue or author.
func (s *Service) Execute(
	ctx context.Context, computerID string, request nodeprotocol.NodeRequest,
) Execution {
	if request.IdempotencyKey == "" || !isMutation(request.Tool) {
		return s.execute(ctx, computerID, request)
	}
	payload, err := json.Marshal(struct {
		RunID   string         `json:"run_id"`
		Tool    string         `json:"tool"`
		Payload map[string]any `json:"payload"`
	}{RunID: request.RunID, Tool: request.Tool, Payload: request.Payload})
	if err != nil {
		return s.execute(ctx, computerID, request)
	}
	key := computerID + "\x00" + request.RunID + "\x00" + request.IdempotencyKey
	fingerprint := sha256.Sum256(payload)
	now := time.Now().UTC()

	s.mu.Lock()
	for existingKey, existing := range s.requests {
		if !existing.completedAt.IsZero() && now.Sub(existing.completedAt) > 10*time.Minute {
			delete(s.requests, existingKey)
		}
	}
	if existing, ok := s.requests[key]; ok {
		if existing.fingerprint != fingerprint {
			s.mu.Unlock()
			return toolFailure(request.RequestID, "idempotency_conflict", "幂等键已用于其他工具请求")
		}
		done := existing.done
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return toolFailure(request.RequestID, "request_cancelled", "工具请求已取消")
		case <-done:
			execution := existing.execution
			execution.Result.RequestID = request.RequestID
			execution.ActivationIssueID = ""
			return execution
		}
	}
	entry := &requestExecution{fingerprint: fingerprint, done: make(chan struct{})}
	s.requests[key] = entry
	s.mu.Unlock()

	execution := s.execute(ctx, computerID, request)
	s.mu.Lock()
	entry.execution = execution
	if execution.Result.OK {
		entry.completedAt = time.Now().UTC()
	} else {
		delete(s.requests, key)
	}
	close(entry.done)
	s.mu.Unlock()
	return execution
}

func (s *Service) execute(
	ctx context.Context, computerID string, request nodeprotocol.NodeRequest,
) Execution {
	fail := func(code, message string) Execution {
		return toolFailure(request.RequestID, code, message)
	}
	if request.RunID == "" {
		return fail("missing_run", "工具调用缺少 run_id")
	}
	scope, err := s.repository.GetRunScope(ctx, request.RunID)
	if err != nil {
		return fail("run_not_found", "运行不存在")
	}
	if scope.ComputerID != "" && scope.ComputerID != computerID {
		return fail("computer_mismatch", "运行不属于当前设备")
	}
	if scope.AssistantSessionID != "" {
		run, err := s.repository.GetRun(ctx, scope.UserID, request.RunID)
		if err != nil || !run.ReadOnly || run.Purpose != "work_assistant" || domain.IsRunTerminal(run.Status) {
			return fail("read_only_tool", "只读助手运行作用域无效")
		}
		data, err := s.repository.ExecuteWorkReadTool(ctx, scope.UserID, request.Tool, request.Payload)
		if err != nil {
			return fail("work_query_failed", "工作历史查询失败或工具不被允许")
		}
		return Execution{Result: nodeprotocol.NodeRequestResult{RequestID: request.RequestID, OK: true, Data: data}}
	}
	if scope.IssueID == "" {
		return fail("no_issue_context", "运行没有关联任务")
	}
	success := func(data map[string]any) Execution {
		return Execution{Result: nodeprotocol.NodeRequestResult{
			RequestID: request.RequestID, OK: true, Data: data,
		}}
	}

	switch request.Tool {
	case "issue_get":
		issue, err := s.repository.GetIssue(ctx, scope.UserID, scope.IssueID)
		if err != nil {
			return fail("issue_not_found", "任务不存在")
		}
		var comments []domain.IssueComment
		var truncated bool
		commentID, ok := request.Payload["comment_id"].(string)
		if value := request.Payload["comment_id"]; value != nil && !ok {
			return fail("invalid_comment", "comment_id 必须是字符串")
		}
		if commentID != "" {
			var comment domain.IssueComment
			comment, err = s.repository.GetIssueComment(ctx, scope.UserID, commentID)
			if err == nil && comment.IssueID != scope.IssueID {
				return fail("comment_not_found", "评论不属于当前任务")
			}
			comments = []domain.IssueComment{comment}
		} else {
			comments, truncated, err = s.repository.ListIssuePromptComments(ctx, scope.UserID, scope.IssueID)
		}
		if err != nil {
			return fail("issue_read_failed", "读取评论失败")
		}
		items := make([]map[string]any, 0, len(comments))
		for _, comment := range comments {
			items = append(items, map[string]any{
				"id": comment.ID, "author_kind": comment.AuthorKind,
				"author_agent_id": comment.AuthorAgentID, "body": comment.Body,
				"version": comment.Version, "run_id": comment.RunID, "status": comment.Status,
				"created_at": comment.CreatedAt, "edited_at": comment.EditedAt,
				"work_items": comment.WorkItems,
			})
		}
		return success(map[string]any{
			"id": issue.ID, "title": issue.Title, "description": issue.Description,
			"status": issue.Status, "worktree_hint": issue.WorktreeHint, "comments": items, "truncated": truncated,
		})

	case "issue_comment_create":
		if scope.AgentID == "" {
			return fail("missing_agent", "运行未绑定 Agent")
		}
		body, _ := request.Payload["body"].(string)
		body = strings.TrimSpace(body)
		if body == "" {
			return fail("empty_body", "评论内容不能为空")
		}
		mentioned := stringSlice(request.Payload["mentioned_agent_ids"])
		for _, targetAgentID := range mentioned {
			if targetAgentID == scope.AgentID {
				return fail("self_mention", "Agent 不能直接 @ 自己")
			}
		}
		var workItems []nodeprotocol.WorkPlanItem
		if value, supplied := request.Payload["work_items"]; supplied {
			raw, err := json.Marshal(value)
			if err != nil {
				return fail("invalid_argument", "work_items 必须是有效的分工数组")
			}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&workItems); err != nil || len(workItems) == 0 || len(mentioned) > 0 {
				return fail("invalid_argument", "work_items 必须是非空分工数组，不能同时指定普通 @")
			}
			for _, item := range workItems {
				if item.AgentID == scope.AgentID {
					return fail("self_mention", "Agent 不能直接 @ 自己")
				}
			}
		}
		appendNew := false
		if value, supplied := request.Payload["append_new"]; supplied {
			var ok bool
			appendNew, ok = value.(bool)
			if !ok {
				return fail("invalid_argument", "append_new 必须是布尔值")
			}
		}
		// The store owns session-summary selection, live Run authorization,
		// revision, publication and every new delegation in one transaction.
		comment, err := s.repository.WriteRunIssueComment(ctx, scope.UserID, computerID, request.RunID, store.RunIssueCommentWrite{
			Body: body, MentionedAgentIDs: mentioned, WorkItems: workItems, AppendNew: appendNew,
			IdempotencyKey: commentRequestKey(request),
		})
		if err != nil {
			return commentWriteFailure(request.RequestID, "comment_failed", err)
		}
		execution := success(map[string]any{"comment_id": comment.CommentID, "version": comment.Version})
		if (len(mentioned) > 0 || len(workItems) > 0) && !comment.Replayed {
			execution.ActivationIssueID = scope.IssueID
		}
		return execution

	case "issue_comment_update_own":
		if scope.AgentID == "" {
			return fail("missing_agent", "运行未绑定 Agent")
		}
		commentID, _ := request.Payload["comment_id"].(string)
		if commentID == "" {
			return fail("missing_comment", "缺少 comment_id")
		}
		body, _ := request.Payload["body"].(string)
		body = strings.TrimSpace(body)
		if body == "" {
			return fail("empty_body", "评论内容不能为空")
		}
		expectedVersion, err := int64FromAny(request.Payload["expected_version"])
		if err != nil || expectedVersion < 1 {
			return fail("invalid_version", "expected_version 无效")
		}
		updated, err := s.repository.WriteRunIssueComment(ctx, scope.UserID, computerID, request.RunID, store.RunIssueCommentWrite{
			CommentID: commentID, ExpectedVersion: expectedVersion, Body: body,
			IdempotencyKey: commentRequestKey(request),
		})
		if err != nil {
			return commentWriteFailure(request.RequestID, "update_failed", err)
		}
		return success(map[string]any{
			"comment_id": updated.CommentID, "version": updated.Version, "edited_at": updated.EditedAt,
		})

	case "issue_agents_list":
		agents, err := s.repository.ListAgents(ctx, scope.UserID, scope.ComputerID)
		if err != nil {
			return fail("agents_failed", "读取可用 Agent 失败")
		}
		instances, err := s.repository.ListIssueAgentInstances(ctx, scope.UserID, scope.IssueID)
		if err != nil {
			return fail("instances_failed", "读取 Agent 实例失败")
		}
		instancesByAgent := make(map[string]domain.IssueAgentInstance, len(instances))
		for _, instance := range instances {
			instancesByAgent[instance.AgentID] = instance
		}
		items := make([]map[string]any, 0, len(agents))
		for _, agent := range agents {
			item := map[string]any{
				"agent_id": agent.ID, "name": agent.Name, "status": agent.Status,
				"is_self": agent.ID == scope.AgentID, "participating": false,
			}
			if instance, ok := instancesByAgent[agent.ID]; ok {
				item["participating"] = true
				item["instance_id"] = instance.ID
				item["instance_status"] = instance.Status
				item["last_activity_at"] = instance.LastActivityAt
				item["pending_mentions"] = instance.PendingMentionCount
			}
			items = append(items, item)
		}
		return success(map[string]any{"agents": items})

	case "issue_agent_status":
		instance, execution := s.agentInstance(ctx, request, scope, fail)
		if !execution.Result.OK {
			return execution
		}
		return success(instanceSummary(instance))

	case "issue_agent_context":
		return s.agentContext(ctx, request, scope)

	default:
		return fail("unknown_tool", "未知工具: "+request.Tool)
	}
}

func isMutation(tool string) bool {
	return tool == "issue_comment_create" || tool == "issue_comment_update_own"
}

func commentRequestKey(request nodeprotocol.NodeRequest) string {
	if request.IdempotencyKey != "" {
		return "key:" + request.IdempotencyKey
	}
	if request.RequestID != "" {
		return "request:" + request.RequestID
	}
	return ""
}

func commentWriteFailure(requestID, fallback string, err error) Execution {
	var businessError *domain.Error
	if errors.As(err, &businessError) {
		return toolFailure(requestID, businessError.Code, businessError.Message)
	}
	return toolFailure(requestID, fallback, "评论写入失败")
}

func toolFailure(requestID, code, message string) Execution {
	return Execution{Result: nodeprotocol.NodeRequestResult{
		RequestID: requestID, ErrorCode: code, Message: message,
	}}
}

func (s *Service) agentInstance(
	ctx context.Context,
	request nodeprotocol.NodeRequest,
	scope store.RunScope,
	fail func(string, string) Execution,
) (domain.IssueAgentInstance, Execution) {
	targetAgentID, _ := request.Payload["agent_id"].(string)
	if targetAgentID == "" {
		return domain.IssueAgentInstance{}, fail("missing_agent", "缺少 agent_id")
	}
	instance, err := s.repository.GetIssueAgentInstance(ctx, scope.UserID, scope.IssueID, targetAgentID)
	if err != nil {
		return domain.IssueAgentInstance{}, fail("agent_not_found", "Agent 实例不存在")
	}
	return instance, Execution{Result: nodeprotocol.NodeRequestResult{OK: true}}
}

func instanceSummary(instance domain.IssueAgentInstance) map[string]any {
	return map[string]any{
		"id": instance.ID, "agent_id": instance.AgentID, "status": instance.Status,
		"last_activity_at": instance.LastActivityAt, "pending_mentions": instance.PendingMentionCount,
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func int64FromAny(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		converted := int64(typed)
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || float64(converted) != typed {
			return 0, errors.New("not an integer")
		}
		return converted, nil
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, errors.New("not an integer")
	}
}
