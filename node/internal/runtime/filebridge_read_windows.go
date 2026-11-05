//go:build windows

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

// The file bridge intentionally keeps directory-relative I/O on Unix. Windows
// has no openat equivalent in os, so these helpers pin the path with Lstat +
// SameFile checks instead of following replaced links mid-operation.
//
// shortcut: path-based checks race with a concurrent local actor; upgrade to
// os.Root handles if Windows ever runs untrusted users or services.
func fileBridgePlatformSupported() bool { return true }

func validRuntimeLeaf(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, "/\\\x00")
}

func openRuntimeDirectory(path string) (*os.File, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) == "" {
		return nil, errors.New("runtime directory is not a safe real directory")
	}
	volume := filepath.VolumeName(cleaned)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(cleaned, current)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("runtime directory is not a safe real directory")
		}
	}
	return os.Open(cleaned)
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
	target := filepath.Join(parent.Name(), name)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(target, 0o700); err != nil {
			return nil, err
		}
		info, err = os.Lstat(target)
	} else if err != nil {
		return nil, err
	} else if exclusive {
		return nil, os.ErrExist
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("runtime subdirectory is not a real directory")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		file.Close()
		return nil, errors.New("runtime subdirectory identity changed")
	}
	return file, nil
}

func readBridgeRequestAt(directory *os.File, name string, limit int64) ([]byte, error) {
	if directory == nil || !validRuntimeLeaf(name) {
		return nil, errors.New("invalid file bridge request name")
	}
	path := filepath.Join(directory.Name(), name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("file bridge request is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("file bridge request identity changed")
	}
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
	info, err := os.Lstat(filepath.Join(directory.Name(), name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch {
	case info.Mode().IsRegular():
		return true, nil
	case info.Mode()&os.ModeSymlink != 0:
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
	temporary, err := os.CreateTemp(directory.Name(), ".teamva-response-"+hex.EncodeToString(nonce[:]))
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
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
	if err := os.Rename(temporaryName, filepath.Join(directory.Name(), name)); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
