package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) listApprovals(c *gin.Context) {
	items, err := a.store.ListApprovals(c.Request.Context(), authenticatedUser(c).ID, c.Query("status"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.Approval]{Items: items}, 0)
}

func (a *API) getApproval(c *gin.Context) {
	item, err := a.store.GetApproval(c.Request.Context(), authenticatedUser(c).ID, c.Param("approval_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) decideApproval(c *gin.Context) {
	var request struct {
		ExpectedVersion int64  `json:"expected_version" binding:"required,min=1"`
		ActionDigest    string `json:"action_digest" binding:"required"`
		Decision        string `json:"decision" binding:"required"`
		Scope           string `json:"scope"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.DecideApproval(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("approval_id"), request.ExpectedVersion,
		request.ActionDigest, request.Decision, request.Scope,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) listPolicyRules(c *gin.Context) {
	items, err := a.store.ListPolicyRules(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.PolicyRule]{Items: items}, 0)
}

func (a *API) createPolicyRule(c *gin.Context) {
	var request struct {
		ActionKind string          `json:"action_kind" binding:"required"`
		Effect     string          `json:"effect" binding:"required"`
		Conditions map[string]any  `json:"conditions" binding:"required"`
		ExpiresAt  json.RawMessage `json:"expires_at" jsonnullable:"true"`
	}
	if !bindJSON(c, &request) {
		return
	}
	request.ActionKind = strings.TrimSpace(request.ActionKind)
	if request.ActionKind == "" {
		respondError(c, domain.Invalid("策略 action_kind 不能为空"))
		return
	}
	if request.Effect != "allow" && request.Effect != "deny" && request.Effect != "require_approval" {
		respondError(c, domain.Invalid("不支持的策略 effect"))
		return
	}
	expiresAt, err := parsePolicyRuleExpiresAt(request.ExpiresAt)
	if err != nil {
		respondError(c, err)
		return
	}
	item, err := a.store.CreatePolicyRule(c.Request.Context(), authenticatedUser(c).ID, domain.PolicyRule{
		ProjectID: c.Param("project_id"), ActionKind: request.ActionKind,
		Effect: request.Effect, Conditions: request.Conditions, ExpiresAt: expiresAt,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) updatePolicyRule(c *gin.Context) {
	var request struct {
		Effect          *string         `json:"effect"`
		Conditions      map[string]any  `json:"conditions"`
		ExpiresAt       json.RawMessage `json:"expires_at" jsonnullable:"true"`
		Enabled         *bool           `json:"enabled"`
		ExpectedVersion int64           `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Effect != nil &&
		*request.Effect != "allow" &&
		*request.Effect != "deny" &&
		*request.Effect != "require_approval" {
		respondError(c, domain.Invalid("不支持的策略 effect"))
		return
	}
	expiresAt, err := parsePolicyRuleExpiresAt(request.ExpiresAt)
	if err != nil {
		respondError(c, err)
		return
	}
	item, err := a.store.UpdatePolicyRule(c.Request.Context(), authenticatedUser(c).ID, c.Param("rule_id"), request.ExpectedVersion, func(rule *domain.PolicyRule) {
		if request.Effect != nil {
			rule.Effect = *request.Effect
		}
		if request.Conditions != nil {
			rule.Conditions = request.Conditions
		}
		if request.ExpiresAt != nil {
			rule.ExpiresAt = expiresAt
		}
		if request.Enabled != nil {
			rule.Enabled = *request.Enabled
		}
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func parsePolicyRuleExpiresAt(value json.RawMessage) (*time.Time, error) {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, nil
	}
	var parsed time.Time
	if err := json.Unmarshal(value, &parsed); err != nil {
		return nil, domain.Invalid("expires_at 必须为 RFC3339 时间或 null")
	}
	parsed = parsed.UTC()
	// An offset near the supported year boundary can normalize outside the
	// JSON timestamp range. Reject it before writing rather than failing while
	// encoding a response after a successful database mutation.
	if _, err := parsed.MarshalJSON(); err != nil {
		return nil, domain.Invalid("expires_at 必须为有效的 UTC RFC3339 时间")
	}
	return &parsed, nil
}

func (a *API) deletePolicyRule(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeletePolicyRule(c.Request.Context(), authenticatedUser(c).ID, c.Param("rule_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
