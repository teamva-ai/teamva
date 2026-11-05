package provider

import (
	"context"
	"errors"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

var ErrUnsupported = errors.New("provider capability unsupported")

// UnsupportedOperations may be embedded by a minimal CLI provider. Start and
// Probe remain mandatory; unimplemented features never silently succeed.
type UnsupportedOperations struct{}

func (UnsupportedOperations) Resume(context.Context, RunRequest, Sink) (NativeSession, error) {
	return NativeSession{}, ErrUnsupported
}
func (UnsupportedOperations) Steer(context.Context, string, string, []nodeprotocol.Attachment) error {
	return ErrUnsupported
}
func (UnsupportedOperations) Interrupt(context.Context, string) error { return ErrUnsupported }
func (UnsupportedOperations) DecideApproval(context.Context, ApprovalDecision) error {
	return ErrUnsupported
}
func (UnsupportedOperations) VerifySession(context.Context, string) (bool, error) {
	return false, ErrUnsupported
}
func (UnsupportedOperations) Close() error { return nil }
