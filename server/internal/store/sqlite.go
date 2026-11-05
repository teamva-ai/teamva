package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqliteschema "github.com/teamva-ai/teamva/server/sqlite/schema"
	_ "modernc.org/sqlite"
)

// SQLite is the normalized, durable repository used by the desktop control
// plane. SQL is its only source of truth.
type SQLite struct {
	db      *sql.DB
	csrfKey []byte
	hmacKey []byte
}

func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite path: %w", err)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create SQLite directory: %w", err)
	}
	existing, existingInfo, err := existingSQLiteRegularFile(path)
	if err != nil {
		return nil, err
	}
	// The first connection to an existing control database is strictly
	// read-only and has no journal_mode pragma. This makes quick_check happen
	// before initialization, secrets, identity seeding or dispatch recovery
	// can write a single byte to the database.
	if existing {
		if err := checkExistingSQLiteIntegrity(ctx, path); err != nil {
			return nil, err
		}
		currentInfo, statErr := os.Lstat(path)
		if statErr != nil || !currentInfo.Mode().IsRegular() ||
			!os.SameFile(existingInfo, currentInfo) {
			if statErr != nil {
				return nil, fmt.Errorf("recheck SQLite file: %w", statErr)
			}
			return nil, errors.New("SQLite file changed during integrity check")
		}
	}
	dsn := sqliteFileDSN(path, false)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}
	if err := sqliteschema.Definition.Initialize(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := checkSQLiteIntegrity(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify current SQLite baseline: %w", err)
	}
	_ = os.Chmod(path, 0o600)

	csrfKey, err := loadOrCreateSQLiteSecret(ctx, db, "install_csrf_key")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite CSRF secret: %w", err)
	}
	hmacKey, err := loadOrCreateSQLiteSecret(ctx, db, "install_hmac_key")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite HMAC secret: %w", err)
	}
	repository := &SQLite{db: db, csrfKey: csrfKey, hmacKey: hmacKey}
	if err := repository.ensureLocalIdentity(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repository, nil
}

func existingSQLiteRegularFile(path string) (bool, fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("inspect SQLite file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, nil, errors.New("existing SQLite path is not a regular file")
	}
	return true, info, nil
}

func sqliteFileDSN(path string, readOnly bool) string {
	// A Windows drive-letter path must be a URI path ("/C:/..."), not an
	// authority ("C:"), or SQLite rejects the DSN.
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	location := &url.URL{Scheme: "file", Path: slashed}
	query := location.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "foreign_keys(1)")
		// Runtime events are acknowledged and deleted from a separate SQLite
		// database immediately after this control transaction commits. FULL is
		// required so a successful commit remains durable across OS/power loss,
		// not only ordinary process crashes.
		query.Add("_pragma", "synchronous(FULL)")
	}
	location.RawQuery = query.Encode()
	return location.String()
}

func checkExistingSQLiteIntegrity(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", sqliteFileDSN(path, true))
	if err != nil {
		return fmt.Errorf("open existing SQLite read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("existing SQLite integrity check failed: ping read-only: %w", err)
	}
	if err := checkSQLiteIntegrity(ctx, db); err != nil {
		return fmt.Errorf("existing SQLite integrity check failed: %w", err)
	}
	if err := sqliteschema.Definition.Check(ctx, db); err != nil {
		return err
	}
	return checkSQLiteIssueWorkIndex(ctx, db)
}

func loadOrCreateSQLiteSecret(ctx context.Context, db *sql.DB, key string) ([]byte, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var encodedJSON string
	err = tx.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key = ?`, key).Scan(&encodedJSON)
	if err == nil {
		var encoded string
		if json.Unmarshal([]byte(encodedJSON), &encoded) != nil {
			return nil, fmt.Errorf("decode %s metadata", key)
		}
		secret, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
		if decodeErr != nil || len(secret) != 32 {
			return nil, fmt.Errorf("invalid %s metadata", key)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return secret, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	value, _ := json.Marshal(base64.RawURLEncoding.EncodeToString(secret))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO app_metadata (key, value, updated_at) VALUES (?, ?, ?)
	`, key, string(value), sqliteTimeValue(time.Now())); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *SQLite) Health(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return checkSQLiteIntegrity(ctx, s.db)
}

type sqliteIntegrityQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkSQLiteIntegrity(ctx context.Context, queryer sqliteIntegrityQueryer) error {
	rows, err := queryer.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("SQLite quick_check: %w", err)
	}
	defer rows.Close()
	checked := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("SQLite quick_check: %w", err)
		}
		checked = true
		if result != "ok" {
			return fmt.Errorf("SQLite quick_check: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("SQLite quick_check: %w", err)
	}
	if !checked {
		return errors.New("SQLite quick_check returned no result")
	}
	foreignRows, err := queryer.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("SQLite foreign_key_check: %w", err)
	}
	defer foreignRows.Close()
	if foreignRows.Next() {
		return errors.New("SQLite foreign_key_check reported a violation")
	}
	if err := foreignRows.Err(); err != nil {
		return fmt.Errorf("SQLite foreign_key_check: %w", err)
	}
	return nil
}

// Checkpoint lets lifecycle callers flush available WAL frames. All normalized
// writes are already durable when their transaction commits.
func (s *SQLite) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint SQLite WAL: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint SQLite WAL: busy (%d of %d frames checkpointed)", checkpointed, logFrames)
	}
	return nil
}

func (s *SQLite) Close() error {
	var busy, logFrames, checkpointed int
	walErr := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	if walErr != nil {
		walErr = fmt.Errorf("truncate SQLite WAL: %w", walErr)
	} else if busy != 0 {
		walErr = fmt.Errorf("truncate SQLite WAL: busy (%d of %d frames checkpointed)", checkpointed, logFrames)
	}
	return errors.Join(walErr, s.db.Close())
}

func sqliteTimeValue(value time.Time) string {
	// SQLite compares timestamps as TEXT in queue/lease/expiry predicates. Keep
	// every application-written value fixed-width so lexical and chronological
	// order are identical, including when one value has zero nanoseconds.
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func sqliteParseTime(value string) (time.Time, error) {
	// Writes stay fixed-width for lexical ordering, but SQLite defaults and
	// older desktop builds can contain shorter RFC3339 fractional seconds.
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse SQLite timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func sqliteParseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := sqliteParseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

var _ CoreLocalRepository = (*SQLite)(nil)
