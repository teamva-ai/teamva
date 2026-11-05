package dispatch

import (
	"context"
	"errors"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type modelSetupController interface {
	ModelProviderSettings(context.Context, string) (nodeprotocol.ModelProviderSettings, error)
	SaveModelProvider(context.Context, string, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error)
	TestModelProvider(context.Context, string, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error)
}

func (l *Local) ModelProviderSettings(ctx context.Context, id string) (nodeprotocol.ModelProviderSettings, error) {
	controller, ok := l.controller.(modelSetupController)
	if !ok {
		return nodeprotocol.ModelProviderSettings{}, errors.New("model setup unavailable")
	}
	return controller.ModelProviderSettings(ctx, id)
}

func (l *Local) SaveModelProvider(ctx context.Context, id string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error) {
	ctx, release, err := l.beginDiscovery(ctx)
	if err != nil {
		return nodeprotocol.ModelProviderView{}, err
	}
	defer release()
	controller, ok := l.controller.(modelSetupController)
	if !ok {
		return nodeprotocol.ModelProviderView{}, errors.New("model setup unavailable")
	}
	view, err := controller.SaveModelProvider(ctx, id, input)
	if err != nil {
		return view, err
	}
	// Refresh the actual catalog, rather than marking an untested API key ready.
	manifests, err := l.controller.Probe(ctx)
	if err != nil {
		return view, err
	}
	for _, manifest := range manifests {
		if _, err := l.upsertAgentManifest(ctx, manifest, time.Now().UTC()); err != nil {
			return view, err
		}
	}
	return view, nil
}

func (l *Local) TestModelProvider(ctx context.Context, id string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error) {
	controller, ok := l.controller.(modelSetupController)
	if !ok {
		return nodeprotocol.ModelProviderTestResult{}, errors.New("model setup unavailable")
	}
	return controller.TestModelProvider(ctx, id, input)
}
