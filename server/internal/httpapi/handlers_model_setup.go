package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type modelSetupRuntime interface {
	ModelProviderSettings(context.Context, string) (nodeprotocol.ModelProviderSettings, error)
	SaveModelProvider(context.Context, string, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderView, error)
	TestModelProvider(context.Context, string, nodeprotocol.ModelProviderInput) (nodeprotocol.ModelProviderTestResult, error)
}

// Pointer strings require field presence without replacing Provider validation
// or preset defaults: an explicit empty base_url must still reach the Provider.
type modelProviderInputRequest struct {
	PresetID *string `json:"preset_id" binding:"required"`
	BaseURL  *string `json:"base_url" binding:"required,max=2048"`
	Model    *string `json:"model" binding:"required"`
	APIKey   string  `json:"api_key,omitempty"`
}

func (r modelProviderInputRequest) input() nodeprotocol.ModelProviderInput {
	return nodeprotocol.ModelProviderInput{
		PresetID: *r.PresetID, BaseURL: *r.BaseURL, Model: *r.Model, APIKey: r.APIKey,
	}
}

func (a *API) modelSetupAccess(c *gin.Context) (modelSetupRuntime, bool) {
	c.Header("Cache-Control", "no-store")
	if IsRemoteAccess(c.Request) {
		respondError(c, domain.Forbidden("local_access_required", "请在本机配置大模型 API"))
		return nil, false
	}
	service, ok := a.runtime.(modelSetupRuntime)
	if !ok {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "本机 Runtime 不支持模型配置", HTTPStatus: 501})
		return nil, false
	}
	return service, true
}

func modelSetupRespondError(c *gin.Context, err error) {
	code := "model_provider_unavailable"
	status := http.StatusServiceUnavailable
	var coded interface{ ModelSetupCode() string }
	if errors.As(err, &coded) {
		code = coded.ModelSetupCode()
		if code == "model_provider_invalid" || code == "model_provider_key_required" {
			status = http.StatusBadRequest
		}
	}
	// Never log raw provider/network errors or reflect submitted credential data.
	respondError(c, &domain.Error{Code: code, Message: "无法完成模型供应商配置，请检查配置后重试", HTTPStatus: status, Retryable: status >= 500})
}

func (a *API) getModelProviderSettings(c *gin.Context) {
	service, ok := a.modelSetupAccess(c)
	if !ok {
		return
	}
	result, err := service.ModelProviderSettings(c.Request.Context(), c.Param("provider"))
	if err != nil {
		modelSetupRespondError(c, err)
		return
	}
	respond(c, http.StatusOK, result, 0)
}

func (a *API) saveModelProvider(c *gin.Context) {
	service, ok := a.modelSetupAccess(c)
	if !ok {
		return
	}
	var request modelProviderInputRequest
	if !bindJSON(c, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 50*time.Second)
	defer cancel()
	result, err := service.SaveModelProvider(ctx, c.Param("provider"), request.input())
	if err != nil {
		modelSetupRespondError(c, err)
		return
	}
	a.audit(c, authenticatedUser(c).ID, "model_provider.configured", map[string]any{"provider": c.Param("provider"), "preset_id": result.PresetID})
	respond(c, http.StatusOK, result, 0)
}

func (a *API) testModelProvider(c *gin.Context) {
	service, ok := a.modelSetupAccess(c)
	if !ok {
		return
	}
	var request modelProviderInputRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := service.TestModelProvider(c.Request.Context(), c.Param("provider"), request.input())
	if err != nil {
		modelSetupRespondError(c, err)
		return
	}
	respond(c, http.StatusOK, result, 0)
}
