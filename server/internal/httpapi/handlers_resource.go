package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) listResources(c *gin.Context) {
	items, err := a.store.ListResources(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ProjectResource]{Items: items}, 0)
}

func (a *API) createResource(c *gin.Context) {
	var request struct {
		Kind string `json:"kind" binding:"required"`
		Name string `json:"name"`
		Path string `json:"path"`
		URL  string `json:"url"`
	}
	if !bindJSON(c, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if len([]rune(request.Name)) > resourceNameMaxRunes {
		respondError(c, domain.Invalid("资源名称为空或过长"))
		return
	}
	switch request.Kind {
	case "code_directory", "document_directory":
		if request.Name == "" {
			respondError(c, domain.Invalid("资源名称为空或过长"))
			return
		}
		request.Path = strings.TrimSpace(request.Path)
		if request.Path == "" {
			respondError(c, domain.Invalid("请选择本地目录"))
			return
		}
		if !isAbsoluteResourcePath(request.Path) {
			respondError(c, domain.Invalid("本地目录必须使用绝对路径"))
			return
		}
	case "url":
		request.URL = strings.TrimSpace(request.URL)
		parsed, err := url.Parse(request.URL)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			respondError(c, domain.Invalid("网址资源必须使用不含凭据的 HTTP(S) URL"))
			return
		}
		if request.Path != "" {
			respondError(c, domain.Invalid("网址资源不能指定本地路径"))
			return
		}
	default:
		respondError(c, domain.Invalid("不支持的资源类型"))
		return
	}
	if request.Kind == "url" && request.Name == "" {
		// Authorize before any DNS lookup or connection. CreateResource repeats
		// ownership validation atomically when inserting, after the fetch.
		if _, err := a.store.GetProject(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id")); err != nil {
			respondError(c, err)
			return
		}
		fetcher := a.resourceTitles
		if fetcher == nil {
			fetcher = newResourceTitleFetcher()
		}
		request.Name = fallbackResourceName(request.URL)
		if title, err := fetcher.fetch(c.Request.Context(), request.URL); err == nil && title != "" {
			request.Name = title
		}
	}
	// A fetch timeout is best-effort, but cancellation of the creation request
	// itself must never turn into a fallback insert.
	if c.Request.Context().Err() != nil {
		c.Status(http.StatusRequestTimeout)
		return
	}
	item, err := a.store.CreateResource(c.Request.Context(), authenticatedUser(c).ID, domain.ProjectResource{
		ProjectID: c.Param("project_id"),
		Kind:      request.Kind,
		Name:      strings.TrimSpace(request.Name),
		Path:      request.Path,
		URL:       request.URL,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func isAbsoluteResourcePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func (a *API) updateResource(c *gin.Context) {
	var request struct {
		Name            *string `json:"name"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if !a.requireResourceInProject(c) {
		return
	}
	if request.Name != nil && (strings.TrimSpace(*request.Name) == "" || len([]rune(*request.Name)) > 200) {
		respondError(c, domain.Invalid("资源名称为空或过长"))
		return
	}
	item, err := a.store.UpdateResource(c.Request.Context(), authenticatedUser(c).ID, c.Param("resource_id"), request.ExpectedVersion, func(resource *domain.ProjectResource) {
		if request.Name != nil {
			resource.Name = strings.TrimSpace(*request.Name)
		}
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) deleteResource(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if !a.requireResourceInProject(c) {
		return
	}
	if err := a.store.DeleteResource(c.Request.Context(), authenticatedUser(c).ID, c.Param("resource_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) validateResource(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if !a.requireResourceInProject(c) {
		return
	}
	item, err := a.store.RequestResourceValidation(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("resource_id"), request.ExpectedVersion,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	a.notifyQueuedCommands()
	respond(c, http.StatusAccepted, item, item.Version)
}

func (a *API) requireResourceInProject(c *gin.Context) bool {
	items, err := a.store.ListResources(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return false
	}
	for _, item := range items {
		if item.ID == c.Param("resource_id") {
			return true
		}
	}
	respondError(c, domain.NotFound("项目资源", c.Param("resource_id")))
	return false
}

func (a *API) listResourceOpeners(c *gin.Context) {
	if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id")); err != nil {
		respondError(c, err)
		return
	}
	executor, ok := a.runtime.(localCommandExecutor)
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "当前运行模式无法直接打开该计算机上的目录",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	result, err := executor.ExecuteLocalCommand(c.Request.Context(), "resource.openers", map[string]any{})
	if err != nil {
		respondError(c, err)
		return
	}
	var openers any
	if err := json.Unmarshal(result, &openers); err != nil {
		respondError(c, errors.New("resource openers returned invalid JSON"))
		return
	}
	respond(c, http.StatusOK, openers, 0)
}

func (a *API) openResource(c *gin.Context) {
	var request struct {
		OpenerID string `json:"opener_id" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	userID := authenticatedUser(c).ID
	project, err := a.store.GetProject(c.Request.Context(), userID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	items, err := a.store.ListResources(c.Request.Context(), userID, project.ID)
	if err != nil {
		respondError(c, err)
		return
	}
	var resource *domain.ProjectResource
	for index := range items {
		if items[index].ID == c.Param("resource_id") {
			resource = &items[index]
			break
		}
	}
	if resource == nil {
		respondError(c, domain.NotFound("项目资源", c.Param("resource_id")))
		return
	}
	if resource.Kind != "code_directory" || !isAbsoluteResourcePath(resource.Path) {
		respondError(c, domain.Invalid("只有已登记的代码目录可以用本机应用打开"))
		return
	}
	executor, ok := a.runtime.(localCommandExecutor)
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "当前运行模式无法直接打开该计算机上的目录",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	result, err := executor.ExecuteLocalCommand(c.Request.Context(), "resource.open", map[string]any{
		"path": resource.Path, "opener_id": strings.TrimSpace(request.OpenerID),
	})
	if err != nil {
		respondError(c, &domain.Error{
			Code: "resource_open_failed", Message: err.Error(), HTTPStatus: http.StatusUnprocessableEntity,
		})
		return
	}
	var opened any
	if err := json.Unmarshal(result, &opened); err != nil {
		respondError(c, errors.New("resource open returned invalid JSON"))
		return
	}
	respond(c, http.StatusOK, opened, 0)
}
