import { describe, expect, it } from "vitest";
import { BoundedRunEventBuffer, MAX_ACTIVITY_EVENT_BYTES, projectActivityRunEvent } from "./activityEvents";
import { materializeRunActivity, projectRunActivity } from "./runActivityProjection";
import { settledActivityStatus } from "./compaction";
import type { RunEvent } from "./types";

const event = (seq: number, item: Record<string, unknown>, payload = {}): RunEvent => ({
  id: `native-${seq}`, run_id: "codex-run", seq, event_type: "provider.item",
  occurred_at: "2026-10-23T00:00:00Z", payload: { provider_id: "codex", item, ...payload }
});
const item = (status = "inProgress") => ({ id: "mcp-call", type: "mcpToolCall", server: "amqa-local", tool: "qa_echo",
  arguments: { text: "AMQA_GOAL_MCP_CODEX_20261023" }, status, result: null, error: null, durationMs: null });
const ingress = (event: RunEvent) => projectActivityRunEvent(event)!;
const progress = (seq: number, message: string): RunEvent => ({ ...event(seq, {}), event_type: "provider.event",
  payload: { provider_id: "codex", method: "item/mcpToolCall/progress", threadId: "thread", turnId: "turn", itemId: "mcp-call", message } });

describe("Codex native MCP activity", () => {
  it("keeps a visible stable tool row through native progress and a successful final snapshot", () => {
    const start = ingress(event(1, item(), { startedAtMs: 100 }));
    const first = ingress(progress(2, "waiting")), second = ingress(progress(3, "working"));
    expect(projectRunActivity([start])[0]).toMatchObject({ event_type: "provider.tool", payload: {
      id: "mcp-call", provider: "mcp", server: "amqa-local", tool: "amqa-local/qa_echo", status: "inProgress", input: { text: "AMQA_GOAL_MCP_CODEX_20261023" }
    } });
    const running = projectRunActivity([start, first, second]);
    expect(running).toHaveLength(1);
    // MCP progress is a message snapshot, not stdout/result deltas to concatenate.
    expect(running[0]).toMatchObject({ id: "native-1", seq: 1, payload: { status: "inProgress", output: { progress: "working" } } });
    const final = ingress(event(4, { ...item("completed"), durationMs: 321, result: {
      content: [{ type: "text", text: "native receipt" }, { type: "image", data: "BINARY_SENTINEL", mimeType: "image/png" }],
      structuredContent: { marker: "receipt", count: 1 }
    } }, { completedAtMs: 421 }));
    const rows = projectRunActivity([final, second, start, first, final]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: "native-1", seq: 1, payload: {
      id: "mcp-call", status: "completed", duration_ms: 321,
      output: { text: "native receipt", structuredContent: { marker: "receipt", count: 1 } }, error: null
    } });
    expect(rows[0].payload).not.toHaveProperty("exit_code");
    expect(JSON.stringify(final)).not.toContain("BINARY_SENTINEL");
    expect(JSON.stringify(final.payload)).not.toContain('"content"');
  });

  it("shows the actual failed MCP shape instead of dropping it or fabricating a shell exit", () => {
    const failure = ingress(event(3, { ...item("failed"), durationMs: 15,
      error: { message: "user rejected MCP tool call" } }, { completedAtMs: 115 }));
    const rows = projectRunActivity([ingress(event(1, item())), ingress(progress(2, "awaiting review")), failure]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ event_type: "provider.tool", payload: { tool: "amqa-local/qa_echo", status: "failed",
      input: { text: "AMQA_GOAL_MCP_CODEX_20261023" }, duration_ms: 15, error: { message: "user rejected MCP tool call" } } });
    expect(rows[0].payload.output).toBeUndefined();
    expect(rows[0].payload).not.toHaveProperty("exit_code");
    expect(projectRunActivity([failure])[0].payload).toEqual(rows[0].payload);
  });

  it.each(["inProgress", "completed", "failed", "interrupted"])("preserves native %s status and disclosed duration on refresh", status => {
    const snapshot = ingress(event(10, { ...item(status), durationMs: 0 }, { completedAtMs: 200 }));
    const live = projectRunActivity([snapshot])[0];
    const refreshed = projectRunActivity([ingress(JSON.parse(JSON.stringify(snapshot)))])[0];
    expect(refreshed.payload).toEqual(live.payload);
    expect(refreshed.payload.status).toBe(status);
    expect(refreshed.payload.duration_ms).toBe(0);
    expect(refreshed.payload).not.toHaveProperty("exit_code");
  });

  it("does not claim completion when an MCP item closes without a disclosed terminal status", () => {
    const closing = event(2, { id: "mcp-call", type: "mcpToolCall" }, { completedAtMs: 200 });
    const row = projectRunActivity([ingress(event(1, item())), ingress(closing)])[0];
    expect(row.payload.status).toBe("inProgress");
    expect(settledActivityStatus(String(row.payload.status), false)).toBe("interrupted");
  });

  it("retains bounded result/error after ingress, eviction, JSON restore and repeated materialization", () => {
    const resultText = "receipt:" + "x".repeat(200_000), message = "failure:" + "y".repeat(200_000);
    const final = ingress(event(4, { ...item("failed"), durationMs: 500, result: {
      content: [{ type: "text", text: resultText }, { type: "resource", resource: { text: "RESOURCE_NOT_DISPLAY_TEXT", blob: "BINARY_SENTINEL" } }],
      structuredContent: { receipt: true, data: "BLOCKED_DATA" }
    }, error: { message } }));
    const buffer = new BoundedRunEventBuffer(1, MAX_ACTIVITY_EVENT_BYTES * 2);
    buffer.upsert(ingress(event(1, item())));
    buffer.upsert(ingress(progress(2, "waiting")));
    buffer.upsert(final);
    expect(buffer.incomplete).toBe(true);
    const restored = JSON.parse(JSON.stringify(buffer.values())).map(ingress);
    const first = projectRunActivity(restored), again = projectRunActivity(materializeRunActivity(restored));
    expect(first[0].payload).toEqual(again[0].payload);
    expect(first[0].payload).toMatchObject({ status: "failed", duration_ms: 500, output: { structuredContent: { receipt: true } } });
    const output = first[0].payload.output as { text: string };
    expect(output.text.startsWith("receipt:")).toBe(true);
    expect(output.text.length).toBeLessThanOrEqual(16 * 1024);
    expect((first[0].payload.error as { message: string }).message.startsWith("failure:")).toBe(true);
    expect((first[0].payload.error as { message: string }).message.length).toBeLessThanOrEqual(16 * 1024);
    const retained = JSON.stringify(restored);
    for (const sentinel of ["BINARY_SENTINEL", "BLOCKED_DATA", "RESOURCE_NOT_DISPLAY_TEXT"]) expect(retained).not.toContain(sentinel);
    expect(retained.length).toBeLessThan(MAX_ACTIVITY_EVENT_BYTES);
  });

  it("keeps native calls with the same item id separated across Runs", () => {
    const first = ingress(event(1, item()));
    expect(projectRunActivity([first, { ...first, id: "other-native", run_id: "other-run" }])).toHaveLength(2);
  });
});
