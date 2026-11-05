package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// ToolCaller 抽象 node→server 的工具 RPC 通道。
type ToolCaller interface {
	SendToolRequest(context.Context, nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error)
}

// ToolBroker 为 Agent 提供 Issue 协作工具：
//   - issue_get：读取 Issue 上下文（标题/描述/评论）
//   - issue_comment_create：发布评论（可携带 @ 名单）
//   - issue_comment_update_own：修正自己发布的评论
//   - issue_agents_list：列出当前计算机中可委派的 Agent 及其参与状态
//   - issue_agent_status：查看某 Agent 实例执行状态
//   - issue_agent_context：查看某 Agent 实例的脱敏执行上下文
//
// 工具作用域（user/computer/issue/instance/run）由 server 依据当前 Run
// 自动绑定；node 侧透传 Run/Issue/Instance 标识。每次调用带幂等键防重试。
type ToolBroker struct {
	caller        ToolCaller
	runID         string
	issueID       string
	instanceID    string
	agentID       string
	workAssistant bool
}

// NewToolBroker 创建绑定当前 Run 的工具代理。
func NewToolBroker(caller ToolCaller, runID, issueID, instanceID, agentID string) *ToolBroker {
	return &ToolBroker{
		caller: caller, runID: runID, issueID: issueID,
		instanceID: instanceID, agentID: agentID,
	}
}

// IssueGet 读取 Issue 上下文。
func (b *ToolBroker) IssueGet(ctx context.Context) (map[string]any, error) {
	return b.call(ctx, "issue_get", nil)
}

// IssueGetComment retrieves one exact current-Issue comment after the bounded
// context window reports clipping. The server still authorizes its Issue.
func (b *ToolBroker) IssueGetComment(ctx context.Context, commentID string) (map[string]any, error) {
	return b.call(ctx, "issue_get", map[string]any{"comment_id": commentID})
}

// IssueCommentCreate 发布评论。mentionedAgentIDs 为结构化 @（Agent 委派）；
// appendNew 仅在用户明确要求另发一条时为 true，否则保留服务端总结占位评论复用。
func (b *ToolBroker) IssueCommentCreate(ctx context.Context, body string, mentionedAgentIDs []string, appendNew bool, workItems ...[]nodeprotocol.WorkPlanItem) (map[string]any, error) {
	payload := map[string]any{
		"body": body, "mentioned_agent_ids": mentionedAgentIDs, "append_new": appendNew,
	}
	if len(workItems) > 0 {
		payload["work_items"] = workItems[0]
	}
	return b.call(ctx, "issue_comment_create", payload)
}

// IssueCommentUpdateOwn 修正自己发布的评论。
func (b *ToolBroker) IssueCommentUpdateOwn(ctx context.Context, commentID string, expectedVersion int64, body string) (map[string]any, error) {
	return b.call(ctx, "issue_comment_update_own", map[string]any{
		"comment_id": commentID, "expected_version": expectedVersion, "body": body,
	})
}

// IssueAgentsList 列出当前计算机中的 Agent，并标注是否已参与当前任务。
func (b *ToolBroker) IssueAgentsList(ctx context.Context) (map[string]any, error) {
	return b.call(ctx, "issue_agents_list", nil)
}

// IssueAgentStatus 查看某 Agent 实例状态。
func (b *ToolBroker) IssueAgentStatus(ctx context.Context, agentID string) (map[string]any, error) {
	return b.call(ctx, "issue_agent_status", map[string]any{"agent_id": agentID})
}

// IssueAgentContext 查看某 Agent 实例的脱敏上下文（公共评论、工具轨迹等）。
func (b *ToolBroker) IssueAgentContext(ctx context.Context, agentID string, cursor string) (map[string]any, error) {
	return b.call(ctx, "issue_agent_context", map[string]any{"agent_id": agentID, "cursor": cursor})
}

// fileToolRequestKey carries only the IPC filename, never a path or payload.
// The same run/tool/file is one operation even if delivery or response writing
// fails. Hashing that scope avoids retaining an ever-growing replay cache.
type fileToolRequestKey struct{}

func (b *ToolBroker) call(ctx context.Context, tool string, payload map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("tool %s: %w", tool, err)
	}
	if b.caller == nil {
		return nil, fmt.Errorf("tool %s: 没有可用的控制面通道", tool)
	}
	requestID, err := newToolRequestID()
	if err != nil {
		return nil, fmt.Errorf("tool %s: 无法建立安全幂等键", tool)
	}
	idempotencyKey := requestID
	if filename, ok := ctx.Value(fileToolRequestKey{}).(string); ok && filename != "" {
		scope := fmt.Sprintf("run:%d:%s:tool:%d:%s:file:%d:%s", len(b.runID), b.runID, len(tool), tool, len(filename), filename)
		digest := sha256.Sum256([]byte(scope))
		idempotencyKey = "tool_file_" + hex.EncodeToString(digest[:])
	}
	deadline := time.Now().UTC().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	request := nodeprotocol.NodeRequest{
		RequestID:      requestID,
		Tool:           tool,
		Payload:        payload,
		RunID:          b.runID,
		IssueID:        b.issueID,
		AgentID:        b.agentID,
		InstanceID:     b.instanceID,
		DeadlineUnix:   deadline.Unix(),
		IdempotencyKey: idempotencyKey,
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("tool %s: %w", tool, err)
	}
	result, err := b.caller.SendToolRequest(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("tool %s: %w", tool, err)
	}
	if !result.OK {
		message := result.Message
		if message == "" {
			message = result.ErrorCode
		}
		return nil, fmt.Errorf("tool %s: %s", tool, message)
	}
	return result.Data, nil
}

func newToolRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "tool_" + hex.EncodeToString(raw), nil
}

// MarshalJSON 便于调试输出。
func (b *ToolBroker) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"run_id": b.runID, "issue_id": b.issueID,
		"instance_id": b.instanceID, "agent_id": b.agentID,
	})
}
