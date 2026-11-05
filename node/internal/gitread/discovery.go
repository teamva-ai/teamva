package gitread

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type canonicalResource struct {
	id    string
	name  string
	path  string
	index int
}

type repositoryIdentity struct {
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
}

type discoveredRepository struct {
	identity      repositoryIdentity
	resourceIDs   map[string]struct{}
	resourceOrder map[string]int
	authPrefixes  map[string]struct{}
	full          bool
	label         string
	rootDisplay   string
	displayRank   int
	origin        string
	sortResource  int
	sortKind      int
	sortRelative  string
}

func (s *Service) discover(ctx context.Context, projectID string, resources []nodeprotocol.GitResourceRequest, resourcesHash string) (*projectSnapshot, error) {
	discoveryContext, cancel := context.WithTimeout(ctx, projectDiscoveryTimeout)
	defer cancel()
	discovered, limited := s.discoverCandidates(discoveryContext, resources)

	project := &projectSnapshot{
		projectID: projectID, resourcesHash: resourcesHash,
		snapshotID:   s.opaqueID("gits", projectID, resourcesHash, time.Now().UTC().Format(time.RFC3339Nano)),
		repositories: make(map[string]*repository),
		truncated:    limited, warnings: []nodeprotocol.GitRepositoryWarning{},
	}
	if limited {
		project.warnings = append(project.warnings, warning(nodeprotocol.GitWarningDiscoveryLimitExceeded))
	}
	values := make([]*discoveredRepository, 0, len(discovered))
	for _, item := range discovered {
		values = append(values, item)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].sortResource != values[j].sortResource {
			return values[i].sortResource < values[j].sortResource
		}
		if values[i].sortKind != values[j].sortKind {
			return values[i].sortKind < values[j].sortKind
		}
		if values[i].sortRelative != values[j].sortRelative {
			return values[i].sortRelative < values[j].sortRelative
		}
		return values[i].identity.root < values[j].identity.root
	})
	for _, item := range values {
		if len(project.ordered) >= maxRepositories {
			item.identity.close()
			limited = true
			break
		}
		repositoryID := stableRepositoryID(item.identity)
		commonID := stableSHA256ID("common", item.identity.commonDir, item.identity.commonIdentity.stable)
		resourceIDs := orderedResourceIDs(item.resourceIDs, item.resourceOrder)
		authPrefixes := mapKeys(item.authPrefixes)
		if item.full {
			authPrefixes = []string{""}
		}
		worktree := worktreeKind(item.identity)
		repository := &repository{
			id: repositoryID, commonID: commonID, root: item.identity.root,
			gitDir: item.identity.gitDir, commonDir: item.identity.commonDir,
			rootIdentity: item.identity.rootIdentity, gitIdentity: item.identity.gitIdentity,
			commonIdentity: item.identity.commonIdentity,
			objectIdentity: item.identity.objectIdentity,
			markerIdentity: item.identity.markerIdentity, commonMarker: item.identity.commonMarker,
			settings:          item.identity.settings,
			authorizationHash: s.resourceAuthorizationScopeHash(resourcesHash),
			authPrefixes:      authPrefixes, resourceIDs: resourceIDs,
			watchState: make(map[string]watchObservation),
			summary: nodeprotocol.GitRepositorySummary{
				ID: repositoryID, CommonID: commonID, Label: item.label,
				RootDisplay: item.rootDisplay, ResourceIDs: resourceIDs,
				DiscoveryOrigin: item.origin, WorktreeKind: worktree,
				Worktree: nodeprotocol.GitWorktreeSummary{
					Linked: worktree == "linked",
					Main:   worktree == "main",
				},
				Scope:      map[bool]string{true: "full", false: "resource_prefix"}[item.full],
				Generation: s.nextGeneration(), State: "unknown",
				Warnings: []nodeprotocol.GitRepositoryWarning{},
			},
		}
		repository.summary.Origin = sanitizedOrigin(item.identity.settings.origin)
		branch, counts, fingerprint, statusErr := s.readRepositoryStatus(discoveryContext, repository)
		if statusErr != nil {
			repository.summary.State = "unavailable"
			repository.summary.Warnings = append(repository.summary.Warnings,
				warning(nodeprotocol.GitWarningStatusUnavailable))
		} else {
			repository.summary.Branch = branch
			repository.summary.Counts = counts
			repository.summary.State = repositoryState(counts)
			repository.fingerprint = fingerprint
		}
		repository.summary.Operation = readOperation(repository)
		if limited {
			repository.summary.Warnings = append(repository.summary.Warnings,
				warning(nodeprotocol.GitWarningDiscoveryLimitExceeded))
		}
		project.repositories[repositoryID] = repository
		project.ordered = append(project.ordered, repositoryID)
	}
	return project, nil
}

// discoverCandidates is the single bounded, nofollow discovery traversal used
// by both project discovery and resource validation. A Resource itself may be
// a repository or a partial scope in an ancestor repository. Ordinary nested
// repositories are scanned to depth two; nested .git files (submodules and
// linked worktrees) retain the explicit-Resource boundary.
func (s *Service) discoverCandidates(ctx context.Context, resources []nodeprotocol.GitResourceRequest) (map[string]*discoveredRepository, bool) {
	canonical := make([]canonicalResource, 0, len(resources))
	for index, resource := range resources {
		resolved, err := canonicalResourceDirectory(resource.Path)
		if err != nil {
			continue
		}
		canonical = append(canonical, canonicalResource{
			id: resource.ResourceID, name: safeDisplay(resource.Name), path: resolved, index: index,
		})
	}

	discovered := make(map[string]*discoveredRepository)
	directories := 0
	entriesRead := 0
	limited := false
	for _, resource := range canonical {
		if ctx.Err() != nil || len(discovered) >= maxRepositories {
			limited = true
			break
		}
		// rev-parse at the Resource itself discovers both a repository rooted at
		// the Resource and a containing repository above it.
		if identity, err := s.confirmRepository(ctx, resource.path); err == nil {
			s.mergeDiscovered(discovered, resource, identity, true)
		}
		resourceIdentity, identityErr := captureStablePathIdentity(resource.path)
		if identityErr != nil || resourceIdentity.validate() != nil {
			resourceIdentity.close()
			continue
		}
		directory, openErr := duplicateIdentityFile(resourceIdentity.handle)
		if openErr == nil {
			completed := s.scanDiscoveryDirectory(ctx, resource, resource.path, directory, 0,
				discovered, &directories, &entriesRead)
			_ = directory.Close()
			if !completed {
				limited = true
			}
		}
		resourceIdentity.close()
	}
	return discovered, limited
}

func (s *Service) scanDiscoveryDirectory(
	ctx context.Context,
	resource canonicalResource,
	directoryPath string,
	directory *os.File,
	depth int,
	discovered map[string]*discoveredRepository,
	directories, entriesRead *int,
) bool {
	if directory == nil || ctx.Err() != nil || *directories >= maxDiscoveryDirectories ||
		len(discovered) >= maxRepositories {
		return false
	}
	handleInfo, handleErr := directory.Stat()
	pathInfo, pathErr := os.Lstat(directoryPath)
	if handleErr != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.IsDir() || !os.SameFile(handleInfo, pathInfo) {
		return false
	}
	(*directories)++
	if depth > 0 {
		// Nested .git files are linked worktrees/submodules and only become peers
		// when explicitly registered as Resources. Directory markers are safe to
		// consider as ordinary nested repositories.
		if mode, err := sandboxEntryModeAt(directory, ".git"); err == nil && mode == sandboxModeDirectory() {
			if identity, confirmErr := s.confirmRepository(ctx, directoryPath); confirmErr == nil {
				if samePath(identity.root, directoryPath) {
					s.mergeDiscovered(discovered, resource, identity, false)
				} else {
					identity.close()
				}
			}
		}
	}
	if depth >= maxDiscoveryDepth {
		return true
	}
	for {
		if ctx.Err() != nil {
			return false
		}
		entries, readErr := directory.ReadDir(256)
		for _, entry := range entries {
			(*entriesRead)++
			if *entriesRead > maxDiscoveryEntries || ctx.Err() != nil || len(discovered) >= maxRepositories {
				return false
			}
			if entry.Name() == ".git" {
				continue
			}
			mode, statErr := sandboxEntryModeAt(directory, entry.Name())
			if statErr != nil || mode != sandboxModeDirectory() {
				continue
			}
			child, openErr := openSandboxDirectoryAt(directory, entry.Name())
			if openErr != nil {
				continue
			}
			completed := s.scanDiscoveryDirectory(ctx, resource, filepath.Join(directoryPath, entry.Name()),
				child, depth+1, discovered, directories, entriesRead)
			_ = child.Close()
			if !completed {
				return false
			}
		}
		if readErr == nil {
			continue
		}
		return errors.Is(readErr, io.EOF)
	}
}

func canonicalDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", os.ErrNotExist
	}
	return filepath.Clean(resolved), nil
}

func (s *Service) confirmRepository(ctx context.Context, directory string) (repositoryIdentity, error) {
	canonical, err := canonicalDirectory(directory)
	if err != nil {
		return repositoryIdentity{}, gitError("git_unavailable", "目录不是可读取的 Git 工作树", false)
	}
	root := canonical
	for depth := 0; depth < 256; depth++ {
		marker, markerErr := os.Lstat(filepath.Join(root, ".git"))
		if markerErr == nil && marker.Mode()&os.ModeSymlink == 0 && (marker.IsDir() || marker.Mode().IsRegular()) {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return repositoryIdentity{}, gitError("git_unavailable", "目录不是可读取的 Git 工作树", false)
		}
		root = parent
	}
	markerIdentity, err := captureStableMarker(root)
	if err != nil {
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库 marker 不安全", false)
	}
	gitDir := filepath.Join(root, ".git")
	if !markerIdentity.directory {
		payload, readErr := readBoundedFile(ctx, markerIdentity.handle, maxGitMarkerBytes)
		if readErr != nil {
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库 marker 不可读取", false)
		}
		line := strings.TrimSpace(string(payload))
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库 marker 无效", false)
		}
		gitDir = strings.TrimSpace(line[len("gitdir:"):])
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	gitDir, err = canonicalDirectory(gitDir)
	if err != nil {
		markerIdentity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库元数据不可读取", false)
	}
	commonDir := gitDir
	var commonMarkerIdentity stableMarkerIdentity
	commonMarker := filepath.Join(gitDir, "commondir")
	if info, markerErr := os.Lstat(commonMarker); markerErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git common dir marker 不安全", false)
		}
		capturedMarker, openErr := captureStableMarkerPath(commonMarker)
		if openErr != nil {
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git common dir marker 不可读取", false)
		}
		commonMarkerIdentity = capturedMarker
		payload, readErr := readBoundedFile(ctx, commonMarkerIdentity.handle, maxGitMarkerBytes)
		if readErr != nil {
			commonMarkerIdentity.close()
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git common dir marker 不可读取", false)
		}
		commonDir = strings.TrimSpace(string(payload))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		commonDir, err = canonicalDirectory(commonDir)
		if err != nil {
			commonMarkerIdentity.close()
			markerIdentity.close()
			return repositoryIdentity{}, gitError("git_unavailable", "Git common dir 不可读取", false)
		}
	} else if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		markerIdentity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git common dir marker 不可读取", false)
	}
	rootIdentity, rootIdentityErr := captureStablePathIdentity(root)
	gitIdentity, gitIdentityErr := captureStablePathIdentity(gitDir)
	commonIdentity, commonIdentityErr := captureStablePathIdentity(commonDir)
	objectIdentity, objectIdentityErr := captureStablePathIdentity(filepath.Join(commonDir, "objects"))
	if rootIdentityErr != nil || gitIdentityErr != nil || commonIdentityErr != nil || objectIdentityErr != nil {
		rootIdentity.close()
		gitIdentity.close()
		commonIdentity.close()
		objectIdentity.close()
		markerIdentity.close()
		commonMarkerIdentity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库路径身份不可读取", false)
	}
	identity := repositoryIdentity{
		root: root, gitDir: gitDir, commonDir: commonDir,
		rootIdentity: rootIdentity, gitIdentity: gitIdentity, commonIdentity: commonIdentity,
		objectIdentity: objectIdentity, markerIdentity: markerIdentity, commonMarker: commonMarkerIdentity,
	}
	if err := identity.validate(); err != nil {
		identity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库路径身份在读取前发生变化", false)
	}
	settings, settingsErr := readRepositorySettings(ctx, root, gitDir,
		gitIdentity.handle, commonIdentity.handle)
	if settingsErr != nil {
		identity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库配置不安全或不受支持", false)
	}
	identity.settings = settings
	if err := identity.validate(); err != nil {
		identity.close()
		return repositoryIdentity{}, gitError("git_unavailable", "Git 仓库路径身份在读取期间发生变化", false)
	}
	return identity, nil
}

// DetectResourceRepository applies the same repository boundary rules as
// project discovery with a validation-specific two-second deadline. Discovery
// remains bounded by 100 repositories, 20,000 directories, 100,000 entries,
// and depth two, and never executes repository-controlled Git configuration.
func (s *Service) DetectResourceRepository(ctx context.Context, directory string) bool {
	done, err := s.beginServiceOperation()
	if err != nil {
		return false
	}
	defer done()
	detectionContext, cancel := context.WithTimeout(ctx, resourceDetectionTimeout)
	defer cancel()
	discovered, _ := s.discoverCandidates(detectionContext, []nodeprotocol.GitResourceRequest{{
		ResourceID: "resource_validation",
		Name:       "Resource",
		Path:       directory,
	}})
	detected := len(discovered) > 0
	for _, repository := range discovered {
		repository.identity.close()
	}
	return detected
}

func (s *Service) mergeDiscovered(target map[string]*discoveredRepository, resource canonicalResource, identity repositoryIdentity, explicit bool) {
	rootInResource, rootRelative := pathWithin(resource.path, identity.root)
	resourceInRoot, resourceRelative := pathWithin(identity.root, resource.path)
	if !rootInResource && !resourceInRoot {
		identity.close()
		return
	}
	// A marker found below a Resource may not redirect to a linked worktree
	// outside that Resource. Explicit Resources are allowed to name a submodule
	// or linked worktree directly.
	if !explicit && !rootInResource {
		identity.close()
		return
	}
	key := identity.root + "\x00" + identity.gitDir + "\x00" + identity.gitIdentity.stable
	item := target[key]
	if item == nil {
		item = &discoveredRepository{
			identity: identity, resourceIDs: make(map[string]struct{}),
			resourceOrder: make(map[string]int),
			authPrefixes:  make(map[string]struct{}), displayRank: 100,
			label: safeDisplay(filepath.Base(identity.root)), sortResource: resource.index,
			sortKind: 100,
		}
		target[key] = item
	} else {
		identity.close()
	}
	item.resourceIDs[resource.id] = struct{}{}
	item.resourceOrder[resource.id] = resource.index
	if rootInResource {
		kind := 1
		if rootRelative == "" {
			kind = 0
		}
		item.considerSort(resource.index, kind, filepath.ToSlash(rootRelative))
		item.full = true
		item.authPrefixes = map[string]struct{}{"": {}}
		rank := 1
		display := resource.name
		if rootRelative != "" {
			rank = 2
			display += "/" + safeDisplay(rootRelative)
		}
		if rank < item.displayRank {
			item.displayRank = rank
			item.rootDisplay = display
			if rootRelative == "" {
				item.origin = "resource"
			} else {
				item.origin = "nested"
			}
		}
		return
	}
	if resourceInRoot && !item.full {
		item.considerSort(resource.index, 2, filepath.ToSlash(resourceRelative))
		item.authPrefixes[filepath.ToSlash(resourceRelative)] = struct{}{}
		if 3 < item.displayRank {
			item.displayRank = 3
			item.rootDisplay = resource.name
			item.origin = "ancestor"
		}
	}
}

func (r *discoveredRepository) considerSort(resource, kind int, relative string) {
	if resource < r.sortResource ||
		(resource == r.sortResource && (kind < r.sortKind ||
			(kind == r.sortKind && relative < r.sortRelative))) {
		r.sortResource = resource
		r.sortKind = kind
		r.sortRelative = relative
	}
}

func stableRepositoryID(identity repositoryIdentity) string {
	return stableSHA256ID("repo", identity.root, identity.gitDir, identity.commonDir,
		identity.rootIdentity.stable, identity.gitIdentity.stable, identity.commonIdentity.stable,
		identity.objectIdentity.stable, identity.markerIdentity.stable, identity.markerIdentity.content,
		identity.commonMarker.stable, identity.commonMarker.content)
}

func worktreeKind(identity repositoryIdentity) string {
	if !samePath(identity.gitDir, identity.commonDir) {
		return "linked"
	}
	if marker, err := os.Lstat(filepath.Join(identity.root, ".git")); err == nil && marker.Mode().IsRegular() && marker.Mode()&os.ModeSymlink == 0 {
		return "submodule"
	}
	return "main"
}

func pathWithin(parent, child string) (bool, string) {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false, ""
	}
	if relative == "." {
		return true, ""
	}
	return true, filepath.ToSlash(relative)
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func mapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func orderedResourceIDs(values map[string]struct{}, order map[string]int) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if order[result[i]] != order[result[j]] {
			return order[result[i]] < order[result[j]]
		}
		return result[i] < result[j]
	})
	return result
}

func sanitizedOrigin(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "file:") {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		if parsed.Scheme == "file" {
			return ""
		}
		return parsed.String()
	}
	// SCP-style SSH remotes may contain a user name. It is unnecessary for
	// display and can itself be sensitive, so only retain host:path.
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	if !strings.Contains(value, ":") {
		return ""
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return safeDisplay(value)
}

func readOperation(repository *repository) nodeprotocol.GitOperationSummary {
	if repository == nil {
		return nodeprotocol.GitOperationSummary{}
	}
	repository.handleMu.Lock()
	if repository.handlesClosed || repository.gitIdentity.handle == nil {
		repository.handleMu.Unlock()
		return nodeprotocol.GitOperationSummary{}
	}
	directory, err := duplicateIdentityFile(repository.gitIdentity.handle)
	repository.handleMu.Unlock()
	if err != nil {
		return nodeprotocol.GitOperationSummary{}
	}
	defer directory.Close()
	candidates := []struct {
		name      string
		kind      string
		directory bool
	}{
		{"rebase-merge", "rebase", true}, {"rebase-apply", "rebase", true},
		{"MERGE_HEAD", "merge", false}, {"CHERRY_PICK_HEAD", "cherry_pick", false},
		{"REVERT_HEAD", "revert", false}, {"BISECT_LOG", "bisect", false},
	}
	for _, candidate := range candidates {
		mode, err := sandboxEntryModeAt(directory, candidate.name)
		if err == nil && ((candidate.directory && mode == sandboxModeDirectory()) ||
			(!candidate.directory && mode == sandboxModeRegular())) {
			return nodeprotocol.GitOperationSummary{Kind: candidate.kind}
		}
	}
	return nodeprotocol.GitOperationSummary{}
}

func repositoryState(counts nodeprotocol.GitChangeCounts) string {
	if counts.Conflict > 0 {
		return "conflicted"
	}
	if counts.Staged+counts.Unstaged+counts.Untracked > 0 {
		return "dirty"
	}
	return "clean"
}
