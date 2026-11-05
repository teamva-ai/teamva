package embedded

import (
	"context"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// GenerateTitle deliberately bypasses Handle, runtime commands/events and all
// user-facing assistant sessions. The Runtime owns its cancellation and cleanup.
func (c *Controller) GenerateTitle(ctx context.Context, request nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
	return c.runtime.GenerateTitle(ctx, request)
}
