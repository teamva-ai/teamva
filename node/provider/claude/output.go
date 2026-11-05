package claude

import (
	"fmt"
	"sort"
	"strings"

	"github.com/teamva-ai/teamva/node/provider"
)

// Only standard semantic events leave this adapter. Native frames, auth
// metadata, user/tool results, hidden thinking and compaction summaries cannot
// accidentally become the assistant's public answer.
type outputProjector struct{}

func (outputProjector) ProjectOutput(kind string, payload map[string]any) (provider.OutputUpdate, bool) {
	if kind == "output.delta" {
		return provider.OutputUpdate{ID: stringValue(payload["message_id"]), Text: stringValue(payload["text"])}, true
	}
	if kind == "output.snapshot" {
		return provider.OutputUpdate{ID: stringValue(payload["id"]), Text: stringValue(payload["text"]), Replace: true, Final: payload["final"] == true}, true
	}
	return provider.OutputUpdate{}, false
}
func stringValue(value any) string { result, _ := value.(string); return result }
func resultStatus(message map[string]any) string {
	if reason := stringValue(message["terminal_reason"]); reason == "aborted_streaming" || reason == "aborted_tools" {
		return "cancelled"
	}
	if message["is_error"] == true || message["subtype"] != "success" {
		return "failed"
	}
	return "succeeded"
}
func errorMessage(message map[string]any) string {
	parts := []string{}
	if result := stringValue(message["result"]); result != "" {
		parts = append(parts, result)
	}
	if errors, ok := message["errors"].([]any); ok {
		for _, value := range errors {
			if text := stringValue(value); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "Claude Code "+stringValue(message["subtype"]))
	}
	return strings.Join(parts, "; ")
}

type messageBlocks struct {
	text      map[int]string
	thinking  map[int]string
	finalized map[int]bool
	fallback  int
}
type streamProjection struct {
	emit             func(string, map[string]any) error
	activeID, lastID string
	messages         map[string]*messageBlocks
	compactionID     string
	compactionCount  int
}

func newStreamProjection(emit func(string, map[string]any) error) *streamProjection {
	return &streamProjection{emit: emit, messages: map[string]*messageBlocks{}}
}
func (p *streamProjection) blocks(id string) *messageBlocks {
	if p.messages[id] == nil {
		p.messages[id] = &messageBlocks{text: map[int]string{}, thinking: map[int]string{}, finalized: map[int]bool{}}
	}
	return p.messages[id]
}
func joined(blocks map[int]string) string {
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	var text strings.Builder
	for _, index := range indices {
		text.WriteString(blocks[index])
	}
	return text.String()
}
func indexValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
func (p *streamProjection) message(message map[string]any) error {
	if message["parent_tool_use_id"] != nil {
		return nil
	}
	switch message["type"] {
	case "stream_event":
		event, _ := message["event"].(map[string]any)
		switch event["type"] {
		case "message_start":
			native, _ := event["message"].(map[string]any)
			p.activeID = stringValue(native["id"])
		case "content_block_start":
			if p.activeID == "" {
				return nil
			}
			block, _ := event["content_block"].(map[string]any)
			index := indexValue(event["index"])
			b := p.blocks(p.activeID)
			if block["type"] == "text" {
				b.text[index] = stringValue(block["text"])
				p.lastID = p.activeID
				if b.text[index] != "" {
					return p.emit("output.delta", map[string]any{"message_id": p.activeID, "text": b.text[index]})
				}
			}
			if block["type"] == "thinking" {
				b.thinking[index] = stringValue(block["thinking"])
			}
		case "content_block_delta":
			if p.activeID == "" {
				return nil
			}
			delta, _ := event["delta"].(map[string]any)
			index := indexValue(event["index"])
			b := p.blocks(p.activeID)
			if delta["type"] == "text_delta" {
				text := stringValue(delta["text"])
				b.text[index] += text
				p.lastID = p.activeID
				return p.emit("output.delta", map[string]any{"message_id": p.activeID, "text": text})
			}
			if delta["type"] == "thinking_delta" {
				b.thinking[index] += stringValue(delta["thinking"])
				return p.emit("reasoning.snapshot", map[string]any{"id": fmt.Sprintf("%s:thinking:%d", p.activeID, index), "text": b.thinking[index], "status": "running"})
			}
		case "content_block_stop":
			if p.activeID == "" {
				return nil
			}
			index := indexValue(event["index"])
			b := p.blocks(p.activeID)
			if text, ok := b.thinking[index]; ok {
				return p.emit("reasoning.snapshot", map[string]any{"id": fmt.Sprintf("%s:thinking:%d", p.activeID, index), "text": text, "status": "completed"})
			}
		}
	case "assistant":
		native, _ := message["message"].(map[string]any)
		id := stringValue(native["id"])
		if id == "" {
			id = stringValue(message["uuid"])
		}
		if id == "" {
			return nil
		}
		if code := stringValue(message["error"]); code != "" || message["is_api_error_message"] == true {
			payload := map[string]any{"kind": "error", "code": code, "message": assistantText(native), "status": "failed", "willRetry": false}
			if value, ok := message["api_error_status"]; ok {
				payload["status_code"] = value
			}
			return p.emit("provider.event", payload)
		}
		blocks, _ := native["content"].([]any)
		b := p.blocks(id)
		for _, value := range blocks {
			block, _ := value.(map[string]any)
			if block["type"] != "text" {
				continue
			}
			text := stringValue(block["text"])
			matched := -1
			for index, streamed := range b.text {
				if !b.finalized[index] && streamed == text && (matched < 0 || index < matched) {
					matched = index
				}
			}
			if matched < 0 {
				matched = b.fallback
				for {
					if _, exists := b.text[matched]; !exists {
						break
					}
					matched++
				}
				b.text[matched] = text
			}
			b.finalized[matched] = true
			b.fallback = matched + 1
			p.lastID = id
		}
		if len(b.text) > 0 {
			return p.emit("output.snapshot", map[string]any{"id": id, "text": joined(b.text)})
		}
	case "system":
		switch message["subtype"] {
		case "api_retry":
			payload := map[string]any{"kind": "error", "status": "retrying", "willRetry": true, "native_type": "api_retry"}
			for _, key := range []string{"attempt", "max_retries", "retry_delay_ms", "error_status", "error"} {
				if value, ok := message[key]; ok {
					payload[key] = value
				}
			}
			if code := stringValue(message["error"]); code != "" {
				payload["message"] = code
				payload["code"] = code
			}
			if status, ok := message["error_status"]; ok {
				payload["status_code"] = status
			}
			return p.emit("provider.event", payload)
		case "status":
			if message["status"] == "compacting" {
				if p.compactionID == "" {
					p.compactionCount++
					p.compactionID = fmt.Sprintf("claude-compact-%d", p.compactionCount)
				}
				return p.emit("context.compaction", map[string]any{"id": p.compactionID, "status": "running"})
			}
			if p.compactionID != "" {
				// An ordinary null/requesting status is NOT proof of success.
				result := stringValue(message["compact_result"])
				if result == "failed" || result == "success" {
					status := "failed"
					if result == "success" {
						status = "completed"
					}
					payload := map[string]any{"id": p.compactionID, "status": status}
					if detail := stringValue(message["compact_error"]); detail != "" {
						payload["reason"] = detail
					}
					p.compactionID = ""
					return p.emit("context.compaction", payload)
				}
			}
		case "compact_boundary":
			if p.compactionID == "" {
				p.compactionCount++
				p.compactionID = fmt.Sprintf("claude-compact-%d", p.compactionCount)
			}
			payload := map[string]any{"id": p.compactionID, "status": "completed"}
			metadata, _ := message["compact_metadata"].(map[string]any)
			for _, key := range []string{"trigger", "pre_tokens", "post_tokens", "duration_ms"} {
				if value, ok := metadata[key]; ok {
					payload[key] = value
				}
			}
			p.compactionID = ""
			return p.emit("context.compaction", payload)
		}
	}
	return nil
}
func assistantText(message map[string]any) string {
	var result strings.Builder
	blocks, _ := message["content"].([]any)
	for _, value := range blocks {
		block, _ := value.(map[string]any)
		if block["type"] == "text" {
			result.WriteString(stringValue(block["text"]))
		}
	}
	return result.String()
}
func (p *streamProjection) result(message map[string]any, status string) error {
	if status == "succeeded" && message["local_command"] != "compact" {
		if result := stringValue(message["result"]); result != "" {
			id := p.lastID
			if id == "" {
				id = "claude-result"
			}
			if err := p.emit("output.snapshot", map[string]any{"id": id, "text": result, "final": true}); err != nil {
				return err
			}
		}
	}
	// Preserve native, known usage fields rather than inventing zeros for an
	// unreported accounting surface. Runtime owns cross-turn aggregation.
	if usage, ok := message["usage"].(map[string]any); ok {
		payload := map[string]any{"accounting_mode": "delta"}
		total := float64(0)
		for native, target := range map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens", "cache_read_input_tokens": "cache_read_tokens", "cache_creation_input_tokens": "cache_write_tokens"} {
			if value, ok := usage[native]; ok {
				payload[target] = value
				if number, ok := value.(float64); ok && number >= 0 {
					total += number
				}
			}
		}
		// total_cost_usd/modelUsage are cumulative, including resumed history;
		// never count them as this Run's delta or present a zero error as proof.
		if total > 0 {
			payload["total_tokens"] = total
			return p.emit("usage.updated", payload)
		}
	}
	return nil
}
