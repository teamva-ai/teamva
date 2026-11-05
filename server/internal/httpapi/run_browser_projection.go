package httpapi

import (
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Browser projections never replace the persisted execution input. In
// particular, the Issue context belongs to the model, not to a user bubble.
// Keeping this at the HTTP boundary also covers mutations and idempotent replies.
func browserResponseData(data any) any {
	switch item := data.(type) {
	case domain.Run:
		return browserRun(item)
	case domain.Page[domain.Run]:
		items := make([]domain.Run, len(item.Items))
		for index, run := range item.Items {
			items[index] = browserRun(run)
		}
		item.Items = items
		return item
	case domain.RunEventPage:
		items := make([]domain.RunEvent, len(item.Items))
		for index, event := range item.Items {
			items[index] = browserRunEvent(event)
		}
		item.Items = items
		return item
	default:
		return data
	}
}

func browserRun(item domain.Run) domain.Run {
	input := make([]domain.ContentBlock, 0, len(item.Input))
	structured := false
	for _, block := range item.Input {
		structured = structured || block.Internal
	}
	for _, block := range item.Input {
		if block.Internal {
			continue
		}
		if !structured && item.IssueID != "" && block.Type == "text" {
			// Existing Runs may contain a single assembled prompt. Only its
			// explicit current instruction is public; never guess a user message
			// from comment history or fall back to the full context.
			block.Text = legacyIssueUserInput(block.Text)
			if block.Text == "" {
				continue
			}
		}
		block.DataURL = ""
		input = append(input, block)
	}
	item.Input = input
	return item
}

func legacyIssueUserInput(value string) string {
	if !strings.HasPrefix(strings.TrimSpace(value), "# 任务（Issue）上下文") {
		return value
	}
	// Mention activations have no separate user message. The actual triggering
	// comment is already visible in the Issue, including when its body contains
	// headings that resemble a follow-up instruction.
	if strings.Contains(value, "\n\n## 触发本次委派的评论\n") {
		return ""
	}
	const marker = "\n## 本次指令\n"
	start := strings.LastIndex(value, marker)
	if start < 0 {
		return ""
	}
	value = value[start+len(marker):]
	for _, suffix := range []string{
		"\n\n请阅读上下文后开始工作", "\n\n## 当前工作回复", "\n\n## 持续目标模式",
	} {
		if end := strings.Index(value, suffix); end >= 0 {
			value = value[:end]
		}
	}
	return strings.TrimSpace(value)
}

func browserRunEvent(event domain.RunEvent) domain.RunEvent {
	payload := event.Payload
	internal := event.Type == "user.steer" && payload["internal"] == true
	// Native input echoes are not user messages: the public initial input and
	// user.steer are authoritative. Do not recurse through tool parameters or
	// assistant output, which may legitimately discuss user-shaped objects.
	if event.Type == "provider.item" || event.Type == "provider.event" {
		item, _ := payload["item"].(map[string]any)
		internal = internal || payload["role"] == "user" || item["role"] == "user" || item["type"] == "userMessage"
	}
	if internal {
		// Retain identity, type and sequence so pagination and SSE replay advance
		// across private input events, even a page containing only such events.
		event.Payload = map[string]any{"internal": true}
		if owner, ok := payload["provider_id"].(string); ok && owner != "" {
			event.Payload["provider_id"] = owner
		}
	}
	return event
}
