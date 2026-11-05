CREATE TABLE computer_skill_packages (
    id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
    computer_id TEXT NOT NULL,
    skill_package_version_id TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    deleted_at TEXT,
    UNIQUE (computer_id, skill_package_version_id),
    FOREIGN KEY (computer_id) REFERENCES computers(id),
    FOREIGN KEY (skill_package_version_id) REFERENCES skill_package_versions(id)
) STRICT;

CREATE INDEX computer_skill_packages_computer_active
    ON computer_skill_packages(computer_id, created_at, id)
    WHERE deleted_at IS NULL;
