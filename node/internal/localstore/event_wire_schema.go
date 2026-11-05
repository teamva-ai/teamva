package localstore

import (
	"context"
	"database/sql"
	"errors"
)

// validateEventWireBytes requires the current wire-size schema. Exact byte
// counts are persisted with each event; guessing sizes for old rows would hide
// an unsupported schema or a damaged database.
func validateEventWireBytes(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(event_log)`)
	if err != nil {
		return err
	}
	hasWireBytes := false
	columns := 0
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns++
		if name == "wire_bytes" {
			hasWireBytes = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if columns == 0 {
		return errors.New("event_log schema is missing")
	}
	if !hasWireBytes {
		return errors.New("event_log has an unsupported schema: wire_bytes is missing")
	}
	return nil
}
