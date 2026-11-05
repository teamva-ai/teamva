package embedded

import (
	"context"
	"encoding/json"
)

// SetMCPResolver connects host-owned MCP management without placing any MCP
// configuration or credentials into runtime commands, events or the database.
func (c *Controller) SetMCPResolver(resolver func(context.Context, string) (map[string]json.RawMessage, error)) {
	c.runtime.SetMCPResolver(resolver)
}
