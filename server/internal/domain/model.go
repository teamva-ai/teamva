package domain

import "time"

type Versioned struct {
	ID        string     `json:"id"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type Project struct {
	Versioned
	ComputerID  string `json:"computer_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsDefault   bool   `json:"is_default"`
}

// DefaultProjectName is deliberately not localized: the built-in project is a
// real, stable project, not a UI placeholder or a name-based convention.
const DefaultProjectName = "默认项目"

func DefaultProjectID(computerID string) string { return "proj_default_" + computerID }

type ProjectResource struct {
	Versioned
	ProjectID string `json:"project_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	Status    string `json:"status"`
	// GitDetected 由 node 探测 code_directory 是否位于 Git 仓库内
	// （git rev-parse --is-inside-work-tree），供 Issue 创建弹窗的 worktree 勾选判定。
	GitDetected bool `json:"git_detected,omitempty"`
}

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`
	DataURL  string `json:"data_url,omitempty"`
	Internal bool   `json:"internal,omitempty"`
}

type Run struct {
	AssistantSessionID string `json:"assistant_session_id,omitempty"`
	Versioned
	// IssueID identifies a collaborative task; graph runs use ConversationNodeID.
	IssueID string `json:"issue_id"`
	// IssueAgentInstanceID / IssueAgentGenerationID / AgentID 标识由哪个 Agent
	// 实例、哪一代配置、哪个 Agent 发起了本次执行（sidesheet 消息流据此归组）。
	IssueAgentInstanceID   string `json:"issue_agent_instance_id,omitempty"`
	IssueAgentGenerationID string `json:"issue_agent_generation_id,omitempty"`
	// ConversationNodeID 标识图对话节点的运行；与 IssueID 互斥。
	ConversationNodeID string         `json:"conversation_node_id,omitempty"`
	AgentID            string         `json:"agent_id,omitempty"`
	ProjectID          string         `json:"project_id"`
	Status             string         `json:"status"`
	AgentServiceID     string         `json:"agent_service_id,omitempty"`
	Model              string         `json:"model,omitempty"`
	ReadOnly           bool           `json:"read_only"`
	LeaseEpoch         int64          `json:"lease_epoch"`
	Input              []ContentBlock `json:"input"`
	IdempotencyKey     string         `json:"-"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	FinishedAt         *time.Time     `json:"finished_at,omitempty"`
	TerminalReason     string         `json:"terminal_reason,omitempty"`
	GoalID             string         `json:"goal_id,omitempty"`
	Purpose            string         `json:"purpose"`
}

type RunEvent struct {
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	Sequence   int64          `json:"seq"`
	Type       string         `json:"event_type"`
	Payload    map[string]any `json:"payload"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type RunEventPage struct {
	Items      []RunEvent `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
	Truncated  bool       `json:"truncated"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

const (
	RunQueued     = "queued"
	RunStarting   = "starting"
	RunRunning    = "running"
	RunWaiting    = "waiting_approval"
	RunCancelling = "cancelling"
	RunSucceeded  = "succeeded"
	RunFailed     = "failed"
	RunCancelled  = "cancelled"
	RunLost       = "lost"
)

func IsRunTerminal(status string) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCancelled, RunLost:
		return true
	default:
		return false
	}
}
