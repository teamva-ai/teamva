package httpapi

import (
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/computeruse"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// getComputerUseStatus 报告本机 open-computer-use 官方 MCP 的检测状态
// （二进制是否安装、版本、npm 是否可用）。仅本地模式提供。
func (a *API) getComputerUseStatus(c *gin.Context) {
	if !a.localComputerUse(c) {
		return
	}
	respond(c, http.StatusOK, computeruse.Detect(), 0)
}

// Configuration is independent of installation and does not start any desktop
// control process. The flag is captured only when a new Run is created.
func (a *API) setComputerUseEnabled(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
		Enabled         *bool `json:"enabled" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.UpdateComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"), request.ExpectedVersion, func(computer *domain.Computer) {
		computer.ComputerUseEnabled = *request.Enabled
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

// installComputerUse 一键 npm 全局安装 open-computer-use（用户显式点击）。
func (a *API) installComputerUse(c *gin.Context) {
	if !a.localComputerUse(c) {
		return
	}
	status, summary, err := computeruse.Install(c.Request.Context())
	if err != nil {
		respondError(c, &domain.Error{
			Code: "computer_use_install_failed", Message: err.Error(),
			HTTPStatus: http.StatusUnprocessableEntity, Retryable: false,
			Details: map[string]any{"output": summary},
		})
		return
	}
	respond(c, http.StatusOK, gin.H{
		"status": status, "output": summary,
	}, 0)
}

// The service is always local. Both browser and Wails use the same ownership
// check; detection/installation does not depend on a Provider Runtime instance.
func (a *API) localComputerUse(c *gin.Context) bool {
	if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id")); err != nil {
		respondError(c, err)
		return false
	}
	return true
}
