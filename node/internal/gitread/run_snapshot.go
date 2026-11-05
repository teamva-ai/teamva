package gitread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const (
	maxRunSnapshotPaths     = 10_000
	maxRunSnapshotPathBytes = 1 << 20
	runSnapshotPatchBudget  = 18 << 20
	maxRunSnapshotJSONBytes = 20 << 20
)

// CaptureWorkspaceSnapshot takes a bounded, read-only observation through the
// same authorization and hardened Git execution path as project Changes. It
// uses one status command and at most two aggregate diff commands; it never
// invokes a shell, fetches, runs textconv/external diff, or reads untracked
// file contents.
func (s *Service) CaptureWorkspaceSnapshot(
	ctx context.Context,
	request nodeprotocol.GitRepositoriesRequest,
	repositoryID string,
	phase string,
) (nodeprotocol.GitRunWorkspaceSnapshot, error) {
	done, operationErr := s.beginServiceOperation()
	if operationErr != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, operationErr
	}
	defer done()
	snapshotContext, cancelSnapshot := context.WithTimeout(ctx, 15*time.Second)
	defer cancelSnapshot()
	ctx = snapshotContext
	if phase != "start" && phase != "end" {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, gitError("invalid_argument", "Git 运行快照 phase 无效", false)
	}
	repository, err := s.executionRepository(ctx, request, repositoryID)
	if err != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, err
	}
	defer repository.closeIdentity()
	if repository.validateIdentity() != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	snapshot := nodeprotocol.GitRunWorkspaceSnapshot{
		RepositoryID: repository.id,
		Label:        repository.summary.Label,
		RootDisplay:  repository.summary.RootDisplay,
		Phase:        phase,
		CapturedAt:   time.Now().UTC(),
		Areas: []nodeprotocol.GitRunPatchArea{
			{Area: "conflict", Paths: []string{}, BinaryPaths: []string{}},
			{Area: "staged", Paths: []string{}, BinaryPaths: []string{}},
			{Area: "unstaged", Paths: []string{}, BinaryPaths: []string{}},
		},
		Untracked: []nodeprotocol.GitRunUntrackedEntry{},
		Warnings:  []nodeprotocol.GitRepositoryWarning{},
	}

	statusArguments := []string{
		"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none", "--",
	}
	statusArguments = append(statusArguments, repository.pathspecs("")...)
	statusOutput, statusErr := s.runGit(ctx, repository, statusOutputLimit, false, statusArguments...)
	if statusErr != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, statusErr
	}
	if statusOutput.truncated {
		snapshot.Truncated = true
		snapshot.Warnings = append(snapshot.Warnings,
			warning(nodeprotocol.GitWarningStatusTruncated))
	}
	status := parseStatusOutput(statusOutput.data, repository)
	if status.truncated {
		snapshot.Truncated = true
		snapshot.Warnings = append(snapshot.Warnings, status.warnings...)
	}
	pathCount := 0
	pathBytes := 0
	pathLimitWarning := false
	appendPath := func(target *[]string, value string) bool {
		display := boundedSnapshotPath(value)
		if display == "" || pathCount >= maxRunSnapshotPaths || pathBytes+len(display) > maxRunSnapshotPathBytes {
			pathLimitWarning = true
			return false
		}
		*target = append(*target, display)
		pathCount++
		pathBytes += len(display)
		return true
	}
	for _, entry := range status.items {
		if snapshotNoisePath(entry.path) {
			continue
		}
		switch {
		case entry.conflict:
			appendPath(&snapshot.Areas[0].Paths, entry.path)
		case entry.index == '?':
			display := boundedSnapshotPath(entry.path)
			if display == "" || pathCount >= maxRunSnapshotPaths || pathBytes+len(display) > maxRunSnapshotPathBytes {
				pathLimitWarning = true
				continue
			}
			metadata, metadataErr := repository.secureMetadata(entry.path)
			if metadataErr != nil {
				continue
			}
			kind := modeKind(metadata.mode)
			if metadata.mode == "040000" {
				kind = "directory"
			}
			snapshot.Untracked = append(snapshot.Untracked, nodeprotocol.GitRunUntrackedEntry{
				Path: display, Kind: kind, SizeBytes: metadata.size,
			})
			pathCount++
			pathBytes += len(display)
		default:
			if !unchanged(entry.index) {
				appendPath(&snapshot.Areas[1].Paths, entry.path)
			}
			if !unchanged(entry.worktree) {
				appendPath(&snapshot.Areas[2].Paths, entry.path)
			}
		}
	}
	if pathLimitWarning {
		snapshot.Truncated = true
		snapshot.Warnings = append(snapshot.Warnings,
			warning(nodeprotocol.GitWarningPathLimitExceeded))
	}

	remaining := runSnapshotPatchBudget
	conflictPaths := make(map[string]struct{}, len(snapshot.Areas[0].Paths))
	for _, conflictPath := range snapshot.Areas[0].Paths {
		conflictPaths[conflictPath] = struct{}{}
	}
	for index, area := range []string{"staged", "unstaged"} {
		if remaining <= 0 {
			snapshot.Truncated = true
			snapshot.Areas[index+1].Truncated = true
			continue
		}
		arguments := []string{
			"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames",
			"--diff-filter=ACDMRTXB", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3",
		}
		if area == "staged" {
			arguments = append(arguments, "--cached")
		}
		arguments = append(arguments, "--")
		arguments = append(arguments, repository.pathspecs("")...)
		if repository.validateIdentity() != nil {
			return nodeprotocol.GitRunWorkspaceSnapshot{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
		}
		output, diffErr := s.runGit(ctx, repository, remaining, false, arguments...)
		if diffErr != nil {
			return nodeprotocol.GitRunWorkspaceSnapshot{}, diffErr
		}
		if authorizationErr := validatePatchPaths(output.data, repository, nil); authorizationErr != nil {
			return nodeprotocol.GitRunWorkspaceSnapshot{}, authorizationErr
		}
		patch, binaryPaths := filterSnapshotPatchBytes(output.data, conflictPaths)
		snapshot.Areas[index+1].Patch = patch
		snapshot.Areas[index+1].BinaryPaths = binaryPaths
		remaining -= len(output.data)
		if output.truncated {
			snapshot.Areas[index+1].Truncated = true
			snapshot.Truncated = true
			snapshot.Warnings = append(snapshot.Warnings,
				warning(nodeprotocol.GitWarningPatchTruncated))
		}
	}

	for index := range snapshot.Areas {
		sort.Strings(snapshot.Areas[index].Paths)
		sort.Strings(snapshot.Areas[index].BinaryPaths)
	}
	sort.Slice(snapshot.Untracked, func(i, j int) bool {
		return snapshot.Untracked[i].Path < snapshot.Untracked[j].Path
	})
	if repository.validateIdentity() != nil {
		s.expireProjectForRepository(request.ProjectID, repository, "repository_replaced")
		return nodeprotocol.GitRunWorkspaceSnapshot{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	if err := s.validateExecutionAuthorization(ctx, request, repository); err != nil {
		return nodeprotocol.GitRunWorkspaceSnapshot{}, err
	}
	enforceRunSnapshotPayloadLimit(&snapshot)
	snapshot.ContentHash = runSnapshotContentHash(snapshot)
	return snapshot, nil
}

// enforceRunSnapshotPayloadLimit bounds the actual JSON representation, not
// only the raw Git output. JSON escaping can expand otherwise bounded source
// text (for example HTML-heavy patches), so a pathological aggregate patch is
// reduced to path/binary metadata before it reaches persistence or HTTP code.
func enforceRunSnapshotPayloadLimit(snapshot *nodeprotocol.GitRunWorkspaceSnapshot) {
	snapshot.ContentHash = ""
	if estimateRunSnapshotJSONBytes(*snapshot) <= maxRunSnapshotJSONBytes {
		return
	}
	dropped := false
	for index := range snapshot.Areas {
		if snapshot.Areas[index].Patch == "" {
			continue
		}
		snapshot.Areas[index].Patch = ""
		snapshot.Areas[index].Truncated = true
		dropped = true
	}
	if !dropped {
		return
	}
	snapshot.Truncated = true
	snapshot.Warnings = append(snapshot.Warnings,
		warning(nodeprotocol.GitWarningSnapshotPayloadTruncated))
	// Path metadata has its own 1 MiB source budget. This loop is a final
	// serialization guard for worst-case JSON escaping and future field growth.
	for estimateRunSnapshotJSONBytes(*snapshot) > maxRunSnapshotJSONBytes {
		trimmed := false
		for index := len(snapshot.Untracked) - 1; index >= 0; index-- {
			snapshot.Untracked = snapshot.Untracked[:index]
			trimmed = true
			break
		}
		if !trimmed {
			for index := len(snapshot.Areas) - 1; index >= 0 && !trimmed; index-- {
				if count := len(snapshot.Areas[index].Paths); count > 0 {
					snapshot.Areas[index].Paths = snapshot.Areas[index].Paths[:count-1]
					trimmed = true
				} else if count := len(snapshot.Areas[index].BinaryPaths); count > 0 {
					snapshot.Areas[index].BinaryPaths = snapshot.Areas[index].BinaryPaths[:count-1]
					trimmed = true
				}
			}
		}
		if !trimmed {
			break
		}
	}
}

func estimateRunSnapshotJSONBytes(snapshot nodeprotocol.GitRunWorkspaceSnapshot) int {
	// Fixed field names, punctuation, booleans, numbers, and timestamps are
	// conservatively covered here. String payloads use an upper bound matching
	// encoding/json's escaping rules without materializing a second giant JSON.
	total := 64 << 10
	add := func(value string) { total += jsonEscapedStringBytes(value) + 8 }
	add(snapshot.RepositoryID)
	add(snapshot.Label)
	add(snapshot.RootDisplay)
	add(snapshot.Phase)
	add(snapshot.CapturedAt.Format(time.RFC3339Nano))
	add(snapshot.ContentHash)
	for _, area := range snapshot.Areas {
		total += 256
		add(area.Area)
		add(area.Patch)
		for _, path := range area.Paths {
			add(path)
		}
		for _, path := range area.BinaryPaths {
			add(path)
		}
	}
	for _, entry := range snapshot.Untracked {
		total += 128
		add(entry.Path)
		add(entry.Kind)
	}
	for _, item := range snapshot.Warnings {
		total += 128
		add(item.Code)
		add(item.Message)
	}
	return total
}

func jsonEscapedStringBytes(value string) int {
	total := 2 // quotes
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			total += 6
			value = value[1:]
			continue
		}
		value = value[size:]
		switch {
		case r < 0x20, r == '<', r == '>', r == '&', r == '\u2028', r == '\u2029':
			total += 6
		case r == '"' || r == '\\':
			total += 2
		default:
			total += size
		}
	}
	return total
}

func boundedSnapshotPath(value string) string {
	display := safeDisplay(value)
	if display == "" || filepath.IsAbs(display) || strings.HasPrefix(display, "../") || display == ".." {
		return ""
	}
	const maxPathBytes = 4 << 10
	if len(display) > maxPathBytes {
		return ""
	}
	return filepath.ToSlash(display)
}

func snapshotNoisePath(value string) bool {
	normalized := strings.ToLower(filepath.ToSlash(value))
	return strings.HasPrefix(normalized, ".teamva/tool-") ||
		strings.Contains(normalized, "/.teamva/tool-")
}

func filterSnapshotPatchBytes(patch []byte, conflictPaths map[string]struct{}) (string, []string) {
	var output bytes.Buffer
	output.Grow(len(patch))
	binarySeen := make(map[string]struct{})
	for offset := 0; offset < len(patch); {
		if !bytes.HasPrefix(patch[offset:], []byte("diff --")) {
			next := bytes.Index(patch[offset:], []byte("diff --"))
			if next < 0 {
				break
			}
			offset += next
		}
		next := bytes.Index(patch[offset+1:], []byte("\ndiff --"))
		end := len(patch)
		if next >= 0 {
			end = offset + 1 + next + 1
		}
		section := patch[offset:end]
		paths, binary, combined := snapshotPatchSectionMetadata(section)
		conflict := combined
		allNoise := len(paths) > 0
		for _, candidatePath := range paths {
			if _, exists := conflictPaths[candidatePath]; exists {
				conflict = true
			}
			if !snapshotNoisePath(candidatePath) {
				allNoise = false
			}
		}
		if !conflict && !allNoise {
			_, _ = output.Write(section)
			if binary && len(paths) > 0 {
				binarySeen[paths[len(paths)-1]] = struct{}{}
			}
		}
		offset = end
	}
	binaryPaths := make([]string, 0, len(binarySeen))
	for value := range binarySeen {
		binaryPaths = append(binaryPaths, value)
	}
	return strings.ToValidUTF8(output.String(), "�"), binaryPaths
}

func snapshotPatchSectionMetadata(section []byte) ([]string, bool, bool) {
	paths := make([]string, 0, 4)
	seen := make(map[string]struct{})
	appendPath := func(value string, prefixed bool) {
		decoded, ok := decodeWholeGitPath(strings.TrimSpace(value))
		if !ok || decoded == "/dev/null" {
			return
		}
		decoded = filepath.ToSlash(decoded)
		if prefixed && (strings.HasPrefix(decoded, "a/") || strings.HasPrefix(decoded, "b/")) {
			decoded = decoded[2:]
		}
		normalized, _, ok := normalizeStatusPath(decoded)
		if !ok {
			return
		}
		display := boundedSnapshotPath(normalized)
		if display == "" {
			return
		}
		if _, exists := seen[display]; !exists {
			seen[display] = struct{}{}
			paths = append(paths, display)
		}
	}
	binaryPatch := false
	combined := false
	first := true
	inHunk := false
	for len(section) > 0 {
		end := bytes.IndexByte(section, '\n')
		line := section
		if end >= 0 {
			line = section[:end]
			section = section[end+1:]
		} else {
			section = nil
		}
		if !first && inHunk {
			continue
		}
		if !first && bytes.HasPrefix(line, []byte("@@")) {
			inHunk = true
			continue
		}
		text := string(line)
		if first {
			first = false
			switch {
			case strings.HasPrefix(text, "diff --cc "):
				combined = true
				appendPath(strings.TrimPrefix(text, "diff --cc "), false)
			case strings.HasPrefix(text, "diff --combined "):
				combined = true
				appendPath(strings.TrimPrefix(text, "diff --combined "), false)
			case strings.HasPrefix(text, "diff --git "):
				left, rest, leftOK := consumeQuotedGitToken(strings.TrimPrefix(text, "diff --git "))
				right, _, rightOK := consumeQuotedGitToken(rest)
				if leftOK {
					appendPath(quotePatchPath(left), true)
				}
				if rightOK {
					appendPath(quotePatchPath(right), true)
				}
			}
		}
		switch {
		case strings.HasPrefix(text, "--- "):
			appendPath(strings.TrimPrefix(text, "--- "), true)
		case strings.HasPrefix(text, "+++ "):
			appendPath(strings.TrimPrefix(text, "+++ "), true)
		case strings.HasPrefix(text, "rename from "):
			appendPath(strings.TrimPrefix(text, "rename from "), false)
		case strings.HasPrefix(text, "rename to "):
			appendPath(strings.TrimPrefix(text, "rename to "), false)
		case strings.HasPrefix(text, "copy from "):
			appendPath(strings.TrimPrefix(text, "copy from "), false)
		case strings.HasPrefix(text, "copy to "):
			appendPath(strings.TrimPrefix(text, "copy to "), false)
		case strings.HasPrefix(text, "Binary files ") && strings.HasSuffix(text, " differ"):
			binaryPatch = true
		}
	}
	return paths, binaryPatch, combined
}

func runSnapshotContentHash(snapshot nodeprotocol.GitRunWorkspaceSnapshot) string {
	snapshot.ContentHash = ""
	hash := sha256.New()
	_, _ = hash.Write([]byte("teamva/git-run-snapshot/v2"))
	writeString := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	writeInt := func(value int64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(value))
		_, _ = hash.Write(encoded[:])
	}
	writeBool := func(value bool) {
		if value {
			writeInt(1)
		} else {
			writeInt(0)
		}
	}
	writeString(snapshot.RepositoryID)
	writeString(snapshot.Label)
	writeString(snapshot.RootDisplay)
	writeString(snapshot.Phase)
	writeString(snapshot.CapturedAt.UTC().Format(time.RFC3339Nano))
	writeBool(snapshot.Truncated)
	writeInt(int64(len(snapshot.Areas)))
	for _, area := range snapshot.Areas {
		writeString(area.Area)
		writeString(area.Patch)
		writeBool(area.Truncated)
		writeInt(int64(len(area.Paths)))
		for _, value := range area.Paths {
			writeString(value)
		}
		writeInt(int64(len(area.BinaryPaths)))
		for _, value := range area.BinaryPaths {
			writeString(value)
		}
	}
	writeInt(int64(len(snapshot.Untracked)))
	for _, item := range snapshot.Untracked {
		writeString(item.Path)
		writeString(item.Kind)
		writeInt(item.SizeBytes)
	}
	writeInt(int64(len(snapshot.Warnings)))
	for _, item := range snapshot.Warnings {
		writeString(item.Code)
		writeString(item.Message)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
