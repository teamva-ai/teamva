package runtime

import (
	"context"
	"encoding/json"
)

// SetMCPResolver installs a private, per-project lookup. Its result bypasses the
// durable command inbox entirely. Replacement is safe while Runs are active;
// only subsequent Run preparation observes the new resolver/configuration.
func (r *Runtime) SetMCPResolver(resolver func(context.Context, string) (map[string]json.RawMessage, error)) {
	r.mcpMu.Lock()
	r.mcpResolver = resolver
	r.mcpMu.Unlock()
}

func (r *Runtime) resolveMCP(ctx context.Context, projectID string, readOnly bool) (map[string]json.RawMessage, error) {
	if readOnly {
		return nil, nil // Lumi must not even invoke a project MCP resolver.
	}
	r.mcpMu.RLock()
	resolver := r.mcpResolver
	r.mcpMu.RUnlock()
	if resolver == nil {
		return nil, nil
	}
	servers, err := resolver(ctx, projectID)
	if err != nil {
		// Resolver errors can contain secret values/paths. Never persist them in
		// the command result or return them through the public runtime boundary.
		return nil, codedError("mcp_configuration_unavailable", "无法读取项目 MCP 配置")
	}
	result := make(map[string]json.RawMessage, len(servers))
	for name, raw := range servers {
		if name != "open-computer-use" {
			result[name] = append(json.RawMessage(nil), raw...)
		}
	}
	return result, nil
}
