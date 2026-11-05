package codex

import "github.com/teamva-ai/teamva/node/provider"

type outputProjector struct{}

func (outputProjector) ProjectOutput(eventType string, payload map[string]any) (provider.OutputUpdate, bool) {
	if eventType == "provider.item" {
		item, _ := payload["item"].(map[string]any)
		kind, _ := item["type"].(string)
		if kind != "agentMessage" {
			return provider.OutputUpdate{Ignore: true}, true
		}
		id, _ := item["id"].(string)
		return provider.OutputUpdate{ID: id, Text: provider.ContentText(item), Replace: true, Final: item["phase"] == "final_answer"}, true
	}
	if eventType == "output.delta" {
		id, _ := payload["item_id"].(string)
		if id != "" {
			text, _ := payload["text"].(string)
			return provider.OutputUpdate{ID: id, Text: text}, true
		}
	}
	return provider.OutputUpdate{}, false
}
