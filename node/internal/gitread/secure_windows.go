package gitread

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
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
	path, info, err := secureWindowsPath(root, relative)
	if err != nil {
		return securePathMetadata{}, err
	}
	_ = path
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return securePathMetadata{mode: "120000", size: info.Size()}, nil
	case info.IsDir():
		return securePathMetadata{mode: "040000", size: info.Size()}, nil
	case info.Mode().IsRegular():
		return securePathMetadata{mode: "100644", size: info.Size()}, nil
	default:
		return securePathMetadata{}, errUnsafePath
	}
}

func secureWorktreeMetadataFromDirectory(_ *os.File, _ string) (securePathMetadata, error) {
	return securePathMetadata{}, errUnsafePath
}

func secureReadRegular(ctx context.Context, root, relative string, limit int) (secureFile, error) {
	if err := ctx.Err(); err != nil {
		return secureFile{}, err
	}
	path, info, err := secureWindowsPath(root, relative)
	if err != nil || !info.Mode().IsRegular() {
		return secureFile{}, errUnsafePath
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return secureFile{}, errUnsafePath
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return secureFile{}, errUnsafePath
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return secureFile{}, errUnsafePath
	}
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		windows.CloseHandle(handle)
		return secureFile{}, errUnsafePath
	}
	size := int64(information.FileSizeHigh)<<32 | int64(information.FileSizeLow)
	if size > int64(limit) {
		windows.CloseHandle(handle)
		return secureFile{size: size}, errFileLarge
	}
	file := os.NewFile(uintptr(handle), "git-worktree-file")
	if file == nil {
		windows.CloseHandle(handle)
		return secureFile{}, errUnsafePath
	}
	defer file.Close()
	capacity := int(size)
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
				return secureFile{size: size}, errFileLarge
			}
			payload = append(payload, buffer[:read]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return secureFile{}, errUnsafePath
		}
	}
	return secureFile{data: payload, size: size}, nil
}

func secureReadRegularFromDirectory(context.Context, *os.File, string, int) (secureFile, error) {
	return secureFile{}, errUnsafePath
}

func secureWindowsPath(root, relative string) (string, os.FileInfo, error) {
	cleaned, err := normalizePrefix(relative)
	if err != nil || cleaned == "" {
		return "", nil, errUnsafePath
	}
	current := filepath.Clean(root)
	parts := strings.Split(filepath.FromSlash(cleaned), string(filepath.Separator))
	var info os.FileInfo
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, errUnsafePath
		}
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", nil, errUnsafePath
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", nil, errUnsafePath
		}
	}
	return current, info, nil
}
