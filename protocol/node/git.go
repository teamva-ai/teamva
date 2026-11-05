package nodeprotocol

import (
	"fmt"
	"time"
)

const (
	MaxGitResourcesPerRun    = 64
	MaxGitRepositoriesPerRun = 100
	MaxGitResourceIDBytes    = 256
	MaxGitResourceNameBytes  = 1024
	MaxGitResourcePathBytes  = 16 << 10
)

// Git warning, diff message, and truncation values are stable localization
// keys. Message remains a safe English fallback; clients should render the
// corresponding code in the user's locale.
const (
	GitWarningDiscoveryLimitExceeded   = "discovery_limit_exceeded"
	GitWarningStatusUnavailable        = "status_unavailable"
	GitWarningWatchLimitExceeded       = "watch_limit_exceeded"
	GitWarningStatsUnavailable         = "stats_unavailable"
	GitWarningStatusOutputTruncated    = "status_output_truncated"
	GitWarningChangesTruncated         = "changes_truncated"
	GitWarningStatusTruncated          = "status_truncated"
	GitWarningPathLimitExceeded        = "path_limit_exceeded"
	GitWarningPatchTruncated           = "patch_truncated"
	GitWarningSnapshotPayloadTruncated = "snapshot_payload_truncated"
	GitWarningSnapshotIncomplete       = "snapshot_incomplete"
	GitWarningSnapshotCaptureFailed    = "snapshot_capture_failed"
	GitWarningSnapshotSaveFailed       = "snapshot_save_failed"
)

const (
	GitDiffMessageUntrackedDirectoryExpandRequired = "untracked_directory_expand_required"
	GitDiffMessageUntrackedSymlinkSummary          = "untracked_symlink_summary"
	GitDiffMessageConfirmationRequired             = "diff_confirmation_required"
	GitDiffMessageLineConfirmationRequired         = "diff_line_confirmation_required"
	GitDiffMessagePatchLimit                       = "diff_patch_limit"
	GitDiffMessageTimeout                          = "diff_timeout"
	GitDiffMessageImageConfirmationRequired        = "image_confirmation_required"
	GitDiffMessageResponseBudget                   = "response_budget"
)

const (
	GitDiffTruncationPatchLimit     = "patch_limit"
	GitDiffTruncationTimeout        = "timeout"
	GitDiffTruncationResponseBudget = "response_budget"
)

// GitResourceRequest is the only place where a local resource path crosses
// from the control plane into the embedded runtime. HTTP clients never supply
// Path; the server fills it from its tenant-scoped resource store.
type GitResourceRequest struct {
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
}

type GitRepositoriesRequest struct {
	ProjectID string               `json:"project_id"`
	Resources []GitResourceRequest `json:"resources"`
	Refresh   bool                 `json:"refresh,omitempty"`
}

type GitRepositoryList struct {
	SnapshotID   string                 `json:"snapshot_id"`
	Repositories []GitRepositorySummary `json:"repositories"`
	Truncated    bool                   `json:"truncated"`
	Warnings     []GitRepositoryWarning `json:"warnings"`
}

type GitRepositorySummary struct {
	ID              string                 `json:"id"`
	CommonID        string                 `json:"common_id"`
	Label           string                 `json:"label"`
	RootDisplay     string                 `json:"root_display"`
	ResourceIDs     []string               `json:"resource_ids"`
	DiscoveryOrigin string                 `json:"discovery_origin"`
	Origin          string                 `json:"origin,omitempty"`
	WorktreeKind    string                 `json:"worktree_kind"`
	Worktree        GitWorktreeSummary     `json:"worktree"`
	Scope           string                 `json:"scope"`
	Branch          GitBranchSummary       `json:"branch"`
	Operation       GitOperationSummary    `json:"operation"`
	Generation      int64                  `json:"generation"`
	State           string                 `json:"state"`
	Counts          GitChangeCounts        `json:"counts"`
	Warnings        []GitRepositoryWarning `json:"warnings"`
}

type GitWorktreeSummary struct {
	Linked bool `json:"linked"`
	Main   bool `json:"main"`
}

type GitBranchSummary struct {
	Name     string `json:"name,omitempty"`
	OID      string `json:"oid,omitempty"`
	Detached bool   `json:"detached"`
	Unborn   bool   `json:"unborn"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
}

type GitOperationSummary struct {
	Kind string `json:"kind,omitempty"`
}

type GitChangeCounts struct {
	Conflict  int `json:"conflict"`
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
}

type GitRepositoryWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewGitRepositoryWarning prevents warning fallbacks from carrying local
// paths, provider output, or server-selected language.
func NewGitRepositoryWarning(code string) GitRepositoryWarning {
	return GitRepositoryWarning{Code: code, Message: GitRepositoryWarningMessage(code)}
}

func GitRepositoryWarningMessage(code string) string {
	switch code {
	case GitWarningDiscoveryLimitExceeded:
		return "Repository discovery reached a safety limit; results may be incomplete."
	case GitWarningStatusUnavailable:
		return "Repository status is temporarily unavailable."
	case GitWarningWatchLimitExceeded:
		return "Filesystem watching is limited; repository status will be reconciled periodically."
	case GitWarningStatsUnavailable:
		return "Some change statistics are temporarily unavailable."
	case GitWarningStatusOutputTruncated:
		return "Git status output reached a safety limit; results may be incomplete."
	case GitWarningChangesTruncated:
		return "The change list reached a safety limit; results may be incomplete."
	case GitWarningStatusTruncated:
		return "Run snapshot status reached a safety limit; path summaries may be incomplete."
	case GitWarningPathLimitExceeded:
		return "Run snapshot path metadata reached a safety limit."
	case GitWarningPatchTruncated:
		return "Run snapshot patches reached the aggregate safety limit."
	case GitWarningSnapshotPayloadTruncated:
		return "Run snapshot content reached a persistence limit; only path metadata was retained."
	case GitWarningSnapshotIncomplete:
		return "The Node stopped before the final run snapshot was saved."
	case GitWarningSnapshotCaptureFailed:
		return "Run snapshot capture failed; only failure metadata was retained."
	case GitWarningSnapshotSaveFailed:
		return "Run snapshot persistence failed; only failure metadata was retained."
	default:
		return "A Git operation completed with a warning."
	}
}

func IsGitRepositoryWarningCode(code string) bool {
	switch code {
	case GitWarningDiscoveryLimitExceeded,
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
		GitWarningSnapshotSaveFailed:
		return true
	default:
		return false
	}
}

func GitDiffMessage(code string) string {
	switch code {
	case GitDiffMessageUntrackedDirectoryExpandRequired:
		return "Expand the untracked directory before selecting a file."
	case GitDiffMessageUntrackedSymlinkSummary:
		return "Only a summary is available for an untracked symbolic link."
	case GitDiffMessageConfirmationRequired:
		return "This diff exceeds 2 MiB. Confirm to continue."
	case GitDiffMessageLineConfirmationRequired:
		return "This diff exceeds 500 changed lines. Confirm to continue."
	case GitDiffMessageTimeout:
		return "Diff reading timed out; only a summary is available."
	case GitDiffMessagePatchLimit:
		return "The diff exceeds 20 MiB; only a summary is available."
	case GitDiffMessageImageConfirmationRequired:
		return "This image diff exceeds 2 MiB. Confirm to continue."
	case GitDiffMessageResponseBudget:
		return "The diff exceeds the safe response budget; use local Git tools to inspect it."
	default:
		return "Additional confirmation or context is required for this Git diff."
	}
}

func IsGitDiffMessageCode(code string) bool {
	switch code {
	case GitDiffMessageUntrackedDirectoryExpandRequired,
		GitDiffMessageUntrackedSymlinkSummary,
		GitDiffMessageConfirmationRequired,
		GitDiffMessageLineConfirmationRequired,
		GitDiffMessagePatchLimit,
		GitDiffMessageTimeout,
		GitDiffMessageImageConfirmationRequired,
		GitDiffMessageResponseBudget:
		return true
	default:
		return false
	}
}

func IsGitDiffTruncationReason(reason string) bool {
	return reason == GitDiffTruncationPatchLimit || reason == GitDiffTruncationTimeout ||
		reason == GitDiffTruncationResponseBudget
}

type GitChangesRequest struct {
	ProjectID       string               `json:"project_id"`
	Resources       []GitResourceRequest `json:"resources"`
	RepositoryID    string               `json:"repository_id"`
	Area            string               `json:"area"`
	Cursor          string               `json:"cursor,omitempty"`
	Limit           int                  `json:"limit"`
	Prefix          string               `json:"prefix,omitempty"`
	ExpandUntracked bool                 `json:"expand_untracked,omitempty"`
}

type GitChangesPage struct {
	SnapshotID           string                 `json:"snapshot_id"`
	RepositoryID         string                 `json:"repository_id"`
	RepositoryGeneration int64                  `json:"repository_generation"`
	Generation           int64                  `json:"generation"`
	Area                 string                 `json:"area,omitempty"`
	Items                []GitChange            `json:"items"`
	NextCursor           string                 `json:"next_cursor,omitempty"`
	Truncated            bool                   `json:"truncated"`
	Warnings             []GitRepositoryWarning `json:"warnings"`
}

type GitChange struct {
	ID             string          `json:"id"`
	PathID         string          `json:"path_id"`
	Area           string          `json:"area"`
	Status         string          `json:"status"`
	Kind           string          `json:"kind"`
	Path           GitDisplayPath  `json:"path"`
	OldPath        *GitDisplayPath `json:"old_path,omitempty"`
	DisplayPath    string          `json:"display_path"`
	OldDisplayPath string          `json:"old_display_path,omitempty"`
	Similarity     int             `json:"similarity,omitempty"`
	Rename         *GitRename      `json:"rename,omitempty"`
	Binary         bool            `json:"binary"`
	ConflictCode   string          `json:"conflict_code,omitempty"`
	Conflict       *GitConflict    `json:"conflict,omitempty"`
	Submodule      *GitSubmodule   `json:"submodule"`
	DiffAvailable  bool            `json:"diff_available"`
	Additions      int             `json:"additions"`
	Deletions      int             `json:"deletions"`
	Stats          GitDiffStats    `json:"stats"`
}

type GitDisplayPath struct {
	Token    string `json:"token"`
	Display  string `json:"display"`
	Encoding string `json:"encoding,omitempty"`
}

type GitRename struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Similarity int    `json:"similarity"`
}

type GitConflict struct {
	IndexStatus    string `json:"index_status"`
	WorktreeStatus string `json:"worktree_status"`
}

type GitSubmodule struct {
	HeadChanged      bool `json:"head_changed"`
	ModifiedContent  bool `json:"modified_content"`
	UntrackedContent bool `json:"untracked_content"`
}

type GitDiffStats struct {
	Additions int   `json:"additions"`
	Deletions int   `json:"deletions"`
	Lines     int   `json:"lines,omitempty"`
	Bytes     int64 `json:"bytes,omitempty"`
	Binary    bool  `json:"binary"`
}

type GitDiffRequest struct {
	ProjectID    string               `json:"project_id"`
	Resources    []GitResourceRequest `json:"resources"`
	RepositoryID string               `json:"repository_id"`
	ChangeID     string               `json:"change_id"`
	Context      int                  `json:"context"`
	AllowLarge   bool                 `json:"allow_large,omitempty"`
}

type GitDiffResult struct {
	SnapshotID      string             `json:"snapshot_id"`
	RepositoryID    string             `json:"repository_id"`
	ChangeID        string             `json:"change_id"`
	Generation      int64              `json:"generation"`
	Comparison      string             `json:"comparison"`
	Kind            string             `json:"kind"`
	Path            GitDisplayPath     `json:"path"`
	OldPath         *GitDisplayPath    `json:"old_path,omitempty"`
	Language        string             `json:"language,omitempty"`
	Stats           GitDiffStats       `json:"stats"`
	Patch           string             `json:"patch,omitempty"`
	Truncated       bool               `json:"truncated"`
	Truncation      *GitDiffTruncation `json:"truncation,omitempty"`
	ConfirmRequired bool               `json:"confirm_required"`
	MessageCode     string             `json:"message_code,omitempty"`
	Message         string             `json:"message,omitempty"`
	Images          *GitDiffImages     `json:"images,omitempty"`
}

type GitDiffTruncation struct {
	Truncated  bool   `json:"truncated"`
	Reason     string `json:"reason"`
	LimitBytes int64  `json:"limit_bytes,omitempty"`
}

type GitDiffImages struct {
	Before *GitDiffImage `json:"before,omitempty"`
	After  *GitDiffImage `json:"after,omitempty"`
}

type GitDiffImage struct {
	MediaType string `json:"mime_type"`
	DataURL   string `json:"data_url"`
	SizeBytes int64  `json:"size_bytes"`
}

type GitRefreshRequest struct {
	ProjectID string               `json:"project_id"`
	Resources []GitResourceRequest `json:"resources"`
}

type GitRefreshResult struct {
	Refreshed bool `json:"refreshed"`
}

type GitEvent struct {
	RepositoryID string `json:"repository_id"`
	Generation   int64  `json:"generation"`
	Reason       string `json:"reason"`
}

// GitRunWorkspaceSnapshot is a bounded observation of a repository at one
// edge of a Run. It is deliberately described as an observation: files can be
// changed concurrently by the user or another Agent, so neither phase proves
// exclusive authorship by the Run.
type GitRunWorkspaceSnapshot struct {
	RepositoryID string                 `json:"repository_id"`
	Label        string                 `json:"label"`
	RootDisplay  string                 `json:"root_display"`
	Phase        string                 `json:"phase"`
	CapturedAt   time.Time              `json:"captured_at"`
	Areas        []GitRunPatchArea      `json:"areas"`
	Untracked    []GitRunUntrackedEntry `json:"untracked"`
	Warnings     []GitRepositoryWarning `json:"warnings"`
	Truncated    bool                   `json:"truncated"`
	ContentHash  string                 `json:"content_hash"`
}

// GitRunPatchArea contains one aggregate, repository-relative unified patch.
// Binary files are represented by Git metadata only; binary payloads are never
// requested. Paths is also used for conflict summaries when no patch is safe.
type GitRunPatchArea struct {
	Area        string   `json:"area"`
	Patch       string   `json:"patch,omitempty"`
	Paths       []string `json:"paths"`
	BinaryPaths []string `json:"binary_paths"`
	Truncated   bool     `json:"truncated"`
}

type GitRunUntrackedEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// GitProviderChangeReport is normalized only from explicit Provider event
// fields. Source identifies the original event shape, while the UI presents
// every value of this type as an Agent report rather than a Git observation.
type GitProviderChangeReport struct {
	ID             string    `json:"id"`
	Sequence       int64     `json:"seq"`
	OccurredAt     time.Time `json:"occurred_at"`
	Source         string    `json:"source"`
	ProviderTurnID string    `json:"provider_turn_id,omitempty"`
	Patch          string    `json:"patch,omitempty"`
	Paths          []string  `json:"paths"`
	// PathsVerified is true only when every reported path is a safe,
	// repository-relative path. Absolute paths, file URIs and parent-relative
	// paths are reduced to non-sensitive display labels and must never be
	// grouped into the Run repository by a consumer.
	PathsVerified bool   `json:"paths_verified"`
	Scope         string `json:"scope"`
	Status        string `json:"status,omitempty"`
	Truncated     bool   `json:"truncated"`
}

type GitRunChangesResult struct {
	RunID                    string                    `json:"run_id"`
	Snapshots                []GitRunWorkspaceSnapshot `json:"snapshots"`
	ProviderReports          []GitProviderChangeReport `json:"provider_reports"`
	ProviderReportsTruncated bool                      `json:"provider_reports_truncated"`
	NextProviderCursor       string                    `json:"next_provider_cursor,omitempty"`
}

// Snapshot-oriented aliases keep names consistent across the embedded and
// browser-facing layers without duplicating wire structures.
type GitRepositoriesSnapshot = GitRepositoryList
type GitRepositoryBranch = GitBranchSummary
type GitRepositoryCounts = GitChangeCounts
type GitRepositoryEvent = GitEvent
type GitDiffResponse = GitDiffResult

// GitReadError is intentionally path-free. Implementations must never put a
// repository path, source text, stderr, or credentials in Message.
type GitReadError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *GitReadError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
