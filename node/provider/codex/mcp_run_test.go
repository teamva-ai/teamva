package codex

import (
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestRunConfigAddsOnlyTheRunScopedIssueMCP(t *testing.T) {
	plain := codexRunConfig(adapter.RunRequest{})
	if _, exists := plain["mcp_servers"]; exists {
		t.Fatal("run without MCP URL gained a server")
	}
	config := codexRunConfig(adapter.RunRequest{MCPURL: "http://127.0.0.1:9999/mcp", MCPToken: "token"})
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers missing: %+v", config)
	}
	server, ok := servers["teamva-issue"].(map[string]any)
	if !ok || server["url"] != "http://127.0.0.1:9999/mcp" || server["experimental_use_rmcp_client"] != true || server["default_tools_approval_mode"] != "approve" {
		t.Fatalf("bad remote server: %+v", server)
	}
	headers, _ := server["http_headers"].(map[string]any)
	if headers["Authorization"] != "Bearer token" {
		t.Fatalf("bad headers: %+v", headers)
	}
}
