package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

const (
	maxProviderReportPatchBytes   = 8 << 20
	maxProviderResponsePatchBytes = 8 << 20
	maxProviderReportsJSONBytes   = 9 << 20
	maxProviderReportPaths        = 10_000
	maxProviderReportPathBytes    = 1 << 20
	maxProviderReportsPerResponse = 100
	maxProviderScannedEvents      = 5_000
	providerEventScanBatch        = 500
	// Project workspaces capture start/end observations for every discovered
	// repository. Aggregate byte/path limits below remain the stronger guard.
	maxRunGitSnapshots          = nodeprotocol.MaxGitRepositoriesPerRun * 2
	maxRunGitSnapshotAreas      = 4
	maxRunGitSnapshotPatchBytes = 8 << 20
	maxRunGitSnapshotPathBytes  = 1 << 20
	maxRunGitSnapshotPaths      = 10_000
	maxRunGitSnapshotWarnings   = 100
)

type runGitSnapshotReader interface {
	GetRunGitSnapshots(context.Context, string) ([]nodeprotocol.GitRunWorkspaceSnapshot, error)
}

func (a *API) getRunGitChanges(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	runID := c.Param("run_id")
	userID := authenticatedUser(c).ID
	after, limit, ok := providerReportPage(c)
	if !ok {
		return
	}
	// Scope the opaque Run ID before it can cross into the local Runtime.
	if _, err := a.store.GetRun(c.Request.Context(), userID, runID); err != nil {
		respondError(c, err)
		return
	}
	reader, ok := a.runtime.(runGitSnapshotReader)
	if !ok || reader == nil {
		respondGitError(c, &nodeprotocol.GitReadError{
			Code: "capability_unsupported", Message: "当前运行模式不支持 Run Git 历史读取",
		})
		return
	}
	snapshots, err := reader.GetRunGitSnapshots(c.Request.Context(), runID)
	if err != nil {
		respondGitError(c, err)
		return
	}
	snapshots = normalizeRunGitSnapshots(snapshots)
	baseResult := nodeprotocol.GitRunChangesResult{
		RunID: runID, Snapshots: snapshots,
		ProviderReports: []nodeprotocol.GitProviderChangeReport{},
	}
	basePayload, encodeErr := encodeBoundedResponse(c, baseResult, 0, maxGitJSONResponseBytes)
	if errors.Is(encodeErr, errJSONResponseBudget) {
		dropRunGitSnapshotPatches(snapshots)
		baseResult.Snapshots = snapshots
		basePayload, encodeErr = encodeBoundedResponse(c, baseResult, 0, maxGitJSONResponseBytes)
	}
	if encodeErr != nil {
		if errors.Is(encodeErr, errJSONResponseBudget) {
			respondError(c, responseTooLargeError())
		} else {
			respondError(c, encodeErr)
		}
		return
	}
	reportBudget := maxGitJSONResponseBytes - len(basePayload) - (64 << 10)
	if reportBudget < 1024 {
		reportBudget = 1024
	}
	if reportBudget > maxProviderReportsJSONBytes {
		reportBudget = maxProviderReportsJSONBytes
	}
	reports, truncated, nextCursor, err := a.scanProviderChangeReportsWithBudget(
		c.Request.Context(), userID, runID, after, limit, reportBudget,
	)
	if err != nil {
		if errors.Is(err, errJSONResponseBudget) {
			respondError(c, responseTooLargeError())
		} else {
			respondError(c, err)
		}
		return
	}
	result := nodeprotocol.GitRunChangesResult{
		RunID: runID, Snapshots: snapshots, ProviderReports: reports,
		ProviderReportsTruncated: truncated, NextProviderCursor: nextCursor,
	}
	for {
		payload, responseErr := encodeBoundedResponse(c, result, 0, maxGitJSONResponseBytes)
		if responseErr == nil {
			writeEncodedJSON(c, http.StatusOK, payload)
			return
		}
		if !errors.Is(responseErr, errJSONResponseBudget) {
			respondError(c, responseErr)
			return
		}
		if len(result.ProviderReports) > 0 {
			result.ProviderReports = result.ProviderReports[:len(result.ProviderReports)-1]
			result.ProviderReportsTruncated = true
			cursor := after
			if len(result.ProviderReports) > 0 {
				cursor = result.ProviderReports[len(result.ProviderReports)-1].Sequence
			}
			result.NextProviderCursor = strconv.FormatInt(cursor, 10)
			continue
		}
		if dropRunGitSnapshotPatches(result.Snapshots) {
			continue
		}
		respondError(c, responseTooLargeError())
		return
	}
}

func normalizeRunGitSnapshots(
	input []nodeprotocol.GitRunWorkspaceSnapshot,
) []nodeprotocol.GitRunWorkspaceSnapshot {
	droppedSnapshots := len(input) > maxRunGitSnapshots
	if len(input) > maxRunGitSnapshots {
		input = input[:maxRunGitSnapshots]
	}
	result := make([]nodeprotocol.GitRunWorkspaceSnapshot, 0, len(input))
	remainingPatchBytes := maxRunGitSnapshotPatchBytes
	remainingPathBytes := maxRunGitSnapshotPathBytes
	remainingPaths := maxRunGitSnapshotPaths
	for _, source := range input {
		item := source
		item.RepositoryID = truncateUTF8Bytes(item.RepositoryID, 256)
		item.Label = truncateUTF8Bytes(item.Label, 256)
		item.RootDisplay = truncateUTF8Bytes(item.RootDisplay, 4<<10)
		item.Phase = truncateUTF8Bytes(item.Phase, 32)
		item.ContentHash = truncateUTF8Bytes(item.ContentHash, 128)
		if len(source.Areas) > maxRunGitSnapshotAreas {
			item.Truncated = true
			source.Areas = source.Areas[:maxRunGitSnapshotAreas]
		}
		item.Areas = make([]nodeprotocol.GitRunPatchArea, 0, len(source.Areas))
		for _, sourceArea := range source.Areas {
			area := sourceArea
			area.Area = truncateUTF8Bytes(area.Area, 32)
			if len(area.Patch) > remainingPatchBytes {
				area.Patch = ""
				area.Truncated = true
				item.Truncated = true
			} else {
				remainingPatchBytes -= len(area.Patch)
			}
			area.Paths, area.Truncated = boundedRunSnapshotPaths(
				sourceArea.Paths, area.Truncated, &remainingPaths, &remainingPathBytes,
			)
			area.BinaryPaths, area.Truncated = boundedRunSnapshotPaths(
				sourceArea.BinaryPaths, area.Truncated, &remainingPaths, &remainingPathBytes,
			)
			item.Truncated = item.Truncated || area.Truncated
			item.Areas = append(item.Areas, area)
		}
		item.Untracked = make([]nodeprotocol.GitRunUntrackedEntry, 0, min(len(source.Untracked), remainingPaths))
		for _, sourceEntry := range source.Untracked {
			if remainingPaths <= 0 || remainingPathBytes <= 0 {
				item.Truncated = true
				break
			}
			display, verified := providerDisplayPath(sourceEntry.Path)
			if !verified || display == "" || len(display) > remainingPathBytes {
				item.Truncated = true
				continue
			}
			entry := sourceEntry
			entry.Path = display
			entry.Kind = truncateUTF8Bytes(entry.Kind, 32)
			item.Untracked = append(item.Untracked, entry)
			remainingPaths--
			remainingPathBytes -= len(display)
		}
		warningLimit := min(len(source.Warnings), maxRunGitSnapshotWarnings)
		item.Warnings = make([]nodeprotocol.GitRepositoryWarning, 0, warningLimit)
		for index := 0; index < warningLimit; index++ {
			item.Warnings = append(item.Warnings, nodeprotocol.GitRepositoryWarning{
				Code:    truncateUTF8Bytes(source.Warnings[index].Code, 256),
				Message: truncateUTF8Bytes(source.Warnings[index].Message, 1024),
			})
		}
		if len(source.Warnings) > warningLimit {
			item.Truncated = true
		}
		result = append(result, item)
	}
	if droppedSnapshots && len(result) > 0 {
		result[len(result)-1].Truncated = true
	}
	if result == nil {
		return []nodeprotocol.GitRunWorkspaceSnapshot{}
	}
	return result
}

func boundedRunSnapshotPaths(
	values []string, truncated bool, remainingCount, remainingBytes *int,
) ([]string, bool) {
	result := make([]string, 0, min(len(values), *remainingCount))
	seen := make(map[string]struct{}, min(len(values), *remainingCount))
	for _, value := range values {
		if *remainingCount <= 0 || *remainingBytes <= 0 {
			return result, true
		}
		display, verified := providerDisplayPath(value)
		if !verified || display == "" {
			truncated = true
			continue
		}
		if _, exists := seen[display]; exists {
			continue
		}
		if len(display) > *remainingBytes {
			return result, true
		}
		seen[display] = struct{}{}
		result = append(result, display)
		(*remainingCount)--
		*remainingBytes -= len(display)
	}
	return result, truncated
}

func dropRunGitSnapshotPatches(items []nodeprotocol.GitRunWorkspaceSnapshot) bool {
	dropped := false
	for snapshotIndex := range items {
		for areaIndex := range items[snapshotIndex].Areas {
			if items[snapshotIndex].Areas[areaIndex].Patch == "" {
				continue
			}
			items[snapshotIndex].Areas[areaIndex].Patch = ""
			items[snapshotIndex].Areas[areaIndex].Truncated = true
			items[snapshotIndex].Truncated = true
			dropped = true
		}
	}
	return dropped
}

func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func providerReportPage(c *gin.Context) (int64, int, bool) {
	after := int64(0)
	if raw := strings.TrimSpace(c.Query("after_provider_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			respondError(c, domain.Invalid("after_provider_seq 必须是非负整数"))
			return 0, 0, false
		}
		after = parsed
	}
	limit := maxProviderReportsPerResponse
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxProviderReportsPerResponse {
			respondError(c, domain.Invalid("limit 必须在 1 到 100 之间"))
			return 0, 0, false
		}
		limit = parsed
	}
	return after, limit, true
}

func (a *API) scanProviderChangeReports(
	ctx context.Context, userID, runID string, after int64, limit int,
) ([]nodeprotocol.GitProviderChangeReport, bool, string, error) {
	return a.scanProviderChangeReportsWithBudget(
		ctx, userID, runID, after, limit, maxProviderReportsJSONBytes,
	)
}

func (a *API) scanProviderChangeReportsWithBudget(
	ctx context.Context, userID, runID string, after int64, limit, maxEncodedBytes int,
) ([]nodeprotocol.GitProviderChangeReport, bool, string, error) {
	reports := make([]nodeprotocol.GitProviderChangeReport, 0, limit)
	indexes := make(map[string]int, limit)
	reportSizes := make([]int, 0, limit)
	lastConsumed := after
	scanned := 0
	totalPatchBytes := 0
	totalEncodedBytes := 0

	for scanned < maxProviderScannedEvents {
		batchLimit := providerEventScanBatch
		if remaining := maxProviderScannedEvents - scanned; batchLimit > remaining {
			batchLimit = remaining
		}
		page, err := a.store.ListRunEventPage(
			ctx, userID, runID, lastConsumed, batchLimit, store.MaxRunEventPagePayloadBytes,
		)
		if err != nil {
			return nil, false, "", err
		}
		events := page.Items
		if len(events) == 0 {
			return sortedProviderReports(reports), false, "", nil
		}
		for _, event := range events {
			scanned++
			report, matched := normalizeProviderChangeReport(event)
			if !matched {
				lastConsumed = event.Sequence
				continue
			}
			key := providerReportDedupKey(event)
			if key != "" {
				if index, exists := indexes[key]; exists {
					previous := reports[index]
					report = mergeProviderReport(previous, report)
					patchBytesWithoutPrevious := totalPatchBytes - len(previous.Patch)
					if patchBytesWithoutPrevious+len(report.Patch) > maxProviderResponsePatchBytes {
						report.Patch = ""
						report.Truncated = true
					}
					available := maxEncodedBytes - totalEncodedBytes + reportSizes[index]
					reportSize, sizeErr := boundedProviderReportSize(&report, available)
					if sizeErr != nil {
						if errors.Is(sizeErr, errJSONResponseBudget) {
							return sortedProviderReports(reports), true, strconv.FormatInt(lastConsumed, 10), nil
						}
						return nil, false, "", sizeErr
					}
					totalPatchBytes = patchBytesWithoutPrevious + len(report.Patch)
					totalEncodedBytes = totalEncodedBytes - reportSizes[index] + reportSize
					reportSizes[index] = reportSize
					reports[index] = report
					lastConsumed = event.Sequence
					continue
				}
			}
			if !providerReportHasContent(report) && (key == "" || report.Status == "") {
				lastConsumed = event.Sequence
				continue
			}
			if len(reports) >= limit {
				return sortedProviderReports(reports), true, strconv.FormatInt(lastConsumed, 10), nil
			}
			if totalPatchBytes+len(report.Patch) > maxProviderResponsePatchBytes {
				if len(reports) > 0 {
					return sortedProviderReports(reports), true, strconv.FormatInt(lastConsumed, 10), nil
				}
				report.Patch = ""
				report.Truncated = true
			}
			reportSize, sizeErr := boundedProviderReportSize(&report, maxEncodedBytes-totalEncodedBytes)
			if sizeErr != nil {
				if errors.Is(sizeErr, errJSONResponseBudget) && len(reports) > 0 {
					return sortedProviderReports(reports), true, strconv.FormatInt(lastConsumed, 10), nil
				}
				return nil, false, "", sizeErr
			}
			if key != "" {
				indexes[key] = len(reports)
			}
			reports = append(reports, report)
			reportSizes = append(reportSizes, reportSize)
			totalPatchBytes += len(report.Patch)
			totalEncodedBytes += reportSize
			lastConsumed = event.Sequence
		}
		if !page.Truncated {
			return sortedProviderReports(reports), false, "", nil
		}
	}
	return sortedProviderReports(reports), true, strconv.FormatInt(lastConsumed, 10), nil
}

func boundedProviderReportSize(
	report *nodeprotocol.GitProviderChangeReport, maxBytes int,
) (int, error) {
	if maxBytes < 1 {
		return 0, errJSONResponseBudget
	}
	payload, err := encodeBoundedJSON(*report, maxBytes)
	if err == nil {
		return len(payload), nil
	}
	if !errors.Is(err, errJSONResponseBudget) {
		return 0, err
	}
	report.Patch = ""
	report.Truncated = true
	payload, err = encodeBoundedJSON(*report, maxBytes)
	if err == nil {
		return len(payload), nil
	}
	if !errors.Is(err, errJSONResponseBudget) {
		return 0, err
	}
	report.Paths = []string{}
	report.PathsVerified = false
	report.Scope = "unverified"
	payload, err = encodeBoundedJSON(*report, maxBytes)
	if err != nil {
		return 0, err
	}
	return len(payload), nil
}

func sortedProviderReports(reports []nodeprotocol.GitProviderChangeReport) []nodeprotocol.GitProviderChangeReport {
	if reports == nil {
		return []nodeprotocol.GitProviderChangeReport{}
	}
	sort.SliceStable(reports, func(i, j int) bool {
		if reports[i].Sequence == reports[j].Sequence {
			return reports[i].ID < reports[j].ID
		}
		return reports[i].Sequence < reports[j].Sequence
	})
	return reports
}

func providerReportDedupKey(event domain.RunEvent) string {
	change, ok := catalog.ProjectFileChange(event.Type, event.Payload)
	if !ok {
		return ""
	}
	source, itemID, turnID := change.Source, change.ItemID, change.TurnID
	identityKind := "item"
	identity := itemID
	if identity == "" {
		identityKind = "turn"
		identity = turnID
	}
	if identity == "" {
		return ""
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(source))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(identityKind))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(identity))
	return "provider_report_" + hex.EncodeToString(digest.Sum(nil))
}

func mergeProviderReport(
	previous, latest nodeprotocol.GitProviderChangeReport,
) nodeprotocol.GitProviderChangeReport {
	if len(latest.Paths) == 0 {
		latest.Paths = previous.Paths
		latest.PathsVerified = previous.PathsVerified
		latest.Scope = previous.Scope
	}
	if latest.Patch == "" && !latest.Truncated {
		latest.Patch = previous.Patch
	}
	if latest.Status == "" {
		latest.Status = previous.Status
	}
	if latest.ProviderTurnID == "" {
		latest.ProviderTurnID = previous.ProviderTurnID
	}
	return latest
}

func providerReportHasContent(report nodeprotocol.GitProviderChangeReport) bool {
	return len(report.Paths) > 0 || report.Patch != "" || report.Truncated
}

func normalizeProviderChangeReports(events []domain.RunEvent) []nodeprotocol.GitProviderChangeReport {
	reports := make([]nodeprotocol.GitProviderChangeReport, 0)
	indexes := make(map[string]int)
	for _, event := range events {
		report, ok := normalizeProviderChangeReport(event)
		if !ok {
			continue
		}
		key := providerReportDedupKey(event)
		if key != "" {
			if index, exists := indexes[key]; exists {
				reports[index] = mergeProviderReport(reports[index], report)
				continue
			}
		}
		if !providerReportHasContent(report) && (key == "" || report.Status == "") {
			continue
		}
		if key != "" {
			indexes[key] = len(reports)
		}
		reports = append(reports, report)
	}
	return sortedProviderReports(reports)
}

func normalizeProviderChangeReport(event domain.RunEvent) (nodeprotocol.GitProviderChangeReport, bool) {
	change, ok := catalog.ProjectFileChange(event.Type, event.Payload)
	if !ok {
		return nodeprotocol.GitProviderChangeReport{}, false
	}
	source, owner := change.Source, change.Owner
	changes := owner["changes"]
	allowStatusOnly := change.AllowStatusOnly && providerReportDedupKey(event) != ""

	patchValue := explicitPatch(owner)
	patchTruncated := false
	if len(patchValue) > maxProviderReportPatchBytes {
		patchValue = ""
		patchTruncated = true
	} else if patchValue != "" {
		// Some Providers report FileBridge writes as a raw diff body plus an
		// explicit path instead of a unified patch. Never expose that body when
		// its only attribution is an internal .teamva/tool-* path.
		if providerPatchHasUnattributedNoise(patchValue, owner, changes) {
			patchValue = ""
		}
	} else if patchValue == "" {
		patchValue, patchTruncated = boundedExplicitChangePatches(changes)
	}

	paths := newProviderPathCollector()
	forEachExplicitChange(changes, func(item map[string]any) bool {
		if candidate := stringField(item, "path"); candidate != "" {
			return paths.add(candidate)
		}
		return true
	})
	forEachExplicitString(owner["paths"], func(candidate string) bool { return paths.add(candidate) })
	if direct := stringField(owner, "path"); direct != "" {
		paths.add(direct)
	}

	reportID := providerReportDedupKey(event)
	if reportID == "" {
		reportID = event.ID
	}
	report := nodeprotocol.GitProviderChangeReport{
		ID: reportID, Sequence: event.Sequence, OccurredAt: event.OccurredAt,
		Source: source, Status: boundedProviderStatus(stringField(owner, "status")),
		ProviderTurnID: change.TurnID,
		Truncated:      patchTruncated,
	}
	if patchValue != "" {
		forEachProviderPatchPath(patchValue, func(candidate string) bool { return paths.add(candidate) })
		sanitized := sanitizeProviderPatch(filterProviderPatchNoise(patchValue))
		if len(sanitized) > maxProviderReportPatchBytes {
			report.Truncated = true
		} else {
			report.Patch = sanitized
		}
	}
	report.Paths = paths.values
	report.Truncated = report.Truncated || paths.truncated
	report.PathsVerified = len(report.Paths) > 0 && paths.verified
	report.Scope = "repository"
	if !report.PathsVerified {
		report.Scope = "unverified"
	}
	if !providerReportHasContent(report) && !allowStatusOnly {
		return nodeprotocol.GitProviderChangeReport{}, false
	}
	return report, true
}

func explicitPatch(owner map[string]any) string {
	if value, ok := owner["patch"].(string); ok {
		return value
	}
	if value, ok := owner["diff"].(string); ok {
		return value
	}
	return ""
}

func explicitChangePatches(value any) string {
	patch, _ := boundedExplicitChangePatches(value)
	return patch
}

func boundedExplicitChangePatches(value any) (string, bool) {
	var builder strings.Builder
	truncated := false
	forEachExplicitChange(value, func(item map[string]any) bool {
		patchValue := explicitPatch(item)
		if patchValue == "" {
			return true
		}
		if providerPatchHasUnattributedNoise(patchValue, item, nil) {
			return true
		}
		patchValue = filterProviderPatchNoise(patchValue)
		if patchValue == "" {
			return true
		}
		separatorBytes := 0
		if builder.Len() > 0 {
			separatorBytes = 1
		}
		if len(patchValue) > maxProviderReportPatchBytes-builder.Len()-separatorBytes {
			truncated = true
			return false
		}
		if separatorBytes != 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(patchValue)
		return true
	})
	return builder.String(), truncated
}

func forEachExplicitChange(value any, visit func(map[string]any) bool) {
	switch items := value.(type) {
	case []map[string]any:
		for _, item := range items {
			if item != nil && !visit(item) {
				return
			}
		}
	case []any:
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok && item != nil && !visit(item) {
				return
			}
		}
	}
}

func forEachExplicitString(value any, visit func(string) bool) {
	switch items := value.(type) {
	case []string:
		for _, item := range items {
			if !visit(item) {
				return
			}
		}
	case []any:
		for _, raw := range items {
			if item, ok := raw.(string); ok && !visit(item) {
				return
			}
		}
	}
}

func stringField(owner map[string]any, key string) string {
	value, _ := owner[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return boundedProviderStatus(value)
		}
	}
	return ""
}

func boundedProviderStatus(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

type providerPathCollector struct {
	seen       map[string]struct{}
	values     []string
	totalBytes int
	verified   bool
	truncated  bool
}

func newProviderPathCollector() *providerPathCollector {
	return &providerPathCollector{
		seen: make(map[string]struct{}), values: make([]string, 0), verified: true,
	}
}

func (collector *providerPathCollector) add(value string) bool {
	if providerNoisePath(value) {
		return true
	}
	display, verified := providerDisplayPath(value)
	if display == "" {
		collector.verified = false
		return true
	}
	if !verified {
		collector.verified = false
	}
	if _, exists := collector.seen[display]; exists {
		return true
	}
	if len(collector.values) >= maxProviderReportPaths ||
		collector.totalBytes+len(display) > maxProviderReportPathBytes {
		collector.truncated = true
		return false
	}
	collector.seen[display] = struct{}{}
	collector.values = append(collector.values, display)
	collector.totalBytes += len(display)
	return true
}

func safeProviderPaths(values []string) []string {
	collector := newProviderPathCollector()
	for _, value := range values {
		if !collector.add(value) {
			break
		}
	}
	return collector.values
}

var (
	windowsAbsolutePath = regexp.MustCompile(`^[A-Za-z]:/`)
	uriPathPrefix       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

func safeProviderPath(value string) string {
	display, _ := providerDisplayPath(value)
	return display
}

func providerDisplayPath(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	value = strings.Trim(value, `"`)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if value == "" {
		return "", false
	}
	candidate := value
	if strings.HasPrefix(candidate, "a/") || strings.HasPrefix(candidate, "b/") {
		candidate = candidate[2:]
	}
	lowerCandidate := strings.ToLower(candidate)
	absolute := strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "//") ||
		strings.HasPrefix(candidate, "~/") || strings.HasPrefix(lowerCandidate, "file:") ||
		windowsAbsolutePath.MatchString(candidate)
	uriLike := uriPathPrefix.MatchString(candidate) && !windowsAbsolutePath.MatchString(candidate)
	parentRelative := false
	for _, segment := range strings.Split(candidate, "/") {
		if segment == ".." {
			parentRelative = true
			break
		}
	}
	cleaned := path.Clean(candidate)
	if !absolute && !uriLike && !parentRelative && cleaned != "." && cleaned != "" &&
		len(cleaned) <= 4<<10 {
		return cleaned, true
	}
	return unverifiedProviderPathLabel(candidate), false
}

func unverifiedProviderPathLabel(value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimRight(value, "/")
	label := path.Base(value)
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, label)
	if label == "" || label == "." || label == ".." {
		label = "unverified-path"
	}
	if len(label) > 256 {
		label = label[:256]
		for !utf8.ValidString(label) {
			label = label[:len(label)-1]
		}
	}
	return label
}

func providerNoisePath(value string) bool {
	value = strings.ToLower(strings.ReplaceAll(value, `\`, "/"))
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if segment != ".teamva" {
			continue
		}
		for _, following := range segments[index+1:] {
			if strings.HasPrefix(following, "tool-") {
				return true
			}
		}
	}
	return false
}

func filterProviderPatchNoise(patchValue string) string {
	lines := strings.Split(patchValue, "\n")
	sections := make([][]string, 0)
	var current []string
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") && len(current) > 0 {
			sections = append(sections, current)
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, current)
	}
	if len(sections) == 1 && len(sections[0]) > 0 &&
		!strings.HasPrefix(strings.TrimSpace(sections[0][0]), "diff --git ") {
		noise := false
		forEachProviderPatchPath(patchValue, func(candidate string) bool {
			if providerNoisePath(candidate) {
				noise = true
				return false
			}
			return true
		})
		if noise {
			return ""
		}
		return patchValue
	}
	kept := make([]string, 0, len(sections))
	for _, section := range sections {
		joined := strings.Join(section, "\n")
		foundPath := false
		allNoise := true
		forEachProviderPatchPath(joined, func(candidate string) bool {
			foundPath = true
			if !providerNoisePath(candidate) {
				allNoise = false
				return false
			}
			return true
		})
		if (!foundPath || !allNoise) && strings.HasPrefix(strings.TrimSpace(joined), "diff --git ") {
			kept = append(kept, joined)
		}
	}
	return strings.Join(kept, "\n")
}

// providerPatchHasUnattributedNoise detects raw, non-unified Provider patch
// bodies whose only path attribution comes from explicit event metadata. Such
// bodies cannot be safely split, so the conservative response is to suppress
// them whenever any attributed path belongs to the private FileBridge tree.
// Unified patches are handled section-by-section by filterProviderPatchNoise.
func providerPatchHasUnattributedNoise(
	patchValue string, owner map[string]any, changes any,
) bool {
	hasPatchPath := false
	forEachProviderPatchPath(patchValue, func(string) bool {
		hasPatchPath = true
		return false
	})
	if hasPatchPath {
		return false
	}
	hasNoisePath := false
	visit := func(candidate string) bool {
		if providerNoisePath(candidate) {
			hasNoisePath = true
		}
		return true
	}
	if direct := stringField(owner, "path"); direct != "" {
		visit(direct)
	}
	forEachExplicitString(owner["paths"], visit)
	forEachExplicitChange(changes, func(item map[string]any) bool {
		if direct := stringField(item, "path"); direct != "" {
			visit(direct)
		}
		forEachExplicitString(item["paths"], visit)
		return true
	})
	return hasNoisePath
}

func forEachProviderPatchPath(patchValue string, visit func(string) bool) {
	for len(patchValue) > 0 {
		line := patchValue
		if newline := strings.IndexByte(patchValue, '\n'); newline >= 0 {
			line = patchValue[:newline]
			patchValue = patchValue[newline+1:]
		} else {
			patchValue = ""
		}
		switch {
		case strings.HasPrefix(line, "+++ "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			value = strings.SplitN(value, "\t", 2)[0]
			if value != "/dev/null" && !visit(value) {
				return
			}
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(line)
			if len(fields) >= 4 && !visit(strings.Trim(fields[3], `"`)) {
				return
			}
		}
	}
}

func providerPatchPaths(patchValue string) []string {
	result := make([]string, 0)
	forEachProviderPatchPath(patchValue, func(value string) bool {
		result = append(result, value)
		return true
	})
	return result
}

func sanitizeProviderPatch(patchValue string) string {
	lines := strings.Split(patchValue, "\n")
	for index, line := range lines {
		for _, prefix := range []string{"--- ", "+++ ", "rename from ", "rename to "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			value := strings.TrimPrefix(line, prefix)
			if value == "/dev/null" {
				break
			}
			tab := ""
			if position := strings.IndexByte(value, '\t'); position >= 0 {
				tab = value[position:]
				value = value[:position]
			}
			marker := ""
			if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
				marker = value[:2]
			}
			lines[index] = prefix + marker + safeProviderPath(value) + tab
			break
		}
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				left := "a/" + safeProviderPath(fields[2])
				right := "b/" + safeProviderPath(fields[3])
				lines[index] = "diff --git " + left + " " + right
			} else {
				lines[index] = "diff --git a/unknown b/unknown"
			}
		}
	}
	return strings.Join(lines, "\n")
}
