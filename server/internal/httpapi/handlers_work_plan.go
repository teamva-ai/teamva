package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type workPlanRuntime interface {
	GenerateWorkPlan(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error)
}

// Draft generation never creates a business Run, comment, mention or instance.
func (a *API) generateIssueWorkPlan(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var request struct {
		AgentID  string   `json:"agent_id" binding:"required"`
		Input    string   `json:"input" binding:"required"`
		AgentIDs []string `json:"agent_ids" binding:"required,min=1,max=64"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Input) == "" || len(request.Input) > 65536 || !utf8.ValidString(request.Input) {
		respondError(c, domain.Invalid("分工需求不能为空或超过 64 KiB"))
		return
	}
	ctx := c.Request.Context()
	userID := authenticatedUser(c).ID
	issue, err := a.store.GetIssue(ctx, userID, c.Param("issue_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	project, err := a.store.GetProject(ctx, userID, issue.ProjectID)
	if err != nil {
		respondError(c, err)
		return
	}
	agent, err := a.store.GetAgent(ctx, userID, request.AgentID)
	if err != nil {
		respondError(c, err)
		return
	}
	if agent.ComputerID != project.ComputerID || agent.Status != domain.AgentActive || agent.AgentServiceID == "" || agent.CurrentRevisionID == "" {
		respondError(c, domain.Invalid("请选择当前项目电脑上已配置并启用的整理 Agent"))
		return
	}
	revision, err := a.store.GetAgentRevision(ctx, userID, agent.CurrentRevisionID)
	if err != nil {
		respondError(c, err)
		return
	}
	if revision.AgentID != agent.ID {
		respondError(c, domain.Invalid("整理 Agent 的配置修订不匹配"))
		return
	}
	services, err := a.store.ListAgentServices(ctx, userID, project.ComputerID)
	if err != nil {
		respondError(c, err)
		return
	}
	var provider string
	for _, service := range services {
		if service.ID == revision.AgentServiceID && service.ID == agent.AgentServiceID && service.Status == "ready" && service.LoginStatus == "authenticated" {
			provider = service.Provider
			break
		}
	}
	definition, found := catalog.Lookup(provider)
	if !found || !definition.Metadata.Supports("no_tools") {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "该 Agent 暂不支持安全的禁工具整理；请手动分工或明确选择其他整理 Agent", HTTPStatus: http.StatusUnprocessableEntity})
		return
	}
	allowed := make([]nodeprotocol.WorkPlanAgent, 0, len(request.AgentIDs))
	seen := make(map[string]bool, len(request.AgentIDs))
	for _, id := range request.AgentIDs {
		if seen[id] {
			respondError(c, domain.Invalid("分工 Agent 不能重复选择"))
			return
		}
		seen[id] = true
		target, err := a.store.GetAgent(ctx, userID, id)
		if err != nil {
			respondError(c, err)
			return
		}
		if target.ComputerID != project.ComputerID || target.Status != domain.AgentActive || target.AgentServiceID == "" || target.CurrentRevisionID == "" {
			respondError(c, domain.Invalid("分工目标必须是当前项目电脑上已配置并启用的 Agent"))
			return
		}
		allowed = append(allowed, nodeprotocol.WorkPlanAgent{ID: target.ID, Name: target.Name})
	}
	planRequest := nodeprotocol.WorkPlanRequest{Provider: provider, Model: revision.Model, Input: request.Input, Agents: allowed}
	if _, err := nodeprotocol.WorkPlanInput(planRequest); err != nil {
		respondError(c, domain.Invalid("整理需求和 Agent 名单的 JSON 编码总量不能超过 64 KiB，且不能包含无效文本"))
		return
	}
	runtime, ok := a.runtime.(workPlanRuntime)
	if !ok {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "当前 Runtime 不支持分工整理，可继续手动填写", HTTPStatus: http.StatusNotImplemented})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := runtime.GenerateWorkPlan(ctx, planRequest)
	if err != nil || nodeprotocol.ValidateWorkPlanItems(result.WorkItems, allowed) != nil {
		// Provider errors can contain prompts or authentication data.
		respondError(c, &domain.Error{Code: "work_plan_failed", Message: "无法整理分工，请检查模型配置后重试，或继续手动填写", HTTPStatus: http.StatusServiceUnavailable, Retryable: true})
		return
	}
	respond(c, http.StatusOK, result, 0)
}
