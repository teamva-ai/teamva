package domain

import (
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

// --- Agent 配置体系 ---

// Agent 是用户在「本机」配置的可复用执行体：引用本机探测出的 AgentService
// （确定软件 opencode/codex）+ 固定模型 + 提示词 + 绑定的 Skill Package。
// Agent 本身不运行；它进入某个 Issue 后才产生 IssueAgentInstance。
type Agent struct {
	Versioned
	ComputerID        string `json:"computer_id"`
	AgentServiceID    string `json:"agent_service_id"`
	SystemRole        string `json:"system_role,omitempty"`
	Model             string `json:"model,omitempty"`
	Name              string `json:"name"`
	Note              string `json:"note,omitempty"`
	Status            string `json:"status"` // active | disabled
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
}

// AgentRevision 是不可变配置快照。编辑 Agent（模型/提示词/Skills）会创建新
// revision；运行中的 IssueAgentInstance 下次唤醒时自动切换到最新 revision。
type AgentRevision struct {
	Versioned
	AgentID        string `json:"agent_id"`
	AgentServiceID string `json:"agent_service_id"`
	Model          string `json:"model,omitempty"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
}

// AgentRevisionSkillPackage 绑定一个不可变的 Skill Package 版本到 Agent 修订。
type AgentRevisionSkillPackage struct {
	ID                    string `json:"id"`
	AgentRevisionID       string `json:"agent_revision_id"`
	SkillPackageVersionID string `json:"skill_package_version_id"`
	Position              int    `json:"position"`
}

// SkillPackage 是商店条目（一个 GitHub 仓库一个条目，含该仓库全部 SKILL.md）。
type SkillPackage struct {
	Versioned
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
	License     string `json:"license,omitempty"`
	Builtin     bool   `json:"builtin"`
}

// SkillPackageVersion 是不可变制品：固定 commit + 规范内容 digest + manifest。
// node 下载固定制品、校验 digest 后安装到实例私有 runtime 目录。
type SkillPackageVersion struct {
	Versioned
	SkillPackageID  string         `json:"skill_package_id"`
	CommitSHA       string         `json:"commit_sha"`
	ContentDigest   string         `json:"content_digest"`
	Manifest        map[string]any `json:"manifest"`
	FileCount       int            `json:"file_count"`
	SizeBytes       int64          `json:"size_bytes"`
	ExecutableFiles []string       `json:"executable_files,omitempty"`
	ProviderInstall map[string]any `json:"provider_install,omitempty"`
}

// ComputerSkillPackage 表示某个不可变 Skill 版本已经安装到一台计算机。
// Agent 只能绑定其所属计算机安装清单中的版本。
type ComputerSkillPackage struct {
	Versioned
	ComputerID            string `json:"computer_id"`
	SkillPackageID        string `json:"skill_package_id"`
	SkillPackageVersionID string `json:"skill_package_version_id"`
}

const (
	AgentActive   = "active"
	AgentDisabled = "disabled"
)

// --- Issue（任务）体系 ---

// Issue 即任务/工单：标题 + 描述 + 评论流。没有自动状态机，状态仅用户手动编辑。
type Issue struct {
	Versioned
	ProjectID       string `json:"project_id"`
	Title           string `json:"title"`
	TitleEdited     bool   `json:"-"` // Explicit title intent, including same-value edits.
	Description     string `json:"description,omitempty"`
	Status          string `json:"status"` // open | resolved | closed
	WorktreeHint    bool   `json:"worktree_hint"`
	CreatedByUserID string `json:"created_by_user_id"`
}

// IssueComment 是 Issue 下的评论，作者可以是用户或 Agent。
type IssueComment struct {
	Versioned
	IssueID       string          `json:"issue_id"`
	AuthorKind    string          `json:"author_kind"` // user | agent
	AuthorUserID  string          `json:"author_user_id,omitempty"`
	AuthorAgentID string          `json:"author_agent_id,omitempty"`
	Body          string          `json:"body"`
	RunID         string          `json:"run_id,omitempty"`
	Status        string          `json:"status"` // working | published | failed
	EditedAt      *time.Time      `json:"edited_at,omitempty"`
	WorkItems     []IssueWorkItem `json:"work_items,omitempty"`
}

// IssueWorkItem keeps the confirmed instruction immutable; execution fields are
// projected from its exact mention and Run, never another session of the Agent.
type IssueWorkItem struct {
	nodeprotocol.WorkPlanItem
	MentionID            string   `json:"mention_id,omitempty"`
	RunID                string   `json:"run_id,omitempty"`
	IssueAgentInstanceID string   `json:"issue_agent_instance_id,omitempty"`
	ResultCommentID      string   `json:"result_comment_id,omitempty"`
	Status               string   `json:"status"`
	StatusReason         string   `json:"status_reason,omitempty"`
	BlockedBy            []string `json:"blocked_by,omitempty"`
}

const (
	IssueCommentWorking   = "working"
	IssueCommentPublished = "published"
	IssueCommentFailed    = "failed"
)

// IssueCommentMention 是评论中的结构化 @。target 为 agent 时 TargetAgentID 非空。
// 每条 mention 只消费一次（ConsumedAt 非空），防止网络重试重复激活。
type IssueCommentMention struct {
	ID            string     `json:"id"`
	CommentID     string     `json:"comment_id"`
	TargetKind    string     `json:"target_kind"` // user | agent
	TargetAgentID string     `json:"target_agent_id,omitempty"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
}

// IssueCommentRevision 是评论修订历史（修正评论按钮触发）。
type IssueCommentRevision struct {
	ID        string    `json:"id"`
	CommentID string    `json:"comment_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// IssueAgentInstance 是一次独立的 Agent 会话。每次评论 @Agent 都创建新实例；
// 同一会话内的追问继续复用该实例。状态：idle | running | paused | queued |
// resource_exhausted。
type IssueAgentInstance struct {
	Versioned
	IssueID             string     `json:"issue_id"`
	AgentID             string     `json:"agent_id"`
	AgentRevisionID     string     `json:"agent_revision_id"`
	Status              string     `json:"status"`
	LastActivityAt      *time.Time `json:"last_activity_at,omitempty"`
	PendingMentionCount int        `json:"pending_mention_count"`
}

// IssueAgentGeneration 是实例的一次配置世代：Agent 配置更新后下次唤醒自动换代。
// node 本地 native session 绑定 generation_id，防止新旧配置复用 provider session。
type IssueAgentGeneration struct {
	Versioned
	IssueAgentInstanceID string `json:"issue_agent_instance_id"`
	AgentRevisionID      string `json:"agent_revision_id"`
	NativeSessionKey     string `json:"native_session_key,omitempty"`
}

const (
	IssueOpen     = "open"
	IssueResolved = "resolved"
	IssueClosed   = "closed"

	IssueCommentAuthorUser  = "user"
	IssueCommentAuthorAgent = "agent"

	IssueMentionTargetUser  = "user"
	IssueMentionTargetAgent = "agent"

	AgentInstanceIdle              = "idle"
	AgentInstanceRunning           = "running"
	AgentInstancePaused            = "paused"
	AgentInstanceQueued            = "queued"
	AgentInstanceResourceExhausted = "resource_exhausted"
)
