package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Auth protection records paths/metadata only. It never reads or copies account
// contents. Preserve both the configured spelling and its real target, since a
// source or an existing instance auth binding can itself be a symlink.
func resolveAuthProtectionPaths(candidates []string) ([]string, error) {
	unique := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(filepath.Clean(candidate))
		if err != nil {
			return nil, errors.New("opencode cannot verify original authentication paths")
		}
		unique[absolute] = true
		canonical, err := canonicalAuthPath(absolute, 0)
		if err != nil {
			return nil, err
		}
		unique[canonical] = true
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func canonicalAuthPath(absolute string, links int) (string, error) {
	if links > 40 {
		return "", errors.New("opencode cannot verify original authentication paths")
	}
	existing := absolute
	var suffix []string
	for {
		info, err := os.Lstat(existing)
		if err == nil {
			var canonical string
			if info.Mode()&os.ModeSymlink != 0 {
				target, readErr := os.Readlink(existing)
				parent, parentErr := filepath.EvalSymlinks(filepath.Dir(existing))
				if readErr != nil || parentErr != nil {
					return "", errors.New("opencode cannot verify original authentication paths")
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(parent, target)
				}
				canonical, err = canonicalAuthPath(filepath.Clean(target), links+1)
			} else {
				canonical, err = filepath.EvalSymlinks(existing)
			}
			if err != nil {
				return "", errors.New("opencode cannot verify original authentication paths")
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, suffix[index])
			}
			return canonical, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(existing) == existing {
			return "", errors.New("opencode cannot verify original authentication paths")
		}
		// Preserve a dangling auth symlink's real reserved target, rather than
		// mistaking its missing target for an ordinary absent binding filename.
		suffix = append(suffix, filepath.Base(existing))
		existing = filepath.Dir(existing)
	}
}

func accountAuthSourcePaths() ([]string, error) {
	candidates := make([]string, 0, 2)
	for _, lookup := range []func() (string, error){dataHome, configHome} {
		home, err := lookup()
		if err != nil {
			return nil, errors.New("opencode cannot locate original authentication paths")
		}
		candidates = append(candidates, filepath.Join(home, "opencode", "auth.json"))
	}
	path, err := nativeAccountPath()
	if err != nil {
		return nil, err
	}
	// Native V2 credentials live in the account DB. Protect its spelling, real
	// target and WAL/SHM sidecars even when they are inside a business write root.
	databasePaths, err := resolveAuthProtectionPaths([]string{path})
	if err != nil {
		return nil, err
	}
	for _, database := range databasePaths {
		candidates = append(candidates, database, database+"-wal", database+"-shm")
	}
	return resolveAuthProtectionPaths(candidates)
}

func (c *Client) protectedAuthPaths() ([]string, error) {
	candidates := append([]string(nil), c.authSourcePaths...)
	if c.configDir != "" {
		// Legacy native readers may also find auth beside private config; its
		// real target must not become writable merely via a data/repository grant.
		candidates = append(candidates, filepath.Join(c.configDir, "opencode", "auth.json"))
	}
	if c.dataDir != "" {
		// Include an already-retained binding even if it points at a different
		// old account target than today's global authSource selection.
		candidates = append(candidates, filepath.Join(c.dataDir, "opencode", "auth.json"))
	}
	return resolveAuthProtectionPaths(candidates)
}
