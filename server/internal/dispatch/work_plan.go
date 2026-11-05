package dispatch

import (
	"context"
	"errors"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type workPlanGenerator interface {
	GenerateWorkPlan(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error)
}

func (l *Local) GenerateWorkPlan(ctx context.Context, request nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
	generator, ok := l.controller.(workPlanGenerator)
	if !ok {
		return nodeprotocol.WorkPlanResult{}, errors.New("work_plan_unsupported")
	}
	request.ExecutionID = randomID("workplan")
	return generator.GenerateWorkPlan(ctx, request)
}
