package codex

import (
	"encoding/json"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

// Fixed native contract: protocol/src/mcp_approval_meta.rs and core/src/
// mcp_tool_call.rs::build_mcp_tool_approval_elicitation_request (0.163.0).
// This is NOT a general form/authentication/device-verification handler.
type mcpToolApproval struct {
	ThreadID   string                     `json:"threadId"`
	TurnID     string                     `json:"turnId"`
	ServerName string                     `json:"serverName"`
	Mode       string                     `json:"mode"`
	Meta       map[string]json.RawMessage `json:"_meta"`
	Schema     struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	} `json:"requestedSchema"`
}

func nativeMCPToolApproval(message rpcMessage) (mcpToolApproval, bool) {
	var request mcpToolApproval
	if message.Method != "mcpServer/elicitation/request" || json.Unmarshal(message.Params, &request) != nil {
		return request, false
	}
	var kind string
	if json.Unmarshal(request.Meta["codex_approval_kind"], &kind) != nil || kind != "mcp_tool_call" {
		return request, false
	}
	return request, request.Mode == "form" && request.ThreadID != "" && request.ServerName != "" && request.Schema.Type == "object" && request.Schema.Properties != nil && len(request.Schema.Properties) == 0 && len(request.Schema.Required) == 0
}

func mcpToolApprovalResult(request mcpToolApproval, decision adapter.ApprovalDecision) map[string]any {
	result := map[string]any{"action": "decline", "content": nil, "_meta": nil}
	if decision.Decision != "approved" {
		return result
	}
	result["action"] = "accept"
	// Only persist an explicitly requested decision when the native request
	// itself offers that scope. Never turn a one-shot UI approval into autoapprove.
	if decision.Scope == "session" || decision.Scope == "always" {
		var offered any
		if json.Unmarshal(request.Meta["persist"], &offered) == nil {
			allowed := offered == decision.Scope
			if scopes, ok := offered.([]any); ok {
				for _, scope := range scopes {
					if scope == decision.Scope {
						allowed = true
					}
				}
			}
			if allowed {
				result["_meta"] = map[string]any{"persist": decision.Scope}
			}
		}
	}
	return result
}
