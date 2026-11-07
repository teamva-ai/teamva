package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const nativeCatalogFilename = "models-dev.json"

type nativeAccount struct {
	// Empty values mark selected non-key credentials, so stale legacy keys
	// cannot silently replace the user's selected OAuth/external account.
	keys    map[string]string
	catalog json.RawMessage
}

func nativeAccountPath() (string, error) {
	home, err := dataHome()
	if err != nil {
		return "", errors.New("opencode cannot locate native account metadata")
	}
	return filepath.Join(home, "opencode", "opencode.db"), nil
}

// Keep Windows drive letters in the URI path, not the authority.
func nativeAccountFileURL(path string) *url.URL {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return &url.URL{Scheme: "file", Path: slashed}
}

// Read only the current V2 account selection and the public models.dev cache.
// Never open the user's database through OpenCode (which may migrate it), copy
// the database, or read OAuth token fields/whole credential values.
func readNativeAccount() (nativeAccount, error) {
	result := nativeAccount{keys: map[string]string{}}
	path, err := nativeAccountPath()
	if err != nil {
		return result, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	invalid := errors.New("opencode native account metadata unavailable or unsupported")
	if err != nil || !info.Mode().IsRegular() {
		return result, invalid
	}
	uri := nativeAccountFileURL(path)
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(1000)"}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return result, invalid
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, invalid
	}
	defer tx.Rollback()
	// Match Credential.list(...).at(-1): active first, then newest creation/id.
	// Project only type and the key field for key credentials; OAuth secrets are
	// not returned from SQLite to the host process at all.
	rows, err := tx.QueryContext(ctx, `WITH selected AS (
		SELECT integration_id, value, row_number() OVER (
			PARTITION BY integration_id ORDER BY active DESC, time_created DESC, id DESC
		) AS position FROM credential WHERE integration_id IS NOT NULL
	) SELECT integration_id, json_extract(value, '$.type'),
		CASE WHEN json_extract(value, '$.type') = 'key' THEN json_extract(value, '$.key') END
		FROM selected WHERE position = 1 LIMIT 1001`)
	if err != nil {
		return result, invalid
	}
	count := 0
	for rows.Next() {
		count++
		var integration, kind string
		var key sql.NullString
		if count > 1000 || rows.Scan(&integration, &kind, &key) != nil || integration == "" || len(integration) > 200 {
			_ = rows.Close()
			return nativeAccount{}, invalid
		}
		if _, selected := result.keys[integration]; selected {
			continue
		}
		result.keys[integration] = ""
		if kind == "key" {
			if !key.Valid || key.String == "" || len(key.String) > 8192 {
				_ = rows.Close()
				return nativeAccount{}, invalid
			}
			result.keys[integration] = key.String
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nativeAccount{}, invalid
	}
	_ = rows.Close()
	var catalog string
	err = tx.QueryRowContext(ctx, `SELECT json_extract(value, '$.body') FROM kv WHERE key = 'models-dev:catalog'`).Scan(&catalog)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil || len(catalog) == 0 || len(catalog) > 16<<20 {
		return nativeAccount{}, invalid
	}
	var providers map[string]json.RawMessage
	if json.Unmarshal([]byte(catalog), &providers) != nil || len(providers) == 0 {
		return nativeAccount{}, invalid
	}
	result.catalog = json.RawMessage(catalog)
	return result, nil
}

func writePrivateNativeFile(directory, name string, payload []byte) error {
	file, err := os.CreateTemp(directory, ".native-config-*.tmp")
	if err != nil {
		return errors.New("write private opencode configuration failed")
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return errors.New("write private opencode configuration failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("write private opencode configuration failed")
	}
	if err := os.Rename(path, filepath.Join(directory, name)); err != nil {
		return errors.New("write private opencode configuration failed")
	}
	return nil
}
