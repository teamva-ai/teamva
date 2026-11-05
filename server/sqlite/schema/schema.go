// Package schema contains a single current control-plane baseline, split by
// domain for readability. These files are not an incremental migration chain.
package schema

import (
	"embed"
	"strings"

	"github.com/teamva-ai/teamva/protocol/sqlitebaseline"
)

//go:embed *.sql
var files embed.FS

var Definition = sqlitebaseline.Definition{ID: "teamva-control-v1", SQL: source()}

func source() string {
	var parts []string
	for _, name := range []string{"core.sql", "skills.sql", "issue_goals.sql", "conversations.sql", "conversation_goals.sql", "events.sql", "attachments.sql", "inspirations.sql", "work_records.sql", "project_search.sql"} {
		data, err := files.ReadFile(name)
		if err != nil {
			panic(err)
		}
		parts = append(parts, string(data))
	}
	return strings.Join(parts, "\n") + workRecordTriggers()
}
