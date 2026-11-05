import { describe, expect, it } from "vitest";
import { providerDefinition, providerDefinitions, viewsForPayload } from "./providers";
import { projectRunActivity } from "./runActivityProjection";
import type { RunEvent } from "./types";

const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
  id: `event-${seq}`, run_id: "future-run", seq, event_type, payload, occurred_at: "2026-09-14T00:00:00Z"
});
describe("Provider source discovery and semantic events", () => {
  it("discovers manifests without a frontend provider list", () => {
    expect(providerDefinition("codex")?.display_name).toBe("Codex");
    expect(providerDefinition("opencode")?.display_name).toBe("OpenCode");
    expect(new Set(providerDefinitions.map((item) => item.id)).size).toBe(providerDefinitions.length);
    expect(viewsForPayload({ provider_id: "futurecli" })).toHaveLength(0);
  });
  it("renders a future CLI using standard events without a native view module", () => {
    const result = projectRunActivity([
      event(1, "output.delta", { provider_id: "futurecli", message_id: "m1", text: "hello " }),
      event(2, "reasoning.snapshot", { provider_id: "futurecli", id: "r1", text: "checking", status: "running" }),
      event(3, "provider.tool", { provider_id: "futurecli", id: "t1", tool: "bash", input: { command: "test" }, status: "running" }),
      event(4, "output.delta", { provider_id: "futurecli", message_id: "m1", text: "world" }),
      event(5, "output.snapshot", { provider_id: "futurecli", id: "m1", text: "hello world", final: true })
    ]);
    expect(result).toHaveLength(3);
    expect(result[0]).toMatchObject({ seq: 1, event_type: "output.snapshot", payload: { text: "hello world" } });
    expect(result[1]).toMatchObject({ event_type: "reasoning.snapshot", payload: { text: "checking" } });
    expect(result[2]).toMatchObject({ event_type: "provider.tool", payload: { tool: "bash" } });
  });
  it("does not interpret another provider's native payload using a built-in codec", () => {
    const foreign = event(1, "provider.item", { provider_id: "futurecli", item: { type: "agentMessage", text: "foreign" } });
    expect(projectRunActivity([foreign])).toEqual([foreign]);
  });
  it("rejects provider output without identity instead of guessing its format", () => {
    expect(projectRunActivity([
      event(1, "output.delta", { part_id: "old", text: "not an identified answer" }),
      event(2, "provider.item", { item: { type: "agentMessage", text: "untagged" } })
    ])).toEqual([]);
  });
  it("keeps compaction separate from reply text and marks tool failure by phase", () => {
    const result = projectRunActivity([
      event(1, "context.compaction", { provider_id: "opencode", id: "summary", status: "running" }),
      event(2, "provider.event", {
        provider_id: "opencode",
        kind: "tool",
        phase: "failed",
        call_id: "call_1",
        name: "bash",
        input: { command: "exit 3" },
        error: "timed out"
      })
    ]);
    expect(result.some((e) => ["output.snapshot", "reasoning.snapshot"].includes(e.event_type))).toBe(false);
    expect(result.find((e) => e.event_type === "provider.tool")).toMatchObject({
      payload: { status: "failed", tool: "bash", error: "timed out" }
    });
    expect(result.find((e) => e.event_type === "context.compaction")).toBeDefined();
  });
});
