package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// Text is required to be present, but an individual block may contain "".
// Keep this HTTP-only DTO narrower than the shared execution ContentBlock.
type scheduleTextBlock struct {
	Type     string  `json:"type" binding:"required"`
	Text     *string `json:"text" binding:"required"`
	Internal bool    `json:"internal"`
}

type scheduleInput struct {
	Name               *string             `json:"name"`
	ScheduleKind       string              `json:"schedule_kind" binding:"required"`
	ScheduleExpression string              `json:"schedule_expression"`
	Times              []string            `json:"times"`
	Weekdays           []int               `json:"weekdays"`
	IssueMode          string              `json:"issue_mode"`
	IssueID            string              `json:"issue_id"`
	Timezone           string              `json:"timezone" binding:"required"`
	Prompt             []scheduleTextBlock `json:"prompt" binding:"required,dive"`
	AgentID            string              `json:"agent_id"`
	ConcurrencyPolicy  string              `json:"concurrency_policy"`
	MisfirePolicy      string              `json:"misfire_policy"`
}

func (a *API) listScheduledTasks(c *gin.Context) {
	items, err := a.store.ListScheduledTasks(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ScheduledTask]{Items: items}, 0)
}

func (a *API) createScheduledTask(c *gin.Context) {
	var request scheduleInput
	if !bindJSON(c, &request) {
		return
	}
	if request.IssueMode == "" {
		request.IssueMode = "new"
	}
	prompt, err := validateScheduleInput(request)
	if err != nil {
		respondError(c, err)
		return
	}
	if strings.TrimSpace(request.AgentID) == "" {
		respondError(c, domain.Invalid("计划任务必须选择 Agent"))
		return
	}
	next, err := domain.NextScheduledTaskTime(request.calendar(), time.Now().UTC())
	if err != nil {
		respondError(c, err)
		return
	}
	if request.ConcurrencyPolicy == "" {
		request.ConcurrencyPolicy = "forbid"
	}
	if request.MisfirePolicy == "" {
		request.MisfirePolicy = "skip"
	}
	if err := validateSchedulePolicies(request.ConcurrencyPolicy, request.MisfirePolicy); err != nil {
		respondError(c, domain.Invalid(err.Error()))
		return
	}
	if err := a.validateScheduleAgent(c, c.Param("project_id"), request.AgentID); err != nil {
		respondError(c, err)
		return
	}
	item, err := a.store.CreateScheduledTask(c.Request.Context(), authenticatedUser(c).ID, domain.ScheduledTask{
		ProjectID:    c.Param("project_id"),
		Name:         request.title(),
		ScheduleKind: request.ScheduleKind, ScheduleExpression: request.ScheduleExpression,
		Times: request.Times, Weekdays: request.Weekdays, IssueMode: request.IssueMode, IssueID: request.IssueID,
		Timezone: request.Timezone, Prompt: prompt, AgentID: request.AgentID,
		ConcurrencyPolicy: request.ConcurrencyPolicy,
		MisfirePolicy:     request.MisfirePolicy, NextRunAt: &next,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) getScheduledTask(c *gin.Context) {
	item, err := a.store.GetScheduledTask(c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) updateScheduledTask(c *gin.Context) {
	var request struct {
		scheduleInput
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Name != nil && request.title() == "" {
		respondError(c, domain.Invalid("计划任务名称不能为空"))
		return
	}
	prompt, err := validateScheduleInput(request.scheduleInput)
	if err != nil {
		respondError(c, err)
		return
	}
	if request.ConcurrencyPolicy == "" {
		request.ConcurrencyPolicy = "forbid"
	}
	if request.MisfirePolicy == "" {
		request.MisfirePolicy = "skip"
	}
	if err := validateSchedulePolicies(request.ConcurrencyPolicy, request.MisfirePolicy); err != nil {
		respondError(c, domain.Invalid(err.Error()))
		return
	}
	current, err := a.store.GetScheduledTask(c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	if strings.TrimSpace(request.AgentID) == "" {
		request.AgentID = current.AgentID
	}
	if request.IssueMode == "" {
		request.IssueMode, request.IssueID = current.IssueMode, current.IssueID
	}
	if err := a.validateScheduleAgent(c, current.ProjectID, request.AgentID); err != nil {
		respondError(c, err)
		return
	}
	next, err := domain.NextScheduledTaskTime(request.calendar(), time.Now().UTC())
	if err != nil {
		respondError(c, err)
		return
	}
	item, err := a.store.UpdateScheduledTask(c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"), request.ExpectedVersion, func(task *domain.ScheduledTask) {
		if request.Name != nil {
			task.Name = request.title()
			task.TitleEdited = true
		}
		task.ScheduleKind = request.ScheduleKind
		task.ScheduleExpression = request.ScheduleExpression
		task.Times, task.Weekdays = request.Times, request.Weekdays
		task.IssueMode, task.IssueID = request.IssueMode, request.IssueID
		task.Timezone = request.Timezone
		task.Prompt = prompt
		task.AgentID = request.AgentID
		task.ConcurrencyPolicy = request.ConcurrencyPolicy
		task.MisfirePolicy = request.MisfirePolicy
		task.NextRunAt = &next
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) validateScheduleAgent(c *gin.Context, projectID, agentID string) error {
	userID := authenticatedUser(c).ID
	project, err := a.store.GetProject(c.Request.Context(), userID, projectID)
	if err != nil {
		return err
	}
	agent, err := a.store.GetAgent(c.Request.Context(), userID, agentID)
	if err != nil {
		return err
	}
	if agent.ComputerID != project.ComputerID || agent.Status != domain.AgentActive || agent.CurrentRevisionID == "" {
		return domain.Invalid("计划任务 Agent 必须属于当前项目电脑、处于启用状态且已完成配置")
	}
	return nil
}

func validateSchedulePolicies(concurrency, misfire string) error {
	if concurrency != "forbid" && concurrency != "allow" {
		return fmt.Errorf("concurrency_policy 必须为 forbid 或 allow")
	}
	if misfire != "skip" && misfire != "run_once" {
		return fmt.Errorf("misfire_policy 必须为 skip 或 run_once")
	}
	return nil
}

func (a *API) pauseScheduledTask(c *gin.Context) {
	a.setScheduledTaskEnabled(c, false)
}

func (a *API) resumeScheduledTask(c *gin.Context) {
	a.setScheduledTaskEnabled(c, true)
}

func (a *API) setScheduledTaskEnabled(c *gin.Context, enabled bool) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.UpdateScheduledTask(c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"), request.ExpectedVersion, func(task *domain.ScheduledTask) {
		task.Enabled = enabled
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) runScheduledTaskNow(c *gin.Context) {
	// Manual triggers are user-facing mutations. Resolve ownership before
	// inspecting request details so another tenant cannot trigger or probe a
	// task by guessing its ID. The scheduler's internal trigger remains global.
	_, err := a.store.GetScheduledTask(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if idempotencyKey == "" {
		respondError(c, domain.Invalid("缺少 Idempotency-Key"))
		return
	}
	item, replayed, err := a.store.TriggerScheduledTask(
		c.Request.Context(), c.Param("task_id"), "manual:"+idempotencyKey, time.Now().UTC(),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	if replayed {
		respondReplayed(c, http.StatusOK, item, 0)
		return
	}
	respond(c, http.StatusAccepted, item, 0)
}

func (a *API) listScheduledTaskRuns(c *gin.Context) {
	items, err := a.store.ListScheduledTaskRuns(c.Request.Context(), authenticatedUser(c).ID, c.Param("task_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ScheduledTaskRun]{Items: items}, 0)
}

func (a *API) previewSchedule(c *gin.Context) {
	var request struct {
		ScheduleKind       string   `json:"schedule_kind" binding:"required"`
		ScheduleExpression string   `json:"schedule_expression"`
		Times              []string `json:"times"`
		Weekdays           []int    `json:"weekdays"`
		Timezone           string   `json:"timezone" binding:"required"`
		Count              *int     `json:"count" binding:"omitempty,min=1,max=20"`
	}
	if !bindJSON(c, &request) {
		return
	}
	count := 5
	if request.Count != nil {
		count = *request.Count
	}
	cursor := time.Now().UTC()
	times := make([]time.Time, 0, count)
	for range count {
		next, err := domain.NextScheduledTaskTime(domain.ScheduledTask{ScheduleKind: request.ScheduleKind, ScheduleExpression: request.ScheduleExpression, Timezone: request.Timezone, Times: request.Times, Weekdays: request.Weekdays}, cursor)
		if err != nil {
			respondError(c, err)
			return
		}
		times = append(times, next)
		cursor = next.Add(time.Second)
		if request.ScheduleKind == "once" {
			break
		}
	}
	respond(c, http.StatusOK, gin.H{
		"times": times, "timezone": request.Timezone,
		"dst_note": "触发点按指定 IANA 时区计算，UTC 时间用于持久化",
	}, 0)
}

func (input scheduleInput) calendar() domain.ScheduledTask {
	return domain.ScheduledTask{ScheduleKind: input.ScheduleKind, ScheduleExpression: input.ScheduleExpression,
		Timezone: input.Timezone, Times: input.Times, Weekdays: input.Weekdays, IssueMode: input.IssueMode, IssueID: input.IssueID}
}

func (input scheduleInput) title() string {
	if input.Name == nil {
		return ""
	}
	return strings.TrimSpace(*input.Name)
}

func validateScheduleInput(input scheduleInput) ([]domain.ContentBlock, error) {
	prompt := make([]domain.ContentBlock, len(input.Prompt))
	for index, block := range input.Prompt {
		if block.Text == nil {
			return nil, domain.Invalid("计划任务提示词文本块必须包含 text")
		}
		prompt[index] = domain.ContentBlock{Type: block.Type, Text: *block.Text, Internal: block.Internal}
	}
	if err := validateTextOnlyContent(prompt); err != nil {
		return nil, err
	}
	var body strings.Builder
	for _, block := range prompt {
		body.WriteString(block.Text)
	}
	if strings.TrimSpace(body.String()) == "" {
		return nil, domain.Invalid("计划任务提示词不能为空")
	}
	calendar := input.calendar()
	if err := domain.NormalizeScheduledTaskConfig(&calendar); err != nil {
		return nil, err
	}
	return prompt, nil
}

func nextScheduleTime(kind, expression, timezone string, after time.Time) (time.Time, error) {
	return domain.NextScheduleTime(kind, expression, timezone, after)
}
