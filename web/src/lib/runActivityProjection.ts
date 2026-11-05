import { MAX_ACTIVITY_EVENT_BYTES } from "./activityEvents";
import { providerViews, viewsForPayload } from "./providers";
import type { RunEvent } from "./types";

const text = (value: unknown) => typeof value === "string" ? value : "";

/** Provider-owned item materializers preserve first-seen identity and ordering.
 * The core only understands standard events, byte limits and deduplication.
 */
export function materializeRunActivity(input: readonly RunEvent[]): RunEvent[] {
  const rows = new Map<string, RunEvent>();
  const pass: RunEvent[] = [];
  const events = [...input].sort((a, b) => a.run_id.localeCompare(b.run_id) || a.seq - b.seq);
  const seen = new Set<string>();
  const key = (event: RunEvent, kind: string, id: string) => `${event.run_id}\0${kind}\0${id}`;
  const limited = (value: string) => value.length > MAX_ACTIVITY_EVENT_BYTES ? value.slice(0, MAX_ACTIVITY_EVENT_BYTES) + "…" : value;
  for (const event of events) {
    const eventKey = key(event, "seq", String(event.seq));
    if (seen.has(eventKey)) continue;
    seen.add(eventKey);
    if (viewsForPayload(event.payload).some((view) => view.materialize(event, { rows, key, limited }))) continue;
    const p = event.payload;
    if (event.event_type === "output.delta" && text(p.message_id)) {
      const id = key(event, "message", text(p.message_id));
      const previous = rows.get(id);
      rows.set(id, { ...(previous ?? event), event_type: "output.snapshot", payload: {
        ...previous?.payload, provider_id: p.provider_id, id: p.message_id, text: limited(text(previous?.payload.text) + text(p.text))
      } });
      continue;
    }
    if (["output.snapshot", "reasoning.snapshot", "files.changed", "provider.tool", "context.compaction"].includes(event.event_type)) {
      const callID = text(p.id) || text(p.call_id) || text(p.request_id);
      if (callID) {
        const tool = event.event_type === "provider.tool";
        const id = key(event, tool ? "tool" : event.event_type === "output.snapshot" ? "message" : event.event_type,
          tool ? `${text(p.provider)}:${text(p.tool)}:${callID}` : callID);
        const previous = rows.get(id);
        rows.set(id, { ...event, id: previous?.id ?? event.id, seq: previous?.seq ?? event.seq,
          occurred_at: previous?.occurred_at ?? event.occurred_at,
          payload: { ...previous?.payload, ...p, id: tool ? `${text(p.tool)}:${callID}` : callID } });
        continue;
      }
    }
    pass.push(event);
  }
  return [...pass, ...rows.values()].sort((a, b) => a.run_id.localeCompare(b.run_id) || a.seq - b.seq);
}

/** Translate materialized native events to Provider API semantic view events. */
export function projectRunActivity(input: readonly RunEvent[]): RunEvent[] {
  const providerEvents = new Set(["output.delta", "output.snapshot", "reasoning.snapshot", "provider.item", "provider.event", "provider.tool", "context.compaction", "files.changed", "workspace.diff"]);
  const events = materializeRunActivity(input.filter((event) => event.payload.internal !== true &&
    (!providerEvents.has(event.event_type) ||
    typeof event.payload.provider_id === "string" && event.payload.provider_id.length > 0)));
  const projections = providerViews.map((view) => ({ id: view.providerId, project: view.projection(events) }));
  return events.flatMap((event) => {
    for (const { id, project } of projections) {
      if (event.payload.provider_id !== id) continue;
      const result = project(event);
      if (result !== undefined) return result ? [result] : [];
    }
    return [event];
  });
}
