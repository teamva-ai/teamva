package store

import (
	"context"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// These are required capabilities of the current product, not optional legacy
// store adapters. Missing implementations are compile errors, not runtime UI errors.
type WorkHistoryRepository interface {
	QueryWorkRecords(context.Context, string, domain.WorkQuery) (domain.WorkPage, error)
	GetWorkRecord(context.Context, string, string) (domain.WorkRecord, error)
	WorkRecordChildren(context.Context, string, string) ([]domain.WorkRecord, error)
	WorkRecordLinks(context.Context, string, domain.WorkRecord) ([]WorkLink, error)
	ResolveWorkLink(context.Context, string, string, string) WorkLink
	AppendWorkRecord(context.Context, string, domain.WorkRecord) error
	FinishWorkOperation(context.Context, string, domain.WorkRecord) error
	ClearWorkRecords(context.Context, string, domain.WorkQuery) (int64, error)
	SaveWorkFilter(context.Context, string, domain.WorkFilter) (domain.WorkFilter, error)
	ListWorkFilters(context.Context, string) ([]domain.WorkFilter, error)
	DeleteWorkFilter(context.Context, string, string) error
}

type AssistantRepository interface {
	EnsureGeneralAgent(context.Context, string) (domain.Agent, error)
	ListAssistantSessions(context.Context, string, domain.AssistantScope) ([]domain.AssistantSession, error)
	CreateAssistantSession(context.Context, string, string, domain.AssistantScope) (domain.AssistantSession, error)
	AssistantMessages(context.Context, string, string) ([]domain.AssistantMessage, error)
	SendAssistantMessage(context.Context, string, string, string, domain.AssistantMessageRequest) (domain.Run, error)
}

type OnboardingRepository interface {
	Onboarding(context.Context, string) (domain.Onboarding, error)
	SetOnboardingDismissed(context.Context, string, bool) error
}

type InspirationRepository interface {
	GetInspiration(context.Context, string, string) (domain.Inspiration, error)
	CreateInspiration(context.Context, string, string, domain.Inspiration) (domain.Inspiration, error)
	UpdateInspiration(context.Context, string, string, int64, domain.InspirationPatch) (domain.Inspiration, error)
	DeleteInspiration(context.Context, string, string, int64) error
	ListInspirations(context.Context, string, domain.InspirationQuery) (domain.InspirationPage, error)
	InspirationGroups(context.Context, string, domain.InspirationQuery) ([]domain.InspirationGroup, error)
}

var _ CoreLocalRepository = (*SQLite)(nil)
