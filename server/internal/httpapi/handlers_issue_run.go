package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

// createIssueRun 创建一次 Issue 下的 Agent 执行。
// 结构化 @ 由 mention worker 创建新会话；sidesheet 追问携带
// issue_agent_instance_id，从而只续接当前打开的会话，不会串到同一
// Agent 的其他 @ 激活会话。调用方必须明确指定当前会话。
// prompt 由服务端基于 Issue 上下文组装，Agent 的模型/提示词/skills 由配置决定，
// 人类不能随意指定模型（「只能选中 Agent 对话」）。
func (a *API) createIssueRun(c *gin.Context) {
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if idempotencyKey == "" {
		respondError(c, domain.Invalid("缺少 Idempotency-Key"))
		return
	}
	var request struct {
		IssueAgentInstanceID string                `json:"issue_agent_instance_id"`
		Input                string                `json:"input"`
		Attachments          []domain.ContentBlock `json:"attachments"`
		Intent               string                `json:"intent"`
		Internal             bool                  `json:"internal"`
		Goal                 bool                  `json:"goal"`
		GoalTokenBudget      *int64                `json:"goal_token_budget"`
		ExpectedIssueVersion int64                 `json:"expected_issue_version" binding:"required,min=1"`
	}
	if !bindAttachmentJSON(c, &request) {
		return
	}
	if err := validateRequestedAttachments(request.Attachments); err != nil {
		respondError(c, err)
		return
	}
	intent := request.Intent
	if intent == "" {
		intent = "message"
	}
	attachments := request.Attachments
	if len(attachments) == 0 {
		attachments = nil
	}
	// Optimistic Issue versions may be stale on retries; user intent, target,
	// attachments and Goal settings may not change under the same key.
	canonical, err := json.Marshal(struct {
		InstanceID  string
		Input       string
		Attachments []domain.ContentBlock
		Intent      string
		Internal    bool
		Goal        bool
		TokenBudget *int64
	}{request.IssueAgentInstanceID, request.Input, attachments, intent, request.Internal, request.Goal, request.GoalTokenBudget})
	if err != nil {
		respondError(c, err)
		return
	}
	c.Request = c.Request.WithContext(store.WithRunRequestFingerprint(c.Request.Context(), fmt.Sprintf("%x", sha256.Sum256(canonical))))
	userID := authenticatedUser(c).ID
	issueID := c.Param("issue_id")
	existing, found, err := a.store.GetRunByIssueIdempotency(
		c.Request.Context(), userID, issueID, idempotencyKey,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	if found {
		if request.Goal && existing.GoalID == "" {
			respondError(c, domain.Conflict("idempotency_conflict", "幂等键已用于普通运行", nil))
			return
		}
		respondReplayed(c, http.StatusOK, existing, existing.Version)
		return
	}

	var instance domain.IssueAgentInstance
	err = nil
	reviseComment := request.Intent == "revise_comment"
	if request.GoalTokenBudget != nil && !request.Goal {
		respondError(c, domain.Invalid("goal_token_budget 只能与 goal=true 一起使用"))
		return
	}
	if request.Goal && reviseComment {
		respondError(c, domain.Invalid("修正评论不能作为持续目标运行"))
		return
	}
	if (request.Goal || reviseComment || request.Internal) && len(request.Attachments) > 0 {
		respondError(c, domain.Invalid("持续目标和内部控制消息不能携带附件"))
		return
	}
	if request.IssueAgentInstanceID != "" {
		instance, err = a.store.GetIssueAgentInstanceByID(c.Request.Context(), userID, request.IssueAgentInstanceID)
		if err == nil && instance.IssueID != issueID {
			err = domain.NotFound("Agent 会话", request.IssueAgentInstanceID)
		}
	} else {
		err = domain.Invalid("缺少 issue_agent_instance_id")
	}
	if err != nil {
		respondError(c, err)
		return
	}
	if _, err := a.store.GetIssue(c.Request.Context(), userID, issueID); err != nil {
		respondError(c, err)
		return
	}
	// 2) 组装 prompt：Issue 标题/描述 + worktree hint + 人类评论上下文 + 已激活的其他 Agent。
	extraInput := request.Input
	switch request.Intent {
	case "", "message":
	case "revise_comment":
		request.Internal = true
		extraInput, err = a.buildReviseCommentPrompt(c.Request.Context(), userID, issueID, instance)
	default:
		err = domain.Invalid("不支持的会话消息 intent")
	}
	if err != nil {
		respondError(c, err)
		return
	}
	prompt, err := a.buildIssuePrompt(c.Request.Context(), userID, issueID, instance, extraInput, !reviseComment && !request.Goal)
	if err != nil {
		respondError(c, err)
		return
	}
	if request.Goal {
		prompt += initialGoalInstruction(request.Input)
	}
	if strings.TrimSpace(request.Input) == "" && len(request.Attachments) == 0 && !reviseComment && !request.Internal {
		respondError(c, domain.Invalid("会话消息不能为空"))
		return
	}

	// 3) 配置选择与 generation 创建都由 store 在同一事务内完成。
	// 普通追问使用当前已保存配置；持续 Goal 保持其冻结修订。
	runBlocks := issueRunInput(prompt, request.Input, request.Internal)
	runBlocks = append(runBlocks, request.Attachments...)
	runInput := domain.Run{
		IssueID:              issueID,
		IssueAgentInstanceID: instance.ID,
		AgentID:              instance.AgentID,
		Input:                runBlocks,
		ReadOnly:             false,
	}
	var item domain.Run
	var replayed bool
	if request.Goal {
		item, _, replayed, err = a.store.CreateIssueAgentGoalRun(
			c.Request.Context(), userID, issueID, instance.ID, request.ExpectedIssueVersion,
			idempotencyKey, request.Input, request.GoalTokenBudget, runInput,
		)
	} else {
		item, replayed, err = a.store.CreateRun(c.Request.Context(), userID,
			issueID, request.ExpectedIssueVersion, idempotencyKey, runInput)
	}
	if err != nil {
		respondError(c, err)
		return
	}
	if replayed {
		respondReplayed(c, http.StatusOK, item, item.Version)
		return
	}
	// 标记实例已进入执行（副作用在 createExecutionRun 内完成）。
	respond(c, http.StatusAccepted, item, item.Version)
}

// Separate the genuine user message from generated context without changing
// the concatenated model prompt (the store joins text blocks with one newline).
// Internal controls and attachment-only messages have no public text block.
func issueRunInput(prompt, userInput string, internal bool) []domain.ContentBlock {
	const marker = "\n## 本次指令\n"
	if !internal && strings.TrimSpace(userInput) != "" {
		start := strings.LastIndex(prompt, marker+userInput)
		if start >= 0 {
			start += len(marker)
			blocks := []domain.ContentBlock{
				{Type: "text", Text: prompt[:start-1], Internal: true},
				{Type: "text", Text: userInput},
			}
			if suffix := prompt[start+len(userInput):]; suffix != "" {
				blocks = append(blocks, domain.ContentBlock{Type: "text", Text: strings.TrimPrefix(suffix, "\n"), Internal: true})
			}
			return blocks
		}
	}
	return []domain.ContentBlock{{Type: "text", Text: prompt, Internal: true}}
}

func initialGoalInstruction(objective string) string {
	return fmt.Sprintf(`

## 持续目标模式
本次消息会启动一个由 Teamva 持久化和独立审核的持续目标。

<goal_objective>
%s
</goal_objective>

- 保持完整目标和验收标准，不要为了尽快结束而缩小范围。
- 用工作区、测试和外部状态中的事实验证完成情况。
- 本轮结束前调用 issue_comment_create 更新当前目标的工作进度；系统会复用同一条工作评论。
- 在最终回答中准确列出已完成、已验证和仍待处理的内容，供独立审核器判断。
- 不要自行终止持续目标；每轮结束后系统会独立决定继续、完成或阻塞。`, html.EscapeString(objective))
}

func latestRevisableComment(
	comments []domain.IssueComment,
	runs []domain.Run,
	agentID string,
) (domain.IssueComment, bool) {
	runIDs := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		runIDs[run.ID] = struct{}{}
	}
	for index := len(comments) - 1; index >= 0; index-- {
		comment := comments[index]
		if comment.AuthorKind != domain.IssueCommentAuthorAgent || comment.AuthorAgentID != agentID ||
			comment.Status != domain.IssueCommentPublished || comment.RunID == "" {
			continue
		}
		if _, ok := runIDs[comment.RunID]; ok {
			return comment, true
		}
	}
	return domain.IssueComment{}, false
}

func (a *API) buildReviseCommentPrompt(
	ctx context.Context,
	userID string,
	issueID string,
	instance domain.IssueAgentInstance,
) (string, error) {
	if instance.IssueID != issueID {
		return "", domain.NotFound("Agent 会话", instance.ID)
	}
	target, ok, err := a.store.FindInstanceSummaryComment(ctx, userID, instance.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", domain.Conflict(
			"comment_not_revisable",
			"当前会话还没有可修正的已发布评论",
			nil,
		)
	}
	return reviseCommentInstruction(target), nil
}

func reviseCommentInstruction(target domain.IssueComment) string {
	return fmt.Sprintf(`这是 Teamva 发起的内部“修正评论”操作，不是用户发来的新任务。

请根据当前会话在原评论发布后新增的实际工作，重写下面这条评论，使它成为准确、完整、自洽的最新总结。

目标评论：
- comment_id: %s
- expected_version: %d
- 当前正文：%s

必须调用 issue_comment_update_own，参数使用上面的 comment_id、expected_version 和修正后的完整 body。
不要调用 issue_comment_create，不要发布新评论，不要修改其他 Agent 的评论。
如果工具返回版本冲突，先调用 issue_get 获取目标评论的最新 version 与正文，然后仅重试一次 issue_comment_update_own。
		工具成功后只需简短确认已经修正；不要把这段内部控制指令复述给用户。`, target.ID, target.Version, clipPromptText(target.Body, 16<<10))
}

func (a *API) listIssueRuns(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			respondError(c, domain.Invalid("limit 必须在 1 到 200 之间"))
			return
		}
		limit = value
	}
	page, err := a.store.ListRunsByIssuePage(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("issue_id"), c.Query("cursor"), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, page, 0)
}

func (a *API) listInstanceRuns(c *gin.Context) {
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			respondError(c, domain.Invalid("limit 必须在 1 到 100 之间"))
			return
		}
		limit = parsed
	}
	page, err := a.store.ListRunsByInstancePage(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("instance_id"), c.Query("cursor"), limit,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, page, 0)
}

// buildIssuePrompt 组装注入给 Agent 的提示词。
// worktree_hint 只注入提示：Agent 自己决定是否创建 worktree，创建后把路径写进评论。
func (a *API) buildIssuePrompt(
	ctx context.Context,
	userID string,
	issueID string,
	instance domain.IssueAgentInstance,
	extraInput string,
	requestSummaryComment bool,
) (string, error) {
	// Never silently truncate the user's current instruction. History is a
	// bounded summary with an explicit retrieval hint instead.
	if len(extraInput) > 64<<10 {
		return "", domain.Invalid("本次指令不能超过 64 KiB")
	}
	issue, err := a.store.GetIssue(ctx, userID, issueID)
	if err != nil {
		return "", err
	}
	comments, historyTruncated, err := a.store.ListIssuePromptComments(ctx, userID, issueID)
	if err != nil {
		return "", err
	}
	// 同时被 @ 的其他 Agent（在同一 Issue 中已有实例的）。
	instances, _ := a.store.ListIssueAgentInstances(ctx, userID, issueID)

	var builder strings.Builder
	builder.WriteString("# 任务（Issue）上下文\n\n")
	builder.WriteString("## 标题\n")
	builder.WriteString(clipPromptText(issue.Title, 4<<10))
	builder.WriteString("\n\n## 描述\n")
	if strings.TrimSpace(issue.Description) == "" {
		builder.WriteString("（无）")
	} else {
		builder.WriteString(clipPromptText(issue.Description, 16<<10))
	}
	builder.WriteString("\n\n## 状态\n")
	builder.WriteString(issue.Status)
	if issue.WorktreeHint {
		builder.WriteString("\n\n## 工作树提示\n")
		builder.WriteString("本任务开启了 Git Worktree 提示：如需修改项目代码，请先创建独立的 Git Worktree（git worktree add），")
		builder.WriteString("在工作树内完成改动；创建后必须把 worktree 的名称与路径写进本任务的评论里，供其他 Agent 可见。")
	}
	builder.WriteString("\n\n## 评论历史\n")
	if len(comments) == 0 {
		builder.WriteString("（暂无评论）")
	} else {
		// Prefer the most recent messages when the aggregate budget is reached.
		start, remaining := len(comments), 32<<10
		for start > 0 {
			size := len(comments[start-1].Body) + 256
			if size > remaining {
				historyTruncated = true
				break
			}
			remaining -= size
			start--
		}
		for _, comment := range comments[start:] {
			if comment.Status == domain.IssueCommentWorking {
				continue
			}
			author := "用户"
			if comment.AuthorKind == domain.IssueCommentAuthorAgent {
				author = "Agent(" + comment.AuthorAgentID + ")"
			}
			builder.WriteString("- ")
			builder.WriteString(author)
			builder.WriteString(" [comment_id=" + comment.ID + "]")
			builder.WriteString(": ")
			builder.WriteString(comment.Body)
			builder.WriteString("\n")
		}
	}
	if historyTruncated || len(issue.Description) > 16<<10 || len(issue.Title) > 4<<10 {
		builder.WriteString("\n（上下文已按最近评论及大小预算裁剪；可通过 issue_get 的 comment_id 精确读取评论，或用 issue_agent_context 分页检索运行轨迹。）\n")
	}
	if len(instances) > 1 {
		builder.WriteString("\n## 并行 Agent\n")
		for index, other := range instances {
			if index >= 50 {
				builder.WriteString("- 更多实例请通过工具检索。\n")
				break
			}
			if other.ID == instance.ID {
				continue
			}
			builder.WriteString("- Agent 实例 ")
			builder.WriteString(other.AgentID)
			builder.WriteString(" 状态：")
			builder.WriteString(other.Status)
			builder.WriteString("\n")
		}
		builder.WriteString("你可以通过工具查看其他 Agent 的执行状态与脱敏上下文，也可以在评论中 @ 其他 Agent 委派工作。\n")
	}
	if strings.TrimSpace(extraInput) != "" {
		builder.WriteString("\n## 本次指令\n")
		builder.WriteString(extraInput)
	}
	if requestSummaryComment {
		builder.WriteString("\n\n请阅读上下文后开始工作；本轮结束前请使用 issue_comment_create 工具向本任务发送本轮结果；向其他 Agent 委派时，委派说明即为本轮结果。")
		builder.WriteString("\n每个 Agent 会话只维护一条总结评论：系统会自动复用你最近发布的评论并原地更新，不要重复发布；")
		builder.WriteString("只有用户明确要求“再发一条”时才使用 append_new=true。")
		builder.WriteString(store.IssueAsyncDelegationPrompt)
	}
	return builder.String(), nil
}

func clipPromptText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes-len("…")]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

// 保留 ListConversationEvents 迁移后的事件流端点（按 issue 聚合 run 事件）。
func (a *API) streamIssueEvents(c *gin.Context) {
	const eventPageSize = 256
	issueID := c.Param("issue_id")
	userID := authenticatedUser(c).ID
	if _, err := a.store.GetIssue(c.Request.Context(), userID, issueID); err != nil {
		respondError(c, err)
		return
	}

	var wake <-chan struct{}
	unsubscribe := func() {}
	if subscriber, ok := a.runtime.(RunEventSubscriber); ok {
		wake, unsubscribe = subscriber.SubscribeIssue(issueID)
	}
	defer unsubscribe()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	// Wake notifications are coalesced hints. Polling is also needed when a
	// subscriber is present so a dropped/closed notification cannot strand data.
	pollTicker := time.NewTicker(time.Second)
	defer pollTicker.Stop()
	poll := pollTicker.C
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	after := make(map[string]int64)
	flush := func() bool {
		cursor := ""
		for {
			runs, err := a.store.ListIssueRunEventHeadsPage(c.Request.Context(), userID, issueID, cursor, 50)
			if err != nil {
				return false
			}
			for _, head := range runs.Items {
				runID := head.RunID
				if head.Sequence <= after[runID] {
					continue
				}
				for {
					page, err := a.store.ListRunEventPage(c.Request.Context(), userID, runID,
						after[runID], eventPageSize, store.MaxRunEventPagePayloadBytes)
					if err != nil {
						return false
					}
					for _, event := range page.Items {
						c.SSEvent(event.Type, browserRunEvent(event))
						after[runID] = event.Sequence
					}
					c.Writer.Flush()
					if c.Writer.Written() && c.Request.Context().Err() != nil {
						return false
					}
					if !page.Truncated || len(page.Items) == 0 {
						break
					}
				}
			}
			if runs.NextCursor == "" {
				return true
			}
			cursor = runs.NextCursor
		}
	}
	if !flush() {
		return
	}
	c.Writer.Flush()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case _, ok := <-wake:
			if !ok {
				wake = nil
			} else {
				c.SSEvent("issue.changed", gin.H{"issue_id": issueID})
			}
		case <-poll:
		case <-heartbeat.C:
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			c.Writer.Flush()
		}
		if !flush() {
			return
		}
		c.Writer.Flush()
	}
}
