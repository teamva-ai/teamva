package runtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// runMCPHost serves the Issue collaboration tools for one Run over streamable
// HTTP. It lives in the runtime process and calls the same ToolBroker the file
// bridge used, so authorization does not depend on the transport.
type runMCPHost struct {
	listener net.Listener
	server   *http.Server
	url      string
	token    string
}

func newRunMCPHost(ctx context.Context, broker *ToolBroker, readOnly bool) (*runMCPHost, error) {
	if broker == nil {
		return nil, errors.New("run MCP requires a tool broker")
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	server := mcp.NewServer(&mcp.Implementation{Name: "teamva-issue", Version: "1"}, nil)
	addIssueMCPTools(server, broker, readOnly)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	httpServer := &http.Server{
		Handler:           requireBearer(token, handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	host := &runMCPHost{
		listener: listener,
		server:   httpServer,
		url:      "http://" + listener.Addr().String() + "/mcp",
		token:    token,
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The run observes tool failures through the transport; do not log
			// the token or request payloads here.
		}
	}()
	return host, nil
}

func (h *runMCPHost) URL() string   { return h.url }
func (h *runMCPHost) Token() string { return h.token }

func (h *runMCPHost) Close() error {
	if h == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(h.server.Shutdown(ctx), h.listener.Close())
}

func requireBearer(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := []byte(request.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func addIssueMCPTools(server *mcp.Server, broker *ToolBroker, readOnly bool) {
	noInput := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	add := func(name, description string, schema map[string]any, handler func(context.Context, map[string]any) (map[string]any, error)) {
		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema},
			func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args := map[string]any{}
				if len(request.Params.Arguments) > 0 {
					if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
						return mcpError("工具参数不是有效 JSON"), nil
					}
				}
				data, err := handler(ctx, args)
				if err != nil {
					return mcpError(err.Error()), nil
				}
				encoded, err := json.Marshal(data)
				if err != nil {
					return mcpError("工具结果无法编码"), nil
				}
				return &mcp.CallToolResult{
					Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
					StructuredContent: data,
				}, nil
			})
	}

	add("issue_get", "Read the current Issue context, or one exact comment when comment_id is supplied.", map[string]any{
		"type": "object", "properties": map[string]any{
			"comment_id": map[string]any{"type": "string"},
		}, "additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (map[string]any, error) {
		if commentID, _ := args["comment_id"].(string); commentID != "" {
			return broker.IssueGetComment(ctx, commentID)
		}
		return broker.IssueGet(ctx)
	})

	add("issue_agents_list", "List agents on this computer and whether they participate in the current Issue.", noInput,
		func(ctx context.Context, _ map[string]any) (map[string]any, error) {
			return broker.IssueAgentsList(ctx)
		})

	add("issue_agent_status", "Read one participating agent's instance status.", map[string]any{
		"type": "object", "properties": map[string]any{
			"agent_id": map[string]any{"type": "string"},
		}, "required": []string{"agent_id"}, "additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (map[string]any, error) {
		agentID, err := mcpRequiredString(args, "agent_id")
		if err != nil {
			return nil, err
		}
		return broker.IssueAgentStatus(ctx, agentID)
	})

	add("issue_agent_context", "Read one other agent's redacted execution context.", map[string]any{
		"type": "object", "properties": map[string]any{
			"agent_id": map[string]any{"type": "string"},
			"cursor":   map[string]any{"type": "string"},
		}, "required": []string{"agent_id"}, "additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (map[string]any, error) {
		agentID, err := mcpRequiredString(args, "agent_id")
		if err != nil {
			return nil, err
		}
		cursor, _ := args["cursor"].(string)
		return broker.IssueAgentContext(ctx, agentID, cursor)
	})

	if readOnly {
		return
	}
	add("issue_comment_create", "Publish this run's public result to the Issue, optionally mentioning other agents or assigning work items.", map[string]any{
		"type": "object", "properties": map[string]any{
			"body":                map[string]any{"type": "string"},
			"mentioned_agent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"append_new":          map[string]any{"type": "boolean"},
			"work_items":          map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		}, "required": []string{"body"}, "additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (map[string]any, error) {
		body, err := mcpRequiredString(args, "body")
		if err != nil {
			return nil, err
		}
		mentioned, err := mcpStringSlice(args, "mentioned_agent_ids")
		if err != nil {
			return nil, err
		}
		appendNew, _ := args["append_new"].(bool)
		var workItems []nodeprotocol.WorkPlanItem
		if raw, exists := args["work_items"]; exists && raw != nil {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, errors.New("work_items 参数无效")
			}
			if err := json.Unmarshal(encoded, &workItems); err != nil {
				return nil, errors.New("work_items 参数无效")
			}
		}
		if len(workItems) > 0 {
			return broker.IssueCommentCreate(ctx, body, mentioned, appendNew, workItems)
		}
		return broker.IssueCommentCreate(ctx, body, mentioned, appendNew)
	})

	add("issue_comment_update_own", "Revise a comment this agent published; expected_version must be current.", map[string]any{
		"type": "object", "properties": map[string]any{
			"comment_id":       map[string]any{"type": "string"},
			"expected_version": map[string]any{"type": "integer"},
			"body":             map[string]any{"type": "string"},
		}, "required": []string{"comment_id", "expected_version", "body"}, "additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (map[string]any, error) {
		commentID, err := mcpRequiredString(args, "comment_id")
		if err != nil {
			return nil, err
		}
		body, err := mcpRequiredString(args, "body")
		if err != nil {
			return nil, err
		}
		version, ok := args["expected_version"].(float64)
		if !ok || version <= 0 || version != float64(int64(version)) {
			return nil, errors.New("expected_version 必须是正整数")
		}
		return broker.IssueCommentUpdateOwn(ctx, commentID, int64(version), body)
	})
}

func mcpError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

func mcpRequiredString(args map[string]any, key string) (string, error) {
	value, _ := args[key].(string)
	if value == "" {
		return "", errors.New(key + " 必须是非空字符串")
	}
	return value, nil
}

func mcpStringSlice(args map[string]any, key string) ([]string, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, errors.New(key + " 必须是字符串数组")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New(key + " 必须是字符串数组")
		}
		result = append(result, text)
	}
	return result, nil
}

// issueMCPToolsMarkdown keeps the collaboration contract that used to live in
// the file-bridge prompt, without teaching any file paths.
func issueMCPToolsMarkdown(agentID string, readOnly bool) string {
	identity := "当前 Agent ID 未提供；如需回报，先调用 issue_agents_list 并使用 is_self=true 的 agent_id。"
	if agentID != "" {
		identity = "当前 Agent ID：" + agentID + "。"
	}
	base := `# Issue 协作工具（MCP）

使用 teamva-issue MCP server；工具参数就是 schema 里的 JSON。不要写请求文件。

- issue_get：读取任务上下文；传 {"comment_id":"..."} 读取单条评论全文。
- issue_agents_list / issue_agent_status / issue_agent_context：查找/查看其他 Agent。

` + identity + ` 本轮是只读运行，不能发布或修改评论。
`
	if readOnly {
		return base
	}
	return base + `- issue_comment_create：发布本轮公共结果。{"body":"...","mentioned_agent_ids":[],"append_new":false}；多份工作改传 work_items。
- issue_comment_update_own：修正自己发布的评论，传 comment_id、expected_version 和 body。

不能 @ 自己。结构化 @ 是异步委派：发布结果和委派说明后结束本轮，不等待对方；需要结果后继续时，在委派正文要求对方完成后 @回本 Agent ID。正文 @名字不算激活。
work_items 最多 32 项，key 唯一，after 只引用本批次 key 且不得成环；同一 Agent 可重复。工作定义提交后不可修改。
每个 Agent 会话只维护一条总结评论，系统会复用并原地更新；只有用户明确要求“再发一条”时才传 append_new=true。
不要修改其他 Agent 的评论。系统会自动展示已发布评论，最终回复只需给出简洁结论。
`
}
