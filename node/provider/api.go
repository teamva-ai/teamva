// Package provider is the public, provider-neutral Coding Agent integration API.
package provider

import (
	"context"
	"encoding/json"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type CapabilityManifest struct {
	Provider              string   `json:"provider"`
	ProviderVersion       string   `json:"provider_version"`
	Available             bool     `json:"available"`
	Authenticated         bool     `json:"authenticated"`
	Streaming             bool     `json:"streaming"`
	Resume                bool     `json:"resume"`
	Interrupt             bool     `json:"interrupt"`
	Approval              bool     `json:"approval"`
	Models                []string `json:"models,omitempty"`
	MaxParallelRuns       int      `json:"max_parallel_runs"`
	CapabilityVersion     int      `json:"capability_version"`
	UnavailableReasonCode string   `json:"unavailable_reason_code,omitempty"`
}

type RunRequest struct {
	RunID           string                    `json:"run_id"`
	ConversationID  string                    `json:"conversation_id,omitempty"`
	IssueID         string                    `json:"issue_id,omitempty"`
	CWD             string                    `json:"cwd"`
	Prompt          string                    `json:"prompt"`
	Attachments     []nodeprotocol.Attachment `json:"attachments,omitempty"`
	Model           string                    `json:"model,omitempty"`
	NativeSessionID string                    `json:"native_session_id,omitempty"`
	ReadOnly        bool                      `json:"read_only"`
	// DisableTools forbids ALL native/custom tools, including read-only tools.
	// It is a host capability boundary, not an instruction the model may relax.
	DisableTools bool `json:"disable_tools,omitempty"`
	// Host-created IPC directory, not a user/project writable repository.
	ToolRequestDir string `json:"-"`
	// Per-run MCP endpoint. When set, the provider should use it instead of
	// the file bridge for Issue collaboration tools.
	MCPURL   string `json:"-"`
	MCPToken string `json:"-"`
}

type NativeSession struct {
	ID       string `json:"id"`
	TurnID   string `json:"turn_id,omitempty"`
	Provider string `json:"provider"`
}

type ApprovalDecision struct {
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Scope     string `json:"scope,omitempty"`
}

type Event struct {
	ID                string          `json:"id,omitempty"`
	RunID             string          `json:"run_id"`
	LeaseEpoch        int64           `json:"lease_epoch,omitempty"`
	Sequence          int64           `json:"sequence"`
	Type              string          `json:"type"`
	Payload           map[string]any  `json:"payload"`
	ProviderEventType string          `json:"provider_event_type,omitempty"`
	ProviderRaw       json.RawMessage `json:"provider_raw,omitempty"`
	OccurredAt        time.Time       `json:"occurred_at"`
}

type Sink func(context.Context, Event) error

type Adapter interface {
	Name() string
	Probe(context.Context) (CapabilityManifest, error)
	Start(context.Context, RunRequest, Sink) (NativeSession, error)
	Resume(context.Context, RunRequest, Sink) (NativeSession, error)
	Steer(context.Context, string, string, []nodeprotocol.Attachment) error
	Interrupt(context.Context, string) error
	DecideApproval(context.Context, ApprovalDecision) error
	VerifySession(context.Context, string) (bool, error)
	Close() error
}

// DiagnosticAdapter exposes the same real checks used by Probe while keeping
// the individual outcomes for an interactive troubleshooting view. It is
// optional so third-party adapters can continue implementing Adapter first.
type DiagnosticAdapter interface {
	Diagnose(context.Context) (CapabilityManifest, []nodeprotocol.DiagnosticStep, error)
}
