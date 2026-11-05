package domain

// ConversationGoal tracks a durable work/audit loop without Issue side effects.
type ConversationGoal struct {
	Versioned
	NodeID          string `json:"node_id"`
	AgentRevisionID string `json:"agent_revision_id"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	Phase           string `json:"phase"`
	TokenBudget     *int64 `json:"token_budget"`
	TokensUsed      int64  `json:"tokens_used"`
	WorkRuns        int    `json:"work_runs"`
	RunCount        int    `json:"run_count"`
	MaxWorkRuns     int    `json:"max_work_runs"`
	CurrentRunID    string `json:"current_run_id,omitempty"`
	LastWorkRunID   string `json:"last_work_run_id,omitempty"`
	Note            string `json:"note"`
	StatusReason    string `json:"status_reason"`
}
