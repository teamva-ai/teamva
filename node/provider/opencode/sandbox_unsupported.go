//go:build !darwin

package opencode

import (
	"context"
	"os/exec"

	"github.com/teamva-ai/teamva/node/provider"
)

// Teamva does not wrap the CLI in an OS write sandbox on these platforms.
// Runtime config is still prepared for IPC/temp isolation, but process
// permissions are left to OpenCode and the operating system.
func platformSandboxAvailable() error { return nil }

func platformSandboxCommand(_ string, executable string, args ...string) (*exec.Cmd, error) {
	return exec.Command(executable, args...), nil
}

func platformSandboxToolEnvironment(context.Context, string) (map[string]string, error) {
	return nil, nil
}

func platformValidateSandbox(context.Context, *writeSandbox) error { return nil }

func platformWriteProfile(context.Context, *Client, provider.RunRequest, string, string) (string, error) {
	return "", nil
}
