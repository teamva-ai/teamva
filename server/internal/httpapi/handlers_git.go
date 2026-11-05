package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

// GitRead is intentionally independent of durable command delivery. Git
// status and patch data are ephemeral and must never pass through command
// inboxes or durable command results.
type GitRead interface {
	ListGitRepositories(context.Context, nodeprotocol.GitRepositoriesRequest) (nodeprotocol.GitRepositoryList, error)
	ListGitChanges(context.Context, nodeprotocol.GitChangesRequest) (nodeprotocol.GitChangesPage, error)
	ReadGitDiff(context.Context, nodeprotocol.GitDiffRequest) (nodeprotocol.GitDiffResult, error)
	RefreshGit(context.Context, nodeprotocol.GitRefreshRequest) (nodeprotocol.GitRefreshResult, error)
	SubscribeGitEvents(context.Context, nodeprotocol.GitRepositoriesRequest) (<-chan nodeprotocol.GitEvent, func(), error)
	GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error)
}

func (a *API) listGitRepositories(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resources, ok := a.gitResources(c)
	if !ok {
		return
	}
	refresh, ok := parseOptionalBool(c, "refresh")
	if !ok {
		return
	}
	if len(resources) == 0 {
		writeBoundedGitResponse(c, http.StatusOK, nodeprotocol.GitRepositoryList{
			Repositories: []nodeprotocol.GitRepositorySummary{},
			Warnings:     []nodeprotocol.GitRepositoryWarning{},
		})
		return
	}
	reader, ok := a.gitReader(c)
	if !ok {
		return
	}
	result, err := reader.ListGitRepositories(c.Request.Context(), nodeprotocol.GitRepositoriesRequest{
		ProjectID: c.Param("project_id"), Resources: resources, Refresh: refresh,
	})
	if err != nil {
		respondGitError(c, err)
		return
	}
	writeBoundedGitResponse(c, http.StatusOK, result)
}

func (a *API) listGitChanges(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resources, ok := a.gitResources(c)
	if !ok {
		return
	}
	if len(resources) == 0 {
		respondGitError(c, &nodeprotocol.GitReadError{
			Code: "repository_not_found", Message: "Git 仓库不存在或不属于当前项目",
		})
		return
	}
	limit := 200
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			respondError(c, domain.Invalid("limit 必须在 1 到 500 之间"))
			return
		}
		limit = parsed
	}
	expand, ok := parseOptionalBool(c, "expand_untracked")
	if !ok {
		return
	}
	reader, ok := a.gitReader(c)
	if !ok {
		return
	}
	request := nodeprotocol.GitChangesRequest{
		ProjectID: c.Param("project_id"), Resources: resources,
		RepositoryID: c.Param("repository_id"), Area: strings.TrimSpace(c.Query("area")),
		Cursor: c.Query("cursor"), Limit: limit, Prefix: c.Query("prefix"), ExpandUntracked: expand,
	}
	responseLimited := false
	for {
		result, err := reader.ListGitChanges(c.Request.Context(), request)
		if err != nil {
			respondGitError(c, err)
			return
		}
		if responseLimited {
			result.Truncated = true
			if strings.TrimSpace(result.NextCursor) == "" {
				respondError(c, responseTooLargeError())
				return
			}
		}
		payload, encodeErr := encodeBoundedResponse(c, result, 0, maxGitJSONResponseBytes)
		if encodeErr == nil {
			writeEncodedJSON(c, http.StatusOK, payload)
			return
		}
		if !errors.Is(encodeErr, errJSONResponseBudget) {
			respondError(c, encodeErr)
			return
		}
		if request.Limit <= 1 {
			respondError(c, responseTooLargeError())
			return
		}
		request.Limit = max(1, request.Limit/2)
		responseLimited = true
	}
}

func (a *API) getGitDiff(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resources, ok := a.gitResources(c)
	if !ok {
		return
	}
	if len(resources) == 0 {
		respondGitError(c, &nodeprotocol.GitReadError{
			Code: "repository_not_found", Message: "Git 仓库不存在或不属于当前项目",
		})
		return
	}
	contextLines := 3
	if raw := strings.TrimSpace(c.Query("context")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 20 {
			respondError(c, domain.Invalid("context 必须在 0 到 20 之间"))
			return
		}
		contextLines = parsed
	}
	allowLarge, ok := parseOptionalBool(c, "allow_large")
	if !ok {
		return
	}
	reader, ok := a.gitReader(c)
	if !ok {
		return
	}
	result, err := reader.ReadGitDiff(c.Request.Context(), nodeprotocol.GitDiffRequest{
		ProjectID: c.Param("project_id"), Resources: resources,
		RepositoryID: c.Param("repository_id"), ChangeID: c.Param("change_id"),
		Context: contextLines, AllowLarge: allowLarge,
	})
	if err != nil {
		respondGitError(c, err)
		return
	}
	payload, encodeErr := encodeBoundedResponse(c, result, 0, maxGitJSONResponseBytes)
	if errors.Is(encodeErr, errJSONResponseBudget) {
		result.Patch = ""
		result.Images = nil
		result.Truncated = true
		result.Truncation = &nodeprotocol.GitDiffTruncation{
			Truncated: true, Reason: "response_budget", LimitBytes: maxGitJSONResponseBytes,
		}
		result.ConfirmRequired = true
		result.Message = "差异超过安全响应上限，请缩小文件或使用本地 Git 工具查看"
		payload, encodeErr = encodeBoundedResponse(c, result, 0, maxGitJSONResponseBytes)
	}
	if encodeErr != nil {
		if errors.Is(encodeErr, errJSONResponseBudget) {
			respondError(c, responseTooLargeError())
		} else {
			respondError(c, encodeErr)
		}
		return
	}
	writeEncodedJSON(c, http.StatusOK, payload)
}

func (a *API) refreshGit(c *gin.Context) {
	resources, ok := a.gitResources(c)
	if !ok {
		return
	}
	if len(resources) == 0 {
		respond(c, http.StatusOK, nodeprotocol.GitRefreshResult{Refreshed: true}, 0)
		return
	}
	reader, ok := a.gitReader(c)
	if !ok {
		return
	}
	result, err := reader.RefreshGit(c.Request.Context(), nodeprotocol.GitRefreshRequest{
		ProjectID: c.Param("project_id"), Resources: resources,
	})
	if err != nil {
		respondGitError(c, err)
		return
	}
	respond(c, http.StatusOK, result, 0)
}

func (a *API) streamGitEvents(c *gin.Context) {
	resources, ok := a.gitResources(c)
	if !ok {
		return
	}
	// An empty project is a valid subscription, not a missing repository.
	// Keep its connection healthy; the client reconciles discovery periodically
	// and resubscribes when the set of repositories/resources changes.
	var events <-chan nodeprotocol.GitEvent
	if len(resources) > 0 {
		reader, ok := a.gitReader(c)
		if !ok {
			return
		}
		var unsubscribe func()
		var err error
		events, unsubscribe, err = reader.SubscribeGitEvents(c.Request.Context(), nodeprotocol.GitRepositoriesRequest{
			ProjectID: c.Param("project_id"), Resources: resources,
		})
		if err != nil {
			respondGitError(c, err)
			return
		}
		defer unsubscribe()
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			c.SSEvent("git.changed", event)
			c.Writer.Flush()
		case <-heartbeat.C:
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			c.Writer.Flush()
		}
	}
}

func (a *API) gitResources(c *gin.Context) ([]nodeprotocol.GitResourceRequest, bool) {
	resources, err := a.projectGitResources(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return nil, false
	}
	return resources, true
}

func (a *API) projectGitResources(ctx context.Context, userID, projectID string) ([]nodeprotocol.GitResourceRequest, error) {
	items, err := a.store.ListResources(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	return store.ReadyGitResources(items)
}

func writeBoundedGitResponse(c *gin.Context, status int, data any) bool {
	payload, err := encodeBoundedResponse(c, data, 0, maxGitJSONResponseBytes)
	if err != nil {
		if errors.Is(err, errJSONResponseBudget) {
			respondError(c, responseTooLargeError())
		} else {
			respondError(c, err)
		}
		return false
	}
	writeEncodedJSON(c, status, payload)
	return true
}

func (a *API) gitReader(c *gin.Context) (GitRead, bool) {
	reader, ok := a.runtime.(GitRead)
	if !ok || reader == nil {
		respondGitError(c, &nodeprotocol.GitReadError{
			Code: "capability_unsupported", Message: "当前运行模式不支持项目 Git 只读能力",
		})
		return nil, false
	}
	return reader, true
}

func parseOptionalBool(c *gin.Context, key string) (bool, bool) {
	raw, exists := c.GetQuery(key)
	if !exists || strings.TrimSpace(raw) == "" {
		return false, true
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		respondError(c, domain.Invalid(key+" 必须是布尔值"))
		return false, false
	}
	return value, true
}

func respondGitError(c *gin.Context, err error) {
	respondError(c, gitDomainError(err))
}

func gitDomainError(err error) error {
	var gitError *nodeprotocol.GitReadError
	if !errors.As(err, &gitError) {
		return err
	}
	status := http.StatusUnprocessableEntity
	switch gitError.Code {
	case "invalid_argument", "invalid_cursor":
		status = http.StatusBadRequest
	case "path_out_of_scope":
		status = http.StatusForbidden
	case "repository_not_found", "change_not_found":
		status = http.StatusNotFound
	case "snapshot_expired":
		status = http.StatusConflict
	case "capability_unsupported":
		status = http.StatusNotImplemented
	case "git_timeout":
		status = http.StatusGatewayTimeout
	case "runtime_closed", "git_unavailable", "git_busy":
		status = http.StatusServiceUnavailable
	}
	return &domain.Error{
		Code: gitError.Code, Message: gitError.Message, Retryable: gitError.Retryable, HTTPStatus: status,
	}
}
