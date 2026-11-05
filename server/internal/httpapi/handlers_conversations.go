package httpapi

import (
	"net/http"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) listConversationGraphs(c *gin.Context) {
	graphs := a.store
	items, err := graphs.ListConversationGraphs(c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ConversationGraph]{Items: items}, 0)
}

func (a *API) createConversationGraph(c *gin.Context) {
	graphs := a.store
	var request struct {
		Title     string `json:"title"`
		AgentID   string `json:"agent_id"`
		NodeTitle string `json:"node_title"`
	}
	if !bindJSON(c, &request) {
		return
	}
	detail, err := graphs.CreateConversationGraph(c.Request.Context(), authenticatedUser(c).ID,
		domain.ConversationGraph{ProjectID: c.Param("project_id"), Title: request.Title},
		domain.ConversationNode{AgentID: request.AgentID, Title: request.NodeTitle},
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, detail, detail.Graph.Version)
}

func (a *API) initializeConversationGraphRoot(c *gin.Context) {
	var request struct {
		AgentID         string `json:"agent_id" binding:"required"`
		ExpectedVersion int64  `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	detail, err := a.store.InitializeConversationGraphRoot(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("graph_id"), request.AgentID, request.ExpectedVersion)
	if err != nil {
		respondError(c, err)
		return
	}
	for _, node := range detail.Nodes {
		if node.ParentID == "" {
			respond(c, http.StatusOK, node, node.Version)
			return
		}
	}
	respondError(c, domain.NotFound("根节点", c.Param("graph_id")))
}

func (a *API) updateConversationGraph(c *gin.Context) {
	var request struct {
		Title           *string `json:"title" binding:"required"`
		ExpectedVersion int64   `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := a.store.UpdateConversationGraphTitle(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("graph_id"), request.ExpectedVersion, *request.Title)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) getConversationGraph(c *gin.Context) {
	graphs := a.store
	detail, err := graphs.GetConversationGraphDetail(c.Request.Context(), authenticatedUser(c).ID, c.Param("graph_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, detail, detail.Graph.Version)
}

func (a *API) deleteConversationGraph(c *gin.Context) {
	graphs := a.store
	version, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := graphs.DeleteConversationGraph(c.Request.Context(), authenticatedUser(c).ID, c.Param("graph_id"), version); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) getConversationNode(c *gin.Context) {
	graphs := a.store
	item, err := graphs.GetConversationNode(c.Request.Context(), authenticatedUser(c).ID, c.Param("node_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) updateConversationNode(c *gin.Context) {
	graphs := a.store
	var request struct {
		ExpectedVersion int64    `json:"expected_version" binding:"required,min=1"`
		Title           *string  `json:"title"`
		AgentID         *string  `json:"agent_id"`
		PositionX       *float64 `json:"position_x"`
		PositionY       *float64 `json:"position_y"`
		Width           *float64 `json:"width"`
		Height          *float64 `json:"height"`
	}
	if !bindJSON(c, &request) {
		return
	}
	if request.Width != nil && (*request.Width < 300 || *request.Width > 1000) {
		respondError(c, domain.Invalid("节点宽度需要在 300 到 1000 之间"))
		return
	}
	if request.Height != nil && (*request.Height < 260 || *request.Height > 1600) {
		respondError(c, domain.Invalid("节点高度需要在 260 到 1600 之间"))
		return
	}
	item, err := graphs.UpdateConversationNode(c.Request.Context(), authenticatedUser(c).ID, c.Param("node_id"),
		request.ExpectedVersion, func(node *domain.ConversationNode) {
			if request.Title != nil {
				node.Title = strings.TrimSpace(*request.Title)
				node.TitleEdited = true
			}
			if request.AgentID != nil {
				node.AgentID = strings.TrimSpace(*request.AgentID)
			}
			if request.PositionX != nil {
				node.PositionX = *request.PositionX
			}
			if request.PositionY != nil {
				node.PositionY = *request.PositionY
			}
			if request.Width != nil {
				node.Width = *request.Width
			}
			if request.Height != nil {
				node.Height = *request.Height
			}
		})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) deleteConversationNode(c *gin.Context) {
	graphs := a.store
	version, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := graphs.DeleteConversationNode(c.Request.Context(), authenticatedUser(c).ID, c.Param("node_id"), version); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) forkConversationNode(c *gin.Context) {
	graphs := a.store
	var request struct {
		MessageID string `json:"message_id" binding:"required"`
		AgentID   string `json:"agent_id"`
		Title     string `json:"title"`
	}
	if !bindJSON(c, &request) {
		return
	}
	item, err := graphs.ForkConversationNode(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("node_id"), request.MessageID,
		domain.ConversationNode{AgentID: request.AgentID, Title: request.Title},
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) listConversationMessages(c *gin.Context) {
	graphs := a.store
	items, err := graphs.ListConversationMessages(c.Request.Context(), authenticatedUser(c).ID, c.Param("node_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ConversationMessage]{Items: items}, 0)
}

func (a *API) createConversationMessage(c *gin.Context) {
	runs := a.store
	var request struct {
		Body                    string `json:"body" binding:"required"`
		ExpectedAgentID         string `json:"expected_agent_id"`
		ExpectedAgentRevisionID string `json:"expected_agent_revision_id"`
	}
	if !bindJSON(c, &request) {
		return
	}
	// Admission, both messages, Run and outbox are a single idempotent transaction.
	item, replayed, err := runs.SendConversationMessage(c.Request.Context(), authenticatedUser(c).ID,
		c.Param("node_id"), c.GetHeader("Idempotency-Key"), request.Body, domain.ConversationAgentBinding{
			ExpectedAgentID: request.ExpectedAgentID, ExpectedAgentRevisionID: request.ExpectedAgentRevisionID,
		})
	if err != nil {
		respondError(c, err)
		return
	}
	if replayed {
		respondReplayed(c, http.StatusCreated, item, item.Version)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}
