//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitread

import (
	"os"

	"golang.org/x/sys/unix"
)

func openIdentityRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "git-identity-regular")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errUnsafePath
	}
	return file, nil
}
