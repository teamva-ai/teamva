package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

type latchedProbeRuntime struct {
	*blockingRuntime
	probeStarted chan struct{}
	probeExited  chan struct{}
	probeOnce    sync.Once
	probeMu      sync.Mutex
	probeCalls   int
}

func (f *latchedProbeRuntime) Probe(ctx context.Context) ([]nodeprotocol.AgentManifest, error) {
	f.probeMu.Lock()
	f.probeCalls++
	f.probeMu.Unlock()
	f.probeOnce.Do(func() { close(f.probeStarted) })
	<-ctx.Done()
	close(f.probeExited)
	return nil, ctx.Err()
}

func TestSlowInitialProbeDoesNotBlockEventsSchedulesOrStop(t *testing.T) {
	ctx := t.Context()
	repository, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// A previous successful probe is durable; the next hung periodic/startup
	// probe must not prevent this configured Provider from doing useful work.
	service, err := repository.UpsertAgentService(ctx, user.ID, domain.AgentService{
		ComputerID: computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := &localToolCaller{tools: toolservice.New(repository), computer: computer}
	runtime := &latchedProbeRuntime{blockingRuntime: newBlockingRuntime(), probeStarted: make(chan struct{}), probeExited: make(chan struct{})}
	local, err := newLocalWithController(ctx, repository, runtime, caller, user, computer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	select {
	case <-runtime.probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("asynchronous initial probe never started")
	}
	manualCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := local.Probe(manualCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("manual probe waiting for the same discovery gate: %v", err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "slow probe"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repository.CreateAgent(ctx, user.ID, domain.Agent{
		ComputerID: computer.ID, AgentServiceID: service.ID, Name: "worker", Status: domain.AgentActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAgentRevision(ctx, user.ID, domain.AgentRevision{AgentID: agent.ID, AgentServiceID: service.ID}, nil); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Second)
	if _, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{
		ProjectID: project.ID, AgentID: agent.ID, Name: "due during probe", ScheduleKind: "once",
		ScheduleExpression: due.Format(time.RFC3339), Timezone: "UTC", NextRunAt: &due,
		Prompt: []domain.ContentBlock{{Type: "text", Text: "work"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once",
	}); err != nil {
		t.Fatal(err)
	}
	local.NotifyIssueActivation("")
	var runID string
	select {
	case runID = <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow probe blocked schedule/run.start dispatch")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		run, err := repository.GetRun(ctx, user.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == domain.RunRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow probe blocked run.started event import")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := repository.StopRun(ctx, user.ID, runID, "force"); err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, 2*time.Second)
	defer stopCancel()
	if err := local.StopLocalRun(stopCtx, runID); err != nil {
		t.Fatalf("slow probe blocked durable run.stop: %v", err)
	}
	runtime.probeMu.Lock()
	calls := runtime.probeCalls
	runtime.probeMu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent manual/background probes = %d, want 1", calls)
	}
	if err := local.Close(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.probeExited:
	default:
		t.Fatal("Close returned before its cancelled probe finished")
	}
}
