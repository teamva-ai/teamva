package httpapi

import (
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) getCurrentIssueAgentGoal(c *gin.Context) {
	repository := a.store
	goal, found, err := repository.GetCurrentIssueAgentGoal(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("instance_id"),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	if !found {
		respond(c, http.StatusOK, gin.H{"goal": nil}, 0)
		return
	}
	respond(c, http.StatusOK, gin.H{"goal": goal}, goal.Version)
}

func (a *API) pauseIssueAgentGoal(c *gin.Context) {
	a.changeIssueAgentGoal(c, "pause")
}

func (a *API) resumeIssueAgentGoal(c *gin.Context) {
	a.changeIssueAgentGoal(c, "resume")
}

func (a *API) cancelIssueAgentGoal(c *gin.Context) {
	a.changeIssueAgentGoal(c, "cancel")
}

func (a *API) changeIssueAgentGoal(c *gin.Context, action string) {
	repository := a.store
	var request struct {
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	var (
		goal domain.IssueAgentGoal
		err  error
	)
	switch action {
	case "pause":
		goal, err = repository.PauseIssueAgentGoal(c.Request.Context(), authenticatedUser(c).ID,
			c.Param("goal_id"), request.ExpectedVersion)
	case "resume":
		goal, err = repository.ResumeIssueAgentGoal(c.Request.Context(), authenticatedUser(c).ID,
			c.Param("goal_id"), request.ExpectedVersion)
	case "cancel":
		goal, err = repository.CancelIssueAgentGoal(c.Request.Context(), authenticatedUser(c).ID,
			c.Param("goal_id"), request.ExpectedVersion)
	}
	if err != nil {
		respondError(c, err)
		return
	}
	if a.runtime != nil {
		a.runtime.NotifyIssueActivation(goal.IssueID)
	}
	respond(c, http.StatusOK, goal, goal.Version)
}
