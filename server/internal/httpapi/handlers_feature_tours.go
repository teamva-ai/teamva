package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) getFeatureTours(c *gin.Context) {
	view, err := a.store.GetFeatureTours(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, view, 0)
}

func (a *API) markFeatureTourSeen(c *gin.Context) {
	var body map[string]json.RawMessage
	if !bindJSON(c, &body) {
		return
	}
	if body == nil || len(body) != 0 {
		respondError(c, domain.Invalid("请求 JSON 必须是空对象"))
		return
	}
	view, err := a.store.MarkFeatureTourSeen(c.Request.Context(), authenticatedUser(c).ID, c.Param("tour_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, view, 0)
}
