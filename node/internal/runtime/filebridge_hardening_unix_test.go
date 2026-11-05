//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package runtime

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFileBridgeRejectsFIFORequestWithoutBlocking(t *testing.T) {
	bridge, err := NewFileBridge(t.TempDir(), "run-fifo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := bridge.openToolDirectory("issue_get")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	const name = "request-fifo.json"
	if err := unix.Mkfifo(filepath.Join(bridge.root, "issue_get", name), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, readErr := readBridgeRequestAt(directory, name, 1<<20)
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr == nil {
			t.Fatal("file bridge accepted a FIFO request")
		}
	case <-time.After(time.Second):
		t.Fatal("file bridge blocked while opening a FIFO request")
	}
}
