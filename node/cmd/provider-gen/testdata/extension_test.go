package embedded_test

import (
	"encoding/json"
	"github.com/teamva-ai/teamva/node/embedded"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveredProviderRuns(t *testing.T) {
	definition, found := catalog.Lookup("fixturecli")
	if !found || definition.Metadata.Supports("read_only") {
		t.Fatal("new provider metadata was not discovered accurately")
	}
	paths := map[string]string{}
	for _, item := range catalog.Definitions() {
		paths[item.Metadata.ID] = filepath.Join(t.TempDir(), "not-installed")
	}
	controller, err := embedded.New(t.Context(), embedded.Config{ComputerID: "computer_fixture", DataDir: t.TempDir(), ProviderPaths: paths})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	manifests, err := controller.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, item := range manifests {
		if item.Provider == "fixturecli" && item.Available && item.Authenticated {
			found = true
		}
	}
	if !found {
		t.Fatal("Controller did not expose the new provider")
	}
	for _, id := range []string{"first", "second"} {
		payload, err := json.Marshal(nodeprotocol.StartRunPayload{RunID: id, Provider: "fixturecli", ProjectID: "project_fixture", LeaseEpoch: 1, Prompt: "test", IssueAgentInstanceID: "fixture_instance", IssueAgentGenerationID: "fixture_generation"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command_" + id, Name: "run.start", ComputerID: "computer_fixture", SentAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute), Payload: payload})
		if err != nil || result.Status != "completed" {
			t.Fatalf("new provider run failed: status=%s err=%v", result.Status, err)
		}
	}
	events, err := controller.UnackedEvents(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	outputs, terminals := 0, 0
	for _, event := range events {
		if event.Type == "output.delta" && event.Payload["text"] == "extension works" {
			outputs++
		}
		if event.Type == "run.finished" && event.Payload["status"] == "succeeded" {
			terminals++
		}
	}
	if outputs != 2 || terminals != 2 {
		t.Fatalf("durable extension events: outputs=%d terminals=%d", outputs, terminals)
	}
	unsupported, err := json.Marshal(nodeprotocol.StartRunPayload{RunID: "readonly", Provider: "fixturecli", ProjectID: "project_fixture", LeaseEpoch: 1, Prompt: "test", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command_readonly", Name: "run.start", ComputerID: "computer_fixture", SentAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute), Payload: unsupported})
	if err == nil || result.ErrorCode != "capability_unsupported" {
		t.Fatalf("unsupported read-only mode was not rejected: code=%s", result.ErrorCode)
	}
	waitPayload, err := json.Marshal(nodeprotocol.StartRunPayload{RunID: "waiting", Provider: "fixturecli", ProjectID: "project_fixture", LeaseEpoch: 1, Prompt: "wait for cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command_waiting", Name: "run.start", ComputerID: "computer_fixture", SentAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute), Payload: waitPayload})
	}()
	started := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		events, err := controller.UnackedEvents(t.Context(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.RunID == "waiting" && event.Type == "output.delta" {
				started = true
			}
		}
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatal("minimal provider did not start")
	}
	stopPayload, _ := json.Marshal(nodeprotocol.StopRunPayload{RunID: "waiting", Provider: "fixturecli"})
	result, err = controller.Handle(t.Context(), nodeprotocol.CommandEnvelope{ProtocolVersion: nodeprotocol.Version, MessageID: "command_stop", Name: "run.stop", ComputerID: "computer_fixture", SentAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute), Payload: stopPayload})
	if err != nil || result.Status != "completed" {
		t.Fatalf("context-only cancellation failed: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider ignored cancellation")
	}
	if _, err := controller.RecycleIdleProvider(t.Context(), "fixturecli"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}
