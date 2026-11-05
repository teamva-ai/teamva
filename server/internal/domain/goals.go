package domain

import "time"

const (
	GoalActive        = "active"
	GoalPaused        = "paused"
	GoalBlocked       = "blocked"
	GoalBudgetReached = "budget_reached"
	GoalCompleted     = "completed"
	GoalCancelled     = "cancelled"

	GoalPhaseRunning     = "running"
	GoalPhaseEvaluating  = "evaluating"
	GoalPhaseFinalizing  = "finalizing"
	GoalPhaseDispatching = "dispatching"
	GoalPhaseSettled     = "settled"

	RunPurposeWork             = "work"
	RunPurposeGoalContinuation = "goal_continuation"
	RunPurposeGoalAudit        = "goal_audit"
	RunPurposeGoalFinalize     = "goal_finalize"
)

const (
	GoalObjectiveLimit          = 5000
	GoalMinimumTokenBudget      = 1000
	GoalMaximumTokenBudget      = 100_000_000
	GoalDefaultMaxContinuations = 20
)

// IssueAgentGoal is a durable, self-continuing objective attached to exactly
// one Issue Agent conversation. Work, independent audits, and final summary
// publication are separate Runs linked through CurrentRunID.
type IssueAgentGoal struct {
	Versioned
	IssueID              string     `json:"issue_id"`
	IssueAgentInstanceID string     `json:"issue_agent_instance_id"`
	AgentID              string     `json:"agent_id"`
	AgentRevisionID      string     `json:"agent_revision_id"`
	Objective            string     `json:"objective"`
	Status               string     `json:"status"`
	Phase                string     `json:"phase"`
	TokenBudget          *int64     `json:"token_budget"`
	TokensUsed           int64      `json:"tokens_used"`
	ContinuationsUsed    int        `json:"continuations_used"`
	MaxContinuations     int        `json:"max_continuations"`
	BlockedStreak        int        `json:"blocked_streak"`
	AuditFailureStreak   int        `json:"audit_failure_streak"`
	FinalizeAttempts     int        `json:"finalize_attempts"`
	Note                 string     `json:"note,omitempty"`
	StatusReason         string     `json:"status_reason,omitempty"`
	SummaryCommentID     string     `json:"summary_comment_id"`
	CurrentRunID         string     `json:"current_run_id,omitempty"`
	LastWorkRunID        string     `json:"last_work_run_id,omitempty"`
	SettledAt            *time.Time `json:"settled_at,omitempty"`
}

func IsGoalLive(status string) bool {
	switch status {
	case GoalActive, GoalPaused, GoalBlocked, GoalBudgetReached:
		return true
	default:
		return false
	}
}
