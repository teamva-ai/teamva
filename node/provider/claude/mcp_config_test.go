package claude

import (
	"encoding/json"
	"testing"
)

func TestClaudeMCPConfigKeepsFileServerAndAddsRunIssueServer(t *testing.T) {
	raw := claudeMCPConfig(false, "http://127.0.0.1:4321/mcp", "run-token")
	var document struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Name    string            `json:"name"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	if document.MCPServers["teamva"].Type != "sdk" || document.MCPServers["teamva"].Name != "teamva" {
		t.Fatalf("file MCP server lost: %s", raw)
	}
	issue := document.MCPServers["teamva-issue"]
	if issue.Type != "http" || issue.URL != "http://127.0.0.1:4321/mcp" ||
		issue.Headers["Authorization"] != "Bearer run-token" {
		t.Fatalf("run MCP server invalid: %s", raw)
	}

	disabled := claudeMCPConfig(true, "http://127.0.0.1:4321/mcp", "run-token")
	var withoutFiles struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(disabled), &withoutFiles); err != nil {
		t.Fatal(err)
	}
	if _, exists := withoutFiles.MCPServers["teamva"]; exists {
		t.Fatalf("DisableTools still exposed file MCP: %s", disabled)
	}
	if _, exists := withoutFiles.MCPServers["teamva-issue"]; !exists {
		t.Fatalf("run MCP server missing: %s", disabled)
	}
}
