package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestWriteRunMCPConfigAddsAndRemovesIssueServer(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfig(dir, provider.InstanceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := writeRunMCPConfig(dir, "http://127.0.0.1:7777/mcp", "run-token"); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		raw, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	config := read()
	mcp, _ := config["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	server, _ := servers["teamva-issue"].(map[string]any)
	headers, _ := server["headers"].(map[string]any)
	if server["type"] != "remote" || server["url"] != "http://127.0.0.1:7777/mcp" || headers["Authorization"] != "Bearer run-token" {
		t.Fatalf("bad run MCP server: %+v", config)
	}
	if err := writeRunMCPConfig(dir, "", ""); err != nil {
		t.Fatal(err)
	}
	config = read()
	if _, exists := config["mcp"]; exists {
		t.Fatalf("stale run MCP server retained: %+v", config)
	}
}
