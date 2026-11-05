package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const (
	maxSkillArchiveBytes = 64 << 20
	maxSkillFiles        = 10_000
	maxSkillTreeBytes    = 128 << 20
)

type skillTreeInfo struct {
	digest      string
	files       []string
	skillFiles  []string
	executables []string
	size        int64
}

func installSkill(ctx context.Context, skillsRoot string, payload nodeprotocol.InstallSkillPayload) (nodeprotocol.InstallSkillResult, error) {
	slug := payload.Slug
	if !validSkillSlug(slug) {
		return nodeprotocol.InstallSkillResult{}, fmt.Errorf("invalid Skill slug")
	}
	owner, repository, err := parseGitHubRepository(payload.SourceURL)
	if err != nil {
		return nodeprotocol.InstallSkillResult{}, err
	}
	if err := os.MkdirAll(skillsRoot, 0o700); err != nil {
		return nodeprotocol.InstallSkillResult{}, fmt.Errorf("create Skills directory: %w", err)
	}
	temporary, err := os.MkdirTemp(skillsRoot, ".install-"+slug+"-")
	if err != nil {
		return nodeprotocol.InstallSkillResult{}, err
	}
	defer os.RemoveAll(temporary)

	client := &http.Client{Timeout: 90 * time.Second}
	branch, commit := "pinned", strings.TrimSpace(payload.Revision)
	if commit == "" {
		branch, commit, err = resolveGitHubRevision(ctx, client, owner, repository)
		if err != nil {
			return nodeprotocol.InstallSkillResult{}, err
		}
	} else if len(commit) != 40 || strings.Trim(commit, "0123456789abcdefABCDEF") != "" {
		return nodeprotocol.InstallSkillResult{}, fmt.Errorf("invalid pinned Skill revision")
	}
	repositoryRoot := filepath.Join(temporary, "repository")
	if err := downloadGitHubArchive(ctx, client, owner, repository, commit, repositoryRoot); err != nil {
		return nodeprotocol.InstallSkillResult{}, err
	}
	staged := filepath.Join(temporary, "package")
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return nodeprotocol.InstallSkillResult{}, err
	}
	includePaths := payload.IncludePaths
	if len(includePaths) == 0 {
		includePaths = []string{"."}
	}
	for _, includePath := range includePaths {
		cleaned, err := safeRelativePath(includePath)
		if err != nil {
			return nodeprotocol.InstallSkillResult{}, fmt.Errorf("invalid include path %q: %w", includePath, err)
		}
		source := filepath.Join(repositoryRoot, cleaned)
		info, err := os.Stat(source)
		if err != nil {
			return nodeprotocol.InstallSkillResult{}, fmt.Errorf("Skill source path %q is unavailable: %w", includePath, err)
		}
		if info.IsDir() {
			if err := copySkillTreeContents(source, staged); err != nil {
				return nodeprotocol.InstallSkillResult{}, err
			}
		} else if info.Mode().IsRegular() {
			if err := copySkillFile(source, filepath.Join(staged, filepath.Base(source)), info.Mode()); err != nil {
				return nodeprotocol.InstallSkillResult{}, err
			}
		} else {
			return nodeprotocol.InstallSkillResult{}, fmt.Errorf("unsupported Skill source %q", includePath)
		}
	}
	tree, err := inspectSkillTree(staged)
	if err != nil {
		return nodeprotocol.InstallSkillResult{}, err
	}
	if len(tree.skillFiles) == 0 {
		return nodeprotocol.InstallSkillResult{}, fmt.Errorf("the downloaded package does not contain SKILL.md")
	}

	target := filepath.Join(skillsRoot, slug)
	backup := filepath.Join(temporary, "previous")
	hadPrevious := false
	if _, statErr := os.Stat(target); statErr == nil {
		if err := os.Rename(target, backup); err != nil {
			return nodeprotocol.InstallSkillResult{}, fmt.Errorf("stage previous Skill: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(statErr) {
		return nodeprotocol.InstallSkillResult{}, statErr
	}
	if err := os.Rename(staged, target); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, target)
		}
		return nodeprotocol.InstallSkillResult{}, fmt.Errorf("activate Skill: %w", err)
	}

	manifest := map[string]any{
		"source":        payload.SourceURL,
		"branch":        branch,
		"files":         tree.files,
		"skill_files":   tree.skillFiles,
		"include_paths": includePaths,
	}
	if first, readErr := os.ReadFile(filepath.Join(target, filepath.FromSlash(tree.skillFiles[0]))); readErr == nil {
		manifest["skill_md"] = string(first)
	}
	return nodeprotocol.InstallSkillResult{
		Slug: slug, Path: target, CommitSHA: commit, ContentDigest: tree.digest,
		Manifest: manifest, FileCount: len(tree.files), SizeBytes: tree.size,
		ExecutableFiles: tree.executables,
	}, nil
}

// Slugs are bounded leaf names, never paths. sanitize is unsuitable here: it
// permits '.' and '..' and must not turn untrusted input into another target.
func validSkillSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 128 {
		return false
	}
	for index, char := range slug {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '-' || char == '_') {
			continue
		}
		return false
	}
	return true
}

func parseGitHubRepository(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("Skill source must be an HTTPS github.com repository")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("Skill source must identify one GitHub repository")
	}
	repository := strings.TrimSuffix(parts[1], ".git")
	if repository == "" || strings.ContainsAny(parts[0]+repository, "\\\x00") {
		return "", "", fmt.Errorf("invalid GitHub repository path")
	}
	return parts[0], repository, nil
}

func resolveGitHubRevision(ctx context.Context, client *http.Client, owner, repository string) (string, string, error) {
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := getGitHubJSON(ctx, client, "https://api.github.com/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repository), &metadata); err != nil {
		return "", "", fmt.Errorf("resolve Skill repository: %w", err)
	}
	if metadata.DefaultBranch == "" {
		return "", "", fmt.Errorf("GitHub repository has no default branch")
	}
	var revision struct {
		SHA string `json:"sha"`
	}
	endpoint := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/commits/" + url.PathEscape(metadata.DefaultBranch)
	if err := getGitHubJSON(ctx, client, endpoint, &revision); err != nil {
		return "", "", fmt.Errorf("resolve Skill revision: %w", err)
	}
	if len(revision.SHA) < 12 {
		return "", "", fmt.Errorf("GitHub returned an invalid revision")
	}
	return metadata.DefaultBranch, revision.SHA, nil
}

func getGitHubJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Teamva-Skill-Installer")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
}

func downloadGitHubArchive(ctx context.Context, client *http.Client, owner, repository, commit, target string) error {
	endpoint := "https://codeload.github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/tar.gz/" + url.PathEscape(commit)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Teamva-Skill-Installer")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Skill archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Skill archive returned HTTP %d", response.StatusCode)
	}
	limited := &io.LimitedReader{R: response.Body, N: maxSkillArchiveBytes + 1}
	gzipReader, err := gzip.NewReader(limited)
	if err != nil {
		return fmt.Errorf("open Skill archive: %w", err)
	}
	defer gzipReader.Close()
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	reader := tar.NewReader(gzipReader)
	var files int
	var size int64
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read Skill archive: %w", nextErr)
		}
		parts := strings.Split(filepath.ToSlash(header.Name), "/")
		if len(parts) < 2 {
			continue
		}
		relative, pathErr := safeRelativePath(strings.Join(parts[1:], "/"))
		if pathErr != nil || relative == "." {
			continue
		}
		destination := filepath.Join(target, relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			size += header.Size
			if files > maxSkillFiles || size > maxSkillTreeBytes {
				return fmt.Errorf("Skill archive exceeds safety limits")
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return err
			}
			mode := fs.FileMode(0o600)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o700
			}
			file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			// Repository archives can legitimately contain convenience symlinks or
			// platform-specific entries outside the selected Skill subtree. Never
			// materialize or follow them: the later packaging step copies regular
			// files only, so ignoring these entries is portable and safe.
			continue
		}
	}
	if limited.N <= 0 {
		return fmt.Errorf("Skill archive exceeds download limit")
	}
	return nil
}

func safeRelativePath(value string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
	if cleaned == "" {
		cleaned = "."
	}
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes package root")
	}
	return cleaned, nil
}

func copySkillTreeContents(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill contains unsupported symlink %q", relative)
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Skill contains unsupported file %q", relative)
		}
		return copySkillFile(path, destination, info.Mode())
	})
}

func copySkillFile(source, target string, sourceMode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	mode := fs.FileMode(0o600)
	if sourceMode&0o111 != 0 {
		mode = 0o700
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, maxSkillTreeBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func inspectSkillTree(root string) (skillTreeInfo, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill contains unsupported symlink %q", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Skill contains unsupported file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return skillTreeInfo{}, err
	}
	sort.Strings(paths)
	if len(paths) > maxSkillFiles {
		return skillTreeInfo{}, fmt.Errorf("Skill contains too many files")
	}
	hash := sha256.New()
	result := skillTreeInfo{files: paths}
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return skillTreeInfo{}, err
		}
		result.size += info.Size()
		if result.size > maxSkillTreeBytes {
			return skillTreeInfo{}, fmt.Errorf("Skill exceeds size limit")
		}
		if filepath.Base(relative) == "SKILL.md" {
			result.skillFiles = append(result.skillFiles, relative)
		}
		if info.Mode()&0o111 != 0 {
			result.executables = append(result.executables, relative)
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		if info.Mode()&0o111 != 0 {
			_, _ = hash.Write([]byte("x"))
		}
		_, _ = hash.Write([]byte{0})
		file, err := os.Open(path)
		if err != nil {
			return skillTreeInfo{}, err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return skillTreeInfo{}, copyErr
		}
		if closeErr != nil {
			return skillTreeInfo{}, closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	result.digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}
