package localstore

import (
	_ "embed"
	"github.com/teamva-ai/teamva/protocol/sqlitebaseline"
)

//go:embed schema.sql
var schemaSQL string

var runtimeBaseline = sqlitebaseline.Definition{ID: "teamva-runtime-v1", SQL: schemaSQL}
