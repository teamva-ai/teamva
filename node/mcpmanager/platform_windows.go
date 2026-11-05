//go:build windows

package mcpmanager

import (
	"errors"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

var errManagerLocked = errors.New("mcp manager lock is held")

func lockManagerFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errManagerLocked
		}
		return nil, err
	}
	return file, nil
}

// Closing the handle releases the LockFileEx range.
func closeManagerLock(file *os.File) error {
	return file.Close()
}

func openPrivate(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorage
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	current, err := file.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		file.Close()
		return nil, ErrStorage
	}
	return file, nil
}

// Windows has no directory handle flush; the file itself is flushed before rename.
func syncDir(string) error {
	return nil
}

// taskkill /T ends the whole tree; the default Cancel only stops the direct child.
func prepareProcessCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
