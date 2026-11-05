package gitread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const (
	gitCommandTimeout   = 15 * time.Second
	gitCommandWaitDelay = 2 * time.Second
)

type gitOutput struct {
	data      []byte
	truncated bool
}

type cappedWriter struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *cappedWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		write := len(payload)
		if write > remaining {
			write = remaining
		}
		_, _ = w.buffer.Write(payload[:write])
	}
	if len(payload) > remaining {
		w.overflow = true
		w.cancel()
	}
	return len(payload), nil
}

func (w *cappedWriter) output() gitOutput {
	w.mu.Lock()
	defer w.mu.Unlock()
	return gitOutput{data: w.buffer.Bytes(), truncated: w.overflow}
}

func resolveGitExecutable() (string, os.FileInfo, string, error) {
	if !platformGitSandboxSupported() {
		return "", nil, "", errors.New("safe Git sandbox is unavailable")
	}
	for _, candidate := range trustedGitExecutablePaths() {
		path, info, digest, err := resolveGitExecutableAt(candidate)
		if err == nil && platformTrustedGitExecutable(info) {
			return path, info, digest, nil
		}
	}
	return "", nil, "", errors.New("trusted Git executable is unavailable")
}

func resolveGitExecutableAt(path string) (string, os.FileInfo, string, error) {
	if !platformGitSandboxSupported() || !filepath.IsAbs(path) {
		return "", nil, "", errors.New("Git executable path is not an absolute trusted input")
	}
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return "", nil, "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, "", errors.New("Git executable is not a safe regular file")
	}
	digest, err := gitExecutableDigest(path, info)
	if err != nil {
		return "", nil, "", err
	}
	return path, info, digest, nil
}

func (s *Service) validateGitExecutable() error {
	if s.gitErr != nil || s.gitPath == "" || s.gitExec == "" || s.gitInfo == nil || s.gitExecInfo == nil ||
		s.gitHash == "" || s.gitFile == nil {
		return gitError("git_unsupported", "找不到可安全执行的 Git", false)
	}
	handleInfo, err := s.gitFile.Stat()
	if err != nil || !os.SameFile(s.gitExecInfo, handleInfo) {
		return gitError("git_unavailable", "固定的 Git 可执行文件身份已失效", false)
	}
	handleDigest, err := gitExecutableDigestHandle(s.gitFile, handleInfo)
	if err != nil || handleDigest != s.gitHash {
		return gitError("git_unavailable", "固定的 Git 可执行文件内容已变化", false)
	}
	execInfo, err := os.Lstat(s.gitExec)
	if err != nil || execInfo.Mode()&os.ModeSymlink != 0 || !execInfo.Mode().IsRegular() ||
		!os.SameFile(s.gitExecInfo, execInfo) {
		return gitError("git_unavailable", "受保护的 Git 可执行快照已失效", false)
	}
	return nil
}

func pinGitExecutable(path string, expected os.FileInfo, expectedHash string) (*os.File, string, string, os.FileInfo, error) {
	file, err := openIdentityRegular(path)
	if err != nil {
		return nil, "", "", nil, err
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(expected, info) {
		file.Close()
		return nil, "", "", nil, errors.New("Git executable changed while pinning")
	}
	directory, err := os.MkdirTemp("", "teamva-git-executable-")
	if err != nil {
		file.Close()
		return nil, "", "", nil, err
	}
	cleanup := func() {
		file.Close()
		_ = os.RemoveAll(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return nil, "", "", nil, err
	}
	executable := filepath.Join(directory, "git")
	destination, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500)
	if err != nil {
		cleanup()
		return nil, "", "", nil, err
	}
	written, copyErr := io.Copy(destination, io.NewSectionReader(file, 0, info.Size()+1))
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != info.Size() {
		cleanup()
		return nil, "", "", nil, errors.New("Git executable snapshot could not be created")
	}
	if err := os.Chmod(executable, 0o500); err != nil {
		cleanup()
		return nil, "", "", nil, err
	}
	execInfo, err := os.Lstat(executable)
	if err != nil || !execInfo.Mode().IsRegular() {
		cleanup()
		return nil, "", "", nil, errors.New("Git executable snapshot is invalid")
	}
	digest, err := gitExecutableDigest(executable, execInfo)
	if err != nil || digest != expectedHash {
		cleanup()
		return nil, "", "", nil, errors.New("Git executable snapshot digest mismatch")
	}
	pinned, err := openIdentityRegular(executable)
	if err != nil {
		cleanup()
		return nil, "", "", nil, err
	}
	file.Close()
	return pinned, executable, directory, execInfo, nil
}

func gitExecutableDigest(path string, expected os.FileInfo) (string, error) {
	const maxExecutableBytes = 128 << 20
	if expected == nil || expected.Size() < 0 || expected.Size() > maxExecutableBytes {
		return "", errors.New("Git executable exceeds identity budget")
	}
	file, err := openIdentityRegular(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(expected, current) || current.Size() != expected.Size() {
		return "", errors.New("Git executable changed while hashing")
	}
	return gitExecutableDigestHandle(file, current)
}

func gitExecutableDigestHandle(file *os.File, expected os.FileInfo) (string, error) {
	const maxExecutableBytes = 128 << 20
	if file == nil || expected == nil || expected.Size() < 0 || expected.Size() > maxExecutableBytes {
		return "", errors.New("Git executable exceeds identity budget")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.NewSectionReader(file, 0, expected.Size()+1))
	if err != nil || written != expected.Size() {
		return "", errors.New("Git executable could not be hashed safely")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *Service) beginServiceOperation() (func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, gitError("runtime_closed", "Git 读取服务已关闭", false)
	}
	if s.secretErr != nil {
		s.mu.Unlock()
		return nil, gitError("git_unavailable", "Git 读取服务无法建立安全令牌", false)
	}
	s.wg.Add(1)
	s.mu.Unlock()
	return s.wg.Done, nil
}

func (s *Service) runGit(ctx context.Context, repository *repository, limit int, allowExitOne bool, arguments ...string) (gitOutput, error) {
	done, err := s.beginServiceOperation()
	if err != nil {
		return gitOutput{}, err
	}
	defer done()
	if repository == nil {
		return gitOutput{}, gitError("git_unavailable", "Git 仓库上下文缺失", false)
	}
	if limit <= 0 || limit > patchOutputLimit {
		limit = patchOutputLimit
	}
	argvBytes := 0
	for _, argument := range arguments {
		argvBytes += len(argument) + 1
	}
	if argvBytes > gitSandboxMaxArgv {
		return gitOutput{}, gitError("invalid_argument", "Git 参数超过安全预算", false)
	}
	select {
	case s.gitSem <- struct{}{}:
	case <-ctx.Done():
		return gitOutput{}, ctx.Err()
	case <-s.ctx.Done():
		return gitOutput{}, gitError("runtime_closed", "Git 读取服务已关闭", false)
	}
	defer func() { <-s.gitSem }()

	commandContext, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	stopServiceCancel := context.AfterFunc(s.ctx, cancel)
	defer func() {
		stopServiceCancel()
		cancel()
	}()
	if err := s.validateGitExecutable(); err != nil {
		return gitOutput{}, err
	}
	sandbox, err := s.prepareGitSandbox(commandContext, repository)
	if err != nil {
		return gitOutput{}, s.classifyGitCommandError(ctx, commandContext, err)
	}
	defer sandbox.close()
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects", "--literal-pathspecs",
		"--git-dir=" + sandbox.gitDir, "--work-tree=" + sandbox.workTree,
		"-c", "color.ui=false", "-c", "core.fsmonitor=false",
		"-c", "diff.external=", "-c", "diff.trustExitCode=false",
		"-c", "status.relativePaths=false", "-c", "protocol.allow=never",
		"-c", "credential.helper=",
	}
	output, err := s.executeGitCommand(commandContext, sandbox, limit, append(global, arguments...))
	identityErr := repository.validateIdentity()
	executableErr := s.validateGitExecutable()
	if identityErr != nil || executableErr != nil {
		return gitOutput{}, gitError("snapshot_expired", "Git 仓库或执行环境在读取期间发生变化", true)
	}
	if output.truncated {
		return output, nil
	}
	if err == nil {
		return output, nil
	}
	var exitError *exec.ExitError
	if allowExitOne && errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return output, nil
	}
	return gitOutput{}, s.classifyGitCommandError(ctx, commandContext, err)
}

func (s *Service) executeGitCommand(ctx context.Context, sandbox *gitSandbox, limit int, arguments []string) (gitOutput, error) {
	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &cappedWriter{limit: limit, cancel: cancel}
	command := exec.CommandContext(commandContext, s.gitExec, arguments...)
	afterStart, cleanupProcess, processErr := configureGitProcess(command)
	if processErr != nil {
		return gitOutput{}, processErr
	}
	defer cleanupProcess()
	command.WaitDelay = gitCommandWaitDelay
	environment, environmentErr := safeGitEnvironment(sandbox)
	if environmentErr != nil {
		return gitOutput{}, environmentErr
	}
	command.Env = environment
	command.ExtraFiles = append([]*os.File(nil), sandbox.extraFiles...)
	command.Dir = sandbox.launchDirectory
	command.Stdout = writer
	command.Stderr = io.Discard
	err := command.Start()
	if err == nil {
		if err = afterStart(); err != nil {
			_ = command.Cancel()
			_ = command.Wait()
		} else {
			err = command.Wait()
		}
	}
	return writer.output(), err
}

func (s *Service) classifyGitCommandError(requestContext, commandContext context.Context, err error) error {
	if requestContext.Err() != nil {
		return requestContext.Err()
	}
	if s.ctx.Err() != nil {
		return gitError("runtime_closed", "Git 读取服务已关闭", false)
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return gitError("git_timeout", "Git 读取超时", true)
	}
	var readError *nodeprotocol.GitReadError
	if errors.As(err, &readError) {
		return err
	}
	return gitError("git_unavailable", "Git 无法读取该仓库", true)
}

func safeGitEnvironment(sandbox *gitSandbox) ([]string, error) {
	home := filepath.Join(sandbox.directory, "home")
	xdg := filepath.Join(sandbox.directory, "xdg")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		return nil, err
	}
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + xdg,
		"TMPDIR=" + sandbox.directory,
		"PATH=/usr/bin:/bin",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_ATTR_SOURCE=" + sandbox.emptyTree,
		"GIT_DIR=" + sandbox.gitDir,
		"GIT_WORK_TREE=" + sandbox.workTree,
		"GIT_INDEX_FILE=" + sandbox.indexFile,
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(sandbox.directory, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + sandbox.objectDirectory,
		"GIT_GRAFT_FILE=" + filepath.Join(sandbox.directory, "no-grafts"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_ALLOW_PROTOCOL=",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GCM_INTERACTIVE=never",
		"LC_ALL=C",
	}, nil
}
