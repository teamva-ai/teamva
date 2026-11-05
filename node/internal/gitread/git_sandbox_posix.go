//go:build darwin || linux

package gitread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func duplicateIdentityFile(file *os.File) (*os.File, error) {
	// Openat(".") creates an independent open-file description for the pinned
	// directory. dup would share the ReadDir offset and make later sandboxes see
	// an artificial EOF after the first object snapshot.
	fd, err := unix.Openat(int(file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	result := os.NewFile(uintptr(fd), "git-sandbox-bound-directory")
	if result == nil {
		unix.Close(fd)
		return nil, errors.New("Git sandbox directory handle is unavailable")
	}
	return result, nil
}

func sandboxRelativeParts(relative string) ([]string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(relative))
	if cleaned == "." || cleaned == "" || filepath.IsAbs(relative) || strings.HasPrefix(cleaned, "../") {
		return nil, errors.New("invalid Git sandbox relative path")
	}
	parts := strings.Split(cleaned, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\\x00") {
			return nil, errors.New("invalid Git sandbox path component")
		}
	}
	return parts, nil
}

func openSandboxDirectoryAt(root *os.File, relative string) (*os.File, error) {
	if root == nil {
		return nil, errors.New("missing Git sandbox root handle")
	}
	if relative == "" || relative == "." {
		return duplicateIdentityFile(root)
	}
	parts, err := sandboxRelativeParts(relative)
	if err != nil {
		return nil, err
	}
	current, err := duplicateIdentityFile(root)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		fd, openErr := unix.Openat(int(current.Fd()), part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), "git-sandbox-metadata-directory")
		if current == nil {
			unix.Close(fd)
			return nil, errors.New("Git sandbox directory handle is unavailable")
		}
	}
	return current, nil
}

func openSandboxRegularAt(root *os.File, relative string) (*os.File, os.FileInfo, error) {
	parts, err := sandboxRelativeParts(relative)
	if err != nil {
		return nil, nil, err
	}
	parent, err := openSandboxDirectoryAt(root, strings.Join(parts[:len(parts)-1], "/"))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), parts[len(parts)-1],
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), "git-sandbox-metadata-file")
	if file == nil {
		unix.Close(fd)
		return nil, nil, errors.New("Git sandbox regular handle is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("Git sandbox metadata is not a regular file")
	}
	return file, info, nil
}

func sandboxEntryModeAt(parent *os.File, name string) (uint32, error) {
	if parent == nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return 0, errors.New("invalid Git sandbox entry name")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, err
	}
	return uint32(stat.Mode) & unix.S_IFMT, nil
}

func sandboxModeDirectory() uint32 { return unix.S_IFDIR }
func sandboxModeRegular() uint32   { return unix.S_IFREG }
