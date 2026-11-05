CREATE TABLE issue_agent_goals (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    issue_id TEXT NOT NULL,
    issue_agent_instance_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    agent_revision_id TEXT NOT NULL,
    objective TEXT NOT NULL CHECK (length(trim(objective)) > 0 AND length(objective) <= 5000),
    status TEXT NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'paused', 'blocked', 'budget_reached', 'completed', 'cancelled')
    ),
    phase TEXT NOT NULL DEFAULT 'running' CHECK (
        phase IN ('running', 'evaluating', 'finalizing', 'dispatching', 'settled')
    ),
    token_budget INTEGER CHECK (token_budget IS NULL OR token_budget BETWEEN 1000 AND 100000000),
    tokens_used INTEGER NOT NULL DEFAULT 0 CHECK (tokens_used >= 0),
    continuations_used INTEGER NOT NULL DEFAULT 0 CHECK (continuations_used >= 0),
    max_continuations INTEGER NOT NULL DEFAULT 20 CHECK (max_continuations BETWEEN 1 AND 100),
    blocked_streak INTEGER NOT NULL DEFAULT 0 CHECK (blocked_streak >= 0),
    audit_failure_streak INTEGER NOT NULL DEFAULT 0 CHECK (audit_failure_streak >= 0),
    finalize_attempts INTEGER NOT NULL DEFAULT 0 CHECK (finalize_attempts >= 0),
    note TEXT NOT NULL DEFAULT '',
    status_reason TEXT NOT NULL DEFAULT '',
    summary_comment_id TEXT NOT NULL,
    current_run_id TEXT,
    last_work_run_id TEXT,
    settled_at TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (issue_agent_instance_id) REFERENCES issue_agent_instances(id),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_revision_id) REFERENCES agent_revisions(id),
    FOREIGN KEY (summary_comment_id) REFERENCES issue_comments(id),
    FOREIGN KEY (current_run_id) REFERENCES runs(id),
    FOREIGN KEY (last_work_run_id) REFERENCES runs(id)
) STRICT;

CREATE UNIQUE INDEX issue_agent_goals_one_live
    ON issue_agent_goals(issue_agent_instance_id)
    WHERE deleted_at IS NULL AND status IN ('active', 'paused', 'blocked', 'budget_reached');
CREATE INDEX issue_agent_goals_instance_created
    ON issue_agent_goals(issue_agent_instance_id, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX issue_agent_goals_active
    ON issue_agent_goals(status, updated_at, id)
    WHERE deleted_at IS NULL AND status = 'active';

CREATE INDEX runs_goal_created
    ON runs(goal_id, created_at, id)
    WHERE goal_id IS NOT NULL AND deleted_at IS NULL;
