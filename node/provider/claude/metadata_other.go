//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package claude

import "os"

// Windows does not expose a link count through os.FileInfo. Roots still confine
// every read to the authorized directory; the extra hard-link check is Unix-only.
func singleLink(info os.FileInfo) bool { return info.Mode().IsRegular() }
func noFollowReadFlags(bool) int       { return os.O_RDONLY }

func openChildRoot(parent *os.Root, name string) (*os.Root, error) {
	return parent.OpenRoot(name)
}
