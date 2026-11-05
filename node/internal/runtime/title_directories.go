package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const titleDirectoryName = "title-executions"

// titleDirectoryOwner holds an exclusive OS lock on the controlled directory
// inode for the entire Runtime lifetime, including native-provider shutdown.
// SIGKILL releases the lock automatically; the next owner removes old attempts
// before accepting any new title work. A second Runtime never reaps an owner's
// active attempts, even when both Runtimes are in the same process.
type titleDirectoryOwner struct {
	path      string
	directory *os.File
	root      *os.Root
}

func openTitleDirectoryOwner(workspaceRoot string) (_ *titleDirectoryOwner, resultErr error) {
	if workspaceRoot == "" {
		return nil, errTitleGeneration
	}
	// The configured Runtime parent is trusted, including OS aliases such as
	// macOS /var. The fixed title-executions child itself must never be a symlink.
	parentPath, err := filepath.Abs(filepath.Dir(filepath.Clean(workspaceRoot)))
	if err != nil {
		return nil, errTitleGeneration
	}
	parentPath, err = filepath.EvalSymlinks(parentPath)
	if err != nil {
		return nil, errTitleGeneration
	}
	parent, err := openRuntimeDirectory(parentPath)
	if err != nil {
		return nil, errTitleGeneration
	}
	defer parent.Close()
	directory, err := ensureRuntimeSubdirectory(parent, titleDirectoryName)
	if err != nil {
		return nil, errTitleGeneration
	}
	owner := &titleDirectoryOwner{path: filepath.Join(parentPath, titleDirectoryName), directory: directory}
	defer func() {
		if resultErr != nil {
			if owner.root != nil {
				_ = owner.root.Close()
			}
			_ = directory.Close()
		}
	}()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || !privateTitleDirectory(info) {
		return nil, errTitleGeneration
	}
	if err := lockTitleDirectory(directory); err != nil {
		return nil, errTitleGeneration
	}
	owner.root, err = os.OpenRoot(owner.path)
	if err != nil {
		return nil, errTitleGeneration
	}
	// OpenRoot and the OS-lock FD must pin the exact same real directory. This
	// also fences a replacement/symlink race while opening the bounded root.
	rootInfo, err := owner.root.Stat(".")
	if err != nil || !os.SameFile(info, rootInfo) || owner.validateBinding() != nil {
		return nil, errTitleGeneration
	}
	if err := owner.removeStaleAttempts(); err != nil {
		return nil, errTitleGeneration
	}
	return owner, nil
}

func (o *titleDirectoryOwner) validateBinding() error {
	if o == nil || o.directory == nil || o.root == nil {
		return errTitleGeneration
	}
	bound, err := o.directory.Stat()
	if err != nil {
		return errTitleGeneration
	}
	current, err := os.Lstat(o.path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(bound, current) {
		return errTitleGeneration
	}
	return nil
}

// Only exact host attempt names are eligible. Decimal suffixes are the legacy
// os.MkdirTemp format; new attempts use a 128-bit random hex suffix. Unrelated
// directories, files and Provider homes are never candidates for this cleanup.
func isTitleAttemptName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "attempt-")
	if !ok {
		return false
	}
	if len(suffix) >= 1 && len(suffix) <= 10 {
		decimal := true
		for _, char := range suffix {
			decimal = decimal && char >= '0' && char <= '9'
		}
		if decimal {
			return true
		}
	}
	if len(suffix) != 32 {
		return false
	}
	for _, char := range suffix {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (o *titleDirectoryOwner) removeStaleAttempts() error {
	directory, err := o.root.Open(".")
	if err != nil {
		return errTitleGeneration
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if !isTitleAttemptName(entry.Name()) {
				continue
			}
			info, err := o.root.Lstat(entry.Name())
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return errTitleGeneration
			}
			if !info.IsDir() {
				continue
			}
			// Root.RemoveAll is FD-rooted and does not follow nested symlinks.
			// Never use a discovered absolute path or recurse into Provider homes.
			if err := o.root.RemoveAll(entry.Name()); err != nil {
				return errTitleGeneration
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errTitleGeneration
		}
	}
}

func (o *titleDirectoryOwner) newAttempt() (string, func() error, error) {
	if err := o.validateBinding(); err != nil {
		return "", nil, errTitleGeneration
	}
	for range 10 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", nil, errTitleGeneration
		}
		name := "attempt-" + hex.EncodeToString(nonce[:])
		if err := o.root.Mkdir(name, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", nil, errTitleGeneration
		}
		return filepath.Join(o.path, name), func() error { return o.root.RemoveAll(name) }, nil
	}
	return "", nil, errTitleGeneration
}

func (o *titleDirectoryOwner) close() error {
	if o == nil {
		return nil
	}
	// Runtime.Close has joined every title attempt before this final sweep.
	// Keep the exclusive lock until cleanup and all rooted handles are closed.
	cleanupErr := o.removeStaleAttempts()
	rootErr := o.root.Close()
	lockErr := o.directory.Close()
	if cleanupErr != nil || rootErr != nil || lockErr != nil {
		return errTitleGeneration
	}
	return nil
}
