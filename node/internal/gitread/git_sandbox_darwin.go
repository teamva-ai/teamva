//go:build darwin

package gitread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const gitSandboxMaxObjectBytes int64 = 64 << 30

func platformGitSandboxSupported() bool { return true }

func trustedGitExecutablePaths() []string {
	// /usr/bin/git is an Apple tool shim whose copied image silently stops
	// dispatching. Pin the actual CLT/Xcode binary so the protected executable
	// snapshot remains functional and independent of PATH.
	return []string{
		"/Library/Developer/CommandLineTools/usr/bin/git",
		"/Applications/Xcode.app/Contents/Developer/usr/bin/git",
	}
}

func platformTrustedGitExecutable(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && info.Mode().Perm()&0o022 == 0
}

type sandboxBoundDirectories struct {
	workTree string
	objects  string
}

func bindSandboxHandles(ctx context.Context, files []*os.File, objectSnapshot string, budget *sandboxBudget) (sandboxBoundDirectories, error) {
	if len(files) != 4 {
		return sandboxBoundDirectories{}, errors.New("invalid Git sandbox descriptor set")
	}
	root, err := darwinVnodePath(files[0])
	if err != nil {
		return sandboxBoundDirectories{}, err
	}
	if err := cloneDarwinObjectDirectory(ctx, files[1], objectSnapshot, budget); err != nil {
		return sandboxBoundDirectories{}, err
	}
	return sandboxBoundDirectories{workTree: root, objects: objectSnapshot}, nil
}

func darwinVnodePath(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("unsupported Darwin file identity")
	}
	path := fmt.Sprintf("/.vol/%d/%d", uint64(stat.Dev), uint64(stat.Ino))
	current, err := os.Stat(path)
	if err != nil || !current.IsDir() || !os.SameFile(current, info) {
		return "", errors.New("cannot bind Darwin worktree identity")
	}
	return path, nil
}

func cloneDarwinObjectDirectory(ctx context.Context, source *os.File, destination string, budget *sandboxBudget) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	type queuedDirectory struct {
		source      *os.File
		destination string
	}
	queue := []queuedDirectory{{source: source, destination: destination}}
	var active *os.File
	defer func() {
		if active != nil && active != source {
			_ = active.Close()
		}
		for _, item := range queue {
			if item.source != source {
				_ = item.source.Close()
			}
		}
	}()
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := queue[0]
		queue = queue[1:]
		active = item.source
		entries, err := item.source.ReadDir(256)
		for {
			for _, entry := range entries {
				if budget.files >= gitSandboxMaxFiles {
					return gitError("git_metadata_too_large", "Git 对象数量超过安全快照预算", false)
				}
				name := entry.Name()
				destinationPath := filepath.Join(item.destination, name)
				var stat unix.Stat_t
				if statErr := unix.Fstatat(int(item.source.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
					return statErr
				}
				mode := stat.Mode & unix.S_IFMT
				switch mode {
				case unix.S_IFDIR:
					fd, openErr := unix.Openat(int(item.source.Fd()), name,
						unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
					if openErr != nil {
						return openErr
					}
					var opened unix.Stat_t
					if statErr := unix.Fstat(fd, &opened); statErr != nil || opened.Mode&unix.S_IFMT != unix.S_IFDIR ||
						opened.Dev != stat.Dev || opened.Ino != stat.Ino {
						unix.Close(fd)
						return gitError("snapshot_expired", "Git 对象目录在快照期间被替换", true)
					}
					if mkdirErr := os.Mkdir(destinationPath, 0o700); mkdirErr != nil {
						unix.Close(fd)
						return mkdirErr
					}
					directory := os.NewFile(uintptr(fd), "git-object-directory")
					if directory == nil {
						unix.Close(fd)
						return errors.New("Git object directory handle is unavailable")
					}
					queue = append(queue, queuedDirectory{source: directory, destination: destinationPath})
					budget.files++
				case unix.S_IFREG:
					fd, openErr := unix.Openat(int(item.source.Fd()), name,
						unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
					if openErr != nil {
						return openErr
					}
					var pinned unix.Stat_t
					if statErr := unix.Fstat(fd, &pinned); statErr != nil || pinned.Mode&unix.S_IFMT != unix.S_IFREG ||
						pinned.Dev != stat.Dev || pinned.Ino != stat.Ino {
						unix.Close(fd)
						return gitError("snapshot_expired", "Git 对象在快照期间被替换", true)
					}
					size := pinned.Size
					if size < 0 || budget.objectBytes+size > gitSandboxMaxObjectBytes {
						unix.Close(fd)
						return gitError("git_metadata_too_large", "Git 对象超过安全快照预算", false)
					}
					cloneErr := unix.Fclonefileat(fd, unix.AT_FDCWD, destinationPath, unix.CLONE_NOOWNERCOPY)
					unix.Close(fd)
					if cloneErr != nil {
						return gitError("git_unsupported", "当前文件系统无法创建受保护的 Git 对象快照", false)
					}
					cloned, statErr := os.Lstat(destinationPath)
					if statErr != nil || !cloned.Mode().IsRegular() || cloned.Size() != size {
						return gitError("snapshot_expired", "Git 对象快照身份不一致", true)
					}
					if chmodErr := os.Chmod(destinationPath, 0o400); chmodErr != nil {
						return chmodErr
					}
					budget.files++
					budget.objectBytes += size
				default:
					return gitError("git_unsupported", "Git 对象目录包含不安全的特殊文件", false)
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			entries, err = item.source.ReadDir(256)
		}
		if item.source != source {
			_ = item.source.Close()
		}
		active = nil
	}
	return nil
}
