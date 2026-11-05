package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// The gitread package separately forces both discoveries across the same cache
// barrier. Here both real Runtime command paths must reach a preparation gate
// and then two fake Providers concurrently, with durable resource approvals.
// The gate also lets stop/cancel/revocation win deterministically before Start.
func TestRuntimeParallelGitAgentStartsKeepScopeAndStopFences(t *testing.T) {
	for _, mode := range []string{"same-scope", "different-scope"} {
		for _, action := range []string{"complete", "stop-one", "cancel-one", "revoke", "replace-inode"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				base := t.TempDir()
				store, err := localstore.Open(t.Context(), filepath.Join(base, "runtime.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				engine := New("cmp-parallel-git-start", store, nil, filepath.Join(base, "workspaces"), nil)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer func() {
					cancel()
					if err := engine.Close(); err != nil {
						t.Error(err)
					}
				}()
				root := filepath.Join(base, "repository")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				initRuntimeGitRepository(t, root)
				root, err = filepath.EvalSymlinks(root)
				if err != nil {
					t.Fatal(err)
				}
				resources := [2][]nodeprotocol.GitResourceRequest{}
				wantRoots := [2]string{}
				for index := range resources {
					path, resourceID := root, "shared-resource"
					if mode == "different-scope" {
						resourceID = fmt.Sprintf("partial-%d", index)
						path = filepath.Join(root, resourceID)
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					wantRoots[index] = path
					resources[index] = []nodeprotocol.GitResourceRequest{{ResourceID: resourceID, Name: resourceID, Path: path}}
					approveRuntimeGitResources(t, engine, resources[index])
				}
				prepared := make(chan struct{}, 2)
				releasePreparation, releaseProviders := make(chan struct{}), make(chan struct{})
				var prepareOnce, providersOnce sync.Once
				unblockPreparation := func() { prepareOnce.Do(func() { close(releasePreparation) }) }
				unblockProviders := func() { providersOnce.Do(func() { close(releaseProviders) }) }
				defer unblockPreparation()
				defer unblockProviders()
				engine.SetMCPResolver(func(runContext context.Context, _ string) (map[string]json.RawMessage, error) {
					prepared <- struct{}{}
					select {
					case <-releasePreparation:
					case <-runContext.Done():
					}
					return nil, nil // No custom MCP and no secret material.
				})
				type observation struct {
					index    int
					cwd      string
					manifest []byte
					roots    map[string][]string
					err      error
				}
				entered := make(chan observation, 2)
				providers := [2]*fakeAdapter{}
				commands := [2]nodeprotocol.CommandEnvelope{}
				contexts := [2]context.Context{}
				cancels := [2]context.CancelFunc{}
				for index := range providers {
					contexts[index], cancels[index] = context.WithCancel(ctx)
					defer cancels[index]()
					providerID := fmt.Sprintf("fake-parallel-%d", index)
					provider := &fakeAdapter{name: providerID}
					provider.onStart = func(request adapter.RunRequest) {
						manifest, err := os.ReadFile(filepath.Join(request.CWD, "AGENTS.md"))
						entered <- observation{index: index, cwd: request.CWD, manifest: manifest, err: err}
						select {
						case <-releaseProviders:
						case <-contexts[index].Done():
						}
					}
					providers[index] = provider
					engine.RegisterTestAdapter(providerID, provider)
					payload, err := json.Marshal(nodeprotocol.StartRunPayload{
						RunID: fmt.Sprintf("parallel-run-%d", index), ProjectID: "parallel-project", IssueID: "parallel-issue",
						AgentID: fmt.Sprintf("parallel-agent-%d", index), IssueAgentInstanceID: fmt.Sprintf("parallel-instance-%d", index),
						IssueAgentGenerationID: fmt.Sprintf("parallel-generation-%d", index), LeaseEpoch: 1,
						Provider: providerID, Prompt: "isolated fake-provider concurrency test", GitResources: resources[index],
					})
					if err != nil {
						t.Fatal(err)
					}
					now := time.Now().UTC()
					commands[index] = nodeprotocol.CommandEnvelope{
						ProtocolVersion: nodeprotocol.Version, MessageID: fmt.Sprintf("parallel-start-%d", index), Name: "run.start",
						ComputerID: "cmp-parallel-git-start", SentAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload,
					}
				}
				start := make(chan struct{})
				done := [2]chan error{make(chan error, 1), make(chan error, 1)}
				for index := range done {
					go func() {
						<-start
						record, replayed, err := engine.Handle(contexts[index], commands[index])
						if err == nil && (record.Status != "completed" || replayed) {
							err = fmt.Errorf("unexpected command status=%s replayed=%t", record.Status, replayed)
						}
						done[index] <- err
					}()
				}
				close(start)
				for range 2 {
					select {
					case <-prepared:
					case err := <-done[0]:
						t.Fatalf("first Agent failed before parallel preparation gate: %v", err)
					case err := <-done[1]:
						t.Fatalf("second Agent failed before parallel preparation gate: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				switch action {
				case "stop-one":
					payload, err := json.Marshal(nodeprotocol.StopRunPayload{
						RunID: "parallel-run-0", Provider: "fake-parallel-0", LeaseEpoch: 1,
					})
					if err != nil {
						t.Fatal(err)
					}
					stop := commands[0]
					stop.Name, stop.MessageID, stop.Payload = "run.stop", "stop-parallel-0", payload
					if _, _, err := engine.Handle(ctx, stop); err != nil {
						t.Fatal(err)
					}
				case "cancel-one":
					cancels[0]()
				case "revoke":
					entries, err := os.ReadDir(store.ResourceAuthorizationDirectory())
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if strings.HasSuffix(entry.Name(), ".json") {
							if err := os.Remove(filepath.Join(store.ResourceAuthorizationDirectory(), entry.Name())); err != nil {
								t.Fatal(err)
							}
						}
					}
				case "replace-inode":
					if err := os.Rename(root, root+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0o700); err != nil {
						t.Fatal(err)
					}
					initRuntimeGitRepository(t, root)
				}
				unblockPreparation()
				wantStarts := 0
				if action == "complete" {
					wantStarts = 2
				} else if action == "stop-one" || action == "cancel-one" {
					wantStarts = 1
				}
				observed := [2]observation{}
				for range wantStarts {
					select {
					case item := <-entered:
						if item.err != nil {
							t.Fatal(item.err)
						}
						item.roots = workspaceManifestRoots(t, item.manifest)
						if len(item.roots) != 1 {
							t.Fatalf("invalid Provider repository map: %+v", item)
						}
						for _, roots := range item.roots {
							if !reflect.DeepEqual(roots, []string{wantRoots[item.index]}) {
								t.Fatalf("Provider %d scope widened: %v", item.index, roots)
							}
						}
						observed[item.index] = item
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				if action == "complete" && ((observed[0].cwd == observed[1].cwd) != (mode == "same-scope")) {
					t.Fatal("parallel Agents did not retain exact scope-private workspace identity")
				}
				unblockProviders()
				for index := range done {
					select {
					case err := <-done[index]:
						shouldStart := action == "complete" || (index == 1 && wantStarts == 1)
						if (err == nil) != shouldStart {
							t.Fatalf("Agent %d completion err=%v shouldStart=%t", index, err, shouldStart)
						}
						providers[index].mu.Lock()
						starts := providers[index].start
						providers[index].mu.Unlock()
						if (starts == 1) != shouldStart {
							t.Fatalf("Agent %d crossed stop/authorization fence: starts=%d", index, starts)
						}
						if shouldStart {
							snapshots, err := engine.GetRunGitSnapshots(ctx, fmt.Sprintf("parallel-run-%d", index))
							if err != nil || len(snapshots) != 2 {
								t.Fatalf("Agent %d start/end snapshots missing: %v", index, err)
							}
							for _, snapshot := range snapshots {
								if len(snapshot.Warnings) != 0 {
									t.Fatalf("Agent %d cache race degraded a real snapshot: %+v", index, snapshot.Warnings)
								}
							}
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			})
		}
	}
}
