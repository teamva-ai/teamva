package nodeprotocol

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxWorkPlanItems            = 32
	MaxWorkPlanTitleRunes       = 120
	MaxWorkPlanInstructionBytes = 16384
	// MaxWorkPlanInputBytes bounds the JSON input + Agent allowlist sent to the model.
	MaxWorkPlanInputBytes  = 65536
	MaxWorkPlanOutputBytes = 131072
)

type WorkPlanAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WorkPlanItem struct {
	Key         string   `json:"key"`
	AgentID     string   `json:"agent_id"`
	Title       string   `json:"title"`
	Instruction string   `json:"instruction"`
	After       []string `json:"after"`
}

// WorkPlanRequest is an isolated, non-resumable, strictly tool-free draft attempt.
// Provider/model are resolved from a saved Agent by the caller, never by the model.
type WorkPlanRequest struct {
	ExecutionID string          `json:"execution_id"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Input       string          `json:"input"`
	Agents      []WorkPlanAgent `json:"agents"`
}

type WorkPlanResult struct {
	WorkItems []WorkPlanItem `json:"work_items"`
}

var errInvalidWorkPlan = errors.New("invalid_work_plan")

// WorkPlanInput encodes only the untrusted text and explicit Agent allowlist.
// Its byte bound includes JSON escaping and the allowlist, not native metadata.
func WorkPlanInput(request WorkPlanRequest) (string, error) {
	if strings.TrimSpace(request.Input) == "" || !workPlanText(request.Input) || len(request.Input) > MaxWorkPlanInputBytes || !validWorkPlanAgents(request.Agents) {
		return "", errInvalidWorkPlan
	}
	for _, agent := range request.Agents {
		if !workPlanLine(agent.Name) {
			return "", errInvalidWorkPlan
		}
	}
	data, err := json.Marshal(struct {
		Input  string          `json:"input"`
		Agents []WorkPlanAgent `json:"agents"`
	}{request.Input, request.Agents})
	if err != nil || len(data) > MaxWorkPlanInputBytes {
		return "", errInvalidWorkPlan
	}
	return string(data), nil
}

// ValidateWorkPlanItems has no Provider or business side effects. Nil agents
// skips the allowlist check; a non-nil list is an exact allowlist. Nil After
// means no dependencies for Go/manual callers. Instruction may be empty.
func ValidateWorkPlanItems(items []WorkPlanItem, agents []WorkPlanAgent) error {
	if len(items) == 0 || len(items) > MaxWorkPlanItems || (agents != nil && !validWorkPlanAgents(agents)) {
		return errInvalidWorkPlan
	}
	allowed := make(map[string]bool, len(agents))
	for _, agent := range agents {
		allowed[agent.ID] = true
	}
	keys := make(map[string]int, len(items))
	for i, item := range items {
		if !workPlanLine(item.Key) || strings.TrimSpace(item.Key) != item.Key ||
			!workPlanLine(item.AgentID) || strings.TrimSpace(item.AgentID) != item.AgentID || (agents != nil && !allowed[item.AgentID]) ||
			!workPlanLine(item.Title) || utf8.RuneCountInString(item.Title) > MaxWorkPlanTitleRunes ||
			!workPlanText(item.Instruction) || len(item.Instruction) > MaxWorkPlanInstructionBytes {
			return errInvalidWorkPlan
		}
		if _, exists := keys[item.Key]; exists {
			return errInvalidWorkPlan
		}
		keys[item.Key] = i
	}
	for _, item := range items {
		if len(item.After) >= len(items) {
			return errInvalidWorkPlan
		}
		seen := make(map[string]bool, len(item.After))
		for _, key := range item.After {
			if _, exists := keys[key]; !exists || key == item.Key || seen[key] {
				return errInvalidWorkPlan
			}
			seen[key] = true
		}
	}
	colors := make([]uint8, len(items))
	var visit func(int) bool
	visit = func(index int) bool {
		if colors[index] != 0 {
			return colors[index] == 2
		}
		colors[index] = 1
		for _, key := range items[index].After {
			if !visit(keys[key]) {
				return false
			}
		}
		colors[index] = 2
		return true
	}
	for i := range items {
		if !visit(i) {
			return errInvalidWorkPlan
		}
	}
	encoded, err := json.Marshal(WorkPlanResult{WorkItems: items})
	if err != nil || len(encoded) > MaxWorkPlanOutputBytes {
		return errInvalidWorkPlan
	}
	return nil
}

// DecodeWorkPlan requires exactly the declared fields, including array-valued
// After. Token-level field tracking rejects duplicates (also escaped aliases).
func DecodeWorkPlan(raw string, agents []WorkPlanAgent) (WorkPlanResult, error) {
	invalid := func() (WorkPlanResult, error) { return WorkPlanResult{}, errInvalidWorkPlan }
	if len(raw) > MaxWorkPlanOutputBytes || !utf8.ValidString(raw) {
		return invalid()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	token := func(expected json.Token) bool {
		actual, err := decoder.Token()
		return err == nil && actual == expected
	}
	if !token(json.Delim('{')) || !token("work_items") || !token(json.Delim('[')) {
		return invalid()
	}
	items := make([]WorkPlanItem, 0)
	for decoder.More() {
		if len(items) == MaxWorkPlanItems || !token(json.Delim('{')) {
			return invalid()
		}
		var item WorkPlanItem
		seen := map[string]bool{}
		for decoder.More() {
			field, err := decoder.Token()
			name, ok := field.(string)
			if err != nil || !ok || seen[name] {
				return invalid()
			}
			seen[name] = true
			var target *string
			switch name {
			case "key":
				target = &item.Key
			case "agent_id":
				target = &item.AgentID
			case "title":
				target = &item.Title
			case "instruction":
				target = &item.Instruction
			case "after":
				if !token(json.Delim('[')) {
					return invalid()
				}
				item.After = make([]string, 0)
				for decoder.More() {
					value, err := decoder.Token()
					key, ok := value.(string)
					if len(item.After) >= MaxWorkPlanItems || err != nil || !ok {
						return invalid()
					}
					item.After = append(item.After, key)
				}
				if !token(json.Delim(']')) {
					return invalid()
				}
			default:
				return invalid()
			}
			if target != nil {
				value, err := decoder.Token()
				text, ok := value.(string)
				if err != nil || !ok {
					return invalid()
				}
				*target = text
			}
		}
		if len(seen) != 5 || !token(json.Delim('}')) {
			return invalid()
		}
		items = append(items, item)
	}
	if !token(json.Delim(']')) || !token(json.Delim('}')) {
		return invalid()
	}
	if _, err := decoder.Token(); err != io.EOF || ValidateWorkPlanItems(items, agents) != nil {
		return invalid()
	}
	return WorkPlanResult{WorkItems: items}, nil
}

func validWorkPlanAgents(agents []WorkPlanAgent) bool {
	if len(agents) == 0 {
		return false
	}
	seen := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if !workPlanLine(agent.ID) || strings.TrimSpace(agent.ID) != agent.ID || seen[agent.ID] {
			return false
		}
		seen[agent.ID] = true
	}
	return true
}

func workPlanText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if r == unicode.ReplacementChar || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	return true
}

func workPlanLine(text string) bool {
	if strings.TrimSpace(text) == "" || !workPlanText(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}
