package embedded

import (
	"context"
	"errors"

	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func modelSetupFor(id string) (provider.ModelSetup, error) {
	definition, ok := catalog.Lookup(id)
	if !ok || definition.ModelSetup == nil {
		return nil, errors.New("model provider setup is unavailable")
	}
	return definition.ModelSetup(), nil
}

func (c *Controller) ModelProviderSettings(ctx context.Context, id string) (nodeprotocol.ModelProviderSettings, error) {
	setup, err := modelSetupFor(id)
	if err != nil {
		return nodeprotocol.ModelProviderSettings{}, err
	}
	return setup.Settings(ctx)
}

func (c *Controller) SaveModelProvider(ctx context.Context, id string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error) {
	return controllerDiscovery(c, ctx, func(ctx context.Context) (nodeprotocol.ModelProviderView, error) {
		setup, err := modelSetupFor(id)
		if err != nil {
			return nodeprotocol.ModelProviderView{}, err
		}
		view, err := setup.Save(ctx, input)
		if err != nil {
			return view, err
		}
		c.mu.Lock()
		resolved := c.resolutions[id]
		c.mu.Unlock()
		if err := c.runtime.SetProviderExecutable(ctx, id, resolved.Path); err != nil {
			return view, err
		}
		return view, nil
	})
}

func (c *Controller) TestModelProvider(ctx context.Context, id string, input nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error) {
	setup, err := modelSetupFor(id)
	if err != nil {
		return nodeprotocol.ModelProviderTestResult{}, err
	}
	return setup.Test(ctx, input)
}
