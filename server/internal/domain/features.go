package domain

import "time"

type ProjectMemoryEntry struct {
	Versioned
	ProjectID  string `json:"project_id"`
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref,omitempty"`
}

type ProjectMemoryVersion struct {
	ID        string    `json:"id"`
	MemoryID  string    `json:"memory_id"`
	Version   int64     `json:"version"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	ActorType string    `json:"actor_type"`
	ActorRef  string    `json:"actor_ref,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Approval struct {
	Versioned
	ProjectID           string         `json:"project_id"`
	IssueID             string         `json:"issue_id"`
	RunID               string         `json:"run_id"`
	Status              string         `json:"status"`
	ActionKind          string         `json:"action_kind"`
	ActionDigest        string         `json:"action_digest"`
	NormalizedAction    map[string]any `json:"normalized_action"`
	Risk                string         `json:"risk"`
	ExpiresAt           time.Time      `json:"expires_at"`
	DecisionScope       string         `json:"decision_scope,omitempty"`
	DecidedAt           *time.Time     `json:"decided_at,omitempty"`
	ProviderApprovalRef string         `json:"provider_approval_ref,omitempty"`
}

type PolicyRule struct {
	Versioned
	ProjectID  string         `json:"project_id"`
	ActionKind string         `json:"action_kind"`
	Effect     string         `json:"effect"`
	Conditions map[string]any `json:"conditions"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Enabled    bool           `json:"enabled"`
}

type ScheduledTask struct {
	Versioned
	ProjectID          string         `json:"project_id"`
	Name               string         `json:"name"`
	TitleEdited        bool           `json:"-"` // Explicit name intent, including same-value edits.
	Enabled            bool           `json:"enabled"`
	ScheduleKind       string         `json:"schedule_kind"`
	ScheduleExpression string         `json:"schedule_expression"`
	Times              []string       `json:"times,omitempty"`
	Weekdays           []int          `json:"weekdays,omitempty"`
	IssueMode          string         `json:"issue_mode,omitempty"`
	IssueID            string         `json:"issue_id,omitempty"`
	Timezone           string         `json:"timezone"`
	Prompt             []ContentBlock `json:"prompt"`
	// AgentID 取代旧 agent_service_id + model：计划任务同样走 Agent 体系，
	// 固定模型、提示词与 Skills 均由 Agent 配置决定。
	AgentID           string     `json:"agent_id,omitempty"`
	ConcurrencyPolicy string     `json:"concurrency_policy"`
	MisfirePolicy     string     `json:"misfire_policy"`
	NextRunAt         *time.Time `json:"next_run_at,omitempty"`
	DispatchAttempts  int        `json:"dispatch_attempts"`
	DispatchRetryAt   *time.Time `json:"dispatch_retry_at,omitempty"`
	LastDispatchError string     `json:"last_dispatch_error,omitempty"`
}

type ScheduledTaskRun struct {
	ID               string     `json:"id"`
	ScheduledTaskID  string     `json:"scheduled_task_id"`
	RunID            string     `json:"run_id,omitempty"`
	IssueID          string     `json:"issue_id,omitempty"`
	TriggerKey       string     `json:"trigger_key"`
	ScheduledFor     time.Time  `json:"scheduled_for"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	DispatchAttempts int        `json:"dispatch_attempts"`
	RetryAt          *time.Time `json:"retry_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}
