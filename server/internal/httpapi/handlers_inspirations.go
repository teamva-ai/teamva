package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func inspirationQuery(c *gin.Context) (domain.InspirationQuery, bool) {
	q := domain.InspirationQuery{Search: c.Query("search"), Status: c.Query("status")}
	if values, exists := c.Request.URL.Query()["project_id"]; exists {
		// An explicitly empty project ID selects the ungrouped section.
		q.ProjectID = &values[0]
	}
	for _, parameter := range []struct {
		name   string
		target *int
	}{{"limit", &q.Limit}, {"offset", &q.Offset}} {
		if raw, ok := c.GetQuery(parameter.name); ok {
			value, err := strconv.Atoi(raw)
			if err != nil {
				respondError(c, domain.Invalid("分页参数无效"))
				return q, false
			}
			*parameter.target = value
		}
	}
	return q, true
}
func (a *API) listInspirations(c *gin.Context) {
	q, ok := inspirationQuery(c)
	if !ok {
		return
	}
	v, err := a.store.ListInspirations(c.Request.Context(), authenticatedUser(c).ID, q)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, v, 0)
}
func (a *API) inspirationGroups(c *gin.Context) {
	q, ok := inspirationQuery(c)
	if !ok {
		return
	}
	v, err := a.store.InspirationGroups(c.Request.Context(), authenticatedUser(c).ID, q)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, map[string]any{"items": v}, 0)
}
func (a *API) getInspiration(c *gin.Context) {
	v, err := a.store.GetInspiration(c.Request.Context(), authenticatedUser(c).ID, c.Param("inspiration_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, v, v.Version)
}
func (a *API) createInspiration(c *gin.Context) {
	var req struct {
		ProjectID string `json:"project_id"`
		Title     string `json:"title"`
		Markdown  string `json:"markdown"`
	}
	if !bindJSON(c, &req) {
		return
	}
	v, err := a.store.CreateInspiration(c.Request.Context(), authenticatedUser(c).ID, c.GetHeader("Idempotency-Key"), domain.Inspiration{ProjectID: req.ProjectID, Title: req.Title, Markdown: req.Markdown})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, v, v.Version)
}
func (a *API) updateInspiration(c *gin.Context) {
	var req struct {
		domain.InspirationPatch
		ExpectedVersion int64 `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &req) {
		return
	}
	v, err := a.store.UpdateInspiration(c.Request.Context(), authenticatedUser(c).ID, c.Param("inspiration_id"), req.ExpectedVersion, req.InspirationPatch)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, v, v.Version)
}
func (a *API) deleteInspiration(c *gin.Context) {
	version, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeleteInspiration(c.Request.Context(), authenticatedUser(c).ID, c.Param("inspiration_id"), version); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (a *API) downloadInspiration(c *gin.Context) {
	v, err := a.store.GetInspiration(c.Request.Context(), authenticatedUser(c).ID, c.Param("inspiration_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="inspiration.md"`)
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(v.Markdown))
}
