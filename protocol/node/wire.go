package nodeprotocol

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	Version = 6
	// Budgets apply to embedded command/result and event-batch JSON, not a
	// removed WebSocket transport envelope.
	MaxCommandResultBytes  = (16 << 20) - (64 << 10)
	MaxRunEventBatchBytes  = (16 << 20) - (64 << 10)
	MaxCommandPayloadBytes = (16 << 20) - (128 << 10)
	// MaxRunEventBytes is the hard encoded size of one RunEvent. A batch still
	// has to fit MaxRunEventBatchBytes, so this limit cannot be used to multiply the
	// transport budget by MaxRunEventsPerBatch.
	MaxRunEventBytes = 8 << 20
	// MaxRunEventPayloadBytes leaves deterministic room for the RunEvent
	// identity, lease, sequence, type and timestamp fields.
	MaxRunEventPayloadBytes = MaxRunEventBytes - (64 << 10)
	// MaxRunEventsPerBatch bounds one durable upload transaction and prevents a
	// reconnecting Node from monopolizing the control plane with an unbounded
	// frame.
	MaxRunEventsPerBatch = 128
)

func EncodeCommandResult(result CommandResult) ([]byte, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxCommandResultBytes {
		return nil, fmt.Errorf("command result exceeds %d bytes", MaxCommandResultBytes)
	}
	return encoded, nil
}

type AgentManifest struct {
	Provider          string         `json:"provider"`
	ProviderVersion   string         `json:"provider_version,omitempty"`
	Available         bool           `json:"available"`
	Authenticated     bool           `json:"authenticated"`
	Capabilities      map[string]any `json:"capabilities,omitempty"`
	CapabilityVersion int            `json:"capability_version"`
}

type CommandResult struct {
	CommandID string          `json:"command_id"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
}

type CommandEnvelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	MessageID       string          `json:"message_id"`
	Name            string          `json:"name"`
	ComputerID      string          `json:"computer_id"`
	SentAt          time.Time       `json:"sent_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	IdempotencyKey  string          `json:"idempotency_key"`
	TraceContext    map[string]any  `json:"trace_context"`
	Payload         json.RawMessage `json:"payload"`
}

type RunEvent struct {
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	LeaseEpoch int64          `json:"lease_epoch"`
	Sequence   int64          `json:"sequence"`
	Type       string         `json:"type"`
	Payload    map[string]any `json:"payload"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type RunEventBatch struct {
	Events []RunEvent `json:"events"`
}

// RunEventCursor acknowledges every local event through Sequence for one run
// lease. A Node only advances its durable SQLite outbox after receiving these
// server-produced cursors.
type RunEventCursor struct {
	RunID      string `json:"run_id"`
	LeaseEpoch int64  `json:"lease_epoch"`
	Sequence   int64  `json:"sequence"`
}

type StartRunPayload struct {
	RunID                  string               `json:"run_id"`
	IssueID                string               `json:"issue_id,omitempty"`
	ProjectID              string               `json:"project_id"`
	GitResources           []GitResourceRequest `json:"git_resources,omitempty"`
	AgentID                string               `json:"agent_id,omitempty"`
	IssueAgentInstanceID   string               `json:"issue_agent_instance_id,omitempty"`
	IssueAgentGenerationID string               `json:"issue_agent_generation_id,omitempty"`
	ConversationNodeID     string               `json:"conversation_node_id,omitempty"`
	AssistantSessionID     string               `json:"assistant_session_id,omitempty"`
	LeaseEpoch             int64                `json:"lease_epoch"`
	Provider               string               `json:"provider"`
	Prompt                 string               `json:"prompt"`
	Attachments            []Attachment         `json:"attachments,omitempty"`
	Model                  string               `json:"model,omitempty"`
	SystemPrompt           string               `json:"system_prompt,omitempty"`
	// Skills 是绑定的 Skill Package 内容（由 server 在 run.start 时下发），
	// node 安装到该实例的私有 runtime 配置目录。
	Skills          []SkillPayload `json:"skills,omitempty"`
	WorktreeHint    bool           `json:"worktree_hint,omitempty"`
	NativeSessionID string         `json:"native_session_id,omitempty"`
	ReadOnly        bool           `json:"read_only"`
	// ComputerUse 为该 Run 的 Provider 实例注入 open-computer-use 官方
	// MCP（Accessibility 元素树 + 截图），由每次 Run 的实例配置携带。
	ComputerUse bool `json:"computer_use,omitempty"`
	// DisableTools creates an isolated evaluation run with no Issue bridge.
	// It is used by the goal auditor so model output cannot mutate task state.
	DisableTools bool `json:"disable_tools,omitempty"`
}

// SteerRunPayload appends a user instruction to the provider's currently
// active turn. The run/lease precondition prevents a delayed command from
// steering a later execution that happens to reuse the same Agent session.
type SteerRunPayload struct {
	RunID       string       `json:"run_id"`
	LeaseEpoch  int64        `json:"lease_epoch"`
	Provider    string       `json:"provider"`
	Prompt      string       `json:"prompt"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a bounded user-provided file transported inline to the local
// Provider. The current desktop UI accepts image inputs only; data_url must be
// a base64 image data URL. It is never written to the project workspace and is
// removed from durable command rows after terminal delivery.
type Attachment struct {
	MimeType string `json:"mime_type"`
	Filename string `json:"filename"`
	DataURL  string `json:"data_url"`
}

// SkillPayload 描述一个要安装到 Agent runtime 的 Skill 包。
type SkillPayload struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	// ContentDigest pins the files that were installed on the target computer.
	// The runtime verifies it before exposing the Skill to a provider instance.
	ContentDigest string `json:"content_digest,omitempty"`
}

// NodeRequest 是 node→server 的 RPC 请求（ToolBroker 调用）。
type NodeRequest struct {
	RequestID string `json:"request_id"`
	// Tool 名称：issue_get | issue_comment_create | issue_comment_update_own |
	// issue_agents_list | issue_agent_status | issue_agent_context
	Tool    string         `json:"tool"`
	Payload map[string]any `json:"payload,omitempty"`
	// 工具作用域由 server 依据当前 Run 自动绑定，node 侧透传。
	RunID      string `json:"run_id"`
	IssueID    string `json:"issue_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	// DeadlineUnix 用于 server 侧限时；IdempotencyKey 用于幂等（防重试重复评论）。
	DeadlineUnix   int64  `json:"deadline_unix,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// NodeRequestResult 是 server 对 NodeRequest 的响应。
type NodeRequestResult struct {
	RequestID string         `json:"request_id"`
	OK        bool           `json:"ok"`
	Data      map[string]any `json:"data,omitempty"`
	ErrorCode string         `json:"error_code,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// ListDirectoryPayload asks the Node to list a directory on the local machine.
// Path empty means the user's home directory; Query filters by name prefix.
type ListDirectoryPayload struct {
	Path       string `json:"path,omitempty"`
	Query      string `json:"query,omitempty"`
	ShowHidden bool   `json:"show_hidden,omitempty"`
}

// ListDirectoryItem is one entry in the directory listing result.
type ListDirectoryItem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// RegisterResourcePayload asks the Node to validate and canonicalize a local
// directory path for a project.
type RegisterResourcePayload struct {
	Path string `json:"path"`
}

type StopRunPayload struct {
	RunID      string `json:"run_id"`
	LeaseEpoch int64  `json:"lease_epoch"`
	Provider   string `json:"provider"`
	Mode       string `json:"mode,omitempty"`
}

// StopRunResult acknowledges the control request, not necessarily Run completion.
// TerminalPending leaves settlement to the owning run.start/event sink (or its
// failed command); it must not make the server synthesize an early terminal.
type StopRunResult struct {
	Interrupted     bool `json:"interrupted"`
	TerminalPending bool `json:"terminal_pending,omitempty"`
}

type ApprovalDecisionPayload struct {
	RunID     string `json:"run_id"`
	Provider  string `json:"provider"`
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Scope     string `json:"scope,omitempty"`
}

type ProbePayload struct {
	ServiceID string `json:"service_id,omitempty"`
	Provider  string `json:"provider"`
}

// DiagnoseProviderPayload requests an on-demand, provider-specific diagnostic.
// Unlike the background probe, diagnostics retain every user-facing check so
// the desktop UI can explain why an installed tool was not detected.
type DiagnoseProviderPayload struct {
	Provider string `json:"provider"`
}

type DiagnosticStep struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type ProviderDiagnostic struct {
	Provider              string           `json:"provider"`
	ProviderVersion       string           `json:"provider_version,omitempty"`
	Available             bool             `json:"available"`
	Authenticated         bool             `json:"authenticated"`
	Status                string           `json:"status"`
	UnavailableReasonCode string           `json:"unavailable_reason_code,omitempty"`
	Capabilities          map[string]any   `json:"capabilities,omitempty"`
	CapabilityVersion     int              `json:"capability_version"`
	Models                []string         `json:"models,omitempty"`
	StartedAt             time.Time        `json:"started_at"`
	FinishedAt            time.Time        `json:"finished_at"`
	Steps                 []DiagnosticStep `json:"steps"`
}

type ValidateResourcePayload struct {
	ResourceID        string `json:"resource_id"`
	Path              string `json:"path"`
	ValidationVersion int64  `json:"validation_version"`
}

// The resource/version echo fences a result to its durable validation request.
// GitDetected is meaningful only when Valid is true.
type ValidateResourceResult struct {
	ResourceID        string `json:"resource_id"`
	ValidationVersion int64  `json:"validation_version"`
	Valid             bool   `json:"valid"`
	GitDetected       bool   `json:"git_detected"`
}

type OpenResourcePayload struct {
	Path     string `json:"path"`
	OpenerID string `json:"opener_id"`
}

type OpenSkillPayload struct {
	Slug string `json:"slug"`
}

type InstallSkillPayload struct {
	Slug         string   `json:"slug"`
	SourceURL    string   `json:"source_url"`
	Revision     string   `json:"revision,omitempty"`
	IncludePaths []string `json:"include_paths,omitempty"`
}

type InstallSkillResult struct {
	Slug            string         `json:"slug"`
	Path            string         `json:"path"`
	CommitSHA       string         `json:"commit_sha"`
	ContentDigest   string         `json:"content_digest"`
	Manifest        map[string]any `json:"manifest"`
	FileCount       int            `json:"file_count"`
	SizeBytes       int64          `json:"size_bytes"`
	ExecutableFiles []string       `json:"executable_files,omitempty"`
}

type UninstallSkillPayload struct {
	Slug string `json:"slug"`
}

type ResourceOpener struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type ResourceOpeners struct {
	Platform string           `json:"platform"`
	Items    []ResourceOpener `json:"items"`
}
