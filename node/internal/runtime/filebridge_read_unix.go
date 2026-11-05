//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func fileBridgePlatformSupported() bool { return true }

func validRuntimeLeaf(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, "/\\\x00")
}

func ensureRuntimeSubdirectory(parent *os.File, name string) (*os.File, error) {
	return openOrCreateRuntimeSubdirectory(parent, name, false)
}

func createRuntimeSubdirectory(parent *os.File, name string) (*os.File, error) {
	return openOrCreateRuntimeSubdirectory(parent, name, true)
}

func openOrCreateRuntimeSubdirectory(parent *os.File, name string, exclusive bool) (*os.File, error) {
	if parent == nil || !validRuntimeLeaf(name) {
		return nil, errors.New("invalid runtime subdirectory")
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		if exclusive || !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(fd)
		return nil, errors.New("runtime subdirectory is not a real directory")
	}
	file := os.NewFile(uintptr(fd), "runtime-subdirectory")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("runtime subdirectory handle is unavailable")
	}
	return file, nil
}

func readBridgeRequestAt(directory *os.File, name string, limit int64) ([]byte, error) {
	if directory == nil || !validRuntimeLeaf(name) {
		return nil, errors.New("invalid file bridge request name")
	}
	fd, err := unix.Openat(int(directory.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Size < 0 || stat.Size > limit {
		unix.Close(fd)
		return nil, errors.New("file bridge request is not a bounded regular file")
	}
	file := os.NewFile(uintptr(fd), "file-bridge-request")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("file bridge request handle is unavailable")
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(payload)) > limit {
		return nil, errors.New("file bridge request exceeds read limit")
	}
	return payload, nil
}

func bridgeResponseExistsAt(directory *os.File, name string) (bool, error) {
	if directory == nil || !validRuntimeLeaf(name) {
		return false, errors.New("invalid file bridge response name")
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return true, nil
	case unix.S_IFLNK:
		return false, nil
	default:
		return false, errors.New("file bridge response path is not replaceable")
	}
}

func writeBridgeResponseAt(directory *os.File, name string, payload []byte) error {
	if directory == nil || !validRuntimeLeaf(name) || len(payload) > 8<<20 {
		return errors.New("invalid file bridge response")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporaryName := ".teamva-response-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(directory.Fd()), temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	temporary := os.NewFile(uintptr(fd), "file-bridge-response")
	if temporary == nil {
		unix.Close(fd)
		_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		return errors.New("file bridge response handle is unavailable")
	}
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		}
	}()
	for written := 0; written < len(payload); {
		count, writeErr := temporary.Write(payload[written:])
		if writeErr != nil {
			return writeErr
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		written += count
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.Fd()), temporaryName, int(directory.Fd()), name); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func openRuntimeDirectory(path string) (*os.File, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return nil, errors.New("runtime directory is not a safe real directory")
	}
	fd, err := unix.Open(string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), "runtime-bounded-directory")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("runtime directory handle is unavailable")
	}
	return file, nil
}
