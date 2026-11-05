package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

type localLeaseRenewal struct {
	Command store.LocalCommand
	Now     time.Time
	At      time.Time
	Err     error
}

// The real SQLite repository remains the authority for claims and fencing.
// These channels observe committed renewals; they do not grant ownership.
type localLeaseTraceStore struct {
	*store.SQLite
	claims   chan store.LocalCommand
	renewals chan localLeaseRenewal
	// Set before starting the dispatcher. A test can deliberately let one
	// lease expire without depending on CPU pressure or a lucky scheduler.
	beforeRenew func(context.Context, string, string, time.Time, time.Duration) (time.Time, error)
}

func newLocalLeaseTraceStore(repository *store.SQLite) *localLeaseTraceStore {
	return &localLeaseTraceStore{SQLite: repository, claims: make(chan store.LocalCommand, 64), renewals: make(chan localLeaseRenewal, 64)}
}

func (s *localLeaseTraceStore) ClaimLocalCommand(ctx context.Context, now time.Time, lease time.Duration, excluded ...string) (store.LocalCommand, bool, error) {
	item, ok, err := s.SQLite.ClaimLocalCommand(ctx, now, lease, excluded...)
	if ok && err == nil {
		select {
		case s.claims <- item:
		default:
		}
	}
	return item, ok, err
}

func (s *localLeaseTraceStore) RenewLocalCommandLease(ctx context.Context, id, token string, now time.Time, lease time.Duration) error {
	var err error
	if s.beforeRenew != nil {
		now, err = s.beforeRenew(ctx, id, token, now, lease)
	}
	if err == nil {
		err = s.SQLite.RenewLocalCommandLease(ctx, id, token, now, lease)
	}
	item, readErr := s.SQLite.GetLocalCommand(context.Background(), id)
	if err == nil {
		err = readErr
	}
	select {
	case s.renewals <- localLeaseRenewal{Command: item, Now: now, At: time.Now(), Err: err}:
	default:
	}
	return err
}

func localLeaseRunFixture(t *testing.T, repository *store.SQLite, user domain.User, computer domain.Computer, service domain.AgentService, key string) domain.Run {
	t.Helper()
	project, err := repository.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: key})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: service.ID, Name: key, Status: domain.AgentActive,
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repository.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: key})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := repository.EnsureIssueAgentInstance(t.Context(), user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := repository.CreateRun(t.Context(), user.ID, issue.ID, issue.Version, key, domain.Run{
		AgentID: agent.ID, IssueAgentInstanceID: instance.ID,
		Input: []domain.ContentBlock{{Type: "text", Text: "lease test fixture"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// Only the ticker notification is injected. Claim/expiry checks use real
// SQLite and real time. Scheduler delay can only strengthen this test: the
// submitted tick is deliberately older than an already expired lease.
func TestLocalCommandLeaseRejectsStaleTickAfterExpiry(t *testing.T) {
	repository, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repository.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated",
	})
	if err != nil {
		t.Fatal(err)
	}
	localLeaseRunFixture(t, repository, user, computer, service, "stale-tick")
	lease := 10 * time.Millisecond
	claim, ok, err := repository.ClaimLocalCommand(t.Context(), time.Now(), lease)
	if err != nil || !ok || claim.ClaimedAt == nil || claim.LeaseUntil == nil {
		t.Fatalf("claim: ok=%t err=%v", ok, err)
	}
	staleTick := claim.ClaimedAt.Add(lease / 3)
	<-time.After(time.Until(*claim.LeaseUntil) + time.Millisecond)
	if !time.Now().After(*claim.LeaseUntil) {
		t.Fatal("fixture did not cross the original lease")
	}
	traced := newLocalLeaseTraceStore(repository)
	local := &Local{repository: traced}
	ticks := make(chan time.Time, 1)
	ticks <- staleTick
	stop := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { done <- local.renewOutboxCommandLease(ctx, claim, lease, ticks, stop) }()
	var observation localLeaseRenewal
	select {
	case observation = <-traced.renewals:
	case <-ctx.Done():
		t.Fatal("renewal did not observe the stale ticker notification")
	}
	close(stop)
	if err := <-done; err == nil || observation.Err == nil {
		t.Fatalf("expired claim was renewed using a historical tick: tick=%s now_argument=%s expired=%s", staleTick, observation.Now, claim.LeaseUntil)
	}
	if observation.Now.Before(*claim.LeaseUntil) || observation.Command.LeaseUntil == nil || !observation.Command.LeaseUntil.Equal(*claim.LeaseUntil) {
		t.Fatal("late renewal revived or extended an expired claim")
	}
	replacement, ok, err := repository.ClaimLocalCommand(t.Context(), time.Now(), time.Minute)
	if err != nil || !ok || replacement.ID != claim.ID || replacement.ClaimToken == claim.ClaimToken {
		t.Fatalf("replacement claim was not uniquely fenced: ok=%t err=%v", ok, err)
	}
	if err := repository.RenewLocalCommandLease(t.Context(), claim.ID, claim.ClaimToken, time.Now(), time.Minute); err == nil {
		t.Fatal("old token renewed the replacement claim")
	}
	if err := repository.CompleteLocalCommand(t.Context(), claim.ID, claim.ClaimToken, json.RawMessage(`{}`)); err == nil {
		t.Fatal("old token completed the replacement claim")
	}
	if err := repository.RetryLocalCommand(t.Context(), claim.ID, claim.ClaimToken, "stale", time.Now()); err == nil {
		t.Fatal("old token changed the replacement claim to pending")
	}
	if err := repository.MarkLocalCommandUncertain(t.Context(), claim.ID, claim.ClaimToken, "stale"); err == nil {
		t.Fatal("old token invalidated the replacement claim")
	}
	if err := repository.FailLocalCommand(t.Context(), claim.ID, claim.ClaimToken, "stale", nil); err == nil {
		t.Fatal("old token failed the replacement claim")
	}
	if err := repository.CompleteLocalCommand(t.Context(), replacement.ID, replacement.ClaimToken, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

// This fixture models the runtime command inbox, not a real Provider: deliveries
// can repeat, but only the first stable MessageID executes or produces events.
type leaseReplayRuntime struct {
	mu          sync.Mutex
	deliveries  []nodeprotocol.CommandEnvelope
	executions  int
	active      int
	maxActive   int
	result      nodeprotocol.CommandResult
	firstDone   chan struct{}
	closeOnce   sync.Once
	started     chan struct{}
	firstExited chan struct{}
}

func newLeaseReplayRuntime() *leaseReplayRuntime {
	return &leaseReplayRuntime{firstDone: make(chan struct{}), started: make(chan struct{}), firstExited: make(chan struct{})}
}

func (r *leaseReplayRuntime) Probe(context.Context) ([]nodeprotocol.AgentManifest, error) {
	return []nodeprotocol.AgentManifest{{Provider: "opencode", Available: true, Authenticated: true, Capabilities: map[string]any{}, CapabilityVersion: 1}}, nil
}

func (r *leaseReplayRuntime) Handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	if command.Name != "run.start" {
		return nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed"}, nil
	}
	r.mu.Lock()
	r.deliveries = append(r.deliveries, command)
	if len(r.deliveries) > 1 {
		original := r.deliveries[0]
		r.mu.Unlock()
		if original.MessageID != command.MessageID || original.IdempotencyKey != command.IdempotencyKey || string(original.Payload) != string(command.Payload) {
			return nodeprotocol.CommandResult{}, fmt.Errorf("redelivery changed durable command identity")
		}
		select {
		case <-r.firstExited:
			r.mu.Lock()
			result := r.result
			r.mu.Unlock()
			return result, nil
		case <-ctx.Done():
			return nodeprotocol.CommandResult{}, ctx.Err()
		}
	}
	r.executions++
	r.active++
	r.maxActive = max(r.maxActive, r.active)
	r.mu.Unlock()
	close(r.started)
	select {
	case <-ctx.Done():
	case <-r.firstDone:
	}
	r.mu.Lock()
	r.active--
	r.result = nodeprotocol.CommandResult{CommandID: command.MessageID, Status: "completed", Result: json.RawMessage(`{"executed_once":true}`)}
	result := r.result
	r.mu.Unlock()
	close(r.firstExited)
	return result, nil
}

func (r *leaseReplayRuntime) UnackedEvents(context.Context, int) ([]nodeprotocol.RunEvent, error) {
	return nil, nil
}
func (r *leaseReplayRuntime) AckEvents(context.Context, []nodeprotocol.RunEventCursor) error {
	return nil
}
func (r *leaseReplayRuntime) Close() error {
	r.closeOnce.Do(func() { close(r.firstDone) })
	return nil
}

func TestLocalDispatcherExpiredLeaseRedeliversStableCommandWithoutReexecution(t *testing.T) {
	repository, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	traced := newLocalLeaseTraceStore(repository)
	var expireOnce sync.Once
	renewGate := make(chan struct{})
	traced.beforeRenew = func(ctx context.Context, id, _ string, now time.Time, _ time.Duration) (time.Time, error) {
		var waitErr error
		expireOnce.Do(func() {
			select {
			case <-renewGate:
			case <-ctx.Done():
				waitErr = ctx.Err()
				return
			}
			claim, err := repository.GetLocalCommand(ctx, id)
			if err != nil || claim.LeaseUntil == nil {
				waitErr = fmt.Errorf("read deliberately expired lease: %w", err)
				return
			}
			// Deliberately miss this lease, irrespective of scheduler load. The
			// actual real-time SQLite guard, not a fake error, rejects renewal.
			timer := time.NewTimer(time.Until(*claim.LeaseUntil) + time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				waitErr = ctx.Err()
			}
			now = time.Now()
		})
		return now, waitErr
	}
	controller := newLeaseReplayRuntime()
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	local, err := newLocalWithController(t.Context(), traced, controller, caller, user, computer)
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	local.commandLeaseNanos.Store(int64(100 * time.Millisecond))
	t.Cleanup(func() { _ = local.Close(context.Background()); _ = repository.Close() })
	services, err := waitForAgentServices(t, repository, user.ID, computer.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services: count=%d err=%v", len(services), err)
	}
	run := localLeaseRunFixture(t, repository, user, computer, services[0], "expiry-replay")
	local.NotifyIssueActivation(run.IssueID)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-controller.started:
	case <-ctx.Done():
		t.Fatal("first execution never started")
	}
	// Only the first claim is intentionally expired. Recovery gets normal
	// scheduler headroom, so this test proves one forced redelivery, not how
	// many extra short leases a heavily instrumented machine might miss.
	local.commandLeaseNanos.Store(int64(2 * time.Second))
	close(renewGate)
	var original store.LocalCommand
	select {
	case original = <-traced.claims:
	case <-ctx.Done():
		t.Fatal("initial claim not observed")
	}
	select {
	case renewal := <-traced.renewals:
		if renewal.Err == nil || original.LeaseUntil == nil || !renewal.Now.After(*original.LeaseUntil) {
			t.Fatal("fixture did not reject a genuinely expired renewal")
		}
	case <-ctx.Done():
		t.Fatal("expired renewal not observed")
	}
	select {
	case <-controller.firstExited:
	case <-ctx.Done():
		t.Fatal("losing the lease did not cancel the original Handle")
	}
	var replacement store.LocalCommand
	select {
	case replacement = <-traced.claims:
	case <-ctx.Done():
		t.Fatal("uncertain delivery was not recovered")
	}
	if replacement.ID != original.ID || replacement.ClaimToken == original.ClaimToken || replacement.Attempts != 2 {
		t.Fatal("redelivery did not retain identity and replace the ownership token")
	}
	if err := repository.CompleteLocalCommand(ctx, original.ID, original.ClaimToken, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expired owner completed the replacement claim")
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		command, err := repository.GetLocalCommand(ctx, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		if command.Status == store.LocalCommandCompleted {
			if command.Attempts != 2 || string(command.Result) != `{"executed_once":true}` {
				t.Fatal("recovery did not commit the original durable result")
			}
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("recovered result was not committed")
		}
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.deliveries) != 2 || controller.executions != 1 || controller.maxActive != 1 || controller.active != 0 {
		t.Fatalf("delivery/execution boundary: deliveries=%d executions=%d max_active=%d active=%d", len(controller.deliveries), controller.executions, controller.maxActive, controller.active)
	}
}
