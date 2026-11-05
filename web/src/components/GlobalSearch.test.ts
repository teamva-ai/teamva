import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import GlobalSearchTestHost from "./GlobalSearchTestHost.svelte";
import { navigate } from "../lib/router.svelte";
import { t } from "../lib/i18n.svelte";

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/workbench");
  navigate("/workbench", { replace: true });
});

describe("global search", () => {
  it("distinguishes an incomplete empty scan from a complete no-match result", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      data: { items: [], truncated: true }
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    render(GlobalSearchTestHost);
    await fireEvent.click(screen.getByRole("button", { name: "Open global search" }));
    await fireEvent.input(screen.getByRole("searchbox", { name: "Global search query" }), { target: { value: "older content" } });
    expect(await screen.findByText(t("search.partialNoResults"))).toBeInTheDocument();
    expect(screen.getByText(t("search.truncated"))).toBeInTheDocument();
    expect(screen.queryByText(t("search.noResults"))).not.toBeInTheDocument();
  });
  it("queries the current project and navigates to an exact comment", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      data: {
        items: [{
          id: "comment:comment-1", kind: "issue_comment", project_id: "project-test",
          issue_id: "issue-1", comment_id: "comment-1", title: "Recovery task",
          excerpt: "deterministic recovery", occurred_at: "2026-08-13T00:00:00Z"
        }],
        truncated: false
      },
      meta: { request_id: "request-search" }
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    render(GlobalSearchTestHost);

    await fireEvent.click(screen.getByRole("button", { name: "Open global search" }));
    const input = screen.getByRole("searchbox", { name: "Global search query" });
    await fireEvent.input(input, { target: { value: "recovery" } });
    const result = await screen.findByRole("option", { name: /Recovery task/ });
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/projects/project-test/search?q=recovery&limit=30"),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
    await fireEvent.click(result);
    await waitFor(() => expect(window.location.pathname + window.location.search)
      .toBe("/issues?issue=issue-1&comment=comment-1"));
  });

  it("opens with Command K", async () => {
    vi.stubGlobal("fetch", vi.fn());
    render(GlobalSearchTestHost);
    await fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(await screen.findByRole("dialog", { name: "Search this project" })).toBeInTheDocument();
  });

  it("moves through results with the keyboard and opens the selected result", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      data: {
        items: [
          {
            id: "memory:memory-1", kind: "memory", project_id: "project-test",
            memory_id: "memory-1", title: "Architecture memory",
            excerpt: "recovery architecture", occurred_at: "2026-08-13T00:00:00Z"
          },
          {
            id: "issue:issue-2", kind: "issue_description", project_id: "project-test",
            issue_id: "issue-2", title: "Recovery task",
            excerpt: "recovery details", occurred_at: "2026-08-13T00:00:01Z"
          }
        ],
        truncated: false
      },
      meta: { request_id: "request-keyboard-search" }
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    render(GlobalSearchTestHost);

    await fireEvent.click(screen.getByRole("button", { name: "Open global search" }));
    const input = screen.getByRole("searchbox", { name: "Global search query" });
    await fireEvent.input(input, { target: { value: "recovery" } });
    const options = await screen.findAllByRole("option");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(options[1]).toHaveAttribute("aria-selected", "true");
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(window.location.pathname + window.location.search)
      .toBe("/issues?issue=issue-2&focus=description"));
  });

  it("ignores Enter during debounce and on empty input, but still opens the matching fresh result", async () => {
    navigate("/workbench", { replace: true });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const current = new URL(String(input), "http://teamva.test").searchParams.get("q");
      return new Response(JSON.stringify({ data: { items: [{
        id: `memory:${current}`, kind: "memory", project_id: "project-test", memory_id: current,
        title: `Result ${current}`, excerpt: current, occurred_at: "2026-10-01T00:00:00Z"
      }], truncated: false } }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(GlobalSearchTestHost);
    await fireEvent.click(screen.getByRole("button", { name: "Open global search" }));
    const input = screen.getByRole("searchbox", { name: "Global search query" });
    await fireEvent.input(input, { target: { value: "old" } });
    await screen.findByRole("option", { name: /Result old/ });
    await fireEvent.input(input, { target: { value: "new" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/workbench");
    await screen.findByRole("option", { name: /Result new/ });
    await fireEvent.input(input, { target: { value: "" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/workbench");
    await fireEvent.input(input, { target: { value: "current" } });
    await screen.findByRole("option", { name: /Result current/ });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(window.location.search).toBe("?memory=current"));
  });

  it("aborts an in-flight old query immediately when input changes", async () => {
    const requests: Array<{ query: string | null; signal?: AbortSignal | null }> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://teamva.test");
      // Count only the request this regression exercises. Deferred navigation
      // telemetry is independent and must not become a hung search request.
      if (url.pathname !== "/v1/projects/project-test/search") return Promise.resolve(new Response(JSON.stringify({ data: {} }), { status: 200 }));
      requests.push({ query: url.searchParams.get("q"), signal: init?.signal });
      return new Promise<Response>(() => {});
    }));
    render(GlobalSearchTestHost);
    const trigger = screen.getByRole("button", { name: "Open global search" });
    trigger.focus();
    await fireEvent.click(trigger);
    const input = screen.getByRole("searchbox", { name: "Global search query" });
    await waitFor(() => expect(input).toHaveFocus());
    await fireEvent.input(input, { target: { value: "old" } });
    await waitFor(() => expect(requests).toHaveLength(1));
    await fireEvent.input(input, { target: { value: "replacement" } });
    expect(requests[0].signal?.aborted).toBe(true);
    await fireEvent.keyDown(input, { key: "Escape" });
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});
