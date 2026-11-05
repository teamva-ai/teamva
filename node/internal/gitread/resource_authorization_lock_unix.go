//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package gitread

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func openResourceApprovalLock(directory *os.File, name string) (*os.File, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, errors.New("invalid approval lock name")
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "resource-approval-lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, errors.New("approval lock is not a private regular file")
	}
	return file, nil
}

func lockResourceApproval(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func unlockResourceApproval(file *os.File) { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }
