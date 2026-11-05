package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/internal/gitread"
	"github.com/teamva-ai/teamva/node/internal/localstore"
	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type Runtime struct {
	ctx              context.Context
	cancel           context.CancelFunc
	computerID       string
	store            *localstore.Store
	adapters         map[string]adapter.Adapter
	providerMetadata map[string]adapter.Metadata
	executables      map[string]string
	workspaceRoot    string
	managers         map[string]*Manager
	managersMu       sync.Mutex
	runStarts        map[string]*runStartRegistration
	runStartsMu      sync.Mutex
	activeHandles    sync.WaitGroup
	activeGitReads   sync.WaitGroup
	closing          bool
	closeDone        chan struct{}
	closeErr         error
	toolCaller       ToolCaller
	mcpMu            sync.RWMutex
	mcpResolver      func(context.Context, string) (map[string]json.RawMessage, error)
	gitReader        *gitread.Service
	now              func() time.Time

	activeGenerations sync.WaitGroup
	titleDirectories  *titleDirectoryOwner
	// titleManagerFactory is a test-only seam shared by isolated generations.
	// Production always creates an independent Manager, never an Issue instance.
	titleManagerFactory func(provider, executable, root string) *Manager
}

// SetToolCaller 注入 node→server 工具 RPC 通道（由 daemon 在连接建立后设置）。
func (r *Runtime) SetToolCaller(caller ToolCaller) {
	r.toolCaller = caller
}

func New(
	computerID string,
	store *localstore.Store,
	adapters []adapter.Adapter,
	workspaceRoot string,
	executables map[string]string,
) *Runtime {
	runtimeContext, runtimeCancel := context.WithCancel(context.Background())
	byName := make(map[string]adapter.Adapter, len(adapters))
	for _, item := range adapters {
		byName[item.Name()] = item
	}
	executablePaths := make(map[string]string, len(executables))
	for provider, executable := range executables {
		if executable != "" {
			executablePaths[provider] = executable
		}
	}
	metadata := make(map[string]adapter.Metadata)
	for _, definition := range catalog.Definitions() {
		metadata[definition.Metadata.ID] = definition.Metadata
	}
	// Reap crash leftovers during startup, even when no new title is requested.
	// A locked/unsafe title scope fails only the optional title facade closed;
	// ordinary execution must not borrow another Runtime's title directories.
	titleDirectories, _ := openTitleDirectoryOwner(workspaceRoot)
	return &Runtime{
		ctx: runtimeContext, cancel: runtimeCancel,
		computerID: computerID, store: store, adapters: byName,
		providerMetadata: metadata,
		executables:      executablePaths,
		workspaceRoot:    workspaceRoot,
		managers:         make(map[string]*Manager),
		runStarts:        make(map[string]*runStartRegistration),
		gitReader:        gitread.NewWithResourceAuthorizationDirectory(store.ResourceAuthorizationDirectory()),
		closeDone:        make(chan struct{}),
		now:              func() time.Time { return time.Now().UTC() },
		titleDirectories: titleDirectories,
	}
}

// runStartRegistration only exists while one exact run.start is preparing or
// executing. It is deliberately not a stop tombstone: unregistering the start
// removes the only map entry, so completed run IDs cannot accumulate forever.
type runStartRegistration struct {
	cancel      context.CancelFunc
	forceFinish func(string) error
	settled     <-chan struct{}
	done        chan struct{}
	// A stop acknowledged before provider preparation completed must not make
	// a later finalizer create a second terminal after the server's fallback.
	earlyForceStop bool
}

func (r *Runtime) registerRunStart(parent context.Context, runID string) (context.Context, func(), error) {
	startContext, cancel := context.WithCancel(parent)
	registration := &runStartRegistration{cancel: cancel, done: make(chan struct{})}
	r.runStartsMu.Lock()
	if _, exists := r.runStarts[runID]; exists {
		r.runStartsMu.Unlock()
		cancel()
		return nil, nil, codedError("run_already_active", "同一 Run 已经在准备或执行")
	}
	r.runStarts[runID] = registration
	r.runStartsMu.Unlock()
	return startContext, func() {
		r.runStartsMu.Lock()
		if r.runStarts[runID] == registration {
			delete(r.runStarts, runID)
			close(registration.done)
		}
		r.runStartsMu.Unlock()
		cancel()
	}, nil
}

// cancelRunStart linearizes a stop request against registration. Cancellation
// happens before run.stop looks up an adapter, so every preparation operation
// and the final Start/Resume gate observe the stop even when no Provider is
// active yet.
func (r *Runtime) cancelRunStart(runID string) bool {
	r.runStartsMu.Lock()
	registration := r.runStarts[runID]
	r.runStartsMu.Unlock()
	if registration == nil {
		return false
	}
	registration.cancel()
	return true
}

// ListGitRepositories, ListGitChanges and ReadGitDiff form an ephemeral,
// read-only path. They deliberately bypass Handle and the durable command
// inbox, so patches can never be persisted as command results.
func (r *Runtime) ListGitRepositories(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	reader, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	defer done()
	return reader.ListRepositories(readContext, request)
}

func (r *Runtime) ListGitChanges(ctx context.Context, request nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	reader, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nodeprotocol.GitChangesPage{}, err
	}
	defer done()
	return reader.ListChanges(readContext, request)
}

func (r *Runtime) ReadGitDiff(ctx context.Context, request nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	reader, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nodeprotocol.GitDiffResult{}, err
	}
	defer done()
	return reader.Diff(readContext, request)
}

func (r *Runtime) RefreshGit(ctx context.Context, request nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	reader, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nodeprotocol.GitRefreshResult{}, err
	}
	defer done()
	return reader.Refresh(readContext, request)
}

func (r *Runtime) SubscribeGitEvents(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	reader, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	return reader.Subscribe(readContext, request)
}

// GetRunGitSnapshots is a direct localstore read. Like the live Git read
// methods it bypasses Handle and therefore cannot copy patches into command
// results or inbox_commands.
func (r *Runtime) GetRunGitSnapshots(ctx context.Context, runID string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	_, readContext, done, err := r.beginGitRead(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return r.store.GetRunGitSnapshots(readContext, runID)
}

func (r *Runtime) gitReadService() (*gitread.Service, error) {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	if r.closing || r.gitReader == nil {
		return nil, &nodeprotocol.GitReadError{Code: "runtime_closed", Message: "Git read service is closed."}
	}
	return r.gitReader, nil
}

func (r *Runtime) beginGitRead(ctx context.Context) (*gitread.Service, context.Context, func(), error) {
	r.managersMu.Lock()
	if r.closing || r.gitReader == nil {
		r.managersMu.Unlock()
		return nil, nil, nil, &nodeprotocol.GitReadError{Code: "runtime_closed", Message: "Git read service is closed."}
	}
	reader := r.gitReader
	r.activeGitReads.Add(1)
	r.managersMu.Unlock()
	readContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			r.activeGitReads.Done()
		})
	}
	return reader, readContext, done, nil
}

// managerFor 按 provider 返回（或创建）实例管理器。
// 每个 provider 一个管理器；实例按 (instance_id, generation, repository) 隔离。
func (r *Runtime) managerFor(provider, executable, rootDir string) (*Manager, error) {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	if r.closing {
		return nil, codedError("runtime_closed", "Runtime 正在关闭")
	}
	if existing := r.managers[provider]; existing != nil {
		return existing, nil
	}
	if _, ok := catalog.Lookup(provider); !ok {
		return nil, codedError("provider_unsupported", "不支持的 Provider")
	}
	logger := log.New(os.Stderr, "runtime/"+provider+" ", log.LstdFlags)
	manager := NewManager(provider, executable, rootDir, r.store, logger)
	retentionContext, cancelRetention := context.WithTimeout(context.Background(), 5*time.Second)
	if err := manager.pruneConfigDirs(retentionContext); err != nil {
		logger.Printf("runtime manager startup config retention failed provider=%s", provider)
	}
	cancelRetention()
	r.managers[provider] = manager
	return manager, nil
}

// adapterForRun 按 runID 返回承载它的实例 adapter。manager 已存在但映射已
// 解除时必须返回 nil，不能误把 stop/approval 发给仅用于 Probe 的 provider 单例。
func (r *Runtime) adapterForRun(runID, provider string) adapter.Adapter {
	r.managersMu.Lock()
	manager := r.managers[provider]
	r.managersMu.Unlock()
	if manager != nil {
		if item := manager.AdapterForRun(runID); item != nil {
			return item
		}
		log.Printf("adapterForRun: manager %s has no run %s (instances=%d)", provider, runID, manager.InstanceCount())
		return nil
	}
	return nil
}

// RecycleIdleProvider synchronously closes idle managed instances for one
// Provider. It never interrupts an active Run and reports any remaining active
// instance so callers can retry after the Run's durable terminal is projected.
func (r *Runtime) RecycleIdleProvider(ctx context.Context, provider string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	result := nodeprotocol.ProviderRuntimeRecycleResult{Provider: provider}
	if _, ok := catalog.Lookup(provider); !ok {
		return result, codedError("provider_unsupported", "不支持的 Provider")
	}
	r.managersMu.Lock()
	if r.closing {
		r.managersMu.Unlock()
		return result, codedError("runtime_closed", "Runtime 正在关闭")
	}
	manager := r.managers[provider]
	r.managersMu.Unlock()
	if manager == nil {
		return result, nil
	}
	recycled, active, remaining, err := manager.RecycleIdleNow(ctx)
	result.RecycledInstances = recycled
	result.ActiveInstances = active
	result.RemainingInstances = remaining
	return result, err
}

// RegisterTestAdapter 注入测试用 adapter（按 provider 名）。
func (r *Runtime) RegisterTestAdapter(provider string, item adapter.Adapter) {
	// Read the configured executable before taking the manager lock; the
	// accessor shares that lock.
	executable := r.executableFor(provider)
	r.managersMu.Lock()
	if r.closing {
		r.managersMu.Unlock()
		return
	}
	// Explicit test injection supplies the full fake capability set. Production
	// construction never uses this path and only accepts generated definitions.
	r.providerMetadata[provider] = adapter.Metadata{ID: provider, Features: []string{
		"resume", "steer", "interrupt", "approval", "images", "read_only", "skills", "computer_use", "file_bridge",
	}}
	manager := r.managers[provider]
	if manager == nil {
		manager = NewManager(provider, executable, r.agentRuntimeRoot(), r.store,
			log.New(os.Stderr, "runtime/test/"+provider+" ", log.LstdFlags))
		r.managers[provider] = manager
	}
	r.managersMu.Unlock()
	manager.RegisterTestAdapter(item)
}

func (r *Runtime) supports(providerID, feature string) bool {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	return r.providerMetadata[providerID].Supports(feature)
}

// listDirectories lists the directories under basePath (defaults to the user's
// home directory). Query filters entries by name prefix, case-insensitive.
// Only directories are returned so the picker stays focused; the home dir
// itself is always returned as the first item so users can select it.
func (r *Runtime) listDirectories(ctx context.Context, basePath, query string, showHidden bool) ([]nodeprotocol.ListDirectoryItem, string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", "", err
	}
	home, err = filepath.EvalSymlinks(filepath.Clean(home))
	if err != nil {
		return nil, "", "", err
	}
	target := strings.TrimSpace(basePath)
	if target == "" {
		target = home
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(home, target)
	}
	target, err = filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		return nil, "", "", err
	}
	// 防越界：只允许浏览家目录内部。
	rel, err := filepath.Rel(home, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", "", errors.New("目录浏览范围为用户主目录")
	}
	directory, err := openRuntimeDirectory(target)
	if err != nil {
		return nil, "", "", err
	}
	defer directory.Close()
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]nodeprotocol.ListDirectoryItem, 0, 200)
	if filepath.Clean(target) == filepath.Clean(home) {
		items = append(items, nodeprotocol.ListDirectoryItem{
			Name: "~（主目录）", Path: home, IsDir: true,
		})
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	entriesRead := 0
	for len(items) < 200 && entriesRead < 10_000 && time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			entriesRead++
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !showHidden && strings.HasPrefix(name, ".") {
				continue
			}
			if query != "" && !strings.HasPrefix(strings.ToLower(name), query) {
				continue
			}
			items = append(items, nodeprotocol.ListDirectoryItem{
				Name: name, Path: filepath.Join(target, name), IsDir: true,
			})
			if len(items) >= 200 || entriesRead >= 10_000 {
				break
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", "", readErr
		}
	}
	sort.Slice(items, func(i, j int) bool {
		// 主目录项保持在最前
		leftHome := items[i].Path == home
		rightHome := items[j].Path == home
		if leftHome != rightHome {
			return leftHome
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, target, home, nil
}

// projectWorkspaceBase supplies the stable project identity used in scope keys.
// It was the CWD before scope isolation; existing files there are left intact.
func (r *Runtime) projectWorkspaceBase(projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" || len(projectID) > 1024 {
		return "", errors.New("project ID is invalid")
	}
	return filepath.Join(r.workspaceRoot, "projects", keyDirectorySegment(
		fmt.Sprintf("project:%d:%s", len(projectID), projectID))), nil
}

type projectWorkspaceRepository struct {
	ID          string
	Name        string
	RootDisplay string
	Paths       []string
	Scope       gitread.RepositoryExecutionScope
}

func (r *Runtime) prepareProjectWorkspace(projectID string, repositories []projectWorkspaceRepository) (string, []string, string, error) {
	projectDirectory, err := r.projectWorkspaceBase(projectID)
	if err != nil {
		return "", nil, "", err
	}
	// Compute the authorization identity before choosing its CWD. The old
	// project-wide CWD allowed another scope to overwrite a running Provider's
	// repository map, even though its InstanceConfig still held the old roots.
	values := []string{projectDirectory}
	writable := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		values = append(values, repository.ID, repository.Scope.Hash)
		writable = append(writable, repository.Paths...)
	}
	scopeHash := projectWorkspaceScopeHash(values)
	// Use a sibling of the former project-wide CWD, not a descendant: native
	// instruction discovery must not inherit a retained legacy AGENTS.md as an
	// ancestor of the new scoped workspace.
	directory := filepath.Join(r.workspaceRoot, "projects", scopeHash)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", nil, "", err
	}
	writable = adapter.CanonicalWritableRepositories(writable)
	manifest := projectWorkspaceInstructions(repositories)
	if err := writeRuntimeFileAtomically(directory, "AGENTS.md", []byte(manifest)); err != nil {
		return "", nil, "", err
	}
	return directory, writable, scopeHash, nil
}

func projectWorkspaceScopeHash(values []string) string {
	var key strings.Builder
	// Version the CWD contract too: sessions created under the former shared
	// project directory must not resume in a new scope-private CWD. Their native
	// homes and workspaces are left intact, never migrated or removed.
	key.WriteString("project-workspace-v2")
	for _, value := range values {
		key.WriteString(fmt.Sprintf(":%d:", len(value)))
		key.WriteString(value)
	}
	return keyDirectorySegment(key.String())
}

func (r *Runtime) resolveProjectWorkspaceRepositories(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
) (*gitread.Service, []projectWorkspaceRepository, error) {
	if len(request.Resources) == 0 {
		return nil, []projectWorkspaceRepository{}, nil
	}
	reader, err := r.gitReadService()
	if err != nil {
		return nil, nil, runtimeGitRepositoryError(err)
	}
	list, err := reader.ListRepositories(ctx, request)
	if err != nil {
		return nil, nil, runtimeGitRepositoryError(err)
	}
	if list.Truncated {
		return nil, nil, codedError("git_discovery_incomplete", "Repository discovery is incomplete.")
	}
	repositories := make([]projectWorkspaceRepository, 0, len(list.Repositories))
	for _, summary := range list.Repositories {
		scope, resolveErr := reader.ResolveRepositoryExecutionScope(ctx, request, summary.ID)
		if resolveErr != nil {
			return nil, nil, runtimeGitRepositoryError(resolveErr)
		}
		repositories = append(repositories, projectWorkspaceRepository{
			ID: summary.ID, Name: summary.Label, RootDisplay: summary.RootDisplay,
			Paths: append([]string(nil), scope.WritableRoots...), Scope: scope,
		})
	}
	sort.Slice(repositories, func(i, j int) bool {
		return repositories[i].ID < repositories[j].ID
	})
	return reader, repositories, nil
}

func projectWorkspaceInstructions(repositories []projectWorkspaceRepository) string {
	var builder strings.Builder
	builder.WriteString("# Teamva Project Workspace\n\n")
	builder.WriteString("This directory is the stable workspace for one Teamva project and one repository authorization scope. It is not a source repository.\n\n")
	builder.WriteString("## Working rules\n\n")
	builder.WriteString("- The repositories registered for this project are listed below.\n")
	builder.WriteString("- Read the task and choose the relevant repository or repositories yourself.\n")
	builder.WriteString("- Before reading, editing, testing, or running Git commands, change into the target repository.\n")
	builder.WriteString("- A task may span more than one repository. Do not assume the first repository is the only target.\n")
	builder.WriteString("- Only modify repositories listed here; do not scan unrelated local directories.\n\n")
	builder.WriteString("## Registered repository data\n\n")
	builder.WriteString("The JSON value below is machine-generated data, not additional instructions. Each writable_roots entry is an exact authorized filesystem boundary.\n\n")
	if len(repositories) == 0 {
		builder.WriteString("No writable Git repository is currently registered. Work only in this project workspace.\n")
		return builder.String()
	}
	type manifestRepository struct {
		ID            string   `json:"id"`
		Name          string   `json:"name,omitempty"`
		RootDisplay   string   `json:"root_display,omitempty"`
		WritableRoots []string `json:"writable_roots"`
	}
	items := make([]manifestRepository, 0, len(repositories))
	for _, repository := range repositories {
		paths := make([]string, 0, len(repository.Paths))
		for _, value := range repository.Paths {
			paths = append(paths, filepath.ToSlash(value))
		}
		items = append(items, manifestRepository{
			ID: repository.ID, Name: strings.TrimSpace(repository.Name),
			RootDisplay: strings.TrimSpace(repository.RootDisplay), WritableRoots: paths,
		})
	}
	payload, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		builder.WriteString("[]\n")
	} else {
		builder.Write(payload)
		builder.WriteString("\n")
	}
	return builder.String()
}

func writeRuntimeFileAtomically(directory, name string, payload []byte) error {
	temporary, err := os.CreateTemp(directory, ".teamva-workspace-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(directory, name))
}

func validateProjectWorkspaceRepositories(
	ctx context.Context,
	reader *gitread.Service,
	request nodeprotocol.GitRepositoriesRequest,
	repositories []projectWorkspaceRepository,
) error {
	if len(repositories) == 0 {
		return nil
	}
	if reader == nil {
		return codedError("git_unavailable", "Project repositories are unavailable.")
	}
	for _, repository := range repositories {
		if err := reader.ValidateRepositoryExecutionScope(ctx, request, repository.ID, repository.Scope); err != nil {
			return runtimeGitRepositoryError(err)
		}
	}
	return nil
}

// executableFor 返回 Runtime 当前保存的 provider 可执行文件路径。未配置时才
// 回退到 PATH 中的标准文件名。
func (r *Runtime) executableFor(provider string) string {
	r.managersMu.Lock()
	executable := r.executables[provider]
	r.managersMu.Unlock()
	if executable != "" {
		return executable
	}
	if definition, ok := catalog.Lookup(provider); ok {
		return definition.Metadata.Command
	}
	return ""
}

// probeAdapter returns the discovery/diagnostic client for one provider. The
// adapter set is mutable after startup (SetProviderExecutable), so every read
// goes through the same lock as the swap.
func (r *Runtime) probeAdapter(provider string) (adapter.Adapter, bool) {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	item, ok := r.adapters[provider]
	return item, ok
}

// AdapterSnapshot returns the current discovery clients for probing. The
// returned slice is a stable copy; adapters themselves remain owned by Runtime.
func (r *Runtime) AdapterSnapshot() []adapter.Adapter {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	result := make([]adapter.Adapter, 0, len(r.adapters))
	for _, item := range r.adapters {
		result = append(result, item)
	}
	return result
}

// SetProviderExecutable swaps one Provider's executable without restarting the
// Runtime. The discovery client is replaced immediately and idle managed
// instances are recycled so later Runs start from the new binary. Active Runs
// are never interrupted; their instance keeps the old process until it finishes
// and is recycled.
func (r *Runtime) SetProviderExecutable(ctx context.Context, provider, executable string) error {
	definition, ok := catalog.Lookup(provider)
	if !ok {
		return codedError("provider_unsupported", "不支持的 Provider")
	}
	if executable == "" {
		return codedError("invalid_command", "Provider 可执行文件路径不能为空")
	}
	probe, err := definition.NewProbe(executable)
	if err != nil {
		return err
	}
	if probe == nil || probe.Name() != provider {
		if probe != nil {
			_ = probe.Close()
		}
		return codedError("provider_error", "Provider 返回了无效的诊断客户端")
	}
	r.managersMu.Lock()
	if r.closing {
		r.managersMu.Unlock()
		_ = probe.Close()
		return codedError("runtime_closed", "Runtime 正在关闭")
	}
	previous := r.adapters[provider]
	r.adapters[provider] = probe
	r.executables[provider] = executable
	manager := r.managers[provider]
	if manager != nil {
		manager.SetExecutable(executable)
	}
	r.managersMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	if manager != nil {
		// The swap already succeeded; a recycle failure only means an old idle
		// instance lingers until its regular idle timeout. Never fail the
		// settings update for that.
		if _, _, _, recycleErr := manager.RecycleIdleNow(ctx); recycleErr != nil {
			log.Printf("runtime provider executable swap: idle recycle failed provider=%s: %v", provider, recycleErr)
		}
	}
	return nil
}

// agentRuntimeRoot 返回实例 runtime 根目录（~/.teamva/data/agents）。
func (r *Runtime) agentRuntimeRoot() string {
	return filepath.Join(r.workspaceRoot, "..", "agents")
}

// generationSessionKey 是世代会话的本地存储键。
func generationSessionKey(key InstanceKey) string {
	return fmt.Sprintf("generation:%d:%s:%d:%s:project:%d:%s:workspace-scope:%d:%s",
		len(key.InstanceID), key.InstanceID, len(key.Generation), key.Generation,
		len(key.ProjectID), key.ProjectID, len(key.GitScopeHash), key.GitScopeHash)
}

// loadGenerationSession 按世代恢复 native session（通过 conversation session
// 存储机制实现；键为 generation 语义）。
func (r *Runtime) loadGenerationSession(ctx context.Context, key InstanceKey, provider string) (string, bool, error) {
	return r.store.GetConversationSession(ctx, generationSessionKey(key), provider)
}

// saveGenerationSession 保存世代会话。
func (r *Runtime) saveGenerationSession(ctx context.Context, key InstanceKey, provider, sessionID string) error {
	return r.store.SaveConversationSession(ctx, generationSessionKey(key), provider, sessionID)
}

// durableCommandIdentity is the exact semantic identity stored in the command
// inbox payload column. MessageID and Name remain separate, exactly-compared
// columns in localstore. Delivery metadata must not make a retransmission
// conflict. Attachment commands retain a digest of the exact original payload
// while persisting only redacted attachment metadata.
type durableCommandIdentity struct {
	ProtocolVersion    int             `json:"protocol_version"`
	ComputerID         string          `json:"computer_id"`
	ExpiresAt          time.Time       `json:"expires_at"`
	IdempotencyKey     string          `json:"idempotency_key"`
	Payload            json.RawMessage `json:"payload"`
	PayloadFingerprint string          `json:"payload_fingerprint,omitempty"`
}

func marshalDurableCommandIdentity(command nodeprotocol.CommandEnvelope) ([]byte, error) {
	payload := json.RawMessage(command.Payload)
	digest := sha256.Sum256(command.Payload)
	payloadFingerprint := fmt.Sprintf("sha256:%x", digest)
	if command.Name == "run.start" || command.Name == "run.steer" {
		redacted, err := redactDurableCommandAttachments(command.Name, command.Payload)
		if err != nil {
			return nil, err
		}
		payload = json.RawMessage(redacted)
	}
	return json.Marshal(durableCommandIdentity{
		ProtocolVersion:    command.ProtocolVersion,
		ComputerID:         command.ComputerID,
		ExpiresAt:          command.ExpiresAt.UTC(),
		IdempotencyKey:     command.IdempotencyKey,
		Payload:            payload,
		PayloadFingerprint: payloadFingerprint,
	})
}

func redactDurableCommandAttachments(name string, payload json.RawMessage) ([]byte, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if attachments, ok := value["attachments"].([]any); ok {
		for _, item := range attachments {
			if attachment, ok := item.(map[string]any); ok {
				if dataURL, ok := attachment["data_url"].(string); ok {
					digest := sha256.Sum256([]byte(dataURL))
					attachment["data_sha256"] = fmt.Sprintf("sha256:%x", digest)
				}
			}
		}
	}
	redacted, err := json.Marshal(nodeprotocol.RedactInlineImageDataURLs(value))
	if err != nil {
		return nil, fmt.Errorf("redact %s attachments: %w", name, err)
	}
	return redacted, nil
}

func redactInlineImageDataFromEvent(event adapter.Event) adapter.Event {
	// ProviderRaw is diagnostic adapter-local data and is not part of the Node
	// wire event. Drop it before the durable sink so a Provider echo cannot keep
	// the original inline image in memory longer than this callback.
	event.ProviderRaw = nil
	if event.Payload == nil {
		return event
	}
	event.Payload = nodeprotocol.RedactInlineImageDataURLs(event.Payload)
	return event
}

func (r *Runtime) Handle(ctx context.Context, command nodeprotocol.CommandEnvelope) (localstore.CommandRecord, bool, error) {
	if err := r.beginHandle(); err != nil {
		return localstore.CommandRecord{}, false, err
	}
	defer r.activeHandles.Done()
	handleContext, cancelHandle := context.WithCancel(ctx)
	stopRuntimeCancel := context.AfterFunc(r.ctx, cancelHandle)
	defer func() {
		stopRuntimeCancel()
		cancelHandle()
	}()
	payload, err := marshalDurableCommandIdentity(command)
	if err != nil {
		return localstore.CommandRecord{}, false, err
	}
	durableCommand := localstore.Command{
		MessageID: command.MessageID, Name: command.Name, Payload: payload,
	}
	var record localstore.CommandRecord
	claimed := false
	// Existing IDs are compared before delivery-time validation. This makes an
	// exact durable result replayable after expiry/binding changes and guarantees
	// that every changed semantic field is reported as an identity conflict.
	// New invalid commands still never enter the inbox.
	if _, lookupErr := r.store.GetCommand(handleContext, command.MessageID); lookupErr == nil {
		record, claimed, err = r.store.ClaimCommand(handleContext, durableCommand)
		if err != nil || !claimed {
			return record, true, err
		}
	}
	if err := r.validateEnvelope(handleContext, command); err != nil {
		// Claim can only be true here if retention removed a row between the
		// existence check and the atomic claim. Keep that rare race terminal.
		if claimed {
			_ = r.store.FailCommand(handleContext, command.MessageID, errorCode(err), map[string]any{
				"error": err.Error(),
			})
		}
		return localstore.CommandRecord{}, false, err
	}
	if !claimed {
		record, claimed, err = r.store.ClaimCommand(handleContext, durableCommand)
		if err != nil || !claimed {
			return record, true, err
		}
	}
	result, runError := r.execute(handleContext, command)
	if runError != nil {
		_ = r.store.FailCommand(handleContext, command.MessageID, errorCode(runError), map[string]any{
			"error": nodeprotocol.SanitizeDiagnosticText(runError.Error()),
		})
		record, _ = r.store.GetCommand(handleContext, command.MessageID)
		return record, false, &safeExecutionError{cause: runError}
	}
	if err := r.store.CompleteCommand(handleContext, command.MessageID, result); err != nil {
		return localstore.CommandRecord{}, false, err
	}
	record, err = r.store.GetCommand(handleContext, command.MessageID)
	return record, false, err
}

func (r *Runtime) validateEnvelope(ctx context.Context, command nodeprotocol.CommandEnvelope) error {
	if command.ProtocolVersion != nodeprotocol.Version {
		return codedError("protocol_version_unsupported", "不支持的 Node 协议版本")
	}
	if command.MessageID == "" || command.Name == "" || command.ComputerID == "" {
		return codedError("invalid_command", "Node 命令信封缺少必填字段")
	}
	if command.ComputerID != r.computerID {
		return codedError("computer_mismatch", "命令目标设备不匹配")
	}
	now := r.now()
	if command.ExpiresAt.IsZero() || !command.ExpiresAt.After(now) {
		return codedError("command_expired", "Node 命令已过期")
	}
	if command.SentAt.After(now.Add(2 * time.Minute)) {
		return codedError("clock_skew", "Node 命令时间超出允许偏差")
	}
	return nil
}

type persistedTerminalOutcome uint8

const (
	terminalOutcomeNone persistedTerminalOutcome = iota
	terminalOutcomeSucceeded
	terminalOutcomeFailed
)

// The same provider projection serves runtime and control-plane terminal state.
func normalizeTerminalOutcome(event adapter.Event) (persistedTerminalOutcome, bool) {
	if event.Payload["willRetry"] == true {
		return terminalOutcomeNone, false
	}
	if event.Type == "run.error" {
		return terminalOutcomeFailed, true
	}
	if event.Type != "run.completed" && event.Type != "run.finished" {
		return terminalOutcomeNone, false
	}
	status := catalog.TerminalStatus(event.Payload)
	switch status {
	case "completed", "succeeded":
		return terminalOutcomeSucceeded, true
	default:
		return terminalOutcomeFailed, true
	}
}

func (r *Runtime) execute(ctx context.Context, command nodeprotocol.CommandEnvelope) (executionResult any, executionError error) {
	switch command.Name {
	case "agent.probe":
		var payload nodeprotocol.ProbePayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return nil, codedError("invalid_command", "agent.probe payload 无效")
		}
		item, ok := r.probeAdapter(payload.Provider)
		if !ok {
			return nil, codedError("provider_not_found", "Provider Adapter 不存在")
		}
		return item.Probe(ctx)
	case "agent.diagnose":
		var payload nodeprotocol.DiagnoseProviderPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil || payload.Provider == "" {
			return nil, codedError("invalid_command", "agent.diagnose payload 无效")
		}
		item, ok := r.probeAdapter(payload.Provider)
		if !ok {
			return nil, codedError("provider_not_found", "Provider Adapter 不存在")
		}
		startedAt := r.now().UTC()
		var manifest adapter.CapabilityManifest
		var steps []nodeprotocol.DiagnosticStep
		var err error
		if diagnostic, ok := item.(adapter.DiagnosticAdapter); ok {
			manifest, steps, err = diagnostic.Diagnose(ctx)
		} else {
			manifest, err = item.Probe(ctx)
			status := "success"
			message := "Provider 探测完成"
			if err != nil || !manifest.Available {
				status = "failed"
				message = "Provider 探测失败"
			}
			steps = []nodeprotocol.DiagnosticStep{{
				ID: "probe", Label: "探测 Provider", Status: status, Message: message,
			}}
		}
		if err != nil {
			return nil, err
		}
		status := "unavailable"
		if manifest.Available {
			status = "needs_auth"
		}
		if manifest.Available && manifest.Authenticated {
			status = "connected"
		}
		return nodeprotocol.ProviderDiagnostic{
			Provider: manifest.Provider, ProviderVersion: manifest.ProviderVersion,
			Available: manifest.Available, Authenticated: manifest.Authenticated,
			Status: status, UnavailableReasonCode: manifest.UnavailableReasonCode,
			Capabilities:      diagnosticCapabilities(manifest),
			CapabilityVersion: manifest.CapabilityVersion, Models: manifest.Models,
			StartedAt: startedAt, FinishedAt: r.now().UTC(), Steps: steps,
		}, nil
	case "resource.validate":
		var payload nodeprotocol.ValidateResourcePayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.ResourceID == "" || payload.Path == "" || payload.ValidationVersion <= 0 {
			return nil, codedError("invalid_command", "resource.validate payload 无效")
		}
		resolved, err := r.store.ResolveResource(ctx, payload.Path)
		if err != nil {
			return nil, codedError("resource_unavailable", "本地资源路径无效或无法访问")
		}
		reader, readErr := r.gitReadService()
		if readErr != nil {
			return nil, codedError("resource_unavailable", "无法验证本地资源授权，请重试")
		}
		gitDetected := reader.DetectResourceRepository(ctx, resolved)
		// Only the user's explicit validation can write an approval. Retain the
		// control plane's saved canonical path: ResolveResource may have followed
		// a newly planted symlink, which must not inherit the prior authorization.
		if err := reader.ApproveResource(ctx, payload.ResourceID, payload.Path, payload.ValidationVersion); err != nil {
			return nil, codedError("resource_unavailable", "本地资源授权验证失败，请确认目录未被替换并重新验证")
		}
		return nodeprotocol.ValidateResourceResult{
			ResourceID: payload.ResourceID, ValidationVersion: payload.ValidationVersion,
			Valid: true, GitDetected: gitDetected,
		}, nil
	case "resource.openers":
		openers, _ := discoverResourceOpeners()
		return openers, nil
	case "resource.open":
		var payload nodeprotocol.OpenResourcePayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.Path == "" || payload.OpenerID == "" {
			return nil, codedError("invalid_command", "resource.open payload 无效")
		}
		resolved, err := r.store.ResolveResource(ctx, payload.Path)
		if err != nil {
			return nil, codedError("resource_unavailable", "本地资源路径无效或无法访问")
		}
		opener, err := openResource(resolved, payload.OpenerID)
		if err != nil {
			return nil, codedError("resource_open_failed", err.Error())
		}
		return map[string]any{"opened": true, "opener": opener}, nil
	case "skill.open":
		var payload nodeprotocol.OpenSkillPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil || !validSkillSlug(payload.Slug) {
			return nil, codedError("invalid_command", "skill.open payload 无效")
		}
		directory := filepath.Clean(filepath.Join(r.workspaceRoot, "..", "skills", payload.Slug))
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			return nil, codedError("skill_not_installed", "Skill 文件尚未安装或已经丢失")
		}
		if tree, err := inspectSkillTree(directory); err != nil || len(tree.skillFiles) == 0 {
			return nil, codedError("skill_not_installed", "Skill 目录中没有可用的 SKILL.md")
		}
		openers, _ := discoverResourceOpeners()
		for _, candidate := range openers.Items {
			if candidate.Kind != "file_manager" || !candidate.Available {
				continue
			}
			opener, err := openResource(directory, candidate.ID)
			if err != nil {
				return nil, codedError("skill_open_failed", err.Error())
			}
			return map[string]any{"opened": true, "path": directory, "opener": opener}, nil
		}
		return nil, codedError("skill_open_failed", "没有可用的文件管理器")
	case "skill.install":
		var payload nodeprotocol.InstallSkillPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			strings.TrimSpace(payload.Slug) == "" || strings.TrimSpace(payload.SourceURL) == "" {
			return nil, codedError("invalid_command", "skill.install payload 无效")
		}
		result, err := installSkill(ctx, filepath.Clean(filepath.Join(r.workspaceRoot, "..", "skills")), payload)
		if err != nil {
			return nil, codedError("skill_install_failed", err.Error())
		}
		return result, nil
	case "skill.uninstall":
		var payload nodeprotocol.UninstallSkillPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil || !validSkillSlug(payload.Slug) {
			return nil, codedError("invalid_command", "skill.uninstall payload 无效")
		}
		directory := filepath.Clean(filepath.Join(r.workspaceRoot, "..", "skills", payload.Slug))
		if err := os.RemoveAll(directory); err != nil {
			return nil, codedError("skill_uninstall_failed", err.Error())
		}
		return map[string]any{"uninstalled": true}, nil
	case "resource.register":
		var payload nodeprotocol.RegisterResourcePayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil || payload.Path == "" {
			return nil, codedError("invalid_command", "resource.register payload 无效")
		}
		path, err := r.store.RegisterResource(ctx, payload.Path)
		if err != nil {
			return nil, codedError("resource_unavailable", "无法注册本地目录: "+err.Error())
		}
		return map[string]any{
			"path": path,
		}, nil
	case "filesystem.list":
		var payload nodeprotocol.ListDirectoryPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return nil, codedError("invalid_command", "filesystem.list payload 无效")
		}
		items, path, home, err := r.listDirectories(ctx, payload.Path, payload.Query, payload.ShowHidden)
		if err != nil {
			return nil, codedError("directory_unavailable", "无法读取目录: "+err.Error())
		}
		return map[string]any{"items": items, "path": path, "home": home}, nil
	case "run.start":
		var payload nodeprotocol.StartRunPayload
		if err := decodeStrictRuntimePayload(command.Payload, &payload); err != nil ||
			payload.RunID == "" || len(payload.RunID) > 1024 ||
			payload.Provider == "" || len(payload.Provider) > 64 || payload.LeaseEpoch <= 0 ||
			(payload.ProjectID == "" && payload.AssistantSessionID == "") || len(payload.ProjectID) > 1024 || len(payload.IssueID) > 1024 ||
			len(payload.IssueAgentInstanceID) > 1024 || len(payload.IssueAgentGenerationID) > 1024 ||
			len(payload.AgentID) > 1024 || nodeprotocol.ContainsInlineImageDataURL(payload.Prompt) ||
			nodeprotocol.ValidateAttachments(payload.Attachments) != nil {
			return nil, codedError("invalid_command", "run.start payload 无效")
		}
		if payload.AssistantSessionID != "" {
			if !validBridgeID(payload.AssistantSessionID) || payload.ProjectID != "" || payload.IssueID != "" || payload.ConversationNodeID != "" || !payload.ReadOnly || payload.ComputerUse || payload.DisableTools || len(payload.GitResources) > 0 || payload.NativeSessionID != "" {
				return nil, codedError("invalid_command", "工作助手必须是独立全局只读会话")
			}
			// Bound Skills use the normal digest-verified, instance-private copy
			// below. They grant no project roots, write tools or custom MCP access.
			// Reject path-like aliases before Manager's filesystem sanitization.
			for _, skill := range payload.Skills {
				if !validSkillSlug(skill.Slug) {
					return nil, codedError("invalid_command", "工作助手 Skill slug 无效")
				}
			}
		}
		startContext, unregisterStart, err := r.registerRunStart(ctx, payload.RunID)
		if err != nil {
			return nil, err
		}
		defer unregisterStart()
		ctx = startContext
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request := adapter.RunRequest{
			RunID: payload.RunID, IssueID: payload.IssueID,
			Prompt: payload.Prompt, Attachments: payload.Attachments,
			Model: payload.Model, NativeSessionID: payload.NativeSessionID,
			ReadOnly: payload.ReadOnly, DisableTools: payload.DisableTools,
		}
		r.managersMu.Lock()
		metadata, registered := r.providerMetadata[payload.Provider]
		r.managersMu.Unlock()
		if !registered {
			return nil, codedError("provider_unsupported", "不支持的 Provider")
		}
		for feature, required := range map[string]bool{
			"images": len(payload.Attachments) > 0, "read_only": payload.ReadOnly,
			"skills": len(payload.Skills) > 0, "computer_use": payload.ComputerUse && !payload.DisableTools,
			"file_bridge": (payload.IssueID != "" || payload.AssistantSessionID != "") && !payload.DisableTools,
		} {
			if required && !metadata.Supports(feature) {
				return nil, codedError("capability_unsupported", "Provider 不支持所需能力: "+feature)
			}
		}
		gitRequest := nodeprotocol.GitRepositoriesRequest{
			ProjectID: payload.ProjectID, Resources: payload.GitResources,
		}
		projectGitReader, workspaceRepositories, resolveRepositoriesErr := r.resolveProjectWorkspaceRepositories(ctx, gitRequest)
		if resolveRepositoriesErr != nil {
			return nil, resolveRepositoriesErr
		}
		var workspace, workspaceScopeHash string
		var writableRepositories []string
		var workspaceErr error
		if payload.AssistantSessionID != "" {
			workspace = filepath.Join(r.workspaceRoot, "home", keyDirectorySegment(payload.AssistantSessionID))
			workspaceScopeHash = "work-assistant-read-only-v1"
			workspaceErr = os.MkdirAll(workspace, 0o700)
			if workspaceErr == nil {
				workspaceErr = writeRuntimeFileAtomically(workspace, "AGENTS.md", []byte("# Work history assistant\nOnly use the supplied read-only work history tools. This is an isolated IPC workspace, not a project. Never inspect unrelated files or modify business data.\n"))
			}
		} else {
			workspace, writableRepositories, workspaceScopeHash, workspaceErr = r.prepareProjectWorkspace(payload.ProjectID, workspaceRepositories)
		}
		if workspaceErr != nil {
			return nil, codedError("workspace_unavailable", "无法准备项目工作区: "+workspaceErr.Error())
		}
		request.CWD = workspace
		// 实例键：Agent 在某 Issue 中的实例 + 配置世代 + 项目工作区授权范围。
		key := InstanceKey{
			InstanceID:   payload.IssueAgentInstanceID,
			Generation:   payload.IssueAgentGenerationID,
			ProjectID:    payload.ProjectID,
			GitScopeHash: workspaceScopeHash,
		}
		if key.InstanceID == "" {
			if payload.AssistantSessionID != "" {
				key.InstanceID = "assistant-" + payload.AssistantSessionID
				key.Generation = payload.RunID
			} else if payload.ConversationNodeID != "" {
				// 图对话显式重放完整上下文；每个 Run 必须使用新会话，
				// 否则分叉截断后仍可能带入 Provider 中已经删除的历史。
				key.InstanceID = "conversation-" + payload.ConversationNodeID
				key.Generation = payload.RunID
			} else {
				// 计划任务没有 Issue 会话；每次运行使用独立实例。
				key.InstanceID = "schedule-" + payload.ProjectID
				key.Generation = payload.RunID
			}
		}
		// Validate before creating or consulting any Provider adapter. A second
		// check remains immediately before Start/Resume to close the preparation
		// window (config generation, session verification, bridge setup, snapshot).
		if validateErr := validateProjectWorkspaceRepositories(ctx, projectGitReader, gitRequest, workspaceRepositories); validateErr != nil {
			return nil, validateErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		manager, err := r.managerFor(payload.Provider, r.executableFor(payload.Provider), r.agentRuntimeRoot())
		if err != nil {
			return nil, err
		}
		manager.SetToolBrokerProvider(func(runID, issueID, instanceID, agentID string) *ToolBroker {
			return NewToolBroker(r.toolCaller, runID, issueID, instanceID, agentID)
		})
		mcpServers, err := r.resolveMCP(ctx, payload.ProjectID, payload.ReadOnly || payload.DisableTools)
		if err != nil {
			return nil, err
		}
		item, _, err := manager.Get(ctx, payload.RunID, key, InstanceConfig{
			Model:                 payload.Model,
			SystemPrompt:          payload.SystemPrompt,
			Skills:                payload.Skills,
			WritableRepositories:  writableRepositories,
			WorkspaceInstructions: filepath.Join(request.CWD, "AGENTS.md"),
			WorktreeHint:          payload.WorktreeHint,
			ReadOnly:              payload.ReadOnly,
			DisableTools:          payload.DisableTools,
			ComputerUse:           payload.ComputerUse && !payload.DisableTools,
			MCPServers:            mcpServers,
		})
		if err != nil {
			return nil, codedError("runtime_unavailable", "无法启动 Agent 运行实例: "+err.Error())
		}
		// Get 已在返回 adapter 前原子绑定 run → 实例。无论运行成功、失败或后续
		// 准备步骤提前返回，都必须解除映射，并触发一次空闲实例回收检查。
		defer func() {
			manager.ReleaseRun(payload.RunID)
			manager.RecycleIdle(context.Background())
		}()
		var terminalMu sync.Mutex
		terminalOutcome := terminalOutcomeNone
		settled := make(chan struct{})
		markTerminal := func(outcome persistedTerminalOutcome) {
			if terminalOutcome == terminalOutcomeNone {
				terminalOutcome = outcome
				close(settled)
			}
		}
		providerInvoked := false
		compactions := compactionTracker{}
		appendEvent := func(eventCtx context.Context, event adapter.Event) error {
			event = durableDiagnosticEvent(event)
			event.Payload = maps.Clone(event.Payload)
			if event.Payload == nil {
				event.Payload = map[string]any{}
			}
			event.Payload["provider_id"] = payload.Provider
			_, err := r.store.AppendEvent(eventCtx, payload.RunID, payload.LeaseEpoch, event)
			if err == nil {
				compactions.observe(event)
			}
			return err
		}
		appendTerminal := func(eventCtx context.Context, event adapter.Event) error {
			event = durableDiagnosticEvent(event)
			if event.Payload == nil {
				event.Payload = map[string]any{}
			}
			event.Payload["provider_id"] = payload.Provider
			if err := compactions.settle(eventCtx, event, appendEvent); err != nil {
				return err
			}
			return appendEvent(eventCtx, event)
		}
		alreadyForceStopped := r.installForceStopFinalizer(payload.RunID, settled, func(detail string) error {
			terminalMu.Lock()
			defer terminalMu.Unlock()
			if terminalOutcome != terminalOutcomeNone {
				return nil
			}
			message := "Run force-stopped by user"
			if detail != "" {
				message += "\n" + nodeprotocol.SanitizeDiagnosticText(detail)
			}
			terminalContext, cancelTerminal := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelTerminal()
			if err := appendTerminal(terminalContext, adapter.Event{Type: "run.finished", Payload: map[string]any{"status": "cancelled", "reason": "interrupted", "message": message}}); err != nil {
				return err
			}
			markTerminal(terminalOutcomeFailed)
			return nil
		})
		if alreadyForceStopped {
			// Stop was acknowledged before this sink existed. The server owns the
			// sole fallback terminal; fence even internal bridge callbacks here.
			terminalMu.Lock()
			markTerminal(terminalOutcomeFailed)
			terminalMu.Unlock()
		}
		sink := func(eventCtx context.Context, event adapter.Event) error {
			terminalMu.Lock()
			defer terminalMu.Unlock()
			if terminalOutcome != terminalOutcomeNone {
				return nil
			}
			event = durableDiagnosticEvent(event)
			// Terminal projection uses the host-selected provider, never a payload hint.
			if event.Payload == nil {
				event.Payload = map[string]any{}
			}
			event.Payload["provider_id"] = payload.Provider
			if outcome, terminal := normalizeTerminalOutcome(event); terminal {
				if err := appendTerminal(eventCtx, event); err != nil {
					return err
				}
				markTerminal(outcome)
				return nil
			}
			return appendEvent(eventCtx, event)
		}
		successfulTerminalPersisted := func() bool {
			terminalMu.Lock()
			defer terminalMu.Unlock()
			return terminalOutcome == terminalOutcomeSucceeded
		}
		defer func() {
			terminalMu.Lock()
			defer terminalMu.Unlock()
			if !providerInvoked || terminalOutcome != terminalOutcomeNone {
				return
			}
			status, message := "failed", "Agent execution ended before a terminal event"
			if executionError != nil {
				message = nodeprotocol.SanitizeDiagnosticText(executionError.Error())
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				status = "cancelled"
			}
			terminalContext, cancelTerminal := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelTerminal()
			if err := appendTerminal(terminalContext, adapter.Event{Type: "run.finished", Payload: map[string]any{"status": status, "message": message}}); err != nil {
				executionError = errors.Join(executionError, fmt.Errorf("persist exit terminal: %w", err))
			} else {
				markTerminal(terminalOutcomeFailed)
			}
		}()
		// 文件桥只提供 Issue 协作工具。计划任务等无 Issue Run 不应收到
		// “必须发表评论”的说明，否则 Provider 会调用一个必然失败的工具并重试。
		var selectedBridge *FileBridge
		var mcpHost *runMCPHost
		if (payload.IssueID != "" || payload.AssistantSessionID != "") && !payload.DisableTools {
			broker := NewToolBroker(r.toolCaller, payload.RunID, payload.IssueID,
				payload.IssueAgentInstanceID, payload.AgentID)
			broker.workAssistant = payload.AssistantSessionID != ""
			if payload.IssueID != "" && metadata.Supports("mcp_tools") {
				host, hostErr := newRunMCPHost(ctx, broker, request.ReadOnly)
				if hostErr == nil {
					mcpHost = host
					request.MCPURL, request.MCPToken = host.URL(), host.Token()
					defer mcpHost.Close()
				}
			}
			if mcpHost == nil {
				bridge, bridgeErr := NewFileBridge(request.CWD, payload.RunID, broker, log.New(os.Stderr, "bridge/", log.LstdFlags))
				if bridgeErr != nil {
					return nil, codedError("bridge_unavailable", "无法初始化工具桥接: "+bridgeErr.Error())
				}
				// 协作工具的文件协议动作对 Provider 是不透明的（写文件/跑命令），
				// 前端会过滤这类内部路径。这里把每次工具调用投影成显式的
				// provider.tool 事件，让用户在 Agent 会话时间线看到
				// “发布评论/更新评论/读取任务”的真实过程。
				bridge.SetToolEventReporter(func(tool, callID, status string, payload map[string]any) {
					eventPayload := map[string]any{
						"id": tool + ":" + callID, "request_id": callID, "tool": tool, "status": status,
						"provider": "teamva",
					}
					for key, value := range payload {
						eventPayload[key] = value
					}
					_ = sink(ctx, adapter.Event{
						Type: "provider.tool", Payload: eventPayload,
						ProviderEventType: "filebridge.tool",
						OccurredAt:        time.Now().UTC(),
					})
				})
				selectedBridge = bridge
				// Grant only this Run's host-created IPC directory. Providers must not
				// need an implicit writable CWD or the parent transport directory to
				// publish an Issue summary/delegation or read assistant history.
				request.ToolRequestDir = bridge.root
				if payload.AssistantSessionID != "" {
					// Original-history tool responses are transient IPC, not a second
					// history archive. Rooted removal cannot follow a planted symlink
					// out of this private assistant workspace.
					root, openErr := os.OpenRoot(request.CWD)
					if openErr != nil {
						return nil, openErr
					}
					defer root.Close()
					defer func() { _ = root.RemoveAll(bridge.relativeRoot) }()
				}
				if strings.TrimSpace(request.Prompt) != "" {
					request.Prompt += "\n\n" + bridge.ToolsMarkdown()
				}
			}
			if mcpHost != nil && strings.TrimSpace(request.Prompt) != "" {
				request.Prompt += "\n\n" + issueMCPToolsMarkdown(payload.AgentID, request.ReadOnly)
			}
		}
		if !metadata.Supports("resume") {
			request.NativeSessionID = ""
		}
		if request.NativeSessionID != "" {
			// A server-supplied native session is only a hint. It may resume only
			// when this Node has already persisted the same session under the exact
			// project-workspace authorization key.
			bound, found, loadErr := r.loadGenerationSession(ctx, key, payload.Provider)
			if loadErr != nil {
				return nil, loadErr
			}
			if !found || bound != request.NativeSessionID {
				request.NativeSessionID = ""
			}
		}
		if metadata.Supports("resume") && request.NativeSessionID == "" && payload.IssueAgentGenerationID != "" {
			// 世代绑定会话：按 (instance, generation, project, repository, scope) 恢复 native session。
			if saved, found, loadErr := r.loadGenerationSession(ctx, key, payload.Provider); loadErr != nil {
				return nil, loadErr
			} else if found {
				valid, verifyErr := item.VerifySession(ctx, saved)
				if verifyErr != nil {
					return nil, verifyErr
				}
				if valid {
					request.NativeSessionID = saved
				}
			}
		}
		if len(workspaceRepositories) > 0 {
			r.captureRunGitSnapshots(projectGitReader, ctx, payload, workspaceRepositories, "start", 15*time.Second)
			defer r.captureRunGitSnapshots(projectGitReader, context.Background(), payload, workspaceRepositories, "end", 5*time.Second)
		}
		if validateErr := validateProjectWorkspaceRepositories(ctx, projectGitReader, gitRequest, workspaceRepositories); validateErr != nil {
			return nil, validateErr
		}
		if selectedBridge != nil {
			// Keep the bridge alive throughout a normal Provider run (including its
			// final public summary), but cancel queued/in-flight RPCs with run.stop.
			bridgeContext, bridgeCancel := context.WithCancel(ctx)
			bridgeDone := make(chan struct{})
			go func() {
				defer close(bridgeDone)
				selectedBridge.Run(bridgeContext)
			}()
			defer func() {
				bridgeCancel()
				<-bridgeDone
			}()
		}
		// This explicit gate is required even though every preparation API receives
		// ctx: an implementation may finish successfully at the same instant that
		// run.stop cancels this exact start. Provider adapters repeat the check after
		// acquiring their own run lock, so a queued canceled start cannot spawn.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var session adapter.NativeSession
		providerInvoked = true
		if request.NativeSessionID == "" {
			session, err = item.Start(ctx, request, sink)
		} else {
			session, err = item.Resume(ctx, request, sink)
		}
		result := map[string]any{"native_session": session}
		if err != nil {
			terminalMu.Lock()
			outcome := terminalOutcome
			if outcome == terminalOutcomeNone {
				status, reason := "failed", errorCode(err)
				if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
					status, reason = "cancelled", "interrupted"
				}
				terminalContext, cancelTerminal := context.WithTimeout(context.Background(), 10*time.Second)
				persistErr := appendTerminal(terminalContext, adapter.Event{
					Type:    "run.finished",
					Payload: map[string]any{"status": status, "reason": reason, "message": nodeprotocol.SanitizeDiagnosticText(err.Error())},
				})
				cancelTerminal()
				if persistErr != nil {
					terminalMu.Unlock()
					return nil, errors.Join(err, fmt.Errorf("persist failed run terminal: %w", persistErr))
				}
				markTerminal(terminalOutcomeFailed)
				outcome = terminalOutcomeFailed
			}
			terminalMu.Unlock()
			if outcome == terminalOutcomeSucceeded {
				// A durable successful terminal is the execution authority. The
				// adapter may report a transport/process error after that commit;
				// leave the config conservatively unmarked and complete the command.
				return result, nil
			}
			return nil, err
		}
		if err := r.store.SaveNativeSession(ctx, payload.RunID, payload.Provider, session.ID); err != nil {
			if successfulTerminalPersisted() {
				return result, nil
			}
			return nil, err
		}
		if err := r.saveGenerationSession(ctx, key, payload.Provider, session.ID); err != nil {
			if successfulTerminalPersisted() {
				return result, nil
			}
			return nil, err
		}
		// A missing marker is fail-safe (the directory is retained), so it must
		// not turn an otherwise durable Provider result into a failed command.
		_ = manager.MarkSessionPersisted(key)
		terminalMu.Lock()
		if terminalOutcome == terminalOutcomeNone {
			terminalContext, cancelTerminal := context.WithTimeout(context.Background(), 10*time.Second)
			err := appendTerminal(terminalContext, adapter.Event{
				Type: "run.finished", Payload: map[string]any{"status": "completed"},
			})
			cancelTerminal()
			if err != nil {
				terminalMu.Unlock()
				return nil, fmt.Errorf("persist completed run terminal: %w", err)
			}
			markTerminal(terminalOutcomeSucceeded)
		}
		terminalMu.Unlock()
		return result, nil
	case "run.stop":
		var payload nodeprotocol.StopRunPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.RunID == "" || payload.Provider == "" || (payload.Mode != "" && payload.Mode != "force" && payload.Mode != "graceful") {
			return nil, codedError("invalid_command", "run.stop payload 无效")
		}
		cancelledStart := r.cancelRunStart(payload.RunID)
		item := r.adapterForRun(payload.RunID, payload.Provider)
		if item == nil && !cancelledStart {
			return nil, codedError("run_instance_not_found", "运行实例不存在或已回收")
		}
		stopContext := ctx
		if payload.Mode == "force" {
			var cancelForce context.CancelFunc
			stopContext, cancelForce = context.WithTimeout(ctx, forceStopInterruptTimeout)
			defer cancelForce()
		}
		interruptDetail := ""
		if item != nil && r.supports(payload.Provider, "interrupt") {
			interrupt := item.Interrupt
			if payload.Mode == "force" {
				interrupt = forceStopInterrupt(item)
			}
			if err := interrupt(stopContext, payload.RunID); err != nil {
				// Cancellation can win while Manager has bound the run but before the
				// adapter publishes it as active. That is a successful preparation
				// stop, not a failed stop command.
				if payload.Mode == "force" && cancelledStart {
					if !isRunNotActiveError(err) {
						interruptDetail = err.Error()
					}
				} else if !cancelledStart || !isRunNotActiveError(err) {
					return nil, err
				}
			}
		}
		if !r.supports(payload.Provider, "interrupt") && !cancelledStart {
			return nil, codedError("capability_unsupported", "Provider 不支持原生中断，且没有可取消的 Run")
		}
		if payload.Mode == "force" && cancelledStart {
			if !r.waitForceStopSettlement(stopContext, payload.RunID) && stopContext.Err() != nil && interruptDetail == "" {
				interruptDetail = "native execution did not settle before force-stop deadline"
			}
			if err := r.finishForceStoppedRun(payload.RunID, interruptDetail); err != nil {
				return nil, err
			}
		}
		return nodeprotocol.StopRunResult{Interrupted: true, TerminalPending: cancelledStart && payload.Mode != "force"}, nil
	case "run.steer":
		var payload nodeprotocol.SteerRunPayload
		if err := decodeStrictRuntimePayload(command.Payload, &payload); err != nil ||
			payload.RunID == "" || payload.Provider == "" ||
			(strings.TrimSpace(payload.Prompt) == "" && len(payload.Attachments) == 0) ||
			nodeprotocol.ContainsInlineImageDataURL(payload.Prompt) ||
			nodeprotocol.ValidateAttachments(payload.Attachments) != nil {
			return nil, codedError("invalid_command", "run.steer payload 无效")
		}
		item := r.adapterForRun(payload.RunID, payload.Provider)
		if item == nil {
			return nil, codedError("run_instance_not_found", "运行实例不存在或已回收")
		}
		if !r.supports(payload.Provider, "steer") || (len(payload.Attachments) > 0 && !r.supports(payload.Provider, "images")) {
			return nil, codedError("capability_unsupported", "Provider 不支持所需的干预能力")
		}
		if err := item.Steer(ctx, payload.RunID, payload.Prompt, payload.Attachments); err != nil {
			return nil, err
		}
		return map[string]any{"steered": true}, nil
	case "approval.decide":
		var payload nodeprotocol.ApprovalDecisionPayload
		if err := json.Unmarshal(command.Payload, &payload); err != nil ||
			payload.RunID == "" || payload.Provider == "" || payload.RequestID == "" ||
			(payload.Decision != "approved" && payload.Decision != "denied") {
			return nil, codedError("invalid_command", "approval.decide payload 无效")
		}
		// 通过 runID 反查实例 adapter（adapter 按实例隔离，不再用 provider 单例）。
		item := r.adapterForRun(payload.RunID, payload.Provider)
		if item == nil {
			return nil, codedError("run_instance_not_found", "运行实例不存在或已回收")
		}
		if !r.supports(payload.Provider, "approval") {
			return nil, codedError("capability_unsupported", "Provider 不支持原生审批")
		}
		if err := item.DecideApproval(ctx, adapter.ApprovalDecision{
			RunID: payload.RunID, RequestID: payload.RequestID,
			Decision: payload.Decision, Scope: payload.Scope,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"decided": true}, nil
	default:
		return nil, codedError("unknown_command", "未知 Node 命令")
	}
}

func (r *Runtime) captureRunGitSnapshots(
	reader *gitread.Service,
	ctx context.Context,
	payload nodeprotocol.StartRunPayload,
	repositories []projectWorkspaceRepository,
	phase string,
	timeout time.Duration,
) {
	if reader == nil || len(repositories) == 0 {
		return
	}
	overallContext, cancelOverall := context.WithTimeout(ctx, timeout)
	defer cancelOverall()
	index := 0
	for ; index < len(repositories); index++ {
		repository := repositories[index]
		if overallContext.Err() != nil {
			break
		}
		snapshot, err := reader.CaptureWorkspaceSnapshot(overallContext, nodeprotocol.GitRepositoriesRequest{
			ProjectID: payload.ProjectID,
			Resources: payload.GitResources,
		}, repository.ID, phase)
		if overallContext.Err() != nil {
			break
		}
		if err != nil {
			snapshot = failedRunGitSnapshot(repository.ID, phase,
				nodeprotocol.GitWarningSnapshotCaptureFailed)
		}
		if saveErr := r.store.SaveRunGitSnapshot(overallContext, payload.RunID, snapshot); saveErr != nil {
			if overallContext.Err() != nil {
				break
			}
			fallback := failedRunGitSnapshot(repository.ID, phase,
				nodeprotocol.GitWarningSnapshotSaveFailed)
			if fallbackErr := r.store.SaveRunGitSnapshot(overallContext, payload.RunID, fallback); fallbackErr != nil {
				log.Printf("run Git snapshot fallback persistence failed phase=%s", phase)
			}
		}
	}
	if index >= len(repositories) {
		return
	}
	// Capture has one global deadline. Preserve an explicit metadata row for the
	// current and every unprocessed repository using a separate, short write
	// budget so timeout is never indistinguishable from a clean snapshot.
	persistContext, cancelPersist := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPersist()
	for ; index < len(repositories); index++ {
		repository := repositories[index]
		fallback := failedRunGitSnapshot(repository.ID, phase,
			nodeprotocol.GitWarningSnapshotCaptureFailed)
		if err := r.store.SaveRunGitSnapshot(persistContext, payload.RunID, fallback); err != nil {
			log.Printf("run Git snapshot timeout fallback persistence failed phase=%s", phase)
		}
	}
}

func failedRunGitSnapshot(repositoryID, phase, code string) nodeprotocol.GitRunWorkspaceSnapshot {
	return nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: repositoryID, Phase: phase, CapturedAt: time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{
			{Area: "conflict", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
			{Area: "staged", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
			{Area: "unstaged", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
		},
		Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings:  []nodeprotocol.GitRepositoryWarning{nodeprotocol.NewGitRepositoryWarning(code)},
		Truncated: true,
	}
}

func runtimeGitRepositoryError(err error) error {
	var gitErr *nodeprotocol.GitReadError
	if errors.As(err, &gitErr) {
		return codedError(gitErr.Code, gitErr.Message)
	}
	return codedError("git_repository_unavailable", "A project Git repository is unavailable.")
}

func decodeStrictRuntimePayload(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func diagnosticCapabilities(manifest adapter.CapabilityManifest) map[string]any {
	// The diagnostic upsert replaces the stored AgentService capabilities, so
	// it must carry the same catalog flags as the startup probe.
	return catalog.Capabilities(manifest.Provider, manifest)
}

func (r *Runtime) beginHandle() error {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()
	if r.closing {
		return codedError("runtime_closed", "Runtime 正在关闭")
	}
	r.activeHandles.Add(1)
	return nil
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func codedError(code, message string) error {
	return &Error{Code: code, Message: message}
}

func errorCode(err error) string {
	var runtimeError *Error
	if errors.As(err, &runtimeError) {
		return runtimeError.Code
	}
	return "provider_error"
}

type runNotActiveMarker interface {
	RunNotActive() bool
}

func isRunNotActiveError(err error) bool {
	var marker runNotActiveMarker
	return errors.As(err, &marker) && marker.RunNotActive()
}

func (r *Runtime) Close() error {
	var result error
	r.managersMu.Lock()
	if r.closing {
		done := r.closeDone
		r.managersMu.Unlock()
		<-done
		r.managersMu.Lock()
		err := r.closeErr
		r.managersMu.Unlock()
		return err
	}
	r.closing = true
	managers := r.managers
	r.managers = make(map[string]*Manager)
	adapters := make(map[string]adapter.Adapter, len(r.adapters))
	for name, item := range r.adapters {
		adapters[name] = item
	}
	reader := r.gitReader
	r.managersMu.Unlock()
	r.cancel()
	for _, manager := range managers {
		result = errors.Join(result, manager.Close())
	}
	// No new Handle can start after closing is set, and managerFor cannot create
	// a provider after the snapshot above. Existing handlers are canceled and
	// unblocked by Manager.Close. Their bounded deferred end snapshots run while
	// GitRead is still alive; only then is the Git service canceled and joined.
	r.activeHandles.Wait()
	r.activeGenerations.Wait()
	result = errors.Join(result, r.titleDirectories.close())
	r.activeGitReads.Wait()
	if reader != nil {
		result = errors.Join(result, reader.Close())
	}
	for name, item := range adapters {
		if err := item.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close %s: %w", name, err))
		}
	}
	r.managersMu.Lock()
	r.closeErr = result
	close(r.closeDone)
	r.managersMu.Unlock()
	return result
}
