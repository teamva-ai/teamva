import type { ProjectSearchResult } from "./types";

export function searchResultTarget(item: ProjectSearchResult): string {
  const params = new URLSearchParams();
  if (item.kind === "memory" && item.memory_id) {
    params.set("memory", item.memory_id);
    return `/memories?${params}`;
  }
  if (!item.issue_id) return "/workbench";
  params.set("issue", item.issue_id);
  if (item.kind === "issue_description") params.set("focus", "description");
  if (item.kind === "issue_comment" && item.comment_id) params.set("comment", item.comment_id);
  if (item.kind === "session_message" && item.issue_agent_instance_id) {
    params.set("session", item.issue_agent_instance_id);
    if (item.run_id) params.set("run", item.run_id);
    if (item.anchor) params.set("entry", item.anchor);
  }
  return `/issues?${params}`;
}
