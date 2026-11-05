import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  resetGitRepositoryCoordinator, type GitRepositoriesSnapshot
} from "../lib/gitChanges";
import { queryClient } from "../lib/query";
import GitRepositoryRoutesTestHost from "./GitRepositoryRoutesTestHost.svelte";

class FakeEventSource {
  static latest: FakeEventSource | undefined;
  listeners = new Map<string, Array<(event: Event) => void>>();
  constructor(public url: string) {
    FakeEventSource.latest = this;
  }
  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const callback = typeof listener === "function"
      ? listener
      : (event: Event) => listener.handleEvent(event);
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), callback]);
  }
  close() {}
  open() {
    for (const listener of this.listeners.get("open") ?? []) listener(new Event("open"));
  }
}

const now = "2026-08-11T00:00:00Z";
const repository = (id: string, label: string) => ({
  id, common_id: `common-${id}`, label, root_display: label,
  resource_ids: [], discovery_origin: "resource", worktree_kind: "main",
  worktree: { linked: false, main: true }, scope: "full",
  branch: { name: "main", detached: false, unborn: false }, operation: {},
  generation: 1, state: "clean",
  counts: { conflict: 0, staged: 0, unstaged: 0, untracked: 0 }, warnings: null
});
const envelope = (data: unknown) => new Response(JSON.stringify({
  data, meta: { request_id: "request-route-repositories" }
}), { status: 200, headers: { "Content-Type": "application/json" } });
const page = { items: [], next_cursor: null };

afterEach(() => {
  cleanup();
  resetGitRepositoryCoordinator();
  queryClient.clear();
  FakeEventSource.latest = undefined;
  vi.unstubAllGlobals();
});

describe("Git repository coordination across routes", () => {
  it("keeps the refreshed repository through Changes and Issues when stale empty settles last", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const fresh = {
      snapshot_id: "snapshot-route-fresh",
      repositories: [repository("repo-shared", "Shared repository")],
      truncated: false, warnings: null
    };
    const stale = {
      snapshot_id: "snapshot-route-stale", repositories: [], truncated: false, warnings: null
    };
    let resolveInitial!: (response: Response) => void;
    let resolveRefresh!: (response: Response) => void;
    let plainRequests = 0;
    let refreshRequests = 0;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        if (url.searchParams.get("refresh") === "true") {
          refreshRequests += 1;
          return new Promise<Response>((resolve) => (resolveRefresh = resolve));
        }
        plainRequests += 1;
        if (plainRequests === 1) {
          return new Promise<Response>((resolve) => (resolveInitial = resolve));
        }
        return Promise.resolve(envelope(fresh));
      }
      return Promise.resolve(envelope(page));
    }));

    const view = render(GitRepositoryRoutesTestHost, {
      props: { route: "changes", projectId: "project-route" }
    });
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    await waitFor(() => expect(plainRequests).toBe(1));
    FakeEventSource.latest!.open();
    await waitFor(() => expect(refreshRequests).toBe(1));

    await view.rerender({ route: "issues", projectId: "project-route" });
    expect(await screen.findByRole("button", { name: "New task" })).toBeInTheDocument();
    await view.rerender({ route: "schedules", projectId: "project-route" });
    resolveRefresh(envelope(fresh));
    expect(await screen.findByRole("button", { name: "New task" })).toBeInTheDocument();

    resolveInitial(envelope(stale));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(queryClient.getQueryData<GitRepositoriesSnapshot>(
      ["git", "repositories", "project-route"]
    )?.repositories[0]?.id).toBe("repo-shared");

    await view.rerender({ route: "issues", projectId: "project-route" });
    expect(await screen.findByRole("button", { name: "New task" })).toBeInTheDocument();

    await view.rerender({ route: "changes", projectId: "project-route" });
    expect(await screen.findByText("Shared repository")).toBeInTheDocument();
    expect(screen.queryByText("No Git repositories")).not.toBeInTheDocument();
  });

  it("keeps a new project's route isolated from an old project's late response", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolveOld!: (response: Response) => void;
    const freshNew = {
      snapshot_id: "snapshot-project-new",
      repositories: [repository("repo-new-project", "New project repository")],
      truncated: false, warnings: null
    };
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        if (url.pathname.includes("/projects/project-old/")) {
          return new Promise<Response>((resolve) => (resolveOld = resolve));
        }
        return Promise.resolve(envelope(freshNew));
      }
      if (url.pathname.endsWith("/scheduled-tasks")) return Promise.resolve(envelope(page));
      if (url.pathname.endsWith("/issues") || url.pathname.endsWith("/resources") ||
        url.pathname.endsWith("/agents")) return Promise.resolve(envelope(page));
      return Promise.resolve(envelope({ created_at: now }));
    }));

    const view = render(GitRepositoryRoutesTestHost, {
      props: { route: "issues", projectId: "project-old" }
    });
    await waitFor(() => expect(resolveOld).toBeTypeOf("function"));
    await view.rerender({ route: "schedules", projectId: "project-new" });
    expect(await screen.findByRole("button", { name: "New task" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Git repository")).not.toBeInTheDocument();

    resolveOld(envelope({
      snapshot_id: "snapshot-project-old-late",
      repositories: [repository("repo-old-late", "Old late repository")],
      truncated: false, warnings: null
    }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(queryClient.getQueryData<GitRepositoriesSnapshot>(
      ["git", "repositories", "project-new"]
    )).toBeUndefined();
  });

  it("creates an Issue without a repository selector even when discovery is partial", async () => {
    vi.stubGlobal("EventSource", undefined);
    let issuePosts = 0;
    const partial = {
      ...repository("repo-partial", "Nested source"), scope: "resource_prefix"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        return envelope({
          snapshot_id: "snapshot-partial", repositories: [partial],
          truncated: false, warnings: []
        });
      }
      if (url.pathname.endsWith("/issues") && init?.method === "POST") {
        issuePosts += 1;
        return envelope({
          id: "issue-workspace", version: 1, project_id: "project-partial",
          title: "Workspace task", status: "open", worktree_hint: false,
          created_by_user_id: "user-route", created_at: now, updated_at: now
        });
      }
      return envelope(page);
    }));

    render(GitRepositoryRoutesTestHost, { props: { route: "issues", projectId: "project-partial" } });
    await fireEvent.click(await screen.findByRole("button", { name: "New task" }));
    expect(screen.queryByLabelText("Git repository")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
    const create = screen.getByRole("button", { name: "Create" });
    expect(create).toBeEnabled();
    await fireEvent.click(create);
    await waitFor(() => expect(issuePosts).toBe(1));
  });
});
