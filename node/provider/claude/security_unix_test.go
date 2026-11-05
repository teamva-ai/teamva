//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func releaseFIFO(path string) {
	if file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
		_ = file.Close()
	}
}

func TestSecurityFIFOReadCannotBlockTheHost(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{})
	config, _ := c.config()
	scope, err := newToolScope(c, config, provider.RunRequest{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.close()
	pipe := filepath.Join(cwd, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := scope.call(t.Context(), "read_file", map[string]any{"path": "pipe"}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read_file accepted a FIFO")
		}
	case <-time.After(300 * time.Millisecond):
		releaseFIFO(pipe)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("FIFO cleanup could not unblock host")
		}
		t.Fatal("read_file blocked in FIFO open before regular-file verification")
	}
}

func TestSecurityFIFOReadOpenIsNonBlockingEvenAfterATypeCheckRace(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		file, err := root.OpenFile("pipe", noFollowReadFlags(false), 0)
		if err == nil {
			file.Close()
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		releaseFIFO(pipe)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("FIFO cleanup failed")
		}
		t.Fatal("safe read flags can block if a regular file is replaced by a FIFO")
	}
}

func TestSecurityFIFOReadRunInterruptReturns(t *testing.T) {
	c, cwd := fakeClient(t, provider.InstanceConfig{Model: "fixture-fifo-read"})
	pipe := filepath.Join(cwd, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	running := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := c.Start(ctx, provider.RunRequest{RunID: "fifo-run", CWD: cwd}, func(_ context.Context, event provider.Event) error {
			if event.Type == "provider.tool" && event.Payload["status"] == "running" {
				select {
				case running <- struct{}{}:
				default:
				}
			}
			return nil
		})
		done <- err
	}()
	select {
	case <-running:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := c.Interrupt(ctx, "fifo-run"); err != nil {
		releaseFIFO(pipe)
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupt error=%v", err)
		}
	// A -race helper process deliberately waits about one second on exit;
	// allow the owned process tree to reap, but never an unbounded host open.
	case <-time.After(3 * time.Second):
		releaseFIFO(pipe)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("FIFO Run cleanup failed")
		}
		t.Fatal("Interrupt killed native CLI but left the host Run blocked on FIFO")
	}
}

func TestSecurityFIFOInstructionsFailWithoutOpeningNativeCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := readWorkspaceInstructions(ctx, path); err == nil {
		t.Fatal("FIFO host instructions were accepted")
	}
}
