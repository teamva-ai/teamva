package codex

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Only path metadata is inspected. Never read, copy, log or return auth bytes.
func codexProtectedPaths(paths ...string) ([]string, error) {
	seen := map[string]bool{}
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, errors.New("resolve codex private path failed")
		}
		seen[absolute] = true
		candidate := absolute
		var suffix []string
		for {
			canonical, err := filepath.EvalSymlinks(candidate)
			if err == nil {
				for i := len(suffix) - 1; i >= 0; i-- {
					canonical = filepath.Join(canonical, suffix[i])
				}
				seen[canonical] = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("inspect codex private path failed")
			}
			if info, statErr := os.Lstat(candidate); err != nil && statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("codex private path has an unresolved symlink")
			}
			parent := filepath.Dir(candidate)
			if parent == candidate {
				break
			}
			suffix = append(suffix, filepath.Base(candidate))
			candidate = parent
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// A read-only file alone does not prevent moving an unprotected ancestor. For
// every writable root, protect its first child containing a private path; that
// subtree's inode and every ancestor inside it then remain read-only. This
// deliberately withholds a narrow private subtree, never broadens a grant.
func codexPrivateCarveouts(paths, roots []string) ([]string, error) {
	seen := map[string]bool{}
	for _, root := range roots {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, errors.New("resolve codex writable root failed")
		}
		for _, path := range paths {
			insidePrivate, err := filepath.Rel(path, canonical)
			if err == nil && insidePrivate != ".." && !strings.HasPrefix(insidePrivate, ".."+string(filepath.Separator)) {
				return nil, errors.New("codex writable root is inside a private path")
			}
			rel, err := filepath.Rel(canonical, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if rel == "." {
				return nil, errors.New("codex writable root overlaps a private directory")
			}
			first := strings.Split(rel, string(filepath.Separator))[0]
			carveout := filepath.Join(canonical, first)
			// Native filesystem config treats these as glob syntax. Refuse an
			// ambiguous private-path rule instead of guessing an escape format.
			if strings.ContainsAny(carveout, "*?[]{}") {
				return nil, errors.New("codex private path cannot be expressed as an exact native rule")
			}
			seen[carveout] = true
		}
	}
	// A same-path write wins over read in native policy. Overlapping roots or
	// an auth target in .git can otherwise reopen a protected subtree. Refuse
	// that unusual scope; never silently accept an ineffective read carveout.
	for _, root := range roots {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, errors.New("resolve codex writable root failed")
		}
		if seen[canonical] || seen[filepath.Join(canonical, ".git")] {
			return nil, errors.New("codex writable roots conflict with private path protection")
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}
