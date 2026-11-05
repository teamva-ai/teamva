package codex

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

// Native 0.163.0 protocol/src/permissions.rs adds a resolved worktree gitdir
// read carveout unless that exact path has an explicit entry. Restore only
// metadata ALREADY inside another authorized primary repository root: never
// infer parent-repository authorization from a subdir or a .git pointer alone.
func codexLinkedMetadataWrites(request adapter.RunRequest, roots, privateReadPaths []string) ([]string, error) {
	if request.ReadOnly || request.DisableTools {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, worktree := range roots {
		pointer, pointerInfo, err := readPinnedGitFile(worktree, ".git")
		if err != nil || !strings.HasPrefix(pointer, "gitdir: ") {
			continue
		}
		gitdir, err := resolveGitMetadataPath(worktree, strings.TrimPrefix(pointer, "gitdir: "))
		if err != nil {
			continue
		}
		contained := false
		for _, root := range roots {
			if root != worktree && pathInside(gitdir, root) {
				contained = true
				break
			}
		}
		if !contained {
			continue
		} // Do not read control-file contents outside authorized roots.
		for _, private := range privateReadPaths {
			if pathInside(gitdir, private) || pathInside(private, gitdir) {
				return nil, errors.New("codex worktree metadata overlaps private path protection")
			}
		}
		info, err := os.Lstat(gitdir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		back, _, err := readPinnedGitFile(gitdir, "gitdir")
		if err != nil {
			continue
		}
		backPath, err := resolveGitMetadataPath(gitdir, back)
		if err != nil {
			continue
		}
		backInfo, err := os.Lstat(backPath)
		if err != nil || !os.SameFile(pointerInfo, backInfo) {
			continue
		}
		common, _, err := readPinnedGitFile(gitdir, "commondir")
		if err != nil {
			continue
		}
		commonDir, err := resolveGitMetadataPath(gitdir, common)
		if err != nil {
			continue
		}
		// Standard linked worktree layout, not arbitrary user text pointing at
		// an otherwise writable application/config/auth directory.
		if filepath.Dir(gitdir) != filepath.Join(commonDir, "worktrees") {
			continue
		}
		commonInfo, err := os.Lstat(commonDir)
		if err != nil || !commonInfo.IsDir() || commonInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		worktreesInfo, err := os.Lstat(filepath.Join(commonDir, "worktrees"))
		if err != nil || !worktreesInfo.IsDir() || worktreesInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		authorized := false
		for _, primary := range roots {
			if primary == worktree {
				continue
			}
			primaryGit := filepath.Join(primary, ".git")
			primaryInfo, err := os.Lstat(primaryGit)
			if err == nil && primaryInfo.IsDir() && primaryInfo.Mode()&os.ModeSymlink == 0 && primaryGit == commonDir && os.SameFile(primaryInfo, commonInfo) {
				authorized = true
				break
			}
		}
		if !authorized {
			continue
		}
		current, err := os.Lstat(gitdir)
		if err != nil || !os.SameFile(info, current) {
			continue
		}
		currentPointer, err := os.Lstat(filepath.Join(worktree, ".git"))
		if err != nil || !os.SameFile(pointerInfo, currentPointer) || pointerInfo.Size() != currentPointer.Size() || !pointerInfo.ModTime().Equal(currentPointer.ModTime()) {
			continue
		}
		if strings.ContainsAny(gitdir, "*?[]{}") {
			return nil, errors.New("codex worktree gitdir cannot be expressed as an exact native rule")
		}
		seen[gitdir] = true
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func pathInside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveGitMetadataPath(base, value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("invalid git metadata path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.EvalSymlinks(value)
}

func readPinnedGitFile(directory, name string) (string, os.FileInfo, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil {
		return "", nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 4096 {
		return "", nil, errors.New("not a small regular git metadata file")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", nil, errors.New("git metadata file changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return "", nil, errors.New("read git metadata file failed")
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", nil, errors.New("git metadata file changed while reading")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), before, nil
}
