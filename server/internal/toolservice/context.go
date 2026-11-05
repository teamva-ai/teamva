package toolservice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

const maxContextEvents = 128

// One page reads at most one Run and one bounded event page. The cursor pins
// the conversation: a newer @ must not silently move an in-flight traversal to
// another instance of the same Agent. Runs are newest-first, events ascending.
type contextCursor struct {
	Version    int    `json:"v"`
	InstanceID string `json:"instance_id"`
	RunID      string `json:"run_id,omitempty"`
	After      int64  `json:"after,omitempty"`
	OlderRuns  string `json:"older_runs,omitempty"`
}

func (s *Service) agentContext(ctx context.Context, request nodeprotocol.NodeRequest, scope store.RunScope) Execution {
	fail := func(code, message string) Execution { return toolFailure(request.RequestID, code, message) }
	targetAgentID, _ := request.Payload["agent_id"].(string)
	if targetAgentID == "" {
		return fail("missing_agent", "缺少 agent_id")
	}
	value := request.Payload["cursor"]
	if value != nil && !okString(value) {
		return fail("invalid_cursor", "无效的上下文游标")
	}
	cursorText, _ := value.(string)
	cursor := contextCursor{Version: 1}
	var instance domain.IssueAgentInstance
	var err error
	if cursorText == "" {
		instance, err = s.repository.GetIssueAgentInstance(ctx, scope.UserID, scope.IssueID, targetAgentID)
		cursor.InstanceID = instance.ID
	} else {
		if len(cursorText) > 4096 {
			return fail("invalid_cursor", "无效的上下文游标")
		}
		cursor = contextCursor{} // The encoded version is required, not defaulted.
		payload, decodeErr := base64.RawURLEncoding.DecodeString(cursorText)
		if decodeErr != nil || json.Unmarshal(payload, &cursor) != nil || cursor.Version != 1 ||
			cursor.InstanceID == "" || len(cursor.InstanceID) > 256 || len(cursor.RunID) > 256 ||
			cursor.After < 0 || (cursor.RunID == "" && cursor.After != 0) {
			return fail("invalid_cursor", "无效的上下文游标")
		}
		instance, err = s.repository.GetIssueAgentInstanceByID(ctx, scope.UserID, cursor.InstanceID)
	}
	if err != nil || instance.IssueID != scope.IssueID || instance.AgentID != targetAgentID {
		return fail("agent_not_found", "Agent 实例不存在或游标不属于当前任务")
	}
	items := make([]map[string]any, 0, maxContextEvents)
	result := func(next *contextCursor) Execution {
		nextValue := ""
		if next != nil {
			payload, _ := json.Marshal(next)
			nextValue = base64.RawURLEncoding.EncodeToString(payload)
		}
		return Execution{Result: nodeprotocol.NodeRequestResult{RequestID: request.RequestID, OK: true, Data: map[string]any{
			"agent_id": instance.AgentID, "instance_id": instance.ID, "status": instance.Status,
			"context": items, "truncated": next != nil, "next_cursor": nextValue,
		}}}
	}
	if cursor.RunID == "" {
		page, listErr := s.repository.ListRunsByInstancePage(ctx, scope.UserID, instance.ID, cursor.OlderRuns, 1)
		if listErr != nil {
			return fail("runs_failed", "读取运行记录失败或游标无效")
		}
		if len(page.Items) == 0 {
			return result(nil)
		}
		run := page.Items[0]
		if run.IssueID != scope.IssueID || run.AgentID != targetAgentID || run.IssueAgentInstanceID != instance.ID {
			return fail("runs_failed", "运行不属于当前任务会话")
		}
		cursor.RunID, cursor.OlderRuns = page.Items[0].ID, page.NextCursor
	} else {
		run, runErr := s.repository.GetRun(ctx, scope.UserID, cursor.RunID)
		if runErr != nil || run.IssueAgentInstanceID != instance.ID || run.IssueID != scope.IssueID || run.AgentID != targetAgentID {
			return fail("invalid_cursor", "游标运行不属于当前会话")
		}
	}
	page, err := s.repository.ListRunEventPage(ctx, scope.UserID, cursor.RunID, cursor.After, maxContextEvents, store.MaxRunEventPagePayloadBytes)
	if err != nil {
		return fail("events_failed", "读取运行事件失败")
	}
	for _, event := range page.Items {
		item := map[string]any{
			"run_id": cursor.RunID, "seq": event.Sequence, "type": event.Type, "occurred_at": event.OccurredAt,
		}
		switch event.Type {
		case "provider.tool":
			// Never return arbitrary values/maps from a provider-controlled payload.
			item["tool"] = contextLabel(event.Payload["tool"])
			item["status"] = contextLabel(event.Payload["status"])
		case "output.delta", "run.completed", "run.finished", "run.error":
		default:
			continue
		}
		items = append(items, item)
	}
	if page.Truncated && len(page.Items) > 0 {
		cursor.After = page.Items[len(page.Items)-1].Sequence
		return result(&cursor)
	}
	if cursor.OlderRuns != "" {
		cursor.RunID, cursor.After = "", 0
		return result(&cursor)
	}
	return result(nil)
}

func okString(value any) bool {
	_, ok := value.(string)
	return ok
}

func contextLabel(value any) string {
	text, _ := value.(string)
	text = strings.TrimSpace(text)
	if len(text) <= 128 {
		return text
	}
	text = text[:128]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
