package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestFileBridgeWorkItemsPreserveDefinitionsScopeAndRetryIdentity(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable")
	}
	var requests []nodeprotocol.NodeRequest
	caller := toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return nodeprotocol.NodeRequestResult{}, errors.New("receipt lost")
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comment_id": "single-summary", "version": 2}}, nil
	})
	var logs bytes.Buffer
	bridge, err := NewFileBridge(t.TempDir(), "run-work-plan", NewToolBroker(caller, "run-work-plan", "issue", "instance", "author"), log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetToolEventReporter(func(_, _, _ string, payload map[string]any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(raw), "private-work-instruction") || strings.Contains(string(raw), "work_items") {
			t.Error("work definition copied into tool display events")
		}
	})
	path := writeBridgeTestRequest(t, bridge, "issue_comment_create", "plan.json", `{"body":"public summary","mentioned_agent_ids":[],"work_items":[{"key":"first","agent_id":"other","title":"First task","instruction":"private-work-instruction","after":[]},{"key":"again","agent_id":"other","title":"Second task","instruction":"","after":["first"]}]}`)
	request := fileBridgeRequest{tool: "issue_comment_create", name: "plan.json"}
	bridge.handleRequest(t.Context(), request)
	if response := readBridgeTestResponse(t, path); response.OK {
		t.Fatal("lost receipt unexpectedly succeeded")
	}
	bridge.handleRequest(t.Context(), request)
	if len(requests) != 1 {
		t.Fatal("existing response resubmitted mutation")
	}
	if err := os.Remove(path + ".response.json"); err != nil {
		t.Fatal(err)
	}
	bridge.handleRequest(t.Context(), request)
	if response := readBridgeTestResponse(t, path); !response.OK || response.Data["comment_id"] != "single-summary" {
		t.Fatalf("retry=%+v", response)
	}
	if len(requests) != 2 || requests[0].IdempotencyKey != requests[1].IdempotencyKey || requests[0].RequestID == requests[1].RequestID {
		t.Fatal("work plan lost stable operation / fresh delivery identity")
	}
	for _, sent := range requests {
		if sent.RunID != "run-work-plan" || sent.AgentID != "author" || sent.InstanceID != "instance" || sent.IssueID != "issue" {
			t.Fatal("work plan lost authoritative scope")
		}
		items, ok := sent.Payload["work_items"].([]nodeprotocol.WorkPlanItem)
		if !ok || len(items) != 2 || items[0].Instruction != "private-work-instruction" || items[1].AgentID != items[0].AgentID || !reflect.DeepEqual(items[1].After, []string{"first"}) {
			t.Fatalf("work definitions changed: %+v", sent.Payload)
		}
	}
	if strings.Contains(logs.String(), "private-work-instruction") {
		t.Fatal("work instruction logged")
	}
}

func TestFileBridgeWorkItemsRejectInvalidWireGraphSelfAndMixedMentions(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable")
	}
	const valid = `{"key":"first","agent_id":"other","title":"First task","instruction":"","after":[]}`
	for name, payload := range map[string]string{
		"null":               `{"body":"summary","work_items":null}`,
		"empty":              `{"body":"summary","work_items":[]}`,
		"object":             `{"body":"summary","work_items":{}}`,
		"mixed":              `{"body":"summary","mentioned_agent_ids":["other"],"work_items":[` + valid + `]}`,
		"self":               `{"body":"summary","work_items":[{"key":"self","agent_id":"author","title":"Self","instruction":"","after":[]}]}`,
		"unknown-field":      `{"body":"summary","work_items":[{"key":"first","agent_id":"other","title":"Task","instruction":"","after":[],"run_id":"forged"}]}`,
		"null-after":         `{"body":"summary","work_items":[{"key":"first","agent_id":"other","title":"Task","instruction":"","after":null}]}`,
		"null-instruction":   `{"body":"summary","work_items":[{"key":"first","agent_id":"other","title":"Task","instruction":null,"after":[]}]}`,
		"missing-title":      `{"body":"summary","work_items":[{"key":"first","agent_id":"other","instruction":"","after":[]}]}`,
		"cycle":              `{"body":"summary","work_items":[{"key":"first","agent_id":"other","title":"First","instruction":"","after":["second"]},{"key":"second","agent_id":"other","title":"Second","instruction":"","after":["first"]}]}`,
		"duplicate-key":      `{"body":"summary","work_items":[` + valid + `,` + valid + `]}`,
		"missing-dependency": `{"body":"summary","work_items":[{"key":"first","agent_id":"other","title":"Task","instruction":"","after":["missing"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			caller := &recordingToolCaller{}
			bridge, err := NewFileBridge(t.TempDir(), "run-invalid-work", NewToolBroker(caller, "run-invalid-work", "issue", "instance", "author"), nil)
			if err != nil {
				t.Fatal(err)
			}
			path := writeBridgeTestRequest(t, bridge, "issue_comment_create", "invalid.json", payload)
			bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "issue_comment_create", name: "invalid.json"})
			if response := readBridgeTestResponse(t, path); response.OK || len(caller.requests) != 0 {
				t.Fatalf("invalid work reached broker: response=%+v calls=%d", response, len(caller.requests))
			}
		})
	}
}

func TestFileBridgeWorkItemsDocumentationKeepsSystemWaitingAndSelfFence(t *testing.T) {
	markdown := (&FileBridge{broker: NewToolBroker(nil, "run", "issue", "instance", "author")}).ToolsMarkdown()
	for _, text := range []string{"work_items", "与非空 mentioned_agent_ids 互斥", "同一目标 Agent 可重复，仍不能指定自己", "after 依赖由系统等待", "前置 Run 成功且发布真实结果", "不让模型轮询"} {
		if !strings.Contains(markdown, text) {
			t.Errorf("work plan instructions missing %q", text)
		}
	}
}
