package httpapi

import (
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) getConversationGoal(c *gin.Context) {
	goal, err := a.store.GetCurrentConversationGoal(c.Request.Context(), authenticatedUser(c).ID, c.Param("node_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"goal": goal}, 0)
}

func (a *API) createConversationGoal(c *gin.Context) {
	var request struct {
		Objective               string `json:"objective"`
		TokenBudget             *int64 `json:"token_budget" jsonnullable:"true"`
		ExpectedAgentID         string `json:"expected_agent_id"`
		ExpectedAgentRevisionID string `json:"expected_agent_revision_id"`
	}
	if !bindJSON(c, &request) {
		return
	}
	goal, replayed, err := a.store.CreateConversationGoal(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("node_id"), c.GetHeader("Idempotency-Key"), request.Objective, request.TokenBudget,
		domain.ConversationAgentBinding{ExpectedAgentID: request.ExpectedAgentID, ExpectedAgentRevisionID: request.ExpectedAgentRevisionID})
	if err != nil {
		respondError(c, err)
		return
	}
	if replayed {
		respondReplayed(c, http.StatusOK, goal, goal.Version)
		return
	}
	respond(c, http.StatusAccepted, goal, goal.Version)
}

func (a *API) changeConversationGoal(c *gin.Context) {
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	goal, err := a.store.ChangeConversationGoal(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("node_id"), c.Param("goal_id"), request.ExpectedVersion, c.Param("action"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, goal, goal.Version)
}
