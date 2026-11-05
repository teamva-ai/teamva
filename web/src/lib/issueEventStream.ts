import { apiBaseURL } from "./api";
import { projectActivityRunEvent } from "./activityEvents";
import { streamedRunEventTypes } from "./runEventStream";
import type { RunEvent } from "./types";

export function subscribeIssueEvents(options: {
  issueId: string;
  onEvent: (event: RunEvent) => void;
  onIssueChanged?: () => void;
}): () => void {
  if (typeof EventSource === "undefined") return () => {};
  const source = new EventSource(
    `${apiBaseURL}/issues/${encodeURIComponent(options.issueId)}/events`,
    { withCredentials: true }
  );
  const listener = (raw: Event) => {
    const data = (raw as MessageEvent<string>).data;
    if (!data) return;
    try {
      const event = projectActivityRunEvent(JSON.parse(data));
      if (event) options.onEvent(event);
    } catch {
      // A malformed event must not stop later task-level invalidations.
    }
  };
  for (const type of streamedRunEventTypes) source.addEventListener(type, listener);
  source.addEventListener("issue.changed", () => options.onIssueChanged?.());
  return () => source.close();
}
