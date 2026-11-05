package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

const (
	sessionIdleTTL          = 30 * 24 * time.Hour
	sessionRotationInterval = 15 * time.Minute
	sessionPreviousTokenTTL = 30 * time.Second
	deviceCookieName        = "am_device"
	sessionCookieName       = "am_session"
)

func randomDeviceID() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *API) audit(c *gin.Context, userID, action string, details map[string]any) {
	_ = a.store.AppendAuditEvent(c.Request.Context(), domain.AuditEvent{
		UserID: userID, ActorType: "user", ActorRef: userID,
		Action: action, Outcome: "succeeded", Details: details, TraceID: requestID(c),
	})
}

// Session metadata uses a random browser identifier, never a machine fingerprint.
// This service has no trusted reverse proxy; forwarded client IPs are ignored.
func (a *API) sessionMetaFromRequest(c *gin.Context) (domain.SessionMeta, error) {
	deviceID, err := a.ensureBrowserDeviceID(c)
	if err != nil {
		return domain.SessionMeta{}, err
	}
	return domain.SessionMeta{
		DeviceID: deviceID, IPHMAC: a.store.HMACValue(c.ClientIP()),
		UserAgentHMAC: a.store.HMACValue(strings.TrimSpace(c.GetHeader("User-Agent"))),
	}, nil
}

func (a *API) ensureBrowserDeviceID(c *gin.Context) (string, error) {
	if cookie, err := c.Cookie(deviceCookieName); err == nil && strings.TrimSpace(cookie) != "" {
		return strings.TrimSpace(cookie), nil
	}
	deviceID, err := randomDeviceID()
	if err != nil {
		return "", err
	}
	a.setBrowserCookie(c, deviceCookieName, deviceID, 365*24*3600)
	return deviceID, nil
}

func (a *API) getCSRFToken(c *gin.Context) {
	csrf, err := a.store.GetSessionCSRF(c.Request.Context(), sessionID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"csrf_token": csrf}, 0)
}

func (a *API) getAuthBootstrap(c *gin.Context) {
	csrf, err := a.store.GetSessionCSRF(c.Request.Context(), sessionID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	user := authenticatedUser(c)
	respond(c, http.StatusOK, gin.H{"csrf_token": csrf, "user": user}, user.Version)
}

func (a *API) getMe(c *gin.Context) {
	user := authenticatedUser(c)
	respond(c, http.StatusOK, user, user.Version)
}

func (a *API) listComputers(c *gin.Context) {
	items, err := a.store.ListComputers(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.Computer]{Items: items}, 0)
}

func (a *API) getComputer(c *gin.Context) {
	item, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) listAgentServices(c *gin.Context) {
	items, err := a.store.ListAgentServices(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.AgentService]{Items: items}, 0)
}

func (a *API) probeAgentService(c *gin.Context) {
	item, err := a.store.RequestAgentProbe(c.Request.Context(), authenticatedUser(c).ID, c.Param("service_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	if prober, ok := a.runtime.(interface{ Probe(context.Context) error }); ok {
		if err := prober.Probe(c.Request.Context()); err != nil {
			respondError(c, err)
			return
		}
		services, err := a.store.ListAgentServices(c.Request.Context(), authenticatedUser(c).ID, item.ComputerID)
		if err != nil {
			respondError(c, err)
			return
		}
		for _, service := range services {
			if service.ID == item.ID {
				item = service
				break
			}
		}
	}
	respond(c, http.StatusAccepted, item, item.Version)
}

func (a *API) diagnoseProgrammingTool(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if _, supported := catalog.Lookup(provider); !supported {
		respondError(c, domain.Invalid("暂不支持诊断该编程工具"))
		return
	}
	if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id")); err != nil {
		respondError(c, err)
		return
	}
	diagnoser, ok := a.runtime.(interface {
		DiagnoseProvider(context.Context, string) (nodeprotocol.ProviderDiagnostic, error)
	})
	if !ok {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "本地 Runtime 不支持工具诊断", HTTPStatus: http.StatusNotImplemented})
		return
	}
	item, err := diagnoser.DiagnoseProvider(c.Request.Context(), provider)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, 0)
}

func (a *API) recycleIdleProgrammingTool(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if _, supported := catalog.Lookup(provider); !supported {
		respondError(c, domain.Invalid("暂不支持回收该编程工具"))
		return
	}
	if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id")); err != nil {
		respondError(c, err)
		return
	}
	recycler, ok := a.runtime.(interface {
		RecycleIdleProvider(context.Context, string) (nodeprotocol.ProviderRuntimeRecycleResult, error)
	})
	if !ok {
		respondError(c, &domain.Error{Code: "capability_unsupported", Message: "本地 Runtime 不支持空闲实例回收", HTTPStatus: http.StatusNotImplemented})
		return
	}
	item, err := recycler.RecycleIdleProvider(c.Request.Context(), provider)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, 0)
}

func (a *API) listAuditEvents(c *gin.Context) {
	items, err := a.store.ListAuditEvents(c.Request.Context(), authenticatedUser(c).ID, c.Query("project_id"), c.Query("run_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.AuditEvent]{Items: items}, 0)
}
