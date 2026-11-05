package gitread

import (
	"context"
	"os"
)

func (repository *repository) duplicateWorktreeHandle() (*os.File, error) {
	return repository.duplicateRepositoryDirectory(&repository.rootIdentity)
}

func (repository *repository) duplicateGitDirectoryHandle() (*os.File, error) {
	return repository.duplicateRepositoryDirectory(&repository.gitIdentity)
}

func (repository *repository) duplicateCommonDirectoryHandle() (*os.File, error) {
	return repository.duplicateRepositoryDirectory(&repository.commonIdentity)
}

func (repository *repository) duplicateRepositoryDirectory(identity *stablePathIdentity) (*os.File, error) {
	repository.handleMu.Lock()
	defer repository.handleMu.Unlock()
	if repository.handlesClosed || identity == nil || identity.handle == nil {
		return nil, errUnsafePath
	}
	file, err := duplicateIdentityFile(identity.handle)
	if err != nil {
		return nil, errUnsafePath
	}
	return file, nil
}

func (repository *repository) secureMetadata(relative string) (securePathMetadata, error) {
	root, err := repository.duplicateWorktreeHandle()
	if err != nil {
		return securePathMetadata{}, err
	}
	defer root.Close()
	return secureWorktreeMetadataFromDirectory(root, relative)
}

func (repository *repository) secureRead(ctx context.Context, relative string, limit int) (secureFile, error) {
	root, err := repository.duplicateWorktreeHandle()
	if err != nil {
		return secureFile{}, err
	}
	defer root.Close()
	return secureReadRegularFromDirectory(ctx, root, relative, limit)
}
