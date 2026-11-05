-- Current execution/recovery schema. No upgrades or metadata backfills.
CREATE TABLE inbox_commands (
    message_id TEXT PRIMARY KEY, name TEXT NOT NULL, payload BLOB NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('processing','completed','failed','uncertain')),
    result BLOB, error_code TEXT, received_at TEXT NOT NULL, finished_at TEXT
);
CREATE TABLE event_log (
    id TEXT PRIMARY KEY, run_id TEXT NOT NULL, lease_epoch INTEGER NOT NULL,
    sequence INTEGER NOT NULL, event_type TEXT NOT NULL, payload BLOB NOT NULL,
    wire_bytes INTEGER NOT NULL, occurred_at TEXT NOT NULL, acked_at TEXT,
    UNIQUE(run_id, lease_epoch, sequence)
);
CREATE TABLE event_outbox_state (
    singleton INTEGER NOT NULL PRIMARY KEY CHECK(singleton = 1),
    unacked_count INTEGER NOT NULL CHECK(unacked_count >= 0),
    unacked_bytes INTEGER NOT NULL CHECK(unacked_bytes >= 0)
);
INSERT INTO event_outbox_state VALUES (1, 0, 0);
CREATE TABLE event_sequence_cursors (
    run_id TEXT NOT NULL CHECK(length(run_id) > 0),
    lease_epoch INTEGER NOT NULL CHECK(lease_epoch > 0),
    last_sequence INTEGER NOT NULL CHECK(last_sequence > 0),
    PRIMARY KEY(run_id, lease_epoch)
) WITHOUT ROWID;
CREATE TABLE native_sessions (
    run_id TEXT PRIMARY KEY, provider TEXT NOT NULL,
    native_session_id TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE conversation_sessions (
    conversation_id TEXT NOT NULL, provider TEXT NOT NULL,
    native_session_id TEXT NOT NULL, updated_at TEXT NOT NULL,
    PRIMARY KEY(conversation_id, provider)
);
CREATE TABLE run_git_snapshots (
    run_id TEXT NOT NULL, phase TEXT NOT NULL CHECK(phase IN ('start','end')),
    repository_id TEXT NOT NULL, captured_at_ns INTEGER NOT NULL, content_hash TEXT NOT NULL,
    uncompressed_bytes INTEGER NOT NULL CHECK(uncompressed_bytes >= 0),
    compressed_bytes INTEGER NOT NULL CHECK(compressed_bytes >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK(truncated IN (0,1)), payload BLOB NOT NULL,
    PRIMARY KEY(run_id, phase, repository_id)
);
CREATE INDEX event_log_unacked_sequence ON event_log(acked_at, run_id, lease_epoch, sequence);
CREATE INDEX run_git_snapshots_retention ON run_git_snapshots(captured_at_ns DESC, run_id);
CREATE INDEX inbox_commands_retention ON inbox_commands(finished_at DESC, message_id DESC) WHERE status IN ('completed','failed');
CREATE INDEX native_sessions_retention ON native_sessions(updated_at DESC, run_id DESC);
CREATE INDEX conversation_sessions_retention ON conversation_sessions(updated_at DESC, conversation_id DESC, provider DESC);
CREATE TRIGGER event_outbox_after_insert AFTER INSERT ON event_log WHEN NEW.acked_at IS NULL BEGIN
    UPDATE event_outbox_state SET unacked_count=unacked_count+1, unacked_bytes=unacked_bytes+NEW.wire_bytes WHERE singleton=1;
END;
CREATE TRIGGER event_outbox_after_delete AFTER DELETE ON event_log WHEN OLD.acked_at IS NULL BEGIN
    UPDATE event_outbox_state SET unacked_count=unacked_count-1, unacked_bytes=unacked_bytes-OLD.wire_bytes WHERE singleton=1;
END;
CREATE TRIGGER event_outbox_after_update AFTER UPDATE OF acked_at, wire_bytes ON event_log BEGIN
    UPDATE event_outbox_state SET unacked_count=unacked_count
        +(CASE WHEN NEW.acked_at IS NULL THEN 1 ELSE 0 END)-(CASE WHEN OLD.acked_at IS NULL THEN 1 ELSE 0 END),
        unacked_bytes=unacked_bytes
        +(CASE WHEN NEW.acked_at IS NULL THEN NEW.wire_bytes ELSE 0 END)-(CASE WHEN OLD.acked_at IS NULL THEN OLD.wire_bytes ELSE 0 END)
    WHERE singleton=1;
END;
