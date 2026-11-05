package nodeprotocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGitWireTypesUseSnapshotAndSnakeCaseFields(t *testing.T) {
	repository := GitRepositorySummary{
		ID: "repo_1", CommonID: "common_1", Label: "repo", RootDisplay: "Code",
		ResourceIDs: []string{"res_1"}, DiscoveryOrigin: "resource", WorktreeKind: "main",
		Scope: "full", Generation: 1, State: "clean", Warnings: []GitRepositoryWarning{},
	}
	payload, err := json.Marshal(GitRepositoryList{
		SnapshotID: "gits_1", Repositories: []GitRepositorySummary{repository},
		Warnings: []GitRepositoryWarning{},
	})
	if err != nil {
		t.Fatal(err)
	}
	value := string(payload)
	for _, expected := range []string{`"snapshot_id"`, `"repositories"`, `"common_id"`, `"root_display"`, `"discovery_origin"`, `"worktree_kind"`} {
		if !strings.Contains(value, expected) {
			t.Fatalf("wire JSON %s does not contain %s", value, expected)
		}
	}
	if strings.Contains(value, `"items"`) {
		t.Fatalf("unsupported items alias appeared in wire JSON: %s", value)
	}

	changePayload, err := json.Marshal(GitChange{
		ID: "chg_1", PathID: "path_1", Area: "unstaged", Status: "modified", Kind: "file",
		Path: GitDisplayPath{Token: "path_1", Display: "src/main.go"}, DiffAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"path_id"`, `"diff_available"`, `"display":"src/main.go"`} {
		if !strings.Contains(string(changePayload), expected) {
			t.Fatalf("change JSON %s does not contain %s", changePayload, expected)
		}
	}
}

func TestGitRunChangesWireSeparatesObservationsAndProviderReports(t *testing.T) {
	payload, err := json.Marshal(GitRunChangesResult{
		RunID: "run_1",
		Snapshots: []GitRunWorkspaceSnapshot{{
			RepositoryID: "repo_1", Label: "Code", RootDisplay: "Code", Phase: "start",
			CapturedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
			Areas:      []GitRunPatchArea{{Area: "unstaged", Patch: "patch", Paths: []string{"src/main.go"}, BinaryPaths: []string{}}},
			Untracked:  []GitRunUntrackedEntry{}, Warnings: []GitRepositoryWarning{}, ContentHash: strings.Repeat("a", 64),
		}},
		ProviderReports: []GitProviderChangeReport{{
			ID: "event_1", Sequence: 2, OccurredAt: time.Date(2026, 8, 11, 1, 2, 4, 0, time.UTC),
			Source: "workspace.diff", Paths: []string{"src/main.go"}, PathsVerified: true, Scope: "repository",
		}},
		ProviderReportsTruncated: true,
		NextProviderCursor:       "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	value := string(payload)
	for _, expected := range []string{
		`"snapshots"`, `"provider_reports"`, `"provider_reports_truncated":true`,
		`"next_provider_cursor":"2"`, `"paths_verified":true`, `"scope":"repository"`,
		`"content_hash"`, `"binary_paths"`, `"provider_turn_id"`,
	} {
		if expected == `"provider_turn_id"` {
			if strings.Contains(value, expected) {
				t.Fatalf("omitempty field unexpectedly present: %s", value)
			}
			continue
		}
		if !strings.Contains(value, expected) {
			t.Fatalf("Run changes JSON %s does not contain %s", value, expected)
		}
	}
}

func TestGitDynamicMessageCodesAreStableAndOptional(t *testing.T) {
	warningCodes := []string{
		GitWarningDiscoveryLimitExceeded,
		GitWarningStatusUnavailable,
		GitWarningWatchLimitExceeded,
		GitWarningStatsUnavailable,
		GitWarningStatusOutputTruncated,
		GitWarningChangesTruncated,
		GitWarningStatusTruncated,
		GitWarningPathLimitExceeded,
		GitWarningPatchTruncated,
		GitWarningSnapshotPayloadTruncated,
		GitWarningSnapshotIncomplete,
		GitWarningSnapshotCaptureFailed,
		GitWarningSnapshotSaveFailed,
	}
	seen := make(map[string]struct{}, len(warningCodes))
	for _, code := range warningCodes {
		if code == "" || !IsGitRepositoryWarningCode(code) {
			t.Fatalf("warning code %q is not registered", code)
		}
		if _, exists := seen[code]; exists {
			t.Fatalf("duplicate warning code %q", code)
		}
		seen[code] = struct{}{}
		warning := NewGitRepositoryWarning(code)
		if warning.Code != code || warning.Message == "" || !isASCII(warning.Message) {
			t.Fatalf("warning fallback=%+v", warning)
		}
	}
	diffMessageCodes := []string{
		GitDiffMessageUntrackedDirectoryExpandRequired,
		GitDiffMessageUntrackedSymlinkSummary,
		GitDiffMessageConfirmationRequired,
		GitDiffMessageLineConfirmationRequired,
		GitDiffMessagePatchLimit,
		GitDiffMessageTimeout,
		GitDiffMessageImageConfirmationRequired,
		GitDiffMessageResponseBudget,
	}
	seen = make(map[string]struct{}, len(diffMessageCodes))
	for _, code := range diffMessageCodes {
		if code == "" || !IsGitDiffMessageCode(code) || !isASCII(GitDiffMessage(code)) {
			t.Fatalf("diff message code=%q fallback=%q", code, GitDiffMessage(code))
		}
		if _, exists := seen[code]; exists {
			t.Fatalf("duplicate diff message code %q", code)
		}
		seen[code] = struct{}{}
	}
	for _, reason := range []string{
		GitDiffTruncationPatchLimit,
		GitDiffTruncationTimeout,
		GitDiffTruncationResponseBudget,
	} {
		if !IsGitDiffTruncationReason(reason) {
			t.Fatalf("unregistered truncation reason %q", reason)
		}
	}

	withCode, err := json.Marshal(GitDiffResult{
		MessageCode: GitDiffMessagePatchLimit,
		Message:     "The diff reached its safety limit.",
		Truncated:   true,
		Truncation: &GitDiffTruncation{
			Truncated: true,
			Reason:    GitDiffTruncationPatchLimit,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withCode), `"message_code":"diff_patch_limit"`) ||
		!strings.Contains(string(withCode), `"reason":"patch_limit"`) {
		t.Fatalf("coded diff JSON=%s", withCode)
	}
	withoutCode, err := json.Marshal(GitDiffResult{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutCode), `"message_code"`) {
		t.Fatalf("optional v5 message_code unexpectedly present: %s", withoutCode)
	}
}

func isASCII(value string) bool {
	for _, character := range value {
		if character > 0x7f {
			return false
		}
	}
	return true
}
