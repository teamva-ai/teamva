-- Latest-first metadata cursors for project search. No content/JSON is copied
-- into these indexes. Event windows reuse run_events_page_budget from events.sql.
CREATE INDEX project_search_memories_latest
    ON project_memory_entries(project_id, updated_at DESC, id)
    WHERE deleted_at IS NULL;

CREATE INDEX project_search_issues_latest
    ON issues(project_id, updated_at DESC, id)
    WHERE deleted_at IS NULL;

CREATE INDEX project_search_comments_latest
    ON issue_comments(updated_at DESC, id, issue_id)
    WHERE deleted_at IS NULL AND status = 'published';

CREATE INDEX project_search_runs_latest
    ON runs(project_id, updated_at DESC, id)
    WHERE deleted_at IS NULL;
