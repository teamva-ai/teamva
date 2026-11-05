//go:build linux

package gitread

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const gitSandboxMaxObjectBytes int64 = 64 << 30

func platformGitSandboxSupported() bool { return true }

func trustedGitExecutablePaths() []string { return []string{"/usr/bin/git", "/bin/git"} }

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
	workTree, err := linuxBoundDirectoryPath(files[0])
	if err != nil {
		return sandboxBoundDirectories{}, err
	}
	if err := cloneLinuxObjectDirectory(ctx, files[1], objectSnapshot, budget); err != nil {
		return sandboxBoundDirectories{}, err
	}
	return sandboxBoundDirectories{workTree: workTree, objects: objectSnapshot}, nil
}

func linuxBoundDirectoryPath(file *os.File) (string, error) {
	path := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.FormatUint(uint64(file.Fd()), 10)
	current, err := os.Stat(path)
	handleInfo, handleErr := file.Stat()
	if err != nil || handleErr != nil || !current.IsDir() || !os.SameFile(current, handleInfo) {
		return "", errors.New("cannot bind Git sandbox directory identity")
	}
	return path, nil
}

func cloneLinuxObjectDirectory(ctx context.Context, source *os.File, destination string, budget *sandboxBudget) error {
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
				switch stat.Mode & unix.S_IFMT {
				case unix.S_IFDIR:
					fd, openErr := unix.Openat(int(item.source.Fd()), name,
						unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
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
					if err := cloneLinuxObjectFile(item.source, name, destinationPath, stat, budget); err != nil {
						return err
					}
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

func cloneLinuxObjectFile(sourceDirectory *os.File, name, destination string, expected unix.Stat_t, budget *sandboxBudget) error {
	sourceFD, err := unix.Openat(int(sourceDirectory.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer unix.Close(sourceFD)
	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil || sourceStat.Mode&unix.S_IFMT != unix.S_IFREG ||
		sourceStat.Dev != expected.Dev || sourceStat.Ino != expected.Ino {
		return gitError("snapshot_expired", "Git 对象在快照期间被替换", true)
	}
	if sourceStat.Size < 0 || budget.objectBytes+sourceStat.Size > gitSandboxMaxObjectBytes {
		return gitError("git_metadata_too_large", "Git 对象超过安全快照预算", false)
	}
	destinationFD, err := unix.Open(destination,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
	if err != nil {
		return err
	}
	cloneErr := unix.IoctlFileClone(destinationFD, sourceFD)
	var destinationStat unix.Stat_t
	statErr := unix.Fstat(destinationFD, &destinationStat)
	closeErr := unix.Close(destinationFD)
	if cloneErr != nil {
		return gitError("git_unsupported", "当前文件系统无法创建受保护的 Git 对象快照", false)
	}
	if statErr != nil || closeErr != nil || destinationStat.Mode&unix.S_IFMT != unix.S_IFREG ||
		destinationStat.Size != sourceStat.Size {
		return gitError("snapshot_expired", "Git 对象快照身份不一致", true)
	}
	budget.files++
	budget.objectBytes += sourceStat.Size
	return nil
}
