// Package sqlitebaseline initializes only the current schema. It never upgrades,
// repairs, or replaces an existing database of a different generation.
package sqlitebaseline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

var ErrUnsupported = errors.New("unsupported database baseline; original database preserved; select a new data directory")

type Definition struct{ ID, SQL string }
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func schemaDigest(ctx context.Context, db Queryer) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT GLOB 'sqlite_*' ORDER BY type, name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var kind, name, statement string
		if err := rows.Scan(&kind, &name, &statement); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%d:%s%d:%s%d:%s", len(kind), kind, len(name), name, len(statement), statement)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Check must run on a read-only connection before enabling WAL, seeding secrets,
// running recovery, pruning rows, or doing any other write to an existing file.
func (definition Definition) Check(ctx context.Context, db Queryer) error {
	var id, source, storedSchema string
	if err := db.QueryRowContext(ctx, `SELECT id, definition_hash, schema_hash FROM database_baseline WHERE singleton = 1`).Scan(&id, &source, &storedSchema); err != nil {
		return fmt.Errorf("%w (%s)", ErrUnsupported, definition.ID)
	}
	if id != definition.ID || source != digest(definition.SQL) {
		return fmt.Errorf("%w (%s)", ErrUnsupported, definition.ID)
	}
	actual, err := schemaDigest(ctx, db)
	if err != nil {
		return err
	}
	if actual != storedSchema {
		return fmt.Errorf("%w: schema objects changed", ErrUnsupported)
	}
	return nil
}

// Initialize is atomic and accepts an empty database only. Reopening a current
// database validates it without re-installing triggers or backfilling metadata.
func (definition Definition) Initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return definition.Check(ctx, tx)
	}
	if _, err := tx.ExecContext(ctx, definition.SQL); err != nil {
		return fmt.Errorf("initialize %s: %w", definition.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE database_baseline (
		singleton INTEGER PRIMARY KEY CHECK(singleton = 1), id TEXT NOT NULL,
		definition_hash TEXT NOT NULL, schema_hash TEXT NOT NULL
	) STRICT`); err != nil {
		return err
	}
	schema, err := schemaDigest(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_baseline VALUES (1, ?, ?, ?)`, definition.ID, digest(definition.SQL), schema); err != nil {
		return err
	}
	return tx.Commit()
}
