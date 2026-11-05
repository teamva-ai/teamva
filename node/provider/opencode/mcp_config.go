package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// writeRunMCPConfig adds or removes the run-scoped Issue MCP server in the
// instance's managed config. The OpenCode server reads this file at startup,
// and prepareSandboxLocked restarts it when the URL changes.
func writeRunMCPConfig(configDir, mcpURL, token string) error {
	path := filepath.Join(configDir, "opencode", "opencode.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && mcpURL == "" {
		return nil
	}
	if err != nil {
		return errors.New("read opencode managed config failed")
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return errors.New("parse opencode managed config failed")
	}
	mcp, _ := config["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		config["mcp"] = mcp
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		mcp["servers"] = servers
	}
	if mcpURL == "" || token == "" {
		delete(servers, "teamva-issue")
	} else {
		servers["teamva-issue"] = map[string]any{
			"type": "remote", "url": mcpURL,
			"headers": map[string]string{"Authorization": "Bearer " + token},
		}
	}
	if len(servers) == 0 {
		delete(mcp, "servers")
	}
	if len(mcp) == 0 {
		delete(config, "mcp")
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return errors.New("encode opencode managed config failed")
	}
	return writePrivateNativeFile(filepath.Join(configDir, "opencode"), "opencode.json", encoded)
}
