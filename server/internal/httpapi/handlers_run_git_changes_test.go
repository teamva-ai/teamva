package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestNormalizeProviderReportsUsesOnlyExplicitFieldsAndFiltersToolNoise(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/.teamva/tool-results/result.json b/.teamva/tool-results/result.json",
		"--- a/.teamva/tool-results/result.json",
		"+++ b/.teamva/tool-results/result.json",
		"@@ -0,0 +1 @@",
		"+noise",
		"diff --git a/src/main.go b/src/main.go",
		"--- a/src/main.go",
		"+++ b/src/main.go",
		"@@ -1 +1 @@",
		"-before",
		"+after",
	}, "\n")
	reports := normalizeProviderChangeReports([]domain.RunEvent{
		{
			ID: "event-file", RunID: "run", Sequence: 3, Type: "provider.item", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{"provider_id": "codex", "item": map[string]any{
				"type": "fileChange", "id": "item", "status": "completed", "turnId": "turn-7", "patch": patch,
				"changes": []any{
					map[string]any{"path": ".teamva/tool-results/result.json"},
					map[string]any{"path": "src/main.go"},
					map[string]any{"path": "/Users/private/absolute.go"},
					map[string]any{"path": "\x01/Users/private/control.go"},
					map[string]any{"path": "file:///Users/private/uri.go"},
				},
			}},
		},
		{
			ID: "event-unknown", Sequence: 4, Type: "provider.item", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{"provider_id": "codex", "item": map[string]any{
				"type": "fileChange", "metadata": map[string]any{"diff": "must not be read"},
			}},
		},
	})
	if len(reports) != 1 {
		t.Fatalf("reports=%+v", reports)
	}
	report := reports[0]
	if report.Source != "provider.item.fileChange" || report.ProviderTurnID != "turn-7" ||
		report.Status != "completed" || !strings.Contains(report.Patch, "src/main.go") ||
		strings.Contains(report.Patch, ".teamva/tool-") {
		t.Fatalf("normalized report=%+v", report)
	}
	if len(report.Paths) != 4 || report.Paths[0] != "src/main.go" || report.Paths[1] != "absolute.go" ||
		report.Paths[2] != "control.go" || report.Paths[3] != "uri.go" {
		t.Fatalf("safe paths=%+v", report.Paths)
	}
	if report.PathsVerified || report.Scope != "unverified" {
		t.Fatalf("absolute/URI paths must remain unverified: %+v", report)
	}
}

func TestNormalizeProviderReportsSuppressesRawFileBridgeBodies(t *testing.T) {
	reports := normalizeProviderChangeReports([]domain.RunEvent{
		{
			ID: "event-file-started", RunID: "run", Sequence: 3, Type: "provider.item",
			OccurredAt: time.Now().UTC(), Payload: map[string]any{
				"provider_id": "codex",
				"item": map[string]any{
					"type": "fileChange", "id": "item", "status": "inProgress",
					"changes": []any{map[string]any{
						"path": "/private/work/.teamva/tool-requests/run/tool/request.json",
						"diff": `{"secret":"must-not-leak"}`,
					}},
				},
			},
		},
		{
			ID: "event-file-completed", RunID: "run", Sequence: 4, Type: "provider.item",
			OccurredAt: time.Now().UTC().Add(time.Second), Payload: map[string]any{
				"provider_id": "codex",
				"item": map[string]any{
					"type": "fileChange", "id": "item", "status": "completed",
					"changes": []any{map[string]any{
						"path": "/private/work/.teamva/tool-requests/run/tool/request.json",
						"diff": `{"secret":"must-not-leak"}`,
					}},
				},
			},
		},
	})
	if len(reports) != 1 {
		t.Fatalf("reports=%+v", reports)
	}
	report := reports[0]
	if report.Status != "completed" || report.Patch != "" || report.Paths == nil || len(report.Paths) != 0 ||
		strings.Contains(report.Patch, "must-not-leak") {
		t.Fatalf("FileBridge report leaked private content: %+v", report)
	}
}

func TestNormalizeProviderReportsKeepsSafeRawChangeBodies(t *testing.T) {
	report, ok := normalizeProviderChangeReport(domain.RunEvent{
		ID: "event-safe", Sequence: 5, Type: "provider.item", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"provider_id": "codex", "item": map[string]any{
			"type": "fileChange", "id": "safe", "status": "completed",
			"changes": []any{
				map[string]any{
					"path": "/private/work/.teamva/tool-results/run/tool/result.json",
					"diff": `{"secret":"must-not-leak"}`,
				},
				map[string]any{"path": "src/main.go", "diff": "-before\n+after"},
			},
		}},
	})
	if !ok || report.Patch != "-before\n+after" || len(report.Paths) != 1 || report.Paths[0] != "src/main.go" ||
		strings.Contains(report.Patch, "must-not-leak") {
		t.Fatalf("safe mixed report=%+v ok=%t", report, ok)
	}
}

func TestNormalizeWorkspaceDiffOmitsOversizedPatch(t *testing.T) {
	report, ok := normalizeProviderChangeReport(domain.RunEvent{
		ID: "event-large", Sequence: 9, Type: "workspace.diff", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"diff":  strings.Repeat("x", maxProviderReportPatchBytes+1),
			"paths": []any{"src/large.ts"},
		},
	})
	if !ok || !report.Truncated || report.Patch != "" || len(report.Paths) != 1 || report.Source != "workspace.diff" {
		t.Fatalf("oversized report=%+v ok=%t", report, ok)
	}
}

func TestNormalizeWorkspaceDiffKeepsOversizedReportWithoutPaths(t *testing.T) {
	report, ok := normalizeProviderChangeReport(domain.RunEvent{
		ID: "event-large-no-path", Sequence: 10, Type: "workspace.diff", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"diff": strings.Repeat("x", maxProviderReportPatchBytes+1)},
	})
	if !ok || !report.Truncated || report.Patch != "" || len(report.Paths) != 0 {
		t.Fatalf("oversized pathless report=%+v ok=%t", report, ok)
	}
}

func TestNormalizeProviderReportsDeduplicatesItemTurnAndKeepsLatest(t *testing.T) {
	now := time.Now().UTC()
	reports := normalizeProviderChangeReports([]domain.RunEvent{
		{ID: "started", Sequence: 2, Type: "provider.item", OccurredAt: now, Payload: map[string]any{
			"provider_id": "codex",
			"turnId":      "turn-1", "item": map[string]any{
				"id": "item-1", "type": "fileChange", "status": "started",
				"changes": []any{map[string]any{"path": "src/main.go"}},
			},
		}},
		{ID: "completed", Sequence: 3, Type: "provider.item", OccurredAt: now.Add(time.Second), Payload: map[string]any{
			"provider_id": "codex",
			"item": map[string]any{
				"id": "item-1", "type": "fileChange", "status": "completed",
			},
		}},
	})
	if len(reports) != 1 || reports[0].ID == "started" || reports[0].ID == "completed" || reports[0].Sequence != 3 ||
		reports[0].Status != "completed" || reports[0].ProviderTurnID != "turn-1" || len(reports[0].Paths) != 1 ||
		reports[0].Paths[0] != "src/main.go" {
		t.Fatalf("deduplicated reports=%+v", reports)
	}
}

func TestExplicitProviderChangesAreBoundedAndMarkedTruncated(t *testing.T) {
	changes := make([]any, maxProviderReportPaths+1)
	for index := range changes {
		changes[index] = map[string]any{"path": "src/generated/" + strings.Repeat("x", 16) + strconv.Itoa(index)}
	}
	report, ok := normalizeProviderChangeReport(domain.RunEvent{
		ID: "many-paths", Sequence: 1, Type: "provider.item", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"provider_id": "codex", "item": map[string]any{
			"id": "many", "type": "fileChange", "changes": changes,
		}},
	})
	if !ok || !report.Truncated || len(report.Paths) > maxProviderReportPaths {
		t.Fatalf("many-path report: ok=%t paths=%d truncated=%t", ok, len(report.Paths), report.Truncated)
	}
	patch, truncated := boundedExplicitChangePatches([]any{
		map[string]any{"patch": strings.Repeat("a", 11<<20)},
		map[string]any{"patch": strings.Repeat("b", 11<<20)},
	})
	if !truncated || len(patch) > maxProviderReportPatchBytes {
		t.Fatalf("bounded patch bytes=%d truncated=%t", len(patch), truncated)
	}
}

type boundedProviderEventStore struct {
	store.CoreLocalRepository
	events     []domain.RunEvent
	fullCalls  int
	afterCalls int
}

func (repository *boundedProviderEventStore) ListRunEvents(
	context.Context, string, string,
) ([]domain.RunEvent, error) {
	repository.fullCalls++
	return nil, nil
}

func (repository *boundedProviderEventStore) ListRunEventsAfter(
	_ context.Context, _, _ string, after int64, limit int,
) ([]domain.RunEvent, error) {
	repository.afterCalls++
	items := make([]domain.RunEvent, 0, limit)
	for _, event := range repository.events {
		if event.Sequence <= after {
			continue
		}
		items = append(items, event)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func (repository *boundedProviderEventStore) ListRunEventPage(
	_ context.Context, _, _ string, after int64, limit, maxPayloadBytes int,
) (domain.RunEventPage, error) {
	repository.afterCalls++
	items := make([]domain.RunEvent, 0, limit)
	payloadBytes := 0
	truncated := false
	for _, event := range repository.events {
		if event.Sequence <= after {
			continue
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return domain.RunEventPage{}, err
		}
		if len(items) >= limit || payloadBytes+len(payload) > maxPayloadBytes {
			truncated = true
			break
		}
		items = append(items, event)
		payloadBytes += len(payload)
	}
	page := domain.RunEventPage{Items: items, Truncated: truncated}
	if truncated && len(items) > 0 {
		page.NextCursor = strconv.FormatInt(items[len(items)-1].Sequence, 10)
	}
	return page, nil
}

func TestRunProviderReportScanIsPagedAndResponseBounded(t *testing.T) {
	repository := &boundedProviderEventStore{}
	for sequence := int64(1); sequence <= 101; sequence++ {
		repository.events = append(repository.events, domain.RunEvent{
			ID: "event-" + strconv.FormatInt(sequence, 10), Sequence: sequence, Type: "workspace.diff",
			OccurredAt: time.Now().UTC(), Payload: map[string]any{"paths": []any{"src/main.go"}},
		})
	}
	api := &API{store: repository}
	reports, truncated, cursor, err := api.scanProviderChangeReports(t.Context(), "user", "run", 0, 100)
	if err != nil || !truncated || len(reports) != 100 || cursor == "" || repository.fullCalls != 0 || repository.afterCalls == 0 {
		t.Fatalf("reports=%d truncated=%t cursor=%q full=%d after=%d err=%v",
			len(reports), truncated, cursor, repository.fullCalls, repository.afterCalls, err)
	}

	repository = &boundedProviderEventStore{}
	for sequence := int64(1); sequence <= 30; sequence++ {
		repository.events = append(repository.events, domain.RunEvent{
			ID: "large", Sequence: sequence, Type: "workspace.diff", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{
				"paths": []any{"src/large.go"}, "patch": strings.Repeat("x", 1<<20),
			},
		})
	}
	api.store = repository
	reports, truncated, cursor, err = api.scanProviderChangeReports(t.Context(), "user", "run", 0, 100)
	totalPatch := 0
	for _, report := range reports {
		totalPatch += len(report.Patch)
	}
	if err != nil || !truncated || cursor == "" || totalPatch > maxProviderResponsePatchBytes {
		t.Fatalf("large reports=%d bytes=%d truncated=%t cursor=%q err=%v",
			len(reports), totalPatch, truncated, cursor, err)
	}
}

func TestRunProviderReportScanStopsAfter5000Events(t *testing.T) {
	repository := &boundedProviderEventStore{events: make([]domain.RunEvent, 6000)}
	for index := range repository.events {
		repository.events[index] = domain.RunEvent{
			ID: "noise", Sequence: int64(index + 1), Type: "output.delta", Payload: map[string]any{},
		}
	}
	api := &API{store: repository}
	reports, truncated, cursor, err := api.scanProviderChangeReports(t.Context(), "user", "run", 0, 100)
	if err != nil || !truncated || len(reports) != 0 || cursor != "5000" || repository.fullCalls != 0 || repository.afterCalls != 10 {
		t.Fatalf("reports=%d truncated=%t cursor=%q full=%d after=%d err=%v",
			len(reports), truncated, cursor, repository.fullCalls, repository.afterCalls, err)
	}
}

func TestRunProviderCursorPagesHaveNoDuplicatesOrOmissions(t *testing.T) {
	repository := &boundedProviderEventStore{}
	for sequence := int64(1); sequence <= 237; sequence++ {
		repository.events = append(repository.events, domain.RunEvent{
			ID: "event-" + strconv.FormatInt(sequence, 10), Sequence: sequence,
			Type: "workspace.diff", OccurredAt: time.Now().UTC(),
			Payload: map[string]any{"paths": []any{"src/file.go"}},
		})
	}
	api := &API{store: repository}
	after := int64(0)
	seen := make(map[int64]struct{}, len(repository.events))
	for {
		reports, truncated, cursor, err := api.scanProviderChangeReportsWithBudget(
			t.Context(), "user", "run", after, 37, 1<<20,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, report := range reports {
			if _, duplicate := seen[report.Sequence]; duplicate {
				t.Fatalf("duplicate provider sequence %d", report.Sequence)
			}
			seen[report.Sequence] = struct{}{}
		}
		if !truncated {
			break
		}
		if cursor == "" {
			t.Fatal("truncated page omitted next cursor")
		}
		next, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || next <= after {
			t.Fatalf("cursor=%q after=%d err=%v", cursor, after, err)
		}
		after = next
	}
	if len(seen) != len(repository.events) {
		t.Fatalf("retrieved %d reports, want %d", len(seen), len(repository.events))
	}
}

func TestProviderReportIdentityIsStableAcrossPaginationUpdates(t *testing.T) {
	now := time.Now().UTC()
	repository := &boundedProviderEventStore{events: make([]domain.RunEvent, 0, maxProviderScannedEvents+2)}
	repository.events = append(repository.events, domain.RunEvent{
		ID: "event-started", Sequence: 1, Type: "provider.item", OccurredAt: now,
		Payload: map[string]any{
			"provider_id": "codex",
			"turnId":      "turn-cross-page",
			"item": map[string]any{
				"id": "file-change-cross-page", "type": "fileChange", "status": "started",
				"changes": []any{map[string]any{"path": "src/started.go"}},
			},
		},
	})
	for sequence := int64(2); sequence <= maxProviderScannedEvents; sequence++ {
		repository.events = append(repository.events, domain.RunEvent{
			ID: "noise-" + strconv.FormatInt(sequence, 10), Sequence: sequence,
			Type: "output.delta", OccurredAt: now, Payload: map[string]any{},
		})
	}
	repository.events = append(repository.events,
		domain.RunEvent{
			ID: "noise-after-cursor", Sequence: maxProviderScannedEvents + 1,
			Type: "output.delta", OccurredAt: now, Payload: map[string]any{},
		},
		domain.RunEvent{
			ID: "event-completed", Sequence: maxProviderScannedEvents + 2,
			Type: "provider.item", OccurredAt: now.Add(time.Second),
			Payload: map[string]any{
				"provider_id": "codex",
				"turnId":      "turn-cross-page",
				"item": map[string]any{
					"id": "file-change-cross-page", "type": "fileChange", "status": "completed",
					"patch": "diff --git a/src/completed.go b/src/completed.go\n--- a/src/completed.go\n+++ b/src/completed.go\n@@ -1 +1 @@\n-old\n+new",
				},
			},
		},
	)
	api := &API{store: repository}
	first, truncated, cursor, err := api.scanProviderChangeReports(
		t.Context(), "user", "run", 0, maxProviderReportsPerResponse,
	)
	if err != nil || !truncated || cursor != strconv.Itoa(maxProviderScannedEvents) || len(first) != 1 {
		t.Fatalf("first page=%+v truncated=%t cursor=%q err=%v", first, truncated, cursor, err)
	}
	if first[0].ID == "event-started" || first[0].Status != "started" ||
		first[0].Sequence != 1 || first[0].ProviderTurnID != "turn-cross-page" {
		t.Fatalf("first stable report=%+v", first[0])
	}
	after, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	second, truncated, nextCursor, err := api.scanProviderChangeReports(
		t.Context(), "user", "run", after, maxProviderReportsPerResponse,
	)
	if err != nil || truncated || nextCursor != "" || len(second) != 1 {
		t.Fatalf("second page=%+v truncated=%t cursor=%q err=%v", second, truncated, nextCursor, err)
	}
	if second[0].ID != first[0].ID || second[0].Status != "completed" ||
		second[0].Sequence != maxProviderScannedEvents+2 ||
		second[0].ProviderTurnID != first[0].ProviderTurnID ||
		second[0].Source != first[0].Source || !strings.Contains(second[0].Patch, "src/completed.go") {
		t.Fatalf("cross-page update is not frontend-mergeable: first=%+v second=%+v", first[0], second[0])
	}

	fallback, ok := normalizeProviderChangeReport(domain.RunEvent{
		ID: "event-without-upstream-key", Sequence: 99, Type: "workspace.diff",
		OccurredAt: now, Payload: map[string]any{"paths": []any{"src/fallback.go"}},
	})
	if !ok || fallback.ID != "event-without-upstream-key" {
		t.Fatalf("event ID fallback=%+v ok=%t", fallback, ok)
	}
}

func TestActivityRunEventsRemovePatchWithoutMutatingStoredPayload(t *testing.T) {
	original := []domain.RunEvent{{
		ID: "workspace", Type: "workspace.diff", Payload: map[string]any{
			"diff": "large patch", "paths": []any{"src/main.go"},
		},
	}, {
		ID: "file", Type: "provider.item", Payload: map[string]any{
			"provider_id": "codex",
			"item": map[string]any{
				"type": "fileChange", "patch": "file patch",
				"changes": []any{map[string]any{"path": "src/file.go", "diff": "nested patch"}},
			},
		},
	}}
	activity := activityRunEvents(original)
	if activity[0].Payload["diff"] != nil || len(activity[0].Payload["paths"].([]any)) != 1 {
		t.Fatalf("workspace activity payload=%+v", activity[0].Payload)
	}
	item := activity[1].Payload["item"].(map[string]any)
	change := item["changes"].([]any)[0].(map[string]any)
	if item["patch"] != nil || change["diff"] != nil || change["path"] != "src/file.go" {
		t.Fatalf("fileChange activity payload=%+v", activity[1].Payload)
	}
	if original[0].Payload["diff"] != "large patch" || original[1].Payload["item"].(map[string]any)["patch"] != "file patch" {
		t.Fatal("activity projection mutated durable event payload")
	}
}
