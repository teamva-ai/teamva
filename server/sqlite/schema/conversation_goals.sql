-- Node goals are independent of Issue comments, mentions and Issue goals.
CREATE TABLE conversation_goals (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES conversation_nodes(id),
    agent_revision_id TEXT NOT NULL REFERENCES agent_revisions(id),
    objective TEXT NOT NULL CHECK (length(trim(objective)) > 0),
    idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','paused','blocked','budget_reached','completed','cancelled')),
    phase TEXT NOT NULL CHECK (phase IN ('running','evaluating','settled')),
    token_budget INTEGER CHECK (token_budget IS NULL OR token_budget > 0),
    tokens_used INTEGER NOT NULL DEFAULT 0 CHECK (tokens_used >= 0),
    work_runs INTEGER NOT NULL DEFAULT 0 CHECK (work_runs >= 0),
    run_count INTEGER NOT NULL DEFAULT 0 CHECK (run_count >= 0),
    max_work_runs INTEGER NOT NULL CHECK (max_work_runs > 0),
    current_run_id TEXT REFERENCES runs(id),
    last_work_run_id TEXT REFERENCES runs(id),
    note TEXT NOT NULL DEFAULT '',
    status_reason TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (node_id, idempotency_key)
) STRICT;
CREATE UNIQUE INDEX conversation_goals_live_node ON conversation_goals(node_id)
    WHERE status IN ('active','paused','blocked','budget_reached');
CREATE INDEX conversation_goals_scan ON conversation_goals(status, updated_at);
CREATE TABLE conversation_goal_runs (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    goal_id TEXT NOT NULL REFERENCES conversation_goals(id),
    ordinal INTEGER NOT NULL,
    UNIQUE (goal_id, ordinal)
) STRICT;
