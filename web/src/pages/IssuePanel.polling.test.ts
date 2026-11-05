import { cleanup, render } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { queryClient } from "../lib/query";
import type { IssueComment, Page } from "../lib/types";
import IssuePanelTestHost from "./IssuePanelTestHost.svelte";

afterEach(() => {
  cleanup();
  clearComposerDrafts();
  queryClient.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("IssuePanel fallback polling", () => {
  it("resumes all four fallback queries after mounting in a hidden tab", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("EventSource", undefined);
    let hidden = true;
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => hidden ? "hidden" : "visible");
    const requests = new Map<string, number>();
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      requests.set(path, (requests.get(path) ?? 0) + 1);
      return new Response(JSON.stringify({ data: { items: [] } }), { headers: { "Content-Type": "application/json" } });
    }));
    render(IssuePanelTestHost, { repositories: [] });
    await tick();
    await vi.advanceTimersByTimeAsync(0);
    const paths = [
      "/v1/issues/issue-test/comments", "/v1/issues/issue-test/agent-instances",
      "/v1/issues/issue-test/runs", "/v1/approvals"
    ];
    for (const path of paths) expect(requests.get(path), path).toBe(1);

    await vi.advanceTimersByTimeAsync(6_000);
    for (const path of paths) expect(requests.get(path), path).toBe(1);
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(10_000);
    for (const path of paths) expect(requests.get(path), path).toBeGreaterThan(1);

    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    const visibleCounts = new Map(requests);
    await vi.advanceTimersByTimeAsync(6_000);
    for (const path of paths) expect(requests.get(path), path).toBe(visibleCounts.get(path));
  });

  it.each(["idle", "running", "working"])("keeps a %s task synchronized without SSE at the appropriate interval", async (activity) => {
    vi.useFakeTimers();
    vi.stubGlobal("EventSource", undefined);
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    let body = "original comment";
    const commentRequests = vi.fn();
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      let data: unknown = { items: [] };
      if (path.endsWith("/comments")) {
        commentRequests();
        data = { items: [{
          id: "comment-test", issue_id: "issue-test", author_kind: activity === "working" ? "agent" : "user",
          body, status: activity === "working" ? "working" : "published", version: 1,
          created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T10:00:00Z"
        }] };
      } else if (path.endsWith("/runs") && activity === "running") {
        data = { items: [{ id: "run-test", status: "running" }] };
      }
      return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
    }));
    render(IssuePanelTestHost, { repositories: [] });
    await tick();
    await vi.advanceTimersByTimeAsync(0);
    expect(commentRequests).toHaveBeenCalledTimes(1);
    // Simulate another window editing an ordinary comment without sending a notification.
    body = "edited in another window";
    await vi.advanceTimersByTimeAsync(2_000);
    expect(commentRequests).toHaveBeenCalledTimes(activity === "idle" ? 1 : 2);
    if (activity === "idle") {
      await vi.advanceTimersByTimeAsync(8_000);
      expect(commentRequests).toHaveBeenCalledTimes(2);
    }
    expect(queryClient.getQueryData<Page<IssueComment>>(["issue-comments", "issue-test"])?.items[0].body).toBe(body);
  });

  it("changes the comment interval as Runs start and settle", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("EventSource", undefined);
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    let runStatus = "succeeded";
    const commentRequests = vi.fn();
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/comments")) commentRequests();
      const data = { items: path.endsWith("/runs") ? [{ id: "run-test", status: runStatus }] : [] };
      return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
    }));
    render(IssuePanelTestHost, { repositories: [] });
    await tick();
    await vi.advanceTimersByTimeAsync(0);
    expect(commentRequests).toHaveBeenCalledTimes(1);

    runStatus = "running";
    await queryClient.invalidateQueries({ queryKey: ["issue-runs", "issue-test"] });
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    await vi.advanceTimersByTimeAsync(2_000);
    expect(commentRequests).toHaveBeenCalledTimes(2);

    runStatus = "succeeded";
    await queryClient.invalidateQueries({ queryKey: ["issue-runs", "issue-test"] });
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    await vi.advanceTimersByTimeAsync(8_000);
    expect(commentRequests).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(commentRequests).toHaveBeenCalledTimes(3);
  });
});
