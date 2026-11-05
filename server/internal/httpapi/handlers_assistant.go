package httpapi

import (
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// Defaults belong at the HTTP boundary. Repository callers must select a scope
// explicitly, rather than relying on variadic arguments or implicit fallbacks.
func requestedAssistantScope(value domain.AssistantScope) domain.AssistantScope {
	if value == "" {
		return domain.AssistantWorkHistory
	}
	return value
}

func (a *API) getWorkAssistant(c *gin.Context) {
	agent, err := a.store.EnsureGeneralAgent(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, agent, agent.Version)
}

func (a *API) listAssistantSessions(c *gin.Context) {
	scope := requestedAssistantScope(domain.AssistantScope(c.Query("scope")))
	sessions, err := a.store.ListAssistantSessions(c.Request.Context(), authenticatedUser(c).ID, scope)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.AssistantSession]{Items: sessions}, 0)
}

func (a *API) createAssistantSession(c *gin.Context) {
	var request struct {
		Title string                `json:"title"`
		Scope domain.AssistantScope `json:"scope"`
	}
	if !bindJSON(c, &request) {
		return
	}
	session, err := a.store.CreateAssistantSession(c.Request.Context(), authenticatedUser(c).ID, request.Title, requestedAssistantScope(request.Scope))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, session, 0)
}

func (a *API) listAssistantMessages(c *gin.Context) {
	messages, err := a.store.AssistantMessages(c.Request.Context(), authenticatedUser(c).ID, c.Param("session_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.AssistantMessage]{Items: messages}, 0)
}

func (a *API) sendAssistantMessage(c *gin.Context) {
	if a.runtime == nil {
		respondError(c, domain.Invalid("Runtime 未启用"))
		return
	}
	var request domain.AssistantMessageRequest
	if !bindJSON(c, &request) {
		return
	}
	run, err := a.store.SendAssistantMessage(c.Request.Context(), authenticatedUser(c).ID, c.Param("session_id"), c.GetHeader("Idempotency-Key"), request)
	if err != nil {
		respondError(c, err)
		return
	}
	a.runtime.NotifyIssueActivation("")
	respond(c, http.StatusAccepted, run, run.Version)
}
