import { describe, expect, it } from "vitest";
import { BoundedRunEventBuffer, MAX_ACTIVITY_EVENT_BYTES, projectActivityRunEvent } from "./activityEvents";
import { materializeRunActivity, projectRunActivity } from "./runActivityProjection";
import type { RunEvent } from "./types";

const event = (seq: number, item: Record<string, unknown>, method?: string): RunEvent => ({
  id: `search-event-${seq}`, run_id: "synthetic-search-run", seq, event_type: "provider.item",
  occurred_at: `2026-01-01T00:00:${String(seq).padStart(2, "0")}Z`,
  payload: { provider_id: "codex", item, ...(method ? { method } : {}) }
});
const start = (seq = 1, id = "search-1") => event(seq, {
  type: "webSearch", id, action: null, query: null, results: null
}, "item/started");
const complete = (seq = 2, id = "search-1") => event(seq, {
  type: "webSearch", id, action: { type: "search", queries: ["synthetic alpha", "synthetic beta"], query: null },
  query: null, results: [{ title: "Synthetic result", url: "https://example.invalid/result" }]
}, "item/completed");
const ingress = (value: RunEvent) => projectActivityRunEvent(value, "synthetic-search-run")!;

describe("Codex native web search activity", () => {
  it("shows the real empty started shape immediately, before any output or result", () => {
    const buffer = new BoundedRunEventBuffer();
    buffer.upsert(ingress(start()));
    const rows = projectRunActivity(buffer.values());
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: "search-event-1", seq: 1, event_type: "provider.tool", payload: {
      id: "search-1", tool: "websearch", status: "running", input: { action: null, query: null }
    } });
    expect(rows[0].payload).not.toHaveProperty("output");
    expect(rows[0].payload).not.toHaveProperty("exit_code");
  });

  it("updates the same row in first-seen order, with search input and no assistant output", () => {
    const buffer = new BoundedRunEventBuffer();
    for (const raw of [start(), start(2, "search-2"), complete(3), complete(4, "search-2")]) {
      buffer.upsert(ingress(raw));
      const rows = projectRunActivity(buffer.values());
      expect(rows.length).toBe(raw.seq === 1 ? 1 : 2);
      expect(rows.every((row) => row.event_type === "provider.tool" && row.payload.tool === "websearch")).toBe(true);
    }
    const rows = projectRunActivity(buffer.values());
    expect(rows.map((row) => row.payload.id)).toEqual(["search-1", "search-2"]);
    expect(rows[0]).toMatchObject({ id: "search-event-1", seq: 1, occurred_at: start().occurred_at,
      payload: { status: "completed", input: {
        action: { type: "search", queries: ["synthetic alpha", "synthetic beta"], query: null },
        query: null, summary: "synthetic alpha\nsynthetic beta"
      } }
    });
    expect(rows[1].payload.status).toBe("completed");
    const replayed = [...buffer.values(), ...buffer.values()].reverse();
    expect(projectRunActivity(replayed)).toEqual(rows);
    expect(projectRunActivity(materializeRunActivity(replayed))).toEqual(rows);
  });

  it("uses lifecycle notifications, never the presence of results, to settle a search", () => {
    const withResults = start();
    (withResults.payload.item as Record<string, unknown>).results = [{ title: "Early synthetic result" }];
    expect(projectRunActivity([ingress(withResults)])[0]?.payload.status).toBe("running");
    const withoutResults = complete();
    (withoutResults.payload.item as Record<string, unknown>).results = null;
    expect(projectRunActivity([ingress(start()), ingress(withoutResults)])[0]?.payload.status).toBe("completed");
    const historical = complete();
    delete historical.payload.method;
    expect(projectRunActivity([ingress(historical)])[0]?.payload.status).toBe("");
  });

  it("preserves explicit native status, exit and timing fields while materializing lifecycle updates", () => {
    const initial = start();
    Object.assign(initial.payload.item as Record<string, unknown>, {
      status: "inProgress", exitCode: 7, startedAt: 100, startedAtMs: 100, time: { start: 100 }
    });
    const closing = complete();
    Object.assign(closing.payload.item as Record<string, unknown>, {
      status: "failed", completedAt: 250, completedAtMs: 250, durationMs: 150
    });
    const materialized = materializeRunActivity([ingress(initial), ingress(closing)]);
    expect(materialized[0].payload.item).toMatchObject({ status: "failed", exitCode: 7,
      startedAt: 100, startedAtMs: 100, completedAt: 250, completedAtMs: 250, durationMs: 150, time: { start: 100 } });
    expect(projectRunActivity(materialized)[0]).toMatchObject({ event_type: "provider.tool",
      payload: { status: "failed", duration_ms: 150 } });
    expect(projectRunActivity([ingress(initial)])[0]?.payload.status).toBe("inProgress");
  });

  it("shows a disclosed single query without copying native results, headers or arbitrary action fields", () => {
    const raw = event(1, { id: "search-1", type: "webSearch", query: "synthetic legacy query",
      action: { type: "search", query: "synthetic action query", queries: ["synthetic alternate"],
        headers: { authorization: "SECRET_HEADER_SENTINEL" }, raw: "RAW_SENTINEL", binary: "BINARY_SENTINEL",
        unknown: { password: "UNKNOWN_SENTINEL" } },
      results: [{ content: [{ text: "RESULT_BODY_SENTINEL" }], secret: "RESULT_SECRET_SENTINEL" }] }, "item/completed");
    const row = projectRunActivity([ingress(raw)])[0];
    expect(row).toMatchObject({ event_type: "provider.tool", payload: { input: {
      query: "synthetic legacy query", action: { type: "search", query: "synthetic action query", queries: ["synthetic alternate"] }
    } } });
    const display = JSON.stringify(row);
    for (const sentinel of ["SECRET_HEADER_SENTINEL", "RAW_SENTINEL", "BINARY_SENTINEL", "UNKNOWN_SENTINEL", "RESULT_BODY_SENTINEL", "RESULT_SECRET_SENTINEL"]) {
      expect(display).not.toContain(sentinel);
    }
  });

  it("bounds public search input and its display summary within the activity event budget", () => {
    const raw = event(1, { id: "search-1", type: "webSearch", query: "q".repeat(16_000),
      action: { type: "search", queries: Array.from({ length: 4 }, () => "x".repeat(8_000)) } }, "item/completed");
    const projected = ingress(raw);
    expect(projected.payload.provider_id).toBe("codex");
    const rows = projectRunActivity([projected]);
    expect(rows).toHaveLength(1);
    expect(JSON.stringify(rows[0]).length).toBeLessThan(MAX_ACTIVITY_EVENT_BYTES);
    expect(String((rows[0].payload.input as Record<string, unknown>).summary).length).toBeLessThanOrEqual(16 * 1024);
  });

  it("retains ownership and existing internal filtering without inventing reasoning text", () => {
    const search = ingress(start());
    expect(search.payload.provider_id).toBe("codex");
    expect(projectRunActivity([search])).toHaveLength(1);
    expect(projectRunActivity([{ ...search, payload: { ...search.payload, internal: true } }])).toEqual([]);
    expect(projectRunActivity([{ ...search, payload: { ...search.payload, provider_id: undefined } }])).toEqual([]);
    const reasoning = ingress(event(2, { id: "reasoning-1", type: "reasoning", summary: [], content: [] }, "item/started"));
    const rows = projectRunActivity([search, reasoning]);
    expect(rows).toHaveLength(2);
    expect(rows.find((row) => row.event_type === "reasoning.snapshot")?.payload.text).toBe("");
    expect(rows.some((row) => row.event_type === "output.snapshot" || row.event_type === "output.delta")).toBe(false);
  });
});
