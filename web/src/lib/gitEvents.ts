import { apiBaseURL } from "./api";
import type { GitRepositoryEvent } from "./gitChanges";

export type GitEventConnectionState =
  | { kind: "connecting" }
  | { kind: "connected" }
  | { kind: "retrying"; delaySeconds: number; attempt: number }
  | { kind: "unsupported" };

export function subscribeGitRepositoryEvents(options: {
  projectId: string;
  onEvent: (event: GitRepositoryEvent) => void;
  onState: (state: GitEventConnectionState) => void;
}): () => void {
  if (typeof EventSource === "undefined") {
    options.onState({ kind: "unsupported" });
    return () => {};
  }

  let source: EventSource | undefined;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let attempt = 0;

  const parse = (raw: Event) => {
    if (stopped) return;
    const text = (raw as MessageEvent<string>).data;
    if (!text) return;
    try {
      const event = JSON.parse(text) as GitRepositoryEvent;
      if (!event.repository_id || !Number.isFinite(event.generation) || typeof event.reason !== "string") return;
      options.onEvent(event);
    } catch {
      // A malformed notification must not tear down repository reconciliation.
    }
  };

  const connect = () => {
    if (stopped) return;
    options.onState({ kind: "connecting" });
    source = new EventSource(
      `${apiBaseURL}/projects/${encodeURIComponent(options.projectId)}/git/events`,
      { withCredentials: true }
    );
    source.addEventListener("open", () => {
      if (stopped) return;
      attempt = 0;
      options.onState({ kind: "connected" });
    });
    // The current HTTP contract emits only git.changed, with a direct payload.
    source.addEventListener("git.changed", parse);
    source.addEventListener("error", () => {
      source?.close();
      source = undefined;
      if (stopped || retryTimer) return;
      attempt += 1;
      const delaySeconds = Math.min(30, 2 ** Math.min(5, attempt - 1));
      options.onState({ kind: "retrying", delaySeconds, attempt });
      retryTimer = setTimeout(() => {
        retryTimer = undefined;
        connect();
      }, delaySeconds * 1_000);
    });
  };

  connect();
  return () => {
    stopped = true;
    source?.close();
    if (retryTimer) clearTimeout(retryTimer);
  };
}
