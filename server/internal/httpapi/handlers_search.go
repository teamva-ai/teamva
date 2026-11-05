package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func (a *API) searchProjectContent(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		respondError(c, domain.Invalid("搜索内容不能为空"))
		return
	}
	limit := 30
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			respondError(c, domain.Invalid("limit 必须在 1 到 50 之间"))
			return
		}
		limit = value
	}
	page, err := a.store.SearchProjectContent(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("project_id"), query, limit,
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, page, 0)
}
