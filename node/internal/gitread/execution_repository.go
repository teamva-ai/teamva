package gitread

import (
	"context"
	"errors"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const maxRepositoryCacheAttempts = 3

// This marker is only for known cache lifecycle races, never arbitrary
// snapshot_expired errors from filesystem identity, Git metadata or approvals.
type repositoryCacheInvalidatedError struct{ error }

func repositoryCacheInvalidated() error {
	return &repositoryCacheInvalidatedError{gitError("snapshot_expired", "Git 仓库刷新屏障已推进，请重试", true)}
}

func (err *repositoryCacheInvalidatedError) Unwrap() error { return err.error }

func isRepositoryCacheInvalidated(err error) bool {
	var invalidated *repositoryCacheInvalidatedError
	return errors.As(err, &invalidated)
}

// executionRepository reopens an independently pinned repository after reading
// its exact cached discovery scope. A concurrent refresh/other scope may retire
// the cache's handles, but cannot close the handles of an active execution read.
// No approval is created or changed; both sides of the reread verify the same
// explicit anchors, and the fresh repository must retain the requested ID.
func (s *Service) executionRepository(ctx context.Context, request nodeprotocol.GitRepositoriesRequest, repositoryID string) (*repository, error) {
	if strings.TrimSpace(repositoryID) == "" {
		return nil, gitError("invalid_argument", "repository_id 不能为空", false)
	}
	if err := s.validateGitExecutable(); err != nil {
		return nil, err
	}
	if err := validateProjectRequest(request.ProjectID, request.Resources); err != nil {
		return nil, err
	}
	resourceHash, err := s.authorizedResourceHash(ctx, request.Resources)
	if err != nil {
		return nil, err
	}
	for range maxRepositoryCacheAttempts {
		_, err := s.listRepositories(ctx, request, resourceHash)
		request.Refresh = false
		if isRepositoryCacheInvalidated(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		currentHash, err := s.authorizedResourceHash(ctx, request.Resources)
		if err != nil {
			return nil, err
		}
		if currentHash != resourceHash {
			return nil, resourceAuthorizationError()
		}
		s.mu.Lock()
		project := s.projects[request.ProjectID]
		if project == nil || project.resourcesHash != resourceHash {
			s.mu.Unlock()
			continue // Cache replacement only; reread the original anchors.
		}
		cached := project.repositories[repositoryID]
		if cached == nil {
			s.mu.Unlock()
			return nil, gitError("repository_not_found", "Git 仓库不在项目授权范围内", false)
		}
		item := &repository{
			id: cached.id, commonID: cached.commonID, root: cached.root,
			authPrefixes:      append([]string(nil), cached.authPrefixes...),
			resourceIDs:       append([]string(nil), cached.resourceIDs...),
			authorizationHash: cached.authorizationHash, summary: cached.summary,
		}
		s.mu.Unlock()
		identity, err := s.confirmRepository(ctx, item.root)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, gitError("repository_not_found", "Git 仓库身份已失效", false)
		}
		if stableRepositoryID(identity) != repositoryID || !samePath(identity.root, item.root) {
			identity.close()
			return nil, gitError("repository_not_found", "Git 仓库身份已变化", false)
		}
		item.gitDir, item.commonDir, item.settings = identity.gitDir, identity.commonDir, identity.settings
		item.rootIdentity, item.gitIdentity = identity.rootIdentity, identity.gitIdentity
		item.commonIdentity, item.objectIdentity = identity.commonIdentity, identity.objectIdentity
		item.markerIdentity, item.commonMarker = identity.markerIdentity, identity.commonMarker
		currentHash, err = s.authorizedResourceHash(ctx, request.Resources)
		if err != nil || currentHash != resourceHash {
			item.closeIdentity()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, resourceAuthorizationError()
		}
		if err := item.validateIdentity(); err != nil {
			item.closeIdentity()
			return nil, gitError("snapshot_expired", "Git 仓库身份在读取期间发生变化", true)
		}
		if ctx.Err() != nil {
			item.closeIdentity()
			return nil, ctx.Err()
		}
		if s.ctx.Err() != nil {
			item.closeIdentity()
			return nil, gitError("runtime_closed", "Git 读取服务已关闭", false)
		}
		return item, nil
	}
	return nil, repositoryCacheInvalidated()
}

func (s *Service) validateExecutionAuthorization(ctx context.Context, request nodeprotocol.GitRepositoriesRequest, item *repository) error {
	currentHash, err := s.authorizedResourceHash(ctx, request.Resources)
	if err != nil {
		return err
	}
	if s.resourceAuthorizationScopeHash(currentHash) != item.authorizationHash {
		return resourceAuthorizationError()
	}
	if err := item.validateIdentity(); err != nil {
		return gitError("snapshot_expired", "Git 仓库身份在读取期间发生变化", true)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.ctx.Err() != nil {
		return gitError("runtime_closed", "Git 读取服务已关闭", false)
	}
	return nil
}
