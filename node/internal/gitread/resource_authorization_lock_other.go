//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package gitread

import (
	"context"
	"errors"
	"os"
)

func openResourceApprovalLock(*os.File, string) (*os.File, error) {
	return nil, errors.New("durable resource approvals are unsupported on this platform")
}

func lockResourceApproval(context.Context, *os.File) error {
	return errors.New("durable resource approvals are unsupported on this platform")
}

func unlockResourceApproval(*os.File) {}
