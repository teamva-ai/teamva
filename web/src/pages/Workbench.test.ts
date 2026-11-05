import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Workbench from "./Workbench.svelte";

afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); });

describe("workbench object navigation and recency", () => {
  it("links every object exactly, prioritizes open/recent tasks, and sorts recent activity by update time", async () => {
    const base = { version: 1, created_at: "2026-01-01T00:00:00Z", project_id: "project-test" };
    const data: Record<string, unknown[]> = {
      issues: [
        { ...base, id: "closed", title: "Closed newest", status: "closed", updated_at: "2026-10-20T00:00:00Z" },
        { ...base, id: "old-open", title: "Older open", status: "open", updated_at: "2026-10-01T00:00:00Z" },
        { ...base, id: "recent/open", title: "  ", status: "open", updated_at: "2026-10-18T00:00:00Z" }
      ],
      "scheduled-tasks": [
        { ...base, id: "paused", name: "Paused", enabled: false, updated_at: "2026-10-19T00:00:00Z" },
        { ...base, id: "schedule-target", name: "Schedule target", enabled: true, next_run_at: "2026-10-21T00:00:00Z", updated_at: "2026-10-02T00:00:00Z" }
      ],
      resources: [{ ...base, id: "resource-target", name: "Resource target", updated_at: "2026-10-17T00:00:00Z" }],
      memories: [{ ...base, id: "memory-target", title: "Memory target", updated_at: "2026-10-16T00:00:00Z" }]
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify({
      data: { items: data[new URL(String(input), "http://teamva.test").pathname.split("/").pop()!] ?? [] }
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const view = render(ConsolePageTestHost, { page: Workbench });
    await waitFor(() => expect(queryClient.getQueryCache().getAll().map((query) => ({ key: query.queryKey, status: query.state.status, error: query.state.error })))
      .toEqual(expect.arrayContaining(["issues", "schedules", "resources", "memories"].map((key) => ({ key: key === "issues" ? [key, "project-test", "overview"] : [key, "project-test"], status: "success", error: null })))));
    await screen.findAllByRole("link", { name: /Resource target/ });
    const issueLinks = [...view.container.querySelectorAll<HTMLAnchorElement>('a[href^="/issues?issue="]')];
    expect(issueLinks.slice(0, 3).map((link) => link.getAttribute("href"))).toEqual([
      "/issues?issue=recent%2Fopen", "/issues?issue=old-open", "/issues?issue=closed"
    ]);
    expect(issueLinks[0]).toHaveTextContent(t("issues.untitled"));
    expect(screen.getAllByRole("link", { name: /Schedule target/ })[0]).toHaveAttribute("href", "/schedules?schedule=schedule-target");
    expect(screen.getAllByRole("link", { name: /Resource target/ })[0]).toHaveAttribute("href", "/resources?resource=resource-target");
    expect(screen.getAllByRole("link", { name: /Memory target/ })[0]).toHaveAttribute("href", "/memories?memory=memory-target");
    const recent = screen.getByRole("region", { name: t("workbench.recentActivity") });
    expect(within(recent).getAllByRole("link").map((link) => link.getAttribute("href"))).toEqual([
      "/issues?issue=closed", "/schedules?schedule=paused", "/issues?issue=recent%2Fopen",
      "/resources?resource=resource-target", "/memories?memory=memory-target"
    ]);
    const scheduleLinks = [...view.container.querySelectorAll<HTMLAnchorElement>('a[href^="/schedules?schedule="]')];
    expect(scheduleLinks[0]).toHaveAttribute("href", "/schedules?schedule=schedule-target");
  });
});

describe("workbench default project protection", () => {
  function mockProjectRequests() {
    const patches: Record<string, unknown>[] = [];
    const fetch = vi.fn(async (_input: RequestInfo | URL, options?: RequestInit) => {
      if (options?.method === "PATCH") {
        const patch = JSON.parse(String(options.body));
        patches.push(patch);
        return new Response(JSON.stringify({ data: {
          id: "project-test", computer_id: "computer-test", name: "默认项目",
          is_default: true, version: 2, ...patch
        } }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ data: { items: [] } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    });
    vi.stubGlobal("fetch", fetch);
    return patches;
  }

  it("renders a non-editable default title and badge while still saving its description", async () => {
    const patches = mockProjectRequests();
    render(ConsolePageTestHost, { page: Workbench, isDefault: true, projectName: "默认项目" });
    const heading = await screen.findByRole("heading", { name: "默认项目", level: 1 });
    expect(heading.closest("button")).toBeNull();
    expect(screen.queryByRole("button", { name: "默认项目" })).not.toBeInTheDocument();
    expect(screen.getByText(t("defaultProject.badge"), { exact: true })).toBeVisible();
    await fireEvent.click(heading);
    expect(screen.queryByRole("textbox", { name: t("settings.projectName") })).not.toBeInTheDocument();
    expect(patches).toEqual([]);

    await fireEvent.click(screen.getByRole("button", { name: t("workbench.noProjectNote") }));
    const description = screen.getByRole("textbox", { name: t("settings.description") });
    await fireEvent.input(description, { target: { value: "默认项目仍可记录说明。" } });
    await fireEvent.blur(description);
    await waitFor(() => expect(patches).toEqual([{ expected_version: 1, description: "默认项目仍可记录说明。" }]));
    expect(patches.every((patch) => !("name" in patch))).toBe(true);
  });

  it("reacts to a newly default project flag without submitting an in-progress name edit", async () => {
    const patches = mockProjectRequests();
    const view = render(ConsolePageTestHost, { page: Workbench });
    const heading = await screen.findByRole("heading", { name: "Project", level: 1 });
    expect(heading.closest("button")).not.toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Project" }));
    const input = screen.getByRole("textbox", { name: t("settings.projectName") });
    await fireEvent.input(input, { target: { value: "不允许的名称" } });
    await view.rerender({ page: Workbench, isDefault: true, projectName: "默认项目" });
    const defaultHeading = await screen.findByRole("heading", { name: "默认项目", level: 1 });
    expect(defaultHeading.closest("button")).toBeNull();
    expect(screen.queryByRole("textbox", { name: t("settings.projectName") })).not.toBeInTheDocument();
    await fireEvent.blur(input);
    expect(patches).toEqual([]);
  });
});
