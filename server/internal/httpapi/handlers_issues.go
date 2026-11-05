package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// --- Issues ---

func (a *API) listIssues(c *gin.Context) {
	query := c.Request.URL.Query()
	if query.Has("limit") || query.Has("offset") || query.Has("search") || query.Has("status") {
		q, err := issueListQuery(c)
		if err != nil {
			respondError(c, err)
			return
		}
		page, err := a.store.ListIssuesPage(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), q)
		if err != nil {
			respondError(c, err)
			return
		}
		respond(c, http.StatusOK, page, 0)
		return
	}
	items, err := a.store.ListIssues(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.Issue]{Items: items}, 0)
}

func (a *API) createIssue(c *gin.Context) {
	var request struct {
		Title        string `json:"title"`
		Description  string `json:"description"`
		Status       string `json:"status"`
		WorktreeHint bool   `json:"worktree_hint"`
	}
	if !bindJSON(c, &request) {
		return
	}
	// 空标题由 Store 原子初始化自动标题；描述创建后只读。
	title := strings.TrimSpace(request.Title)
	if request.Status != "" && !validIssueStatus(request.Status) {
		respondError(c, domain.Invalid("不支持的任务状态"))
		return
	}
	item, err := a.store.CreateIssue(c.Request.Context(), authenticatedUser(c).ID, domain.Issue{
		ProjectID:    c.Param("project_id"),
		Title:        title,
		Description:  strings.TrimSpace(request.Description),
		Status:       request.Status,
		WorktreeHint: request.WorktreeHint,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) getIssue(c *gin.Context) {
	item, err := a.store.GetIssue(c.Request.Context(), authenticatedUser(c).ID, c.Param("issue_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) updateIssue(c *gin.Context) {
	// 描述创建后只读；bindJSON 严格拒绝未知字段，包括 description: null。
	var request struct {
		Title           *string `json:"title"`
		Status          *string `json:"status"`
		WorktreeHint    *bool   `json:"worktree_hint"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Status != nil && !validIssueStatus(*request.Status) {
		respondError(c, domain.Invalid("不支持的任务状态"))
		return
	}
	item, err := a.store.UpdateIssue(c.Request.Context(), authenticatedUser(c).ID, c.Param("issue_id"), request.ExpectedVersion, func(issue *domain.Issue) {
		if request.Title != nil {
			issue.Title = strings.TrimSpace(*request.Title)
			issue.TitleEdited = true
		}
		if request.Status != nil {
			issue.Status = *request.Status
		}
		if request.WorktreeHint != nil {
			issue.WorktreeHint = *request.WorktreeHint
		}
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func validIssueStatus(status string) bool {
	switch status {
	case domain.IssueOpen, domain.IssueResolved, domain.IssueClosed:
		return true
	default:
		return false
	}
}

func (a *API) deleteIssue(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeleteIssue(c.Request.Context(), authenticatedUser(c).ID, c.Param("issue_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Issue comments ---

func (a *API) listIssueComments(c *gin.Context) {
	items, err := a.store.ListIssueComments(c.Request.Context(), authenticatedUser(c).ID, c.Param("issue_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.IssueComment]{Items: items}, 0)
}

// createIssueComment 创建评论。人类评论可携带 mentioned_agent_ids（结构化 @），
// 提交后由 scheduler 消费 mentions 激活对应 Agent 实例；Agent 评论由工具
// (ToolService) 从持久化 Run 作用域发布，不能借用用户 HTTP 入口伪造。
func (a *API) createIssueComment(c *gin.Context) {
	var request struct {
		Body              string                      `json:"body"`
		MentionedAgentIDs []string                    `json:"mentioned_agent_ids"`
		WorkItems         []nodeprotocol.WorkPlanItem `json:"work_items"`
		AuthorKind        string                      `json:"author_kind"`
		AuthorAgentID     string                      `json:"author_agent_id"`
		RunID             string                      `json:"run_id"`
	}
	if !bindJSON(c, &request) {
		return
	}
	body := strings.TrimSpace(request.Body)
	if body == "" && len(request.WorkItems) > 0 {
		body = "工作分配"
	}
	if body == "" {
		respondError(c, domain.Invalid("评论内容不能为空"))
		return
	}
	if (request.AuthorKind != "" && request.AuthorKind != domain.IssueCommentAuthorUser) ||
		request.AuthorAgentID != "" || request.RunID != "" {
		respondError(c, domain.Invalid("用户评论不能指定 Agent 作者或关联运行"))
		return
	}
	if len(request.MentionedAgentIDs) > 1 || (len(request.WorkItems) > 0 && len(request.MentionedAgentIDs) > 0) {
		respondError(c, domain.Invalid("普通评论只能 @ 一个 Agent；多份工作请使用工作分配，不能同时提交两种激活方式"))
		return
	}
	comment := domain.IssueComment{
		IssueID:      c.Param("issue_id"),
		AuthorKind:   domain.IssueCommentAuthorUser,
		AuthorUserID: authenticatedUser(c).ID,
		Body:         body,
	}
	var item domain.IssueComment
	var err error
	if len(request.WorkItems) > 0 {
		item, err = a.store.CreateIssueWorkComment(c.Request.Context(), authenticatedUser(c).ID, comment, request.WorkItems, c.GetHeader("Idempotency-Key"))
	} else {
		item, err = a.store.CreateIssueComment(c.Request.Context(), authenticatedUser(c).ID, comment, request.MentionedAgentIDs)
	}
	if err != nil {
		respondError(c, err)
		return
	}
	// 通知 scheduler 消费 mentions 激活 Agent。
	if (len(request.MentionedAgentIDs) > 0 || len(request.WorkItems) > 0) && a.runtime != nil {
		a.runtime.NotifyIssueActivation(c.Param("issue_id"))
		// A running dispatcher can materialize conversations before the response;
		// API-only tests may provide just the durable notification capability.
		if activator, ok := a.runtime.(IssueMentionActivator); ok {
			_ = activator.ActivateIssueMentions(c.Request.Context(), c.Param("issue_id"))
		}
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) updateIssueComment(c *gin.Context) {
	var request struct {
		Body            *string `json:"body"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	userID := authenticatedUser(c).ID
	existing, err := a.store.GetIssueComment(c.Request.Context(), userID, c.Param("comment_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	// 只有用户自己发布的评论可以编辑；Agent 评论只能删除。
	if existing.AuthorKind != domain.IssueCommentAuthorUser {
		respondError(c, domain.Forbidden("not_own_comment", "只能编辑自己发送的评论"))
		return
	}
	item, err := a.store.UpdateIssueComment(c.Request.Context(), userID, c.Param("comment_id"), request.ExpectedVersion, func(comment *domain.IssueComment) error {
		if request.Body != nil {
			comment.Body = strings.TrimSpace(*request.Body)
			if comment.Body == "" {
				return domain.Invalid("评论内容不能为空")
			}
		}
		return nil
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) deleteIssueComment(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeleteIssueComment(c.Request.Context(), authenticatedUser(c).ID, c.Param("comment_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) listIssueCommentRevisions(c *gin.Context) {
	items, err := a.store.ListIssueCommentRevisions(c.Request.Context(), authenticatedUser(c).ID, c.Param("comment_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.IssueCommentRevision]{Items: items}, 0)
}

// --- Issue agent instances ---

func (a *API) listIssueAgentInstances(c *gin.Context) {
	items, err := a.store.ListIssueAgentInstances(c.Request.Context(), authenticatedUser(c).ID, c.Param("issue_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.IssueAgentInstance]{Items: items}, 0)
}

func (a *API) pauseIssueAgentInstance(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if !a.requireIssueAgentInstancePath(c) {
		return
	}
	item, err := a.store.UpdateIssueAgentInstance(c.Request.Context(), authenticatedUser(c).ID, c.Param("instance_id"), request.ExpectedVersion, func(instance *domain.IssueAgentInstance) {
		instance.Status = domain.AgentInstancePaused
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) resumeIssueAgentInstance(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if !a.requireIssueAgentInstancePath(c) {
		return
	}
	item, err := a.store.UpdateIssueAgentInstance(c.Request.Context(), authenticatedUser(c).ID, c.Param("instance_id"), request.ExpectedVersion, func(instance *domain.IssueAgentInstance) {
		instance.Status = domain.AgentInstanceIdle
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) requireIssueAgentInstancePath(c *gin.Context) bool {
	instance, err := a.store.GetIssueAgentInstanceByID(c.Request.Context(), authenticatedUser(c).ID, c.Param("instance_id"))
	if err != nil {
		respondError(c, err)
		return false
	}
	// Instance ownership is not enough: the immutable Issue identity must also
	// match the URL, otherwise another Issue/project could pause this instance.
	if instance.IssueID != c.Param("issue_id") {
		respondError(c, domain.NotFound("任务 Agent 会话", c.Param("instance_id")))
		return false
	}
	return true
}
