package embedded

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	"github.com/teamva-ai/teamva/node/internal/runtime"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestWorkPlanFacadeFailsClosedWithoutCommandsOrEvents(t *testing.T) {
	root := t.TempDir()
	store, err := localstore.Open(t.Context(), filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine := runtime.New("computer", store, nil, filepath.Join(root, "workspaces"), nil)
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	controller := &Controller{runtime: engine, store: store}
	request := nodeprotocol.WorkPlanRequest{ExecutionID: "draft", Provider: "codex", Input: "Prepare a draft only", Agents: []nodeprotocol.WorkPlanAgent{{ID: "a", Name: "Agent"}}}
	result, err := controller.GenerateWorkPlan(t.Context(), request)
	if err == nil || err.Error() != "no_tools_unsupported" || len(result.WorkItems) != 0 {
		t.Fatalf("facade did not preserve strict refusal: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := controller.GenerateWorkPlan(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("facade lost cancellation")
	}
	events, err := controller.UnackedEvents(t.Context(), 100)
	if err != nil || len(events) != 0 {
		t.Fatal("draft entered durable runtime events")
	}
}
