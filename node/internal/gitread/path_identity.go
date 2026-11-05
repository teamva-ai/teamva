package gitread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// stablePathIdentity pins every directory component as well as the final
// platform file identity. Checking the full chain matters: Lstat on the final
// directory alone would accept an unchanged inode reached through a newly
// introduced symlink or a replaced ancestor.
type stablePathIdentity struct {
	path       string
	stable     string
	components []stablePathComponent
	handle     *os.File
}

type stablePathComponent struct {
	path string
	info os.FileInfo
}

func captureStablePathIdentity(value string) (stablePathIdentity, error) {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) {
		return stablePathIdentity{}, errors.New("path identity requires an absolute path")
	}
	paths, err := absolutePathComponents(cleaned)
	if err != nil {
		return stablePathIdentity{}, err
	}
	identity := stablePathIdentity{path: cleaned, components: make([]stablePathComponent, 0, len(paths))}
	for _, candidate := range paths {
		info, statErr := os.Lstat(candidate)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return stablePathIdentity{}, errors.New("path identity is not a real directory")
		}
		identity.components = append(identity.components, stablePathComponent{path: candidate, info: info})
	}
	stable, err := platformStableFileIdentity(cleaned)
	if err != nil {
		return stablePathIdentity{}, err
	}
	identity.stable = stable
	handle, err := openIdentityDirectory(cleaned)
	if err != nil {
		return stablePathIdentity{}, err
	}
	handleInfo, err := handle.Stat()
	if err != nil || !os.SameFile(identity.components[len(identity.components)-1].info, handleInfo) {
		handle.Close()
		return stablePathIdentity{}, errors.New("directory changed while pinning identity")
	}
	identity.handle = handle
	return identity, nil
}

func (identity stablePathIdentity) validate() error {
	if identity.path == "" || identity.stable == "" || len(identity.components) == 0 || identity.handle == nil {
		return errors.New("missing path identity")
	}
	for _, component := range identity.components {
		current, err := os.Lstat(component.path)
		if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
			!os.SameFile(component.info, current) {
			return errors.New("path identity changed")
		}
	}
	stable, err := platformStableFileIdentity(identity.path)
	if err != nil || stable != identity.stable {
		return errors.New("stable file identity changed")
	}
	handleInfo, err := identity.handle.Stat()
	if err != nil || !os.SameFile(identity.components[len(identity.components)-1].info, handleInfo) {
		return errors.New("pinned directory identity changed")
	}
	return nil
}

func (identity *stablePathIdentity) close() {
	if identity != nil && identity.handle != nil {
		_ = identity.handle.Close()
		identity.handle = nil
	}
}

func absolutePathComponents(value string) ([]string, error) {
	volume := filepath.VolumeName(value)
	anchor := volume + string(filepath.Separator)
	if volume == "" {
		anchor = string(filepath.Separator)
	}
	remainder := strings.TrimPrefix(value, anchor)
	if remainder == value && volume != "" {
		return nil, errors.New("invalid absolute path volume")
	}
	result := []string{filepath.Clean(anchor)}
	current := anchor
	for _, part := range strings.Split(remainder, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		result = append(result, filepath.Clean(current))
	}
	return result, nil
}
