package gitread

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func (s *Service) readDiff(ctx context.Context, repository *repository, snapshot *changeSnapshot, change internalChange, contextLines int, allowLarge bool) (nodeprotocol.GitDiffResult, error) {
	if repository.validateIdentity() != nil {
		return nodeprotocol.GitDiffResult{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	result := nodeprotocol.GitDiffResult{
		SnapshotID: snapshot.snapshotID, RepositoryID: repository.id, ChangeID: change.public.ID,
		Generation: snapshot.generation, Comparison: comparisonForArea(change.public.Area),
		Kind: change.public.Kind, Path: change.public.Path, OldPath: change.public.OldPath,
		Language: languageForPath(change.path), Stats: change.public.Stats,
	}
	if change.public.Kind == "directory" {
		setDiffMessage(&result, nodeprotocol.GitDiffMessageUntrackedDirectoryExpandRequired)
		return result, nil
	}
	if change.public.Area == "untracked" && change.public.Kind == "symlink" {
		setDiffMessage(&result, nodeprotocol.GitDiffMessageUntrackedSymlinkSummary)
		return result, nil
	}

	var untrackedPayload []byte
	if change.public.Area == "untracked" {
		readLimit := patchOutputLimit
		if !allowLarge {
			readLimit = diffConfirmationLimit
		}
		file, err := repository.secureRead(ctx, change.path, readLimit)
		result.Stats.Bytes = file.size
		if errors.Is(err, errFileLarge) {
			if !allowLarge {
				result.ConfirmRequired = true
				setDiffMessage(&result, nodeprotocol.GitDiffMessageConfirmationRequired)
				return result, nil
			}
			result.Truncated = true
			result.Truncation = &nodeprotocol.GitDiffTruncation{
				Truncated: true, Reason: nodeprotocol.GitDiffTruncationPatchLimit, LimitBytes: patchOutputLimit,
			}
			setDiffMessage(&result, nodeprotocol.GitDiffMessagePatchLimit)
			return result, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nodeprotocol.GitDiffResult{}, ctx.Err()
			}
			return nodeprotocol.GitDiffResult{}, gitError("change_unavailable", "变更文件已移动、已删除或不是安全的普通文件", false)
		}
		untrackedPayload = file.data
		result.Stats = statsForPayload(untrackedPayload)
		result.Stats.Bytes = file.size
		if imageMediaType(untrackedPayload) != "" {
			result.Stats.Binary = true
		}
	}
	if result.Stats == (nodeprotocol.GitDiffStats{}) && (change.public.Area == "staged" || change.public.Area == "unstaged") {
		if stats, err := s.readStatsForChange(ctx, repository, change); err == nil {
			result.Stats = stats
		}
	}
	if result.Stats.Lines == 0 {
		result.Stats.Lines = result.Stats.Additions + result.Stats.Deletions
	}
	if result.Stats.Additions+result.Stats.Deletions > 500 && !allowLarge {
		result.ConfirmRequired = true
		setDiffMessage(&result, nodeprotocol.GitDiffMessageLineConfirmationRequired)
		return result, nil
	}

	outputLimit := patchOutputLimit
	if !allowLarge {
		outputLimit = diffConfirmationLimit
	}
	var output gitOutput
	var err error
	if change.public.Area == "untracked" {
		output = renderUntrackedPatch(change.path, untrackedPayload, outputLimit)
	} else {
		arguments := diffArguments(change, contextLines)
		output, err = s.runGit(ctx, repository, outputLimit, false, arguments...)
	}
	if err != nil {
		var readError *nodeprotocol.GitReadError
		if errors.As(err, &readError) && readError.Code == "git_timeout" {
			result.Truncated = true
			result.Truncation = &nodeprotocol.GitDiffTruncation{
				Truncated: true, Reason: nodeprotocol.GitDiffTruncationTimeout,
			}
			setDiffMessage(&result, nodeprotocol.GitDiffMessageTimeout)
			return result, nil
		}
		return nodeprotocol.GitDiffResult{}, err
	}
	if output.truncated {
		if !allowLarge {
			result.ConfirmRequired = true
			setDiffMessage(&result, nodeprotocol.GitDiffMessageConfirmationRequired)
			return result, nil
		}
		result.Truncated = true
		result.Truncation = &nodeprotocol.GitDiffTruncation{
			Truncated: true, Reason: nodeprotocol.GitDiffTruncationPatchLimit, LimitBytes: patchOutputLimit,
		}
		setDiffMessage(&result, nodeprotocol.GitDiffMessagePatchLimit)
		return result, nil
	}
	if err := validatePatchAuthorization(output.data, repository, change); err != nil {
		return nodeprotocol.GitDiffResult{}, err
	}
	if !allowLarge && patchChangedLines(output.data) > 500 {
		result.ConfirmRequired = true
		setDiffMessage(&result, nodeprotocol.GitDiffMessageLineConfirmationRequired)
		return result, nil
	}
	var images *nodeprotocol.GitDiffImages
	imageTooLarge := false
	if result.Stats.Binary || imageMediaType(untrackedPayload) != "" {
		imageBudget := imageOutputLimit
		if !allowLarge {
			imageBudget = diffConfirmationLimit
		}
		images, imageTooLarge = s.readDiffImages(ctx, repository, change, untrackedPayload, imageBudget)
	}
	if err := ctx.Err(); err != nil {
		return nodeprotocol.GitDiffResult{}, err
	}
	if imageTooLarge && !allowLarge {
		result.ConfirmRequired = true
		setDiffMessage(&result, nodeprotocol.GitDiffMessageImageConfirmationRequired)
		return result, nil
	}
	if images != nil {
		result.Images = images
		result.Kind = "image"
		result.Stats.Binary = true
	} else if result.Stats.Binary || bytes.IndexByte(untrackedPayload, 0) >= 0 {
		result.Kind = "binary"
		result.Stats.Binary = true
	}
	result.Patch = string(output.data)
	return result, nil
}

func setDiffMessage(result *nodeprotocol.GitDiffResult, code string) {
	result.MessageCode = code
	result.Message = nodeprotocol.GitDiffMessage(code)
}

func patchChangedLines(patch []byte) int {
	count := 0
	for len(patch) > 0 {
		end := bytes.IndexByte(patch, '\n')
		line := patch
		if end >= 0 {
			line = patch[:end]
			patch = patch[end+1:]
		} else {
			patch = nil
		}
		if len(line) == 0 || (line[0] != '+' && line[0] != '-') ||
			bytes.HasPrefix(line, []byte("+++ ")) || bytes.HasPrefix(line, []byte("--- ")) {
			continue
		}
		count++
		if count > 500 {
			return count
		}
	}
	return count
}

func comparisonForArea(area string) string {
	switch area {
	case "staged":
		return "head_to_index"
	case "unstaged":
		return "index_to_worktree"
	case "untracked":
		return "empty_to_worktree"
	case "conflict":
		return "conflict"
	default:
		return "unknown"
	}
}

func diffArguments(change internalChange, contextLines int) []string {
	arguments := []string{
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames",
		"--src-prefix=a/", "--dst-prefix=b/", "--unified=" + strconvItoa(contextLines),
	}
	switch change.public.Area {
	case "staged":
		arguments = append(arguments, "--cached")
	}
	arguments = append(arguments, "--")
	if change.oldPath != "" {
		arguments = append(arguments, change.oldPath)
	}
	return append(arguments, change.path)
}

func renderUntrackedPatch(relative string, payload []byte, limit int) gitOutput {
	if len(payload) == 0 {
		return gitOutput{}
	}
	var output []byte
	appendBytes := func(value []byte) bool {
		remaining := limit - len(output)
		if remaining <= 0 {
			return false
		}
		if len(value) > remaining {
			output = append(output, value[:remaining]...)
			return false
		}
		output = append(output, value...)
		return true
	}
	appendValue := func(value string) bool { return appendBytes([]byte(value)) }
	aPath := quotePatchPath("a/" + relative)
	bPath := quotePatchPath("b/" + relative)
	if !appendValue("diff --git " + aPath + " " + bPath + "\nnew file mode 100644\n") {
		return gitOutput{data: output, truncated: true}
	}
	if bytes.IndexByte(payload, 0) >= 0 {
		complete := appendValue("Binary files /dev/null and " + bPath + " differ\n")
		return gitOutput{data: output, truncated: !complete}
	}
	lines := bytes.Count(payload, []byte{'\n'})
	if payload[len(payload)-1] != '\n' {
		lines++
	}
	if !appendValue("--- /dev/null\n+++ " + bPath + "\n@@ -0,0 +1," + strconv.Itoa(lines) + " @@\n") {
		return gitOutput{data: output, truncated: true}
	}
	remaining := payload
	for len(remaining) > 0 {
		end := bytes.IndexByte(remaining, '\n')
		if end < 0 {
			if !appendValue("+") || !appendBytes(remaining) || !appendValue("\n\\ No newline at end of file\n") {
				return gitOutput{data: output, truncated: true}
			}
			break
		}
		if !appendValue("+") || !appendBytes(remaining[:end+1]) {
			return gitOutput{data: output, truncated: true}
		}
		remaining = remaining[end+1:]
	}
	return gitOutput{data: output}
}

func quotePatchPath(value string) string {
	return strconv.Quote(filepath.ToSlash(value))
}

func validatePatchAuthorization(patch []byte, repository *repository, change internalChange) error {
	expected := map[string]struct{}{change.path: {}}
	if change.oldPath != "" {
		expected[change.oldPath] = struct{}{}
	}
	return validatePatchPaths(patch, repository, expected)
}

func validatePatchPaths(patch []byte, repository *repository, expected map[string]struct{}) error {
	if len(patch) == 0 {
		return nil
	}
	inHunk := false
	for len(patch) > 0 {
		end := bytes.IndexByte(patch, '\n')
		line := patch
		if end >= 0 {
			line = patch[:end]
			patch = patch[end+1:]
		} else {
			patch = nil
		}
		if inHunk && !bytes.HasPrefix(line, []byte("diff --")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("@@")) {
			inHunk = true
			continue
		}
		text := string(line)
		switch {
		case strings.HasPrefix(text, "diff --git "):
			inHunk = false
			left, rest, ok := consumeQuotedGitToken(strings.TrimPrefix(text, "diff --git "))
			if !ok {
				return unauthorizedPatchError()
			}
			right, trailing, ok := consumeQuotedGitToken(rest)
			if !ok || strings.TrimSpace(trailing) != "" ||
				!authorizedPatchPath(repository, expected, left, true) ||
				!authorizedPatchPath(repository, expected, right, true) {
				return unauthorizedPatchError()
			}
		case strings.HasPrefix(text, "diff --cc ") || strings.HasPrefix(text, "diff --combined "):
			inHunk = false
			value := strings.TrimPrefix(strings.TrimPrefix(text, "diff --cc "), "diff --combined ")
			decoded, ok := decodeWholeGitPath(value)
			if !ok || !authorizedPatchPath(repository, expected, decoded, false) {
				return unauthorizedPatchError()
			}
		case !inHunk && (strings.HasPrefix(text, "--- ") || strings.HasPrefix(text, "+++ ")):
			value := strings.TrimSpace(text[4:])
			decoded, ok := decodeWholeGitPath(value)
			if !ok || !authorizedPatchPath(repository, expected, decoded, true) {
				return unauthorizedPatchError()
			}
		case !inHunk && (strings.HasPrefix(text, "rename from ") || strings.HasPrefix(text, "rename to ") ||
			strings.HasPrefix(text, "copy from ") || strings.HasPrefix(text, "copy to ")):
			separator := strings.IndexByte(text, ' ')
			value := text[separator+1:]
			if strings.HasPrefix(value, "from ") || strings.HasPrefix(value, "to ") {
				value = value[strings.IndexByte(value, ' ')+1:]
			}
			decoded, ok := decodeWholeGitPath(value)
			if !ok || !authorizedPatchPath(repository, expected, decoded, false) {
				return unauthorizedPatchError()
			}
		}
	}
	return nil
}

func consumeQuotedGitToken(value string) (string, string, bool) {
	value = strings.TrimLeft(value, " ")
	if value == "" {
		return "", "", false
	}
	if value[0] != '"' {
		if split := strings.IndexByte(value, ' '); split >= 0 {
			return value[:split], value[split+1:], true
		}
		return value, "", true
	}
	escaped := false
	for index := 1; index < len(value); index++ {
		switch {
		case escaped:
			escaped = false
		case value[index] == '\\':
			escaped = true
		case value[index] == '"':
			decoded, err := strconv.Unquote(value[:index+1])
			return decoded, value[index+1:], err == nil
		}
	}
	return "", "", false
}

func decodeWholeGitPath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if value[0] != '"' {
		return value, true
	}
	decoded, rest, ok := consumeQuotedGitToken(value)
	return decoded, ok && strings.TrimSpace(rest) == ""
}

func authorizedPatchPath(repository *repository, expected map[string]struct{}, value string, prefixed bool) bool {
	if value == os.DevNull || value == "/dev/null" {
		return true
	}
	value = filepath.ToSlash(value)
	if prefixed && (strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/")) {
		value = value[2:]
	}
	cleaned, _, ok := normalizeStatusPath(value)
	if !ok || !repository.authorizes(cleaned) {
		return false
	}
	if expected == nil {
		return true
	}
	_, ok = expected[cleaned]
	return ok
}

func unauthorizedPatchError() error {
	return gitError("path_out_of_scope", "Git 差异包含未授权或非预期的路径元数据", false)
}

func strconvItoa(value int) string {
	// This tiny conversion avoids format strings in command construction and
	// makes it explicit that only a validated integer reaches git argv.
	if value == 0 {
		return "0"
	}
	digits := [3]byte{}
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

func (s *Service) readStatsForChange(ctx context.Context, repository *repository, change internalChange) (nodeprotocol.GitDiffStats, error) {
	arguments := []string{"diff", "--numstat", "-z", "--find-renames", "--no-ext-diff", "--no-textconv", "--no-color"}
	if change.public.Area == "staged" {
		arguments = append(arguments, "--cached")
	}
	arguments = append(arguments, "--")
	if change.oldPath != "" {
		arguments = append(arguments, change.oldPath)
	}
	arguments = append(arguments, change.path)
	output, err := s.runGit(ctx, repository, 1<<20, false, arguments...)
	if err != nil || output.truncated {
		return nodeprotocol.GitDiffStats{}, gitError("stats_unavailable", "差异统计暂时不可用", true)
	}
	stats := parseNumStats(output.data, repository)
	if value, exists := stats[change.path]; exists {
		return value, nil
	}
	return nodeprotocol.GitDiffStats{}, nil
}

func statsForPayload(payload []byte) nodeprotocol.GitDiffStats {
	if bytes.IndexByte(payload, 0) >= 0 {
		return nodeprotocol.GitDiffStats{Binary: true, Bytes: int64(len(payload))}
	}
	lines := bytes.Count(payload, []byte{'\n'})
	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		lines++
	}
	return nodeprotocol.GitDiffStats{Additions: lines, Lines: lines, Bytes: int64(len(payload))}
}

func (s *Service) readDiffImages(ctx context.Context, repository *repository, change internalChange, untrackedPayload []byte, budget int) (*nodeprotocol.GitDiffImages, bool) {
	var before, after []byte
	remaining := budget
	readBlob := func(object string) ([]byte, bool) {
		if remaining <= 0 {
			return nil, true
		}
		payload, tooLarge := s.readBlob(ctx, repository, object, remaining)
		remaining -= len(payload)
		return payload, tooLarge
	}
	switch change.public.Area {
	case "staged":
		beforePath := change.path
		if change.oldPath != "" {
			beforePath = change.oldPath
		}
		var tooLarge bool
		before, tooLarge = readBlob("HEAD:" + beforePath)
		if tooLarge {
			return nil, true
		}
		after, tooLarge = readBlob(":" + change.path)
		if tooLarge {
			return nil, true
		}
	case "unstaged":
		var tooLarge bool
		before, tooLarge = readBlob(":" + change.path)
		if tooLarge {
			return nil, true
		}
		metadata, metadataErr := repository.secureMetadata(change.path)
		if metadataErr == nil && metadata.mode == "100644" && metadata.size > int64(remaining) {
			return nil, true
		}
		if file, err := repository.secureRead(ctx, change.path, remaining); err == nil {
			after = file.data
			remaining -= len(after)
		} else if errors.Is(err, errFileLarge) {
			return nil, true
		}
	case "untracked":
		if len(untrackedPayload) <= remaining {
			after = untrackedPayload
		} else {
			return nil, true
		}
	default:
		return nil, false
	}
	beforeImage := imageData(before)
	afterImage := imageData(after)
	if beforeImage == nil && afterImage == nil {
		return nil, false
	}
	return &nodeprotocol.GitDiffImages{Before: beforeImage, After: afterImage}, false
}

func (s *Service) readBlob(ctx context.Context, repository *repository, object string, limit int) ([]byte, bool) {
	if limit <= 0 {
		return nil, true
	}
	output, err := s.runGit(ctx, repository, limit, false, "cat-file", "blob", object)
	if output.truncated {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	return output.data, false
}

func imageData(payload []byte) *nodeprotocol.GitDiffImage {
	if len(payload) == 0 || len(payload) > imageOutputLimit {
		return nil
	}
	mediaType := imageMediaType(payload)
	if mediaType == "" {
		return nil
	}
	return &nodeprotocol.GitDiffImage{
		MediaType: mediaType, SizeBytes: int64(len(payload)),
		DataURL: "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(payload),
	}
}

func imageMediaType(payload []byte) string {
	switch {
	case len(payload) >= 8 && bytes.Equal(payload[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case len(payload) >= 3 && payload[0] == 0xff && payload[1] == 0xd8 && payload[2] == 0xff:
		return "image/jpeg"
	case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

func languageForPath(value string) string {
	extension := strings.ToLower(filepath.Ext(value))
	languages := map[string]string{
		".go": "go", ".js": "javascript", ".jsx": "javascript", ".ts": "typescript", ".tsx": "typescript",
		".py": "python", ".rb": "ruby", ".rs": "rust", ".java": "java", ".kt": "kotlin",
		".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
		".css": "css", ".scss": "scss", ".html": "html", ".htm": "html", ".svg": "svg",
		".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".xml": "xml",
		".md": "markdown", ".sh": "shell", ".zsh": "shell", ".sql": "sql",
	}
	return languages[extension]
}
