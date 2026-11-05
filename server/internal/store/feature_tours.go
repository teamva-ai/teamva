package store

import (
	"context"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

type FeatureTourRepository interface {
	GetFeatureTours(context.Context, string) (domain.FeatureTours, error)
	MarkFeatureTourSeen(context.Context, string, string) (domain.FeatureTours, error)
}
