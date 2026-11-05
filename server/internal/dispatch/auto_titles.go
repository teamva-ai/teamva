package dispatch

import (
	"context"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/store"
)

// These optional contracts keep the isolated title path outside the business
// Run/outbox API and do not force unrelated controllers/repositories to change.
type autoTitleRepository interface {
	ClaimAutoTitle(context.Context, time.Time, time.Duration) (store.AutoTitleClaim, bool, error)
	CompleteAutoTitle(context.Context, store.AutoTitleClaim, string) error
	FailAutoTitle(context.Context, store.AutoTitleClaim, string) error
	AutoTitleClaimActive(context.Context, store.AutoTitleClaim) (bool, error)
}

type titleGenerator interface {
	GenerateTitle(context.Context, nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error)
}

const (
	autoTitleLease   = 3 * time.Minute
	autoTitleTimeout = 90 * time.Second
)

func (l *Local) startAutoTitles(ctx context.Context) {
	repository, hasRepository := l.repository.(autoTitleRepository)
	generator, hasGenerator := l.controller.(titleGenerator)
	if !hasRepository || !hasGenerator {
		return
	}
	l.wg.Add(1)
	go l.runAutoTitles(ctx, repository, generator)
}

// One independent serial worker: a slow provider neither blocks the main
// dispatch/stop loop nor creates concurrent title attempts in this process.
func (l *Local) runAutoTitles(ctx context.Context, repository autoTitleRepository, generator titleGenerator) {
	defer l.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		claim, ok, err := repository.ClaimAutoTitle(ctx, time.Now().UTC(), autoTitleLease)
		if err == nil && ok {
			l.executeAutoTitle(ctx, repository, generator, claim)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Local) executeAutoTitle(parent context.Context, repository autoTitleRepository, generator titleGenerator, claim store.AutoTitleClaim) {
	ctx, cancel := context.WithTimeout(parent, autoTitleTimeout)
	defer cancel()
	active, err := repository.AutoTitleClaimActive(ctx, claim)
	if err != nil || !active {
		return
	}
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				active, err := repository.AutoTitleClaimActive(ctx, claim)
				if err != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	// GenerateTitle joins provider shutdown on cancellation. Do not abandon its
	// goroutine or release the claim while a native attempt can still run.
	result, generationErr := generator.GenerateTitle(ctx, claim.Request)
	close(stopMonitor)
	<-monitorDone

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWrite()
	// Recheck after the provider returns, including the final monitor window.
	// The repository also fences Complete/Fail atomically by lease token/epoch.
	active, err = repository.AutoTitleClaimActive(writeCtx, claim)
	if err != nil || !active {
		return
	}
	if ctx.Err() != nil {
		_ = repository.FailAutoTitle(writeCtx, claim, "cancelled")
		return
	}
	if generationErr != nil || result.Title == "" {
		_ = repository.FailAutoTitle(writeCtx, claim, "generation_failed")
		return
	}
	if err := repository.CompleteAutoTitle(writeCtx, claim, result.Title); err != nil {
		_ = repository.FailAutoTitle(writeCtx, claim, "generation_failed")
		return
	}
	if claim.Kind == "issue" {
		l.NotifyIssueActivation(claim.EntityID)
	}
}
