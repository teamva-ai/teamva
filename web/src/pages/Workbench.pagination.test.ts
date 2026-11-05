import { cleanup, render, screen, within } from "@testing-library/svelte";
import { afterEach, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Workbench from "./Workbench.svelte";

afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); });

it("bounds overview requests without losing full counts, older open tasks or newest closed activity", async () => {
  const issueRequests: URL[] = [];
  const tasks = Array.from({ length: 300 }, (_, index) => ({
    id: `task-${index}`, project_id: "project-test", version: 1,
    title: `Task ${index}`, status: index < 160 ? "open" : "closed",
    created_at: "2026-01-01T00:00:00Z", updated_at: new Date(Date.UTC(2026, 0, 1, 0, index)).toISOString()
  }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://teamva.test");
    let data: unknown = { items: [] };
    if (url.pathname.endsWith("/issues")) {
      issueRequests.push(url);
      const matching = tasks.filter((item) => !url.searchParams.get("status") || item.status === url.searchParams.get("status"));
      data = { items: matching.reverse().slice(0, Number(url.searchParams.get("limit") ?? 300)), total: matching.length };
    }
    return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
  }));
  render(ConsolePageTestHost, { page: Workbench });
  const stats = await screen.findByRole("navigation", { name: t("workbench.title") });
  expect(await within(stats).findByText("160", { exact: true })).toBeInTheDocument();
  expect(screen.getByText("300", { exact: true })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /Task 159/ })).toHaveAttribute("href", "/issues?issue=task-159");
  const recent = screen.getByRole("region", { name: t("workbench.recentActivity") });
  expect(within(recent).getAllByRole("link")[0]).toHaveAttribute("href", "/issues?issue=task-299");
  expect(issueRequests).toHaveLength(2);
  expect(issueRequests.every((url) => url.searchParams.get("limit") === "8")).toBe(true);
});
