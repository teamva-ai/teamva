package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	instanceManifestName = ".teamva-instance.json"
	// data is Provider-owned durable native session state, not regenerable
	// configuration. Its location remains stable across config retention/reuse.
	instanceDataDirectoryName   = "data"
	maxInstanceManifestBytes    = 16 << 10
	maxRetainedConfigDirs       = 128
	maxRetainedConfigBytes      = 512 << 20
	configRetentionAge          = 30 * 24 * time.Hour
	maxConfigRetentionEntries   = 4096
	maxConfigRetentionTreeFiles = 100_000
)

type configRetentionPolicy struct {
	maxDirectories int
	maxBytes       int64
	maxAge         time.Duration
}

func defaultConfigRetentionPolicy() configRetentionPolicy {
	return configRetentionPolicy{
		maxDirectories: maxRetainedConfigDirs, maxBytes: maxRetainedConfigBytes, maxAge: configRetentionAge,
	}
}

type instanceManifest struct {
	Version          int       `json:"version"`
	Provider         string    `json:"provider"`
	SessionKey       string    `json:"session_key"`
	UpdatedAt        time.Time `json:"updated_at"`
	SessionPersisted bool      `json:"session_persisted"`
	// Only host-created config with an external native data home is prunable.
	// A missing marker is fail-safe, not an instruction to migrate old data.
	ConfigOnly bool `json:"config_only,omitempty"`
}

func writeInstanceManifest(directory string, manifest instanceManifest) error {
	if manifest.Version != 1 || manifest.Provider == "" || manifest.SessionKey == "" || manifest.UpdatedAt.IsZero() {
		return errors.New("invalid instance retention manifest")
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(payload) > maxInstanceManifestBytes {
		return errors.New("instance retention manifest exceeds limit")
	}
	temporary, err := os.CreateTemp(directory, ".teamva-instance-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	destination := filepath.Join(directory, instanceManifestName)
	// Removing the old marker first is crash-safe for retention: a crash in the
	// small replacement window makes the directory ineligible for deletion. It
	// also provides Windows replacement semantics without touching credentials.
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryName, destination)
}

func readInstanceManifest(directory string) (instanceManifest, error) {
	path := filepath.Join(directory, instanceManifestName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxInstanceManifestBytes {
		return instanceManifest{}, errors.New("unsafe instance retention manifest")
	}
	file, err := os.Open(path)
	if err != nil {
		return instanceManifest{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return instanceManifest{}, errors.New("instance retention manifest changed during open")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxInstanceManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest instanceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return instanceManifest{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return instanceManifest{}, errors.New("instance retention manifest has trailing data")
	}
	if manifest.Version != 1 || manifest.Provider == "" || manifest.SessionKey == "" || manifest.UpdatedAt.IsZero() {
		return instanceManifest{}, errors.New("invalid instance retention manifest")
	}
	return manifest, nil
}

type configRetentionItem struct {
	path      string
	name      string
	updatedAt time.Time
	bytes     int64
	deletable bool
	identity  os.FileInfo
}

func (m *Manager) pruneConfigDirs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.retentionMu.Lock()
	defer m.retentionMu.Unlock()
	m.mu.Lock()
	active := make(map[string]struct{}, len(m.instances))
	for _, instance := range m.instances {
		active[filepath.Clean(instance.configDir)] = struct{}{}
	}
	provider := m.provider
	root := filepath.Join(m.rootDir, "instances")
	policy := m.retention
	m.mu.Unlock()
	if policy.maxDirectories < 0 || policy.maxBytes < 0 || policy.maxAge < 0 {
		return errors.New("invalid config retention policy")
	}
	rootIdentity, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !rootIdentity.IsDir() || rootIdentity.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe config retention root")
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	openedRoot, err := directory.Stat()
	if err != nil || !os.SameFile(rootIdentity, openedRoot) {
		return errors.New("config retention root changed during open")
	}
	items := make([]configRetentionItem, 0)
	seen := 0
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name())
			identity, err := os.Lstat(path)
			if err != nil || !identity.IsDir() || identity.Mode()&os.ModeSymlink != 0 {
				continue
			}
			manifest, err := readInstanceManifest(path)
			if err != nil || !manifest.ConfigOnly || manifest.Provider != provider ||
				entry.Name() != keyDirectorySegment(instanceDirectoryKey(provider, manifest.SessionKey)) {
				continue
			}
			dataOnly, err := instanceHasOnlySessionData(ctx, path)
			if err != nil || dataOnly {
				// A retained data home and its manifest do not occupy a config slot
				// or config byte budget. Never traverse native databases to count it.
				continue
			}
			// Durable homes are in provider-data, outside this scan. Defensive
			// unclassified/data-only directories are not config entries either.
			seen++
			if seen > maxConfigRetentionEntries {
				return errors.New("config retention directory count exceeds scan bound")
			}
			bytes, err := boundedConfigTreeBytes(ctx, path)
			if err != nil {
				continue
			}
			_, isActive := active[filepath.Clean(path)]
			items = append(items, configRetentionItem{
				path: path, name: entry.Name(), updatedAt: manifest.UpdatedAt.UTC(), bytes: bytes,
				deletable: manifest.SessionPersisted && !isActive, identity: identity,
			})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].updatedAt.Equal(items[j].updatedAt) {
			return items[i].name > items[j].name
		}
		return items[i].updatedAt.After(items[j].updatedAt)
	})
	cutoff := time.Now().UTC().Add(-policy.maxAge)
	retained := 0
	retainedBytes := int64(0)
	for _, item := range items {
		remove := item.deletable && (item.updatedAt.Before(cutoff) || retained >= policy.maxDirectories ||
			item.bytes > policy.maxBytes-retainedBytes)
		if remove {
			current, err := os.Lstat(item.path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, item.identity) {
				continue
			}
			if err := pruneInstanceConfig(ctx, item.path, item.identity); err != nil {
				return err
			}
			continue
		}
		retained++
		retainedBytes += item.bytes
	}
	return nil
}

// instanceHasOnlySessionData recognizes the shell left after config pruning.
// It is deliberately Provider-agnostic: any data entry (even a symlink) is
// protected, and only the manifest may remain alongside it without a slot.
func instanceHasOnlySessionData(ctx context.Context, path string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(path, instanceDataDirectoryName)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		entries, err := directory.ReadDir(128)
		for _, entry := range entries {
			if entry.Name() != instanceDataDirectoryName && entry.Name() != instanceManifestName {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// pruneInstanceConfig removes only rebuildable siblings of data. Rooted
// removal cannot follow credential/config symlinks outside this instance.
// Keeping the data path in place preserves Provider resource/session binding;
// no migration, backup or copy from a user's default home is involved.
func pruneInstanceConfig(ctx context.Context, path string, identity os.FileInfo) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(identity, opened) {
		return errors.New("config retention directory changed during open")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if entry.Name() == instanceDataDirectoryName || entry.Name() == instanceManifestName {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := root.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if _, err := root.Lstat(instanceDataDirectoryName); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Remove(instanceManifestName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(identity, current) {
		return errors.New("config retention directory changed before removal")
	}
	// Non-recursive removal also protects data created after the last check.
	return os.Remove(path)
}

func boundedConfigTreeBytes(ctx context.Context, root string) (int64, error) {
	files := 0
	bytes := int64(0)
	errConfigTreeTooLarge := errors.New("config retention tree exceeds scan bound")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == filepath.Join(root, instanceDataDirectoryName) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if files > maxConfigRetentionTreeFiles {
			return errConfigTreeTooLarge
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 0 {
			if info.Size() > maxRetainedConfigBytes-bytes {
				return errConfigTreeTooLarge
			}
			bytes += info.Size()
		}
		return nil
	})
	if errors.Is(err, errConfigTreeTooLarge) {
		return maxRetainedConfigBytes + 1, nil
	}
	return bytes, err
}
