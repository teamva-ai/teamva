package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

type API struct {
	store               store.CoreLocalRepository
	version             string
	runtime             Runtime
	lifecycleContext    context.Context
	allowedOrigins      map[string]struct{}
	cookieSecure        bool
	webOrigin           string
	externalAccess      bool
	networkAccess       NetworkAccessController
	localUserID         string
	localBootstrap      string
	localBootstrapUntil time.Time
	localBootstrapMu    sync.Mutex
	localBootstrapped   bool
	capabilities        RuntimeCapabilities
	mcp                 MCPManager
	resourceTitles      *resourceTitleFetcher
}

type Options struct {
	AllowedOrigins   []string
	WebOrigin        string
	CookieSecure     bool
	LifecycleContext context.Context
	// LocalBootstrapNonce is a short-lived one-time secret used by the desktop
	// shell to obtain a normal HttpOnly browser session.
	LocalUserID         string
	LocalBootstrapNonce string
	// ExternalAccess permits same-origin requests from non-loopback peers. The
	// listener wrapper has already required a password for those peers.
	ExternalAccess bool
	NetworkAccess  NetworkAccessController
	Capabilities   RuntimeCapabilities
	MCP            MCPManager
}

type RuntimeCapabilities struct {
	Mode       string            `json:"mode"`
	Auth       map[string]bool   `json:"auth"`
	Topology   map[string]bool   `json:"topology"`
	Runtime    map[string]bool   `json:"runtime"`
	Filesystem map[string]bool   `json:"filesystem"`
	Database   map[string]string `json:"database"`
}

// Runtime is the in-process notification boundary. Filesystem, diagnostics and
// event subscriptions expose separate, purpose-specific capability interfaces.
type Runtime interface {
	NotifyIssueActivation(string)
}

type RunEventSubscriber interface {
	SubscribeRunEvents(string) (<-chan struct{}, func())
	SubscribeIssue(string) (<-chan struct{}, func())
	NotifyIssueActivation(string)
}

type IssueMentionActivator interface {
	ActivateIssueMentions(context.Context, string) error
}

// NewLocal serves the same authenticated API in a browser and in Wails.
func NewLocal(repository store.CoreLocalRepository, runtime Runtime, version string, options Options) *gin.Engine {
	return buildRouter(repository, runtime, version, true, options)
}

func buildRouter(
	repository store.CoreLocalRepository,
	runtime Runtime,
	version string,
	authRequired bool,
	options Options,
) *gin.Engine {
	lifecycleContext := options.LifecycleContext
	if lifecycleContext == nil {
		lifecycleContext = context.Background()
	}
	allowedOrigins := normalizeOrigins(options.AllowedOrigins)
	webOrigin, webOriginErr := CanonicalBrowserOrigin(options.WebOrigin)
	if webOriginErr == nil {
		allowedOrigins[webOrigin] = struct{}{}
	} else {
		webOrigin = ""
	}
	api := &API{
		store: repository, runtime: runtime, version: version,
		networkAccess:       options.NetworkAccess,
		lifecycleContext:    lifecycleContext,
		allowedOrigins:      allowedOrigins,
		cookieSecure:        options.CookieSecure,
		webOrigin:           webOrigin,
		externalAccess:      options.ExternalAccess,
		localUserID:         options.LocalUserID,
		localBootstrap:      options.LocalBootstrapNonce,
		localBootstrapUntil: time.Now().Add(30 * time.Second),
		capabilities:        options.Capabilities,
		mcp:                 options.MCP,
	}
	router := gin.New()
	router.Use(gin.Recovery(), requestContext(), api.cors())
	if err := router.SetTrustedProxies(nil); err != nil {
		panic(err)
	}

	router.GET("/livez", api.live)
	router.GET("/readyz", api.ready)
	router.GET("/desktop/bootstrap", api.desktopBootstrap)
	v1 := router.Group("/v1")
	v1.GET("/health", api.health)
	v1.POST("/auth/local-session", api.localSession)

	protected := v1.Group("")
	if authRequired {
		protected.Use(authenticate(api))
	}
	if authRequired {
		protected.Use(csrfProtection(api))
	}
	if authRequired {
		protected.Use(api.captureWorkOperation())
	}
	protected.POST("/work-records/query", api.queryWorkRecords)
	protected.POST("/work-records/navigation", api.recordWorkNavigation)
	protected.POST("/work-records/clear", api.clearWorkRecords)
	protected.GET("/work-records/:record_id", api.getWorkRecord)
	protected.GET("/work-record-filters", api.listWorkFilters)
	protected.POST("/work-record-filters", api.saveWorkFilter)
	protected.DELETE("/work-record-filters/:filter_id", api.deleteWorkFilter)
	protected.GET("/work-assistant", api.getWorkAssistant)
	protected.GET("/inspirations", api.listInspirations)
	protected.GET("/inspirations/groups", api.inspirationGroups)
	protected.POST("/inspirations", api.createInspiration)
	protected.GET("/inspirations/:inspiration_id", api.getInspiration)
	protected.PATCH("/inspirations/:inspiration_id", api.updateInspiration)
	protected.DELETE("/inspirations/:inspiration_id", api.deleteInspiration)
	protected.GET("/inspirations/:inspiration_id/markdown", api.downloadInspiration)
	protected.GET("/work-assistant/sessions", api.listAssistantSessions)
	protected.POST("/work-assistant/sessions", api.createAssistantSession)
	protected.GET("/work-assistant/sessions/:session_id/messages", api.listAssistantMessages)
	protected.POST("/work-assistant/sessions/:session_id/messages", api.sendAssistantMessage)
	protected.GET("/auth/bootstrap", api.getAuthBootstrap)
	protected.GET("/auth/csrf", api.getCSRFToken)
	protected.GET("/me", api.getMe)
	protected.GET("/system/capabilities", api.getRuntimeCapabilities)
	protected.GET("/system/network-access", api.getNetworkAccess)
	protected.PUT("/system/network-access", api.updateNetworkAccess)
	protected.GET("/system/programming-tools", api.listProgrammingToolExecutables)
	protected.PUT("/system/programming-tools/:provider", api.updateProgrammingToolExecutable)
	protected.GET("/system/programming-tools/:provider/model-providers", api.getModelProviderSettings)
	protected.PUT("/system/programming-tools/:provider/model-providers", api.saveModelProvider)
	protected.POST("/system/programming-tools/:provider/model-providers/test", api.testModelProvider)
	protected.GET("/mcp/servers", api.listMCPServers)
	protected.POST("/mcp/servers", api.saveMCPServer)
	protected.DELETE("/mcp/servers/:name", api.deleteMCPServer)
	protected.POST("/mcp/servers/:name/connect", api.connectMCPServer)
	protected.POST("/mcp/servers/:name/disconnect", api.disconnectMCPServer)
	protected.POST("/mcp/servers/:name/auth", api.authMCPServer)
	protected.POST("/mcp/servers/:name/revoke", api.revokeMCPServer)
	protected.GET("/system/onboarding", api.getOnboarding)
	protected.PUT("/system/onboarding", api.updateOnboarding)
	protected.GET("/me/preferences", api.getUserPreferences)
	protected.PATCH("/me/preferences", api.updateUserPreferences)
	protected.GET("/me/feature-tours", api.getFeatureTours)
	protected.POST("/me/feature-tours/:tour_id/seen", api.markFeatureTourSeen)
	protected.GET("/computers", api.listComputers)
	protected.GET("/computers/:computer_id", api.getComputer)
	protected.GET("/computers/:computer_id/agent-services", api.listAgentServices)
	protected.POST("/agent-services/:service_id/probe", api.probeAgentService)
	protected.POST("/computers/:computer_id/programming-tools/:provider/diagnose", api.diagnoseProgrammingTool)
	protected.POST("/computers/:computer_id/programming-tools/:provider/recycle-idle", api.recycleIdleProgrammingTool)
	protected.GET("/computers/:computer_id/resource-openers", api.listResourceOpeners)
	protected.POST("/computers/:computer_id/directory-list", api.listDirectory)
	protected.GET("/computers/:computer_id/directory-list/:command_id", api.listDirectoryResult)
	protected.POST("/computers/:computer_id/resource-register", api.registerResource)
	protected.GET("/audit-events", api.listAuditEvents)

	protected.GET("/projects", api.listProjects)
	protected.POST("/projects", api.createProject)
	protected.GET("/projects/:project_id", api.getProject)
	protected.PATCH("/projects/:project_id", api.updateProject)
	protected.DELETE("/projects/:project_id", api.deleteProject)
	protected.GET("/projects/:project_id/search", api.searchProjectContent)

	protected.GET("/projects/:project_id/resources", api.listResources)
	protected.POST("/projects/:project_id/resources", api.createResource)
	protected.PATCH("/projects/:project_id/resources/:resource_id", api.updateResource)
	protected.DELETE("/projects/:project_id/resources/:resource_id", api.deleteResource)
	protected.POST("/projects/:project_id/resources/:resource_id/validate", api.validateResource)
	protected.POST("/projects/:project_id/resources/:resource_id/open", api.openResource)
	protected.GET("/projects/:project_id/git/repositories", api.listGitRepositories)
	protected.GET("/projects/:project_id/git/repositories/:repository_id/changes", api.listGitChanges)
	protected.GET("/projects/:project_id/git/repositories/:repository_id/changes/:change_id/diff", api.getGitDiff)
	protected.POST("/projects/:project_id/git/refresh", api.refreshGit)
	protected.GET("/projects/:project_id/git/events", api.streamGitEvents)

	protected.GET("/computers/:computer_id/agents", api.listAgents)
	protected.POST("/computers/:computer_id/agents", api.createAgent)
	protected.GET("/agents/:agent_id", api.getAgent)
	protected.PATCH("/agents/:agent_id", api.updateAgent)
	protected.DELETE("/agents/:agent_id", api.deleteAgent)
	protected.GET("/agent-revisions/:revision_id", api.getAgentRevisions)
	protected.GET("/skill-packages", api.listSkillPackages)
	protected.GET("/computers/:computer_id/computer-use", api.getComputerUseStatus)
	protected.PATCH("/computers/:computer_id/computer-use", api.setComputerUseEnabled)
	protected.POST("/computers/:computer_id/computer-use", api.installComputerUse)
	protected.GET("/computers/:computer_id/skill-packages", api.listComputerSkillPackages)
	protected.POST("/computers/:computer_id/skill-packages", api.installComputerSkillPackage)
	protected.POST("/computers/:computer_id/skill-packages/:installation_id/open", api.openComputerSkillPackage)
	protected.DELETE("/computers/:computer_id/skill-packages/:installation_id", api.uninstallComputerSkillPackage)

	protected.GET("/projects/:project_id/issues", api.listIssues)
	protected.POST("/projects/:project_id/issues", api.createIssue)
	protected.GET("/issues/:issue_id", api.getIssue)
	protected.PATCH("/issues/:issue_id", api.updateIssue)
	protected.DELETE("/issues/:issue_id", api.deleteIssue)
	protected.GET("/issues/:issue_id/comments", api.listIssueComments)
	protected.POST("/issues/:issue_id/comments", api.createIssueComment)
	protected.POST("/issues/:issue_id/work-plan", api.generateIssueWorkPlan)
	protected.PATCH("/comments/:comment_id", api.updateIssueComment)
	protected.DELETE("/comments/:comment_id", api.deleteIssueComment)
	protected.GET("/comments/:comment_id/revisions", api.listIssueCommentRevisions)
	protected.GET("/issues/:issue_id/agent-instances", api.listIssueAgentInstances)
	protected.GET("/issues/:issue_id/runs", api.listIssueRuns)
	protected.GET("/agent-instances/:instance_id/runs", api.listInstanceRuns)
	protected.GET("/agent-instances/:instance_id/goal", api.getCurrentIssueAgentGoal)
	protected.POST("/issues/:issue_id/agent-instances/:instance_id/pause", api.pauseIssueAgentInstance)
	protected.POST("/issues/:issue_id/agent-instances/:instance_id/resume", api.resumeIssueAgentInstance)
	protected.POST("/issues/:issue_id/runs", api.createIssueRun)
	protected.GET("/issues/:issue_id/events", api.streamIssueEvents)

	protected.GET("/runs/:run_id", api.getRun)
	protected.GET("/runs/:run_id/events", api.listRunEvents)
	protected.GET("/runs/:run_id/events/stream", api.streamRunEvents)
	protected.GET("/runs/:run_id/git/changes", api.getRunGitChanges)
	protected.POST("/runs/:run_id/stop", api.stopRun)
	protected.POST("/runs/:run_id/steer", api.steerRun)
	protected.POST("/goals/:goal_id/pause", api.pauseIssueAgentGoal)
	protected.POST("/goals/:goal_id/resume", api.resumeIssueAgentGoal)
	protected.POST("/goals/:goal_id/cancel", api.cancelIssueAgentGoal)

	protected.GET("/projects/:project_id/conversation-graphs", api.listConversationGraphs)
	protected.POST("/projects/:project_id/conversation-graphs", api.createConversationGraph)
	protected.GET("/conversation-graphs/:graph_id", api.getConversationGraph)
	protected.PATCH("/conversation-graphs/:graph_id", api.updateConversationGraph)
	protected.POST("/conversation-graphs/:graph_id/root", api.initializeConversationGraphRoot)
	protected.DELETE("/conversation-graphs/:graph_id", api.deleteConversationGraph)
	protected.GET("/conversation-nodes/:node_id", api.getConversationNode)
	protected.PATCH("/conversation-nodes/:node_id", api.updateConversationNode)
	protected.DELETE("/conversation-nodes/:node_id", api.deleteConversationNode)
	protected.POST("/conversation-nodes/:node_id/fork", api.forkConversationNode)
	protected.GET("/conversation-nodes/:node_id/messages", api.listConversationMessages)
	protected.POST("/conversation-nodes/:node_id/messages", api.createConversationMessage)
	protected.GET("/conversation-nodes/:node_id/goal", api.getConversationGoal)
	protected.POST("/conversation-nodes/:node_id/goals", api.createConversationGoal)
	protected.POST("/conversation-nodes/:node_id/goals/:goal_id/:action", api.changeConversationGoal)

	protected.GET("/projects/:project_id/memories", api.listMemories)
	protected.POST("/projects/:project_id/memories", api.createMemory)
	protected.GET("/projects/:project_id/memories/:memory_id", api.getMemory)
	protected.PATCH("/projects/:project_id/memories/:memory_id", api.updateMemory)
	protected.POST("/projects/:project_id/memories/:memory_id/archive", api.archiveMemory)
	protected.POST("/projects/:project_id/memories/:memory_id/restore", api.restoreMemory)
	protected.GET("/projects/:project_id/memories/:memory_id/versions", api.listMemoryVersions)

	protected.GET("/approvals", api.listApprovals)
	protected.GET("/approvals/:approval_id", api.getApproval)
	protected.POST("/approvals/:approval_id/decisions", api.decideApproval)
	protected.GET("/projects/:project_id/policy-rules", api.listPolicyRules)
	protected.POST("/projects/:project_id/policy-rules", api.createPolicyRule)
	protected.PATCH("/policy-rules/:rule_id", api.updatePolicyRule)
	protected.DELETE("/policy-rules/:rule_id", api.deletePolicyRule)

	protected.GET("/projects/:project_id/scheduled-tasks", api.listScheduledTasks)
	protected.POST("/projects/:project_id/scheduled-tasks", api.createScheduledTask)
	protected.GET("/scheduled-tasks/:task_id", api.getScheduledTask)
	protected.PATCH("/scheduled-tasks/:task_id", api.updateScheduledTask)
	protected.POST("/scheduled-tasks/:task_id/pause", api.pauseScheduledTask)
	protected.POST("/scheduled-tasks/:task_id/resume", api.resumeScheduledTask)
	protected.POST("/scheduled-tasks/:task_id/run-now", api.runScheduledTaskNow)
	protected.GET("/scheduled-tasks/:task_id/runs", api.listScheduledTaskRuns)
	protected.POST("/scheduled-tasks/preview", api.previewSchedule)

	return router
}

func (a *API) desktopBootstrap(c *gin.Context) {
	if IsRemoteAccess(c.Request) {
		c.Status(http.StatusNotFound)
		return
	}
	if a.localBootstrap == "" || a.localUserID == "" {
		c.Status(http.StatusNotFound)
		return
	}
	provided := strings.TrimSpace(c.Query("nonce"))
	a.localBootstrapMu.Lock()
	valid := !a.localBootstrapped && time.Now().Before(a.localBootstrapUntil) &&
		len(provided) == len(a.localBootstrap) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(a.localBootstrap)) == 1
	if valid {
		a.localBootstrapped = true
	}
	a.localBootstrapMu.Unlock()
	if !valid {
		c.Status(http.StatusNotFound)
		return
	}
	meta, err := a.sessionMetaFromRequest(c)
	if err != nil {
		respondError(c, err)
		return
	}
	_, session, err := a.store.CreateLocalSession(c.Request.Context(), a.localUserID, sessionIdleTTL, meta)
	if err != nil {
		respondError(c, err)
		return
	}
	a.setBrowserCookie(c, sessionCookieName, session.Token, int(sessionIdleTTL.Seconds()))
	c.Header("Referrer-Policy", "no-referrer")
	c.Redirect(http.StatusSeeOther, "/workbench")
}

func (a *API) getRuntimeCapabilities(c *gin.Context) {
	respond(c, http.StatusOK, a.capabilities, 0)
}

func (a *API) live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": a.version, "time": utcNow()})
}

func (a *API) health(c *gin.Context) {
	if err := a.store.Health(c.Request.Context()); err != nil {
		respondError(c, &domain.Error{
			Code: "dependency_unavailable", Message: "本地数据库当前不可用",
			HTTPStatus: http.StatusServiceUnavailable, Retryable: true,
		})
		return
	}
	respond(c, http.StatusOK, gin.H{"status": "ok", "version": a.version}, 0)
}

func (a *API) ready(c *gin.Context) {
	if err := a.store.Health(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unavailable", "version": a.version, "time": utcNow(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": a.version, "time": utcNow()})
}
