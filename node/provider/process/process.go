package providerprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const defaultCloseTimeout = 10 * time.Second

// Tree owns one Provider process and its descendants. Darwin/Linux use an
// independent lease watchdog plus PID birth identities; Windows retains a Job
// Object, and secondary Unix targets retain a dedicated process group.
type Tree struct {
	command   *exec.Cmd
	terminate func() error
	release   func()

	done       chan struct{}
	mu         sync.Mutex
	err        error
	platformMu sync.Mutex
	released   bool
}

// Start starts command only after configuring a dedicated process tree. On
// Darwin/Linux command is transparently launched behind an internal watchdog;
// its private arguments and environment are transferred over an inherited
// pipe. Callers that need pipes should attach explicit *os.File endpoints to
// command before Start; avoiding Cmd.StdoutPipe keeps Wait independent from
// descendants that inherited a Provider pipe.
func Start(command *exec.Cmd) (*Tree, error) {
	if command == nil {
		return nil, errors.New("provider command is required")
	}
	afterStart, terminate, release, err := configureCommand(command)
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		release()
		return nil, err
	}
	if err := afterStart(); err != nil {
		_ = terminate()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		release()
		return nil, err
	}
	tree := &Tree{
		command: command, terminate: terminate, release: release, done: make(chan struct{}),
	}
	go tree.reap()
	return tree, nil
}

func (tree *Tree) reap() {
	err := tree.command.Wait()
	// A Provider leader may exit after daemonizing a helper. Terminate the tree
	// before releasing its group/job ownership so no descendant survives a
	// nominally successful leader exit.
	tree.platformMu.Lock()
	if !tree.released {
		_ = tree.terminate()
		tree.release()
		tree.released = true
	}
	tree.platformMu.Unlock()
	tree.mu.Lock()
	tree.err = err
	close(tree.done)
	tree.mu.Unlock()
}

func (tree *Tree) Done() <-chan struct{} { return tree.done }

func (tree *Tree) Err() error {
	select {
	case <-tree.done:
		tree.mu.Lock()
		defer tree.mu.Unlock()
		return tree.err
	default:
		return nil
	}
}

func (tree *Tree) Terminate() error {
	select {
	case <-tree.done:
		return nil
	default:
		tree.platformMu.Lock()
		defer tree.platformMu.Unlock()
		if tree.released {
			return nil
		}
		return tree.terminate()
	}
}

func (tree *Tree) Wait(ctx context.Context) error {
	select {
	case <-tree.done:
		return tree.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (tree *Tree) Close() error {
	terminateErr := tree.Terminate()
	ctx, cancel := context.WithTimeout(context.Background(), defaultCloseTimeout)
	defer cancel()
	waitErr := tree.Wait(ctx)
	return errors.Join(terminateErr, waitErr)
}

type outputResult struct {
	payload []byte
	err     error
}

// CombinedOutput runs a short Provider diagnostic under the same process-tree
// ownership as a long-lived adapter. Output is bounded so a broken executable
// cannot grow memory without limit.
func CombinedOutput(ctx context.Context, command *exec.Cmd, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("provider output limit must be positive")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	command.Stdout = writer
	command.Stderr = writer
	tree, err := Start(command)
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	output := make(chan outputResult, 1)
	go func() {
		var buffer bytes.Buffer
		_, copyErr := io.CopyN(&buffer, reader, maxBytes+1)
		if errors.Is(copyErr, io.EOF) {
			copyErr = nil
		}
		_ = reader.Close()
		if int64(buffer.Len()) > maxBytes {
			copyErr = fmt.Errorf("provider output exceeds %d bytes", maxBytes)
		}
		output <- outputResult{payload: buffer.Bytes(), err: copyErr}
	}()

	select {
	case <-ctx.Done():
		_ = tree.Terminate()
		closeCtx, cancel := context.WithTimeout(context.Background(), defaultCloseTimeout)
		_ = tree.Wait(closeCtx)
		cancel()
		return nil, ctx.Err()
	case <-tree.Done():
	}
	result := <-output
	return result.payload, errors.Join(tree.Err(), result.err)
}
