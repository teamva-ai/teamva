import { describe, expect, it } from "vitest";
import { searchResultTarget } from "./searchNavigation";
import type { ProjectSearchResult } from "./types";

const base: ProjectSearchResult = {
  id: "result", kind: "memory", project_id: "project", title: "Title",
  excerpt: "match", occurred_at: "2026-08-13T00:00:00Z"
};

describe("search result navigation", () => {
  it("targets a specific memory", () => {
    expect(searchResultTarget({ ...base, memory_id: "memory-1" })).toBe("/memories?memory=memory-1");
  });

  it("targets the issue description and comment precisely", () => {
    expect(searchResultTarget({ ...base, kind: "issue_description", issue_id: "issue-1" }))
      .toBe("/issues?issue=issue-1&focus=description");
    expect(searchResultTarget({ ...base, kind: "issue_comment", issue_id: "issue-1", comment_id: "comment-1" }))
      .toBe("/issues?issue=issue-1&comment=comment-1");
  });

  it("opens the matching Agent session and run", () => {
    expect(searchResultTarget({
      ...base, kind: "session_message", issue_id: "issue-1",
      issue_agent_instance_id: "session-1", run_id: "run-1", role: "assistant", anchor: "assistant"
    })).toBe("/issues?issue=issue-1&session=session-1&run=run-1&entry=assistant");
  });

  it("falls back safely when a result has no navigable owner", () => {
    expect(searchResultTarget({ ...base, kind: "session_message" })).toBe("/workbench");
  });
});
