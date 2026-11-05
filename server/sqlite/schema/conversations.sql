-- 图对话：项目下的一棵对话树，节点内是单 Agent 的消息流。
-- 与 Issue 完全独立：不共用评论、mention、实例和 Goal 表。
CREATE TABLE conversation_graphs (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL,
    computer_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (computer_id) REFERENCES computers(id)
) STRICT;

CREATE INDEX conversation_graphs_project
    ON conversation_graphs(project_id, updated_at DESC)
    WHERE deleted_at IS NULL;

-- 节点：一次对话。parent_id 为 NULL 时是根节点。
CREATE TABLE conversation_nodes (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    graph_id TEXT NOT NULL,
    parent_id TEXT,
    fork_message_id TEXT,
    agent_id TEXT NOT NULL,
    agent_revision_id TEXT,
    -- Immutable fork context, independent of later edits to ancestors.
    inherited_messages TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(inherited_messages) AND json_type(inherited_messages) = 'array'),
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'idle' CHECK (
        status IN ('idle', 'running', 'paused', 'failed')
    ),
    position_x REAL NOT NULL DEFAULT 0,
    position_y REAL NOT NULL DEFAULT 0,
    width REAL NOT NULL DEFAULT 380,
    height REAL NOT NULL DEFAULT 560,
    locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    issue_id TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (graph_id) REFERENCES conversation_graphs(id),
    FOREIGN KEY (parent_id) REFERENCES conversation_nodes(id),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (issue_id) REFERENCES issues(id)
) STRICT;

CREATE INDEX conversation_nodes_graph
    ON conversation_nodes(graph_id, created_at, id)
    WHERE deleted_at IS NULL;
CREATE INDEX conversation_nodes_parent
    ON conversation_nodes(parent_id, created_at)
    WHERE parent_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX conversation_nodes_issue
    ON conversation_nodes(issue_id)
    WHERE issue_id IS NOT NULL AND deleted_at IS NULL;

-- 节点消息：用户输入或 Agent 最终回复。
CREATE TABLE conversation_messages (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    node_id TEXT NOT NULL,
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent')),
    body TEXT NOT NULL CHECK (length(trim(body)) > 0),
    status TEXT NOT NULL DEFAULT 'published' CHECK (
        status IN ('working', 'published', 'failed')
    ),
    run_id TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    FOREIGN KEY (node_id) REFERENCES conversation_nodes(id),
    FOREIGN KEY (run_id) REFERENCES runs(id)
) STRICT;

CREATE INDEX conversation_messages_node
    ON conversation_messages(node_id, created_at, id)
    WHERE deleted_at IS NULL;
CREATE INDEX conversation_messages_run
    ON conversation_messages(run_id, created_at)
    WHERE run_id IS NOT NULL;

-- 图对话的执行同样走 runs，但 issue_id 为空、conversation_node_id 非空。
CREATE UNIQUE INDEX runs_conversation_idempotency
    ON runs(conversation_node_id, idempotency_key)
    WHERE conversation_node_id IS NOT NULL;
CREATE UNIQUE INDEX runs_conversation_active
    ON runs(conversation_node_id)
    WHERE conversation_node_id IS NOT NULL AND deleted_at IS NULL
      AND status IN ('queued', 'starting', 'running', 'waiting_approval', 'cancelling');
CREATE INDEX runs_conversation_created
    ON runs(conversation_node_id, created_at DESC)
    WHERE conversation_node_id IS NOT NULL;
