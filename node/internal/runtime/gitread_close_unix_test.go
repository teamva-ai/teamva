//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/internal/gitread"
	"github.com/teamva-ai/teamva/node/internal/localstore"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestRuntimeCloseCancelsAndJoinsActiveGitRead(t *testing.T) {
	base := t.TempDir()
	initRuntimeGitRepository(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(base, "git-ready")
	fakeGit := filepath.Join(bin, "git")
	script := `#!/bin/sh
for argument in "$@"; do
  if [ "$argument" = "config" ]; then
    exit 1
  fi
done
printf ready > ` + strconv.Quote(ready) + `
sleep 300
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := localstore.Open(t.Context(), filepath.Join(base, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := New("computer-git-close", store, nil, filepath.Join(base, "workspaces"), nil)
	approveRuntimeGitResources(t, engine, []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: base}})
	if err := engine.gitReader.Close(); err != nil {
		t.Fatal(err)
	}
	engine.gitReader = gitread.NewWithExecutable(fakeGit)
	readDone := make(chan error, 1)
	go func() {
		_, err := engine.ListGitRepositories(context.Background(), nodeprotocol.GitRepositoriesRequest{
			ProjectID: "project-git-close",
			Resources: []nodeprotocol.GitResourceRequest{{ResourceID: "resource", Name: "Code", Path: base}},
		})
		readDone <- err
	}()
	// Package-level test parallelism can delay executable pinning and the first
	// child process on loaded builders. The assertion below still measures Close
	// from the observed process start, so a wider startup wait does not weaken
	// the cancellation/join guarantee.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = engine.Close()
		t.Fatal("active Git read did not start")
	}
	started := time.Now()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("Runtime.Close waited %s for active Git read", elapsed)
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Git read returned after Runtime.Close")
	}
}
