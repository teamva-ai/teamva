package gitread

import (
	"os"
	"path/filepath"
)

// watchObservation deliberately excludes access time. On Darwin, kqueue
// reports an atime update caused by a read as fsnotify.Chmod. Every field that
// can represent a repository mutation (identity, mode, size, mtime, ctime,
// ownership, and link count) is retained in platformIdentity instead.
type watchObservation struct {
	mode             os.FileMode
	size             int64
	modTimeUnixNano  int64
	platformIdentity string
}

func newWatchObservation(info os.FileInfo) (watchObservation, bool) {
	if info == nil {
		return watchObservation{}, false
	}
	identity, ok := platformWatchIdentity(info)
	if !ok {
		return watchObservation{}, false
	}
	return watchObservation{
		mode: info.Mode(), size: info.Size(), modTimeUnixNano: info.ModTime().UnixNano(),
		platformIdentity: identity,
	}, true
}

func observeWatchPath(value string) (watchObservation, bool) {
	info, err := os.Lstat(filepath.Clean(value))
	if err != nil {
		return watchObservation{}, false
	}
	return newWatchObservation(info)
}
