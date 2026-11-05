-- Inspirations are owned globally by the user. Project is an optional grouping
-- reference, never the ownership boundary. Markdown TEXT is the only body format.
CREATE TABLE inspirations (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
    title TEXT NOT NULL CHECK(length(trim(title)) BETWEEN 1 AND 200),
    markdown TEXT NOT NULL DEFAULT '' CHECK(length(CAST(markdown AS BLOB)) <= 262144),
    completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1)),
    completed_at TEXT,
    create_key TEXT NOT NULL,
    create_fingerprint TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    UNIQUE(user_id,create_key),
    CHECK((completed=0 AND completed_at IS NULL) OR (completed=1 AND completed_at IS NOT NULL))
) STRICT;
CREATE INDEX inspirations_owner ON inspirations(user_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX inspirations_group ON inspirations(user_id,project_id,completed,created_at DESC,id DESC) WHERE deleted_at IS NULL;
