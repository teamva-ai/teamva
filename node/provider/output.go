package provider

import "strings"

// OutputUpdate is provider-neutral and explicitly excludes reasoning/user text.
// Replace replaces only the identified message; Final marks the preferred answer.
type OutputUpdate struct {
	ID      string
	Text    string
	Replace bool
	Final   bool
	Ignore  bool
}

type OutputProjector interface {
	ProjectOutput(eventType string, payload map[string]any) (OutputUpdate, bool)
}

func ContentText(input map[string]any) string {
	if text, _ := input["text"].(string); strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	if text, _ := input["output_text"].(string); strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	switch content := input["content"].(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		var result strings.Builder
		for _, value := range content {
			if item, ok := value.(map[string]any); ok {
				result.WriteString(ContentText(item))
			}
		}
		return strings.TrimSpace(result.String())
	}
	return ""
}
