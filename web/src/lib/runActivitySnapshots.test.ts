import { describe, expect, it } from "vitest";
import { BoundedRunEventBuffer, projectActivityRunEvent } from "./activityEvents";
import { projectRunActivity } from "./runActivityProjection";
import type { RunEvent } from "./types";

const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
  id: `event-${seq}`, run_id: "run", seq, event_type, payload: { provider_id: "futurecli", ...payload },
  occurred_at: "2026-10-24T00:00:00Z"
});
const project = (events: RunEvent[]) => {
  const buffer = new BoundedRunEventBuffer();
  for (const item of events) buffer.upsert(projectActivityRunEvent(item)!);
  return projectRunActivity(buffer.values());
};

describe("cumulative activity snapshots", () => {
  it("replaces 16/32 KiB snapshots by message identity, including corrections that are not prefixes", () => {
    const text = "abcdefgh ".repeat(4000);
    const events = [
      event(1, "output.snapshot", { id: "message", text: text.slice(0, 16 * 1024) }),
      event(2, "output.snapshot", { id: "message", text: text.slice(0, 32 * 1024) }),
      event(3, "output.snapshot", { id: "message", text: "corrected " + text, final: true })
    ];
    for (let count = 1; count <= events.length; count++) {
      const rows = project(events.slice(0, count));
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ id: "event-1", seq: 1, payload: { text: events[count - 1].payload.text } });
    }
  });

  it("reconciles interleaved deltas and snapshots without merging messages, tools, reasoning or Runs", () => {
    const events = [
      event(1, "output.delta", { message_id: "a", text: "prefix" }),
      event(2, "output.delta", { message_id: "b", text: "prefix" }),
      event(3, "reasoning.snapshot", { id: "a", text: "thinking", status: "running" }),
      event(4, "provider.tool", { id: "a", tool: "bash", output: "stdout", status: "completed" }),
      event(5, "output.snapshot", { id: "a", text: "corrected" }),
      event(6, "output.delta", { message_id: "a", text: " tail" }),
      event(7, "output.snapshot", { id: "b", text: "prefix separate", final: true }),
      event(8, "reasoning.snapshot", { id: "a", text: "thought complete", status: "completed" }),
      { ...event(1, "output.snapshot", { id: "a", text: "other run" }), id: "other-run-event", run_id: "other-run" }
    ];
    const rows = project([...events, events[4]]);
    expect(rows).toHaveLength(5);
    expect(rows.filter((row) => row.run_id === "run").map((row) => [row.seq, row.event_type, row.payload.text ?? row.payload.output])).toEqual([
      [1, "output.snapshot", "corrected tail"],
      [2, "output.snapshot", "prefix separate"],
      [3, "reasoning.snapshot", "thought complete"],
      [4, "provider.tool", "stdout"]
    ]);
    expect(rows.find((row) => row.run_id === "other-run")?.payload.text).toBe("other run");
  });

  it.each([22425, 32 * 1024 + 1])("does not overwrite %i accumulated native characters with a 16 KiB final preview", (length) => {
    const text = "a".repeat(length - 4) + "LAST";
    const events: RunEvent[] = [];
    for (let start = 0; start < text.length; start += 512) {
      events.push(event(events.length + 1, "output.delta", {
        provider_id: "opencode", part_id: "text:message:0", message_id: "message", text: text.slice(start, start + 512)
      }));
    }
    const streaming = project(events);
    expect(streaming).toHaveLength(1);
    expect((streaming[0].payload.text as string).length).toBe(length);
    events.push(event(events.length + 1, "provider.event", {
      provider_id: "opencode", kind: "text", part_id: "text:message:0", message_id: "message", text
    }));
    const final = project(events);
    expect(final).toHaveLength(1);
    expect((final[0].payload.text as string).length).toBe(length);
    expect(final[0]).toMatchObject({ id: "event-1", seq: 1, payload: { text } });
  });

  it("keeps native text parts separate even within one message and applies authoritative corrections", () => {
    const events = [
      event(1, "output.delta", { provider_id: "opencode", part_id: "text:message:0", message_id: "message", text: "part zero" }),
      event(2, "output.delta", { provider_id: "opencode", part_id: "text:message:1", message_id: "message", text: "part one" }),
      event(3, "provider.event", { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:message:0", message_id: "message", delta: "thinking" }),
      event(4, "provider.event", { provider_id: "opencode", kind: "tool", call_id: "text:message:0", name: "bash", phase: "called", input: { command: "test" } }),
      event(5, "provider.event", { provider_id: "opencode", kind: "text", part_id: "text:message:0", message_id: "message", text: "authoritative correction" }),
      event(6, "output.delta", { provider_id: "opencode", part_id: "text:message:0", message_id: "message", text: " then tail" }),
      event(7, "provider.event", { provider_id: "opencode", kind: "text", part_id: "text:message:1", message_id: "message", text: "part one final" }),
      event(8, "provider.event", { provider_id: "opencode", kind: "tool", call_id: "text:message:0", name: "bash", phase: "success", content: [{ type: "text", text: "stdout" }], metadata: { exit: 0 } })
    ];
    const rows = project(events);
    expect(rows.map((row) => [row.seq, row.event_type, row.payload.text ?? row.payload.output])).toEqual([
      [1, "output.snapshot", "authoritative correction then tail"],
      [2, "output.snapshot", "part one final"],
      [3, "reasoning.snapshot", "thinking"],
      [4, "provider.tool", "stdout"]
    ]);
  });
});
