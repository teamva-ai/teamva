package gitread

import (
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	gitSandboxMaxFiles = 100_000
	gitSandboxMaxBytes = 64 << 20
	gitSandboxMaxArgv  = 64 << 10
)

type sandboxBudget struct {
	files       int
	bytes       int64
	objectBytes int64
}

type gitSandbox struct {
	directory       string
	gitDir          string
	workTree        string
	launchDirectory string
	objectDirectory string
	sourceGitHandle *os.File
	sourceCommon    *os.File
	indexFile       string
	emptyTree       string
	extraFiles      []*os.File
}

func (sandbox *gitSandbox) close() {
	for _, file := range sandbox.extraFiles {
		_ = file.Close()
	}
	if sandbox.directory != "" {
		_ = os.RemoveAll(sandbox.directory)
	}
}

func (s *Service) prepareGitSandbox(ctx context.Context, repository *repository) (*gitSandbox, error) {
	if !platformGitSandboxSupported() {
		return nil, gitError("git_unsupported", "当前平台无法提供安全的 Git 读取隔离", false)
	}
	if err := repository.validateIdentity(); err != nil {
		return nil, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	repository.handleMu.Lock()
	if repository.handlesClosed {
		repository.handleMu.Unlock()
		return nil, gitError("snapshot_expired", "Git 仓库路径已失效", true)
	}
	rootHandle, rootErr := duplicateIdentityFile(repository.rootIdentity.handle)
	var objectHandle, gitHandle, commonHandle *os.File
	if rootErr == nil {
		objectHandle, rootErr = duplicateIdentityFile(repository.objectIdentity.handle)
	}
	if rootErr == nil {
		gitHandle, rootErr = duplicateIdentityFile(repository.gitIdentity.handle)
	}
	if rootErr == nil {
		commonHandle, rootErr = duplicateIdentityFile(repository.commonIdentity.handle)
	}
	repository.handleMu.Unlock()
	if rootErr != nil {
		if rootHandle != nil {
			rootHandle.Close()
		}
		if objectHandle != nil {
			objectHandle.Close()
		}
		if gitHandle != nil {
			gitHandle.Close()
		}
		if commonHandle != nil {
			commonHandle.Close()
		}
		return nil, rootErr
	}
	directory, err := os.MkdirTemp("", "teamva-gitread-")
	if err != nil {
		rootHandle.Close()
		objectHandle.Close()
		gitHandle.Close()
		commonHandle.Close()
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		rootHandle.Close()
		objectHandle.Close()
		gitHandle.Close()
		commonHandle.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	sandbox := &gitSandbox{directory: directory, gitDir: filepath.Join(directory, "git"),
		objectDirectory: filepath.Join(directory, "original-objects"),
		sourceGitHandle: gitHandle, sourceCommon: commonHandle,
		extraFiles: []*os.File{rootHandle, objectHandle, gitHandle, commonHandle}}
	budget := &sandboxBudget{}
	bound, err := bindSandboxHandles(ctx, sandbox.extraFiles, sandbox.objectDirectory, budget)
	if err != nil {
		sandbox.close()
		return nil, err
	}
	sandbox.launchDirectory = bound.workTree
	sandbox.objectDirectory = bound.objects
	// The source-side check in copyRepositoryMetadata is intentionally kept,
	// but is not sufficient on its own: objects/info can be atomically replaced
	// after that check while a platform clone is in progress. Validate the
	// private, immutable copy as well, before Git is allowed to observe it.
	if err := validateClonedObjectAlternates(ctx, sandbox.objectDirectory); err != nil {
		sandbox.close()
		return nil, err
	}
	sandbox.workTree = "."
	for _, path := range []string{sandbox.gitDir, filepath.Join(sandbox.gitDir, "info"),
		filepath.Join(sandbox.gitDir, "refs"), filepath.Join(directory, "objects")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			sandbox.close()
			return nil, err
		}
	}
	if err := copyRepositoryMetadata(ctx, repository, sandbox, budget); err != nil {
		sandbox.close()
		return nil, err
	}
	if err := writeSandboxPolicy(repository.settings, sandbox); err != nil {
		sandbox.close()
		return nil, err
	}
	emptyTree, err := writeEmptyTreeObject(filepath.Join(directory, "objects"), repository.settings.objectFormat)
	if err != nil {
		sandbox.close()
		return nil, err
	}
	sandbox.emptyTree = emptyTree
	if err := repository.validateIdentity(); err != nil {
		sandbox.close()
		return nil, gitError("snapshot_expired", "Git 仓库身份在读取准备期间发生变化", true)
	}
	return sandbox, nil
}

// validateClonedObjectAlternates rejects every alternates declaration in the
// completed private object snapshot. Git follows alternates recursively, so a
// check limited to the top-level objects/info directory would still allow a
// nested object directory to escape the snapshot. The walk is handle-relative,
// nofollow, and bounded independently of the source-side pre-check.
func validateClonedObjectAlternates(ctx context.Context, objectDirectory string) error {
	identity, err := os.Lstat(objectDirectory)
	if err != nil || !identity.IsDir() || identity.Mode()&os.ModeSymlink != 0 {
		return gitError("snapshot_expired", "Git 对象快照目录身份无效", true)
	}
	root, err := os.Open(objectDirectory)
	if err != nil {
		return err
	}
	defer root.Close()
	openedIdentity, err := root.Stat()
	if err != nil || !os.SameFile(identity, openedIdentity) {
		return gitError("snapshot_expired", "Git 对象快照目录在检查前被替换", true)
	}
	type queuedDirectory struct {
		handle   *os.File
		relative string
	}
	queue := []queuedDirectory{{handle: root}}
	var active *os.File
	defer func() {
		if active != nil && active != root {
			_ = active.Close()
		}
		for _, item := range queue {
			if item.handle != root {
				_ = item.handle.Close()
			}
		}
	}()
	seen := 0
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := queue[0]
		queue = queue[1:]
		active = item.handle
		entries, readErr := item.handle.ReadDir(256)
		for {
			for _, entry := range entries {
				seen++
				if seen > gitSandboxMaxFiles {
					return gitError("git_metadata_too_large", "Git 对象快照检查超过安全预算", false)
				}
				name := entry.Name()
				mode, statErr := sandboxEntryModeAt(item.handle, name)
				if statErr != nil {
					return gitError("snapshot_expired", "Git 对象快照在检查期间发生变化", true)
				}
				relative := name
				if item.relative != "" {
					relative = item.relative + "/" + name
				}
				if mode == sandboxModeDirectory() {
					directory, openErr := openSandboxDirectoryAt(item.handle, name)
					if openErr != nil {
						return gitError("snapshot_expired", "Git 对象快照目录在检查期间发生变化", true)
					}
					queue = append(queue, queuedDirectory{handle: directory, relative: relative})
					continue
				}
				base := filepath.Base(relative)
				parent := filepath.Base(filepath.Dir(relative))
				isAlternate := parent == "info" && (base == "alternates" || base == "http-alternates")
				if !isAlternate {
					continue
				}
				if mode != sandboxModeRegular() {
					return gitError("git_unsupported", "Git 对象快照包含不安全的 alternates 条目", false)
				}
				file, info, openErr := openSandboxRegularAt(item.handle, name)
				if openErr != nil {
					return gitError("snapshot_expired", "Git alternates 快照在检查期间发生变化", true)
				}
				if info.Size() < 0 || info.Size() > maxGitMarkerBytes {
					_ = file.Close()
					return gitError("git_metadata_too_large", "Git alternates 条目超过安全预算", false)
				}
				_, readErr := readBoundedFile(ctx, file, maxGitMarkerBytes)
				_ = file.Close()
				if readErr != nil {
					return readErr
				}
				return gitError("git_unsupported", "包含对象 alternates 的仓库暂不支持安全读取", false)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
			entries, readErr = item.handle.ReadDir(256)
		}
		if item.handle != root {
			_ = item.handle.Close()
		}
		active = nil
	}
	return nil
}

func copyRepositoryMetadata(ctx context.Context, repository *repository, sandbox *gitSandbox, budget *sandboxBudget) error {
	for _, alternate := range []string{"objects/info/alternates", "objects/info/http-alternates"} {
		file, _, err := openSandboxRegularAt(sandbox.sourceCommon, alternate)
		if err == nil {
			_ = file.Close()
			return gitError("git_unsupported", "包含对象 alternates 的仓库暂不支持安全读取", false)
		} else if !errors.Is(err, os.ErrNotExist) {
			// Any existing non-regular entry is also unsafe. Distinguish it from a
			// genuinely absent optional file using a nofollow parent lookup.
			parent, parentErr := openSandboxDirectoryAt(sandbox.sourceCommon, "objects/info")
			if parentErr == nil {
				_, entryErr := sandboxEntryModeAt(parent, filepath.Base(alternate))
				_ = parent.Close()
				if entryErr == nil {
					return gitError("git_unsupported", "Git 对象 alternates 条目不安全", false)
				}
				if !errors.Is(entryErr, os.ErrNotExist) {
					return entryErr
				}
			} else if !errors.Is(parentErr, os.ErrNotExist) {
				return parentErr
			}
		}
	}
	if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, "HEAD", sandbox.gitDir, "HEAD", budget, maxGitMarkerBytes); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(sandbox.gitDir, "HEAD")); err != nil {
		return gitError("git_unavailable", "Git HEAD 不可读取", false)
	}
	if err := copyOptionalTree(ctx, sandbox.sourceCommon, "refs", sandbox.gitDir, "refs", budget); err != nil {
		return err
	}
	if !samePath(repository.gitDir, repository.commonDir) {
		if err := copyOptionalTree(ctx, sandbox.sourceGitHandle, "refs", sandbox.gitDir, "refs", budget); err != nil {
			return err
		}
	}
	for _, name := range []string{"packed-refs", "shallow"} {
		if err := copyOptionalRegular(ctx, sandbox.sourceCommon, name, sandbox.gitDir, name, budget, 16<<20); err != nil {
			return err
		}
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_HEAD"} {
		if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, name, sandbox.gitDir, name, budget, maxGitMarkerBytes); err != nil {
			return err
		}
	}
	sandbox.indexFile = filepath.Join(sandbox.gitDir, "index")
	if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, "index", sandbox.gitDir, "index", budget, gitSandboxMaxBytes); err != nil {
		return err
	}
	if err := forEachDirectoryHandleEntry(ctx, sandbox.sourceGitHandle, 256, gitSandboxMaxFiles, func(entry os.DirEntry) error {
		if strings.HasPrefix(entry.Name(), "sharedindex.") {
			if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, entry.Name(), sandbox.gitDir, entry.Name(), budget, gitSandboxMaxBytes); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if repository.settings.sparseCheckout {
		if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, "info/sparse-checkout", sandbox.gitDir,
			"info/sparse-checkout", budget, 4<<20); err != nil {
			return err
		}
	}
	if err := copyOptionalRegular(ctx, sandbox.sourceGitHandle, "info/exclude", sandbox.gitDir, "info/exclude", budget, 4<<20); err != nil {
		return err
	}
	return nil
}

func copyOptionalRegular(ctx context.Context, sourceRoot *os.File, sourceRelative, destinationRoot, destinationRelative string,
	budget *sandboxBudget, perFileLimit int) error {
	file, info, err := openSandboxRegularAt(sourceRoot, sourceRelative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return gitError("git_unavailable", "Git 元数据不是安全的普通文件", false)
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > int64(perFileLimit) || budget.files >= gitSandboxMaxFiles ||
		budget.bytes+info.Size() > gitSandboxMaxBytes {
		return gitError("git_metadata_too_large", "Git 元数据超过安全复制预算", false)
	}
	payload, readErr := readBoundedFile(ctx, file, perFileLimit)
	if readErr != nil {
		return readErr
	}
	if int64(len(payload)) != info.Size() {
		return gitError("snapshot_expired", "Git 元数据在读取期间发生变化", true)
	}
	destination := filepath.Join(destinationRoot, filepath.FromSlash(destinationRelative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(destination, payload, 0o600); err != nil {
		return err
	}
	budget.files++
	budget.bytes += int64(len(payload))
	return nil
}

func copyOptionalTree(ctx context.Context, sourceRoot *os.File, sourceRelative, destinationRoot, destinationRelative string,
	budget *sandboxBudget) error {
	source, err := openSandboxDirectoryAt(sourceRoot, sourceRelative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return gitError("git_unavailable", "Git refs 目录不安全", false)
	}
	type queued struct {
		source      *os.File
		destination string
	}
	queue := []queued{{source: source, destination: filepath.Join(destinationRoot, filepath.FromSlash(destinationRelative))}}
	defer func() {
		for _, item := range queue {
			_ = item.source.Close()
		}
	}()
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := queue[0]
		queue = queue[1:]
		if err := os.MkdirAll(item.destination, 0o700); err != nil {
			_ = item.source.Close()
			return err
		}
		if err := forEachOpenDirectoryEntry(ctx, item.source, 256, gitSandboxMaxFiles, func(entry os.DirEntry) error {
			if budget.files >= gitSandboxMaxFiles {
				return gitError("git_metadata_too_large", "Git refs 数量超过安全预算", false)
			}
			destinationPath := filepath.Join(item.destination, entry.Name())
			mode, statErr := sandboxEntryModeAt(item.source, entry.Name())
			if statErr != nil {
				return gitError("git_unavailable", "Git ref 不是安全文件", false)
			}
			if mode == sandboxModeDirectory() {
				directory, openErr := openSandboxDirectoryAt(item.source, entry.Name())
				if openErr != nil {
					return openErr
				}
				budget.files++
				queue = append(queue, queued{source: directory, destination: destinationPath})
				return nil
			}
			if mode != sandboxModeRegular() {
				return gitError("git_unavailable", "Git ref 不是普通文件", false)
			}
			if err := copyRegularFromDirectory(ctx, item.source, entry.Name(), destinationPath,
				budget, maxGitMarkerBytes); err != nil {
				return err
			}
			return nil
		}); err != nil {
			_ = item.source.Close()
			return err
		}
		_ = item.source.Close()
	}
	return nil
}

func copyRegularFromDirectory(ctx context.Context, sourceDirectory *os.File, name, destination string,
	budget *sandboxBudget, perFileLimit int) error {
	file, info, err := openSandboxRegularAt(sourceDirectory, name)
	if err != nil {
		return gitError("git_unavailable", "Git ref 不是安全的普通文件", false)
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > int64(perFileLimit) || budget.files >= gitSandboxMaxFiles ||
		budget.bytes+info.Size() > gitSandboxMaxBytes {
		return gitError("git_metadata_too_large", "Git 元数据超过安全复制预算", false)
	}
	payload, err := readBoundedFile(ctx, file, perFileLimit)
	if err != nil || int64(len(payload)) != info.Size() {
		return gitError("snapshot_expired", "Git ref 在读取期间发生变化", true)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(destination, payload, 0o600); err != nil {
		return err
	}
	budget.files++
	budget.bytes += int64(len(payload))
	return nil
}

func forEachDirectoryHandleEntry(ctx context.Context, root *os.File, batch, limit int, visit func(os.DirEntry) error) error {
	directory, err := openSandboxDirectoryAt(root, "")
	if err != nil {
		return err
	}
	defer directory.Close()
	return forEachOpenDirectoryEntry(ctx, directory, batch, limit, visit)
}

func forEachOpenDirectoryEntry(ctx context.Context, directory *os.File, batch, limit int, visit func(os.DirEntry) error) error {
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := directory.ReadDir(batch)
		for _, entry := range entries {
			seen++
			if seen > limit {
				return gitError("git_metadata_too_large", "Git 目录条目超过安全扫描预算", false)
			}
			if visitErr := visit(entry); visitErr != nil {
				return visitErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func writeSandboxPolicy(settings repositorySettings, sandbox *gitSandbox) error {
	attributes := "* -filter !diff !merge -working-tree-encoding\n** -filter !diff !merge -working-tree-encoding\n"
	if err := os.WriteFile(filepath.Join(sandbox.gitDir, "info", "attributes"), []byte(attributes), 0o600); err != nil {
		return err
	}
	var config strings.Builder
	formatVersion := 0
	if settings.objectFormat == "sha256" {
		formatVersion = 1
	}
	fmt.Fprintf(&config, "[core]\n\trepositoryFormatVersion = %d\n\tbare = false\n\tworktree = ", formatVersion)
	config.WriteString(quoteGitConfigValue(sandbox.workTree))
	config.WriteString("\n\tfsmonitor = false\n\thooksPath = ")
	config.WriteString(quoteGitConfigValue(filepath.Join(sandbox.directory, "no-hooks")))
	config.WriteString("\n\tattributesFile = ")
	config.WriteString(quoteGitConfigValue(filepath.Join(sandbox.gitDir, "info", "attributes")))
	config.WriteByte('\n')
	writeOptionalBool := func(name string, value *bool) {
		if value != nil {
			fmt.Fprintf(&config, "\t%s = %t\n", name, *value)
		}
	}
	writeOptionalBool("fileMode", settings.fileMode)
	writeOptionalBool("ignoreCase", settings.ignoreCase)
	writeOptionalBool("symlinks", settings.symlinks)
	writeOptionalBool("precomposeUnicode", settings.precomposeUnicode)
	if settings.sparseCheckout {
		config.WriteString("\tsparseCheckout = true\n")
	}
	if settings.sparseCheckoutCone {
		config.WriteString("\tsparseCheckoutCone = true\n")
	}
	if settings.indexSparse {
		config.WriteString("[index]\n\tsparse = true\n")
	}
	if settings.objectFormat == "sha256" {
		config.WriteString("[extensions]\n\tobjectFormat = sha256\n")
	}
	return os.WriteFile(filepath.Join(sandbox.gitDir, "config"), []byte(config.String()), 0o600)
}

func quoteGitConfigValue(value string) string {
	return strconv.Quote(value)
}

func writeEmptyTreeObject(objectsDir, format string) (string, error) {
	payload := []byte("tree 0\x00")
	var objectID string
	if format == "sha256" {
		digest := sha256.Sum256(payload)
		objectID = hex.EncodeToString(digest[:])
	} else {
		digest := sha1.Sum(payload)
		objectID = hex.EncodeToString(digest[:])
	}
	directory := filepath.Join(objectsDir, objectID[:2])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(directory, objectID[2:]), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	writer := zlib.NewWriter(file)
	_, writeErr := writer.Write(payload)
	closeWriterErr := writer.Close()
	closeFileErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeWriterErr != nil {
		return "", closeWriterErr
	}
	return objectID, closeFileErr
}
