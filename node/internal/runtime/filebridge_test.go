package runtime

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBridgeToolsMarkdownAsyncDelegationContract(t *testing.T) {
	bridge := &FileBridge{
		relativeRoot: ".teamva/tool-requests/run-prompt-contract",
		broker:       NewToolBroker(nil, "run-prompt-contract", "issue-contract", "instance-contract", "agt-delegator-contract"),
	}
	prompt := bridge.ToolsMarkdown()
	for name, required := range map[string][]string{
		"authoritative identity": {"当前 Agent ID", `"agt-delegator-contract"`},
		"asynchronous end":       {"仅本轮向其他 Agent 委派时", "结构化 @", "异步委派", "确认本次工具提交成功后结束当前 Run，不等待对方完成", "委派说明已是本轮公共结果"},
		"no result polling":      {"不要用 sleep、native wait、issue_get、issue_agent_status 或 issue_agent_context 轮询等结果"},
		"no delegated rewrite":   {"不要第二次调用 issue_comment_create 或 issue_comment_update_own 回写本轮已发布的委派评论"},
		"explicit callback":      {"若需要结果回来后继续", "委派正文", "结果评论的 mentioned_agent_ids 中显式 @回本 Agent ID", "正文 @名字不算激活", "新 Run"},
		"no callback ping pong":  {"只按明确的回报要求", "不要仅因本轮由其他 Agent 的评论触发就自动 @回", "结果接收者无需无意义回@", "允许有实际需要的 A→B→A"},
		"IPC receipt preserved":  {"最多 30 秒", "req-<id>.json.response.json", "提交回执", "不是等待被委派者完成"},
		"normal tools preserved": {
			"读取任务最新上下文", "当用户要求修正/补充/更新你之前的评论时，必须使用本工具更新原评论",
			"不限制正常上下文读取或用户显式要求的评论修正", "不能 @ 自己", `"append_new": true`,
		},
		"Goal gate preserved": {"Goal", "working", "final", "独立审核完成门禁不变", "不代表 Goal 已完成"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range required {
				if !strings.Contains(prompt, text) {
					t.Errorf("actual ToolsMarkdown missing %q", text)
				}
			}
		})
	}
	for _, wrongIdentity := range []string{`当前 Agent ID："run-prompt-contract"`, `当前 Agent ID："instance-contract"`, "当前 Agent ID 未提供"} {
		if strings.Contains(prompt, wrongIdentity) {
			t.Errorf("ToolsMarkdown used a guessed identity: %q", wrongIdentity)
		}
	}
}

func TestFileBridgeToolsMarkdownMissingAgentIDDoesNotInventIdentity(t *testing.T) {
	for name, broker := range map[string]*ToolBroker{
		"missing broker": nil,
		"empty agent ID": NewToolBroker(nil, "run-fallback", "issue-fallback", "instance-fallback", ""),
	} {
		t.Run(name, func(t *testing.T) {
			prompt := (&FileBridge{relativeRoot: ".teamva/tool-requests/run-fallback", broker: broker}).ToolsMarkdown()
			for _, text := range []string{"当前 Agent ID 未提供", "issue_agents_list", "is_self=true", "不要猜测"} {
				if !strings.Contains(prompt, text) {
					t.Errorf("missing-ID ToolsMarkdown missing %q", text)
				}
			}
			if strings.Contains(prompt, "当前 Agent ID：") {
				t.Error("missing-ID ToolsMarkdown advertised a fabricated Agent ID")
			}
		})
	}
}

func TestFileBridgeToolsMarkdownReadOnlyAssistantHasNoDelegationInstructions(t *testing.T) {
	broker := NewToolBroker(nil, "run-read-contract", "", "", "agt-read-contract")
	broker.workAssistant = true
	prompt := (&FileBridge{relativeRoot: ".teamva/tool-requests/run-read-contract", broker: broker}).ToolsMarkdown()
	for _, text := range []string{"Read-only work history tools", "Business writes, shell commands and permission escalation are forbidden", "work_records_query", "inspiration_get"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("read-only ToolsMarkdown missing %q", text)
		}
	}
	for _, text := range []string{"issue_comment_create", "issue_comment_update_own", "mentioned_agent_ids", "异步委派", "当前 Agent ID"} {
		if strings.Contains(prompt, text) {
			t.Errorf("read-only ToolsMarkdown leaked Issue delegation instruction %q", text)
		}
	}
}

func TestFileBridgeIsolatesRunsAndIgnoresHistoricalRequests(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	cwd := t.TempDir()
	oldBridge, err := NewFileBridge(cwd, "run-old", nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("create old bridge: %v", err)
	}
	oldRequestDir := filepath.Join(oldBridge.root, "issue_comment_create")
	if err := os.MkdirAll(oldRequestDir, 0o700); err != nil {
		t.Fatalf("create old request directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRequestDir, "req-old.json"), []byte(`{"body":"stale"}`), 0o600); err != nil {
		t.Fatalf("write old request: %v", err)
	}

	newBridge, err := NewFileBridge(cwd, "run-new", nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("create new bridge: %v", err)
	}
	requests, err := newBridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil {
		t.Fatalf("scan new bridge: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("new run consumed historical requests: %v", requests)
	}
	if oldBridge.root == newBridge.root {
		t.Fatal("run bridges share a request root")
	}
	for _, tool := range fileBridgeTools {
		info, err := os.Stat(filepath.Join(newBridge.root, tool))
		if err != nil || !info.IsDir() {
			t.Fatalf("tool directory %q was not pre-created: %v", tool, err)
		}
	}

	markdown := newBridge.ToolsMarkdown()
	if !strings.Contains(markdown, ".teamva/tool-requests/run-new/<tool>/") {
		t.Fatalf("tool instructions do not contain isolated run path: %s", markdown)
	}
	if strings.Contains(markdown, "mkdir -p .teamva/tool-requests/<tool>/") {
		t.Fatalf("tool instructions still expose the shared legacy path: %s", markdown)
	}
	if !strings.Contains(markdown, "不要执行 mkdir、ls 或 glob") ||
		!strings.Contains(markdown, "不要用 glob 查找响应") {
		t.Fatalf("tool instructions do not prevent noisy hidden-directory polling: %s", markdown)
	}
}

func TestFileBridgeRejectsUnsafeRunID(t *testing.T) {
	for _, runID := range []string{"", "../other-run", "run/other", "run other"} {
		if _, err := NewFileBridge(t.TempDir(), runID, nil, nil); err == nil {
			t.Fatalf("unsafe run id %q was accepted", runID)
		}
	}
}

func TestFileBridgeRejectsSymlinkRequestAndAtomicallyReplacesResponseSymlink(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	bridge, err := NewFileBridge(t.TempDir(), "run-safe-files", nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(bridge.root, "issue_get")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(directory, "req-link.json")
	if err := os.Symlink(outside, request); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 0 {
		t.Fatalf("symlink request scan=%v err=%v", requests, err)
	}
	toolDirectory, err := bridge.openToolDirectory("issue_get")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBridgeRequestAt(toolDirectory, filepath.Base(request), 1<<20); err == nil {
		_ = toolDirectory.Close()
		t.Fatal("symlink request was read")
	}
	_ = toolDirectory.Close()
	regularRequest := filepath.Join(directory, "req-regular.json")
	if err := os.WriteFile(regularRequest, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response := regularRequest + ".response.json"
	if err := os.Symlink(outside, response); err != nil {
		t.Fatal(err)
	}
	toolDirectory, err = bridge.openToolDirectory("issue_get")
	if err != nil {
		t.Fatal(err)
	}
	bridge.writeResponse(toolDirectory, "issue_get", filepath.Base(regularRequest), "req-regular", false, "test", "safe", nil)
	_ = toolDirectory.Close()
	payload, err := os.ReadFile(outside)
	if err != nil || string(payload) != `{"secret":true}` {
		t.Fatalf("response write followed outside symlink: payload=%q err=%v", payload, err)
	}
	info, err := os.Lstat(response)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("response symlink was not atomically replaced: info=%v err=%v", info, err)
	}
}

func TestFileBridgeRejectsSymlinkedControlDirectory(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cwd, ".teamva")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := NewFileBridge(cwd, "run-symlink", nil, nil); err == nil {
		t.Fatal("file bridge followed a symlinked control directory")
	}
	if _, err := os.Lstat(filepath.Join(outside, "tool-requests")); !os.IsNotExist(err) {
		t.Fatalf("file bridge wrote outside CWD through symlink: %v", err)
	}
}

func TestFileBridgeRejectsReplacedToolDirectory(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	bridge, err := NewFileBridge(t.TempDir(), "run-replaced-directory", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(bridge.root, "issue_get")
	if err := os.Rename(toolPath, toolPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(toolPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.openToolDirectory("issue_get"); err == nil {
		t.Fatal("file bridge accepted a replacement tool directory")
	}
}

func TestFileBridgeRejectsPreexistingRunDirectory(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	cwd := t.TempDir()
	preexisting := filepath.Join(cwd, ".teamva", "tool-requests", "run-preplanted")
	if err := os.MkdirAll(filepath.Join(preexisting, "issue_get"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(preexisting, "issue_get", "attacker.json")
	if err := os.WriteFile(request, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileBridge(cwd, "run-preplanted", nil, nil); err == nil {
		t.Fatal("file bridge accepted a preexisting run request directory")
	}
	if payload, err := os.ReadFile(request); err != nil || string(payload) != `{}` {
		t.Fatalf("rejected preexisting request was mutated: payload=%q err=%v", payload, err)
	}
}

func TestFileBridgeRejectsNonIntegerVersionBeforeToolCall(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	caller := &recordingToolCaller{}
	bridge, err := NewFileBridge(t.TempDir(), "run-invalid-version",
		NewToolBroker(caller, "run-invalid-version", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(bridge.root, "issue_comment_update_own", "request.json")
	if err := os.WriteFile(requestPath,
		[]byte(`{"comment_id":"comment","expected_version":1.5,"body":"updated"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])
	if len(caller.requests) != 0 {
		t.Fatalf("invalid numeric request reached ToolBroker: %+v", caller.requests)
	}
	responsePath := requestPath + ".response.json"
	payload, err := os.ReadFile(responsePath)
	if err != nil || !strings.Contains(string(payload), `"ok": false`) {
		t.Fatalf("invalid request response=%q err=%v", payload, err)
	}
}

func TestFileBridgeReportsCollaborationToolEvents(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	type callEvent struct {
		tool, callID, status string
		payload              map[string]any
	}
	var events []callEvent
	caller := &recordingToolCaller{responseData: map[string]any{
		"comment_id": "comment-9", "version": 4,
	}}
	bridge, err := NewFileBridge(t.TempDir(), "run-tool-events",
		NewToolBroker(caller, "run-tool-events", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetToolEventReporter(func(tool, callID, status string, payload map[string]any) {
		events = append(events, callEvent{tool: tool, callID: callID, status: status, payload: payload})
	})
	requestPath := filepath.Join(bridge.root, "issue_comment_create", "req-events.json")
	body := strings.Repeat("评论内容 ", 40)
	if err := os.WriteFile(requestPath,
		[]byte(`{"body":"`+body+`","mentioned_agent_ids":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])

	if len(events) != 2 {
		t.Fatalf("expected running and completed events, got %+v", events)
	}
	running, completed := events[0], events[1]
	if running.tool != "issue_comment_create" || running.callID != "req-events" || running.status != "running" {
		t.Fatalf("running event=%+v", running)
	}
	if completed.status != "completed" {
		t.Fatalf("completed event=%+v", completed)
	}
	input, _ := completed.payload["input"].(map[string]any)
	summary, _ := input["summary"].(string)
	if !strings.HasPrefix(summary, "评论内容") || !strings.HasSuffix(summary, "…") ||
		len([]rune(summary)) > 121 {
		t.Fatalf("input summary was not truncated: %q", summary)
	}
	output, _ := completed.payload["output"].(map[string]any)
	if output["comment_id"] != "comment-9" || output["version"] != 4 {
		t.Fatalf("output summary=%+v", output)
	}
	if _, ok := completed.payload["duration_ms"]; !ok {
		t.Fatalf("completed event lacks duration: %+v", completed.payload)
	}
}

func TestFileBridgeIssueGetSummaryIncludesCommentPreview(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	comments := make([]any, 0, 6)
	for index := 0; index < 6; index++ {
		comments = append(comments, map[string]any{
			"author_kind": "user",
			"body":        strings.Repeat("评论", 100),
		})
	}
	caller := &recordingToolCaller{responseData: map[string]any{
		"id": "issue-1", "title": "任务标题", "status": "open",
		"description": strings.Repeat("描述", 150),
		"comments":    comments,
	}}
	var completed map[string]any
	bridge, err := NewFileBridge(t.TempDir(), "run-issue-get",
		NewToolBroker(caller, "run-issue-get", "issue-1", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetToolEventReporter(func(tool, callID, status string, payload map[string]any) {
		if status != "completed" {
			return
		}
		completed, _ = payload["output"].(map[string]any)
	})
	requestPath := filepath.Join(bridge.root, "issue_get", "req-get.json")
	if err := os.WriteFile(requestPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])

	if completed == nil {
		t.Fatal("issue_get completed event was not reported")
	}
	if completed["title"] != "任务标题" || completed["status"] != "open" || completed["comment_count"] != 6 {
		t.Fatalf("summary=%+v", completed)
	}
	description, _ := completed["description"].(string)
	if !strings.HasSuffix(description, "…") || len([]rune(description)) > 201 {
		t.Fatalf("description was not truncated: %q", description)
	}
	preview, _ := completed["comments"].([]map[string]any)
	if len(preview) != 5 {
		t.Fatalf("comment preview length = %d, want 5", len(preview))
	}
	body, _ := preview[0]["body"].(string)
	if !strings.HasSuffix(body, "…") || len([]rune(body)) > 161 {
		t.Fatalf("comment body was not truncated: %q", body)
	}
}

func TestFileBridgeReportsFailedEventForInvalidRequest(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	var statuses []string
	bridge, err := NewFileBridge(t.TempDir(), "run-tool-failure",
		NewToolBroker(&recordingToolCaller{}, "run-tool-failure", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetToolEventReporter(func(tool, callID, status string, payload map[string]any) {
		statuses = append(statuses, status)
	})
	requestPath := filepath.Join(bridge.root, "issue_get", "req-broken.json")
	if err := os.WriteFile(requestPath, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])
	if len(statuses) != 1 || statuses[0] != "failed" {
		t.Fatalf("invalid request events=%v", statuses)
	}
}

func TestFileBridgeAgentsListSummaryReportsAgents(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	caller := &recordingToolCaller{responseData: map[string]any{
		"agents": []any{
			map[string]any{
				"agent_id": "agt-1", "name": "Lumi", "status": "idle",
				"participating": true, "is_self": false, "instance_status": "idle",
			},
			map[string]any{
				"agent_id": "agt-2", "name": "opencode 测试", "status": "idle",
				"participating": true, "is_self": true, "instance_status": "running",
			},
		},
	}}
	var completed map[string]any
	bridge, err := NewFileBridge(t.TempDir(), "run-agents-list",
		NewToolBroker(caller, "run-agents-list", "issue-1", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetToolEventReporter(func(tool, callID, status string, payload map[string]any) {
		if status == "completed" {
			completed, _ = payload["output"].(map[string]any)
		}
	})
	requestPath := filepath.Join(bridge.root, "issue_agents_list", "req-agents.json")
	if err := os.WriteFile(requestPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])

	if completed == nil {
		t.Fatal("issue_agents_list completed event was not reported")
	}
	if completed["agent_count"] != 2 {
		t.Fatalf("agent_count=%v summary=%+v", completed["agent_count"], completed)
	}
	preview, _ := completed["agents"].([]map[string]any)
	if len(preview) != 2 {
		t.Fatalf("agents preview=%+v", preview)
	}
	if preview[0]["name"] != "Lumi" || preview[0]["participating"] != true || preview[1]["is_self"] != true {
		t.Fatalf("agents preview=%+v", preview)
	}
}

func TestBridgeResultSummaryCoversAgentToolsAndReadOnlyAssistant(t *testing.T) {
	status := bridgeResultSummary(map[string]any{
		"id": "instance-1", "agent_id": "agt-1", "status": "idle",
		"last_activity_at": "2026-09-27T20:00:00Z", "pending_mentions": 2,
	})
	if status["id"] != "instance-1" || status["agent_id"] != "agt-1" ||
		status["status"] != "idle" || status["pending_mentions"] != 2 {
		t.Fatalf("agent status summary=%+v", status)
	}

	contextItems := make([]any, 0, 7)
	for index := 1; index <= 7; index++ {
		contextItems = append(contextItems, map[string]any{
			"seq": index, "type": "provider.tool", "tool": "issue_get", "status": "completed",
		})
	}
	contextSummary := bridgeResultSummary(map[string]any{
		"agent_id": "agt-1", "status": "running", "context": contextItems,
	})
	if contextSummary["context_count"] != 7 {
		t.Fatalf("context summary=%+v", contextSummary)
	}
	contextPreview, _ := contextSummary["context"].([]map[string]any)
	if len(contextPreview) != 5 || contextPreview[4]["seq"] != 7 {
		t.Fatalf("context preview=%+v", contextPreview)
	}

	longTitle := strings.Repeat("长标题", 80)
	readSummary := bridgeResultSummary(map[string]any{
		"items": []any{map[string]any{
			"id": "rec-1", "kind": "issue", "title": longTitle,
			"outcome": "succeeded", "occurred_at": "2026-09-27T20:00:00Z",
		}},
		"total": 9,
	})
	if readSummary["item_count"] != 1 || readSummary["total"] != 9 {
		t.Fatalf("read summary=%+v", readSummary)
	}
	readPreview, _ := readSummary["items"].([]map[string]any)
	title, _ := readPreview[0]["title"].(string)
	if len(readPreview) != 1 || !strings.HasSuffix(title, "…") || len([]rune(title)) > 201 {
		t.Fatalf("read preview=%+v", readPreview)
	}

	recordSummary := bridgeResultSummary(map[string]any{
		"record": map[string]any{
			"id": "rec-1", "title": "标题", "object_type": "issue", "object_id": "iss-1",
			"before": map[string]any{"status": "open"},
		},
		"children": []any{map[string]any{"id": "rec-2"}},
		"links":    []any{map[string]any{"kind": "run", "id": "run-1"}},
	})
	record, _ := recordSummary["record"].(map[string]any)
	if record["id"] != "rec-1" || record["object_type"] != "issue" || record["before"] != nil {
		t.Fatalf("record preview=%+v", record)
	}
	if recordSummary["children_count"] != 1 || recordSummary["links_count"] != 1 {
		t.Fatalf("record summary=%+v", recordSummary)
	}

	fullSummary := bridgeResultSummary(map[string]any{
		"link":  map[string]any{"kind": "issue", "id": "iss-1", "title": "旧标题", "url": "/issues?issue=iss-1"},
		"issue": map[string]any{"id": "iss-1", "title": "新标题", "status": "open"},
		"comments": []any{map[string]any{
			"author_kind": "agent", "author_agent_id": "agt-1", "status": "published",
			"body": strings.Repeat("评论", 100),
		}},
		"page": map[string]any{"total": 12, "has_more": true, "next_offset": 5},
	})
	link, _ := fullSummary["link"].(map[string]any)
	issue, _ := fullSummary["issue"].(map[string]any)
	if link["kind"] != "issue" || link["title"] != "旧标题" || issue["title"] != "新标题" {
		t.Fatalf("full summary=%+v", fullSummary)
	}
	if fullSummary["comment_count"] != 1 || fullSummary["total"] != 12 ||
		fullSummary["has_more"] != true || fullSummary["next_offset"] != nil {
		t.Fatalf("full summary=%+v", fullSummary)
	}
}

func TestFileBridgeCommentResponsePreservesCommentIdentity(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	caller := &recordingToolCaller{responseData: map[string]any{
		"comment_id": "comment-published", "version": 3,
	}}
	bridge, err := NewFileBridge(t.TempDir(), "run-comment-response",
		NewToolBroker(caller, "run-comment-response", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(bridge.root, "issue_comment_create", "req-comment.json")
	if err := os.WriteFile(requestPath,
		[]byte(`{"body":"marker","mentioned_agent_ids":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])
	payload, err := os.ReadFile(requestPath + ".response.json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RequestID string `json:"request_id"`
		OK        bool   `json:"ok"`
		Data      struct {
			CommentID string `json:"comment_id"`
			Version   int    `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "req-comment" || !response.OK ||
		response.Data.CommentID != "comment-published" || response.Data.Version != 3 {
		t.Fatalf("response=%+v", response)
	}
}
