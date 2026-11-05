package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func issueListQuery(c *gin.Context) (store.IssueListQuery, error) {
	q := store.IssueListQuery{Search: c.Query("search"), Status: c.Query("status"), Limit: 100}
	for _, parameter := range []struct {
		name   string
		target *int
	}{{"limit", &q.Limit}, {"offset", &q.Offset}} {
		if raw, exists := c.Request.URL.Query()[parameter.name]; exists {
			value, err := strconv.Atoi(raw[0])
			if err != nil {
				return q, domain.Invalid("分页参数无效")
			}
			*parameter.target = value
		}
	}
	return q, nil
}
