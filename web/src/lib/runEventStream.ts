import { apiBaseURL } from "./api";
import { projectActivityRunEvent } from "./activityEvents";
import type { RunEvent } from "./types";
import { isRunTerminalEvent } from "./runDiagnostics";

export const streamedRunEventTypes = [
  "run.queued",
  "run.started",
  "run.completed",
  "run.finished",
  "run.error",
  "run.cancelling",
  "output.delta",
  "usage.updated",
  "provider.item",
  "output.snapshot",
  "reasoning.snapshot",
  "files.changed",
  "provider.event",
  "provider.tool",
  "provider.internal",
  "approval.requested",
  "workspace.diff",
  "user.steer",
  "context.compaction",
  "node.diagnostic"
] as const;

export function subscribeRunEvents(options: {
  runId: string;
  afterSeq?: number;
  onEvent: (event: RunEvent) => void;
  onTerminal?: (event: RunEvent) => void;
  onGap?: (afterSequence: number, incomingSequence: number) => void;
  onState?: (state: "connecting" | "connected" | "retrying") => void;
}): () => void {
  if (typeof EventSource === "undefined") return () => {};
  let source: EventSource | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let reconnectAttempt = 0;
  let lastSequence = Math.max(0, options.afterSeq ?? 0);

  const connect = () => {
    if (stopped) return;
    options.onState?.("connecting");
    const query = new URLSearchParams({ after_seq: String(lastSequence) });
    const current = new EventSource(
      `${apiBaseURL}/runs/${encodeURIComponent(options.runId)}/events/stream?${query}`,
      { withCredentials: true }
    );
    source = current;

    const listener = (raw: Event) => {
      if (stopped || source !== current) return;
      const data = (raw as MessageEvent<string>).data;
      if (!data) return;
      try {
        // The parsed source is kept only for this expression. Projection copies
        // bounded activity fields and discards patch/content before callbacks.
        const event = projectActivityRunEvent(JSON.parse(data), options.runId);
        if (!event || event.seq <= lastSequence) return;
        if (event.seq !== lastSequence + 1) {
          options.onGap?.(lastSequence, event.seq);
          current.close();
          if (source === current) source = undefined;
          reconnectAttempt += 1;
          reconnectTimer = setTimeout(() => {
            reconnectTimer = undefined;
            connect();
          }, Math.min(5_000, 250 * 2 ** Math.min(4, reconnectAttempt - 1)));
          return;
        }
        lastSequence = event.seq;
        options.onEvent(event);
        if (isRunTerminalEvent(event)) {
          stopped = true;
          current.close();
          options.onTerminal?.(event);
        }
      } catch {
        // A malformed provider event must not tear down the remaining live stream.
      }
    };
    for (const type of streamedRunEventTypes) current.addEventListener(type, listener);
    current.addEventListener("open", () => {
      if (source !== current) return;
      reconnectAttempt = 0;
      options.onState?.("connected");
    });
    current.addEventListener("error", () => {
      if (stopped || source !== current || reconnectTimer) return;
      current.close();
      source = undefined;
      reconnectAttempt += 1;
      options.onState?.("retrying");
      reconnectTimer = setTimeout(() => {
        reconnectTimer = undefined;
        connect();
      }, Math.min(5_000, 250 * 2 ** Math.min(4, reconnectAttempt - 1)));
    });
  };

  connect();
  return () => {
    stopped = true;
    source?.close();
    if (reconnectTimer) clearTimeout(reconnectTimer);
  };
}
