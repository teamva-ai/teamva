package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) listProjects(c *gin.Context) {
	computerID := strings.TrimSpace(c.Query("computer_id"))
	if computerID == "" {
		respondError(c, domain.Invalid("computer_id 不能为空"))
		return
	}
	items, err := a.store.ListProjects(c.Request.Context(), authenticatedUser(c).ID, computerID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.Project]{Items: items}, 0)
}

func (a *API) createProject(c *gin.Context) {
	var request struct {
		ComputerID  string `json:"computer_id" binding:"required"`
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
	}
	if !bindJSON(c, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		// 保持设备存在性/归属校验先于落库字段校验。
		if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, request.ComputerID); err != nil {
			respondError(c, err)
			return
		}
		respondError(c, domain.Invalid("项目名称不能为空"))
		return
	}
	item, err := a.store.CreateProject(c.Request.Context(), authenticatedUser(c).ID, domain.Project{
		ComputerID:  request.ComputerID,
		Name:        request.Name,
		Description: request.Description,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) getProject(c *gin.Context) {
	item, err := a.store.GetProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) updateProject(c *gin.Context) {
	var request struct {
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Name != nil && strings.TrimSpace(*request.Name) == "" {
		// UpdateProject 的顺序是归属、版本、默认项目保护，再写入字段；
		// 仅对非法名称提前返回，不能改变这些既有错误的优先级。
		existing, err := a.store.GetProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
		if err != nil {
			respondError(c, err)
			return
		}
		if existing.Version != request.ExpectedVersion {
			respondError(c, domain.Conflict("version_conflict", "资源版本已变化", map[string]any{"current_version": existing.Version}))
			return
		}
		if existing.IsDefault {
			respondError(c, domain.Forbidden("protected_project", "默认项目名称不能修改"))
			return
		}
		respondError(c, domain.Invalid("项目名称不能为空"))
		return
	}
	item, err := a.store.UpdateProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), request.ExpectedVersion, func(project *domain.Project) {
		if request.Name != nil {
			project.Name = strings.TrimSpace(*request.Name)
		}
		if request.Description != nil {
			project.Description = *request.Description
		}
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) deleteProject(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeleteProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func expectedVersionFromQuery(c *gin.Context) (int64, bool) {
	value := c.Query("expected_version")
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version <= 0 {
		respondError(c, domain.Invalid("expected_version 必须是正整数"))
		return 0, false
	}
	return version, true
}
