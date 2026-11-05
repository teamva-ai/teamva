package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// MCPManager is deliberately outside the durable command/ToolService boundary:
// private configuration and OAuth credentials must never enter SQLite or events.
type MCPManager interface {
	List(context.Context, string) ([]mcpmanager.View, error)
	Save(context.Context, mcpmanager.Input) (mcpmanager.View, error)
	Delete(context.Context, string, string, string, int64) error
	Connect(context.Context, string, string, string) (mcpmanager.View, error)
	Disconnect(context.Context, string, string, string) (mcpmanager.View, error)
	StartAuth(context.Context, string, string, string) (mcpmanager.AuthResult, error)
	RevokeAuth(context.Context, string, string, string) (mcpmanager.View, error)
}

func (a *API) mcpAccess(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if IsRemoteAccess(c.Request) || !IsLoopbackRemote(c.Request.RemoteAddr) || authenticatedUser(c).ID != a.localUserID {
		respondError(c, domain.Forbidden("local_access_required", "请在本机管理 MCP"))
		return false
	}
	if a.mcp == nil {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "MCP 管理不可用", HTTPStatus: http.StatusNotImplemented})
		return false
	}
	return true
}

func (a *API) mcpProject(c *gin.Context, scope, projectID string) bool {
	if scope != "user" && scope != "project" || scope == "user" && projectID != "" || scope == "project" && projectID == "" {
		respondError(c, &domain.Error{Code: "mcp_invalid", Message: "请选择有效的 MCP 作用域", HTTPStatus: http.StatusBadRequest})
		return false
	}
	if projectID != "" {
		if _, err := a.store.GetProject(c.Request.Context(), authenticatedUser(c).ID, projectID); err != nil {
			respondError(c, err)
			return false
		}
	}
	return true
}

func respondMCPError(c *gin.Context, err error) {
	code, status := "mcp_unavailable", http.StatusServiceUnavailable
	switch {
	case errors.Is(err, mcpmanager.ErrNotFound):
		code, status = "not_found", http.StatusNotFound
	case errors.Is(err, mcpmanager.ErrConflict):
		code, status = "version_conflict", http.StatusConflict
	case errors.Is(err, mcpmanager.ErrInvalid):
		code, status = "mcp_invalid", http.StatusBadRequest
	}
	// Raw errors can contain subprocess output, remote URLs or credentials.
	respondError(c, &domain.Error{Code: code, Message: "无法完成 MCP 操作，请检查配置或刷新后重试", HTTPStatus: status, Retryable: status >= 500})
}

func (a *API) listMCPServers(c *gin.Context) {
	if !a.mcpAccess(c) {
		return
	}
	projectID := c.Query("project_id")
	if projectID != "" && !a.mcpProject(c, "project", projectID) {
		return
	}
	items, err := a.mcp.List(c.Request.Context(), projectID)
	if err != nil {
		respondMCPError(c, err)
		return
	}
	if items == nil {
		items = []mcpmanager.View{}
	}
	respond(c, http.StatusOK, gin.H{"items": items}, 0)
}

// Keep HTTP field presence separate from the private Manager's native CAS:
// version zero is an explicit create token, not a missing request field.
type mcpServerRequest struct {
	Name      string          `json:"name" binding:"required"`
	Scope     string          `json:"scope" binding:"required"`
	ProjectID string          `json:"project_id,omitempty"`
	Version   *int64          `json:"version" binding:"required,min=0"`
	Config    json.RawMessage `json:"config" binding:"required"`
}

func (a *API) saveMCPServer(c *gin.Context) {
	if !a.mcpAccess(c) {
		return
	}
	var request mcpServerRequest
	if !bindJSON(c, &request) || !a.mcpProject(c, request.Scope, request.ProjectID) {
		return
	}
	input := mcpmanager.Input{
		Name: request.Name, Scope: request.Scope, ProjectID: request.ProjectID,
		Version: *request.Version, Config: request.Config,
	}
	item, err := a.mcp.Save(c.Request.Context(), input)
	if err != nil {
		respondMCPError(c, err)
		return
	}
	respond(c, http.StatusOK, item, 0)
}

func (a *API) deleteMCPServer(c *gin.Context) {
	if !a.mcpAccess(c) {
		return
	}
	scope, projectID := c.Query("scope"), c.Query("project_id")
	if !a.mcpProject(c, scope, projectID) {
		return
	}
	version, err := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || version <= 0 {
		respondMCPError(c, mcpmanager.ErrInvalid)
		return
	}
	if err := a.mcp.Delete(c.Request.Context(), scope, projectID, c.Param("name"), version); err != nil {
		respondMCPError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"deleted": true}, 0)
}

type mcpTarget struct {
	Scope     string `json:"scope"`
	ProjectID string `json:"project_id"`
	// Optional page snapshot only; actions continue to target the saved entry,
	// not a new compare-and-swap operation against this version.
	Version *int64 `json:"version,omitempty" binding:"omitempty,min=1"`
}

func (a *API) mcpAction(c *gin.Context, action string) {
	if !a.mcpAccess(c) {
		return
	}
	var target mcpTarget
	if !bindJSON(c, &target) || !a.mcpProject(c, target.Scope, target.ProjectID) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	var value any
	var err error
	switch action {
	case "connect":
		value, err = a.mcp.Connect(ctx, target.Scope, target.ProjectID, c.Param("name"))
	case "disconnect":
		value, err = a.mcp.Disconnect(ctx, target.Scope, target.ProjectID, c.Param("name"))
	case "auth":
		value, err = a.mcp.StartAuth(ctx, target.Scope, target.ProjectID, c.Param("name"))
	case "revoke":
		value, err = a.mcp.RevokeAuth(ctx, target.Scope, target.ProjectID, c.Param("name"))
	}
	if err != nil {
		respondMCPError(c, err)
		return
	}
	respond(c, http.StatusOK, value, 0)
}

func (a *API) connectMCPServer(c *gin.Context)    { a.mcpAction(c, "connect") }
func (a *API) disconnectMCPServer(c *gin.Context) { a.mcpAction(c, "disconnect") }
func (a *API) authMCPServer(c *gin.Context)       { a.mcpAction(c, "auth") }
func (a *API) revokeMCPServer(c *gin.Context)     { a.mcpAction(c, "revoke") }
