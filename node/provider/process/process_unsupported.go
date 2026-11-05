//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package providerprocess

import (
	"errors"
	"os/exec"
)

func configureCommand(*exec.Cmd) (func() error, func() error, func(), error) {
	return nil, nil, nil, errors.New("Provider process-tree isolation is unavailable on this platform")
}
