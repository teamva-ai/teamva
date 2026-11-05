//go:build !darwin

package opencode

import (
	"context"
	"errors"
	"os/exec"

	"github.com/teamva-ai/teamva/node/provider"
)

func platformSandboxAvailable() error {
	return errors.New("OpenCode OS write sandbox is currently supported only on macOS; Run refused")
}

func platformSandboxCommand(string, string, ...string) (*exec.Cmd, error) {
	return nil, platformSandboxAvailable()
}

func platformSandboxToolEnvironment(context.Context, string) (map[string]string, error) {
	return nil, platformSandboxAvailable()
}

func platformValidateSandbox(context.Context, *writeSandbox) error {
	return platformSandboxAvailable()
}

func platformWriteProfile(context.Context, *Client, provider.RunRequest, string, string) (string, error) {
	return "", platformSandboxAvailable()
}
