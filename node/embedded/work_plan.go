package embedded

import (
	"context"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// GenerateWorkPlan bypasses durable commands, Runs and all business tool paths.
func (c *Controller) GenerateWorkPlan(ctx context.Context, request nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
	return c.runtime.GenerateWorkPlan(ctx, request)
}
