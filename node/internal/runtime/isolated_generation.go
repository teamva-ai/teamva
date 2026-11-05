package runtime

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

var errNoToolsUnsupported = errors.New("no_tools_unsupported")

type isolatedGeneration struct {
	executionID, provider, model, system, prompt string
	disableTools                                 bool
	maxResponseBytes                             int
}

// No Handle, localstore, business workspace, broker or resumable session is used.
// The historically named title directory owner also owns work-plan attempts.
func (r *Runtime) generateIsolatedText(parent context.Context, request isolatedGeneration) (result string, resultErr error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	r.managersMu.Lock()
	if r.closing {
		r.managersMu.Unlock()
		return "", context.Canceled
	}
	if request.disableTools && !r.providerMetadata[request.provider].Supports("no_tools") {
		r.managersMu.Unlock()
		return "", errNoToolsUnsupported
	}
	r.activeGenerations.Add(1)
	factory := r.titleManagerFactory
	r.managersMu.Unlock()
	defer r.activeGenerations.Done()
	ctx, cancel := context.WithCancelCause(parent)
	stopRuntimeCancel := context.AfterFunc(r.ctx, func() { cancel(context.Canceled) })
	defer stopRuntimeCancel()
	defer cancel(context.Canceled)

	attemptDir, removeAttempt, err := r.titleDirectories.newAttempt()
	if err != nil {
		return "", errTitleGeneration
	}
	defer func() {
		if err := removeAttempt(); err != nil {
			result, resultErr = "", errTitleGeneration
		}
	}()
	cwd := filepath.Join(attemptDir, "workspace")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		return "", errTitleGeneration
	}
	managerRoot := filepath.Join(attemptDir, "manager")
	var manager *Manager
	if factory != nil {
		manager = factory(request.provider, r.executableFor(request.provider), managerRoot)
	} else {
		manager = NewManager(request.provider, r.executableFor(request.provider), managerRoot, nil, log.New(io.Discard, "", 0))
	}
	if manager == nil {
		return "", errTitleGeneration
	}
	// Close must release even a Start that ignores cancellation; join it before
	// deleting native private data or allowing Runtime.Close to return.
	closedOnCancel := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		_ = manager.Close()
		close(closedOnCancel)
	})
	defer func() {
		if !stopClose() {
			<-closedOnCancel
		}
		if err := manager.Close(); err != nil {
			result, resultErr = "", errTitleGeneration
		}
		if err := ctx.Err(); err != nil {
			result, resultErr = "", err
			if errors.Is(context.Cause(ctx), errTitleGeneration) {
				resultErr = errTitleGeneration
			}
		}
	}()

	key := InstanceKey{InstanceID: "isolated-" + keyDirectorySegment(request.executionID), Generation: filepath.Base(attemptDir)}
	config := InstanceConfig{Model: request.model, SystemPrompt: request.system, ReadOnly: true, DisableTools: request.disableTools}
	runID := key.Generation
	item, _, err := manager.Get(ctx, runID, key, config)
	if err != nil {
		return "", errTitleGeneration
	}
	defer manager.ReleaseRun(runID)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output := newGenerationOutput(request.provider, request.maxResponseBytes)
	output.disableTools = request.disableTools
	_, err = item.Start(ctx, adapter.RunRequest{
		RunID: runID, CWD: cwd, Model: request.model, Prompt: request.prompt, ReadOnly: true, DisableTools: request.disableTools,
	}, func(ctx context.Context, event adapter.Event) error {
		err := output.accept(ctx, event)
		if err != nil {
			cancel(errTitleGeneration)
		}
		return err
	})
	if err != nil {
		return "", errTitleGeneration
	}
	return output.finalText()
}
