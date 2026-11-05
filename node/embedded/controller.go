// Package embedded exposes the Node execution plane as an in-process
// controller for the local service. It is a narrow public facade over
// node/internal so browser and desktop clients share one Adapter Runtime.
package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	"github.com/teamva-ai/teamva/node/internal/runtime"
	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type ToolCaller interface {
	SendToolRequest(context.Context, nodeprotocol.NodeRequest) (nodeprotocol.NodeRequestResult, error)
}

// GitRead is a deliberately narrow, ephemeral read path. It is separate from
// Handle so status and patch payloads never enter inbox_commands or command
// result persistence.
type GitRead interface {
	ListGitRepositories(context.Context, nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error)
	ListGitChanges(context.Context, nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error)
	ReadGitDiff(context.Context, nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error)
	RefreshGit(context.Context, nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error)
	SubscribeGitEvents(context.Context, nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error)
	GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error)
}

type Config struct {
	ComputerID    string
	DataDir       string
	WorkspaceDir  string
	ProviderPaths map[string]string
	ToolCaller    ToolCaller
	Logger        *log.Logger
}

type Controller struct {
	computerID string
	store      *localstore.Store
	runtime    *runtime.Runtime
	logger     *log.Logger
	// dataDir anchors bundled-runtime discovery for executable resolution.
	dataDir string
	// mu guards the settings-facing executable projection. The Runtime owns the
	// actual adapter/manager swap; this is only the projection returned by
	// ProviderExecutables and SetProviderExecutable.
	mu          sync.Mutex
	resolutions map[string]nodeprotocol.ProviderExecutable
	closeOnce   sync.Once
	closeErr    error
	discoveryMu sync.Mutex
	discovery   chan struct{}
	probeCtx    context.Context
	probeCancel context.CancelFunc
	probeWG     sync.WaitGroup
	closing     bool
}

func New(ctx context.Context, config Config) (*Controller, error) {
	if config.ComputerID == "" {
		return nil, errors.New("embedded runtime requires computer ID")
	}
	if config.DataDir == "" {
		return nil, errors.New("embedded runtime requires data directory")
	}
	if config.WorkspaceDir == "" {
		config.WorkspaceDir = filepath.Join(config.DataDir, "workspaces")
	}
	if config.Logger == nil {
		config.Logger = log.New(os.Stderr, "embedded-runtime ", log.LstdFlags)
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.WorkspaceDir, 0o700); err != nil {
		return nil, err
	}
	store, err := localstore.Open(ctx, filepath.Join(config.DataDir, "runtime.db"))
	if err != nil {
		return nil, err
	}
	definitions := catalog.Definitions()
	adapters := make([]adapter.Adapter, 0, len(definitions))
	executables := make(map[string]string, len(definitions))
	resolutions := make(map[string]nodeprotocol.ProviderExecutable, len(definitions))
	for _, definition := range definitions {
		id := definition.Metadata.ID
		custom := strings.TrimSpace(config.ProviderPaths[id])
		executable, source := resolveProviderExecutable(id, definition.Metadata.Command, custom, config.DataDir)
		item, createErr := definition.NewProbe(executable)
		if createErr == nil && (item == nil || item.Name() != id) {
			createErr = fmt.Errorf("provider %s returned an invalid probe client", id)
		}
		if createErr != nil {
			if item != nil {
				_ = item.Close()
			}
			for _, previous := range adapters {
				_ = previous.Close()
			}
			_ = store.Close()
			return nil, createErr
		}
		adapters = append(adapters, item)
		executables[id] = executable
		resolutions[id] = nodeprotocol.ProviderExecutable{Provider: id, Path: executable, Source: source}
	}
	engine := runtime.New(config.ComputerID, store, adapters, config.WorkspaceDir, executables)
	engine.SetToolCaller(config.ToolCaller)
	probeCtx, probeCancel := context.WithCancel(ctx)
	return &Controller{
		computerID: config.ComputerID, store: store, runtime: engine,
		dataDir: config.DataDir, resolutions: resolutions,
		logger:    config.Logger,
		discovery: make(chan struct{}, 1), probeCtx: probeCtx, probeCancel: probeCancel,
	}, nil
}

// resolveProviderExecutable picks the effective CLI for one Provider. The
// precedence is: explicit settings path, TEAMVA_<ID>_PATH environment
// override, app-bundled/development vendor runtime, then the manifest command
// resolved through PATH. The source value is reported to the UI.
func resolveProviderExecutable(id, command, custom, dataDir string) (string, string) {
	if custom != "" {
		return custom, "custom"
	}
	if configured := strings.TrimSpace(os.Getenv(providerExecutableEnv(id))); configured != "" {
		return configured, "environment"
	}
	if bundled := bundledExecutable(id, command, dataDir); bundled != "" {
		return bundled, "bundled"
	}
	return command, "path"
}

func providerExecutableEnv(id string) string {
	return "TEAMVA_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_PATH"
}

// ProviderExecutables returns the effective executable projection for every
// configured Provider in catalog order.
func (c *Controller) ProviderExecutables() []nodeprotocol.ProviderExecutable {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := make([]nodeprotocol.ProviderExecutable, 0, len(c.resolutions))
	for _, definition := range catalog.Definitions() {
		if item, ok := c.resolutions[definition.Metadata.ID]; ok {
			items = append(items, item)
		}
	}
	return items
}

// SetProviderExecutable applies one Provider's executable path immediately.
// An empty path clears the explicit setting back to the default resolution.
// The discovery client and idle instances are swapped along the way; active
// Runs are never interrupted.
func (c *Controller) SetProviderExecutable(
	ctx context.Context, provider, executable string,
) (nodeprotocol.ProviderExecutable, error) {
	return controllerDiscovery(c, ctx, func(ctx context.Context) (nodeprotocol.ProviderExecutable, error) {
		return c.setProviderExecutable(ctx, provider, executable)
	})
}

func (c *Controller) setProviderExecutable(ctx context.Context, provider, executable string) (nodeprotocol.ProviderExecutable, error) {
	definition, ok := catalog.Lookup(provider)
	if !ok {
		return nodeprotocol.ProviderExecutable{}, fmt.Errorf("provider %s is not supported", provider)
	}
	executable = strings.TrimSpace(executable)
	resolved, source := resolveProviderExecutable(provider, definition.Metadata.Command, executable, c.dataDir)
	if err := c.runtime.SetProviderExecutable(ctx, provider, resolved); err != nil {
		return nodeprotocol.ProviderExecutable{}, err
	}
	item := nodeprotocol.ProviderExecutable{Provider: provider, Path: resolved, Source: source}
	c.mu.Lock()
	c.resolutions[provider] = item
	c.mu.Unlock()
	return item, nil
}

func (c *Controller) Probe(ctx context.Context) ([]nodeprotocol.AgentManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return controllerDiscovery(c, ctx, c.probe)
}

func (c *Controller) probe(ctx context.Context) ([]nodeprotocol.AgentManifest, error) {
	adapters := c.runtime.AdapterSnapshot()
	type result struct {
		index    int
		manifest adapter.CapabilityManifest
		err      error
	}
	results := make(chan result, len(adapters))
	for index, item := range adapters {
		go func(index int, item adapter.Adapter) {
			probeContext, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			manifest, err := item.Probe(probeContext)
			results <- result{index: index, manifest: manifest, err: err}
		}(index, item)
	}
	ordered := make([]result, len(adapters))
	for range adapters {
		// controllerDiscovery lets the caller return on cancellation, but keeps
		// the gate until ALL workers return. Releasing it here on ctx.Done would
		// allow a second probe to race a misbehaving, still-running adapter.
		item := <-results
		ordered[item.index] = item
	}
	manifests := make([]nodeprotocol.AgentManifest, 0, len(ordered))
	for index, item := range ordered {
		manifest := item.manifest
		if item.err != nil {
			manifest = adapter.CapabilityManifest{
				Provider: adapters[index].Name(), CapabilityVersion: 1,
				UnavailableReasonCode: "probe_failed",
			}
		}
		capabilities := catalog.Capabilities(adapters[index].Name(), manifest)
		manifests = append(manifests, nodeprotocol.AgentManifest{
			Provider: manifest.Provider, ProviderVersion: manifest.ProviderVersion,
			Available: manifest.Available, Authenticated: manifest.Authenticated,
			Capabilities: capabilities, CapabilityVersion: manifest.CapabilityVersion,
		})
	}
	return manifests, nil
}

func (c *Controller) Handle(
	ctx context.Context, command nodeprotocol.CommandEnvelope,
) (nodeprotocol.CommandResult, error) {
	if command.Name == "agent.probe" || command.Name == "agent.diagnose" {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		return controllerDiscovery(c, ctx, func(ctx context.Context) (nodeprotocol.CommandResult, error) {
			return c.handle(ctx, command)
		})
	}
	return c.handle(ctx, command)
}

func (c *Controller) handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (nodeprotocol.CommandResult, error) {
	record, replayed, err := c.runtime.Handle(ctx, command)
	result := nodeprotocol.CommandResult{CommandID: command.MessageID, Result: record.Result}
	if err != nil || record.Status == "failed" || record.Status == "uncertain" {
		result.Status = "failed"
		result.ErrorCode = record.ErrorCode
		if result.ErrorCode == "" {
			result.ErrorCode = "embedded_command_failed"
		}
		return result, err
	}
	if replayed && record.Status == "processing" {
		result.Status = "processing"
	} else {
		result.Status = "completed"
	}
	return result, nil
}

func (c *Controller) UnackedEvents(ctx context.Context, limit int) ([]nodeprotocol.RunEvent, error) {
	if limit <= 0 || limit > nodeprotocol.MaxRunEventsPerBatch {
		limit = nodeprotocol.MaxRunEventsPerBatch
	}
	events, err := c.store.UnackedEvents(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]nodeprotocol.RunEvent, 0, len(events))
	for _, event := range events {
		result = append(result, nodeprotocol.RunEvent{
			ID: event.ID, RunID: event.RunID, LeaseEpoch: event.LeaseEpoch,
			Sequence: event.Sequence, Type: event.Type, Payload: event.Payload,
			OccurredAt: event.OccurredAt,
		})
	}
	return result, nil
}

func (c *Controller) AckEvents(ctx context.Context, cursors []nodeprotocol.RunEventCursor) error {
	values := make([]localstore.EventCursor, 0, len(cursors))
	for _, cursor := range cursors {
		values = append(values, localstore.EventCursor{
			RunID: cursor.RunID, LeaseEpoch: cursor.LeaseEpoch, Sequence: cursor.Sequence,
		})
	}
	return c.store.AckEventsThrough(ctx, values)
}

func (c *Controller) ListGitRepositories(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	return c.runtime.ListGitRepositories(ctx, request)
}

func (c *Controller) ListGitChanges(ctx context.Context, request nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	return c.runtime.ListGitChanges(ctx, request)
}

func (c *Controller) ReadGitDiff(ctx context.Context, request nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	return c.runtime.ReadGitDiff(ctx, request)
}

func (c *Controller) RefreshGit(ctx context.Context, request nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	return c.runtime.RefreshGit(ctx, request)
}

func (c *Controller) SubscribeGitEvents(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	return c.runtime.SubscribeGitEvents(ctx, request)
}

func (c *Controller) GetRunGitSnapshots(ctx context.Context, runID string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	return c.runtime.GetRunGitSnapshots(ctx, runID)
}

// RecycleIdleProvider is the local operational boundary used by diagnostics
// and acceptance cleanup. Active Runs are reported but never interrupted.
func (c *Controller) RecycleIdleProvider(ctx context.Context, provider string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	return c.runtime.RecycleIdleProvider(ctx, provider)
}

func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		c.discoveryMu.Lock()
		c.closing = true
		c.probeCancel()
		c.discoveryMu.Unlock()
		// Provider shutdown releases workers that do not honor cancellation on
		// their own. Keep runtime.db open until those workers finish.
		// Runtime.Close joins Handle before closing its discovery adapters. Close
		// these clients first too: a manual agent.diagnose/agent.probe Handle may
		// itself be the stuck worker that only adapter.Close can release.
		var discoveryErr error
		for _, item := range c.runtime.AdapterSnapshot() {
			discoveryErr = errors.Join(discoveryErr, item.Close())
		}
		runtimeErr := c.runtime.Close()
		c.probeWG.Wait()
		c.closeErr = errors.Join(discoveryErr, runtimeErr, c.store.Close())
	})
	return c.closeErr
}

// Discovery clients need exclusive access even when a caller's deadline fires
// before an adapter returns. The buffered result never blocks a late worker;
// its gate/lifecycle remain owned by the worker, not the cancelled caller.
func controllerDiscovery[T any](c *Controller, ctx context.Context, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	c.discoveryMu.Lock()
	if c.closing {
		c.discoveryMu.Unlock()
		return zero, context.Canceled
	}
	c.probeWG.Add(1)
	c.discoveryMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.probeCtx, cancel)
	defer stop()
	defer cancel()
	select {
	case <-ctx.Done():
		c.probeWG.Done()
		return zero, ctx.Err()
	case c.discovery <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-c.discovery
		c.probeWG.Done()
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	results := make(chan result, 1)
	go func() {
		defer c.probeWG.Done()
		defer func() { <-c.discovery }()
		value, err := operation(ctx)
		results <- result{value, err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case item := <-results:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return item.value, item.err
	}
}

func (c *Controller) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"computer_id": c.computerID})
}

var _ GitRead = (*Controller)(nil)
