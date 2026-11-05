package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// Mirror the JSON-tagged DTO shape returned by ExecuteWorkReadTool without
// importing server/internal or serializing original history to normalize it.
type bridgeWorkRecordDTO struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Action        string         `json:"action"`
	Outcome       string         `json:"outcome"`
	Title         string         `json:"title"`
	ProjectID     string         `json:"project_id"`
	ObjectType    string         `json:"object_type"`
	ObjectID      string         `json:"object_id"`
	ObjectVersion int64          `json:"object_version"`
	OccurredAt    string         `json:"occurred_at"`
	ErrorCode     string         `json:"error_code"`
	Refs          map[string]any `json:"refs"`
	Before        map[string]any `json:"before"`
	After         map[string]any `json:"after"`
	Unknown       any            `json:"unknown"`
}

type bridgeWorkLinkDTO struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Deleted bool   `json:"deleted"`
}

type bridgeReadSummaryCaller struct {
	data map[string]any
	err  error
}

func (c bridgeReadSummaryCaller) SendToolRequest(context.Context, nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
	return nodeprotocol.NodeRequestResult{OK: c.err == nil, Data: c.data}, c.err
}

func TestFileBridgeReadOnlyRealDTOShapes(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	record := bridgeWorkRecordDTO{
		ID: "op-record-1", Kind: "operation", Action: "inspiration.update", Outcome: "succeeded",
		Before:  map[string]any{"body": "PRIVATE_HISTORY_SENTINEL"},
		After:   map[string]any{"markdown": "PRIVATE_MARKDOWN_SENTINEL"},
		Unknown: "PRIVATE_UNKNOWN_SENTINEL",
	}
	for _, test := range []struct {
		tool    string
		request string
		data    map[string]any
	}{
		{"work_records_query", `{"search":"PRIVATE_SEARCH_SENTINEL","limit":50}`, map[string]any{"items": []bridgeWorkRecordDTO{record}, "total": 5}},
		{"work_record_get", `{"id":"op-record-1"}`, map[string]any{"record": record, "children": []bridgeWorkRecordDTO{}, "links": []bridgeWorkLinkDTO{{Kind: "inspiration", ID: "ins-1"}}}},
	} {
		t.Run(test.tool, func(t *testing.T) {
			broker := NewToolBroker(bridgeReadSummaryCaller{data: test.data}, "run-read-summary", "", "", "lumi")
			broker.workAssistant = true
			bridge, err := NewFileBridge(t.TempDir(), "run-read-summary", broker, nil)
			if err != nil {
				t.Fatal(err)
			}
			var completed map[string]any
			bridge.SetToolEventReporter(func(_, _, status string, payload map[string]any) {
				// This callback is the payload that Runtime forwards to persistence.
				wire, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"PRIVATE_HISTORY_SENTINEL", "PRIVATE_MARKDOWN_SENTINEL", "PRIVATE_UNKNOWN_SENTINEL", "PRIVATE_SEARCH_SENTINEL"} {
					if strings.Contains(string(wire), secret) {
						t.Error("read-only event copied private context/search into persistence")
					}
				}
				if status == "completed" {
					completed = payload
				}
			})
			name := filepath.Join(bridge.root, test.tool, "req-1.json")
			if err := os.WriteFile(name, []byte(test.request), 0o600); err != nil {
				t.Fatal(err)
			}
			bridge.handleRequest(t.Context(), fileBridgeRequest{tool: test.tool, name: "req-1.json"})
			if completed == nil {
				t.Fatal("no completed callback")
			}
			output, _ := completed["output"].(map[string]any)
			input, _ := completed["input"].(map[string]any)
			if test.tool == "work_records_query" {
				if output["item_count"] != 1 || output["total"] != 5 {
					t.Fatalf("actual typed query lost count: %v", output)
				}
				preview, _ := output["items"].([]map[string]any)
				if len(preview) != 1 || preview[0]["id"] != record.ID || preview[0]["action"] != record.Action || preview[0]["outcome"] != record.Outcome {
					t.Fatalf("actual typed query lost metadata: %v", output)
				}
			} else {
				metadata, _ := output["record"].(map[string]any)
				if metadata["id"] != record.ID || metadata["kind"] != record.Kind || metadata["action"] != record.Action || metadata["outcome"] != record.Outcome || output["children_count"] != 0 || output["links_count"] != 1 || input["id"] != record.ID {
					t.Fatalf("actual typed get lost metadata/count/input: %v / %v", output, input)
				}
			}
		})
	}
}

type BridgeSummaryVersioned struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type bridgeInspirationDTO struct {
	BridgeSummaryVersioned
	Title     string `json:"title"`
	ProjectID string `json:"project_id"`
	Completed bool   `json:"completed"`
	Markdown  string `json:"markdown"`
	Preview   string `json:"preview"`
}

type bridgeHistoryPageDTO struct {
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset"`
}

type bridgeHistoryMessageDTO struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Body    string `json:"body"`
	Context any    `json:"context"`
}

func bridgeSummaryWire(t *testing.T, data map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(data) // Already-whitelisted summary, never original DTOs.
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestBridgeReadSummaryTypedAndWireShapeParity(t *testing.T) {
	record := bridgeWorkRecordDTO{ID: "op-1", Kind: "operation", Action: "inspiration.update", Outcome: "succeeded", Title: "中文🙂", Before: map[string]any{"body": "PRIVATE"}}
	wireRecord := map[string]any{
		"id": "op-1", "kind": "operation", "action": "inspiration.update", "outcome": "succeeded", "title": "中文🙂",
		"project_id": "", "object_type": "", "object_id": "", "object_version": int64(0), "occurred_at": "", "error_code": "",
		"before": map[string]any{"body": "PRIVATE"},
	}
	note := bridgeInspirationDTO{BridgeSummaryVersioned: BridgeSummaryVersioned{ID: "ins-1", Version: 7}, Title: "灵感", Completed: true, Markdown: "PRIVATE", Preview: "PRIVATE"}
	wireNote := map[string]any{"id": "ins-1", "version": float64(7), "title": "灵感", "project_id": "", "completed": true, "markdown": "PRIVATE", "preview": "PRIVATE"}
	page := bridgeHistoryPageDTO{Total: 12, HasMore: true, NextOffset: 3}
	wirePage := map[string]any{"total": 12, "has_more": true, "next_offset": 3}
	for _, test := range []struct {
		tool  string
		typed map[string]any
		wire  map[string]any
		input map[string]any
	}{
		{"work_records_query", map[string]any{"items": []bridgeWorkRecordDTO{record}, "total": 5}, map[string]any{"items": []any{wireRecord}, "total": float64(5)}, map[string]any{"offset": json.Number("2")}},
		{"work_record_get", map[string]any{"record": &record, "children": []bridgeWorkRecordDTO{}, "links": []bridgeWorkLinkDTO{{ID: "ins-1"}}}, map[string]any{"record": wireRecord, "children": []any{}, "links": []any{map[string]any{"id": "ins-1"}}}, nil},
		{"inspiration_list", map[string]any{"items": []bridgeInspirationDTO{note}, "total": 1}, map[string]any{"items": []map[string]any{wireNote}, "total": json.Number("1")}, nil},
		{"inspiration_get", map[string]any{"inspiration": &note}, map[string]any{"inspiration": wireNote}, nil},
		{"work_history_read", map[string]any{"link": bridgeWorkLinkDTO{Kind: "assistant", ID: "was-1"}, "messages": []bridgeHistoryMessageDTO{{ID: "message-1", Body: "PRIVATE"}}, "page": page}, map[string]any{"link": map[string]any{"id": "was-1", "kind": "assistant", "deleted": false}, "messages": []any{map[string]any{"id": "message-1", "body": "PRIVATE"}}, "page": wirePage}, nil},
	} {
		t.Run(test.tool, func(t *testing.T) {
			typed := bridgeSummaryWire(t, bridgeToolResultSummary(test.tool, test.input, test.typed))
			wire := bridgeSummaryWire(t, bridgeToolResultSummary(test.tool, test.input, test.wire))
			if typed != wire {
				t.Fatalf("typed/wire metadata differ: %s != %s", typed, wire)
			}
			if strings.Contains(typed, "PRIVATE") {
				t.Fatal("summary copied private context or Markdown")
			}
		})
	}
}

func TestBridgeReadSummaryTypedSafeFieldCoverage(t *testing.T) {
	record := bridgeWorkRecordDTO{
		ID: "op-1", Kind: "operation", Action: "inspiration.update", Outcome: "failed", Title: "正常名称",
		ProjectID: "project-1", ObjectType: "inspiration", ObjectID: "ins-1", ObjectVersion: 8,
		OccurredAt: "2026-10-24T06:00:00+08:00", ErrorCode: "version_conflict",
		Refs: map[string]any{"context": "PRIVATE"}, Before: map[string]any{"markdown": "PRIVATE"},
		After: map[string]any{"body": "PRIVATE"}, Unknown: bridgePoisonBody{},
	}
	metadata := map[string]any{
		"id": record.ID, "kind": record.Kind, "action": record.Action, "outcome": record.Outcome, "title": record.Title,
		"project_id": record.ProjectID, "object_type": record.ObjectType, "object_id": record.ObjectID,
		"object_version": record.ObjectVersion, "occurred_at": record.OccurredAt, "error_code": record.ErrorCode,
	}
	get := bridgeToolResultSummary("work_record_get", nil, map[string]any{
		"record": &record, "children": []*bridgeWorkRecordDTO{&record}, "links": [1]bridgeWorkLinkDTO{{ID: "ins-1", URL: "/PRIVATE"}},
	})
	wantGet := map[string]any{"record": metadata, "children_count": 1, "links_count": 1}
	if !reflect.DeepEqual(get, wantGet) {
		t.Fatalf("typed work record metadata mismatch: got %v, want %v", get, wantGet)
	}
	query := bridgeToolResultSummary("work_records_query", nil, map[string]any{"items": []*bridgeWorkRecordDTO{&record}, "total": 1})
	wantQuery := map[string]any{
		"total": 1, "item_count": 1, "has_more": false, "next_offset": int64(1),
		"items": []map[string]any{{"id": record.ID, "kind": record.Kind, "action": record.Action,
			"title": record.Title, "project_id": record.ProjectID, "outcome": record.Outcome, "occurred_at": record.OccurredAt}},
	}
	if !reflect.DeepEqual(query, wantQuery) {
		t.Fatalf("typed work query metadata mismatch: got %v, want %v", query, wantQuery)
	}
	note := bridgeInspirationDTO{
		BridgeSummaryVersioned: BridgeSummaryVersioned{ID: "ins-1", Version: 8},
		ProjectID:              "project-1", Title: "灵感标题", Completed: true, Markdown: "PRIVATE", Preview: "PRIVATE",
	}
	inspiration := bridgeToolResultSummary("inspiration_get", nil, map[string]any{"inspiration": &note, "url": "/PRIVATE"})
	wantInspiration := map[string]any{"inspiration": map[string]any{
		"id": note.ID, "version": note.Version, "project_id": note.ProjectID, "title": note.Title, "completed": true,
	}}
	if !reflect.DeepEqual(inspiration, wantInspiration) {
		t.Fatalf("typed inspiration metadata mismatch: got %v, want %v", inspiration, wantInspiration)
	}
	list := bridgeToolResultSummary("inspiration_list", nil, map[string]any{"items": [1]*bridgeInspirationDTO{&note}, "total": 1})
	wantList := map[string]any{
		"total": 1, "item_count": 1, "has_more": false, "next_offset": int64(1),
		"items": []map[string]any{{"id": note.ID, "project_id": note.ProjectID, "title": note.Title, "completed": true}},
	}
	if !reflect.DeepEqual(list, wantList) {
		t.Fatalf("typed inspiration list metadata mismatch: got %v, want %v", list, wantList)
	}
	for _, summary := range []map[string]any{get, query, inspiration, list} {
		if strings.Contains(bridgeSummaryWire(t, summary), "PRIVATE") {
			t.Fatal("metadata whitelist copied refs, original history, Markdown, preview or URL")
		}
	}
}

func TestBridgeReadSummaryTypedHistoryKinds(t *testing.T) {
	type historyObject struct {
		BridgeSummaryVersioned
		Status      string `json:"status"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Body        any    `json:"body"`
		Context     any    `json:"context"`
	}
	for _, kind := range []string{"issue", "node", "message", "comment", "assistant"} {
		t.Run(kind, func(t *testing.T) {
			link := bridgeWorkLinkDTO{Kind: kind, ID: kind + "-1", Title: "PRIVATE", URL: "/PRIVATE"}
			object := historyObject{BridgeSummaryVersioned: BridgeSummaryVersioned{ID: kind + "-1", Version: 4}, Status: "completed",
				Title: "PRIVATE", Description: "PRIVATE", Body: bridgePoisonBody{}, Context: bridgePoisonBody{}}
			data := map[string]any{"link": &link}
			want := map[string]any{"link": map[string]any{"id": link.ID, "kind": kind, "deleted": false}}
			switch kind {
			case "issue", "message", "comment":
				data[kind] = &object
				want[kind] = map[string]any{"id": object.ID, "status": object.Status}
			}
			if kind == "issue" || kind == "node" || kind == "assistant" {
				data["page"] = &bridgeHistoryPageDTO{Total: 8, HasMore: true, NextOffset: 3}
				want["total"], want["has_more"], want["next_offset"] = 8, true, 3
				if kind == "issue" {
					data["comments"] = []*historyObject{&object}
					want["comment_count"] = 1
				} else {
					data["messages"] = []*historyObject{&object}
					want["messages_count"] = 1
				}
			}
			summary := bridgeToolResultSummary("work_history_read", nil, data)
			if !reflect.DeepEqual(summary, want) {
				t.Fatalf("typed %s history metadata mismatch: got %v, want %v", kind, summary, want)
			}
			if strings.Contains(bridgeSummaryWire(t, summary), "PRIVATE") {
				t.Fatal("history summary copied original title, body, description, context or URL")
			}
		})
	}
}

func TestBridgeReadSummaryBoundsPaginationAndUnsupportedShapes(t *testing.T) {
	items := make([]bridgeWorkRecordDTO, 100)
	for index := range items {
		items[index] = bridgeWorkRecordDTO{ID: fmt.Sprintf("op-%d", index), Kind: "operation", Action: "issue.update", Outcome: "succeeded", Title: strings.Repeat("中🙂\n", 300)}
	}
	summary := bridgeToolResultSummary("work_records_query", map[string]any{"offset": json.Number("100")}, map[string]any{"items": items, "total": 250})
	preview, _ := summary["items"].([]map[string]any)
	if summary["item_count"] != 100 || len(preview) != 5 || preview[4]["id"] != "op-4" || summary["has_more"] != true || summary["next_offset"] != int64(200) {
		t.Fatalf("count/page/preview not bounded or inaccurate: %v", summary)
	}
	for _, entry := range preview {
		title, _ := entry["title"].(string)
		if !utf8.ValidString(title) || utf8.RuneCountInString(title) != 201 || !strings.HasSuffix(title, "…") || strings.Contains(title, "\n") {
			t.Fatal("UTF-8 metadata title was not safely flattened/truncated")
		}
	}
	if wire := bridgeSummaryWire(t, summary); len(wire) > 6<<10 {
		t.Fatalf("bounded summary exceeded 6 KiB: %d", len(wire))
	}
	for _, items := range []any{[]bridgeWorkRecordDTO{}, []bridgeWorkRecordDTO(nil), []any{}, []map[string]any{}} {
		for _, offset := range []int{0, 200} {
			empty := bridgeToolResultSummary("work_records_query", map[string]any{"offset": offset}, map[string]any{"items": items, "total": 0})
			if empty["item_count"] != 0 || empty["total"] != 0 || empty["has_more"] != false || empty["next_offset"] != int64(0) || empty["items"] != nil {
				t.Fatalf("empty page was not represented faithfully: %v", empty)
			}
		}
	}
	for _, data := range []map[string]any{
		nil, {}, {"unknown": "PRIVATE"}, {"items": []string{"PRIVATE"}},
		{"record": map[string]any{"body": "PRIVATE", "title": map[string]any{"secret": "PRIVATE"}, "id": "/private/PRIVATE"}},
		{"total": map[string]any{"secret": "PRIVATE"}}, {"total": -1}, {"total": math.NaN()},
	} {
		for _, tool := range []string{"work_records_query", "work_record_get"} {
			unknown := bridgeToolResultSummary(tool, nil, data)
			if len(unknown) != 1 || unknown["summary_available"] != false {
				t.Fatalf("unsupported shape invented a result: %v", unknown)
			}
		}
	}
	deleted := bridgeToolResultSummary("work_history_read", nil, map[string]any{"unavailable": true, "link": bridgeWorkLinkDTO{ID: "message-removed", Kind: "message", Deleted: true}})
	if deleted["unavailable"] != true || deleted["link"].(map[string]any)["deleted"] != true {
		t.Fatal("unavailable history was misreported as a successful read")
	}
}

type bridgePoisonNumber int

func (bridgePoisonNumber) MarshalJSON() ([]byte, error) {
	panic("must not invoke original DTO scalar MarshalJSON")
}

type bridgePoisonBody struct{}

func (bridgePoisonBody) MarshalJSON() ([]byte, error) {
	panic("must not marshal original history/body")
}

func TestBridgeReadSummaryDoesNotSerializeOriginalDTOs(t *testing.T) {
	data := map[string]any{
		"record":  bridgeWorkRecordDTO{ID: "op-1", Unknown: bridgePoisonBody{}, Before: map[string]any{"body": bridgePoisonBody{}}},
		"total":   bridgePoisonNumber(3),
		"context": bridgePoisonBody{},
	}
	get := bridgeToolResultSummary("work_record_get", nil, data)
	query := bridgeToolResultSummary("work_records_query", nil, data)
	if !strings.Contains(bridgeSummaryWire(t, get), "op-1") || bridgeSummaryWire(t, query) != `{"total":3}` {
		t.Fatal("safe fields were not copied without serializing DTOs")
	}
}

func TestBridgeReadInputSummaryDoesNotRetainSearchFiltersOrUnknownFields(t *testing.T) {
	input := map[string]any{
		"search": "PRIVATE", "filter": map[string]any{"conditions": []any{map[string]any{"value": "PRIVATE"}}},
		"offset": json.Number("3"), "limit": json.Number("20"), "include_changes": true,
		"from": "2026-10-23T01:00:00Z", "until": "PRIVATE", "context": "PRIVATE", "provider_command": "PRIVATE",
	}
	summary := bridgeInputSummary("work_records_query", input)
	if summary["search_provided"] != true || summary["filter_provided"] != true || summary["offset"] != int64(3) || summary["limit"] != int64(20) || summary["include_changes"] != true || summary["from"] != "2026-10-23T01:00:00Z" || strings.Contains(bridgeSummaryWire(t, summary), "PRIVATE") {
		t.Fatalf("query metadata was inaccurate or copied context: %v", summary)
	}
	history := bridgeInputSummary("work_history_read", map[string]any{"id": "message-1", "kind": "message", "offset": 1, "limit": 2, "body": "PRIVATE"})
	if history["id"] != "message-1" || history["kind"] != "message" || history["offset"] != 1 || history["limit"] != 2 || len(history) != 4 {
		t.Fatalf("history input metadata was lost: %v", history)
	}
}

func TestBridgeToolSummaryKeepsIssueAndMultiAgentSummariesUnchanged(t *testing.T) {
	data := map[string]any{
		"title": "public issue", "description": "public description", "comment_id": "comment-1", "version": 2,
		"comments": []any{map[string]any{"body": "public comment", "author_agent_id": "agent-1"}},
		"agents":   []any{map[string]any{"agent_id": "agent-2", "name": "Lumi", "participating": true, "is_self": false}},
		"context":  []any{map[string]any{"seq": 3, "tool": "issue_get", "status": "completed"}},
	}
	for _, tool := range fileBridgeTools {
		if summary := bridgeToolResultSummary(tool, nil, data); !reflect.DeepEqual(summary, bridgeResultSummary(data)) {
			t.Fatalf("Issue/multi-agent summary changed for %s", tool)
		}
	}
	history := bridgeToolResultSummary("work_history_read", nil, data)
	if history["comment_count"] != 1 || strings.Contains(bridgeSummaryWire(t, history), "public comment") || history["description"] != nil || history["context"] != nil {
		t.Fatal("history tool accidentally reused Issue context/body excerpts")
	}
}

func TestReadSummaryLumiIssueRetainsOrdinaryAgentScope(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	resolved := 0
	engine.SetMCPResolver(func(_ context.Context, project string) (map[string]json.RawMessage, error) {
		resolved++
		if project != "project" {
			t.Error("ordinary Lumi Issue did not resolve its project MCP scope")
		}
		return map[string]json.RawMessage{"custom": json.RawMessage(`{"type":"remote","url":"https://example.test/mcp"}`)}, nil
	})
	started := false
	engine.RegisterTestAdapter("fake", &fakeAdapter{onStart: func(request adapter.RunRequest) {
		started = true
		if request.ReadOnly || request.DisableTools || request.IssueID != "issue" ||
			strings.HasPrefix(request.CWD, filepath.Join(engine.workspaceRoot, "home")+string(filepath.Separator)) ||
			!strings.Contains(request.Prompt, "issue_comment_create") || strings.Contains(request.Prompt, "work_records_query") {
			t.Error("Lumi identity incorrectly selected the global read-only assistant scope")
		}
		if assertExactRunIPC(t, request) {
			for _, tool := range fileBridgeTools {
				if info, err := os.Stat(filepath.Join(request.ToolRequestDir, tool)); err != nil || !info.IsDir() {
					t.Error("ordinary Lumi Issue lost its collaboration tool directory")
				}
			}
		}
	}})
	runIPCBridgeCommand(t, engine, nodeprotocol.StartRunPayload{
		RunID: "lumi-ordinary-issue", AgentID: "lumi", IssueID: "issue", ProjectID: "project",
		IssueAgentInstanceID: "instance", IssueAgentGenerationID: "generation",
		Provider: "fake", LeaseEpoch: 1, Prompt: "work on the assigned Issue",
	})
	if !started || resolved != 1 {
		t.Fatalf("ordinary Lumi Issue did not reach its normal Provider/MCP scope: started=%t resolved=%d", started, resolved)
	}
	called := false
	broker := NewToolBroker(toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		called = true
		if request.Tool != "issue_comment_create" || request.AgentID != "lumi" || request.IssueID != "issue" || request.InstanceID != "instance" {
			t.Error("ordinary Lumi Issue write lost its bound collaboration scope")
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comment_id": "comment-1"}}, nil
	}), "lumi-issue-write", "issue", "instance", "lumi")
	bridge, err := NewFileBridge(t.TempDir(), "lumi-issue-write", broker, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := bridge.dispatch(t.Context(), "issue_comment_create", map[string]any{"body": "public summary"})
	if err != nil || !called || data["comment_id"] != "comment-1" {
		t.Fatalf("ordinary Lumi Issue write was incorrectly blocked: called=%t err=%v", called, err)
	}
}

func TestFileBridgeReadSummaryErrorsStayFailedWithoutPersistingPrivateError(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	for _, failure := range []error{errors.New("PRIVATE /private/path provider --secret PRIVATE"), context.Canceled, context.DeadlineExceeded} {
		broker := NewToolBroker(bridgeReadSummaryCaller{err: failure}, "run-read-error", "", "", "lumi")
		broker.workAssistant = true
		bridge, err := NewFileBridge(t.TempDir(), "run-read-error", broker, nil)
		if err != nil {
			t.Fatal(err)
		}
		var failed bool
		bridge.SetToolEventReporter(func(_, _, status string, payload map[string]any) {
			if strings.Contains(bridgeSummaryWire(t, payload), "PRIVATE") || payload["output"] != nil {
				t.Fatal("private error was persisted or a failed call acquired a success result")
			}
			if status == "failed" {
				failed = true
				if payload["error"] != bridgeEventError("work_record_get", failure) {
					t.Fatal("failed event did not preserve safe error category")
				}
			}
		})
		path := writeBridgeTestRequest(t, bridge, "work_record_get", "request.json", `{"id":"op-1"}`)
		bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "work_record_get", name: "request.json"})
		response := readBridgeTestResponse(t, path)
		if !failed || response.OK || response.ErrorCode != "tool_failed" || !strings.Contains(response.Message, failure.Error()) {
			t.Fatal("failed call was not faithfully reported in its transient response")
		}
	}
}

func TestReadSummaryRuntimePersistsMetadataButOnlyIPCContainsHistoryAndMarkdown(t *testing.T) {
	engine := newIPCBridgeRuntime(t)
	engine.SetToolCaller(toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		var data map[string]any
		switch request.Tool {
		case "work_history_read":
			data = map[string]any{"link": bridgeWorkLinkDTO{Kind: "assistant", ID: "was-1"}, "messages": []bridgeHistoryMessageDTO{{ID: "message-1", Body: "PRIVATE_HISTORY_SENTINEL", Context: "PRIVATE_CONTEXT_SENTINEL"}}, "page": bridgeHistoryPageDTO{Total: 1, NextOffset: 1}}
		case "inspiration_get":
			data = map[string]any{"inspiration": bridgeInspirationDTO{BridgeSummaryVersioned: BridgeSummaryVersioned{ID: "ins-1", Version: 4}, Title: "saved title", Markdown: "PRIVATE_MARKDOWN_SENTINEL", Preview: "PRIVATE_PREVIEW_SENTINEL"}}
		default:
			return nodeprotocol.NodeRequestResult{}, errors.New("unexpected tool")
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: data}, nil
	}))
	var ipc string
	provider := &bridgeRunAdapter{fakeAdapter: &fakeAdapter{}, run: func(ctx context.Context, request adapter.RunRequest, _ adapter.Sink) (adapter.NativeSession, error) {
		ipc = request.ToolRequestDir
		for _, test := range []struct{ tool, input, marker string }{
			{"work_history_read", `{"kind":"assistant","id":"was-1"}`, "PRIVATE_HISTORY_SENTINEL"},
			{"inspiration_get", `{"id":"ins-1"}`, "PRIVATE_MARKDOWN_SENTINEL"},
		} {
			path := filepath.Join(ipc, test.tool, "request.json")
			if err := os.WriteFile(path, []byte(test.input), 0o600); err != nil {
				return adapter.NativeSession{}, err
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			deadline := time.NewTimer(5 * time.Second)
			for {
				payload, err := os.ReadFile(path + ".response.json")
				if err == nil {
					ticker.Stop()
					deadline.Stop()
					if !strings.Contains(string(payload), test.marker) {
						return adapter.NativeSession{}, errors.New("assistant IPC lost original saved content")
					}
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					ticker.Stop()
					deadline.Stop()
					return adapter.NativeSession{}, ctx.Err()
				case <-deadline.C:
					ticker.Stop()
					return adapter.NativeSession{}, errors.New("assistant IPC response timed out")
				}
			}
		}
		return adapter.NativeSession{ID: "native_1", Provider: "fake"}, nil
	}}
	engine.RegisterTestAdapter("fake", provider)
	runIPCBridgeCommand(t, engine, nodeprotocol.StartRunPayload{RunID: "read-summary-private-ipc", AssistantSessionID: "summary-session", AgentID: "lumi", Provider: "fake", LeaseEpoch: 1, ReadOnly: true, Prompt: "read saved context"})
	if _, err := os.Stat(ipc); !os.IsNotExist(err) {
		t.Fatal("assistant original history/Markdown IPC survived completion")
	}
	events, err := engine.store.UnackedEvents(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, event := range events {
		if strings.Contains(bridgeSummaryWire(t, event.Payload), "PRIVATE_") {
			t.Fatal("Runtime durable event copied raw history, context, Markdown or preview")
		}
		if event.Type == "provider.tool" && event.Payload["status"] == "completed" {
			completed++
			if event.Payload["output"] == nil {
				t.Fatal("Runtime persisted a completed tool without metadata")
			}
		}
	}
	if completed != 2 {
		t.Fatalf("durable completed read count=%d, want 2", completed)
	}
}
