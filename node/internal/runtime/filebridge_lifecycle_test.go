package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type toolCallerFunc func(context.Context, nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error)

func (call toolCallerFunc) SendToolRequest(ctx context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
	return call(ctx, request)
}

type bridgeTestResponse struct {
	RequestID string         `json:"request_id"`
	OK        bool           `json:"ok"`
	ErrorCode string         `json:"error_code"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
}

func writeBridgeTestRequest(t *testing.T, bridge *FileBridge, tool, name, payload string) string {
	t.Helper()
	path := filepath.Join(bridge.root, tool, name)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readBridgeTestResponse(t *testing.T, path string) bridgeTestResponse {
	t.Helper()
	payload, err := os.ReadFile(path + ".response.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > maxFileBridgeResponseBytes {
		t.Fatalf("response exceeds bridge budget: %d bytes", len(payload))
	}
	var response bridgeTestResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestFileBridgeCommentAppendNewStrictBooleanAndDefaultPlaceholderReuse(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	for _, test := range []struct {
		name, option string
		appendNew    bool
		invalid      bool
	}{
		{name: "omitted"},
		{name: "false", option: `,"append_new":false`},
		{name: "true", option: `,"append_new":true`, appendNew: true},
		{name: "null", option: `,"append_new":null`, invalid: true},
		{name: "string", option: `,"append_new":"true"`, invalid: true},
		{name: "number", option: `,"append_new":1`, invalid: true},
		{name: "array", option: `,"append_new":[]`, invalid: true},
		{name: "object", option: `,"append_new":{}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			caller := toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
				calls++
				if request.Tool != "issue_comment_create" || request.Payload["append_new"] != test.appendNew || request.Payload["body"] != "summary" {
					t.Errorf("comment payload=%+v", request)
				}
				mentioned, _ := request.Payload["mentioned_agent_ids"].([]string)
				if len(mentioned) != 1 || mentioned[0] != "agent-other" {
					t.Errorf("structured mentions were lost: %+v", request.Payload)
				}
				commentID := "working-placeholder"
				if test.appendNew {
					commentID = "new-comment"
				}
				return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comment_id": commentID, "version": 2}}, nil
			})
			bridge, err := NewFileBridge(t.TempDir(), "run-append",
				NewToolBroker(caller, "run-append", "issue", "instance", "agent"), nil)
			if err != nil {
				t.Fatal(err)
			}
			path := writeBridgeTestRequest(t, bridge, "issue_comment_create", "request.json",
				`{"body":"summary","mentioned_agent_ids":["agent-other"]`+test.option+`}`)
			bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "issue_comment_create", name: "request.json"})
			response := readBridgeTestResponse(t, path)
			if test.invalid {
				if calls != 0 || response.OK || !strings.Contains(response.Message, "append_new must be a boolean") {
					t.Fatalf("invalid append mode reached broker: calls=%d response=%+v", calls, response)
				}
				return
			}
			wantComment := "working-placeholder"
			if test.appendNew {
				wantComment = "new-comment"
			}
			if calls != 1 || !response.OK || response.Data["comment_id"] != wantComment {
				t.Fatalf("comment identity=%+v calls=%d", response, calls)
			}
		})
	}
}

func TestFileBridgeExactCommentReadPreservesScopeAndStrictID(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable")
	}
	calls := 0
	caller := toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		calls++
		if request.Tool != "issue_get" || request.Payload["comment_id"] != "comment-target" || request.RunID != "run-reader" {
			t.Errorf("exact comment request changed: %+v", request)
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comments": []any{}}}, nil
	})
	bridge, err := NewFileBridge(t.TempDir(), "run-reader", NewToolBroker(caller, "run-reader", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := writeBridgeTestRequest(t, bridge, "issue_get", "exact.json", `{"comment_id":"comment-target"}`)
	bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "issue_get", name: "exact.json"})
	if result := readBridgeTestResponse(t, path); !result.OK || calls != 1 {
		t.Fatalf("response=%+v calls=%d", result, calls)
	}
	path = writeBridgeTestRequest(t, bridge, "issue_get", "invalid.json", `{"comment_id":true}`)
	bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "issue_get", name: "invalid.json"})
	if result := readBridgeTestResponse(t, path); result.OK || calls != 1 {
		t.Fatalf("invalid read reached broker: %+v calls=%d", result, calls)
	}
}

func TestFileBridgeSameFileRetryReusesOperationKeyAndDoesNotDuplicateMutation(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	calls, mutations := 0, 0
	var operationKey, firstDeliveryID string
	caller := toolCallerFunc(func(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		calls++
		if operationKey == "" {
			operationKey, firstDeliveryID = request.IdempotencyKey, request.RequestID
			mutations++
		} else if request.IdempotencyKey != operationKey || request.RequestID == firstDeliveryID {
			t.Errorf("retry identities were not preserved: %+v", request)
		}
		if calls == 1 {
			// The server committed, but the delivery lost its acknowledgement.
			return nodeprotocol.NodeRequestResult{}, errors.New("acknowledgement lost")
		}
		return nodeprotocol.NodeRequestResult{OK: true, Data: map[string]any{"comment_id": "single-comment"}}, nil
	})
	bridge, err := NewFileBridge(t.TempDir(), "run-retry",
		NewToolBroker(caller, "run-retry", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := writeBridgeTestRequest(t, bridge, "issue_comment_create", "request.json", `{"body":"summary","append_new":true}`)
	request := fileBridgeRequest{tool: "issue_comment_create", name: "request.json"}
	bridge.handleRequest(t.Context(), request)
	if response := readBridgeTestResponse(t, path); response.OK {
		t.Fatalf("first delivery unexpectedly succeeded: %+v", response)
	}
	// A completed response is not re-dispatched, even if handleRequest is called.
	bridge.handleRequest(t.Context(), request)
	if calls != 1 {
		t.Fatalf("existing response was re-submitted: %d calls", calls)
	}
	if err := os.Remove(path + ".response.json"); err != nil {
		t.Fatal(err)
	}
	requests, err := bridge.scanRequests(t.Context(), make(map[string]struct{}))
	if err != nil || len(requests) != 1 {
		t.Fatalf("retry scan=%+v err=%v", requests, err)
	}
	bridge.handleRequest(t.Context(), requests[0])
	if response := readBridgeTestResponse(t, path); !response.OK || response.Data["comment_id"] != "single-comment" {
		t.Fatalf("retry response=%+v", response)
	}
	if calls != 2 || mutations != 1 {
		t.Fatalf("calls=%d mutations=%d", calls, mutations)
	}
}

func TestFileBridgeOversizedAndUnencodableResultsAlwaysWriteBoundedError(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	for _, test := range []struct {
		name, code string
		data       map[string]any
	}{
		{name: "budget", code: "response_too_large", data: map[string]any{"context": strings.Repeat("x", maxFileBridgeResponseBytes)}},
		{name: "encoding", code: "response_encoding_failed", data: map[string]any{"invalid": make(chan struct{})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller := &recordingToolCaller{responseData: test.data}
			bridge, err := NewFileBridge(t.TempDir(), "run-response",
				NewToolBroker(caller, "run-response", "issue", "instance", "agent"), nil)
			if err != nil {
				t.Fatal(err)
			}
			var statuses []string
			bridge.SetToolEventReporter(func(_, _, status string, _ map[string]any) { statuses = append(statuses, status) })
			path := writeBridgeTestRequest(t, bridge, "issue_agent_context", "request.json", `{"agent_id":"other","cursor":"page-cursor"}`)
			started := time.Now()
			bridge.handleRequest(t.Context(), fileBridgeRequest{tool: "issue_agent_context", name: "request.json"})
			response := readBridgeTestResponse(t, path)
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("bounded error took %s instead of unblocking the waiter", elapsed)
			}
			if response.OK || response.ErrorCode != test.code || response.Message == "" || response.Data != nil {
				t.Fatalf("bounded response=%+v", response)
			}
			if len(statuses) != 2 || statuses[1] != "failed" {
				t.Fatalf("failed response reported completed: %v", statuses)
			}
			if len(caller.requests) != 1 || caller.requests[0].Payload["cursor"] != "page-cursor" {
				t.Fatalf("context cursor was lost: %+v", caller.requests)
			}
		})
	}
}

func TestFileBridgeCancellationAbortsInFlightAndSkipsQueuedRequests(t *testing.T) {
	if !fileBridgePlatformSupported() {
		t.Skip("safe file bridge is unavailable on this platform")
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	calls := 0
	caller := toolCallerFunc(func(ctx context.Context, _ nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
		calls++
		close(started)
		<-ctx.Done()
		close(canceled)
		return nodeprotocol.NodeRequestResult{}, ctx.Err()
	})
	bridge, err := NewFileBridge(t.TempDir(), "run-cancel",
		NewToolBroker(caller, "run-cancel", "issue", "instance", "agent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := writeBridgeTestRequest(t, bridge, "issue_comment_create", "first.json", `{"body":"summary"}`)
	queuedPath := writeBridgeTestRequest(t, bridge, "issue_comment_create", "queued.json", `{"body":"must not submit"}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.handleRequest(ctx, fileBridgeRequest{tool: "issue_comment_create", name: "first.json"})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool call did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("tool call did not inherit Run cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge request did not stop promptly")
	}
	if response := readBridgeTestResponse(t, path); response.OK || !strings.Contains(response.Message, "canceled") {
		t.Fatalf("canceled response=%+v", response)
	}
	bridge.handleRequest(ctx, fileBridgeRequest{tool: "issue_comment_create", name: "queued.json"})
	if calls != 1 {
		t.Fatalf("canceled queued request reached ToolBroker: %d calls", calls)
	}
	if response := readBridgeTestResponse(t, queuedPath); response.OK || response.ErrorCode != "request_cancelled" {
		t.Fatalf("queued response=%+v", response)
	}
}
