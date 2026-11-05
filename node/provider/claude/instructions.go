package claude

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"unicode/utf8"
)

const maxWorkspaceInstructionBytes = 1 << 20

// WorkspaceInstructions is the host-generated AGENTS.md filename, not prompt
// text. Bare mode skips auto-discovery, so a missing/unsafe file must fail closed.
func readWorkspaceInstructions(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("workspace instructions must name an absolute file")
	}
	path, err := absoluteScopePath(path)
	if err != nil {
		return "", err
	}
	parent, err := pinDirectory(filepath.Dir(path))
	if err != nil {
		return "", errors.New("workspace instruction directory is unavailable or unsafe")
	}
	defer parent.close()
	root, err := parent.clone()
	if err != nil {
		return "", errors.New("workspace instruction directory identity changed")
	}
	defer root.Close()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || !singleLink(before) || before.Size() > maxWorkspaceInstructionBytes {
		return "", errors.New("workspace instructions must be a bounded single-link regular file")
	}
	file, err := root.OpenFile(name, noFollowReadFlags(false), 0)
	if err != nil {
		return "", errors.New("open workspace instructions failed")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !singleLink(info) || info.Size() > maxWorkspaceInstructionBytes {
		return "", errors.New("workspace instructions are not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspaceInstructionBytes+1))
	if err != nil || len(data) > maxWorkspaceInstructionBytes || !utf8.Valid(data) {
		return "", errors.New("workspace instructions must be valid UTF-8 within the 1 MiB limit")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}
