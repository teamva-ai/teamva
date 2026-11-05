import { describe, expect, it } from "vitest";
import { MAX_ACTIVITY_EVENT_BYTES, projectActivityRunEvent } from "./activityEvents";
import { projectRunActivity } from "./runActivityProjection";
import type { RunEvent } from "./types";

const event = (event_type: string, payload: Record<string, unknown>): RunEvent => ({
  id: "event-1", run_id: "run", seq: 1, event_type, payload,
  occurred_at: "2026-10-24T00:00:00Z"
});
const longText = (length: number) => "0123456789 abcdefghijklmnopqrstuvwxyz ".repeat(Math.ceil(length / 37)).slice(0, length);

describe("assistant text at the bounded activity ingress", () => {
  it.each([16 * 1024 + 1, 22425, 32 * 1024 + 1])("retains a %i character snapshot within the existing event budget", (length) => {
    const text = longText(length);
    const sources = [
      event("output.snapshot", { provider_id: "futurecli", id: "message", text }),
      event("provider.event", { provider_id: "opencode", kind: "text", part_id: "text:message:0", message_id: "message", text }),
      event("provider.item", { provider_id: "codex", item: { id: "message", type: "agentMessage", text } }),
      event("provider.item", { provider_id: "codex", item: { id: "message", type: "agentMessage", content: [{ type: "text", text }] } })
    ];
    for (const source of sources) {
      const projected = projectActivityRunEvent(source)!;
      const reply = projectRunActivity([projected])[0];
      expect((reply.payload.text as string).length).toBe(length);
      expect(reply.payload.text).toBe(text);
      expect(projectActivityRunEvent(projected)).toEqual(projected);
      expect(JSON.stringify(projected)).not.toContain('"content":');
    }
  });

  it("keeps the per-event budget, identities and recursive exclusions for oversized assistant text", () => {
    const projected = projectActivityRunEvent(event("provider.event", {
      text: "x".repeat(20 * 1024 * 1024),
      provider_id: "opencode", kind: "text", part_id: "text:message:0", message_id: "message",
      input: { command: "retained", content: "NOT-RETAINED", raw: "NOT-RETAINED" },
      raw: "NOT-RETAINED", data: "NOT-RETAINED"
    }))!;
    const reply = projectRunActivity([projected])[0];
    const text = reply.payload.text as string;
    expect(text.length).toBeGreaterThan(32 * 1024);
    expect(text.length).toBeLessThanOrEqual(MAX_ACTIVITY_EVENT_BYTES);
    expect(text.endsWith("…")).toBe(true);
    expect(projected.payload).toMatchObject({ provider_id: "opencode", part_id: "text:message:0", message_id: "message" });
    expect(JSON.stringify(projected).length).toBeLessThan(MAX_ACTIVITY_EVENT_BYTES + 1024);
    expect(JSON.stringify(projected)).not.toContain("NOT-RETAINED");
  });

  it("does not enlarge arbitrary metadata, tool output or reasoning scalar limits", () => {
    const text = longText(32 * 1024);
    const assistant = projectActivityRunEvent(event("output.snapshot", {
      provider_id: "futurecli", id: "message", text, metadata: { description: text }
    }))!;
    expect((assistant.payload.text as string).length).toBe(text.length);
    expect(((assistant.payload.metadata as Record<string, unknown>).description as string).length).toBe(16 * 1024);
    for (const source of [
      event("reasoning.snapshot", { provider_id: "futurecli", id: "reasoning", text }),
      event("provider.event", { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:message:0", text }),
      event("provider.tool", { provider_id: "futurecli", id: "tool", tool: "bash", output: text }),
      event("provider.event", { provider_id: "opencode", kind: "tool", call_id: "tool", name: "bash", content: [{ type: "text", text }] })
    ]) {
      const projected = projectRunActivity([projectActivityRunEvent(source)!])[0];
      expect(((projected.payload.text ?? projected.payload.output) as string).length).toBeLessThanOrEqual(16 * 1024);
    }
  });
});
