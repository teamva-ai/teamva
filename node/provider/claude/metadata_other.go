//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package claude

import (
	"errors"
	"os"
)

// A platform without verified link-count checks must fail closed for file reads.
func singleLink(os.FileInfo) bool { return false }
func noFollowReadFlags(bool) int  { return os.O_RDONLY }
func openChildRoot(*os.Root, string) (*os.Root, error) {
	return nil, errors.New("safe directory descriptors unavailable on this platform")
}
