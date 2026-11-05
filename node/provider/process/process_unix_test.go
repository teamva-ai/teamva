//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package providerprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLeaderExitReapsInheritedSleepChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command("/bin/sh", "-c", "sleep 30 & child=$!; printf '%s' \"$child\" > \"$1\"; exit 0", "provider", pidFile)
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = devNull
	command.Stderr = devNull
	tree, err := Start(command)
	_ = devNull.Close()
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := tree.Wait(waitContext); err != nil {
		// The leader exits successfully; its descendant is killed by the reaper.
		t.Fatalf("provider leader wait: %v", err)
	}
	payload, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil || childPID <= 0 {
		t.Fatalf("invalid child PID %q: %v", payload, err)
	}
	assertProcessGone(t, childPID)
}

func TestCloseReapsProviderLeaderAndSleepChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	command := exec.Command("/bin/sh", "-c", "sleep 30 & child=$!; printf '%s' \"$child\" > \"$1\"; wait", "provider", pidFile)
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = devNull
	command.Stderr = devNull
	tree, err := Start(command)
	_ = devNull.Close()
	if err != nil {
		t.Fatal(err)
	}
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for childPID <= 0 && time.Now().Before(deadline) {
		payload, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(payload)))
		}
		if childPID <= 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if childPID <= 0 {
		t.Fatal("Provider child PID was not published")
	}
	_ = tree.Close()
	assertProcessGone(t, childPID)
}

func assertProcessGone(t *testing.T, processID int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(processID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Provider descendant %d survived cleanup: %v", processID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
