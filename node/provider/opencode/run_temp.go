package opencode

import (
	"errors"
	"os"
	"path/filepath"
)

// runTempDirectory is host-created transport scratch space, never a repository
// or a grant to the IPC parent. Rooted operations pin the exact IPC inode during
// creation; the ancestry snapshot detects replacement before native startup.
type runTempDirectory struct {
	path      string
	directory os.FileInfo
	ancestors []runTempAncestor
}

type runTempAncestor struct {
	path string
	info os.FileInfo
}

func prepareRunTempDirectory(ipc string) (*runTempDirectory, error) {
	fail := func() (*runTempDirectory, error) {
		return nil, errors.New("opencode host IPC temporary directory is unsafe or changed")
	}
	canonical, err := permissionDirectory(ipc)
	if err != nil || canonical != filepath.Clean(ipc) {
		return fail() // Reject aliases; callers must supply the canonical host IPC.
	}
	temp := &runTempDirectory{path: filepath.Join(canonical, ".tmp")}
	for path := canonical; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail()
		}
		temp.ancestors = append(temp.ancestors, runTempAncestor{path, info})
		if filepath.Dir(path) == path {
			break
		}
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return fail()
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(info, temp.ancestors[0].info) {
		return fail()
	}
	if err := root.Mkdir(".tmp", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail()
	}
	info, err = root.Lstat(".tmp")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail()
	}
	pinned, err := root.OpenRoot(".tmp")
	if err != nil {
		return fail()
	}
	defer pinned.Close()
	opened, err := pinned.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return fail()
	}
	// Chmod the opened inode, not a path that can be replaced with a symlink.
	if err := pinned.Chmod(".", 0o700); err != nil {
		return fail()
	}
	temp.directory, err = pinned.Stat(".")
	if err != nil || temp.validate() != nil {
		return fail()
	}
	return temp, nil
}

func (temp *runTempDirectory) validate() error {
	failure := errors.New("opencode host IPC temporary directory is unsafe or changed")
	for _, ancestor := range temp.ancestors {
		current, err := os.Lstat(ancestor.path)
		if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, ancestor.info) {
			return failure
		}
	}
	current, err := os.Lstat(temp.path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || current.Mode().Perm() != 0o700 || !os.SameFile(current, temp.directory) {
		return failure
	}
	canonical, err := filepath.EvalSymlinks(temp.path)
	if err != nil || canonical != temp.path {
		return failure
	}
	return nil
}
