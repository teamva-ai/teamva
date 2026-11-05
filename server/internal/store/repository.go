package store

import (
	"context"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Interfaces are grouped by their business consumers. SQLite is the sole
// persistent store; browser and Wails share the same local contracts.
//
// Tenant isolation contract: every user-scoped method accepts an explicit
// actorUserID after ctx, scopes queries through user ownership, and returns a
// uniform not-found error for missing and cross-tenant resources.
type Health interface {
	Health(context.Context) error
}

type IdentitySession interface {
	CreateLocalSession(context.Context, string, time.Duration, domain.SessionMeta) (domain.User, domain.Session, error)
	GetUserPreferences(context.Context, string) (domain.UserPreferences, error)
	UpdateUserPreferences(context.Context, string, int64, string, string, bool, bool, *domain.Appearance) (domain.UserPreferences, error)
	HMACValue(string) string
	AuthenticateSession(context.Context, string, time.Duration, time.Duration, time.Duration) (domain.SessionAuthentication, error)
	RevokeSession(context.Context, string) error
	ListSessions(context.Context, string, string) ([]domain.WebSessionInfo, error)
	RevokeSessionByID(context.Context, string, string) error
	RevokeOtherSessions(context.Context, string, string) error
	VerifySessionCSRF(context.Context, string, string) bool
	GetSessionCSRF(context.Context, string) (string, error)
	GetUser(context.Context, string) (domain.User, error)
}

type ComputerService interface {
	HeartbeatComputer(context.Context, string, domain.NodeHeartbeat) (domain.Computer, int64, error)
	ListComputers(context.Context, string) ([]domain.Computer, error)
	GetComputer(context.Context, string, string) (domain.Computer, error)
	UpdateComputer(context.Context, string, string, int64, func(*domain.Computer)) (domain.Computer, error)
	RevokeComputer(context.Context, string, string) (domain.Computer, error)
	ListAgentServices(context.Context, string, string) ([]domain.AgentService, error)
	UpsertAgentService(context.Context, string, domain.AgentService) (domain.AgentService, error)
	RequestAgentProbe(context.Context, string, string) (domain.AgentService, error)
	CreateDirectoryListCommand(context.Context, string, string, string, string, bool) (string, error)
	GetDirectoryListResult(context.Context, string, string, string) (string, []byte, error)
	CreateResourceRegisterCommand(context.Context, string, string, string) (string, error)
}

type ProjectResource interface {
	ListProjects(context.Context, string, string) ([]domain.Project, error)
	CreateProject(context.Context, string, domain.Project) (domain.Project, error)
	GetProject(context.Context, string, string) (domain.Project, error)
	UpdateProject(context.Context, string, string, int64, func(*domain.Project)) (domain.Project, error)
	DeleteProject(context.Context, string, string, int64) error
	ListResources(context.Context, string, string) ([]domain.ProjectResource, error)
	CreateResource(context.Context, string, domain.ProjectResource) (domain.ProjectResource, error)
	UpdateResource(context.Context, string, string, int64, func(*domain.ProjectResource)) (domain.ProjectResource, error)
	DeleteResource(context.Context, string, string, int64) error
	RequestResourceValidation(context.Context, string, string, int64) (domain.ProjectResource, error)
	SetResourceGitDetected(context.Context, string, string, bool) error
}

type ProjectSearch interface {
	SearchProjectContent(context.Context, string, string, string, int) (domain.ProjectSearchPage, error)
}

type AgentSkill interface {
	CreateConfiguredAgent(context.Context, string, domain.Agent, string, []string) (domain.Agent, error)
	UpdateConfiguredAgent(context.Context, string, string, int64, AgentConfigurationPatch) (domain.Agent, error)
	ListAgents(context.Context, string, string) ([]domain.Agent, error)
	GetAgent(context.Context, string, string) (domain.Agent, error)
	CreateAgent(context.Context, string, domain.Agent) (domain.Agent, error)
	UpdateAgent(context.Context, string, string, int64, func(*domain.Agent)) (domain.Agent, error)
	DeleteAgent(context.Context, string, string, int64) error
	CreateAgentRevision(context.Context, string, domain.AgentRevision, []string) (domain.AgentRevision, error)
	GetAgentRevision(context.Context, string, string) (domain.AgentRevision, error)
	ListAgentRevisionSkillPackages(context.Context, string, string) ([]domain.AgentRevisionSkillPackage, error)
	ListSkillPackages(context.Context, string) ([]domain.SkillPackage, error)
	GetSkillPackage(context.Context, string, string) (domain.SkillPackage, error)
	UpsertSkillPackage(context.Context, domain.SkillPackage) (domain.SkillPackage, error)
	GetSkillPackageVersion(context.Context, string, string) (domain.SkillPackageVersion, error)
	ListComputerSkillPackages(context.Context, string, string) ([]domain.ComputerSkillPackage, error)
	InstallComputerSkillPackageVersion(context.Context, string, string, string, domain.SkillPackageVersion) (domain.ComputerSkillPackage, error)
	UninstallComputerSkillPackage(context.Context, string, string, string, int64) error
	ValidateComputerSkillPackages(context.Context, string, string, []string) error
}

type IssueComment interface {
	ListIssues(context.Context, string, string) ([]domain.Issue, error)
	ListIssuesPage(context.Context, string, string, IssueListQuery) (IssuePage, error)
	GetIssue(context.Context, string, string) (domain.Issue, error)
	CreateIssue(context.Context, string, domain.Issue) (domain.Issue, error)
	UpdateIssue(context.Context, string, string, int64, func(*domain.Issue)) (domain.Issue, error)
	DeleteIssue(context.Context, string, string, int64) error
	ListIssueComments(context.Context, string, string) ([]domain.IssueComment, error)
	ListIssuePromptComments(context.Context, string, string) ([]domain.IssueComment, bool, error)
	FindInstanceSummaryComment(context.Context, string, string) (domain.IssueComment, bool, error)
	CreateIssueComment(context.Context, string, domain.IssueComment, []string) (domain.IssueComment, error)
	CreateIssueWorkComment(context.Context, string, domain.IssueComment, []nodeprotocol.WorkPlanItem, string) (domain.IssueComment, error)
	WriteRunIssueComment(context.Context, string, string, string, RunIssueCommentWrite) (RunIssueCommentResult, error)
	AddIssueCommentMentions(context.Context, string, string, string, []string) error
	UpdateIssueComment(context.Context, string, string, int64, func(*domain.IssueComment) error) (domain.IssueComment, error)
	DeleteIssueComment(context.Context, string, string, int64) error
	GetIssueComment(context.Context, string, string) (domain.IssueComment, error)
	ListIssueCommentMentions(context.Context, string, string) ([]domain.IssueCommentMention, error)
	ConsumeIssueCommentMention(context.Context, string, string, string) error
	ListIssueCommentRevisions(context.Context, string, string) ([]domain.IssueCommentRevision, error)
}

type RunEvent interface {
	ListIssueAgentInstances(context.Context, string, string) ([]domain.IssueAgentInstance, error)
	GetIssueAgentInstance(context.Context, string, string, string) (domain.IssueAgentInstance, error)
	GetIssueAgentInstanceByID(context.Context, string, string) (domain.IssueAgentInstance, error)
	EnsureIssueAgentInstance(context.Context, string, string, string) (domain.IssueAgentInstance, bool, error)
	UpdateIssueAgentInstance(context.Context, string, string, int64, func(*domain.IssueAgentInstance)) (domain.IssueAgentInstance, error)
	CreateIssueAgentGeneration(context.Context, string, domain.IssueAgentGeneration) (domain.IssueAgentGeneration, error)
	GetIssueAgentGeneration(context.Context, string, string) (domain.IssueAgentGeneration, error)
	ListRunEvents(context.Context, string, string) ([]domain.RunEvent, error)
	ListRunEventsAfter(context.Context, string, string, int64, int) ([]domain.RunEvent, error)
	ListRunEventPage(context.Context, string, string, int64, int, int) (domain.RunEventPage, error)
	GetRunByIssueIdempotency(context.Context, string, string, string) (domain.Run, bool, error)
	CreateRun(context.Context, string, string, int64, string, domain.Run) (domain.Run, bool, error)
	GetRun(context.Context, string, string) (domain.Run, error)
	StopRun(context.Context, string, string, string) (domain.Run, error)
	SteerRun(context.Context, string, string, string, []domain.ContentBlock, bool) (domain.Run, error)
	GetRunScope(context.Context, string) (RunScope, error)
	ListRunsByInstance(context.Context, string, string, int) ([]domain.Run, error)
	ListRunsByInstancePage(context.Context, string, string, string, int) (domain.Page[domain.Run], error)
	ListRunsByIssue(context.Context, string, string, int) ([]domain.Run, error)
	ListRunsByIssuePage(context.Context, string, string, string, int) (domain.Page[domain.Run], error)
	ListIssueRunEventHeadsPage(context.Context, string, string, string, int) (domain.Page[IssueRunEventHead], error)
	ClaimNextIssueMention(context.Context, time.Time, time.Duration) (IssueMentionClaim, bool, error)
	RetryIssueMention(context.Context, IssueMentionClaim, string, time.Time) error
	DeadLetterIssueMention(context.Context, IssueMentionClaim, string) error
	CreateRunForMention(context.Context, string, string, string, string, []domain.ContentBlock, string) (domain.Run, error)
	CreateRunForClaimedMention(context.Context, IssueMentionClaim, []domain.ContentBlock, string) (domain.Run, error)
}

// LocalGoalRepository coordinates durable goals through the embedded dispatcher.
type LocalGoalRepository interface {
	AdvanceConversationGoals(context.Context, time.Time) (int, error)
	CreateIssueAgentGoalRun(
		context.Context, string, string, string, int64, string, string, *int64, domain.Run,
	) (domain.Run, domain.IssueAgentGoal, bool, error)
	GetCurrentIssueAgentGoal(context.Context, string, string) (domain.IssueAgentGoal, bool, error)
	GetIssueAgentGoal(context.Context, string, string) (domain.IssueAgentGoal, error)
	PauseIssueAgentGoal(context.Context, string, string, int64) (domain.IssueAgentGoal, error)
	ResumeIssueAgentGoal(context.Context, string, string, int64) (domain.IssueAgentGoal, error)
	CancelIssueAgentGoal(context.Context, string, string, int64) (domain.IssueAgentGoal, error)
	AdvanceLocalGoals(context.Context, time.Time) (int, error)
}

// ConversationGraphRepository owns graph conversations in the local workbench.
// Graphs are independent of Issues: they reuse projects, resources, agents and
// memories, but keep their own nodes, messages and runs.
type ConversationGraphRepository interface {
	CreateConversationGoal(context.Context, string, string, string, string, *int64, ...domain.ConversationAgentBinding) (domain.ConversationGoal, bool, error)
	GetCurrentConversationGoal(context.Context, string, string) (*domain.ConversationGoal, error)
	ChangeConversationGoal(context.Context, string, string, string, int64, string) (domain.ConversationGoal, error)
	ListConversationGraphs(context.Context, string, string) ([]domain.ConversationGraph, error)
	CreateConversationGraph(context.Context, string, domain.ConversationGraph, domain.ConversationNode) (domain.ConversationGraphDetail, error)
	InitializeConversationGraphRoot(context.Context, string, string, string, int64) (domain.ConversationGraphDetail, error)
	UpdateConversationGraphTitle(context.Context, string, string, int64, string) (domain.ConversationGraph, error)
	GetConversationGraph(context.Context, string, string) (domain.ConversationGraph, error)
	GetConversationGraphDetail(context.Context, string, string) (domain.ConversationGraphDetail, error)
	DeleteConversationGraph(context.Context, string, string, int64) error

	GetConversationNode(context.Context, string, string) (domain.ConversationNode, error)
	UpdateConversationNode(context.Context, string, string, int64, func(*domain.ConversationNode)) (domain.ConversationNode, error)
	DeleteConversationNode(context.Context, string, string, int64) error
	ForkConversationNode(context.Context, string, string, string, domain.ConversationNode) (domain.ConversationNode, error)

	ListConversationMessages(context.Context, string, string) ([]domain.ConversationMessage, error)
	CreateConversationMessage(context.Context, string, string, domain.ConversationMessage) (domain.ConversationMessage, error)
}

// ConversationRunRepository creates execution runs owned by a conversation node.
type ConversationRunRepository interface {
	SendConversationMessage(context.Context, string, string, string, string, ...domain.ConversationAgentBinding) (domain.ConversationMessage, bool, error)
}

type MemoryPolicy interface {
	ListMemories(context.Context, string, string) ([]domain.ProjectMemoryEntry, error)
	ListArchivedMemories(context.Context, string, string) ([]domain.ProjectMemoryEntry, error)
	CreateMemory(context.Context, string, domain.ProjectMemoryEntry, string) (domain.ProjectMemoryEntry, error)
	GetMemory(context.Context, string, string, string) (domain.ProjectMemoryEntry, error)
	UpdateMemory(context.Context, string, string, string, int64, string, string, string, ...bool) (domain.ProjectMemoryEntry, error)
	ArchiveMemory(context.Context, string, string, string, int64) error
	RestoreMemory(context.Context, string, string, string, int64) (domain.ProjectMemoryEntry, error)
	ListMemoryVersions(context.Context, string, string, string) ([]domain.ProjectMemoryVersion, error)
	ListApprovals(context.Context, string, string) ([]domain.Approval, error)
	CreateApproval(context.Context, domain.Approval) (domain.Approval, error)
	GetApproval(context.Context, string, string) (domain.Approval, error)
	DecideApproval(context.Context, string, string, int64, string, string, string) (domain.Approval, error)
	ListPolicyRules(context.Context, string, string) ([]domain.PolicyRule, error)
	CreatePolicyRule(context.Context, string, domain.PolicyRule) (domain.PolicyRule, error)
	UpdatePolicyRule(context.Context, string, string, int64, func(*domain.PolicyRule)) (domain.PolicyRule, error)
	DeletePolicyRule(context.Context, string, string, int64) error
}

type ScheduleAudit interface {
	ListScheduledTasks(context.Context, string, string) ([]domain.ScheduledTask, error)
	CreateScheduledTask(context.Context, string, domain.ScheduledTask) (domain.ScheduledTask, error)
	GetScheduledTask(context.Context, string, string) (domain.ScheduledTask, error)
	UpdateScheduledTask(context.Context, string, string, int64, func(*domain.ScheduledTask)) (domain.ScheduledTask, error)
	TriggerScheduledTask(context.Context, string, string, time.Time) (domain.ScheduledTaskRun, bool, error)
	ListScheduledTaskRuns(context.Context, string, string) ([]domain.ScheduledTaskRun, error)
	AppendAuditEvent(context.Context, domain.AuditEvent) error
	ListAuditEvents(context.Context, string, string, string) ([]domain.AuditEvent, error)
}

// ProviderSettingsRepository persists local, non-secret Provider runtime
// preferences such as a user-configured CLI path. It never stores credentials.
type ProviderSettingsRepository interface {
	ProviderExecutablePaths(context.Context) (map[string]string, error)
	ProviderExecutablePath(context.Context, string) (string, error)
	SetProviderExecutablePath(context.Context, string, string) error
}

type CoreLocalRepository interface {
	Health
	IdentitySession
	ComputerService
	ProjectResource
	ProjectSearch
	AgentSkill
	IssueComment
	RunEvent
	MemoryPolicy
	ScheduleAudit
	LocalGoalRepository
	ConversationGraphRepository
	ConversationRunRepository
	WorkHistoryRepository
	AssistantRepository
	InspirationRepository
	OnboardingRepository
	FeatureTourRepository
	ProviderSettingsRepository
}

// RunScope identifies the durable owner and execution scope of a run.
type RunScope struct {
	AssistantSessionID string
	UserID             string
	ComputerID         string
	IssueID            string
	AgentID            string
	InstanceID         string
	GoalID             string
	RunPurpose         string
	GoalCommentID      string
	ConversationNodeID string
}

// IssueMentionClaim is a durable, fenced claim on one structured @ mention.
// Attempts is incremented when the lease is committed. ClaimToken must be
// presented by completion/retry so an expired worker cannot mutate a newer
// owner's claim.
type IssueMentionClaim struct {
	IssueID    string
	UserID     string
	AgentID    string
	MentionID  string
	CommentID  string
	ClaimToken string
	Attempts   int
}
