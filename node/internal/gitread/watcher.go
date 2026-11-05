package gitread

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/fsnotify/fsnotify"
)

func (s *Service) refreshDirtyRepositories(ctx context.Context, project *projectSnapshot) error {
	type dirtyRepository struct {
		repository *repository
		generation int64
	}
	s.mu.Lock()
	dirty := make([]dirtyRepository, 0)
	current := s.projects[project.projectID]
	if current != nil && current.resourcesHash == project.resourcesHash {
		for _, repositoryID := range current.ordered {
			if repository := current.repositories[repositoryID]; repository != nil && repository.dirty {
				dirty = append(dirty, dirtyRepository{repository: repository, generation: repository.summary.Generation})
			}
		}
	}
	s.mu.Unlock()
	for _, candidate := range dirty {
		item := candidate.repository
		branch, counts, fingerprint, err := s.readRepositoryStatus(ctx, item)
		s.mu.Lock()
		currentProject := s.projects[project.projectID]
		var currentRepository *repository
		if currentProject != nil {
			currentRepository = currentProject.repositories[item.id]
		}
		if currentRepository == item && item.summary.Generation == candidate.generation {
			if err != nil {
				item.summary.State = "unavailable"
				appendWarningOnce(&item.summary.Warnings,
					warning(nodeprotocol.GitWarningStatusUnavailable))
			} else {
				item.summary.Branch = branch
				item.summary.Counts = counts
				item.summary.State = repositoryState(counts)
				item.summary.Operation = readOperation(item)
				item.fingerprint = fingerprint
				item.dirty = false
			}
		}
		s.mu.Unlock()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) updateStatusObservation(projectID, repositoryID string, expectedGeneration int64, branch nodeprotocol.GitBranchSummary, counts nodeprotocol.GitChangeCounts, fingerprint string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := s.projects[projectID]
	if project == nil {
		return 0, false
	}
	repository := project.repositories[repositoryID]
	if repository == nil || repository.summary.Generation != expectedGeneration {
		return 0, false
	}
	if repository.fingerprint != "" && fingerprint != "" && repository.fingerprint != fingerprint {
		repository.summary.Generation = s.nextGeneration()
		for element := s.changeLRU.Back(); element != nil; {
			previous := element.Prev()
			snapshot := element.Value.(*changeSnapshot)
			if snapshot.projectID == projectID && snapshot.repositoryID == repositoryID {
				s.evictSnapshotLocked(element)
			}
			element = previous
		}
		s.emitLocked(projectID, nodeprotocol.GitEvent{
			RepositoryID: repositoryID, Generation: repository.summary.Generation, Reason: "status_changed",
		})
	}
	repository.summary.Branch = branch
	repository.summary.Counts = counts
	repository.summary.State = repositoryState(counts)
	repository.summary.Operation = readOperation(repository)
	repository.fingerprint = fingerprint
	repository.dirty = false
	return repository.summary.Generation, true
}

func (s *Service) ensureWatched(projectID string, repository *repository, deadline time.Time) {
	target := repoTarget{projectID: projectID, repositoryID: repository.id}
	if s.watcher == nil {
		s.markWatchLimited(target)
		return
	}
	roots := make([]string, 0, len(repository.authPrefixes)+2)
	for _, prefix := range repository.authPrefixes {
		root := repository.root
		if prefix != "" {
			root = filepath.Join(root, filepath.FromSlash(prefix))
		}
		roots = append(roots, root)
	}
	s.mu.Lock()
	project := s.projects[projectID]
	if project == nil || project.repositories[repository.id] != repository {
		s.mu.Unlock()
		return
	}
	s.watchRoots[target] = append([]string(nil), roots...)
	s.mu.Unlock()
	for _, root := range roots {
		base, err := repository.duplicateWorktreeHandle()
		if err != nil {
			s.markWatchLimited(target)
			return
		}
		inside, relative := pathWithin(repository.root, root)
		bound := base
		if !inside {
			_ = base.Close()
			s.markWatchLimited(target)
			return
		}
		if relative != "" {
			bound, err = openSandboxDirectoryAt(base, relative)
			_ = base.Close()
		}
		if err != nil || bound == nil {
			s.markWatchLimited(target)
			return
		}
		expected, statErr := bound.Stat()
		completed := statErr == nil &&
			s.addWatchTreeExpected(target, root, expected, -1, true, deadline)
		_ = bound.Close()
		if !completed {
			s.markWatchLimited(target)
			return
		}
	}
	type metadataRoot struct {
		path string
		open func() (*os.File, error)
	}
	metadataRoots := []metadataRoot{{repository.gitDir, repository.duplicateGitDirectoryHandle}}
	if !samePath(repository.gitDir, repository.commonDir) {
		metadataRoots = append(metadataRoots, metadataRoot{repository.commonDir, repository.duplicateCommonDirectoryHandle})
	}
	for _, root := range metadataRoots {
		bound, err := root.open()
		if err != nil {
			s.markWatchLimited(target)
			return
		}
		expected, statErr := bound.Stat()
		completed := statErr == nil && s.addWatchTreeExpected(target, root.path, expected, 4, false, deadline)
		_ = bound.Close()
		if !completed {
			s.markWatchLimited(target)
			return
		}
	}
	s.mu.Lock()
	delete(s.watchLimited, target)
	delete(s.watchRebuild, target)
	if project := s.projects[target.projectID]; project != nil {
		if repository := project.repositories[target.repositoryID]; repository != nil {
			removeWarning(&repository.summary.Warnings, nodeprotocol.GitWarningWatchLimitExceeded)
		}
	}
	s.mu.Unlock()
}

func (s *Service) addWatchTree(target repoTarget, root string, maxDepth int, source bool, deadline time.Time) bool {
	expected, err := os.Lstat(root)
	if err != nil || expected.Mode()&os.ModeSymlink != 0 || !expected.IsDir() {
		return false
	}
	return s.addWatchTreeExpected(target, root, expected, maxDepth, source, deadline)
}

func (s *Service) addWatchTreeExpected(target repoTarget, root string, expected os.FileInfo, maxDepth int, source bool, deadline time.Time) bool {
	type queueItem struct {
		path     string
		depth    int
		expected os.FileInfo
	}
	queue := []queueItem{{path: root, expected: expected}}
	entriesRead := 0
	for len(queue) > 0 {
		if time.Now().After(deadline) || s.ctx.Err() != nil {
			return false
		}
		item := queue[0]
		queue = queue[1:]
		if maxDepth >= 0 && item.depth > maxDepth {
			continue
		}
		info, err := os.Lstat(item.path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			(item.expected != nil && !os.SameFile(item.expected, info)) {
			if item.depth == 0 {
				return false
			}
			continue
		}
		directory, err := openIdentityDirectory(item.path)
		if err != nil {
			continue
		}
		openedInfo, statErr := directory.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			_ = directory.Close()
			if item.depth == 0 {
				return false
			}
			continue
		}
		if !s.addWatchDirectory(target, item.path, openedInfo) {
			_ = directory.Close()
			return false
		}
		s.rememberWatchObservation(target, item.path, openedInfo)
		stop := false
		for {
			entries, readErr := directory.ReadDir(256)
			for _, entry := range entries {
				entriesRead++
				if entriesRead > maxWatchScanEntries || time.Now().After(deadline) || s.ctx.Err() != nil {
					stop = true
					break
				}
				entryPath := filepath.Join(item.path, entry.Name())
				// DirEntry.Info resolves relative to os.File.Name. Identity handles
				// intentionally use a non-path diagnostic name, so use the already
				// validated absolute directory path and revalidate descendants when
				// they are opened below.
				entryInfo, infoErr := os.Lstat(entryPath)
				if infoErr == nil {
					s.rememberWatchObservation(target, entryPath, entryInfo)
				}
				if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
					continue
				}
				if source && entry.Name() == ".git" {
					continue
				}
				if !source && (entry.Name() == "objects" || entry.Name() == "logs" || entry.Name() == "hooks") {
					continue
				}
				if infoErr != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.IsDir() {
					continue
				}
				queue = append(queue, queueItem{path: entryPath,
					depth: item.depth + 1, expected: entryInfo})
			}
			if stop || errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				break
			}
		}
		_ = directory.Close()
		if stop {
			return false
		}
	}
	return true
}

func (s *Service) rememberWatchObservation(target repoTarget, path string, info os.FileInfo) {
	observation, ok := newWatchObservation(info)
	if !ok {
		return
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	project := s.projects[target.projectID]
	if project == nil {
		return
	}
	repository := project.repositories[target.repositoryID]
	if repository == nil {
		return
	}
	if repository.watchState == nil {
		repository.watchState = make(map[string]watchObservation)
	}
	// Once a watch is live, only fsnotify events advance an existing baseline.
	// A later reconciliation scan must not bless a user mutation whose event is
	// still waiting in the kernel queue.
	if _, exists := repository.watchState[path]; !exists {
		repository.watchState[path] = observation
	}
}

func (s *Service) addWatchDirectory(target repoTarget, directory string, expected os.FileInfo) bool {
	directory = filepath.Clean(directory)
	current, currentErr := os.Lstat(directory)
	if expected == nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() || !os.SameFile(expected, current) {
		return false
	}
	s.mu.Lock()
	project := s.projects[target.projectID]
	if project == nil || project.repositories[target.repositoryID] == nil {
		s.mu.Unlock()
		return false
	}
	if targets := s.watched[directory]; targets != nil {
		if watchedIdentity := s.watchIdentity[directory]; watchedIdentity == nil ||
			!os.SameFile(watchedIdentity, expected) {
			s.mu.Unlock()
			return false
		}
		targets[target] = struct{}{}
		s.mu.Unlock()
		return true
	}
	if len(s.watched) >= maxWatchedDirectories || s.closed {
		s.mu.Unlock()
		return false
	}
	if err := s.watcher.Add(directory); err != nil {
		s.mu.Unlock()
		return false
	}
	current, currentErr = os.Lstat(directory)
	if currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(expected, current) {
		_ = s.watcher.Remove(directory)
		s.mu.Unlock()
		return false
	}
	s.watched[directory] = map[repoTarget]struct{}{target: {}}
	s.watchIdentity[directory] = expected
	s.mu.Unlock()
	return true
}

func (s *Service) markWatchLimited(target repoTarget) {
	s.mu.Lock()
	s.watchLimited[target] = true
	if project := s.projects[target.projectID]; project != nil {
		if repository := project.repositories[target.repositoryID]; repository != nil {
			appendWarningOnce(&repository.summary.Warnings,
				warning(nodeprotocol.GitWarningWatchLimitExceeded))
		}
	}
	s.mu.Unlock()
}

func appendWarningOnce(target *[]nodeprotocol.GitRepositoryWarning, value nodeprotocol.GitRepositoryWarning) {
	for _, existing := range *target {
		if existing.Code == value.Code {
			return
		}
	}
	*target = append(*target, value)
}

func removeWarning(target *[]nodeprotocol.GitRepositoryWarning, code string) {
	filtered := (*target)[:0]
	for _, existing := range *target {
		if existing.Code != code {
			filtered = append(filtered, existing)
		}
	}
	*target = filtered
}

func (s *Service) watchLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			s.queueWatchEvent(event)
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) || err != nil {
				s.invalidateAll("overflow")
			}
		case now := <-ticker.C:
			s.flushPendingEvents(now)
		}
	}
}

func (s *Service) queueWatchEvent(event fsnotify.Event) {
	directory := filepath.Clean(event.Name)
	currentObservation, observationOK := observeWatchPath(directory)
	s.mu.Lock()
	targets := make(map[repoTarget]struct{})
	removedWatches := make([]string, 0)
	if direct := s.watched[directory]; direct != nil {
		for target := range direct {
			targets[target] = struct{}{}
		}
	}
	if parent := s.watched[filepath.Dir(directory)]; parent != nil {
		for target := range parent {
			targets[target] = struct{}{}
		}
	}
	if event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
		for watchedDirectory, watchedTargets := range s.watched {
			if watchedDirectory != directory && !pathUnder(directory, watchedDirectory) {
				continue
			}
			for target := range watchedTargets {
				targets[target] = struct{}{}
				s.watchRebuild[target] = true
			}
			delete(s.watched, watchedDirectory)
			delete(s.watchIdentity, watchedDirectory)
			removedWatches = append(removedWatches, watchedDirectory)
		}
	}
	now := time.Now()
	for target := range targets {
		reason := "worktree_changed"
		if project := s.projects[target.projectID]; project != nil {
			if repository := project.repositories[target.repositoryID]; repository != nil {
				// Darwin reports access-time updates caused by our own status/diff
				// reads as Chmod. Suppress only an attribute-only event whose complete
				// mutation-bearing identity still equals the service's prior
				// observation. A real chmod/chown/touch, index update, ref update,
				// replacement, write, rename, create, or remove cannot pass this check.
				if event.Op == fsnotify.Chmod && observationOK &&
					repository.watchState[directory] == currentObservation {
					continue
				}
				if repository.watchState == nil {
					repository.watchState = make(map[string]watchObservation)
				}
				if observationOK {
					repository.watchState[directory] = currentObservation
				} else {
					delete(repository.watchState, directory)
				}
				if pathUnder(repository.gitDir, directory) || pathUnder(repository.commonDir, directory) {
					reason = "git_metadata_changed"
				}
			}
		}
		pending := s.pendingEvents[target]
		if pending.first.IsZero() {
			pending.first = now
		}
		pending.reason = reason
		pending.due = now.Add(400 * time.Millisecond)
		maximum := pending.first.Add(2 * time.Second)
		if pending.due.After(maximum) {
			pending.due = maximum
		}
		s.pendingEvents[target] = pending
	}
	s.mu.Unlock()
	for _, watchedDirectory := range removedWatches {
		_ = s.watcher.Remove(watchedDirectory)
	}
	if event.Op&fsnotify.Create != 0 && filepath.Base(directory) != ".git" {
		info, err := os.Lstat(directory)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			for target := range targets {
				if s.isAuthorizedWatchDirectory(target, directory) &&
					!s.addWatchTree(target, directory, -1, true, time.Now().Add(500*time.Millisecond)) {
					s.markWatchLimited(target)
				}
			}
		}
	}
}

func (s *Service) isAuthorizedWatchDirectory(target repoTarget, directory string) bool {
	s.mu.Lock()
	roots := append([]string(nil), s.watchRoots[target]...)
	s.mu.Unlock()
	for _, root := range roots {
		if pathUnder(root, directory) {
			return true
		}
	}
	return false
}

func pathUnder(root, candidate string) bool {
	inside, _ := pathWithin(filepath.Clean(root), filepath.Clean(candidate))
	return inside
}

func (s *Service) flushPendingEvents(now time.Time) {
	s.mu.Lock()
	due := make([]repoTarget, 0)
	reasons := make(map[repoTarget]string)
	for target, pending := range s.pendingEvents {
		if !now.Before(pending.due) {
			due = append(due, target)
			reasons[target] = pending.reason
			delete(s.pendingEvents, target)
		}
	}
	s.mu.Unlock()
	for _, target := range due {
		s.invalidateRepository(target, reasons[target])
	}
}

func (s *Service) invalidateAll(reason string) {
	s.mu.Lock()
	targets := make([]repoTarget, 0)
	for projectID, project := range s.projects {
		for repositoryID := range project.repositories {
			targets = append(targets, repoTarget{projectID: projectID, repositoryID: repositoryID})
		}
	}
	s.mu.Unlock()
	for _, target := range targets {
		s.invalidateRepository(target, reason)
	}
}

func (s *Service) reconcileLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.reconcile()
		}
	}
}

func (s *Service) reconcile() {
	type candidate struct {
		target      repoTarget
		repository  *repository
		fingerprint string
	}
	reconcileContext, cancelReconcile := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancelReconcile()
	s.pruneInvalidWatches()
	s.mu.Lock()
	candidates := make([]candidate, 0)
	for projectID, project := range s.projects {
		for repositoryID, repository := range project.repositories {
			target := repoTarget{projectID: projectID, repositoryID: repositoryID}
			candidates = append(candidates, candidate{
				target:     target,
				repository: repository, fingerprint: repository.fingerprint,
			})
		}
	}
	s.mu.Unlock()
	jobs := make(chan candidate)
	var wait sync.WaitGroup
	workers := 16
	if len(candidates) < workers {
		workers = len(candidates)
	}
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for item := range jobs {
				if item.repository.validateIdentity() != nil {
					s.expireProjectForRepository(item.target.projectID, item.repository, "repository_replaced")
					continue
				}
				ctx, cancel := context.WithTimeout(reconcileContext, 2*time.Second)
				_, _, fingerprint, err := s.readRepositoryStatus(ctx, item.repository)
				cancel()
				if err == nil && (item.fingerprint == "" || fingerprint != item.fingerprint) {
					s.invalidateRepositoryIfCurrent(item.target, item.repository, "reconcile")
				}
				if err == nil {
					s.ensureWatched(item.target.projectID, item.repository, time.Now().Add(500*time.Millisecond))
				}
			}
		}()
	}
	for _, item := range candidates {
		select {
		case jobs <- item:
		case <-reconcileContext.Done():
			close(jobs)
			wait.Wait()
			return
		}
	}
	close(jobs)
	wait.Wait()
}

func (s *Service) pruneInvalidWatches() {
	s.mu.Lock()
	directories := make(map[string]os.FileInfo, len(s.watched))
	for directory := range s.watched {
		directories[directory] = s.watchIdentity[directory]
	}
	s.mu.Unlock()
	for directory, expected := range directories {
		info, err := os.Lstat(directory)
		if err == nil && expected != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
			os.SameFile(expected, info) {
			continue
		}
		s.mu.Lock()
		for target := range s.watched[directory] {
			s.watchRebuild[target] = true
		}
		delete(s.watched, directory)
		delete(s.watchIdentity, directory)
		s.mu.Unlock()
		if s.watcher != nil {
			_ = s.watcher.Remove(directory)
		}
	}
}
