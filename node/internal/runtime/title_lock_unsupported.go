//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package runtime

import "os"

// shortcut: Windows/plan9 have no flock-style ownership lock here. The desktop
// app is single-instance and the runtime owns the directory; revisit if title
// generation ever becomes multi-process.
func lockTitleDirectory(*os.File) error { return nil }

func privateTitleDirectory(os.FileInfo) bool { return true }
