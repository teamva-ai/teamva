package nodeprotocol

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func workPlanAgents() []WorkPlanAgent {
	return []WorkPlanAgent{{ID: "a", Name: "甲"}, {ID: "b", Name: "乙"}}
}

func workPlanItems() []WorkPlanItem {
	return []WorkPlanItem{
		{Key: "one", AgentID: "a", Title: "实现并验证接口", Instruction: "", After: []string{}},
		{Key: "two", AgentID: "b", Title: "审核实现", Instruction: "检查边界\n运行测试", After: []string{"one"}},
	}
}

func TestWorkPlanWireAndStrictDecode(t *testing.T) {
	request := WorkPlanRequest{ExecutionID: "attempt", Provider: "provider", Model: "model", Input: "目标", Agents: workPlanAgents()}
	data, err := json.Marshal(request)
	if err != nil || string(data) != `{"execution_id":"attempt","provider":"provider","model":"model","input":"目标","agents":[{"id":"a","name":"甲"},{"id":"b","name":"乙"}]}` {
		t.Fatalf("request contract: %s %v", data, err)
	}
	items := workPlanItems()
	data, err = json.Marshal(WorkPlanResult{WorkItems: items})
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeWorkPlan(" \n"+string(data)+"\t", workPlanAgents())
	if err != nil || !reflect.DeepEqual(result.WorkItems, items) {
		t.Fatalf("valid draft: %+v %v", result, err)
	}
	// Field and dependency order are not semantic restrictions; forward refs work.
	items[0], items[1] = items[1], items[0]
	if err := ValidateWorkPlanItems(items, workPlanAgents()); err != nil {
		t.Fatal("forward dependency rejected")
	}
}

func TestWorkPlanStrictJSONRejectsAmbiguousOrPartialOutput(t *testing.T) {
	item := `{"key":"one","agent_id":"a","title":"任务","instruction":"","after":[]}`
	valid := `{"work_items":[` + item + `]}`
	cases := map[string]string{
		"empty": "", "null": "null", "array": "[]", "no-items": `{"work_items":[]}`,
		"null-items": `{"work_items":null}`, "missing-root": `{}`, "root-case": strings.Replace(valid, "work_items", "Work_Items", 1),
		"extra-root":             strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		"duplicate-root":         strings.TrimSuffix(valid, "}") + `,"work_items":[]}`,
		"escaped-duplicate-root": strings.TrimSuffix(valid, "}") + `,"work_\u0069tems":[]}`,
		"duplicate-item":         strings.Replace(valid, `"key":"one"`, `"key":"one","key":"other"`, 1),
		"escaped-duplicate-item": strings.Replace(valid, `"key":"one"`, `"key":"one","k\u0065y":"one"`, 1),
		"unknown-item-field":     strings.Replace(valid, `"key":"one"`, `"key":"one","extra":1`, 1),
		"missing-field":          strings.Replace(valid, `,"instruction":""`, "", 1),
		"wrong-field-case":       strings.Replace(valid, `"title"`, `"Title"`, 1),
		"null-instruction":       strings.Replace(valid, `"instruction":""`, `"instruction":null`, 1),
		"object-instruction":     strings.Replace(valid, `"instruction":""`, `"instruction":{}`, 1),
		"null-after":             strings.Replace(valid, `"after":[]`, `"after":null`, 1),
		"string-after":           strings.Replace(valid, `"after":[]`, `"after":"one"`, 1),
		"null-dependency":        strings.Replace(valid, `"after":[]`, `"after":[null]`, 1),
		"empty-title":            strings.Replace(valid, `"title":"任务"`, `"title":""`, 1),
		"multiline-title":        strings.Replace(valid, `"title":"任务"`, `"title":"任务\n其他"`, 1),
		"unpaired-surrogate":     strings.Replace(valid, `"title":"任务"`, `"title":"\ud800"`, 1),
		"unknown-agent":          strings.Replace(valid, `"agent_id":"a"`, `"agent_id":"foreign"`, 1),
		"trailing-object":        valid + `{}`, "trailing-null": valid + `null`, "trailing-text": valid + "private-output",
		"prose": "Draft: " + valid, "fence": "```json\n" + valid + "\n```", "incomplete": strings.TrimSuffix(valid, "}"),
		"invalid-utf8": strings.Replace(valid, "任务", string([]byte{0xff}), 1),
		"too-large":    valid + strings.Repeat(" ", MaxWorkPlanOutputBytes),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := DecodeWorkPlan(raw, workPlanAgents())
			if err == nil || err.Error() != "invalid_work_plan" || len(result.WorkItems) != 0 {
				t.Fatalf("invalid draft accepted or output leaked: %v", err)
			}
		})
	}
}

func TestValidateWorkPlanStructureAllowlistAndDAG(t *testing.T) {
	if err := ValidateWorkPlanItems(workPlanItems(), nil); err != nil {
		t.Fatal("nil allowlist must perform structural validation only")
	}
	if err := ValidateWorkPlanItems(workPlanItems(), []WorkPlanAgent{{ID: "a"}, {ID: "b"}}); err != nil {
		t.Fatal("pure allowlist validation must not require model-only Agent names")
	}
	optional := workPlanItems()
	optional[0].Instruction = " \n\t"
	if err := ValidateWorkPlanItems(optional, nil); err != nil {
		t.Fatal("optional instruction must not require text beyond the explicit title")
	}
	for name, change := range map[string]func([]WorkPlanItem) []WorkPlanItem{
		"empty":         func([]WorkPlanItem) []WorkPlanItem { return nil },
		"duplicate-key": func(items []WorkPlanItem) []WorkPlanItem { items[1].Key = "one"; return items },
		"missing-key":   func(items []WorkPlanItem) []WorkPlanItem { items[0].Key = ""; return items },
		"empty-title":   func(items []WorkPlanItem) []WorkPlanItem { items[0].Title = " \t"; return items },
		"long-title": func(items []WorkPlanItem) []WorkPlanItem {
			items[0].Title = strings.Repeat("字", MaxWorkPlanTitleRunes+1)
			return items
		},
		"long-instruction": func(items []WorkPlanItem) []WorkPlanItem {
			items[0].Instruction = strings.Repeat("a", MaxWorkPlanInstructionBytes+1)
			return items
		},
		"invalid-instruction":  func(items []WorkPlanItem) []WorkPlanItem { items[0].Instruction = string([]byte{0xff}); return items },
		"missing-agent":        func(items []WorkPlanItem) []WorkPlanItem { items[0].AgentID = ""; return items },
		"self-dependency":      func(items []WorkPlanItem) []WorkPlanItem { items[0].After = []string{"one"}; return items },
		"unknown-dependency":   func(items []WorkPlanItem) []WorkPlanItem { items[0].After = []string{"missing"}; return items },
		"duplicate-dependency": func(items []WorkPlanItem) []WorkPlanItem { items[1].After = []string{"one", "one"}; return items },
		"cycle":                func(items []WorkPlanItem) []WorkPlanItem { items[0].After = []string{"two"}; return items },
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateWorkPlanItems(change(workPlanItems()), nil); err == nil || err.Error() != "invalid_work_plan" {
				t.Fatal("invalid structure accepted")
			}
		})
	}
	for _, agents := range [][]WorkPlanAgent{{}, {{ID: "a", Name: "甲"}}, {{ID: "a", Name: "甲"}, {ID: "a", Name: "重复"}}} {
		if err := ValidateWorkPlanItems(workPlanItems(), agents); err == nil {
			t.Fatal("non-nil allowlist was not enforced")
		}
	}
	items := workPlanItems()
	items[0].Title, items[0].Instruction = strings.Repeat("字", MaxWorkPlanTitleRunes), strings.Repeat("字", MaxWorkPlanInstructionBytes/3)+"x"
	if err := ValidateWorkPlanItems(items, nil); err != nil {
		t.Fatal("inclusive Unicode/byte bounds rejected")
	}
	items = nil
	for i := range MaxWorkPlanItems {
		item := WorkPlanItem{Key: fmt.Sprint(i), AgentID: "a", Title: "任务"}
		if i > 0 {
			item.After = []string{fmt.Sprint(i - 1)}
		}
		items = append(items, item)
	}
	if err := ValidateWorkPlanItems(items, nil); err != nil {
		t.Fatal(err)
	}
	items = append(items, WorkPlanItem{Key: "overflow", AgentID: "a", Title: "任务"})
	if err := ValidateWorkPlanItems(items, nil); err == nil {
		t.Fatal("item limit ignored")
	}
}

func TestWorkPlanInputAndAggregateOutputBounds(t *testing.T) {
	request := WorkPlanRequest{Input: "x", Agents: workPlanAgents()}
	prompt, err := WorkPlanInput(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "provider") || strings.Contains(prompt, "execution_id") || strings.Contains(prompt, "system_prompt") {
		t.Fatal("native metadata entered model input")
	}
	request.Input = strings.Repeat("x", MaxWorkPlanInputBytes-len(prompt)+1)
	if prompt, err = WorkPlanInput(request); err != nil || len(prompt) != MaxWorkPlanInputBytes {
		t.Fatal("inclusive input byte limit rejected")
	}
	request.Input += "x"
	if _, err = WorkPlanInput(request); err == nil {
		t.Fatal("input limit ignored")
	}
	for _, request := range []WorkPlanRequest{
		{Input: "x"}, {Input: "", Agents: workPlanAgents()},
		{Input: "x", Agents: []WorkPlanAgent{{ID: "a"}}},
		{Input: "x", Agents: []WorkPlanAgent{{ID: "a", Name: strings.Repeat("x", MaxWorkPlanInputBytes)}}},
		{Input: strings.Repeat("<", MaxWorkPlanInputBytes/2), Agents: workPlanAgents()},
	} {
		if _, err := WorkPlanInput(request); err == nil {
			t.Fatal("invalid/escaped oversized input accepted")
		}
	}
	items := make([]WorkPlanItem, 8)
	for i := range items {
		items[i] = WorkPlanItem{Key: fmt.Sprint(i), AgentID: "a", Title: "任务", After: []string{}}
		if i < len(items)-1 {
			items[i].Instruction = strings.Repeat("x", MaxWorkPlanInstructionBytes)
		}
	}
	encoded, _ := json.Marshal(WorkPlanResult{WorkItems: items})
	items[7].Instruction = strings.Repeat("x", MaxWorkPlanOutputBytes-len(encoded))
	encoded, _ = json.Marshal(WorkPlanResult{WorkItems: items})
	if len(encoded) != MaxWorkPlanOutputBytes {
		t.Fatal("bad output boundary fixture")
	}
	if _, err := DecodeWorkPlan(string(encoded), workPlanAgents()); err != nil {
		t.Fatal("inclusive output limit rejected")
	}
	items[7].Instruction += "x"
	if err := ValidateWorkPlanItems(items, nil); err == nil {
		t.Fatal("aggregate output limit ignored")
	}
}
