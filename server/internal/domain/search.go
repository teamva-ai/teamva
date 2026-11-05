package domain

import "time"

const (
	ProjectSearchMemory           = "memory"
	ProjectSearchIssueDescription = "issue_description"
	ProjectSearchIssueComment     = "issue_comment"
	ProjectSearchSessionMessage   = "session_message"
)

// ProjectSearchResult is a bounded, user-visible search hit. It intentionally
// carries navigation identifiers instead of raw Provider payloads: search must
// never expose hidden reasoning, internal control prompts, tool output or
// Provider protocol events.
type ProjectSearchResult struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	ProjectID            string    `json:"project_id"`
	Title                string    `json:"title"`
	Excerpt              string    `json:"excerpt"`
	MemoryID             string    `json:"memory_id,omitempty"`
	IssueID              string    `json:"issue_id,omitempty"`
	CommentID            string    `json:"comment_id,omitempty"`
	IssueAgentInstanceID string    `json:"issue_agent_instance_id,omitempty"`
	RunID                string    `json:"run_id,omitempty"`
	Role                 string    `json:"role,omitempty"`
	Anchor               string    `json:"anchor,omitempty"`
	OccurredAt           time.Time `json:"occurred_at"`
}

type ProjectSearchPage struct {
	Items     []ProjectSearchResult `json:"items"`
	Truncated bool                  `json:"truncated"`
}
