package gitread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strconv"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type parsedStatus struct {
	branch      nodeprotocol.GitBranchSummary
	items       []statusEntry
	counts      nodeprotocol.GitChangeCounts
	fingerprint string
	warnings    []nodeprotocol.GitRepositoryWarning
	truncated   bool
}

type statusEntry struct {
	path       string
	oldPath    string
	directory  bool
	index      byte
	worktree   byte
	headMode   string
	indexMode  string
	workMode   string
	sub        string
	similarity int
	conflict   bool
}

type changesResult struct {
	items     []internalChange
	counts    nodeprotocol.GitChangeCounts
	warnings  []nodeprotocol.GitRepositoryWarning
	truncated bool
}

const maxStatusPathBytes = 1 << 20

func (s *Service) readRepositoryStatus(ctx context.Context, repository *repository) (nodeprotocol.GitBranchSummary, nodeprotocol.GitChangeCounts, string, error) {
	status, err := s.readStatus(ctx, repository, "", false)
	if err != nil {
		return nodeprotocol.GitBranchSummary{}, nodeprotocol.GitChangeCounts{}, "", err
	}
	return status.branch, status.counts, status.fingerprint, nil
}

func (s *Service) readChanges(ctx context.Context, repository *repository, area, prefix string, expand bool) (changesResult, nodeprotocol.GitBranchSummary, string, error) {
	status, err := s.readStatus(ctx, repository, prefix, expand)
	if err != nil {
		return changesResult{}, nodeprotocol.GitBranchSummary{}, "", err
	}
	result := changesResult{counts: status.counts, warnings: status.warnings, truncated: status.truncated}
	areas := []string{area}
	if area == "" {
		areas = []string{"conflict", "staged", "unstaged", "untracked"}
	}
	for _, selectedArea := range areas {
		for _, entry := range status.items {
			var code byte
			switch selectedArea {
			case "conflict":
				if !entry.conflict {
					continue
				}
				code = 'U'
			case "staged":
				if entry.conflict || unchanged(entry.index) || entry.index == '?' {
					continue
				}
				code = entry.index
			case "unstaged":
				if entry.conflict || unchanged(entry.worktree) || entry.worktree == '?' {
					continue
				}
				code = entry.worktree
			case "untracked":
				if entry.index != '?' {
					continue
				}
				code = '?'
			}
			change := makeInternalChange(selectedArea, code, entry)
			result.items = append(result.items, change)
		}
	}
	statsAreas := []string{}
	if area == "" {
		statsAreas = []string{"staged", "unstaged"}
	} else if area == "staged" || area == "unstaged" {
		statsAreas = []string{area}
	}
	for _, statsArea := range statsAreas {
		stats, statsErr := s.readNumStats(ctx, repository, statsArea, prefix)
		if statsErr != nil {
			result.warnings = append(result.warnings, warning(nodeprotocol.GitWarningStatsUnavailable))
			continue
		}
		for index := range result.items {
			if result.items[index].public.Area != statsArea {
				continue
			}
			if stat, ok := stats[result.items[index].path]; ok {
				result.items[index].public.Stats = stat
				result.items[index].public.Binary = stat.Binary
				result.items[index].public.Additions = stat.Additions
				result.items[index].public.Deletions = stat.Deletions
			}
		}
	}
	sort.SliceStable(result.items, func(i, j int) bool {
		if result.items[i].public.Area != result.items[j].public.Area {
			return areaOrder(result.items[i].public.Area) < areaOrder(result.items[j].public.Area)
		}
		if result.items[i].path == result.items[j].path {
			if result.items[i].oldPath == result.items[j].oldPath {
				return result.items[i].public.Status < result.items[j].public.Status
			}
			return result.items[i].oldPath < result.items[j].oldPath
		}
		return result.items[i].path < result.items[j].path
	})
	return result, status.branch, status.fingerprint, nil
}

func (s *Service) readStatus(ctx context.Context, repository *repository, prefix string, expand bool) (parsedStatus, error) {
	if repository.validateIdentity() != nil {
		return parsedStatus{}, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	untrackedMode := "normal"
	if expand {
		untrackedMode = "all"
	}
	arguments := []string{
		"status", "--porcelain=v2", "-z", "--branch", "--find-renames=50%",
		"--untracked-files=" + untrackedMode, "--ignore-submodules=none",
	}
	arguments = append(arguments, "--")
	arguments = append(arguments, repository.pathspecs(prefix)...)
	output, err := s.runGit(ctx, repository, statusOutputLimit, false, arguments...)
	if err != nil {
		return parsedStatus{}, err
	}
	result := parseStatusOutput(output.data, repository)
	if output.truncated {
		result.truncated = true
		appendWarningOnce(&result.warnings,
			warning(nodeprotocol.GitWarningStatusOutputTruncated))
	}
	hash := sha256.Sum256(output.data)
	result.fingerprint = hex.EncodeToString(hash[:])
	return result, nil
}

func parseStatusOutput(payload []byte, repository *repository) parsedStatus {
	result := parsedStatus{warnings: []nodeprotocol.GitRepositoryWarning{}}
	remaining := payload
	for bytes.HasPrefix(remaining, []byte("# ")) {
		lineEnd := bytes.IndexByte(remaining, '\n')
		nulEnd := bytes.IndexByte(remaining, 0)
		end := lineEnd
		if end < 0 || (nulEnd >= 0 && nulEnd < end) {
			end = nulEnd
		}
		if end < 0 {
			end = len(remaining)
		}
		parseBranchHeader(string(remaining[:end]), &result.branch)
		if end >= len(remaining) {
			remaining = nil
			break
		}
		remaining = remaining[end+1:]
	}
	pathBytes := 0
	for len(remaining) > 0 {
		record, rest := nextNULRecord(remaining)
		remaining = rest
		if len(record) < 2 {
			continue
		}
		if bytes.HasPrefix(record, []byte("# ")) {
			parseBranchHeader(string(record), &result.branch)
			continue
		}
		entry, rename, ok := parseStatusRecord(record)
		if !ok {
			continue
		}
		if rename {
			if len(remaining) == 0 {
				continue
			}
			oldPath, rest := nextNULRecord(remaining)
			remaining = rest
			entry.oldPath = string(oldPath)
		}
		entry.path, entry.directory, ok = normalizeStatusPath(entry.path)
		if !ok || !repository.authorizes(entry.path) {
			continue
		}
		if entry.oldPath != "" {
			oldPath, _, oldOK := normalizeStatusPath(entry.oldPath)
			if oldOK && repository.authorizes(oldPath) {
				entry.oldPath = oldPath
			} else {
				entry.oldPath = ""
			}
		}
		switch {
		case entry.conflict:
			result.counts.Conflict++
		case entry.index == '?':
			result.counts.Untracked++
		default:
			if !unchanged(entry.index) {
				result.counts.Staged++
			}
			if !unchanged(entry.worktree) {
				result.counts.Unstaged++
			}
		}
		entryPathBytes := len(entry.path) + len(entry.oldPath)
		if len(result.items) >= maxSnapshotChanges || pathBytes+entryPathBytes > maxStatusPathBytes {
			result.truncated = true
			continue
		}
		if entry.index == '?' {
			if metadata, modeErr := repository.secureMetadata(entry.path); modeErr == nil {
				entry.workMode = metadata.mode
				if metadata.mode == "040000" {
					entry.directory = true
				}
			}
		}
		result.items = append(result.items, entry)
		pathBytes += entryPathBytes
	}
	if result.truncated {
		result.warnings = append(result.warnings,
			warning(nodeprotocol.GitWarningChangesTruncated))
	}
	return result
}

func nextNULRecord(payload []byte) ([]byte, []byte) {
	end := bytes.IndexByte(payload, 0)
	if end < 0 {
		return payload, nil
	}
	return payload[:end], payload[end+1:]
}

func parseBranchHeader(line string, branch *nodeprotocol.GitBranchSummary) {
	switch {
	case strings.HasPrefix(line, "# branch.oid "):
		value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
		if value == "(initial)" {
			branch.Unborn = true
		} else {
			branch.OID = value
		}
	case strings.HasPrefix(line, "# branch.head "):
		value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
		if value == "(detached)" {
			branch.Detached = true
		} else {
			branch.Name = safeDisplay(value)
		}
	case strings.HasPrefix(line, "# branch.upstream "):
		branch.Upstream = safeDisplay(strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream ")))
	case strings.HasPrefix(line, "# branch.ab "):
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "# branch.ab ")))
		for _, field := range fields {
			if strings.HasPrefix(field, "+") {
				branch.Ahead, _ = strconv.Atoi(strings.TrimPrefix(field, "+"))
			} else if strings.HasPrefix(field, "-") {
				branch.Behind, _ = strconv.Atoi(strings.TrimPrefix(field, "-"))
			}
		}
	}
}

func parseStatusRecord(record []byte) (statusEntry, bool, bool) {
	value := string(record)
	switch record[0] {
	case '1':
		fields := strings.SplitN(value, " ", 9)
		if len(fields) != 9 || len(fields[1]) != 2 {
			return statusEntry{}, false, false
		}
		return statusEntry{
			path: fields[8], index: fields[1][0], worktree: fields[1][1],
			sub: fields[2], headMode: fields[3], indexMode: fields[4], workMode: fields[5],
		}, false, true
	case '2':
		fields := strings.SplitN(value, " ", 10)
		if len(fields) != 10 || len(fields[1]) != 2 {
			return statusEntry{}, false, false
		}
		similarity := 0
		if len(fields[8]) > 1 {
			similarity, _ = strconv.Atoi(fields[8][1:])
		}
		return statusEntry{
			path: fields[9], index: fields[1][0], worktree: fields[1][1],
			sub: fields[2], headMode: fields[3], indexMode: fields[4], workMode: fields[5],
			similarity: similarity,
		}, true, true
	case 'u':
		fields := strings.SplitN(value, " ", 11)
		if len(fields) != 11 || len(fields[1]) != 2 {
			return statusEntry{}, false, false
		}
		return statusEntry{
			path: fields[10], index: fields[1][0], worktree: fields[1][1], sub: fields[2],
			headMode:  preferredMode(fields[3], fields[4], fields[5]),
			indexMode: preferredMode(fields[4], fields[5], fields[3]), workMode: fields[6],
			conflict: true,
		}, false, true
	case '?':
		if len(record) < 3 || record[1] != ' ' {
			return statusEntry{}, false, false
		}
		return statusEntry{path: string(record[2:]), index: '?', worktree: '?'}, false, true
	default:
		return statusEntry{}, false, false
	}
}

func normalizeStatusPath(value string) (string, bool, bool) {
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") {
		return "", false, false
	}
	directory := strings.HasSuffix(value, "/")
	cleaned := path.Clean(strings.TrimSuffix(value, "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false, false
	}
	return cleaned, directory, true
}

func preferredMode(values ...string) string {
	for _, value := range values {
		if value != "" && value != "000000" {
			return value
		}
	}
	return ""
}

func unchanged(code byte) bool {
	return code == 0 || code == '.' || code == ' '
}

func makeInternalChange(area string, code byte, entry statusEntry) internalChange {
	status := statusName(code)
	mode := entry.modeForArea(area)
	kind := modeKind(mode)
	if entry.directory {
		kind = "directory"
	}
	change := internalChange{
		path: entry.path, indexStatus: entry.index, workStatus: entry.worktree,
		mode: mode, sub: entry.sub,
		public: nodeprotocol.GitChange{
			Area: area, Status: status, Kind: kind, DisplayPath: safeDisplay(entry.path),
			Stats: nodeprotocol.GitDiffStats{},
		},
	}
	if entry.conflict {
		change.public.Status = "unmerged"
		change.public.Conflict = &nodeprotocol.GitConflict{
			IndexStatus: statusName(entry.index), WorktreeStatus: statusName(entry.worktree),
		}
		change.public.ConflictCode = string([]byte{entry.index, entry.worktree})
	}
	if (code == 'R' || code == 'C') && entry.oldPath != "" {
		change.oldPath = entry.oldPath
		change.public.OldDisplayPath = safeDisplay(entry.oldPath)
		change.public.Rename = &nodeprotocol.GitRename{
			From: safeDisplay(entry.oldPath), To: safeDisplay(entry.path), Similarity: entry.similarity,
		}
		change.public.Similarity = entry.similarity
	}
	if submodule := parseSubmodule(entry.sub); submodule != nil {
		change.public.Kind = "submodule"
		change.public.Submodule = submodule
	}
	return change
}

func (entry statusEntry) modeForArea(area string) string {
	switch area {
	case "staged":
		return preferredMode(entry.indexMode, entry.headMode, entry.workMode)
	case "unstaged", "untracked":
		return preferredMode(entry.workMode, entry.indexMode, entry.headMode)
	case "conflict":
		return preferredMode(entry.workMode, entry.indexMode, entry.headMode)
	default:
		return preferredMode(entry.workMode, entry.indexMode, entry.headMode)
	}
}

func areaOrder(area string) int {
	switch area {
	case "conflict":
		return 0
	case "staged":
		return 1
	case "unstaged":
		return 2
	case "untracked":
		return 3
	default:
		return 4
	}
}

func statusName(code byte) string {
	switch code {
	case 'M':
		return "modified"
	case 'T':
		return "type_changed"
	case 'A', '?':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'U':
		return "unmerged"
	default:
		return "modified"
	}
}

func modeKind(mode string) string {
	switch mode {
	case "120000":
		return "symlink"
	case "160000":
		return "submodule"
	default:
		return "file"
	}
}

func parseSubmodule(value string) *nodeprotocol.GitSubmodule {
	if len(value) != 4 || value[0] != 'S' {
		return nil
	}
	return &nodeprotocol.GitSubmodule{
		HeadChanged: value[1] != '.', ModifiedContent: value[2] != '.', UntrackedContent: value[3] != '.',
	}
}

func (r *repository) pathspecs(prefix string) []string {
	if prefix != "" {
		return []string{prefix}
	}
	for _, allowed := range r.authPrefixes {
		if allowed == "" {
			return nil
		}
	}
	return append([]string(nil), r.authPrefixes...)
}

func (s *Service) readNumStats(ctx context.Context, repository *repository, area, prefix string) (map[string]nodeprotocol.GitDiffStats, error) {
	if repository.validateIdentity() != nil {
		return nil, gitError("snapshot_expired", "Git 仓库路径已被替换，请刷新后重试", true)
	}
	arguments := []string{"diff", "--numstat", "-z", "--find-renames", "--no-ext-diff", "--no-textconv", "--no-color"}
	if area == "staged" {
		arguments = append(arguments, "--cached")
	}
	arguments = append(arguments, "--")
	arguments = append(arguments, repository.pathspecs(prefix)...)
	output, err := s.runGit(ctx, repository, statusOutputLimit, false, arguments...)
	if err != nil {
		return nil, err
	}
	if output.truncated {
		return nil, gitError("stats_too_large", "Git 变更统计超过安全读取上限", false)
	}
	return parseNumStats(output.data, repository), nil
}

func parseNumStats(payload []byte, repository *repository) map[string]nodeprotocol.GitDiffStats {
	result := make(map[string]nodeprotocol.GitDiffStats)
	remaining := payload
	for len(remaining) > 0 {
		record, rest := nextNULRecord(remaining)
		remaining = rest
		if len(record) == 0 {
			continue
		}
		fields := strings.SplitN(string(record), "\t", 3)
		if len(fields) != 3 {
			continue
		}
		filePath := fields[2]
		if filePath == "" && len(remaining) > 0 {
			// With -z, rename/copy records carry an empty pathname followed by
			// NUL-terminated old and new pathnames.
			_, rest = nextNULRecord(remaining)
			newPath, afterNew := nextNULRecord(rest)
			remaining = afterNew
			if len(newPath) == 0 && len(rest) == 0 {
				continue
			}
			filePath = string(newPath)
		}
		filePath, _, ok := normalizeStatusPath(filePath)
		if !ok || !repository.authorizes(filePath) {
			continue
		}
		stat := nodeprotocol.GitDiffStats{}
		if fields[0] == "-" || fields[1] == "-" {
			stat.Binary = true
		} else {
			stat.Additions, _ = strconv.Atoi(fields[0])
			stat.Deletions, _ = strconv.Atoi(fields[1])
		}
		result[filePath] = stat
	}
	return result
}
