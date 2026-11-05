package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type recordingToolCaller struct {
	requests     []nodeprotocol.NodeRequest
	responseData map[string]any
}

func (c *recordingToolCaller) SendToolRequest(
	_ context.Context, request nodeprotocol.NodeRequest,
) (nodeprotocol.NodeRequestResult, error) {
	c.requests = append(c.requests, request)
	data := c.responseData
	if data == nil {
		data = map[string]any{"accepted": true}
	}
	return nodeprotocol.NodeRequestResult{
		RequestID: request.RequestID, OK: true, Data: data,
	}, nil
}

func TestToolBrokerScopesRequestsAndUsesOperationIdempotencyKeys(t *testing.T) {
	caller := &recordingToolCaller{}
	broker := NewToolBroker(caller, "run-1", "issue-1", "instance-1", "agent-1")
	if _, err := broker.IssueCommentCreate(t.Context(), "first", []string{"agent-2"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.IssueCommentCreate(t.Context(), "second", nil, true); err != nil {
		t.Fatal(err)
	}
	if len(caller.requests) != 2 {
		t.Fatalf("requests=%+v", caller.requests)
	}
	for _, request := range caller.requests {
		if request.RunID != "run-1" || request.IssueID != "issue-1" ||
			request.InstanceID != "instance-1" || request.AgentID != "agent-1" {
			t.Fatalf("request scope=%+v", request)
		}
		if request.RequestID == "" || request.IdempotencyKey != request.RequestID {
			t.Fatalf("request idempotency=%+v", request)
		}
	}
	if caller.requests[0].IdempotencyKey == caller.requests[1].IdempotencyKey {
		t.Fatalf("independent operations reused idempotency key: %+v", caller.requests)
	}
	if caller.requests[0].Payload["append_new"] != false || caller.requests[1].Payload["append_new"] != true {
		t.Fatalf("comment append mode was lost: %+v", caller.requests)
	}
}

func TestToolBrokerFileIdempotencyBindsRunToolAndFilenameWithoutPayload(t *testing.T) {
	caller := &recordingToolCaller{}
	broker := NewToolBroker(caller, "run-1", "issue", "instance", "agent")
	ctx := context.WithValue(t.Context(), fileToolRequestKey{}, "request.json")
	if _, err := broker.IssueCommentCreate(ctx, "first", nil, true); err != nil {
		t.Fatal(err)
	}
	// A changed payload must reach the server with the same key so its
	// idempotency conflict check wins instead of committing a second mutation.
	if _, err := broker.IssueCommentCreate(ctx, "changed", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.IssueCommentUpdateOwn(ctx, "comment", 1, "changed"); err != nil {
		t.Fatal(err)
	}
	otherFile := context.WithValue(t.Context(), fileToolRequestKey{}, "other.json")
	if _, err := broker.IssueCommentCreate(otherFile, "first", nil, true); err != nil {
		t.Fatal(err)
	}
	otherRun := NewToolBroker(caller, "run-2", "issue", "instance", "agent")
	if _, err := otherRun.IssueCommentCreate(ctx, "first", nil, true); err != nil {
		t.Fatal(err)
	}
	first := caller.requests[0]
	if first.IdempotencyKey != caller.requests[1].IdempotencyKey || first.RequestID == caller.requests[1].RequestID {
		t.Fatalf("file retry lost operation identity or reused delivery identity: %+v", caller.requests[:2])
	}
	for _, request := range caller.requests[2:] {
		if request.IdempotencyKey == first.IdempotencyKey {
			t.Fatalf("file operation escaped its scope: %+v", request)
		}
	}
}

func TestToolBrokerRejectsCanceledRequestBeforeCallingAndBoundsDeadline(t *testing.T) {
	caller := &recordingToolCaller{}
	broker := NewToolBroker(caller, "run", "issue", "instance", "agent")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := broker.IssueGet(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tool error=%v", err)
	}
	if len(caller.requests) != 0 {
		t.Fatal("canceled tool reached the caller")
	}
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel = context.WithDeadline(t.Context(), deadline)
	defer cancel()
	if _, err := broker.IssueGet(ctx); err != nil {
		t.Fatal(err)
	}
	if got := caller.requests[0].DeadlineUnix; got != deadline.Unix() {
		t.Fatalf("tool deadline=%d, want run deadline=%d", got, deadline.Unix())
	}
}

var _ ToolCaller = (*recordingToolCaller)(nil)
