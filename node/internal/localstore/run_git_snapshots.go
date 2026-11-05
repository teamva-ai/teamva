package localstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const (
	maxRunGitSnapshotUncompressed = 20 << 20
	maxRunGitSnapshotCompressed   = 10 << 20
	maxRunGitSnapshotRuns         = 200
	maxRunGitSnapshotTotal        = 100 << 20
	maxRunGitSnapshotDecodedTotal = 256 << 20
	// Codex and OpenCode each have one global 64-instance manager. Keep count
	// capacity for all 128 active Runs while retaining the independent byte
	// ceilings below as the stronger memory/disk safety bounds.
	maxActiveRunGitSnapshotRuns        = 128
	maxActiveRunGitSnapshotTotal       = 64 << 20
	maxActiveRunGitSnapshotDecoded     = 128 << 20
	maxRunGitSnapshotsPerRun           = nodeprotocol.MaxGitRepositoriesPerRun * 2
	maxRunGitSnapshotValidationRows    = maxRunGitSnapshotRuns * maxRunGitSnapshotsPerRun
	maxRunGitSnapshotValidationDecoded = 256 << 20
	runGitSnapshotMaxAge               = 30 * 24 * time.Hour
	maxRunGitSnapshotRunIDBytes        = 1024
	maxRunGitSnapshotRepositoryIDBytes = 512
	maxRunGitSnapshotHashBytes         = 128
)

var errRunGitSnapshotTooLarge = errors.New("run Git snapshot exceeds bounded encoding limit")

// SaveRunGitSnapshot streams the dedicated snapshot JSON directly into gzip.
// Patch text is never materialized as a second full JSON byte slice. If the
// bounded stream does not fit, a metadata-only truncated snapshot is retained.
func (s *Store) SaveRunGitSnapshot(
	ctx context.Context,
	runID string,
	snapshot nodeprotocol.GitRunWorkspaceSnapshot,
) error {
	if strings.TrimSpace(runID) == "" || len(runID) > maxRunGitSnapshotRunIDBytes ||
		strings.TrimSpace(snapshot.RepositoryID) == "" ||
		len(snapshot.RepositoryID) > maxRunGitSnapshotRepositoryIDBytes ||
		(snapshot.Phase != "start" && snapshot.Phase != "end") {
		return errors.New("invalid run Git snapshot metadata")
	}
	if snapshot.CapturedAt.IsZero() {
		return errors.New("invalid run Git snapshot captured_at")
	}
	snapshot.CapturedAt = snapshot.CapturedAt.UTC()
	capturedAtNS := snapshot.CapturedAt.UnixNano()
	if !time.Unix(0, capturedAtNS).UTC().Equal(snapshot.CapturedAt) {
		return errors.New("run Git snapshot captured_at is outside nanosecond range")
	}
	snapshot.ContentHash = snapshotContentHash(runID, snapshot)
	uncompressedBytes, compressed, err := encodeRunGitSnapshot(ctx, snapshot)
	if errors.Is(err, errRunGitSnapshotTooLarge) {
		truncateRunGitSnapshotPayload(&snapshot)
		snapshot.ContentHash = snapshotContentHash(runID, snapshot)
		uncompressedBytes, compressed, err = encodeRunGitSnapshot(ctx, snapshot)
	}
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rowCount, repositoryCount, exactExists, repositoryExists int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT repository_id),
		       EXISTS(
			   SELECT 1 FROM run_git_snapshots
			   WHERE run_id = ? AND phase = ? AND repository_id = ?
		       ),
		       EXISTS(
			   SELECT 1 FROM run_git_snapshots
			   WHERE run_id = ? AND repository_id = ?
		       )
		FROM run_git_snapshots WHERE run_id = ?
	`, runID, snapshot.Phase, snapshot.RepositoryID,
		runID, snapshot.RepositoryID, runID).Scan(
		&rowCount, &repositoryCount, &exactExists, &repositoryExists,
	); err != nil {
		return err
	}
	if exactExists == 0 && rowCount >= maxRunGitSnapshotsPerRun {
		return errors.New("run Git snapshot count exceeds per-run bound")
	}
	if repositoryExists == 0 && repositoryCount >= nodeprotocol.MaxGitRepositoriesPerRun {
		return errors.New("run Git snapshot repository count exceeds per-run bound")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_git_snapshots (
			run_id, phase, repository_id, captured_at_ns, content_hash,
			uncompressed_bytes, compressed_bytes, truncated, payload
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, phase, repository_id) DO UPDATE SET
			captured_at_ns = excluded.captured_at_ns,
			content_hash = excluded.content_hash,
			uncompressed_bytes = excluded.uncompressed_bytes,
			compressed_bytes = excluded.compressed_bytes,
			truncated = excluded.truncated,
			payload = excluded.payload
	`, runID, snapshot.Phase, snapshot.RepositoryID,
		capturedAtNS, snapshot.ContentHash,
		uncompressedBytes, len(compressed), boolInteger(snapshot.Truncated), compressed); err != nil {
		return err
	}
	if err := pruneRunGitSnapshots(ctx, tx, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func truncateRunGitSnapshotPayload(snapshot *nodeprotocol.GitRunWorkspaceSnapshot) {
	for index := range snapshot.Areas {
		snapshot.Areas[index].Patch = ""
		snapshot.Areas[index].Truncated = true
	}
	snapshot.Truncated = true
	for _, warning := range snapshot.Warnings {
		if warning.Code == nodeprotocol.GitWarningSnapshotPayloadTruncated {
			return
		}
	}
	snapshot.Warnings = append(snapshot.Warnings,
		nodeprotocol.NewGitRepositoryWarning(nodeprotocol.GitWarningSnapshotPayloadTruncated))
}

func (s *Store) GetRunGitSnapshots(
	ctx context.Context,
	runID string,
) ([]nodeprotocol.GitRunWorkspaceSnapshot, error) {
	if strings.TrimSpace(runID) == "" || len(runID) > maxRunGitSnapshotRunIDBytes {
		return nil, errors.New("run ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT phase, length(CAST(repository_id AS BLOB)), substr(repository_id, 1, ?), captured_at_ns,
		       length(CAST(content_hash AS BLOB)), substr(content_hash, 1, ?), uncompressed_bytes,
		       compressed_bytes, truncated, length(payload), substr(payload, 1, ?)
		FROM run_git_snapshots
		WHERE run_id = ?
		ORDER BY CASE phase WHEN 'start' THEN 0 ELSE 1 END, repository_id
	`, maxRunGitSnapshotRepositoryIDBytes+1, maxRunGitSnapshotHashBytes+1,
		maxRunGitSnapshotCompressed+1, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]nodeprotocol.GitRunWorkspaceSnapshot, 0, maxRunGitSnapshotsPerRun)
	for rows.Next() {
		var phase, repositoryID, contentHash string
		var repositoryIDBytes, contentHashBytes int
		var capturedAtNS int64
		var uncompressedBytes, compressedBytes, truncated, payloadBytes int
		var compressed []byte
		if err := rows.Scan(&phase, &repositoryIDBytes, &repositoryID, &capturedAtNS,
			&contentHashBytes, &contentHash, &uncompressedBytes,
			&compressedBytes, &truncated, &payloadBytes, &compressed); err != nil {
			return nil, err
		}
		if repositoryIDBytes != len(repositoryID) || contentHashBytes != len(contentHash) ||
			payloadBytes != compressedBytes || payloadBytes != len(compressed) {
			return nil, errors.New("corrupt or oversized run Git snapshot storage")
		}
		if len(result) >= maxRunGitSnapshotsPerRun {
			return nil, errors.New("run Git snapshot count exceeds per-run bound")
		}
		snapshot, err := validateAndDecodeRunGitSnapshot(runID, phase, repositoryID, capturedAtNS,
			contentHash, uncompressedBytes, compressedBytes, truncated, compressed)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

func validateAndDecodeRunGitSnapshot(
	runID, phase, repositoryID string,
	capturedAtNS int64,
	contentHash string,
	uncompressedBytes, compressedBytes, truncated int,
	compressed []byte,
) (nodeprotocol.GitRunWorkspaceSnapshot, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(repositoryID) == "" ||
		(phase != "start" && phase != "end") ||
		len(runID) > maxRunGitSnapshotRunIDBytes || len(repositoryID) > maxRunGitSnapshotRepositoryIDBytes ||
		len(contentHash) != sha256.Size*2 || !validHexDigest(contentHash) ||
		compressedBytes != len(compressed) || compressedBytes < 0 || compressedBytes > maxRunGitSnapshotCompressed ||
		uncompressedBytes < 0 || uncompressedBytes > maxRunGitSnapshotUncompressed ||
		(truncated != 0 && truncated != 1) {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("corrupt run Git snapshot bounds")
	}
	capturedAt := time.Unix(0, capturedAtNS).UTC()
	if capturedAt.UnixNano() != capturedAtNS {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("corrupt run Git snapshot timestamp")
	}
	snapshot, err := decodeRunGitSnapshot(compressed, uncompressedBytes)
	if err != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, err
	}
	if snapshot.Phase != phase || snapshot.RepositoryID != repositoryID ||
		!snapshot.CapturedAt.Equal(capturedAt) || snapshot.Truncated != (truncated == 1) ||
		snapshot.ContentHash != contentHash || snapshotContentHash(runID, snapshot) != contentHash {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("corrupt run Git snapshot metadata")
	}
	return snapshot, nil
}

func validHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (writer *boundedBuffer) Write(payload []byte) (int, error) {
	if len(payload) > writer.limit-writer.buffer.Len() {
		return 0, errRunGitSnapshotTooLarge
	}
	return writer.buffer.Write(payload)
}

type boundedCountingWriter struct {
	writer io.Writer
	count  int
	limit  int
}

func (writer *boundedCountingWriter) Write(payload []byte) (int, error) {
	if len(payload) > writer.limit-writer.count {
		return 0, errRunGitSnapshotTooLarge
	}
	written, err := writer.writer.Write(payload)
	writer.count += written
	return written, err
}

func encodeRunGitSnapshot(ctx context.Context, snapshot nodeprotocol.GitRunWorkspaceSnapshot) (int, []byte, error) {
	compressed := &boundedBuffer{limit: maxRunGitSnapshotCompressed}
	gzipWriter, err := gzip.NewWriterLevel(compressed, gzip.BestSpeed)
	if err != nil {
		return 0, nil, err
	}
	counted := &boundedCountingWriter{writer: gzipWriter, limit: maxRunGitSnapshotUncompressed}
	writer := &snapshotJSONWriter{ctx: ctx, writer: counted}
	writer.writeSnapshot(snapshot)
	if writer.err != nil {
		_ = gzipWriter.Close()
		return 0, nil, writer.err
	}
	if err := gzipWriter.Close(); err != nil {
		return 0, nil, err
	}
	return counted.count, append([]byte(nil), compressed.buffer.Bytes()...), nil
}

type countingReader struct {
	reader io.Reader
	count  int
}

func (reader *countingReader) Read(payload []byte) (int, error) {
	read, err := reader.reader.Read(payload)
	reader.count += read
	return read, err
}

func decodeRunGitSnapshot(compressed []byte, expectedBytes int) (nodeprotocol.GitRunWorkspaceSnapshot, error) {
	if len(compressed) > maxRunGitSnapshotCompressed || expectedBytes < 0 || expectedBytes > maxRunGitSnapshotUncompressed {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("compressed run Git snapshot exceeds limit")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("corrupt run Git snapshot compression")
	}
	limited := &io.LimitedReader{R: reader, N: int64(maxRunGitSnapshotUncompressed + 1)}
	counted := &countingReader{reader: limited}
	decoder := json.NewDecoder(counted)
	decoder.DisallowUnknownFields()
	var snapshot nodeprotocol.GitRunWorkspaceSnapshot
	decodeErr := decoder.Decode(&snapshot)
	var trailing json.RawMessage
	trailingErr := decoder.Decode(&trailing)
	closeErr := reader.Close()
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || closeErr != nil || limited.N <= 0 || counted.count != expectedBytes {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, errors.New("corrupt or oversized run Git snapshot payload")
	}
	return snapshot, nil
}

func snapshotContentHash(runID string, snapshot nodeprotocol.GitRunWorkspaceSnapshot) string {
	snapshot.ContentHash = ""
	digest := sha256.New()
	_, _ = digest.Write([]byte("teamva/run-git-snapshot/v3"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(runID)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(runID))
	writer := &snapshotJSONWriter{writer: digest}
	writer.writeSnapshot(snapshot)
	return hex.EncodeToString(digest.Sum(nil))
}

type snapshotJSONWriter struct {
	ctx    context.Context
	writer io.Writer
	err    error
}

func (writer *snapshotJSONWriter) write(payload string) {
	if writer.err != nil {
		return
	}
	if writer.ctx != nil {
		if err := writer.ctx.Err(); err != nil {
			writer.err = err
			return
		}
	}
	_, writer.err = io.WriteString(writer.writer, payload)
}

func (writer *snapshotJSONWriter) string(value string) {
	writer.write(`"`)
	start := 0
	for index := 0; index < len(value); {
		current := value[index]
		if current >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(value[index:])
			if size > 1 {
				index += size
				continue
			}
			if start < index {
				writer.write(value[start:index])
			}
			writer.write(`\ufffd`)
			index++
			start = index
			continue
		}
		var escaped string
		switch current {
		case '\\':
			escaped = `\\`
		case '"':
			escaped = `\"`
		case '\b':
			escaped = `\b`
		case '\f':
			escaped = `\f`
		case '\n':
			escaped = `\n`
		case '\r':
			escaped = `\r`
		case '\t':
			escaped = `\t`
		default:
			if current < 0x20 {
				escaped = `\u00` + hex.EncodeToString([]byte{current})
			}
		}
		if escaped == "" {
			index++
			continue
		}
		if start < index {
			writer.write(value[start:index])
		}
		writer.write(escaped)
		index++
		start = index
	}
	if start < len(value) {
		writer.write(value[start:])
	}
	writer.write(`"`)
}

func (writer *snapshotJSONWriter) integer(value int64) { writer.write(strconv.FormatInt(value, 10)) }

func (writer *snapshotJSONWriter) boolean(value bool) {
	if value {
		writer.write("true")
	} else {
		writer.write("false")
	}
}

func (writer *snapshotJSONWriter) stringArray(values []string) {
	writer.write("[")
	for index, value := range values {
		if index > 0 {
			writer.write(",")
		}
		writer.string(value)
	}
	writer.write("]")
}

func (writer *snapshotJSONWriter) writeSnapshot(snapshot nodeprotocol.GitRunWorkspaceSnapshot) {
	writer.write(`{"repository_id":`)
	writer.string(snapshot.RepositoryID)
	writer.write(`,"label":`)
	writer.string(snapshot.Label)
	writer.write(`,"root_display":`)
	writer.string(snapshot.RootDisplay)
	writer.write(`,"phase":`)
	writer.string(snapshot.Phase)
	writer.write(`,"captured_at":`)
	writer.string(snapshot.CapturedAt.UTC().Format(time.RFC3339Nano))
	writer.write(`,"areas":[`)
	for index, area := range snapshot.Areas {
		if index > 0 {
			writer.write(",")
		}
		writer.write(`{"area":`)
		writer.string(area.Area)
		if area.Patch != "" {
			writer.write(`,"patch":`)
			writer.string(area.Patch)
		}
		writer.write(`,"paths":`)
		writer.stringArray(area.Paths)
		writer.write(`,"binary_paths":`)
		writer.stringArray(area.BinaryPaths)
		writer.write(`,"truncated":`)
		writer.boolean(area.Truncated)
		writer.write("}")
	}
	writer.write(`],"untracked":[`)
	for index, entry := range snapshot.Untracked {
		if index > 0 {
			writer.write(",")
		}
		writer.write(`{"path":`)
		writer.string(entry.Path)
		writer.write(`,"kind":`)
		writer.string(entry.Kind)
		if entry.SizeBytes != 0 {
			writer.write(`,"size_bytes":`)
			writer.integer(entry.SizeBytes)
		}
		writer.write("}")
	}
	writer.write(`],"warnings":[`)
	for index, warning := range snapshot.Warnings {
		if index > 0 {
			writer.write(",")
		}
		writer.write(`{"code":`)
		writer.string(warning.Code)
		writer.write(`,"message":`)
		writer.string(warning.Message)
		writer.write("}")
	}
	writer.write(`],"truncated":`)
	writer.boolean(snapshot.Truncated)
	writer.write(`,"content_hash":`)
	writer.string(snapshot.ContentHash)
	writer.write("}")
}

// validateRunGitSnapshots runs at Open before the Store is exposed. It checks
// every bounded gzip stream, JSON payload, duplicate metadata field, and hash.
// Failure preserves runtime.db and refuses startup.
func (s *Store) validateRunGitSnapshots(ctx context.Context) error {
	return validateRunGitSnapshotsQuery(ctx, s.db)
}

type runGitSnapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func validateRunGitSnapshotsQuery(ctx context.Context, queryer runGitSnapshotQueryer) error {
	rows, err := queryer.QueryContext(ctx, `
		SELECT length(CAST(run_id AS BLOB)), substr(run_id, 1, ?), phase,
		       length(CAST(repository_id AS BLOB)), substr(repository_id, 1, ?), captured_at_ns,
		       length(CAST(content_hash AS BLOB)), substr(content_hash, 1, ?),
		       uncompressed_bytes, compressed_bytes, truncated,
		       length(payload), substr(payload, 1, ?)
		FROM run_git_snapshots
	`, maxRunGitSnapshotRunIDBytes+1, maxRunGitSnapshotRepositoryIDBytes+1,
		maxRunGitSnapshotHashBytes+1, maxRunGitSnapshotCompressed+1)
	if err != nil {
		return fmt.Errorf("validate run Git snapshots: %w", err)
	}
	defer rows.Close()
	rowCount := 0
	compressedTotal := int64(0)
	decodedTotal := int64(0)
	type validatedRun struct {
		compressed   int64
		decoded      int64
		rows         int
		repositories map[string]struct{}
		starts       map[string]struct{}
		ends         map[string]struct{}
	}
	runs := make(map[string]validatedRun)
	for rows.Next() {
		rowCount++
		if rowCount > maxRunGitSnapshotValidationRows {
			return errors.New("run Git snapshot row count exceeds retention bound")
		}
		var runID, phase, repositoryID, contentHash string
		var runIDBytes, repositoryIDBytes, contentHashBytes int
		var capturedAtNS int64
		var uncompressedBytes, compressedBytes, truncated, payloadBytes int
		var payload []byte
		if err := rows.Scan(&runIDBytes, &runID, &phase, &repositoryIDBytes, &repositoryID,
			&capturedAtNS, &contentHashBytes, &contentHash,
			&uncompressedBytes, &compressedBytes, &truncated, &payloadBytes, &payload); err != nil {
			return fmt.Errorf("validate run Git snapshots: %w", err)
		}
		if runIDBytes != len(runID) || repositoryIDBytes != len(repositoryID) ||
			contentHashBytes != len(contentHash) || payloadBytes != compressedBytes || payloadBytes != len(payload) {
			return errors.New("run Git snapshot payload exceeds or disagrees with storage bound")
		}
		compressedTotal += int64(compressedBytes)
		decodedTotal += int64(uncompressedBytes)
		if compressedTotal > maxRunGitSnapshotTotal || decodedTotal > maxRunGitSnapshotValidationDecoded {
			return errors.New("run Git snapshot validation budget exceeded")
		}
		if _, err := validateAndDecodeRunGitSnapshot(runID, phase, repositoryID, capturedAtNS,
			contentHash, uncompressedBytes, compressedBytes, truncated, payload); err != nil {
			return fmt.Errorf("validate run Git snapshot content: %w", err)
		}
		item := runs[runID]
		if item.starts == nil {
			item.repositories = make(map[string]struct{})
			item.starts = make(map[string]struct{})
			item.ends = make(map[string]struct{})
		}
		item.rows++
		if item.rows > maxRunGitSnapshotsPerRun {
			return errors.New("run Git snapshot count exceeds per-run bound")
		}
		item.repositories[repositoryID] = struct{}{}
		if len(item.repositories) > nodeprotocol.MaxGitRepositoriesPerRun {
			return errors.New("run Git snapshot repository count exceeds per-run bound")
		}
		item.compressed += int64(compressedBytes)
		item.decoded += int64(uncompressedBytes)
		if phase == "start" {
			item.starts[repositoryID] = struct{}{}
		} else {
			item.ends[repositoryID] = struct{}{}
		}
		runs[runID] = item
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate run Git snapshots: %w", err)
	}
	activeRuns := 0
	activeCompressed := int64(0)
	activeDecoded := int64(0)
	for _, item := range runs {
		active := false
		for repositoryID := range item.starts {
			if _, finished := item.ends[repositoryID]; !finished {
				active = true
				break
			}
		}
		if !active {
			continue
		}
		activeRuns++
		activeCompressed += item.compressed
		activeDecoded += item.decoded
	}
	if len(runs) > maxRunGitSnapshotRuns || activeRuns > maxActiveRunGitSnapshotRuns ||
		activeCompressed > maxActiveRunGitSnapshotTotal || activeDecoded > maxActiveRunGitSnapshotDecoded {
		return errors.New("run Git snapshot retention state exceeds safety bound")
	}
	return nil
}

type runGitRetentionRow struct {
	runID      string
	capturedAt time.Time
	bytes      int64
	decoded    int64
	active     bool
}

type interruptedSnapshotEnd struct {
	runID             string
	snapshot          nodeprotocol.GitRunWorkspaceSnapshot
	uncompressedBytes int
	compressed        []byte
}

type interruptedSnapshotKey struct {
	runID        string
	repositoryID string
}

// completeInterruptedRunGitSnapshots runs once while Store.Open still owns the
// startup boundary. Therefore every start-only snapshot belongs to a prior
// process lifetime and can no longer receive its original deferred end phase.
// Decode all starts before opening the write transaction: corruption refuses
// startup without partially "repairing" or deleting the database.
func (s *Store) completeInterruptedRunGitSnapshots(ctx context.Context, capturedAt time.Time) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT start.run_id, start.repository_id
		FROM run_git_snapshots AS start
		WHERE start.phase = 'start' AND NOT EXISTS (
			SELECT 1 FROM run_git_snapshots AS finish
			WHERE finish.run_id = start.run_id AND finish.phase = 'end'
			  AND finish.repository_id = start.repository_id
		)
		ORDER BY start.captured_at_ns, start.run_id, start.repository_id
		LIMIT ?
	`, maxRunGitSnapshotValidationRows+1)
	if err != nil {
		return err
	}
	keys := make([]interruptedSnapshotKey, 0, maxActiveRunGitSnapshotRuns)
	for rows.Next() {
		var key interruptedSnapshotKey
		if err := rows.Scan(&key.runID, &key.repositoryID); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(keys) > maxRunGitSnapshotValidationRows {
		return errors.New("interrupted run Git snapshots exceed recovery bound")
	}
	prepared := make([]interruptedSnapshotEnd, 0, len(keys))
	for index, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshots, err := s.GetRunGitSnapshots(ctx, key.runID)
		if err != nil {
			return err
		}
		var start nodeprotocol.GitRunWorkspaceSnapshot
		found := false
		for _, snapshot := range snapshots {
			if snapshot.Phase == "start" && snapshot.RepositoryID == key.repositoryID {
				start = snapshot
				found = true
				break
			}
		}
		if !found {
			if err != nil {
				return err
			}
			return errors.New("corrupt interrupted run Git snapshot phases")
		}
		end := nodeprotocol.GitRunWorkspaceSnapshot{
			RepositoryID: start.RepositoryID, Label: start.Label, RootDisplay: start.RootDisplay,
			Phase: "end", CapturedAt: capturedAt.Add(time.Duration(index) * time.Nanosecond),
			Areas: []nodeprotocol.GitRunPatchArea{
				{Area: "conflict", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
				{Area: "staged", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
				{Area: "unstaged", Paths: []string{}, BinaryPaths: []string{}, Truncated: true},
			},
			Untracked: []nodeprotocol.GitRunUntrackedEntry{},
			Warnings: []nodeprotocol.GitRepositoryWarning{
				nodeprotocol.NewGitRepositoryWarning(nodeprotocol.GitWarningSnapshotIncomplete),
			},
			Truncated: true,
		}
		end.ContentHash = snapshotContentHash(key.runID, end)
		uncompressedBytes, compressed, err := encodeRunGitSnapshot(ctx, end)
		if err != nil {
			return err
		}
		prepared = append(prepared, interruptedSnapshotEnd{
			runID: key.runID, snapshot: end, uncompressedBytes: uncompressedBytes, compressed: compressed,
		})
	}
	if len(prepared) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range prepared {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_git_snapshots (
				run_id, phase, repository_id, captured_at_ns, content_hash,
				uncompressed_bytes, compressed_bytes, truncated, payload
			) VALUES (?, 'end', ?, ?, ?, ?, ?, 1, ?)
		`, item.runID, item.snapshot.RepositoryID, item.snapshot.CapturedAt.UnixNano(),
			item.snapshot.ContentHash, item.uncompressedBytes, len(item.compressed), item.compressed); err != nil {
			return err
		}
	}
	if err := pruneRunGitSnapshots(ctx, tx, capturedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneRunGitSnapshots(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT snapshot.run_id, MAX(snapshot.captured_at_ns), SUM(snapshot.compressed_bytes),
		       SUM(snapshot.uncompressed_bytes), EXISTS (
			SELECT 1 FROM run_git_snapshots AS start
			WHERE start.run_id = snapshot.run_id AND start.phase = 'start'
			  AND NOT EXISTS (
				SELECT 1 FROM run_git_snapshots AS finish
				WHERE finish.run_id = start.run_id AND finish.phase = 'end'
				  AND finish.repository_id = start.repository_id
			  )
		)
		FROM run_git_snapshots AS snapshot
		GROUP BY snapshot.run_id
		ORDER BY MAX(captured_at_ns) DESC, run_id DESC
	`)
	if err != nil {
		return err
	}
	retentionRows := make([]runGitRetentionRow, 0, maxRunGitSnapshotRuns+1)
	activeRuns := 0
	activeBytes := int64(0)
	activeDecoded := int64(0)
	for rows.Next() {
		var item runGitRetentionRow
		var capturedAtNS int64
		var active int
		if err := rows.Scan(&item.runID, &capturedAtNS, &item.bytes, &item.decoded, &active); err != nil {
			rows.Close()
			return err
		}
		item.capturedAt = time.Unix(0, capturedAtNS).UTC()
		if item.capturedAt.UnixNano() != capturedAtNS || item.bytes < 0 || item.decoded < 0 {
			rows.Close()
			return errors.New("invalid run Git snapshot retention metadata")
		}
		item.active = active != 0
		if item.active {
			activeRuns++
			activeBytes += item.bytes
			activeDecoded += item.decoded
		}
		retentionRows = append(retentionRows, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if activeRuns > maxActiveRunGitSnapshotRuns || activeBytes > maxActiveRunGitSnapshotTotal ||
		activeDecoded > maxActiveRunGitSnapshotDecoded || activeRuns > maxRunGitSnapshotRuns ||
		activeBytes > maxRunGitSnapshotTotal || activeDecoded > maxRunGitSnapshotDecodedTotal {
		return errors.New("active run Git snapshots exceed retention safety bound")
	}
	cutoff := now.Add(-runGitSnapshotMaxAge)
	retainedRuns := activeRuns
	retainedBytes := activeBytes
	retainedDecoded := activeDecoded
	for _, item := range retentionRows {
		if item.active {
			continue
		}
		remove := item.capturedAt.Before(cutoff) || retainedRuns >= maxRunGitSnapshotRuns ||
			retainedBytes+item.bytes > maxRunGitSnapshotTotal ||
			retainedDecoded+item.decoded > maxRunGitSnapshotDecodedTotal
		if remove {
			if _, err := tx.ExecContext(ctx, `DELETE FROM run_git_snapshots WHERE run_id = ?`, item.runID); err != nil {
				return err
			}
			continue
		}
		retainedRuns++
		retainedBytes += item.bytes
		retainedDecoded += item.decoded
	}
	return nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
