package gitread

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/fsnotify/fsnotify"
)

const (
	maxDiscoveryDirectories  = 20_000
	maxDiscoveryEntries      = 100_000
	maxDiscoveryDepth        = 2
	maxRepositories          = nodeprotocol.MaxGitRepositoriesPerRun
	maxSnapshotChanges       = 10_000
	maxChangeSnapshots       = 128
	maxChangeSnapshotBytes   = 64 << 20
	maxProjectSnapshots      = 32
	maxProjectSnapshotBytes  = 8 << 20
	maxFlights               = 64
	maxWatchedDirectories    = 4_096
	maxWatchScanEntries      = 100_000
	maxEventSubscribers      = 256
	defaultChangesLimit      = 200
	maxChangesLimit          = 500
	statusOutputLimit        = 20 << 20
	patchOutputLimit         = 20 << 20
	diffConfirmationLimit    = 2 << 20
	imageOutputLimit         = 5 << 20
	projectDiscoveryTimeout  = 5 * time.Second
	resourceDetectionTimeout = 2 * time.Second
)

type Service struct {
	ctx        context.Context
	cancel     context.CancelFunc
	secret     [32]byte
	secretErr  error
	generation atomic.Int64

	gitSem      chan struct{}
	gitPath     string
	gitExec     string
	gitExecDir  string
	gitExecInfo os.FileInfo
	gitInfo     os.FileInfo
	gitHash     string
	gitFile     *os.File
	gitErr      error

	resourceAuthorizations *resourceAuthorizationState

	mu           sync.Mutex
	projects     map[string]*projectSnapshot
	changes      map[string]*changeSnapshot
	changeKeys   map[string]*changeSnapshot
	changeIndex  map[string]*changeSnapshot
	pathIndex    map[string]pathReference
	changeBytes  int64
	changeLRU    *list.List
	projectLRU   *list.List
	projectBytes int64
	flights      map[string]*flight
	projectEpoch uint64
	subscribers  map[string]map[*eventSubscriber]struct{}
	closed       bool
	closeDone    chan struct{}
	closeErr     error

	watcher       *fsnotify.Watcher
	watched       map[string]map[repoTarget]struct{}
	watchIdentity map[string]os.FileInfo
	watchRoots    map[repoTarget][]string
	watchLimited  map[repoTarget]bool
	watchRebuild  map[repoTarget]bool
	pendingEvents map[repoTarget]pendingEvent

	wg sync.WaitGroup
}

type projectSnapshot struct {
	projectID      string
	snapshotID     string
	resourcesHash  string
	repositories   map[string]*repository
	ordered        []string
	truncated      bool
	warnings       []nodeprotocol.GitRepositoryWarning
	lruElement     *list.Element
	estimatedBytes int64
}

type repository struct {
	id             string
	commonID       string
	root           string
	gitDir         string
	commonDir      string
	rootIdentity   stablePathIdentity
	gitIdentity    stablePathIdentity
	commonIdentity stablePathIdentity
	objectIdentity stablePathIdentity
	markerIdentity stableMarkerIdentity
	commonMarker   stableMarkerIdentity
	settings       repositorySettings
	handleMu       sync.Mutex
	handlesClosed  bool
	authPrefixes   []string
	resourceIDs    []string
	summary        nodeprotocol.GitRepositorySummary
	fingerprint    string
	dirty          bool
	watchState     map[string]watchObservation

	authorizationHash string
}

// RepositoryExecutionScope is the exact filesystem boundary that may be
// exposed to a Provider for one discovered repository. WritableRoots contains
// every canonical authorized root; partial authorizations are never widened to
// the containing repository root. CWD is retained as a convenience view for
// callers that need one representative root, not as the Provider process CWD.
type RepositoryExecutionScope struct {
	Root          string
	CWD           string
	WritableRoots []string
	Hash          string
	Full          bool
}

type internalChange struct {
	public      nodeprotocol.GitChange
	path        string
	oldPath     string
	indexStatus byte
	workStatus  byte
	mode        string
	sub         string
}

type changeSnapshot struct {
	id             string
	snapshotID     string
	projectID      string
	resourcesHash  string
	repositoryID   string
	generation     int64
	area           string
	prefix         string
	expand         bool
	items          []internalChange
	byID           map[string]int
	warnings       []nodeprotocol.GitRepositoryWarning
	truncated      bool
	pathTokens     []string
	createdAt      time.Time
	cacheKey       string
	estimatedBytes int64
	lruElement     *list.Element
}

type pathReference struct {
	snapshot     *changeSnapshot
	projectID    string
	repositoryID string
	generation   int64
	path         string
	directory    bool
}

type flight struct {
	done  chan struct{}
	value any
	err   error
}

type repoTarget struct {
	projectID    string
	repositoryID string
}

type pendingEvent struct {
	reason string
	first  time.Time
	due    time.Time
}

type eventSubscriber struct {
	channel chan nodeprotocol.GitEvent
}

func New() *Service {
	return newService("")
}

// NewWithResourceAuthorizationDirectory requires durable, explicit resource
// validation approvals in the application's private local-state directory.
// Reads never create or update approvals. Existing resources without an anchor
// fail closed until the user explicitly revalidates them.
func NewWithResourceAuthorizationDirectory(directory string) *Service {
	service := newService("")
	service.resourceAuthorizations = &resourceAuthorizationState{directory: directory}
	return service
}

// NewWithExecutable uses an explicitly supplied, absolute Git executable as a
// trust-boundary input. The executable is resolved, hashed, and pinned exactly
// like the built-in system Git. Embedded production callers should use the
// durable-authorization constructor instead of this standalone read client.
func NewWithExecutable(executable string) *Service {
	return newService(executable)
}

func newService(executable string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		ctx: ctx, cancel: cancel, gitSem: make(chan struct{}, 16),
		projects: make(map[string]*projectSnapshot), changes: make(map[string]*changeSnapshot),
		changeKeys: make(map[string]*changeSnapshot), changeIndex: make(map[string]*changeSnapshot),
		pathIndex: make(map[string]pathReference),
		changeLRU: list.New(), projectLRU: list.New(),
		flights:     make(map[string]*flight),
		subscribers: make(map[string]map[*eventSubscriber]struct{}),
		closeDone:   make(chan struct{}),
		watched:     make(map[string]map[repoTarget]struct{}), watchIdentity: make(map[string]os.FileInfo),
		watchRoots:   make(map[repoTarget][]string),
		watchLimited: make(map[repoTarget]bool), watchRebuild: make(map[repoTarget]bool),
		pendingEvents: make(map[repoTarget]pendingEvent),
	}
	if executable == "" {
		service.gitPath, service.gitInfo, service.gitHash, service.gitErr = resolveGitExecutable()
	} else {
		service.gitPath, service.gitInfo, service.gitHash, service.gitErr = resolveGitExecutableAt(executable)
	}
	if service.gitErr == nil {
		service.gitFile, service.gitExec, service.gitExecDir, service.gitExecInfo, service.gitErr =
			pinGitExecutable(service.gitPath, service.gitInfo, service.gitHash)
	}
	if _, err := rand.Read(service.secret[:]); err != nil {
		service.secretErr = err
	}
	if watcher, err := fsnotify.NewWatcher(); err == nil {
		service.watcher = watcher
		service.wg.Add(2)
		go service.watchLoop()
		go service.reconcileLoop()
	} else {
		service.wg.Add(1)
		go service.reconcileLoop()
	}
	return service
}

func (s *Service) nextGeneration() int64 {
	return s.generation.Add(1)
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.closed = true
	for _, subscribers := range s.subscribers {
		for subscriber := range subscribers {
			close(subscriber.channel)
		}
	}
	s.subscribers = make(map[string]map[*eventSubscriber]struct{})
	s.mu.Unlock()
	s.cancel()
	var err error
	if s.watcher != nil {
		err = s.watcher.Close()
	}
	s.wg.Wait()
	s.mu.Lock()
	projectIDs := make([]string, 0, len(s.projects))
	for projectID := range s.projects {
		projectIDs = append(projectIDs, projectID)
	}
	for _, projectID := range projectIDs {
		s.removeProjectLocked(projectID)
	}
	if s.gitFile != nil {
		_ = s.gitFile.Close()
		s.gitFile = nil
	}
	if s.gitExecDir != "" {
		_ = os.RemoveAll(s.gitExecDir)
		s.gitExecDir = ""
	}
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
	return err
}

func (s *Service) ListRepositories(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitRepositoryList{}, operationErr
	}
	defer done()
	if err := validateProjectRequest(request.ProjectID, request.Resources); err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	resourceHash, err := s.authorizedResourceHash(ctx, request.Resources)
	if err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	var result nodeprotocol.GitRepositoryList
	for range maxRepositoryCacheAttempts {
		if err := ctx.Err(); err != nil {
			return nodeprotocol.GitRepositoryList{}, err
		}
		result, err = s.listRepositories(ctx, request, resourceHash)
		if !isRepositoryCacheInvalidated(err) {
			return result, err
		}
		// Refresh advances the barrier once per request, not once per retry.
		// Every attempt still rechecks the original explicit resource anchors.
		request.Refresh = false
	}
	return result, err
}

func (s *Service) listRepositories(ctx context.Context, request nodeprotocol.GitRepositoriesRequest, resourceHash string) (nodeprotocol.GitRepositoryList, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitRepositoryList{}, operationErr
	}
	defer done()
	if err := validateProjectRequest(request.ProjectID, request.Resources); err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	currentResourceHash, err := s.authorizedResourceHash(ctx, request.Resources)
	if err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	if currentResourceHash != resourceHash {
		return nodeprotocol.GitRepositoryList{}, resourceAuthorizationError()
	}
	if request.Refresh {
		s.invalidateProject(request.ProjectID, "refresh")
	}
	s.mu.Lock()
	epoch := s.projectEpoch
	s.mu.Unlock()
	key := "repositories:" + request.ProjectID + ":" + resourceHash + ":" + strconv.FormatUint(epoch, 10)
	value, err := s.singleflight(ctx, key, func() (any, error) {
		s.mu.Lock()
		existing := s.projects[request.ProjectID]
		if existing != nil && existing.resourcesHash == resourceHash {
			s.projectLRU.MoveToFront(existing.lruElement)
			s.mu.Unlock()
			return existing, nil
		}
		s.mu.Unlock()
		discovered, discoverErr := s.discover(ctx, request.ProjectID, request.Resources, resourceHash)
		if discoverErr != nil {
			return nil, discoverErr
		}
		s.mu.Lock()
		if s.closed {
			closeProjectIdentities(discovered)
			s.mu.Unlock()
			return nil, gitError("runtime_closed", "Git 读取服务已关闭", false)
		}
		if s.projectEpoch != epoch {
			closeProjectIdentities(discovered)
			s.mu.Unlock()
			return nil, repositoryCacheInvalidated()
		}
		s.installProjectLocked(discovered)
		s.mu.Unlock()
		watchDeadline := time.Now().Add(2 * time.Second)
		for _, repositoryID := range discovered.ordered {
			s.ensureWatched(discovered.projectID, discovered.repositories[repositoryID], watchDeadline)
		}
		return discovered, nil
	})
	if err != nil {
		return nodeprotocol.GitRepositoryList{}, err
	}
	project := value.(*projectSnapshot)
	refreshContext, cancelRefresh := context.WithTimeout(ctx, 5*time.Second)
	refreshErr := s.refreshDirtyRepositories(refreshContext, project)
	cancelRefresh()
	if refreshErr != nil && ctx.Err() != nil {
		return nodeprotocol.GitRepositoryList{}, ctx.Err()
	}
	for _, repositoryID := range project.ordered {
		repository := project.repositories[repositoryID]
		if repository == nil {
			continue
		}
		if err := repository.validateIdentity(); err != nil {
			if errors.Is(err, errRepositoryIdentityClosed) {
				return nodeprotocol.GitRepositoryList{}, repositoryCacheInvalidated()
			}
			s.expireProjectForRepository(project.projectID, repository, "repository_replaced")
			return nodeprotocol.GitRepositoryList{}, gitError("repository_not_found", "Git 仓库路径已失效，请刷新后重试", true)
		}
	}
	finalResourceHash, authorizationErr := s.authorizedResourceHash(ctx, request.Resources)
	if authorizationErr != nil || finalResourceHash != resourceHash {
		if ctx.Err() != nil {
			return nodeprotocol.GitRepositoryList{}, ctx.Err()
		}
		s.invalidateProject(request.ProjectID, "resource_authorization_changed")
		return nodeprotocol.GitRepositoryList{}, resourceAuthorizationError()
	}
	s.mu.Lock()
	current := s.projects[request.ProjectID]
	if current == nil || current.resourcesHash != resourceHash {
		s.mu.Unlock()
		return nodeprotocol.GitRepositoryList{}, repositoryCacheInvalidated()
	}
	items := make([]nodeprotocol.GitRepositorySummary, 0, len(current.ordered))
	for _, id := range current.ordered {
		repository := current.repositories[id]
		summary := repository.summary
		summary.ResourceIDs = append([]string{}, summary.ResourceIDs...)
		summary.Warnings = append([]nodeprotocol.GitRepositoryWarning{}, summary.Warnings...)
		items = append(items, summary)
		if repository.summary.State == "unavailable" {
			repository.dirty = true
		}
	}
	snapshotID := current.snapshotID
	truncated := current.truncated
	warnings := append([]nodeprotocol.GitRepositoryWarning{}, current.warnings...)
	s.mu.Unlock()
	return nodeprotocol.GitRepositoryList{
		SnapshotID: snapshotID, Repositories: items,
		Truncated: truncated, Warnings: warnings,
	}, nil
}

func closeProjectIdentities(project *projectSnapshot) {
	if project == nil {
		return
	}
	for _, repository := range project.repositories {
		repository.closeIdentity()
	}
}

func (s *Service) ListChanges(ctx context.Context, request nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitChangesPage{}, operationErr
	}
	defer done()
	if request.Limit == 0 {
		request.Limit = defaultChangesLimit
	}
	if request.Limit < 1 || request.Limit > maxChangesLimit {
		return nodeprotocol.GitChangesPage{}, gitError("invalid_argument", "limit 必须在 1 到 500 之间", false)
	}
	if !validArea(request.Area) {
		return nodeprotocol.GitChangesPage{}, gitError("invalid_argument", "area 参数无效", false)
	}
	if request.ExpandUntracked && request.Area != "untracked" {
		return nodeprotocol.GitChangesPage{}, gitError("invalid_argument", "expand_untracked 仅适用于未跟踪文件", false)
	}
	project, repository, err := s.authorizedRepository(ctx, request.ProjectID, request.Resources, request.RepositoryID)
	if err != nil {
		return nodeprotocol.GitChangesPage{}, err
	}
	prefix := ""
	if request.Prefix != "" {
		resolved, resolveErr := s.resolvePathToken(project, repository, request.Prefix)
		if resolveErr != nil {
			return nodeprotocol.GitChangesPage{}, resolveErr
		}
		prefix = resolved
	}
	request.Prefix = prefix
	if request.Cursor != "" {
		return s.pageFromCursor(request, project, repository)
	}

	s.mu.Lock()
	currentProject := s.projects[project.projectID]
	if currentProject != project || currentProject.repositories[repository.id] != repository {
		s.mu.Unlock()
		return nodeprotocol.GitChangesPage{}, gitError("snapshot_expired", "Git 仓库快照已失效，请刷新后重试", true)
	}
	generation := repository.summary.Generation
	s.mu.Unlock()
	key := strings.Join([]string{"changes", request.ProjectID, project.resourcesHash, request.RepositoryID,
		request.Area, prefix, strconv.FormatBool(request.ExpandUntracked), strconv.FormatInt(generation, 10)}, ":")
	value, err := s.singleflight(ctx, key, func() (any, error) {
		s.mu.Lock()
		if cached := s.changeKeys[key]; cached != nil && cached.snapshotID == project.snapshotID &&
			cached.generation == generation {
			s.changeLRU.MoveToFront(cached.lruElement)
			s.mu.Unlock()
			return cached, nil
		}
		s.mu.Unlock()
		changes, branch, fingerprint, readErr := s.readChanges(ctx, repository, request.Area, prefix, request.ExpandUntracked)
		if readErr != nil {
			return nil, readErr
		}
		if repository.validateIdentity() != nil {
			s.expireProjectForRepository(project.projectID, repository, "repository_replaced")
			return nil, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
		}
		observedGeneration := generation
		if prefix == "" && !request.ExpandUntracked {
			var observed bool
			observedGeneration, observed = s.updateStatusObservation(
				project.projectID, repository.id, generation, branch, changes.counts, fingerprint,
			)
			if !observed {
				return nil, gitError("snapshot_expired", "Git 仓库状态已变化，请刷新后重试", true)
			}
		}
		s.mu.Lock()
		currentProject := s.projects[project.projectID]
		if currentProject == nil || currentProject.resourcesHash != project.resourcesHash {
			s.mu.Unlock()
			return nil, gitError("snapshot_expired", "Git 仓库快照已失效，请刷新后重试", true)
		}
		currentRepository := currentProject.repositories[repository.id]
		if currentRepository == nil || currentRepository.summary.Generation != observedGeneration {
			s.mu.Unlock()
			return nil, gitError("snapshot_expired", "Git 仓库状态已变化，请刷新后重试", true)
		}
		snapshot := s.newChangeSnapshotLocked(currentProject, currentRepository, key, request.Area, prefix,
			request.ExpandUntracked, changes.items, changes.warnings, changes.truncated)
		s.mu.Unlock()
		return snapshot, nil
	})
	if err != nil {
		return nodeprotocol.GitChangesPage{}, err
	}
	return s.pageSnapshot(value.(*changeSnapshot), 0, request.Limit)
}

func (s *Service) Diff(ctx context.Context, request nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitDiffResult{}, operationErr
	}
	defer done()
	if request.Context < 0 || request.Context > 20 {
		return nodeprotocol.GitDiffResult{}, gitError("invalid_argument", "context 必须在 0 到 20 之间", false)
	}
	project, repository, err := s.authorizedRepository(ctx, request.ProjectID, request.Resources, request.RepositoryID)
	if err != nil {
		return nodeprotocol.GitDiffResult{}, err
	}
	s.mu.Lock()
	snapshot := s.changeIndex[request.ChangeID]
	if snapshot == nil || snapshot.projectID != request.ProjectID || snapshot.repositoryID != request.RepositoryID ||
		snapshot.resourcesHash != project.resourcesHash || snapshot.generation != repository.summary.Generation {
		s.mu.Unlock()
		return nodeprotocol.GitDiffResult{}, gitError("change_not_found", "变更不存在、已过期或不在授权范围内", false)
	}
	index, ok := snapshot.byID[request.ChangeID]
	if !ok || index < 0 || index >= len(snapshot.items) {
		s.mu.Unlock()
		return nodeprotocol.GitDiffResult{}, gitError("change_not_found", "变更不存在、已过期或不在授权范围内", false)
	}
	change := snapshot.items[index]
	s.changeLRU.MoveToFront(snapshot.lruElement)
	s.mu.Unlock()
	if !repository.authorizes(change.path) || (change.oldPath != "" && !repository.authorizes(change.oldPath)) {
		return nodeprotocol.GitDiffResult{}, gitError("path_out_of_scope", "变更路径超出已授权的资源范围", false)
	}
	result, err := s.readDiff(ctx, repository, snapshot, change, request.Context, request.AllowLarge)
	if err != nil {
		return nodeprotocol.GitDiffResult{}, err
	}
	if repository.validateIdentity() != nil {
		s.expireProjectForRepository(project.projectID, repository, "repository_replaced")
		return nodeprotocol.GitDiffResult{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	s.mu.Lock()
	currentProject := s.projects[request.ProjectID]
	valid := currentProject != nil && currentProject.snapshotID == snapshot.snapshotID &&
		currentProject.repositories[request.RepositoryID] == repository &&
		repository.summary.Generation == snapshot.generation && s.changeIndex[request.ChangeID] == snapshot
	s.mu.Unlock()
	if !valid {
		return nodeprotocol.GitDiffResult{}, gitError("snapshot_expired", "Git 变更快照已失效，请刷新后重试", true)
	}
	return result, nil
}

func (s *Service) Refresh(ctx context.Context, request nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitRefreshResult{}, operationErr
	}
	defer done()
	if err := validateProjectRequest(request.ProjectID, request.Resources); err != nil {
		return nodeprotocol.GitRefreshResult{}, err
	}
	s.invalidateProject(request.ProjectID, "refresh")
	_, err := s.ListRepositories(ctx, nodeprotocol.GitRepositoriesRequest{
		ProjectID: request.ProjectID, Resources: request.Resources,
	})
	if err != nil {
		return nodeprotocol.GitRefreshResult{}, err
	}
	return nodeprotocol.GitRefreshResult{Refreshed: true}, nil
}

func (s *Service) Subscribe(ctx context.Context, request nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nil, nil, operationErr
	}
	defer done()
	if err := validateProjectRequest(request.ProjectID, request.Resources); err != nil {
		return nil, nil, err
	}
	// One slot per possible repository allows overflow handling to coalesce to
	// the newest invalidation for every repository without silently losing one.
	subscriber := &eventSubscriber{channel: make(chan nodeprotocol.GitEvent, maxRepositories)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, gitError("runtime_closed", "Git 读取服务已关闭", false)
	}
	subscriberCount := 0
	for _, subscribers := range s.subscribers {
		subscriberCount += len(subscribers)
	}
	if subscriberCount >= maxEventSubscribers {
		s.mu.Unlock()
		return nil, nil, gitError("git_busy", "Git 事件订阅已达到并发上限", true)
	}
	if s.subscribers[request.ProjectID] == nil {
		s.subscribers[request.ProjectID] = make(map[*eventSubscriber]struct{})
	}
	// Registration precedes initialization. Any invalidation racing
	// ListRepositories is queued and observed by the caller after Subscribe
	// returns, closing the former list→register event-loss window.
	s.subscribers[request.ProjectID][subscriber] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			if subscribers := s.subscribers[request.ProjectID]; subscribers != nil {
				if _, exists := subscribers[subscriber]; exists {
					delete(subscribers, subscriber)
					close(subscriber.channel)
				}
				if len(subscribers) == 0 {
					delete(s.subscribers, request.ProjectID)
				}
			}
			s.mu.Unlock()
		})
	}
	if _, err := s.ListRepositories(ctx, request); err != nil {
		cancel()
		return nil, nil, err
	}
	return subscriber.channel, cancel, nil
}

func (s *Service) authorizedRepository(ctx context.Context, projectID string, resources []nodeprotocol.GitResourceRequest, repositoryID string) (*projectSnapshot, *repository, error) {
	if repositoryID == "" {
		return nil, nil, gitError("invalid_argument", "repository_id 不能为空", false)
	}
	list, err := s.ListRepositories(ctx, nodeprotocol.GitRepositoriesRequest{ProjectID: projectID, Resources: resources})
	if err != nil {
		return nil, nil, err
	}
	_ = list
	resourceHash, err := s.authorizedResourceHash(ctx, resources)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	project := s.projects[projectID]
	if project == nil || project.resourcesHash != resourceHash {
		s.mu.Unlock()
		return nil, nil, gitError("snapshot_expired", "Git 仓库快照已失效，请刷新后重试", true)
	}
	repository := project.repositories[repositoryID]
	if repository == nil {
		s.mu.Unlock()
		return nil, nil, gitError("repository_not_found", "Git 仓库不存在或不在项目授权范围内", false)
	}
	s.projectLRU.MoveToFront(project.lruElement)
	s.mu.Unlock()
	if repository.validateIdentity() != nil {
		s.expireProjectForRepository(projectID, repository, "repository_replaced")
		return nil, nil, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	return project, repository, nil
}

// ResolveRepositoryRoot resolves a stable repository identity only against the
// server-supplied project resources. It deliberately refreshes discovery so a
// removed, moved, or newly out-of-scope repository ID cannot reuse a cached
// root when a run starts.
func (s *Service) ResolveRepositoryRoot(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
	repositoryID string,
) (string, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return "", operationErr
	}
	defer done()
	request.Refresh = true
	repository, err := s.executionRepository(ctx, request, repositoryID)
	if err != nil {
		return "", err
	}
	defer repository.closeIdentity()
	return repository.root, nil
}

// ResolveRepositoryExecutionScope resolves canonical writable roots and a
// stable authorization hash. Disjoint partial prefixes remain distinct roots;
// they are never replaced by their wider common repository root.
func (s *Service) ResolveRepositoryExecutionScope(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
	repositoryID string,
) (RepositoryExecutionScope, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return RepositoryExecutionScope{}, operationErr
	}
	defer done()
	request.Refresh = true
	repository, err := s.executionRepository(ctx, request, repositoryID)
	if err != nil {
		return RepositoryExecutionScope{}, err
	}
	defer repository.closeIdentity()
	scope, err := executionScopeForRepository(repository)
	if err != nil {
		return RepositoryExecutionScope{}, err
	}
	if err := s.validateExecutionAuthorization(ctx, request, repository); err != nil {
		return RepositoryExecutionScope{}, err
	}
	return scope, nil
}

func executionScopeForRepository(repository *repository) (RepositoryExecutionScope, error) {
	if repository == nil || repository.validateIdentity() != nil {
		return RepositoryExecutionScope{}, gitError("snapshot_expired", "Git 仓库授权身份已失效", true)
	}
	prefixes := canonicalAuthorizationPrefixes(repository.authPrefixes)
	if len(prefixes) == 0 {
		return RepositoryExecutionScope{}, gitError("path_out_of_scope", "Git 仓库没有可执行的授权范围", false)
	}
	scope := RepositoryExecutionScope{Root: repository.root, Full: prefixes[0] == ""}
	values := []string{repository.id}
	if repository.authorizationHash != "" {
		values = append(values, repository.authorizationHash)
	}
	if scope.Full {
		scope.CWD = repository.root
		scope.WritableRoots = []string{repository.root}
		values = append(values, "", repository.rootIdentity.stable)
	} else {
		scope.WritableRoots = make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			candidate := filepath.Join(repository.root, filepath.FromSlash(prefix))
			canonical, err := canonicalDirectory(candidate)
			inside, relative := pathWithin(repository.root, canonical)
			if err != nil || !inside || relative != prefix || !repository.authorizes(relative) {
				return RepositoryExecutionScope{}, gitError("path_out_of_scope", "Git 资源范围已失效或超出授权目录", false)
			}
			identity, identityErr := captureStablePathIdentity(canonical)
			if identityErr != nil {
				return RepositoryExecutionScope{}, gitError("path_out_of_scope", "Git 资源范围身份不可读取", false)
			}
			if validateErr := identity.validate(); validateErr != nil {
				identity.close()
				return RepositoryExecutionScope{}, gitError("snapshot_expired", "Git 资源范围在解析期间发生变化", true)
			}
			values = append(values, prefix, identity.stable)
			identity.close()
			scope.WritableRoots = append(scope.WritableRoots, canonical)
		}
		scope.CWD = scope.WritableRoots[0]
	}
	scope.Hash = stableSHA256ID("scope", values...)
	return scope, nil
}

func canonicalAuthorizationPrefixes(values []string) []string {
	prefixes := make([]string, 0, len(values))
	for _, value := range values {
		cleaned, err := normalizePrefix(value)
		if err != nil {
			continue
		}
		if cleaned == "" {
			return []string{""}
		}
		prefixes = append(prefixes, cleaned)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i]) != len(prefixes[j]) {
			return len(prefixes[i]) < len(prefixes[j])
		}
		return prefixes[i] < prefixes[j]
	})
	result := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		covered := false
		for _, existing := range result {
			if prefix == existing || strings.HasPrefix(prefix, existing+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, prefix)
		}
	}
	return result
}

// ValidateRepositoryRoot is the final identity guard used immediately before a
// Provider Start/Resume call. A fresh, independently pinned directory chain must
// retain the requested repository ID and the original explicit resource anchors.
func (s *Service) ValidateRepositoryRoot(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
	repositoryID string,
	expectedRoot string,
) error {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return operationErr
	}
	defer done()
	repository, err := s.executionRepository(ctx, request, repositoryID)
	if err != nil {
		return err
	}
	defer repository.closeIdentity()
	if !samePath(repository.root, expectedRoot) || repository.validateIdentity() != nil {
		s.expireProjectForRepository(request.ProjectID, repository, "repository_replaced")
		return gitError("repository_not_found", "Git 仓库路径已失效，请刷新后重试", true)
	}
	return s.validateExecutionAuthorization(ctx, request, repository)
}

// ValidateRepositoryExecutionScope is the final guard immediately before a
// Provider Start/Resume. It verifies both repository identity and the exact
// partial/full authorization boundary used for CWD and session isolation.
func (s *Service) ValidateRepositoryExecutionScope(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
	repositoryID string,
	expected RepositoryExecutionScope,
) error {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return operationErr
	}
	defer done()
	repository, err := s.executionRepository(ctx, request, repositoryID)
	if err != nil {
		return err
	}
	defer repository.closeIdentity()
	current, err := executionScopeForRepository(repository)
	if err != nil {
		return err
	}
	if current.Hash != expected.Hash || current.Full != expected.Full ||
		!samePath(current.Root, expected.Root) || !samePath(current.CWD, expected.CWD) ||
		len(current.WritableRoots) != len(expected.WritableRoots) {
		return gitError("snapshot_expired", "Git 仓库授权范围已变化，请刷新后重试", true)
	}
	for index, root := range current.WritableRoots {
		if !samePath(root, expected.WritableRoots[index]) {
			return gitError("snapshot_expired", "Git 仓库授权范围已变化，请刷新后重试", true)
		}
		identity, err := captureStablePathIdentity(root)
		if err != nil {
			return gitError("repository_not_found", "Git Agent 工作目录已失效", true)
		}
		validateErr := identity.validate()
		identity.close()
		if validateErr != nil {
			return gitError("repository_not_found", "Git Agent 工作目录已失效", true)
		}
	}
	return s.validateExecutionAuthorization(ctx, request, repository)
}

func (s *Service) resolvePathToken(project *projectSnapshot, repository *repository, token string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, exists := s.pathIndex[token]
	if !exists || s.projects[project.projectID] != project || project.repositories[repository.id] != repository ||
		reference.projectID != project.projectID || reference.repositoryID != repository.id ||
		reference.generation != repository.summary.Generation || reference.snapshot == nil ||
		reference.snapshot.snapshotID != project.snapshotID || !repository.authorizes(reference.path) {
		return "", gitError("path_out_of_scope", "prefix 无效、已过期或超出已授权的资源范围", false)
	}
	if reference.snapshot.lruElement != nil {
		s.changeLRU.MoveToFront(reference.snapshot.lruElement)
	}
	return reference.path, nil
}

func (s *Service) pageFromCursor(request nodeprotocol.GitChangesRequest, project *projectSnapshot, repository *repository) (nodeprotocol.GitChangesPage, error) {
	snapshotID, offset, err := s.decodeCursor(request.Cursor)
	if err != nil {
		return nodeprotocol.GitChangesPage{}, gitError("invalid_cursor", "cursor 无效或已过期", false)
	}
	prefix, _ := normalizePrefix(request.Prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.changes[snapshotID]
	if snapshot == nil || snapshot.projectID != request.ProjectID || snapshot.repositoryID != request.RepositoryID ||
		snapshot.resourcesHash != project.resourcesHash || snapshot.generation != repository.summary.Generation ||
		snapshot.area != request.Area || snapshot.prefix != prefix || snapshot.expand != request.ExpandUntracked || offset > len(snapshot.items) {
		return nodeprotocol.GitChangesPage{}, gitError("invalid_cursor", "cursor 无效或已过期", false)
	}
	s.changeLRU.MoveToFront(snapshot.lruElement)
	return s.pageSnapshotLocked(snapshot, offset, request.Limit), nil
}

func (s *Service) pageSnapshot(snapshot *changeSnapshot, offset, limit int) (nodeprotocol.GitChangesPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := s.projects[snapshot.projectID]
	if s.changes[snapshot.id] != snapshot || project == nil || project.snapshotID != snapshot.snapshotID {
		return nodeprotocol.GitChangesPage{}, gitError("snapshot_expired", "Git 变更快照已失效，请刷新后重试", true)
	}
	repository := project.repositories[snapshot.repositoryID]
	if repository == nil || repository.summary.Generation != snapshot.generation {
		return nodeprotocol.GitChangesPage{}, gitError("snapshot_expired", "Git 变更快照已失效，请刷新后重试", true)
	}
	return s.pageSnapshotLocked(snapshot, offset, limit), nil
}

func (s *Service) pageSnapshotLocked(snapshot *changeSnapshot, offset, limit int) nodeprotocol.GitChangesPage {
	end := offset + limit
	if end > len(snapshot.items) {
		end = len(snapshot.items)
	}
	items := make([]nodeprotocol.GitChange, 0, end-offset)
	for _, item := range snapshot.items[offset:end] {
		items = append(items, item.public)
	}
	next := ""
	if end < len(snapshot.items) {
		next = s.encodeCursor(snapshot.id, end)
	}
	return nodeprotocol.GitChangesPage{
		SnapshotID: snapshot.snapshotID, RepositoryID: snapshot.repositoryID,
		RepositoryGeneration: snapshot.generation, Generation: snapshot.generation, Area: snapshot.area,
		Items: items, NextCursor: next,
		Truncated: snapshot.truncated,
		Warnings:  append([]nodeprotocol.GitRepositoryWarning{}, snapshot.warnings...),
	}
}

func (s *Service) newChangeSnapshotLocked(project *projectSnapshot, repository *repository, cacheKey, area, prefix string, expand bool, items []internalChange, warnings []nodeprotocol.GitRepositoryWarning, truncated bool) *changeSnapshot {
	if len(items) > maxSnapshotChanges {
		items = items[:maxSnapshotChanges]
		truncated = true
		warnings = append(warnings, warning(nodeprotocol.GitWarningChangesTruncated))
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id := "gcs_" + hex.EncodeToString(raw)
	snapshot := &changeSnapshot{
		id: id, projectID: project.projectID, resourcesHash: project.resourcesHash,
		snapshotID:   project.snapshotID,
		repositoryID: repository.id, generation: repository.summary.Generation,
		area: area, prefix: prefix, expand: expand, items: items, byID: make(map[string]int, len(items)),
		warnings: append([]nodeprotocol.GitRepositoryWarning(nil), warnings...), truncated: truncated,
		createdAt: time.Now(), cacheKey: cacheKey,
	}
	for index := range snapshot.items {
		changeID := s.opaqueID("chg", id, area, snapshot.items[index].path, snapshot.items[index].oldPath, strconv.Itoa(index))
		snapshot.items[index].public.ID = changeID
		pathToken := s.opaqueID("path", id, snapshot.items[index].path, strconv.Itoa(index))
		snapshot.items[index].public.PathID = pathToken
		snapshot.items[index].public.Path = nodeprotocol.GitDisplayPath{
			Token: pathToken, Display: snapshot.items[index].public.DisplayPath,
		}
		snapshot.items[index].public.DiffAvailable = snapshot.items[index].public.Kind != "directory"
		snapshot.items[index].public.Additions = snapshot.items[index].public.Stats.Additions
		snapshot.items[index].public.Deletions = snapshot.items[index].public.Stats.Deletions
		snapshot.pathTokens = append(snapshot.pathTokens, pathToken)
		s.pathIndex[pathToken] = pathReference{
			snapshot: snapshot, projectID: project.projectID, repositoryID: repository.id,
			generation: repository.summary.Generation, path: snapshot.items[index].path,
			directory: snapshot.items[index].public.Kind == "directory",
		}
		if snapshot.items[index].oldPath != "" {
			oldToken := s.opaqueID("path", id, snapshot.items[index].oldPath, "old", strconv.Itoa(index))
			snapshot.items[index].public.OldPath = &nodeprotocol.GitDisplayPath{
				Token: oldToken, Display: snapshot.items[index].public.OldDisplayPath,
			}
			snapshot.pathTokens = append(snapshot.pathTokens, oldToken)
			s.pathIndex[oldToken] = pathReference{
				snapshot: snapshot, projectID: project.projectID, repositoryID: repository.id,
				generation: repository.summary.Generation, path: snapshot.items[index].oldPath,
			}
		}
		snapshot.byID[changeID] = index
		s.changeIndex[changeID] = snapshot
	}
	snapshot.lruElement = s.changeLRU.PushFront(snapshot)
	snapshot.estimatedBytes = estimateChangeSnapshotBytes(snapshot)
	s.changeBytes += snapshot.estimatedBytes
	s.changes[id] = snapshot
	s.changeKeys[cacheKey] = snapshot
	s.enforceChangeSnapshotLimitsLocked()
	return snapshot
}

func (s *Service) evictSnapshotLocked(element *list.Element) {
	if element == nil {
		return
	}
	snapshot := element.Value.(*changeSnapshot)
	delete(s.changes, snapshot.id)
	if s.changeKeys[snapshot.cacheKey] == snapshot {
		delete(s.changeKeys, snapshot.cacheKey)
	}
	s.changeBytes -= snapshot.estimatedBytes
	if s.changeBytes < 0 {
		s.changeBytes = 0
	}
	for changeID := range snapshot.byID {
		if s.changeIndex[changeID] == snapshot {
			delete(s.changeIndex, changeID)
		}
	}
	for _, pathToken := range snapshot.pathTokens {
		if reference, exists := s.pathIndex[pathToken]; exists && reference.snapshot == snapshot {
			delete(s.pathIndex, pathToken)
		}
	}
	s.changeLRU.Remove(element)
	snapshot.lruElement = nil
}

func estimateChangeSnapshotBytes(snapshot *changeSnapshot) int64 {
	// This is intentionally conservative. It includes map/list/interface
	// overhead in addition to all retained strings, so the byte ceiling remains
	// useful across Go releases without relying on unsafe.Sizeof internals.
	total := int64(2<<10 + len(snapshot.id) + len(snapshot.cacheKey) + len(snapshot.prefix))
	for _, item := range snapshot.items {
		total += 768
		total += int64(len(item.path) + len(item.oldPath) + len(item.mode) + len(item.sub))
		total += int64(len(item.public.DisplayPath) + len(item.public.OldDisplayPath) +
			len(item.public.ID) + len(item.public.PathID) + len(item.public.Path.Token))
		if item.public.OldPath != nil {
			total += int64(len(item.public.OldPath.Token) + len(item.public.OldPath.Display))
		}
	}
	for _, item := range snapshot.warnings {
		total += int64(128 + len(item.Code) + len(item.Message))
	}
	return total
}

func (s *Service) enforceChangeSnapshotLimitsLocked() {
	for s.changeLRU.Len() > maxChangeSnapshots || s.changeBytes > maxChangeSnapshotBytes {
		candidate := s.changeLRU.Back()
		for element := candidate; element != nil; element = element.Prev() {
			snapshot := element.Value.(*changeSnapshot)
			if len(s.subscribers[snapshot.projectID]) == 0 {
				candidate = element
				break
			}
		}
		if candidate == nil {
			return
		}
		snapshot := candidate.Value.(*changeSnapshot)
		if len(s.subscribers[snapshot.projectID]) > 0 {
			generation := snapshot.generation
			if project := s.projects[snapshot.projectID]; project != nil {
				if repository := project.repositories[snapshot.repositoryID]; repository != nil {
					generation = repository.summary.Generation
				}
			}
			s.emitLocked(snapshot.projectID, nodeprotocol.GitEvent{
				RepositoryID: snapshot.repositoryID, Generation: generation, Reason: "change_snapshot_evicted",
			})
		}
		s.evictSnapshotLocked(candidate)
	}
}

func (s *Service) invalidateProject(projectID, reason string) {
	s.mu.Lock()
	s.projectEpoch++
	project := s.projects[projectID]
	if project != nil {
		for _, repositoryID := range project.ordered {
			repository := project.repositories[repositoryID]
			repository.summary.Generation = s.nextGeneration()
			s.emitLocked(projectID, nodeprotocol.GitEvent{
				RepositoryID: repository.id, Generation: repository.summary.Generation, Reason: reason,
			})
		}
	}
	s.removeProjectLocked(projectID)
	s.mu.Unlock()
}

func (s *Service) expireProjectForRepository(projectID string, expected *repository, reason string) {
	s.mu.Lock()
	project := s.projects[projectID]
	if project == nil || project.repositories[expected.id] != expected {
		s.mu.Unlock()
		return
	}
	s.projectEpoch++
	for _, repositoryID := range project.ordered {
		repository := project.repositories[repositoryID]
		repository.summary.Generation = s.nextGeneration()
		s.emitLocked(projectID, nodeprotocol.GitEvent{
			RepositoryID: repository.id, Generation: repository.summary.Generation, Reason: reason,
		})
	}
	s.removeProjectLocked(projectID)
	s.mu.Unlock()
}

func (s *Service) installProjectLocked(project *projectSnapshot) {
	if existing := s.projects[project.projectID]; existing != nil {
		s.removeProjectLocked(project.projectID)
	}
	project.lruElement = s.projectLRU.PushFront(project)
	project.estimatedBytes = estimateProjectSnapshotBytes(project)
	s.projectBytes += project.estimatedBytes
	s.projects[project.projectID] = project
	for s.projectLRU.Len() > maxProjectSnapshots || s.projectBytes > maxProjectSnapshotBytes {
		var candidate *list.Element
		for element := s.projectLRU.Back(); element != nil; element = element.Prev() {
			candidateProject := element.Value.(*projectSnapshot)
			if len(s.subscribers[candidateProject.projectID]) == 0 {
				candidate = element
				break
			}
		}
		if candidate == nil {
			candidate = s.projectLRU.Back()
		}
		if candidate == nil {
			return
		}
		candidateProject := candidate.Value.(*projectSnapshot)
		if len(s.subscribers[candidateProject.projectID]) > 0 {
			for _, repositoryID := range candidateProject.ordered {
				repository := candidateProject.repositories[repositoryID]
				s.emitLocked(candidateProject.projectID, nodeprotocol.GitEvent{
					RepositoryID: repository.id, Generation: repository.summary.Generation,
					Reason: "project_snapshot_evicted",
				})
			}
		}
		s.removeProjectLocked(candidateProject.projectID)
	}
}

func estimateProjectSnapshotBytes(project *projectSnapshot) int64 {
	total := int64(2<<10 + len(project.projectID) + len(project.snapshotID) + len(project.resourcesHash))
	for _, warning := range project.warnings {
		total += int64(128 + len(warning.Code) + len(warning.Message))
	}
	for _, repository := range project.repositories {
		total += 4 << 10
		total += int64(len(repository.id) + len(repository.commonID) + len(repository.root) +
			len(repository.gitDir) + len(repository.commonDir) + len(repository.fingerprint))
		for _, prefix := range repository.authPrefixes {
			total += int64(64 + len(prefix))
		}
		for _, resourceID := range repository.resourceIDs {
			total += int64(64 + len(resourceID))
		}
		total += int64(len(repository.authorizationHash) + len(repository.summary.Label) + len(repository.summary.RootDisplay) +
			len(repository.summary.Origin) + len(repository.summary.Branch.Name) + len(repository.summary.Branch.OID))
		for _, warning := range repository.summary.Warnings {
			total += int64(128 + len(warning.Code) + len(warning.Message))
		}
	}
	return total
}

func (s *Service) removeProjectLocked(projectID string) {
	project := s.projects[projectID]
	if project == nil {
		return
	}
	delete(s.projects, projectID)
	s.projectBytes -= project.estimatedBytes
	if s.projectBytes < 0 {
		s.projectBytes = 0
	}
	for _, repository := range project.repositories {
		repository.closeIdentity()
	}
	if project.lruElement != nil {
		s.projectLRU.Remove(project.lruElement)
		project.lruElement = nil
	}
	for element := s.changeLRU.Back(); element != nil; {
		previous := element.Prev()
		if element.Value.(*changeSnapshot).projectID == projectID {
			s.evictSnapshotLocked(element)
		}
		element = previous
	}
	for target := range s.watchRoots {
		if target.projectID == projectID {
			delete(s.watchRoots, target)
			delete(s.watchLimited, target)
			delete(s.watchRebuild, target)
			delete(s.pendingEvents, target)
		}
	}
	for directory, targets := range s.watched {
		for target := range targets {
			if target.projectID == projectID {
				delete(targets, target)
			}
		}
		if len(targets) == 0 {
			delete(s.watched, directory)
			delete(s.watchIdentity, directory)
			if s.watcher != nil {
				_ = s.watcher.Remove(directory)
			}
		}
	}
}

func (s *Service) invalidateRepository(target repoTarget, reason string) {
	s.invalidateRepositoryIfCurrent(target, nil, reason)
}

func (s *Service) invalidateRepositoryIfCurrent(target repoTarget, expected *repository, reason string) {
	s.mu.Lock()
	project := s.projects[target.projectID]
	if project == nil {
		s.mu.Unlock()
		return
	}
	repository := project.repositories[target.repositoryID]
	if repository == nil || (expected != nil && repository != expected) {
		s.mu.Unlock()
		return
	}
	repository.summary.Generation = s.nextGeneration()
	repository.dirty = true
	for element := s.changeLRU.Back(); element != nil; {
		previous := element.Prev()
		snapshot := element.Value.(*changeSnapshot)
		if snapshot.projectID == target.projectID && snapshot.repositoryID == target.repositoryID {
			s.evictSnapshotLocked(element)
		}
		element = previous
	}
	s.emitLocked(target.projectID, nodeprotocol.GitEvent{
		RepositoryID: repository.id, Generation: repository.summary.Generation, Reason: reason,
	})
	s.mu.Unlock()
}

func (s *Service) emitLocked(projectID string, event nodeprotocol.GitEvent) {
	for subscriber := range s.subscribers[projectID] {
		select {
		case subscriber.channel <- event:
		default:
			s.coalesceSubscriberLocked(subscriber, event)
		}
	}
}

func (s *Service) coalesceSubscriberLocked(subscriber *eventSubscriber, newest nodeprotocol.GitEvent) {
	coalesced := map[string]nodeprotocol.GitEvent{newest.RepositoryID: newest}
	for {
		select {
		case queued := <-subscriber.channel:
			current, exists := coalesced[queued.RepositoryID]
			if !exists || queued.Generation > current.Generation ||
				(queued.RepositoryID != newest.RepositoryID && queued.Generation == current.Generation) {
				coalesced[queued.RepositoryID] = queued
			}
		default:
			keys := make([]string, 0, len(coalesced))
			for repositoryID := range coalesced {
				keys = append(keys, repositoryID)
			}
			sort.Strings(keys)
			for _, repositoryID := range keys {
				subscriber.channel <- coalesced[repositoryID]
			}
			return
		}
	}
}

func (s *Service) singleflight(ctx context.Context, key string, function func() (any, error)) (any, error) {
	s.mu.Lock()
	if existing := s.flights[key]; existing != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, gitError("runtime_closed", "Git 读取服务已关闭", false)
		case <-existing.done:
			return existing.value, existing.err
		}
	}
	if len(s.flights) >= maxFlights {
		s.mu.Unlock()
		return nil, gitError("git_busy", "Git 读取任务已达到并发上限", true)
	}
	call := &flight{done: make(chan struct{})}
	s.flights[key] = call
	s.mu.Unlock()
	call.value, call.err = function()
	s.mu.Lock()
	delete(s.flights, key)
	close(call.done)
	s.mu.Unlock()
	return call.value, call.err
}

func (s *Service) opaqueID(prefix string, values ...string) string {
	mac := hmac.New(sha256.New, s.secret[:])
	for _, value := range values {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

func stableSHA256ID(prefix string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("teamva/git-identity/v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(prefix))
	_, _ = hash.Write([]byte{0})
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func (s *Service) encodeCursor(snapshotID string, offset int) string {
	payload := snapshotID + ":" + strconv.Itoa(offset)
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = mac.Write([]byte(payload))
	value := payload + ":" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (s *Service) decodeCursor(cursor string) (string, int, error) {
	if len(cursor) > 512 {
		return "", 0, errors.New("invalid cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) > 256 {
		return "", 0, errors.New("invalid cursor")
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "gcs_") {
		return "", 0, errors.New("invalid cursor")
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return "", 0, errors.New("invalid cursor")
	}
	payload := parts[0] + ":" + parts[1]
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return "", 0, errors.New("invalid cursor")
	}
	return parts[0], offset, nil
}

func validateProjectRequest(projectID string, resources []nodeprotocol.GitResourceRequest) error {
	if strings.TrimSpace(projectID) == "" || len(projectID) > 256 || strings.ContainsRune(projectID, 0) {
		return gitError("invalid_argument", "project_id 不能为空", false)
	}
	if len(resources) > nodeprotocol.MaxGitResourcesPerRun {
		return gitError("invalid_argument", "项目资源数量超过上限", false)
	}
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if strings.TrimSpace(resource.ResourceID) == "" || strings.TrimSpace(resource.Name) == "" ||
			len(resource.ResourceID) > nodeprotocol.MaxGitResourceIDBytes ||
			len(resource.Name) > nodeprotocol.MaxGitResourceNameBytes ||
			len(resource.Path) > nodeprotocol.MaxGitResourcePathBytes || strings.ContainsRune(resource.Path, 0) ||
			!filepath.IsAbs(resource.Path) {
			return gitError("invalid_argument", "Git 资源请求无效", false)
		}
		if _, exists := seen[resource.ResourceID]; exists {
			return gitError("invalid_argument", "Git 资源请求包含重复 ID", false)
		}
		seen[resource.ResourceID] = struct{}{}
	}
	return nil
}

func hashResources(resources []nodeprotocol.GitResourceRequest) string {
	hash := sha256.New()
	for _, resource := range resources {
		_, _ = hash.Write([]byte(resource.ResourceID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(resource.Name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(filepath.Clean(resource.Path)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizePrefix(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.ContainsRune(value, 0) || filepath.IsAbs(value) {
		return "", errors.New("invalid prefix")
	}
	cleaned := path.Clean(strings.TrimPrefix(value, "./"))
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("invalid prefix")
	}
	return cleaned, nil
}

func (r *repository) authorizes(relative string) bool {
	cleaned, err := normalizePrefix(relative)
	if err != nil {
		return false
	}
	for _, prefix := range r.authPrefixes {
		if prefix == "" || cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

func validArea(area string) bool {
	switch area {
	case "", "conflict", "staged", "unstaged", "untracked":
		return true
	default:
		return false
	}
}

func gitError(code, _ string, retryable bool) error {
	return &nodeprotocol.GitReadError{Code: code, Message: gitErrorFallback(code), Retryable: retryable}
}

func gitErrorFallback(code string) string {
	switch code {
	case "invalid_argument":
		return "The Git request is invalid."
	case "runtime_closed":
		return "Git read service is closed."
	case "snapshot_expired":
		return "The Git snapshot is no longer current; refresh and retry."
	case "repository_not_found":
		return "The Git repository is unavailable or outside the authorized resources."
	case "git_busy":
		return "Git read concurrency reached its safety limit."
	case "path_out_of_scope":
		return "The Git path is outside the authorized resource scope."
	case "change_not_found":
		return "The Git change is unavailable, expired, or outside the authorized resources."
	case "invalid_cursor":
		return "The Git cursor is invalid or expired."
	case "git_unavailable":
		return "Git cannot safely read this repository."
	case "git_unsupported":
		return "This repository cannot be read safely on the current platform."
	case "git_metadata_too_large":
		return "Git metadata reached a safety limit."
	case "git_timeout":
		return "Git reading timed out."
	case "change_unavailable":
		return "The changed file is unavailable or is not a safe regular file."
	case "stats_unavailable":
		return "Git change statistics are temporarily unavailable."
	case "stats_too_large":
		return "Git change statistics reached a safety limit."
	default:
		return "Git reading failed."
	}
}

func warning(code string) nodeprotocol.GitRepositoryWarning {
	return nodeprotocol.NewGitRepositoryWarning(code)
}

func safeDisplay(value string) string {
	value = filepath.ToSlash(value)
	var result strings.Builder
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			result.WriteString("\\x")
			result.WriteString(hex.EncodeToString([]byte{value[0]}))
			value = value[1:]
			continue
		}
		value = value[size:]
		if unicode.IsControl(r) {
			if r <= 0xff {
				result.WriteString(fmt.Sprintf("\\x%02x", r))
			} else {
				result.WriteString("�")
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
