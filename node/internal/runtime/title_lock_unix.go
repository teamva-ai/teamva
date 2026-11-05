//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package runtime

import (
	"os"

	"golang.org/x/sys/unix"
)

// Lock the directory inode, not a replaceable/deletable lock-file path. The
// descriptor is O_CLOEXEC, so native subprocesses cannot retain it after SIGKILL.
func lockTitleDirectory(directory *os.File) error {
	return unix.Flock(int(directory.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func privateTitleDirectory(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}
