package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/store"
)

type autoTitleTestRepository struct {
	mu          sync.Mutex
	active      bool
	queue       []store.AutoTitleClaim
	claims      int
	checks      int
	completed   []string
	failures    []string
	checkActive func(int) bool
}

func (r *autoTitleTestRepository) ClaimAutoTitle(_ context.Context, _ time.Time, lease time.Duration) (store.AutoTitleClaim, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lease != 3*time.Minute {
		return store.AutoTitleClaim{}, false, errors.New("invalid test lease")
	}
	r.claims++
	if len(r.queue) == 0 {
		return store.AutoTitleClaim{}, false, nil
	}
	claim := r.queue[0]
	r.queue = r.queue[1:]
	return claim, true, nil
}

func (r *autoTitleTestRepository) AutoTitleClaimActive(_ context.Context, _ store.AutoTitleClaim) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks++
	if r.checkActive != nil {
		return r.checkActive(r.checks), nil
	}
	return r.active, nil
}

func (r *autoTitleTestRepository) CompleteAutoTitle(_ context.Context, claim store.AutoTitleClaim, title string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return errors.New("fenced")
	}
	r.completed = append(r.completed, claim.ID+":"+title)
	return nil
}

func (r *autoTitleTestRepository) FailAutoTitle(_ context.Context, _ store.AutoTitleClaim, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, reason)
	return nil
}

type titleGeneratorFunc func(context.Context, nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error)

func (f titleGeneratorFunc) GenerateTitle(ctx context.Context, request nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
	return f(ctx, request)
}

func testAutoTitleClaim() store.AutoTitleClaim {
	return store.AutoTitleClaim{ID: "job", LeaseToken: "lease", UserID: "user", Kind: "issue", EntityID: "issue", Request: nodeprotocol.TitleRequest{ExecutionID: "attempt", Provider: "fake", Seed: "private seed"}}
}

func testTitleLocal() *Local {
	return &Local{wake: make(chan struct{}, 1), issueWaiters: make(map[string]map[chan struct{}]struct{})}
}

func TestAutoTitleCompletionNotifiesIssueAndSanitizesFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repository := &autoTitleTestRepository{active: true}
		local := testTitleLocal()
		notified := make(chan struct{}, 1)
		local.issueWaiters["issue"] = map[chan struct{}]struct{}{notified: {}}
		generator := titleGeneratorFunc(func(ctx context.Context, request nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
			if request != testAutoTitleClaim().Request {
				t.Fatal("worker changed the isolated request")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 90*time.Second || time.Until(deadline) < 89*time.Second {
				t.Fatal("title attempt did not have a 90s timeout")
			}
			if fail {
				return nodeprotocol.TitleResult{}, errors.New("private seed and raw invalid output")
			}
			return nodeprotocol.TitleResult{Title: "生成标题"}, nil
		})
		local.executeAutoTitle(t.Context(), repository, generator, testAutoTitleClaim())
		if fail {
			if len(repository.failures) != 1 || repository.failures[0] != "generation_failed" || len(repository.completed) != 0 {
				t.Fatal("worker persisted a raw provider error")
			}
		} else {
			if len(repository.completed) != 1 || repository.completed[0] != "job:生成标题" || len(repository.failures) != 0 {
				t.Fatal("title was not completed")
			}
			select {
			case <-notified:
			default:
				t.Fatal("completed title did not notify the issue")
			}
		}
	}
}

func TestAutoTitleManualDeleteOrLeaseLossCancelsAndFences(t *testing.T) {
	repository := &autoTitleTestRepository{active: true}
	local := testTitleLocal()
	started := make(chan struct{})
	done := make(chan struct{})
	generator := titleGeneratorFunc(func(ctx context.Context, _ nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
		close(started)
		<-ctx.Done()
		// Even an adapter returning success after cancellation must be fenced.
		return nodeprotocol.TitleResult{Title: "stale"}, nil
	})
	go func() {
		local.executeAutoTitle(t.Context(), repository, generator, testAutoTitleClaim())
		close(done)
	}()
	<-started
	repository.mu.Lock()
	repository.active = false
	repository.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("inactive title claim did not cancel the native attempt")
	}
	if len(repository.completed) != 0 || len(repository.failures) != 0 {
		t.Fatal("stale attempt wrote after ownership was revoked")
	}
}

func TestAutoTitleFinalClaimRecheckFencesFastReturn(t *testing.T) {
	repository := &autoTitleTestRepository{active: true, checkActive: func(check int) bool { return check == 1 }}
	local := testTitleLocal()
	generator := titleGeneratorFunc(func(context.Context, nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
		return nodeprotocol.TitleResult{Title: "stale"}, nil
	})
	local.executeAutoTitle(t.Context(), repository, generator, testAutoTitleClaim())
	if len(repository.completed) != 0 || len(repository.failures) != 0 || repository.checks != 2 {
		t.Fatal("fast attempt bypassed the final ownership fence")
	}
}

func TestAutoTitleShutdownWaitsForFacadeRelease(t *testing.T) {
	repository := &autoTitleTestRepository{active: true}
	local := testTitleLocal()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, cancelled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	generator := titleGeneratorFunc(func(ctx context.Context, _ nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nodeprotocol.TitleResult{}, ctx.Err()
	})
	go func() { local.executeAutoTitle(ctx, repository, generator, testAutoTitleClaim()); close(done) }()
	<-started
	cancel()
	<-cancelled
	select {
	case <-done:
		t.Fatal("worker abandoned facade/provider cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if len(repository.completed) != 0 || len(repository.failures) != 1 || repository.failures[0] != "cancelled" {
		t.Fatal("shutdown did not release the active claim with a fixed reason")
	}
}

func TestAutoTitleWorkerSerialAndIndependent(t *testing.T) {
	first, second := testAutoTitleClaim(), testAutoTitleClaim()
	second.ID = "second"
	repository := &autoTitleTestRepository{active: true, queue: []store.AutoTitleClaim{first, second}}
	local := testTitleLocal()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, release, secondStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	generator := titleGeneratorFunc(func(context.Context, nodeprotocol.TitleRequest) (nodeprotocol.TitleResult, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
		} else {
			close(secondStarted)
			cancel()
		}
		return nodeprotocol.TitleResult{Title: "title"}, nil
	})
	local.wg.Add(1)
	go local.runAutoTitles(ctx, repository, generator)
	<-started
	// A title provider does not own the main dispatch wake channel.
	local.NotifyIssueActivation("other")
	select {
	case <-local.wake:
	default:
		t.Fatal("title attempt blocked dispatch notifications")
	}
	select {
	case <-secondStarted:
		t.Fatal("title attempts ran concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("serial title worker did not claim its next job")
	}
	local.wg.Wait()
	if calls != 2 || repository.claims != 2 {
		t.Fatal("title worker did not honor serial claims")
	}
}
