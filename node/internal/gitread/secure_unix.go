//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitread

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	errUnsafePath = errors.New("unsafe worktree path")
	errFileLarge  = errors.New("worktree file exceeds limit")
)

type secureFile struct {
	data []byte
	size int64
	mode uint32
}

type securePathMetadata struct {
	mode string
	size int64
}

func secureWorktreeMode(root, relative string) (string, error) {
	metadata, err := secureWorktreeMetadata(root, relative)
	return metadata.mode, err
}

func secureWorktreeMetadata(root, relative string) (securePathMetadata, error) {
	rootFile, err := openIdentityDirectory(root)
	if err != nil {
		return securePathMetadata{}, errUnsafePath
	}
	defer rootFile.Close()
	return secureWorktreeMetadataFromDirectory(rootFile, relative)
}

func secureWorktreeMetadataFromDirectory(rootFile *os.File, relative string) (securePathMetadata, error) {
	cleaned, err := normalizePrefix(relative)
	if err != nil || cleaned == "" || rootFile == nil {
		return securePathMetadata{}, errUnsafePath
	}
	rootFD := int(rootFile.Fd())
	currentFD := rootFD
	defer func() {
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
	}()
	parts := strings.Split(filepath.FromSlash(cleaned), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return securePathMetadata{}, errUnsafePath
		}
		nextFD, openErr := unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return securePathMetadata{}, errUnsafePath
		}
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		currentFD = nextFD
	}
	final := parts[len(parts)-1]
	if final == "" || final == "." || final == ".." {
		return securePathMetadata{}, errUnsafePath
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(currentFD, final, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return securePathMetadata{}, errUnsafePath
	}
	switch uint32(stat.Mode) & unix.S_IFMT {
	case unix.S_IFLNK:
		return securePathMetadata{mode: "120000", size: stat.Size}, nil
	case unix.S_IFDIR:
		return securePathMetadata{mode: "040000", size: stat.Size}, nil
	case unix.S_IFREG:
		return securePathMetadata{mode: "100644", size: stat.Size}, nil
	default:
		return securePathMetadata{}, errUnsafePath
	}
}

func secureReadRegular(ctx context.Context, root, relative string, limit int) (secureFile, error) {
	rootFile, err := openIdentityDirectory(root)
	if err != nil {
		return secureFile{}, errUnsafePath
	}
	defer rootFile.Close()
	return secureReadRegularFromDirectory(ctx, rootFile, relative, limit)
}

func secureReadRegularFromDirectory(ctx context.Context, rootFile *os.File, relative string, limit int) (secureFile, error) {
	if err := ctx.Err(); err != nil {
		return secureFile{}, err
	}
	cleaned, err := normalizePrefix(relative)
	if err != nil || cleaned == "" || rootFile == nil {
		return secureFile{}, errUnsafePath
	}
	rootFD := int(rootFile.Fd())
	currentFD := rootFD
	defer func() {
		if currentFD >= 0 && currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
	}()
	parts := strings.Split(filepath.FromSlash(cleaned), string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return secureFile{}, errUnsafePath
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			// Opening a FIFO read-only blocks before Fstat can reject it. O_NONBLOCK
			// makes the final open safe for every non-regular file type.
			flags |= unix.O_NONBLOCK
		}
		nextFD, openErr := unix.Openat(currentFD, part, flags, 0)
		if openErr != nil {
			return secureFile{}, errUnsafePath
		}
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		currentFD = nextFD
	}
	var stat unix.Stat_t
	if err := unix.Fstat(currentFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return secureFile{}, errUnsafePath
	}
	if stat.Size > int64(limit) {
		return secureFile{size: stat.Size, mode: uint32(stat.Mode)}, errFileLarge
	}
	file := os.NewFile(uintptr(currentFD), "git-worktree-file")
	if file == nil {
		return secureFile{}, errUnsafePath
	}
	// Ownership is transferred to os.File so the deferred unix.Close must not
	// close the same descriptor a second time.
	currentFD = -1
	defer file.Close()
	capacity := int(stat.Size)
	if capacity < 0 || capacity > limit {
		capacity = limit
	}
	payload := make([]byte, 0, capacity)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return secureFile{}, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if len(payload)+read > limit {
				return secureFile{size: stat.Size, mode: uint32(stat.Mode)}, errFileLarge
			}
			payload = append(payload, buffer[:read]...)
		}
		if errors.Is(readErr, os.ErrClosed) {
			return secureFile{}, errUnsafePath
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return secureFile{}, errUnsafePath
		}
	}
	return secureFile{data: payload, size: stat.Size, mode: uint32(stat.Mode)}, nil
}
