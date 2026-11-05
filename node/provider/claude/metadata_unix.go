//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package claude

import (
	"fmt"
	"os"
	"syscall"
)

func singleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func noFollowReadFlags(directory bool) int {
	// A racing FIFO/device replacement must not block inside open() before we
	// can fstat the descriptor and reject anything other than a regular file.
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	return flags
}

// Pin every ancestor directory with O_NOFOLLOW. os.Root alone prevents an
// escape but intentionally permits in-root symlinks; those could alias .git or
// another private in-root subtree during a racing rename.
func openChildRoot(parent *os.Root, name string) (*os.Root, error) {
	file, err := parent.OpenFile(name, noFollowReadFlags(true), 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return os.OpenRoot(fmt.Sprintf("/dev/fd/%d", file.Fd()))
}
