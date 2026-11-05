package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type mcpHostCaller struct {
	requests []nodeprotocol.NodeRequest
}

func (c *mcpHostCaller) SendToolRequest(_ context.Context, request nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error) {
	c.requests = append(c.requests, request)
	return nodeprotocol.NodeRequestResult{RequestID: request.RequestID, OK: true, Data: map[string]any{"title": "hello"}}, nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestRunMCPHostServesIssueToolsWithRunIdentity(t *testing.T) {
	caller := &mcpHostCaller{}
	broker := NewToolBroker(caller, "run-1", "issue-1", "instance-1", "agent-1")
	host, err := newRunMCPHost(t.Context(), broker, false)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             host.URL(),
		HTTPClient:           &http.Client{Transport: bearerTransport{token: host.Token(), base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["issue_get"] || !names["issue_comment_create"] {
		t.Fatalf("missing Issue tools: %+v", tools.Tools)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "issue_get", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("issue_get failed: %+v", result)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	if text == nil || !strings.Contains(text.Text, "hello") {
		t.Fatalf("unexpected tool result: %+v", result.Content)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("tool calls = %d", len(caller.requests))
	}
	request := caller.requests[0]
	if request.RunID != "run-1" || request.IssueID != "issue-1" || request.AgentID != "agent-1" || request.InstanceID != "instance-1" {
		t.Fatalf("run identity lost: %+v", request)
	}
	if request.Tool != "issue_get" {
		t.Fatalf("tool = %s", request.Tool)
	}
}

func TestRunMCPHostRejectsMissingTokenAndHidesWritesWhenReadOnly(t *testing.T) {
	broker := NewToolBroker(&mcpHostCaller{}, "run-2", "issue-2", "instance-2", "agent-2")
	host, err := newRunMCPHost(t.Context(), broker, true)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	response, err := http.Get(host.URL())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             host.URL(),
		HTTPClient:           &http.Client{Transport: bearerTransport{token: host.Token(), base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "issue_comment_create" || tool.Name == "issue_comment_update_own" {
			t.Fatalf("read-only run exposed write tool %s", tool.Name)
		}
	}
}

func TestRunMCPHostRejectsMalformedWorkItems(t *testing.T) {
	caller := &mcpHostCaller{}
	broker := NewToolBroker(caller, "run-3", "issue-3", "instance-3", "agent-3")
	host, err := newRunMCPHost(t.Context(), broker, false)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             host.URL(),
		HTTPClient:           &http.Client{Transport: bearerTransport{token: host.Token(), base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "issue_comment_create",
		Arguments: map[string]any{
			"body":       "summary",
			"work_items": "not-an-array",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(caller.requests) != 0 {
		t.Fatalf("malformed work_items reached the broker: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	t.Logf("%s", encoded)
}
