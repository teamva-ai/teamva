//go:build !darwin && !linux

package gitread

import (
	"context"
	"errors"
	"os"
)

func platformGitSandboxSupported() bool { return false }

func trustedGitExecutablePaths() []string { return nil }

func platformTrustedGitExecutable(os.FileInfo) bool { return false }

func duplicateIdentityFile(*os.File) (*os.File, error) {
	return nil, errors.New("safe Git sandbox is unavailable on this platform")
}

func openSandboxDirectoryAt(*os.File, string) (*os.File, error) {
	return nil, errors.New("safe Git sandbox is unavailable on this platform")
}

func openSandboxRegularAt(*os.File, string) (*os.File, os.FileInfo, error) {
	return nil, nil, errors.New("safe Git sandbox is unavailable on this platform")
}

func sandboxEntryModeAt(*os.File, string) (uint32, error) {
	return 0, errors.New("safe Git sandbox is unavailable on this platform")
}

func sandboxModeDirectory() uint32 { return 0 }
func sandboxModeRegular() uint32   { return 0 }

type sandboxBoundDirectories struct {
	workTree string
	objects  string
}

func bindSandboxHandles(context.Context, []*os.File, string, *sandboxBudget) (sandboxBoundDirectories, error) {
	return sandboxBoundDirectories{}, errors.New("safe Git sandbox is unavailable on this platform")
}
