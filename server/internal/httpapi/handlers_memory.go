package httpapi

import (
	"net/http"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) listMemories(c *gin.Context) {
	archived, ok := parseOptionalBool(c, "archived")
	if !ok {
		return
	}
	list := a.store.ListMemories
	if archived {
		list = a.store.ListArchivedMemories
	}
	items, err := list(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ProjectMemoryEntry]{Items: items}, 0)
}

func (a *API) createMemory(c *gin.Context) {
	var request struct {
		Key        string `json:"key" binding:"required"`
		Kind       string `json:"kind" binding:"required"`
		Title      string `json:"title"`
		Content    string `json:"content" binding:"required"`
		SourceType string `json:"source_type"`
		SourceRef  string `json:"source_ref"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.SourceType == "" {
		request.SourceType = "user"
	}
	request.Key = strings.TrimSpace(request.Key)
	request.Title = strings.TrimSpace(request.Title)
	if request.Key == "" || strings.TrimSpace(request.Kind) == "" || strings.TrimSpace(request.SourceType) == "" {
		if _, err := a.store.GetProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id")); err != nil {
			respondError(c, err)
			return
		}
		respondError(c, domain.Invalid("记忆 key、kind 和 source_type 不能为空"))
		return
	}
	item, err := a.store.CreateMemory(c.Request.Context(), authenticatedUser(c).ID, domain.ProjectMemoryEntry{
		ProjectID: c.Param("project_id"), Key: request.Key, Kind: request.Kind,
		Title: request.Title, Content: request.Content,
		SourceType: request.SourceType, SourceRef: request.SourceRef,
	}, "user")
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) getMemory(c *gin.Context) {
	item, err := a.store.GetMemory(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), c.Param("memory_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) updateMemory(c *gin.Context) {
	var request struct {
		Title           *string `json:"title"`
		Content         string  `json:"content" binding:"required"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	title := ""
	if request.Title != nil {
		title = strings.TrimSpace(*request.Title)
	}
	if request.Title != nil && title == "" {
		existing, err := a.store.GetMemory(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), c.Param("memory_id"))
		if err != nil {
			respondError(c, err)
			return
		}
		if existing.DeletedAt != nil {
			respondError(c, domain.NotFound("项目记忆", existing.ID))
			return
		}
		if existing.Version != request.ExpectedVersion {
			respondError(c, domain.Conflict("version_conflict", "资源版本已变化", map[string]any{"current_version": existing.Version}))
			return
		}
		respondError(c, domain.Invalid("记忆标题不能为空"))
		return
	}
	item, err := a.store.UpdateMemory(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("project_id"), c.Param("memory_id"),
		request.ExpectedVersion, title, request.Content, "user", request.Title != nil,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) archiveMemory(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if err := a.store.ArchiveMemory(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), c.Param("memory_id"), request.ExpectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) restoreMemory(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.RestoreMemory(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), c.Param("memory_id"), request.ExpectedVersion)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) listMemoryVersions(c *gin.Context) {
	items, err := a.store.ListMemoryVersions(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), c.Param("memory_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ProjectMemoryVersion]{Items: items}, 0)
}
