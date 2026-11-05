package opencode

import "github.com/teamva-ai/teamva/node/provider"

// outputProjector turns the adapter's normalized semantic events into assistant
// text updates. Reasoning, tool and lifecycle events are never assistant text.
type outputProjector struct{}

func newOutputProjector(int) provider.OutputProjector { return outputProjector{} }

func (outputProjector) ProjectOutput(eventType string, payload map[string]any) (provider.OutputUpdate, bool) {
	switch eventType {
	case "output.delta":
		id, _ := payload["part_id"].(string)
		if id == "" {
			id, _ = payload["message_id"].(string)
		}
		text, _ := payload["text"].(string)
		return provider.OutputUpdate{ID: id, Text: text}, true
	case "provider.event":
		kind, _ := payload["kind"].(string)
		if kind != "text" {
			return provider.OutputUpdate{Ignore: true}, true
		}
		id, _ := payload["part_id"].(string)
		text, _ := payload["text"].(string)
		if id == "" || text == "" {
			return provider.OutputUpdate{Ignore: true}, true
		}
		return provider.OutputUpdate{ID: id, Text: text, Replace: true}, true
	}
	return provider.OutputUpdate{}, false
}
