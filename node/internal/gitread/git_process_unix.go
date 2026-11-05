//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitread

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureGitProcess(command *exec.Cmd) (func() error, func(), error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	processGroupID := 0
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	afterStart := func() error {
		if command.Process == nil {
			return errors.New("Git process did not start")
		}
		processGroupID = command.Process.Pid
		return nil
	}
	cleanup := func() {
		if processGroupID <= 0 {
			return
		}
		// Always reap the dedicated process group, including after a successful
		// leader exit. A helper that daemonizes but remains in the group must not
		// outlive the bounded read operation.
		_ = syscall.Kill(-processGroupID, syscall.SIGKILL)
	}
	return afterStart, cleanup, nil
}
