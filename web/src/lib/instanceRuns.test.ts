import { afterEach, describe, expect, it, vi } from "vitest";
import {
  INSTANCE_RUN_PAGE_SIZE, MAX_RETAINED_INSTANCE_RUN_BYTES,
  fetchInstanceRunsPage, mergeInstanceRunPages
} from "./instanceRuns";
import type { Run } from "./types";

afterEach(() => vi.unstubAllGlobals());

function run(index: number): Run {
  return {
    id: `run-${index}`,
    version: 1,
    issue_id: "issue-1",
    issue_agent_instance_id: "instance-1",
    project_id: "project-1",
    status: "succeeded",
    read_only: false,
    lease_epoch: 1,
    purpose: "work",
    input: [],
    created_at: `2026-08-11T00:${String(index % 60).padStart(2, "0")}:00Z`,
    updated_at: "2026-08-11T01:00:00Z"
  };
}

describe("Agent instance Run pagination", () => {
  it("loads one bounded page and leaves older history behind the opaque cursor", async () => {
    const requestedCursors: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const cursor = url.searchParams.get("cursor") ?? "";
      requestedCursors.push(cursor);
      const items = Array.from({ length: INSTANCE_RUN_PAGE_SIZE }, (_, index) => run(index));
      const data = { items, next_cursor: "cursor-25" };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-runs" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    const page = await fetchInstanceRunsPage("instance-1");
    expect(requestedCursors).toEqual([""]);
    expect(page.items).toHaveLength(INSTANCE_RUN_PAGE_SIZE);
    expect(page.next_cursor).toBe("cursor-25");
  });

  it("merges an explicitly requested older page without duplicate boundaries", () => {
    const first = mergeInstanceRunPages(undefined, {
      items: Array.from({ length: 25 }, (_, index) => run(index)),
      next_cursor: "cursor-25"
    });
    const merged = mergeInstanceRunPages(first, {
      items: [run(24), ...Array.from({ length: 5 }, (_, index) => run(index + 25))]
    });
    expect(merged.items).toHaveLength(30);
    expect(new Set(merged.items.map((item) => item.id)).size).toBe(30);
  });

  it("clips retained Run input before the session exceeds its byte budget", () => {
    const huge = Array.from({ length: 100 }, (_, index) => ({
      ...run(index), input: [{ type: "text" as const, text: "x".repeat(128 * 1024) }]
    }));
    const merged = mergeInstanceRunPages(undefined, { items: huge, next_cursor: "older" });
    expect(merged.retained_bytes).toBeLessThanOrEqual(MAX_RETAINED_INSTANCE_RUN_BYTES);
    expect(merged.history_limited).toBe(true);
    expect(merged.next_cursor).toBeUndefined();
    expect(merged.items.every((item) => (item.input[0]?.text?.length ?? 0) <= 64 * 1024)).toBe(true);
    expect(merged.items.every((item) =>
      item.input.reduce((total, block) => total + (block.text?.length ?? 0), 0) <= 64 * 1024
    )).toBe(true);
  });

  it("compacts 25 large inputs before the fetched Query value can be cached", async () => {
    const rawInput = "start\n" + "x".repeat(256 * 1024) + "\nfinish";
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      data: {
        items: Array.from({ length: INSTANCE_RUN_PAGE_SIZE }, (_, index) => ({
          ...run(index), input: [{ type: "text" as const, text: rawInput }]
        })),
        next_cursor: "older"
      },
      meta: { request_id: "request-large-runs" }
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const page = await fetchInstanceRunsPage("instance-1");
    expect(page.items).toHaveLength(INSTANCE_RUN_PAGE_SIZE);
    expect(page.retained_bytes).toBeLessThanOrEqual(MAX_RETAINED_INSTANCE_RUN_BYTES);
    expect(page.content_limited).toBe(true);
    expect(page.history_limited).toBeUndefined();
    expect(page.next_cursor).toBe("older");
    expect(page.items.every((item) => item.input[0].text!.length <= 64 * 1024)).toBe(true);
    expect(page.items[0].input[0].text).toContain("start");
    expect(page.items[0].input[0].text).toContain("finish");
  });

  it("cancels the old instance request", async () => {
    const controller = new AbortController();
    vi.stubGlobal("fetch", vi.fn((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    })));
    const pending = fetchInstanceRunsPage("instance-old", "", controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
});
