package store

import (
	"database/sql/driver"
	"strings"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"modernc.org/sqlite"
)

type IssueListQuery struct {
	Search, Status string
	Offset, Limit  int
}

type IssuePage struct {
	domain.Page[domain.Issue]
	Total int `json:"total"`
}

func (q IssueListQuery) validate() error {
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 1_000_000 || utf8.RuneCountInString(q.Search) > 500 {
		return domain.Invalid("任务查询范围无效")
	}
	switch q.Status {
	case "", domain.IssueOpen, domain.IssueResolved, domain.IssueClosed:
		return nil
	default:
		return domain.Invalid("不支持的任务状态")
	}
}

func init() {
	// SQLite lower/NOCASE only fold ASCII; title search must also match Unicode literally.
	sqlite.MustRegisterDeterministicScalarFunction("teamva_issue_title_contains", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			text, textOK := args[0].(string)
			query, queryOK := args[1].(string)
			return textOK && queryOK && strings.Contains(strings.ToLower(text), query), nil
		})
}
