-- Teamva desktop control-plane baseline.
--
-- SQLite values use the following canonical representations:
--   * IDs and UTC timestamps: TEXT
--   * JSON documents: TEXT guarded by json_valid()
--   * booleans: INTEGER guarded by CHECK(value IN (0, 1))
-- Only initialize an empty database. Existing other baselines are rejected.

CREATE TABLE app_metadata (
    key TEXT PRIMARY KEY CHECK (length(trim(key)) > 0),
    value TEXT NOT NULL CHECK (json_valid(value)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;

CREATE TABLE users (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    email TEXT NOT NULL UNIQUE CHECK (
        length(trim(email)) > 0 AND email = lower(trim(email))
    ),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT
) STRICT;

CREATE TABLE user_preferences (
    user_id TEXT PRIMARY KEY,
    locale TEXT NOT NULL DEFAULT 'zh-CN' CHECK (locale IN ('zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'pt-BR', 'ru-RU')),
    locale_explicit INTEGER NOT NULL DEFAULT 0 CHECK (locale_explicit IN (0, 1)),
    theme TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark')),
    allow_write INTEGER NOT NULL DEFAULT 0 CHECK (allow_write IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE web_sessions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE CHECK (length(trim(token_hash)) > 0),
    previous_token_hash TEXT,
    previous_token_expires_at TEXT,
    idle_expires_at TEXT NOT NULL CHECK (length(trim(idle_expires_at)) > 0),
    revoked_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    rotated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_seen_at TEXT,
    device_id TEXT NOT NULL DEFAULT '',
    ip_hmac TEXT NOT NULL DEFAULT '',
    user_agent_hmac TEXT NOT NULL DEFAULT '',
    CHECK (
        (previous_token_hash IS NULL AND previous_token_expires_at IS NULL) OR
        (previous_token_hash IS NOT NULL AND previous_token_expires_at IS NOT NULL)
    ),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) STRICT;

CREATE UNIQUE INDEX web_sessions_previous_token_unique
    ON web_sessions(previous_token_hash)
    WHERE previous_token_hash IS NOT NULL;
CREATE UNIQUE INDEX web_sessions_user_device_active
    ON web_sessions(user_id, device_id)
    WHERE revoked_at IS NULL AND device_id <> '';
CREATE INDEX web_sessions_active
    ON web_sessions(token_hash, idle_expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX web_sessions_previous_token
    ON web_sessions(previous_token_hash, previous_token_expires_at)
    WHERE revoked_at IS NULL AND previous_token_hash IS NOT NULL;
CREATE INDEX web_sessions_user_active
    ON web_sessions(user_id, created_at DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE computers (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    user_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    note TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL CHECK (length(trim(platform)) > 0),
    architecture TEXT NOT NULL CHECK (length(trim(architecture)) > 0),
    fingerprint TEXT NOT NULL CHECK (length(trim(fingerprint)) > 0),
    status TEXT NOT NULL CHECK (status IN ('online', 'offline', 'revoked')),
    last_seen_at TEXT,
    node_version TEXT NOT NULL DEFAULT '',
    computer_use_enabled INTEGER NOT NULL DEFAULT 0 CHECK (computer_use_enabled IN (0, 1)),
    protocol_version INTEGER NOT NULL DEFAULT 1 CHECK (protocol_version > 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id)
) STRICT;

CREATE UNIQUE INDEX computers_user_fingerprint_active
    ON computers(user_id, fingerprint)
    WHERE deleted_at IS NULL AND status <> 'revoked';
CREATE INDEX computers_user_status
    ON computers(user_id, status, created_at);

CREATE TABLE agent_services (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    computer_id TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (length(trim(provider)) > 0),
    provider_version TEXT NOT NULL DEFAULT '',
    login_status TEXT NOT NULL CHECK (length(trim(login_status)) > 0),
    status TEXT NOT NULL CHECK (status IN ('probing', 'ready', 'unavailable')),
    capability_manifest TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capability_manifest)),
    capability_version INTEGER NOT NULL DEFAULT 1 CHECK (capability_version > 0),
    last_probed_at TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (computer_id) REFERENCES computers(id)
) STRICT;

CREATE UNIQUE INDEX agent_services_computer_provider_active
    ON agent_services(computer_id, provider)
    WHERE deleted_at IS NULL;
CREATE INDEX agent_services_computer_status
    ON agent_services(computer_id, status, created_at);

CREATE TABLE projects (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    computer_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    theme TEXT NOT NULL DEFAULT 'hallmark' CHECK (theme IN ('hallmark', 'chamber', 'flexoki')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (computer_id) REFERENCES computers(id)
) STRICT;

CREATE INDEX projects_computer_active
    ON projects(computer_id, created_at)
    WHERE deleted_at IS NULL;

CREATE TABLE project_resources (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('code_directory', 'document_directory', 'url')),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    local_reference TEXT,
    url TEXT,
    status TEXT NOT NULL CHECK (length(trim(status)) > 0),
    metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    git_detected INTEGER NOT NULL DEFAULT 0 CHECK (git_detected IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    CHECK (
        (kind IN ('code_directory', 'document_directory') AND
            local_reference IS NOT NULL AND length(trim(local_reference)) > 0 AND url IS NULL) OR
        (kind = 'url' AND url IS NOT NULL AND length(trim(url)) > 0 AND local_reference IS NULL)
    ),
    FOREIGN KEY (project_id) REFERENCES projects(id)
) STRICT;

CREATE INDEX project_resources_project_active
    ON project_resources(project_id, created_at)
    WHERE deleted_at IS NULL;
CREATE INDEX project_resources_ready_code
    ON project_resources(project_id, kind, created_at)
    WHERE deleted_at IS NULL AND status = 'ready';

CREATE TABLE skill_packages (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    slug TEXT NOT NULL UNIQUE CHECK (length(trim(slug)) > 0),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    source_url TEXT,
    license TEXT,
    builtin INTEGER NOT NULL DEFAULT 0 CHECK (builtin IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT
) STRICT;

CREATE INDEX skill_packages_active
    ON skill_packages(builtin DESC, name, id)
    WHERE deleted_at IS NULL;

CREATE TABLE skill_package_versions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    skill_package_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL CHECK (length(trim(commit_sha)) > 0),
    content_digest TEXT NOT NULL CHECK (length(trim(content_digest)) > 0),
    manifest TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(manifest)),
    file_count INTEGER NOT NULL DEFAULT 0 CHECK (file_count >= 0),
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    executable_files TEXT NOT NULL DEFAULT '[]' CHECK (
        json_valid(executable_files) AND json_type(executable_files) = 'array'
    ),
    provider_install TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(provider_install)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    UNIQUE (skill_package_id, commit_sha),
    FOREIGN KEY (skill_package_id) REFERENCES skill_packages(id)
) STRICT;

CREATE INDEX skill_package_versions_package
    ON skill_package_versions(skill_package_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- agents.current_revision_id and agent_revisions.agent_id form a deliberate
-- deferred cycle. The composite FK prevents an Agent from selecting another
-- Agent's revision as its current revision.
CREATE TABLE agents (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    computer_id TEXT NOT NULL,
    agent_service_id TEXT,
    system_role TEXT NOT NULL DEFAULT '' CHECK(system_role IN ('','general')),
    model TEXT,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    note TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    current_revision_id TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    CHECK (system_role = 'general' OR agent_service_id IS NOT NULL),
    FOREIGN KEY (computer_id) REFERENCES computers(id),
    FOREIGN KEY (agent_service_id) REFERENCES agent_services(id),
    FOREIGN KEY (current_revision_id, id)
        REFERENCES agent_revisions(id, agent_id)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE agent_revisions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    agent_id TEXT NOT NULL,
    agent_service_id TEXT NOT NULL,
    model TEXT,
    system_prompt TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    UNIQUE (id, agent_id),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_service_id) REFERENCES agent_services(id)
) STRICT;

CREATE INDEX agents_computer_active
    ON agents(computer_id, created_at)
    WHERE deleted_at IS NULL;
CREATE INDEX agents_service_active
    ON agents(agent_service_id, status)
    WHERE deleted_at IS NULL;
CREATE INDEX agent_revisions_agent
    ON agent_revisions(agent_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE agent_revision_skill_packages (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    agent_revision_id TEXT NOT NULL,
    skill_package_version_id TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    UNIQUE (agent_revision_id, skill_package_version_id),
    UNIQUE (agent_revision_id, position),
    FOREIGN KEY (agent_revision_id) REFERENCES agent_revisions(id) ON DELETE CASCADE,
    FOREIGN KEY (skill_package_version_id) REFERENCES skill_package_versions(id)
) STRICT;

CREATE INDEX agent_revision_skills_version
    ON agent_revision_skill_packages(skill_package_version_id, agent_revision_id);

CREATE TABLE issues (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open' CHECK (
        status IN ('open', 'resolved', 'closed')
    ),
    worktree_hint INTEGER NOT NULL DEFAULT 0 CHECK (worktree_hint IN (0, 1)),
    created_by_user_id TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (created_by_user_id) REFERENCES users(id)
) STRICT;

CREATE INDEX issues_project_active
    ON issues(project_id, created_at)
    WHERE deleted_at IS NULL;
CREATE INDEX issues_project_status
    ON issues(project_id, status, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE issue_comments (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    issue_id TEXT NOT NULL,
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent')),
    author_user_id TEXT,
    author_agent_id TEXT,
    body TEXT NOT NULL CHECK (length(trim(body)) > 0),
    run_id TEXT,
    edited_at TEXT,
    status TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('working', 'published', 'failed')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    CHECK (
        (author_kind = 'user' AND author_user_id IS NOT NULL AND author_agent_id IS NULL) OR
        (author_kind = 'agent' AND author_agent_id IS NOT NULL AND author_user_id IS NULL)
    ),
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (author_user_id) REFERENCES users(id),
    FOREIGN KEY (author_agent_id) REFERENCES agents(id),
    FOREIGN KEY (run_id) REFERENCES runs(id)
) STRICT;

CREATE INDEX issue_comments_issue_created
    ON issue_comments(issue_id, created_at, id)
    WHERE deleted_at IS NULL;
CREATE INDEX issue_comments_run
    ON issue_comments(run_id, created_at)
    WHERE run_id IS NOT NULL;

CREATE TABLE issue_comment_mentions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    comment_id TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('user', 'agent')),
    target_agent_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'processing', 'completed', 'dead_letter')
    ),
    available_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    claimed_at TEXT,
    lease_until TEXT,
    claim_token TEXT,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    consumed_at TEXT,
    CHECK (
        (target_kind = 'user' AND target_agent_id IS NULL) OR
        (target_kind = 'agent' AND target_agent_id IS NOT NULL)
    ),
    CHECK (
        status <> 'processing' OR
        (claimed_at IS NOT NULL AND lease_until IS NOT NULL AND claim_token IS NOT NULL)
    ),
    CHECK (status <> 'completed' OR consumed_at IS NOT NULL),
    FOREIGN KEY (comment_id) REFERENCES issue_comments(id) ON DELETE CASCADE,
    FOREIGN KEY (target_agent_id) REFERENCES agents(id)
) STRICT;

CREATE UNIQUE INDEX issue_comment_mentions_user_unique
    ON issue_comment_mentions(comment_id, target_kind)
    WHERE target_kind = 'user';
-- A reused session summary can delegate to the same Agent again. Every
-- activation is a new mention; tool request idempotency is fenced separately.
CREATE INDEX issue_comment_mentions_agent
    ON issue_comment_mentions(comment_id, target_agent_id)
    WHERE target_kind = 'agent';
CREATE INDEX issue_comment_mentions_pending
    ON issue_comment_mentions(available_at, comment_id, id)
    WHERE target_kind = 'agent' AND status = 'pending';
CREATE INDEX issue_comment_mentions_lease
    ON issue_comment_mentions(lease_until, id)
    WHERE target_kind = 'agent' AND status = 'processing';

CREATE TABLE issue_comment_revisions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    comment_id TEXT NOT NULL,
    body TEXT NOT NULL CHECK (length(trim(body)) > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    FOREIGN KEY (comment_id) REFERENCES issue_comments(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX issue_comment_revisions_comment
    ON issue_comment_revisions(comment_id, created_at, id);

CREATE TABLE issue_agent_instances (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    issue_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    agent_revision_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'idle' CHECK (
        status IN ('idle', 'running', 'paused', 'queued', 'resource_exhausted')
    ),
    last_activity_at TEXT,
    pending_mention_count INTEGER NOT NULL DEFAULT 0 CHECK (pending_mention_count >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_revision_id, agent_id) REFERENCES agent_revisions(id, agent_id)
) STRICT;

CREATE INDEX issue_agent_instances_issue_agent
    ON issue_agent_instances(issue_id, agent_id, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX issue_agent_instances_issue_active
    ON issue_agent_instances(issue_id, status, updated_at)
    WHERE deleted_at IS NULL;
CREATE INDEX issue_agent_instances_agent
    ON issue_agent_instances(agent_id, status)
    WHERE deleted_at IS NULL;

CREATE TABLE issue_agent_generations (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    issue_agent_instance_id TEXT NOT NULL,
    agent_revision_id TEXT NOT NULL,
    native_session_key TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (issue_agent_instance_id) REFERENCES issue_agent_instances(id),
    FOREIGN KEY (agent_revision_id) REFERENCES agent_revisions(id)
) STRICT;

CREATE INDEX issue_agent_generations_instance
    ON issue_agent_generations(issue_agent_instance_id, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX issue_agent_generations_native_session
    ON issue_agent_generations(issue_agent_instance_id, native_session_key)
    WHERE deleted_at IS NULL AND native_session_key IS NOT NULL;

CREATE TABLE runs (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    issue_id TEXT,
    issue_agent_instance_id TEXT,
    issue_agent_generation_id TEXT,
    agent_id TEXT,
    project_id TEXT,
    assistant_session_id TEXT REFERENCES assistant_sessions(id),
    computer_id TEXT,
    status TEXT NOT NULL CHECK (
        status IN (
            'queued', 'starting', 'running', 'waiting_approval', 'cancelling',
            'succeeded', 'failed', 'cancelled', 'lost'
        )
    ),
    agent_service_id TEXT,
    provider TEXT,
    model TEXT,
    read_only INTEGER NOT NULL DEFAULT 1 CHECK (read_only IN (0, 1)),
    lease_epoch INTEGER NOT NULL DEFAULT 1 CHECK (lease_epoch > 0),
    input TEXT NOT NULL CHECK (json_valid(input) AND json_type(input) = 'array'),
    snapshot TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(snapshot)),
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    started_at TEXT,
    finished_at TEXT,
    terminal_reason TEXT,
    goal_id TEXT REFERENCES issue_agent_goals(id),
    purpose TEXT NOT NULL DEFAULT 'work' CHECK (purpose IN ('work', 'goal_continuation', 'goal_audit', 'goal_finalize', 'work_assistant')),
    conversation_node_id TEXT REFERENCES conversation_nodes(id),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    CHECK ((assistant_session_id IS NULL AND project_id IS NOT NULL) OR (assistant_session_id IS NOT NULL AND project_id IS NULL AND issue_id IS NULL AND read_only=1 AND purpose='work_assistant')),
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (issue_agent_instance_id) REFERENCES issue_agent_instances(id),
    FOREIGN KEY (issue_agent_generation_id) REFERENCES issue_agent_generations(id),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (computer_id) REFERENCES computers(id),
    FOREIGN KEY (agent_service_id) REFERENCES agent_services(id)
) STRICT;

CREATE UNIQUE INDEX runs_issue_idempotency
    ON runs(issue_id, idempotency_key)
    WHERE issue_id IS NOT NULL;
CREATE UNIQUE INDEX runs_project_idempotency_without_issue
    ON runs(project_id, idempotency_key)
    WHERE issue_id IS NULL;
CREATE INDEX runs_issue_created
    ON runs(issue_id, created_at DESC)
    WHERE issue_id IS NOT NULL;
CREATE INDEX runs_issue_active
    ON runs(issue_id, status, created_at)
    WHERE issue_id IS NOT NULL AND deleted_at IS NULL
      AND status IN ('queued', 'starting', 'running', 'waiting_approval', 'cancelling');
CREATE INDEX runs_instance_active
    ON runs(issue_agent_instance_id, status, created_at)
    WHERE issue_agent_instance_id IS NOT NULL AND deleted_at IS NULL
      AND status IN ('queued', 'starting', 'running', 'waiting_approval', 'cancelling');
CREATE INDEX runs_dispatch_queue
    ON runs(status, created_at, id)
    WHERE deleted_at IS NULL AND status = 'queued';
CREATE INDEX runs_computer_active
    ON runs(computer_id, status, created_at)
    WHERE computer_id IS NOT NULL AND deleted_at IS NULL
      AND status IN ('queued', 'starting', 'running', 'waiting_approval', 'cancelling');

CREATE INDEX runs_instance_created
    ON runs(issue_agent_instance_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- Keep only the result metadata, never another copy of the comment body.
CREATE TABLE issue_tool_comment_requests (
    run_id TEXT NOT NULL REFERENCES runs(id),
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    fingerprint TEXT NOT NULL,
    comment_id TEXT NOT NULL REFERENCES issue_comments(id),
    comment_version INTEGER NOT NULL CHECK (comment_version > 0),
    edited_at TEXT,
    PRIMARY KEY (run_id, idempotency_key)
) STRICT;

CREATE TABLE run_events (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    run_id TEXT NOT NULL,
    lease_epoch INTEGER NOT NULL DEFAULT 1 CHECK (lease_epoch > 0),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    runtime_sequence INTEGER CHECK (runtime_sequence IS NULL OR runtime_sequence > 0),
    event_type TEXT NOT NULL CHECK (length(trim(event_type)) > 0),
    payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    payload_wire TEXT NOT NULL,
    event_wire_prefix TEXT NOT NULL,
    event_wire_suffix TEXT NOT NULL,
    payload_wire_bytes INTEGER NOT NULL,
    event_wire_bytes INTEGER NOT NULL,
    occurred_at TEXT NOT NULL CHECK (length(trim(occurred_at)) > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX run_events_run_time
    ON run_events(run_id, occurred_at, id);
CREATE UNIQUE INDEX run_events_runtime_sequence
    ON run_events(run_id, lease_epoch, runtime_sequence)
    WHERE runtime_sequence IS NOT NULL;
CREATE INDEX run_events_detail_retention
    ON run_events(created_at, id)
    WHERE event_type IN (
        'output.delta', 'provider.event', 'provider.item',
        'provider.tool', 'workspace.diff'
    );

CREATE TABLE approvals (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    issue_id TEXT,
    run_id TEXT NOT NULL,
    provider_approval_ref TEXT,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')
    ),
    action_kind TEXT NOT NULL CHECK (length(trim(action_kind)) > 0),
    action_digest TEXT NOT NULL CHECK (length(trim(action_digest)) > 0),
    normalized_action TEXT NOT NULL CHECK (json_valid(normalized_action)),
    risk TEXT NOT NULL CHECK (risk IN ('low', 'medium', 'high', 'critical')),
    expires_at TEXT NOT NULL CHECK (length(trim(expires_at)) > 0),
    decision_scope TEXT,
    decided_at TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    CHECK (
        (status IN ('approved', 'denied') AND decided_at IS NOT NULL) OR
        (status NOT IN ('approved', 'denied'))
    ),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (run_id) REFERENCES runs(id)
) STRICT;

CREATE UNIQUE INDEX approvals_run_provider_ref
    ON approvals(run_id, provider_approval_ref)
    WHERE provider_approval_ref IS NOT NULL;
CREATE INDEX approvals_project_status
    ON approvals(project_id, status, created_at DESC);
CREATE INDEX approvals_run_status
    ON approvals(run_id, status, created_at);
CREATE INDEX approvals_pending_expiry
    ON approvals(expires_at, created_at)
    WHERE status = 'pending';

-- Approval scope is copied from its run for efficient tenant/status queries,
-- but the run remains authoritative. These guards prevent direct SQL writers
-- from creating or later moving a cross-project/cross-issue approval.
CREATE TRIGGER approvals_scope_insert
BEFORE INSERT ON approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE runs.id = NEW.run_id
      AND runs.project_id = NEW.project_id
      AND COALESCE(runs.issue_id, '') = COALESCE(NEW.issue_id, '')
)
BEGIN
    SELECT RAISE(ABORT, 'approval scope must match run');
END;

CREATE TRIGGER approvals_scope_update
BEFORE UPDATE OF run_id, project_id, issue_id ON approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE runs.id = NEW.run_id
      AND runs.project_id = NEW.project_id
      AND COALESCE(runs.issue_id, '') = COALESCE(NEW.issue_id, '')
)
BEGIN
    SELECT RAISE(ABORT, 'approval scope must match run');
END;

CREATE TABLE policy_rules (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    action_kind TEXT NOT NULL CHECK (length(trim(action_kind)) > 0),
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny', 'require_approval')),
    conditions TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(conditions)),
    expires_at TEXT,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id)
) STRICT;

CREATE INDEX policy_rules_project_active
    ON policy_rules(project_id, enabled, action_kind, created_at)
    WHERE deleted_at IS NULL;
CREATE INDEX policy_rules_expiry
    ON policy_rules(expires_at)
    WHERE enabled = 1 AND deleted_at IS NULL AND expires_at IS NOT NULL;

CREATE TABLE project_memory_entries (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    memory_key TEXT NOT NULL CHECK (length(trim(memory_key)) > 0),
    kind TEXT NOT NULL CHECK (length(trim(kind)) > 0),
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    content TEXT NOT NULL,
    source_type TEXT NOT NULL CHECK (length(trim(source_type)) > 0),
    source_ref TEXT,
    current_version INTEGER NOT NULL DEFAULT 1 CHECK (current_version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id)
) STRICT;

CREATE UNIQUE INDEX project_memory_key_active
    ON project_memory_entries(project_id, memory_key)
    WHERE deleted_at IS NULL;
CREATE INDEX project_memories_project_active
    ON project_memory_entries(project_id, kind, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE project_memory_versions (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    memory_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    content TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (length(trim(actor_type)) > 0),
    actor_ref TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    UNIQUE (memory_id, version),
    FOREIGN KEY (memory_id) REFERENCES project_memory_entries(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX project_memory_versions_memory
    ON project_memory_versions(memory_id, version DESC);

CREATE TABLE scheduled_tasks (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    schedule_kind TEXT NOT NULL CHECK (schedule_kind IN ('once', 'cron')),
    schedule_expression TEXT NOT NULL CHECK (length(trim(schedule_expression)) > 0),
    timezone TEXT NOT NULL CHECK (length(trim(timezone)) > 0),
    prompt TEXT NOT NULL CHECK (json_valid(prompt) AND json_type(prompt) = 'array'),
    agent_id TEXT,
    concurrency_policy TEXT NOT NULL DEFAULT 'forbid' CHECK (
        concurrency_policy IN ('forbid', 'allow')
    ),
    misfire_policy TEXT NOT NULL DEFAULT 'skip' CHECK (misfire_policy IN ('skip', 'run_once')),
    next_run_at TEXT,
    dispatch_attempts INTEGER NOT NULL DEFAULT 0 CHECK (dispatch_attempts >= 0),
    dispatch_retry_at TEXT,
    last_dispatch_error TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (agent_id) REFERENCES agents(id)
) STRICT;

CREATE INDEX scheduled_tasks_project_active
    ON scheduled_tasks(project_id, created_at)
    WHERE deleted_at IS NULL;
CREATE INDEX scheduled_tasks_due
    ON scheduled_tasks(next_run_at, dispatch_retry_at, id)
    WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at IS NOT NULL;

CREATE TABLE scheduled_task_runs (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    scheduled_task_id TEXT NOT NULL,
    run_id TEXT,
    trigger_key TEXT NOT NULL CHECK (length(trim(trigger_key)) > 0),
    scheduled_for TEXT NOT NULL CHECK (length(trim(scheduled_for)) > 0),
    status TEXT NOT NULL CHECK (
        status IN (
            'pending', 'running', 'succeeded', 'failed', 'cancelled', 'lost',
            'skipped_misfire', 'skipped_concurrency'
        )
    ),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    UNIQUE (scheduled_task_id, trigger_key),
    UNIQUE (run_id),
    FOREIGN KEY (scheduled_task_id) REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id)
) STRICT;

CREATE INDEX scheduled_task_runs_task_time
    ON scheduled_task_runs(scheduled_task_id, scheduled_for DESC);
CREATE INDEX scheduled_task_runs_status
    ON scheduled_task_runs(status, scheduled_for);

CREATE TABLE command_outbox (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    computer_id TEXT NOT NULL,
    command_name TEXT NOT NULL CHECK (length(trim(command_name)) > 0),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(trim(idempotency_key)) > 0),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    result TEXT CHECK (result IS NULL OR json_valid(result)),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'processing', 'uncertain', 'completed', 'failed', 'expired')
    ),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    claimed_at TEXT,
    lease_until TEXT,
    claim_token TEXT,
    expires_at TEXT NOT NULL CHECK (length(trim(expires_at)) > 0),
    completed_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    CHECK (
        status <> 'processing' OR
        (claimed_at IS NOT NULL AND lease_until IS NOT NULL AND claim_token IS NOT NULL)
    ),
    CHECK (status NOT IN ('completed', 'failed', 'expired') OR completed_at IS NOT NULL),
    FOREIGN KEY (computer_id) REFERENCES computers(id)
) STRICT;

CREATE INDEX command_outbox_pending
    ON command_outbox(available_at, created_at, id)
    WHERE status IN ('pending', 'uncertain');
CREATE INDEX command_outbox_computer_pending
    ON command_outbox(computer_id, available_at, created_at)
    WHERE status IN ('pending', 'uncertain');
CREATE INDEX command_outbox_computer_created
    ON command_outbox(computer_id, created_at DESC);
CREATE INDEX command_outbox_lease
    ON command_outbox(lease_until, id)
    WHERE status = 'processing';

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    user_id TEXT,
    project_id TEXT,
    issue_id TEXT,
    run_id TEXT,
    computer_id TEXT,
    actor_type TEXT NOT NULL CHECK (length(trim(actor_type)) > 0),
    actor_ref TEXT,
    action TEXT NOT NULL CHECK (length(trim(action)) > 0),
    outcome TEXT NOT NULL CHECK (length(trim(outcome)) > 0),
    details TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details)),
    trace_id TEXT,
    occurred_at TEXT NOT NULL CHECK (length(trim(occurred_at)) > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    FOREIGN KEY (user_id) REFERENCES users(id),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (issue_id) REFERENCES issues(id),
    FOREIGN KEY (run_id) REFERENCES runs(id),
    FOREIGN KEY (computer_id) REFERENCES computers(id)
) STRICT;

CREATE INDEX audit_events_user_time
    ON audit_events(user_id, occurred_at DESC, id);
CREATE INDEX audit_events_project_time
    ON audit_events(project_id, occurred_at DESC, id);
CREATE INDEX audit_events_issue_time
    ON audit_events(issue_id, occurred_at, id);
CREATE INDEX audit_events_run_time
    ON audit_events(run_id, occurred_at, id);
CREATE INDEX audit_events_trace
    ON audit_events(trace_id, occurred_at)
    WHERE trace_id IS NOT NULL;
