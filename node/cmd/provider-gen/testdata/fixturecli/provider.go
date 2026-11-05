package fixturecli

import (
	"context"
	"errors"
	"github.com/teamva-ai/teamva/node/provider"
	"time"
)

type client struct {
	provider.UnsupportedOperations
	instance bool
}

func (*client) Name() string { return "fixturecli" }
func (*client) Probe(context.Context) (provider.CapabilityManifest, error) {
	return provider.CapabilityManifest{Provider: "fixturecli", ProviderVersion: "fixture-1", Available: true, Authenticated: true, Streaming: true, CapabilityVersion: 1}, nil
}
func (c *client) Start(ctx context.Context, request provider.RunRequest, sink provider.Sink) (provider.NativeSession, error) {
	if !c.instance {
		return provider.NativeSession{}, errors.New("probe client must never execute a run")
	}
	if request.Prompt == "wait for cancellation" {
		if err := sink(ctx, provider.Event{Type: "output.delta", Payload: map[string]any{"text": "waiting"}, OccurredAt: time.Now().UTC()}); err != nil {
			return provider.NativeSession{}, err
		}
		<-ctx.Done()
		return provider.NativeSession{}, ctx.Err()
	}
	for _, event := range []provider.Event{
		{Type: "output.delta", Payload: map[string]any{"text": "extension works"}},
		{Type: "run.finished", Payload: map[string]any{"status": "succeeded"}},
	} {
		event.RunID = request.RunID
		event.OccurredAt = time.Now().UTC()
		if err := sink(ctx, event); err != nil {
			return provider.NativeSession{}, err
		}
	}
	return provider.NativeSession{ID: "session-" + request.RunID, Provider: "fixturecli"}, nil
}
func Definition(metadata provider.Metadata) provider.Definition {
	return provider.Definition{Metadata: metadata,
		NewProbe:    func(string) (provider.Adapter, error) { return &client{}, nil },
		NewInstance: func(provider.InstanceOptions) (provider.Adapter, error) { return &client{instance: true}, nil },
		Configure:   func(string, provider.InstanceConfig) error { return nil },
	}
}
