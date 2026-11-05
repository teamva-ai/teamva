//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

package providerprocess

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// These secondary Unix targets retain process-group ownership. The independent
// lease watchdog is currently implemented only for the product target (Darwin)
// and Linux CI, where birth identities and same-UID process inspection are
// available without invoking external utilities.
func configureCommand(command *exec.Cmd) (func() error, func() error, func(), error) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	var mu sync.Mutex
	processGroupID := 0
	afterStart := func() error {
		if command.Process == nil {
			return errors.New("provider process did not start")
		}
		mu.Lock()
		processGroupID = command.Process.Pid
		mu.Unlock()
		return nil
	}
	terminate := func() error {
		mu.Lock()
		group := processGroupID
		mu.Unlock()
		if group <= 0 {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-group, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return afterStart, terminate, func() {}, nil
}
