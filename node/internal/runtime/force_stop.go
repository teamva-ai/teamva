package runtime

import (
	"context"
	"errors"
	"time"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

const forceStopInterruptTimeout = 2 * time.Second

// forceStopInterrupt bounds the native acknowledgement, including a broken
// adapter that does not honor ctx. The Run's execution/tool contexts have already
// been cancelled; the sink barrier below is the authority for public settlement.
func forceStopInterrupt(item adapter.Adapter) func(context.Context, string) error {
	return func(ctx context.Context, runID string) error {
		interruptContext, cancel := context.WithTimeout(ctx, forceStopInterruptTimeout)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- item.Interrupt(interruptContext, runID) }()
		select {
		case err := <-done:
			return err
		case <-interruptContext.Done():
			return errors.New("native interrupt acknowledgement timed out; execution was force-cancelled")
		}
	}
}

func (r *Runtime) installForceStopFinalizer(runID string, settled <-chan struct{}, finish func(string) error) bool {
	r.runStartsMu.Lock()
	defer r.runStartsMu.Unlock()
	if registration := r.runStarts[runID]; registration != nil {
		if registration.earlyForceStop {
			return true
		}
		registration.forceFinish = finish
		registration.settled = settled
	}
	return false
}

func (r *Runtime) waitForceStopSettlement(ctx context.Context, runID string) bool {
	r.runStartsMu.Lock()
	registration := r.runStarts[runID]
	var settled <-chan struct{}
	var done <-chan struct{}
	if registration != nil {
		settled, done = registration.settled, registration.done
	}
	r.runStartsMu.Unlock()
	if registration == nil {
		return true
	}
	if settled == nil {
		return false
	} // No Provider sink: server fallback remains the only terminal.
	select {
	case <-settled:
		return true
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Runtime) finishForceStoppedRun(runID, detail string) error {
	r.runStartsMu.Lock()
	registration := r.runStarts[runID]
	var finish func(string) error
	if registration != nil {
		finish = registration.forceFinish
		if finish == nil {
			registration.earlyForceStop = true
		}
	}
	r.runStartsMu.Unlock()
	if finish != nil {
		return finish(detail)
	}
	return nil
}
