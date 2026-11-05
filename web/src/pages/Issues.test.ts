import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import type { Issue } from "../lib/types";
import IssuesTestHost from "./IssuesTestHost.svelte";

function stubIssuesApi() {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, "http://teamva.test");
    let data: unknown = { items: [] };
    if (url.pathname.endsWith("/git/repositories")) {
      data = { repositories: [], truncated: false, warnings: [], generation: 1 };
    }
    return new Response(JSON.stringify({ data, meta: { request_id: "request-issues" } }), {
      status: 200, headers: { "Content-Type": "application/json" }
    });
  }));
}

afterEach(() => {
  cleanup();
  clearComposerDrafts();
  queryClient.clear();
  vi.unstubAllGlobals();
  navigate("/", { replace: true });
});

const baseIssue: Issue = {
  id: "issue-1", version: 2, project_id: "project-test", title: "", description: "",
  status: "open", worktree_hint: false, created_by_user_id: "user-test",
  created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
};

describe("Issue list server pagination", () => {
  it("requests bounded pages and searches beyond the first page without losing the selected detail", async () => {
    const tasks = Array.from({ length: 240 }, (_, index) => ({ ...baseIssue, id: `paged-${index}`, title: `Paged ${index}` }));
    const requests: URL[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname === "/v1/projects/project-test/issues") {
        requests.push(url);
        const filtered = tasks.filter((item) =>
          (!url.searchParams.get("status") || item.status === url.searchParams.get("status")) &&
          item.title.toLowerCase().includes((url.searchParams.get("search") ?? "").toLowerCase()));
        const offset = Number(url.searchParams.get("offset") ?? 0);
        const limit = Number(url.searchParams.get("limit") ?? tasks.length);
        data = { items: filtered.slice(offset, offset + limit), total: filtered.length };
      } else if (url.pathname === "/v1/issues/paged-0") data = tasks[0];
      else if (url.pathname.endsWith("/git/repositories")) data = { repositories: [], truncated: false, warnings: [], generation: 1 };
      return new Response(JSON.stringify({ data, meta: {} }), { headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues?issue=paged-0", { replace: true });
    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    await waitFor(() => expect(requests[0]?.searchParams.get("limit")).toBe("100"));
    expect(within(list).getAllByText(/^Paged \d+$/)).toHaveLength(100);
    expect(within(list).getByText("1–100 / 240")).toBeInTheDocument();
    await fireEvent.click(within(list).getByLabelText(`${t("issues.listLabel")} +`));
    await waitFor(() => expect(within(list).getByText("101–200 / 240")).toBeInTheDocument());
    expect(requests.some((url) => url.searchParams.get("offset") === "100")).toBe(true);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Paged 0");
    await fireEvent.input(within(list).getByRole("searchbox"), { target: { value: "Paged 230" } });
    await within(list).findByText("Paged 230");
    expect(requests.at(-1)?.searchParams.get("search")).toBe("Paged 230");
    expect(requests.at(-1)?.searchParams.get("offset")).toBe("0");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Paged 0");
    await fireEvent.change(within(list).getByLabelText(t("issues.status")), { target: { value: "resolved" } });
    await within(list).findByText(t("search.noResults"));
    expect(requests.at(-1)?.searchParams.get("status")).toBe("resolved");
    const before = requests.length;
    await queryClient.invalidateQueries({ queryKey: ["issues", "project-test"] });
    await waitFor(() => expect(requests.length).toBeGreaterThan(before));
    expect(requests.every((url) => url.searchParams.get("limit") === "100")).toBe(true);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Paged 0");
  }, 20_000);

  it("opens a newly created detail without inserting it into a nonmatching filtered page", async () => {
    const created = { ...baseIssue, id: "created-outside-filter", title: "New open issue" };
    const requests: URL[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname === "/v1/projects/project-test/issues") {
        if (init?.method === "POST") data = created;
        else { requests.push(url); data = { items: [], total: 0 }; }
      } else if (url.pathname === `/v1/issues/${created.id}`) data = created;
      else if (url.pathname.endsWith("/git/repositories")) data = { repositories: [], truncated: false, warnings: [], generation: 1 };
      return new Response(JSON.stringify({ data, meta: {} }), { headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues", { replace: true });
    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    await fireEvent.change(within(list).getByLabelText(t("issues.status")), { target: { value: "resolved" } });
    await within(list).findByText(t("search.noResults"));
    await fireEvent.click(within(list).getByRole("button", { name: t("issues.newIssue") }));
    const before = requests.length;
    await fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t("common.create") }));
    expect(await screen.findByRole("heading", { level: 1, name: created.title })).toBeInTheDocument();
    await waitFor(() => expect(requests.length).toBeGreaterThan(before));
    expect(within(list).queryByText(created.title)).not.toBeInTheDocument();
    expect(within(list).getByText(t("issues.count", { count: 0 }))).toBeInTheDocument();
    expect(window.location.search).toContain(`issue=${created.id}`);
  });
});

describe("Issues creation without a title", () => {
  it("creates without a title input or title field while preserving the description", async () => {
    let postedBody: Record<string, unknown> | undefined;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/git/repositories")) {
        data = { repositories: [], truncated: false, warnings: [], generation: 1 };
      }
      if (url.pathname.endsWith("/projects/project-test/issues") && init?.method === "POST") {
        postedBody = JSON.parse(String(init.body)) as Record<string, unknown>;
        data = { ...baseIssue, id: "issue-new" };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-untitled" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    window.history.replaceState({}, "", "/issues");
    navigate("/issues", { replace: true });

    render(IssuesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.newIssue") }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByLabelText(t("issues.titleField"))).not.toBeInTheDocument();
    await fireEvent.input(within(dialog).getByLabelText(t("issues.descriptionField")), { target: { value: "  Describe this task  " } });
    const create = await screen.findByRole("button", { name: t("common.create") });
    expect(create).toBeEnabled();
    await fireEvent.click(create);

    await waitFor(() => expect(postedBody).toBeDefined());
    expect(postedBody).not.toHaveProperty("title");
    expect(postedBody).toMatchObject({ description: "Describe this task", worktree_hint: false });
  });

  it("still edits an untitled issue by clicking its title, without sending description", async () => {
    let patchBody: Record<string, unknown> | undefined;
    let untitled = { ...baseIssue, description: "创建时保存的描述" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/projects/project-test/issues")) data = { items: [untitled] };
      else if (url.pathname.endsWith("/git/repositories")) {
        data = { repositories: [], truncated: false, warnings: [], generation: 1 };
      } else if (url.pathname.endsWith("/issues/issue-1") && init?.method === "PATCH") {
        patchBody = JSON.parse(String(init.body)) as Record<string, unknown>;
        untitled = { ...untitled, title: String(patchBody.title), version: 3 };
        data = untitled;
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-edit" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    window.history.replaceState({}, "", "/issues");
    navigate("/issues", { replace: true });

    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    await within(list).findByText(t("issues.untitled"));
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.editTitle") }));
    const title = screen.getByRole("textbox", { name: t("issues.titleField") });
    await waitFor(() => expect(title).toHaveFocus());
    await fireEvent.input(title, { target: { value: "补上标题" } });
    await fireEvent.keyDown(title, { key: "Enter" });

    await waitFor(() => expect(patchBody).toBeDefined());
    expect(patchBody).toEqual({ title: "补上标题", expected_version: 2 });
    expect(await screen.findByRole("heading", { level: 1, name: "补上标题" })).toBeInTheDocument();
    expect(within(list).getByText("补上标题")).toBeInTheDocument();
    expect(screen.getByText("创建时保存的描述")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("Issues create deep link", () => {
  it("opens the create dialog when arriving with ?new=1 and consumes the parameter", async () => {
    stubIssuesApi();
    window.history.replaceState({}, "", "/issues");
    navigate("/issues?new=1", { replace: true });

    render(IssuesTestHost);

    expect(await screen.findByRole("dialog", { name: t("issues.createTitle") })).toBeInTheDocument();
    await waitFor(() => expect(window.location.search).toBe(""));
  });

  it("keeps the create dialog closed without the deep link parameter", async () => {
    stubIssuesApi();
    window.history.replaceState({}, "", "/issues");
    navigate("/issues", { replace: true });

    render(IssuesTestHost);

    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.newIssue") })).toBeInTheDocument());
    expect(screen.queryByRole("dialog", { name: t("issues.createTitle") })).not.toBeInTheDocument();
  });
});

describe("Issue inline title editing remains independent of its read-only description", () => {
  it.each(["blur", "escape", "conflict"] as const)("handles %s without changing the creation description", async (mode) => {
    let issue = { ...baseIssue, title: "原标题", description: "创建后保持不变的描述" };
    const patches: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      let data: unknown = { items: [] };
      if (path.endsWith("/projects/project-test/issues")) data = { items: [issue] };
      else if (path.endsWith("/issues/issue-1") && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        patches.push(body);
        if (mode === "conflict") return new Response(JSON.stringify({
          error: { code: "version_conflict", message: "标题版本冲突" }, meta: {}
        }), { status: 409, headers: { "Content-Type": "application/json" } });
        issue = { ...issue, title: String(body.title), version: issue.version + 1 };
        data = issue;
      }
      return new Response(JSON.stringify({ data, meta: {} }), { headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues", { replace: true });
    render(IssuesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.editTitle") }));
    const title = screen.getByRole("textbox", { name: t("issues.titleField") });
    await waitFor(() => expect(title).toHaveFocus());
    await fireEvent.input(title, { target: { value: "新标题" } });
    if (mode === "escape") {
      await fireEvent.keyDown(title, { key: "Escape" });
      expect(screen.getByRole("heading", { name: "原标题" })).toBeInTheDocument();
      expect(patches).toHaveLength(0);
    } else {
      await fireEvent.blur(title);
      await waitFor(() => expect(patches).toHaveLength(1));
      expect(patches[0]).toEqual({ title: "新标题", expected_version: 2 });
      if (mode === "conflict") {
        expect(await screen.findByRole("alert")).toHaveTextContent(t("errors.codes.version_conflict"));
        expect(title).toHaveValue("新标题");
        await waitFor(() => expect(title).toBeEnabled());
      } else {
        expect(await screen.findByRole("heading", { name: "新标题" })).toBeInTheDocument();
      }
    }
    expect(screen.getByText("创建后保持不变的描述")).toBeInTheDocument();
    expect(screen.queryByLabelText(t("issues.descriptionField"))).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("Issue list navigation and discoverability", () => {
  const tasks = [
    { ...baseIssue, id: "older", title: "Older work", updated_at: "2026-08-10T00:00:00Z" },
    { ...baseIssue, id: "newer", title: "Newer work", status: "resolved", updated_at: "2026-08-12T00:00:00Z" }
  ];
  function stubList() {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://teamva.test");
      const path = url.pathname;
      const matches = tasks.filter((item) =>
        (!url.searchParams.get("status") || item.status === url.searchParams.get("status")) &&
        item.title.toLowerCase().includes((url.searchParams.get("search") ?? "").toLowerCase())
      ).sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
      const data = path.endsWith("/projects/project-test/issues") ? { items: matches, total: matches.length }
        : path.endsWith("/issues/older") ? tasks[0]
          : path.endsWith("/issues/newer") ? tasks[1]
            : path.endsWith("/git/repositories") ? { repositories: [], truncated: false, warnings: [], generation: 1 }
              : { items: [] };
      return new Response(JSON.stringify({ data, meta: {} }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues", { replace: true });
  }

  it("keeps the description read-only and removes the separate issue editor", async () => {
    stubList();
    render(IssuesTestHost);
    await screen.findByRole("heading", { level: 1, name: "Newer work" });
    expect(screen.queryByRole("button", { name: "Edit title and description" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText(t("issues.descriptionField"))).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.editTitle") }));
    expect(screen.getByRole("textbox", { name: t("issues.titleField") })).toHaveValue("Newer work");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("sorts by last update, filters status/title, and keeps the selected deep-link detail", async () => {
    stubList();
    navigate("/issues?issue=older", { replace: true });
    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    const rows = await within(list).findAllByRole("button", { name: /(?:Older|Newer) work/ });
    expect(rows.map((row) => row.textContent)).toEqual([expect.stringContaining("Newer work"), expect.stringContaining("Older work")]);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Older work");
    await fireEvent.change(within(list).getByLabelText(t("issues.status")), { target: { value: "resolved" } });
    await waitFor(() => expect(within(list).queryByText("Older work")).not.toBeInTheDocument());
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Older work");
    await fireEvent.input(within(list).getByRole("searchbox"), { target: { value: "missing" } });
    expect(await within(list).findByText(t("search.noResults"))).toBeInTheDocument();
    expect(window.location.search).toContain("issue=older");
  });

  it("uses a switchable full-height narrow master/detail view and preserves comment drafts on task switches", async () => {
    stubList();
    render(IssuesTestHost);
    const layout = await screen.findByTestId("issues-master-detail");
    expect(layout).toHaveAttribute("data-mobile-view", "list");
    const list = screen.getByRole("complementary", { name: t("issues.listLabel") });
    await fireEvent.click(await within(list).findByRole("button", { name: /Older work/ }));
    expect(layout).toHaveAttribute("data-mobile-view", "detail");
    expect(list).toHaveClass("max-md:hidden");
    expect(layout.className).not.toContain("30%");
    await fireEvent.input(screen.getByLabelText(t("issues.commentInput")), { target: { value: "older draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("issues.listLabel") }));
    expect(layout).toHaveAttribute("data-mobile-view", "list");
    await fireEvent.click(within(list).getByRole("button", { name: /Newer work/ }));
    expect(screen.getByLabelText(t("issues.commentInput"))).toHaveValue("");
    await fireEvent.input(screen.getByLabelText(t("issues.commentInput")), { target: { value: "newer draft" } });
    await fireEvent.click(within(list).getByRole("button", { name: /Older work/ }));
    expect(screen.getByLabelText(t("issues.commentInput"))).toHaveValue("older draft");
  });

  it("does not expose the removed editor through right-click or keyboard context menus", async () => {
    stubList();
    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    const row = await within(list).findByRole("button", { name: /Older work/ });
    row.focus();
    await fireEvent.keyDown(row, { key: "F10", shiftKey: true });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(row).toHaveFocus();
    await fireEvent.contextMenu(row, { clientX: window.innerWidth + 100, clientY: window.innerHeight + 100 });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(t("issues.descriptionField"))).not.toBeInTheDocument();
  });

  it("bounds rendered task rows while preserving deep links beyond the current list page", async () => {
    const many = Array.from({ length: 240 }, (_, index) => ({ ...baseIssue, id: `task-${index}`, title: `Task ${index}` }));
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://teamva.test");
      const offset = Number(url.searchParams.get("offset") ?? 0);
      return new Response(JSON.stringify({
        data: url.pathname.endsWith("/projects/project-test/issues") ? { items: many.slice(offset, offset + 100), total: many.length }
          : url.pathname.endsWith("/issues/task-1") ? many[1] : { items: [] }, meta: {}
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues?issue=task-1", { replace: true });
    render(IssuesTestHost);
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    await waitFor(() => expect(within(list).getAllByText(/^Task \d+$/)).toHaveLength(100));
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Task 1");
    await fireEvent.click(within(list).getByLabelText(`${t("issues.listLabel")} +`));
    await within(list).findByText("Task 100");
    expect(within(list).getAllByText(/^Task \d+$/)).toHaveLength(100);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Task 1");
  });

  it("fetches a deep-linked task omitted from the bounded list without selecting the first row", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      const data = path.endsWith("/projects/project-test/issues") ? { items: tasks }
        : path.endsWith("/issues/deep") ? { ...baseIssue, id: "deep", title: "Deep task" }
          : { items: [] };
      return new Response(JSON.stringify({ data, meta: {} }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    navigate("/issues?issue=deep", { replace: true });
    render(IssuesTestHost);
    expect(await screen.findByRole("heading", { level: 1, name: "Deep task" })).toBeInTheDocument();
    expect(screen.getByTestId("issues-master-detail")).toHaveAttribute("data-mobile-view", "detail");
  });
});

describe("Issue deep-link hydration and request isolation", () => {
  const linked: Issue = { ...baseIssue, id: "linked", title: "Linked task" };
  const other: Issue = { ...baseIssue, id: "other", title: "Manually selected task" };
  const foreign: Issue = { ...baseIssue, id: "foreign", project_id: "project-other", title: "Private foreign task" };

  function response(data: unknown, status = 200) {
    return new Response(JSON.stringify(status < 400 ? { data, meta: {} } : {
      error: { code: status === 403 ? "forbidden" : "not_found", message: "Unavailable" }, meta: {}
    }), { status, headers: { "Content-Type": "application/json" } });
  }

  function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((complete) => { resolve = complete; });
    return { promise, resolve };
  }

  function stubSelectionAPI(options: {
    list?: Issue[];
    listResponse?: Promise<Response>;
    detail?: (id: string) => Response | Promise<Response>;
  } = {}) {
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path === "/v1/projects/project-test/issues") return options.listResponse?.then((result) => result.clone()) ?? response({ items: options.list ?? [linked, other] });
      if (path === "/v1/projects/project-other/issues") return response({ items: [foreign] });
      const match = /^\/v1\/issues\/([^/]+)$/.exec(path);
      if (match) return options.detail?.(decodeURIComponent(match[1])) ?? response(linked);
      return response(path.endsWith("/git/repositories")
        ? { repositories: [], truncated: false, warnings: [], generation: 1 }
        : { items: [] });
    });
    vi.stubGlobal("fetch", fetch);
    return fetch;
  }

  it("restores the same-project query selection after asynchronous project hydration and a delayed list", async () => {
    const list = deferred<Response>();
    stubSelectionAPI({ listResponse: list.promise });
    navigate("/issues?issue=linked", { replace: true });
    const view = render(IssuesTestHost, { projectId: null });
    expect(screen.queryByRole("heading", { level: 1, name: linked.title })).not.toBeInTheDocument();

    await view.rerender({ projectId: "project-test" });
    list.resolve(response({ items: [other, linked] }));
    await waitFor(() => expect(queryClient.getQueryState(["issues", "project-test", { search: "", status: "", offset: 0 }])).toMatchObject({ status: "success" }));
    expect(await screen.findByRole("heading", { level: 1, name: linked.title })).toBeInTheDocument();
    const row = within(screen.getByRole("complementary", { name: t("issues.listLabel") })).getByRole("button", { name: /Linked task/ });
    expect(row).toHaveAttribute("aria-current", "page");
    expect(window.location.search).toBe("?issue=linked");
  });

  it("hydrates a task outside the API's first 100 rows through get-detail, never a first-row fallback", async () => {
    const detail = deferred<Response>();
    const first100 = Array.from({ length: 100 }, (_, index) => ({ ...baseIssue, id: `bounded-${index}`, title: `Bounded ${index}` }));
    const fetch = stubSelectionAPI({ list: first100, detail: () => detail.promise });
    navigate("/issues?issue=linked", { replace: true });
    const view = render(IssuesTestHost, { projectId: null });
    await view.rerender({ projectId: "project-test" });
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/v1/issues/linked", expect.anything()));
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    detail.resolve(response(linked));
    expect(await screen.findByRole("heading", { level: 1, name: linked.title })).toBeInTheDocument();
    expect(screen.getByTestId("issues-master-detail")).toHaveAttribute("data-mobile-view", "detail");
  });

  it("applies explicit issue query changes without a remount", async () => {
    stubSelectionAPI();
    navigate("/issues?issue=linked", { replace: true });
    render(IssuesTestHost);
    expect(await screen.findByRole("heading", { level: 1, name: linked.title })).toBeInTheDocument();
    navigate("/issues?issue=other", { replace: true });
    expect(await screen.findByRole("heading", { level: 1, name: other.title })).toBeInTheDocument();
    navigate("/issues?issue=linked", { replace: true });
    expect(await screen.findByRole("heading", { level: 1, name: linked.title })).toBeInTheDocument();
  });

  it("does not let an older get-detail response replace a user's newer list selection", async () => {
    const detail = deferred<Response>();
    const fetch = stubSelectionAPI({ list: [other], detail: () => detail.promise.then((result) => result.clone()) });
    navigate("/issues?issue=linked", { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/v1/issues/linked", expect.anything()));
    const list = screen.getByRole("complementary", { name: t("issues.listLabel") });
    await fireEvent.click(within(list).getByRole("button", { name: /Manually selected task/ }));
    expect(await screen.findByRole("heading", { level: 1, name: other.title })).toBeInTheDocument();
    detail.resolve(response(linked));
    await detail.promise;
    await waitFor(() => expect(queryClient.isFetching({ queryKey: ["issue-detail", "project-test", linked.id] })).toBe(0));
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(other.title);
    expect(window.location.search).toBe("?issue=other");
  });

  it("clears old-project detail immediately and ignores a late response after switching projects", async () => {
    const detail = deferred<Response>();
    const fetch = stubSelectionAPI({ list: [other], detail: () => detail.promise.then((result) => result.clone()) });
    navigate("/issues?issue=linked", { replace: true });
    const view = render(IssuesTestHost);
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/v1/issues/linked", expect.anything()));
    await view.rerender({ projectId: "project-other" });
    detail.resolve(response(linked));
    await detail.promise;
    await waitFor(() => expect(queryClient.getQueryState(["issue-detail", "project-other", linked.id])?.status).toBe("success"));
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    expect(screen.queryByText(linked.title)).not.toBeInTheDocument();
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    expect(within(list).getByText(foreign.title)).toBeInTheDocument();
  });

  it("removes an already rendered old-project task when the project changes", async () => {
    stubSelectionAPI();
    navigate("/issues?issue=linked", { replace: true });
    const view = render(IssuesTestHost);
    expect(await screen.findByRole("heading", { level: 1, name: linked.title })).toBeInTheDocument();
    await view.rerender({ projectId: "project-other" });
    expect(screen.queryByRole("heading", { level: 1, name: linked.title })).not.toBeInTheDocument();
    const list = await screen.findByRole("complementary", { name: t("issues.listLabel") });
    expect(within(list).getByText(foreign.title)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
  });

  it("never displays a foreign-project detail even if get-detail returns it successfully", async () => {
    stubSelectionAPI({ detail: () => response(foreign) });
    navigate("/issues?issue=foreign", { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(queryClient.getQueryData(["issue-detail", "project-test", foreign.id])).toEqual(foreign));
    expect(screen.queryByText(foreign.title)).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
  });

  it.each([403, 404])("keeps HTTP %s deep links empty without exposing a different task", async (status) => {
    stubSelectionAPI({ detail: () => response(undefined, status) });
    navigate("/issues?issue=unavailable", { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(queryClient.getQueryState(["issue-detail", "project-test", "unavailable"])?.status).toBe("error"), { timeout: 3_000 });
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    expect(screen.getByText(t("issues.selectOrCreate"))).toBeInTheDocument();
    expect(window.location.search).toBe("?issue=unavailable");
  });

  it.each([403, 404])("hides cached detail after an HTTP %s revalidation failure", async (status) => {
    queryClient.setQueryData(["issue-detail", "project-test", linked.id], linked, { updatedAt: 1 });
    stubSelectionAPI({ list: [other], detail: () => response(undefined, status) });
    navigate("/issues?issue=linked", { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(queryClient.getQueryState(["issue-detail", "project-test", linked.id])?.status).toBe("error"), { timeout: 3_000 });
    expect(screen.queryByText(linked.title)).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument();
    expect(window.location.search).toBe("?issue=linked");
  });
});
