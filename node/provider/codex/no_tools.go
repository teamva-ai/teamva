package codex

import "errors"

// 0.163.0 exposes no public app-server ToolPolicy allow/deny-all option.
// Its TUI temporary_structured_request feature recipe is not a complete native
// capability boundary: model_info.tool_mode overrides feature flags, clock is
// registered from model experimental_supported_tools, and omitted advertised
// tools are still executable through the native registry. Never fake NoTools
// with approvalPolicy=never, readonly, empty dynamicTools, or hidden tool specs.
var errCodexNoToolsUnsupported = errors.New("codex cannot enforce DisableTools with the supported native app-server contract; refusing this Run before any native turn (use a Provider with a verified all-tools denial boundary)")
