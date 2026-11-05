package dispatch

import (
	"context"
	"errors"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

type notificationStore struct {
	LocalExecutionStore
	lookups map[string]int
	fail    bool
}

func (s *notificationStore) ApplyLocalEvents(_ context.Context, events []nodeprotocol.RunEvent) ([]nodeprotocol.RunEventCursor, error) {
	cursors := make([]nodeprotocol.RunEventCursor, len(events))
	for i, event := range events {
		cursors[i] = nodeprotocol.RunEventCursor{RunID: event.RunID, Sequence: event.Sequence}
	}
	return cursors, nil
}

func (s *notificationStore) LocalRun(_ context.Context, runID string) (domain.Run, error) {
	s.lookups[runID]++
	if s.fail {
		return domain.Run{}, errors.New("lookup unavailable")
	}
	return domain.Run{IssueID: "issue-1"}, nil
}

func TestFlushEventsCoalescesRunNotifications(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lookup failure"}[fail], func(t *testing.T) {
			repository := &notificationStore{lookups: map[string]int{}, fail: fail}
			runtime := &fakeRuntime{}
			for i := range nodeprotocol.MaxRunEventsPerBatch - 1 {
				runtime.events = append(runtime.events, nodeprotocol.RunEvent{RunID: "run-1", Sequence: int64(i + 1)})
			}
			runtime.events = append(runtime.events, nodeprotocol.RunEvent{RunID: "run-2", Sequence: 1})
			local := &Local{repository: repository, controller: runtime, runWaiters: map[string]map[chan struct{}]struct{}{}, issueWaiters: map[string]map[chan struct{}]struct{}{}}
			changed, cancel := local.SubscribeRunEvents("run-1")
			defer cancel()
			runtime.beforeAck = func([]nodeprotocol.RunEventCursor) error {
				if len(repository.lookups) != 0 {
					t.Fatal("notification lookup must not precede ACK")
				}
				return nil
			}
			if err := local.flushEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if repository.lookups["run-1"] != 1 || repository.lookups["run-2"] != 1 {
				t.Fatalf("wanted one lookup per run, got %v", repository.lookups)
			}
			select {
			case <-changed:
			default:
				t.Fatal("lookup failure must not suppress run notifications")
			}
		})
	}
}
