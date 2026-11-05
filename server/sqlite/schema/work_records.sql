-- Work history is separate from technical/audit logs. References intentionally
-- have no object FKs: deleting a task must not delete the user's history.
CREATE TABLE work_records (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK (kind IN ('operation','change','navigation')),
    action TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL DEFAULT '',
    object_type TEXT NOT NULL DEFAULT '',
    object_id TEXT NOT NULL DEFAULT '',
    object_version INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    client_id TEXT NOT NULL DEFAULT '',
    page_id TEXT NOT NULL DEFAULT '',
    entry_type TEXT NOT NULL DEFAULT 'system',
    sequence INTEGER NOT NULL DEFAULT 0,
    outcome TEXT NOT NULL DEFAULT 'succeeded',
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z','now')),
    finished_at TEXT,
    refs TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(refs)),
    before_values TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(before_values)),
    after_values TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(after_values)),
    error_code TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX work_records_time ON work_records(user_id,occurred_at DESC,id DESC);
CREATE INDEX work_records_project ON work_records(user_id,project_id,occurred_at DESC,id DESC);
CREATE INDEX work_records_object ON work_records(user_id,object_type,object_id,object_version);
CREATE INDEX work_records_parent ON work_records(user_id,parent_id,occurred_at,id);
CREATE INDEX work_records_run ON work_records(user_id,run_id,occurred_at,id);
CREATE INDEX work_records_client ON work_records(user_id,client_id,page_id,sequence);

CREATE TABLE work_record_filters (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL CHECK(length(trim(name)) BETWEEN 1 AND 100),
    query TEXT NOT NULL CHECK(json_valid(query)),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE assistant_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    scope TEXT NOT NULL DEFAULT 'work_history' CHECK(scope IN ('work_history','inspirations')),
    title TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX assistant_sessions_owner ON assistant_sessions(user_id,scope,updated_at DESC,id);
CREATE TABLE assistant_messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES assistant_sessions(id),
    author_kind TEXT NOT NULL CHECK(author_kind IN ('user','agent')),
    body TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL REFERENCES runs(id),
    query TEXT NOT NULL CHECK(json_valid(query) AND json_type(query)='object'
      AND COALESCE(json_type(query,'$.query')='object',0)
      AND COALESCE(json_type(query,'$.selected_ids') IN ('array','null'),0)),
    created_at TEXT NOT NULL,
    UNIQUE(run_id,author_kind)
) STRICT;
CREATE TRIGGER assistant_message_run_scope_insert BEFORE INSERT ON assistant_messages
WHEN NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND assistant_session_id=NEW.session_id AND read_only=1 AND purpose='work_assistant')
BEGIN SELECT RAISE(ABORT,'assistant message run scope mismatch'); END;
CREATE TRIGGER assistant_message_run_scope_update BEFORE UPDATE OF run_id,session_id ON assistant_messages
WHEN NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND assistant_session_id=NEW.session_id AND read_only=1 AND purpose='work_assistant')
BEGIN SELECT RAISE(ABORT,'assistant message run scope mismatch'); END;
CREATE INDEX assistant_messages_session ON assistant_messages(session_id,created_at,id);
CREATE UNIQUE INDEX assistant_run_idempotency ON runs(assistant_session_id,idempotency_key) WHERE assistant_session_id IS NOT NULL;
CREATE UNIQUE INDEX assistant_run_active ON runs(assistant_session_id) WHERE assistant_session_id IS NOT NULL AND status IN ('queued','starting','running','waiting_approval','cancelling');
CREATE UNIQUE INDEX agents_general ON agents(computer_id) WHERE system_role='general';
CREATE TRIGGER protect_general_agent BEFORE UPDATE ON agents
WHEN OLD.system_role='general' AND (NEW.deleted_at IS NOT NULL OR NEW.system_role<>'general' OR NEW.status<>'active')
BEGIN SELECT RAISE(ABORT,'general agent cannot be deleted or disabled'); END;
CREATE TRIGGER protect_general_agent_delete BEFORE DELETE ON agents WHEN OLD.system_role='general'
BEGIN SELECT RAISE(ABORT,'general agent cannot be deleted'); END;

-- Preserve native session/turn and durable history storage identifiers only.
CREATE TRIGGER work_native_session AFTER UPDATE OF result ON command_outbox
WHEN NEW.command_name='run.start' AND json_extract(NEW.result,'$.native_session.id') IS NOT NULL
BEGIN
 UPDATE work_records SET refs=json_set(refs,
 '$.native_session_id',json_extract(NEW.result,'$.native_session.id'),
 '$.native_turn_id',json_extract(NEW.result,'$.native_session.turn_id'),
 '$.history_storage_id',json_extract(NEW.payload,'$.run_id'),
 '$.runtime_command_id',NEW.id)
 WHERE run_id=json_extract(NEW.payload,'$.run_id');
END;

CREATE TRIGGER work_assistant_message AFTER INSERT ON assistant_messages BEGIN
 INSERT INTO work_records(id,user_id,kind,action,actor_type,object_type,object_id,run_id,occurred_at,refs)
 SELECT 'wrk_'||lower(hex(randomblob(16))),s.user_id,'change','assistant.message',NEW.author_kind,'assistant_message',NEW.id,COALESCE(NEW.run_id,''),NEW.created_at,
 json_object('assistant_session_id',s.id,'agent_id',s.agent_id,'message_id',NEW.id,'run_id',NEW.run_id)
 FROM assistant_sessions s WHERE s.id=NEW.session_id;
END;
