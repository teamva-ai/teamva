package gitread

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxGitMarkerBytes = 16 << 10
	maxGitConfigBytes = 1 << 20
)

var errRepositoryIdentityClosed = errors.New("repository identity is closed")

type stableMarkerIdentity struct {
	path      string
	info      os.FileInfo
	handle    *os.File
	directory bool
	content   string
	stable    string
}

type repositorySettings struct {
	objectFormat       string
	fileMode           *bool
	ignoreCase         *bool
	symlinks           *bool
	precomposeUnicode  *bool
	sparseCheckout     bool
	sparseCheckoutCone bool
	indexSparse        bool
	origin             string
}

func captureStableMarker(root string) (stableMarkerIdentity, error) {
	return captureStableMarkerPath(filepath.Join(root, ".git"))
}

func captureStableMarkerPath(markerPath string) (stableMarkerIdentity, error) {
	info, err := os.Lstat(markerPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return stableMarkerIdentity{}, errors.New("invalid Git marker")
	}
	marker := stableMarkerIdentity{path: markerPath, info: info, directory: info.IsDir()}
	marker.stable, err = platformStableFileIdentity(markerPath)
	if err != nil {
		return stableMarkerIdentity{}, err
	}
	if info.IsDir() {
		marker.handle, err = openIdentityDirectory(markerPath)
	} else {
		marker.handle, err = openIdentityRegular(markerPath)
	}
	if err != nil {
		return stableMarkerIdentity{}, err
	}
	handleInfo, err := marker.handle.Stat()
	if err != nil || !os.SameFile(info, handleInfo) {
		marker.close()
		return stableMarkerIdentity{}, errors.New("Git marker changed while pinning")
	}
	if !marker.directory {
		payload, readErr := readBoundedFile(context.Background(), marker.handle, maxGitMarkerBytes)
		if readErr != nil {
			marker.close()
			return stableMarkerIdentity{}, readErr
		}
		digest := sha256.Sum256(payload)
		marker.content = hex.EncodeToString(digest[:])
	}
	return marker, nil
}

func (marker *stableMarkerIdentity) validate() error {
	if marker == nil || marker.handle == nil {
		return errors.New("missing Git marker identity")
	}
	current, err := os.Lstat(marker.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || current.IsDir() != marker.directory ||
		!os.SameFile(marker.info, current) {
		return errors.New("Git marker identity changed")
	}
	handleInfo, err := marker.handle.Stat()
	if err != nil || !os.SameFile(marker.info, handleInfo) {
		return errors.New("pinned Git marker changed")
	}
	if !marker.directory {
		payload, readErr := readBoundedFile(context.Background(), marker.handle, maxGitMarkerBytes)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(payload)
		if hex.EncodeToString(digest[:]) != marker.content {
			return errors.New("Git marker content changed")
		}
	}
	return nil
}

func (marker *stableMarkerIdentity) close() {
	if marker != nil && marker.handle != nil {
		_ = marker.handle.Close()
		marker.handle = nil
	}
}

func (identity *repositoryIdentity) close() {
	identity.rootIdentity.close()
	identity.gitIdentity.close()
	identity.commonIdentity.close()
	identity.objectIdentity.close()
	identity.markerIdentity.close()
	identity.commonMarker.close()
}

func (identity *repositoryIdentity) validate() error {
	if identity == nil {
		return errors.New("missing repository identity")
	}
	for _, item := range []*stablePathIdentity{
		&identity.rootIdentity, &identity.gitIdentity, &identity.commonIdentity, &identity.objectIdentity,
	} {
		if err := item.validate(); err != nil {
			return err
		}
	}
	if err := identity.markerIdentity.validate(); err != nil {
		return err
	}
	if identity.commonMarker.handle != nil {
		return identity.commonMarker.validate()
	}
	return nil
}

func (r *repository) closeIdentity() {
	r.handleMu.Lock()
	defer r.handleMu.Unlock()
	if r.handlesClosed {
		return
	}
	r.handlesClosed = true
	r.rootIdentity.close()
	r.gitIdentity.close()
	r.commonIdentity.close()
	r.objectIdentity.close()
	r.markerIdentity.close()
	r.commonMarker.close()
}

func (r *repository) validateIdentity() error {
	r.handleMu.Lock()
	defer r.handleMu.Unlock()
	if r.handlesClosed {
		return errRepositoryIdentityClosed
	}
	if err := r.rootIdentity.validate(); err != nil {
		return err
	}
	if err := r.gitIdentity.validate(); err != nil {
		return err
	}
	if err := r.commonIdentity.validate(); err != nil {
		return err
	}
	if err := r.objectIdentity.validate(); err != nil {
		return err
	}
	if err := r.markerIdentity.validate(); err != nil {
		return err
	}
	if r.commonMarker.handle != nil {
		return r.commonMarker.validate()
	}
	return nil
}

func readBoundedFile(ctx context.Context, file *os.File, limit int) ([]byte, error) {
	if file == nil || limit <= 0 {
		return nil, errUnsafePath
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	buffer := make([]byte, 64<<10)
	result := make([]byte, 0, min(limit, 64<<10))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, err := file.Read(buffer)
		if read > 0 {
			if len(result)+read > limit {
				return nil, errFileLarge
			}
			result = append(result, buffer[:read]...)
		}
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func readRepositorySettings(ctx context.Context, root, gitDir string, gitHandle, commonHandle *os.File) (repositorySettings, error) {
	settings := repositorySettings{objectFormat: "sha1"}
	total := 0
	for _, candidate := range []struct {
		handle *os.File
		name   string
	}{{commonHandle, "config"}, {gitHandle, "config.worktree"}} {
		file, _, err := openSandboxRegularAt(candidate.handle, candidate.name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return repositorySettings{}, errors.New("Git config is not a safe regular file")
		}
		payload, readErr := readBoundedFile(ctx, file, maxGitConfigBytes-total)
		file.Close()
		if readErr != nil {
			return repositorySettings{}, readErr
		}
		total += len(payload)
		if err := parseSafeGitConfig(payload, root, gitDir, &settings); err != nil {
			return repositorySettings{}, err
		}
	}
	if settings.objectFormat != "sha1" && settings.objectFormat != "sha256" {
		return repositorySettings{}, errors.New("unsupported Git object format")
	}
	return settings, nil
}

func parseSafeGitConfig(payload []byte, root, gitDir string, settings *repositorySettings) error {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 4<<10), 64<<10)
	section := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		fullKey := section + "." + key
		switch fullKey {
		case "extensions.objectformat":
			settings.objectFormat = strings.ToLower(value)
		case "extensions.refstorage":
			if value != "" && !strings.EqualFold(value, "files") {
				return errors.New("unsupported Git ref storage")
			}
		case "core.worktree":
			candidate := value
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(gitDir, candidate)
			}
			canonical, err := canonicalDirectory(candidate)
			if err != nil || !samePath(canonical, root) {
				return errors.New("Git core.worktree escapes the selected worktree")
			}
		case "core.filemode":
			settings.fileMode = parsedBool(value)
		case "core.ignorecase":
			settings.ignoreCase = parsedBool(value)
		case "core.symlinks":
			settings.symlinks = parsedBool(value)
		case "core.precomposeunicode":
			settings.precomposeUnicode = parsedBool(value)
		case "core.sparsecheckout":
			settings.sparseCheckout = boolValue(value)
		case "core.sparsecheckoutcone":
			settings.sparseCheckoutCone = boolValue(value)
		case "index.sparse":
			settings.indexSparse = boolValue(value)
		}
		if strings.HasPrefix(section, `remote "origin"`) && key == "url" && settings.origin == "" {
			settings.origin = value
		}
	}
	return scanner.Err()
}

func parsedBool(value string) *bool {
	parsed := boolValue(value)
	return &parsed
}

func boolValue(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}
