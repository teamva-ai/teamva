//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package runtime

import "os"

// Never attempt cleanup or generation without an OS-enforced ownership lock.
func lockTitleDirectory(*os.File) error { return errTitleGeneration }
