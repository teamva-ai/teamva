//go:build darwin

package gitread

import (
	"fmt"
	"os"
	"syscall"
)

func platformWatchIdentity(info os.FileInfo) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%x:%x:%x:%x:%x:%x:%x:%x:%x:%x",
		uint64(stat.Dev), uint64(stat.Ino), uint64(stat.Mode), uint64(stat.Nlink),
		uint64(stat.Uid), uint64(stat.Gid), uint64(stat.Rdev),
		stat.Ctimespec.Sec, stat.Ctimespec.Nsec, uint64(stat.Flags)), true
}
