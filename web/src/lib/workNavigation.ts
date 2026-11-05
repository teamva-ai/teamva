import { api, APIError } from "./api";
import { v4 } from "uuid";
import { workClientHeaders } from "./workRecords";

export interface WorkLocation {
  project_id?: string;
  issue_id?: string;
  graph_id?: string;
  node_id?: string;
}
type Observation = {
  id: string;
  sequence: number;
  occurred_at: string;
  from: WorkLocation;
  to: WorkLocation;
  headers?: Record<string, string>;
};
let previous: WorkLocation = {};
let sequence = 0;
let queue: Observation[] = [];
let flushing = false;
let retry: ReturnType<typeof setTimeout> | undefined;
const key = "teamva-work-navigation";
try {
  const saved = JSON.parse(sessionStorage.getItem(key) ?? "[]");
  if (Array.isArray(saved))
    queue = saved
      .filter(
        (v) => v && typeof v.id === "string" && typeof v.sequence === "number" && v.from && v.to
      )
      .slice(-500);
  sequence = Math.max(0, ...queue.map((v) => v.sequence));
} catch {
  /* private storage or invalid local data */
}
function persist() {
  try {
    sessionStorage.setItem(key, JSON.stringify(queue));
  } catch {
    /* server remains authoritative */
  }
}
export function observeWorkProject(projectID: string) {
  // Leaving a task for Settings or Work history is only a page change, not a
  // new project selection. Keep the last actual object as the next source.
  if (previous.project_id !== projectID) observeWorkLocation({ project_id: projectID });
}
export function observeWorkLocation(next: WorkLocation) {
  const normalized: WorkLocation = {};
  for (const field of ["project_id", "issue_id", "graph_id", "node_id"] as const)
    if (next[field]) normalized[field] = next[field];
  if (!normalized.project_id || JSON.stringify(previous) === JSON.stringify(normalized)) return;
  queue.push({
    id: v4(),
    sequence: ++sequence,
    occurred_at: new Date().toISOString(),
    from: previous,
    to: normalized,
    headers: workClientHeaders()
  });
  previous = normalized;
  persist();
  void flushWorkNavigation();
}
export async function flushWorkNavigation() {
  if (flushing) return;
  if (retry) {
    clearTimeout(retry);
    retry = undefined;
  }
  flushing = true;
  try {
    while (queue.length) {
      try {
        const { headers, ...body } = queue[0];
        await api("/work-records/navigation", { body, headers });
      } catch (error) {
        if (
          !(error instanceof APIError) ||
          error.status >= 500 ||
          error.status === 401 ||
          error.status === 403
        )
          throw error;
        // Deleted targets and malformed observations cannot block later ones.
      }
      queue.shift();
      persist();
    }
  } catch {
    retry = setTimeout(() => {
      void flushWorkNavigation();
    }, 5000);
  } finally {
    flushing = false;
  }
}
window.addEventListener("online", () => {
  void flushWorkNavigation();
});
