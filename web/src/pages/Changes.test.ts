import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tick } from "svelte";
import { onlineManager } from "@tanstack/svelte-query";
import Root from "../Root.svelte";
import { t } from "../lib/i18n.svelte";
import { fetchGitDiff, resetGitRepositoryCoordinator } from "../lib/gitChanges";
import { queryClient } from "../lib/query";
import { setComputer, setProject } from "../lib/stores.svelte";
import ChangesTestHost from "./ChangesTestHost.svelte";
// Compile the real shell during collection, not inside the route's interaction timeout.
import "./ConsoleShell.svelte";

describe("changes page", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    resetGitRepositoryCoordinator();
    queryClient.clear();
    onlineManager.setOnline(true);
    setComputer("");
    setProject("");
    vi.unstubAllGlobals();
    window.history.replaceState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  it("renders the project-level route and empty repository state", async () => {
    const sourceIndexPath = resolve(process.cwd(), "index.html");
    const sourceIndexBefore = await readFile(sourceIndexPath, "utf8");
    window.history.replaceState({}, "", "/changes");
    window.dispatchEvent(new PopStateEvent("popstate"));
    setComputer("cmp-test");
    setProject("project-test");
    vi.stubGlobal("EventSource", undefined);

    const computer = {
      id: "cmp-test", name: "This Mac", version: 1, user_id: "user-test",
      platform: "darwin", architecture: "arm64", fingerprint: "fp", status: "online"
    };
    const project = {
      id: "project-test", computer_id: "cmp-test", name: "Frontend", description: "",
      version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };

    const requested: URL[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const requestURL = new URL(raw, "http://teamva.test");
      requested.push(requestURL);
      const path = requestURL.pathname;
      let data: unknown = { items: [], next_cursor: null };
      if (path.endsWith("/auth/local-session")) {
        data = {
          csrf_token: "csrf-test",
          user: {
            id: "user-test", email: "owner@example.test", display_name: "Owner",
            version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
          }
        };
      } else if (path.endsWith("/me/preferences")) {
        data = {
          user_id: "user-test", locale: "zh-CN", locale_explicit: false,
          theme: "system", allow_write: false, version: 1
        };
      } else if (path.endsWith("/computers")) {
        data = { items: [computer], next_cursor: null };
      } else if (path.endsWith("/projects")) {
        data = { items: [project], next_cursor: null };
      } else if (path.endsWith("/git/repositories")) {
        data = { snapshot_id: "snapshot-1", repositories: [], truncated: false, warnings: null };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-test" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    render(Root);

    await waitFor(() => expect(screen.getByTestId("changes-page")).toBeInTheDocument());
    expect(screen.getByRole("heading", { name: t("changes.title") })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(t("changes.noRepositories"))).toBeInTheDocument());
    expect(screen.getAllByText(t("shell.changes")).length).toBeGreaterThan(0);
    expect(window.location.pathname).toBe("/changes");
    expect(requested.some((url) => url.pathname === "/v1/projects/project-test/git/repositories")).toBe(true);
    expect(await readFile(sourceIndexPath, "utf8")).toBe(sourceIndexBefore);
  });

  it("keeps a reconciled repository when connected aborts the initial Query", async () => {
    class FakeEventSource {
      static latest: FakeEventSource | undefined;
      listeners = new Map<string, Array<(event: Event) => void>>();
      constructor(public url: string) {
        const replacingSubscription = Boolean(FakeEventSource.latest);
        FakeEventSource.latest = this;
        // Discovery changes the resource set and creates a new subscription.
        // Real EventSource emits open for that connection as well.
        if (replacingSubscription) queueMicrotask(() => this.open());
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
      emit(type: string, data: unknown) {
        const event = new MessageEvent(type, { data: JSON.stringify(data) });
        for (const listener of this.listeners.get(type) ?? []) listener(event);
      }
    }
    vi.stubGlobal("EventSource", FakeEventSource);

    const snapshot = {
      snapshot_id: "snapshot-fresh",
      repositories: [{
        id: "repo-race", label: "Race repository", root_display: "Race repository",
        resource_ids: ["resource-race"], discovery_origin: "resource", worktree_kind: "main",
        scope: "full", branch: { name: "main" }, operation: null, generation: 7,
        state: "dirty", counts: { unstaged: 1, total: 1 }, warnings: null
      }],
      truncated: false,
      warnings: null
    };
    const staleSnapshot = {
      snapshot_id: "snapshot-stale", repositories: [], truncated: false, warnings: null
    };
    const pendingRepositories: Array<{
      signal?: AbortSignal;
      resolve: (snapshot: typeof staleSnapshot) => void;
    }> = [];
    let repositoryRequests = 0;
    let refreshRepositoryRequests = 0;
    let changeRequests = 0;
    const response = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-race" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });

    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        repositoryRequests += 1;
        if (url.searchParams.get("refresh") === "true") {
          refreshRepositoryRequests += 1;
          return Promise.resolve(response(snapshot));
        }
        return new Promise<Response>((resolve) => {
          pendingRepositories.push({
            signal: init?.signal ?? undefined,
            resolve: (value) => resolve(response(value))
          });
        });
      }
      if (url.pathname.endsWith("/diff")) {
        return Promise.resolve(response({
          snapshot_id: snapshot.snapshot_id, repository_id: "repo-race",
          change_id: "change-race", generation: 7, comparison: "worktree",
          kind: "text", path: { token: "token-race", display: "race.ts" },
          stats: { additions: 1, deletions: 0, lines: 1 },
          patch: "diff --git a/race.ts b/race.ts\n--- a/race.ts\n+++ b/race.ts\n@@ -1 +1 @@\n-old\n+new",
          truncated: false, confirm_required: false
        }));
      }
      if (url.pathname.includes("/changes")) {
        changeRequests += 1;
        return Promise.resolve(response({
          snapshot_id: snapshot.snapshot_id, repository_id: "repo-race",
          repository_generation: 7, area: "unstaged",
          items: [{
            id: "change-race", path_id: "path-race", area: "unstaged",
            status: "modified", kind: "file",
            path: { token: "token-race", display: "race.ts" },
            binary: false, submodule: false, diff_available: true,
            additions: 1, deletions: 0
          }],
          next_cursor: null, truncated: false, warnings: []
        }));
      }
      throw new Error(`Unexpected request: ${url.pathname}${url.search}`);
    }));

    render(ChangesTestHost);
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    // The Query and visibility reconcile share one underlying initial flight.
    await waitFor(() => expect(repositoryRequests).toBe(1));

    FakeEventSource.latest!.open();
    FakeEventSource.latest!.open();
    FakeEventSource.latest!.open();
    await waitFor(() => expect(
      pendingRepositories.some((request) => request.signal?.aborted)
    ).toBe(true));
    expect(await screen.findByText("Race repository")).toBeInTheDocument();
    expect((await screen.findAllByText("race.ts")).length).toBeGreaterThan(0);
    expect(refreshRepositoryRequests).toBe(2);

    const changeRequestsAfterRefresh = changeRequests;
    FakeEventSource.latest!.emit("git.changed", {
      repository_id: "repo-race", generation: 8, reason: "refresh"
    });
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(changeRequests).toBe(changeRequestsAfterRefresh);
    expect(screen.getAllByText("race.ts").length).toBeGreaterThan(0);

    FakeEventSource.latest!.emit("git.changed", {
      repository_id: "repo-race", generation: 9, reason: "worktree_changed"
    });
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(changeRequests).toBe(changeRequestsAfterRefresh);
    expect(screen.getAllByText("race.ts").length).toBeGreaterThan(0);

    // Simulate fetch implementations that settle after abort. Neither the old
    // Query nor the superseded direct reconcile may roll back fresh authority.
    for (const request of pendingRepositories) request.resolve(staleSnapshot);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByText("Race repository")).toBeInTheDocument();
    expect(screen.getAllByText("race.ts").length).toBeGreaterThan(0);
    expect(screen.queryByText(t("changes.noRepositories"))).not.toBeInTheDocument();
    expect(queryClient.getQueryData<{ snapshot_id: string }>(
      ["git", "repositories", "project-race"]
    )?.snapshot_id).toBe("snapshot-fresh");
  });

  it("never presents a repository request error as an authoritative empty state", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async () => {
      throw new Error("repository discovery failed");
    }));

    render(ChangesTestHost);
    expect(await screen.findByText(
      t("changes.repositoriesLoadFailed"), {}, { timeout: 5_000 }
    )).toBeInTheDocument();
    expect(screen.queryByText(t("changes.noRepositories"))).not.toBeInTheDocument();
  });

  it("does not publish a superseded project's late repository response", async () => {
    vi.stubGlobal("EventSource", undefined);
    const oldResolvers: Array<(value: Response) => void> = [];
    const envelope = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-switch" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const snapshot = (id: string, label: string) => ({
      snapshot_id: `snapshot-${id}`,
      repositories: [{
        id: `repo-${id}`, label, root_display: label, resource_ids: [],
        discovery_origin: "resource", worktree_kind: "main", scope: "full",
        branch: null, operation: null, generation: 1, state: "clean",
        counts: {}, warnings: []
      }],
      truncated: false, warnings: []
    });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.includes("/projects/project-old/git/repositories")) {
        return new Promise<Response>((resolve) => oldResolvers.push(resolve));
      }
      if (url.pathname.includes("/projects/project-new/git/repositories")) {
        return Promise.resolve(envelope(snapshot("new", "New repository")));
      }
      throw new Error(`Unexpected request: ${url.pathname}${url.search}`);
    }));

    const view = render(ChangesTestHost, { props: { projectId: "project-old" } });
    await waitFor(() => expect(oldResolvers.length).toBe(1));
    await view.rerender({ projectId: "project-new" });
    expect(await screen.findByText("New repository")).toBeInTheDocument();

    for (const resolve of oldResolvers) {
      resolve(envelope(snapshot("old", "Old stale repository")));
    }
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByText("New repository")).toBeInTheDocument();
    expect(screen.queryByText("Old stale repository")).not.toBeInTheDocument();
  });

  it("loads a never-expanded lazy untracked prefix and finds the first searched child", async () => {
    vi.stubGlobal("EventSource", undefined);
    let prefixRequests = 0;
    const response = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-lazy-search" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const directory = {
      id: "directory-vendor", path_id: "path-vendor", area: "untracked",
      status: "untracked", kind: "directory",
      path: { token: "prefix-vendor", display: "vendor" },
      binary: false, submodule: false, diff_available: false
    };
    const child = {
      id: "change-needle", path_id: "path-needle", area: "untracked",
      status: "untracked", kind: "file",
      path: { token: "token-needle", display: "vendor/needle.ts" },
      binary: false, submodule: false, diff_available: true,
      additions: 1, deletions: 0
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        return response({
          snapshot_id: "snapshot-lazy-search",
          repositories: [{
            id: "repo-lazy", label: "Lazy repository", root_display: "Lazy repository",
            resource_ids: [], discovery_origin: "resource", worktree_kind: "main",
            scope: "full", branch: { name: "main" }, operation: null, generation: 4,
            state: "dirty", counts: { untracked: 1 }, warnings: null
          }],
          truncated: false, warnings: null
        });
      }
      if (url.pathname.endsWith("/diff")) {
        return response({
          snapshot_id: "snapshot-lazy-search", repository_id: "repo-lazy",
          change_id: "change-needle", generation: 4, comparison: "worktree",
          kind: "text", path: child.path, stats: { additions: 1, deletions: 0, lines: 1 },
          patch: "diff --git a/vendor/needle.ts b/vendor/needle.ts\n--- /dev/null\n+++ b/vendor/needle.ts\n@@ -0,0 +1 @@\n+needle",
          truncated: false, confirm_required: false
        });
      }
      if (url.pathname.endsWith("/changes")) {
        const prefix = url.searchParams.get("prefix");
        if (prefix) prefixRequests += 1;
        return response({
          snapshot_id: "snapshot-lazy-search", repository_id: "repo-lazy",
          repository_generation: 4, area: "untracked",
          items: prefix ? [child] : [directory], next_cursor: null,
          truncated: false, warnings: null
        });
      }
      throw new Error(`Unexpected request: ${url.pathname}${url.search}`);
    }));

    render(ChangesTestHost);
    expect(await screen.findByText("vendor")).toBeInTheDocument();
    await fireEvent.input(screen.getByPlaceholderText(t("changes.searchPlaceholder")), {
      target: { value: "needle.ts" }
    });
    expect(await screen.findByText(t("changes.loadToSearch"))).toBeInTheDocument();
    expect(screen.queryByText("needle.ts")).not.toBeInTheDocument();

    await fireEvent.click(screen.getByText(t("changes.loadToSearch")));
    await waitFor(() => expect(prefixRequests).toBe(1));
    expect((await screen.findAllByText("needle.ts")).length).toBeGreaterThan(0);
  });

  it("keeps resource-prefix repositories readable and marks their exact scope", async () => {
    vi.stubGlobal("EventSource", undefined);
    const response = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-partial" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.pathname.endsWith("/git/repositories")) {
        return response({
          snapshot_id: "snapshot-partial",
          repositories: [{
            id: "repo-partial", label: "Nested source", root_display: "Nested source",
            resource_ids: ["resource-nested"], discovery_origin: "ancestor",
            worktree_kind: "main", scope: "resource_prefix", branch: { name: "main" },
            operation: {}, generation: 1, state: "dirty", counts: { unstaged: 1 },
            warnings: []
          }],
          truncated: false, warnings: []
        });
      }
      if (url.pathname.endsWith("/changes")) {
        return response({
          snapshot_id: "snapshot-partial", repository_id: "repo-partial",
          repository_generation: 1, area: "unstaged", truncated: false, warnings: [],
          items: [{
            id: "change-partial", path_id: "path-partial", area: "unstaged",
            status: "modified", kind: "file",
            path: { token: "token-partial", display: "src/partial.ts" },
            binary: false, submodule: null, diff_available: true, additions: 1, deletions: 0
          }]
        });
      }
      throw new Error(`Unexpected request: ${url.pathname}${url.search}`);
    }));

    render(ChangesTestHost);
    const repositoryLabel = await screen.findByText("Nested source");
    expect(repositoryLabel.closest('[role="treeitem"]')).toHaveAttribute("aria-expanded", "true");
    expect(await screen.findByText(t("changes.partialExact"))).toBeInTheDocument();
  });

  describe("resource authorization and reconnect classification", () => {
    class FakeEventSource {
      static instances: FakeEventSource[] = [];
      static failAutomatically = false;
      listeners = new Map<string, EventListener[]>();
      closed = false;
      constructor(readonly url: string) {
        FakeEventSource.instances.push(this);
        if (FakeEventSource.failAutomatically) queueMicrotask(() => this.emit("error"));
      }
      addEventListener(type: string, listener: EventListener) {
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
      }
      close() { this.closed = true; }
      emit(type: string) {
        if (!this.closed) for (const listener of this.listeners.get(type) ?? []) listener(new Event(type));
      }
    }

    const response = (data: unknown) => new Response(JSON.stringify({ data }), {
      headers: { "Content-Type": "application/json" }
    });
    const denial = (code = "path_out_of_scope", status = 403) => new Response(JSON.stringify({
      error: { code, message: "/private/backend-detail: authorization anchor missing", retryable: true }
    }), { status, headers: { "Content-Type": "application/json" } });
    const snapshot = (dirty = false) => ({
      snapshot_id: "snapshot-authorized", truncated: false, warnings: [],
      repositories: dirty ? [{
        id: "repo-authorized", label: "Authorized repository", root_display: "Authorized repository",
        resource_ids: ["resource-authorized"], discovery_origin: "resource", worktree_kind: "main",
        scope: "full", branch: { name: "main" }, operation: null, generation: 1,
        state: "dirty", counts: { unstaged: 1 }, warnings: []
      }] : []
    });
    const file = {
      id: "change-authorized", path_id: "path-authorized", area: "unstaged",
      status: "modified", kind: "file", path: { token: "token-authorized", display: "allowed.ts" },
      binary: false, submodule: false, diff_available: true, additions: 1, deletions: 0
    };
    const changes = (items: unknown[] = [file]) => ({
      snapshot_id: "snapshot-authorized", repository_id: "repo-authorized", repository_generation: 1,
      area: "unstaged", items, next_cursor: null, truncated: false, warnings: []
    });
    function installAPI(handler: (url: URL, init?: RequestInit) => Response | Promise<Response>) {
      const requests: Array<{ url: URL; init?: RequestInit }> = [];
      const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), "http://teamva.test");
        requests.push({ url, init });
        return handler(url, init);
      });
      vi.stubGlobal("fetch", fetchMock);
      return { requests, fetchMock };
    }
    async function flush() {
      await vi.advanceTimersByTimeAsync(0);
      await tick();
    }
    async function advance(ms: number) {
      await vi.advanceTimersByTimeAsync(ms);
      await tick();
    }

    beforeEach(() => {
      vi.useFakeTimers();
      FakeEventSource.instances = [];
      FakeEventSource.failAutomatically = false;
      vi.stubGlobal("EventSource", FakeEventSource);
    });

    it.each([[403, "path_out_of_scope"], [403, "forbidden"], [400, "invalid_scope"]])(
      "stops SSE/discovery retries for %i %s and links to this project's resources instead of claiming disconnection",
      async (status, code) => {
        FakeEventSource.failAutomatically = true;
        const { requests } = installAPI(() => denial(String(code), Number(status)));
        render(ChangesTestHost);
        await flush();
        expect(screen.getAllByText(t("gitAuthorization.title"))).toHaveLength(2);
        expect(screen.getByText(t("gitAuthorization.description"))).toBeVisible();
        expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
        expect(screen.queryByText(t("changes.loadingRepositories"))).not.toBeInTheDocument();
        expect(screen.queryByText(t("changes.liveRetrying", { seconds: 1 }))).not.toBeInTheDocument();
        expect(screen.queryByText(t("changes.noRepositories"))).not.toBeInTheDocument();
        expect(document.body.textContent).not.toContain("backend-detail");
        const link = screen.getByRole("link", { name: t("gitAuthorization.goToResources") });
        expect(link).toHaveAttribute("href", "/resources?project=project-race");
        const calls = requests.length;
        const connections = FakeEventSource.instances.length;
        await advance(90_000);
        expect(requests).toHaveLength(calls);
        expect(FakeEventSource.instances).toHaveLength(connections);
        expect(FakeEventSource.instances.every((source) => source.closed)).toBe(true);
        onlineManager.setOnline(false);
        onlineManager.setOnline(true);
        await flush();
        expect(requests).toHaveLength(calls);
        expect(requests.every(({ url, init }) => url.pathname.endsWith("/git/repositories") && init?.method === "GET")).toBe(true);
        await fireEvent.click(link);
        expect(window.location.pathname + window.location.search).toBe("/resources?project=project-race");
      }
    );

    it.each(["refresh", "query invalidation"])("recovers after explicit resource revalidation via %s, without automatically validating resources", async (action) => {
      let authorized = false;
      const { requests } = installAPI((url) => {
        if (!authorized) return denial();
        if (url.pathname.endsWith("/git/refresh")) return response({});
        return response(snapshot());
      });
      render(ChangesTestHost);
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      const oldConnections = FakeEventSource.instances.length;
      // Only the mock's external resource authorization changes here. The page
      // must never issue /resources/.../validate or approve on the user's behalf.
      authorized = true;
      if (action === "refresh") {
        await fireEvent.click(screen.getByRole("button", { name: t("changes.refresh") }));
      } else {
        await queryClient.invalidateQueries({ queryKey: ["git", "repositories", "project-race"] });
      }
      await flush();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      expect(screen.getByText(t("changes.noRepositories"))).toBeVisible();
      expect(FakeEventSource.instances.length).toBeGreaterThan(oldConnections);
      FakeEventSource.instances.at(-1)!.emit("open");
      await flush();
      expect(screen.getByText(t("changes.liveConnected"))).toBeVisible();
      expect(requests.some(({ url }) => url.pathname.includes("/resources/"))).toBe(false);
    });

    it("probes an SSE error with authenticated HTTP and stops reconnecting if resource authorization was revoked", async () => {
      let authorized = true;
      const { requests } = installAPI(() => authorized ? response(snapshot()) : denial());
      render(ChangesTestHost);
      await flush();
      expect(screen.getByText(t("changes.noRepositories"))).toBeVisible();
      authorized = false;
      const source = FakeEventSource.instances.at(-1)!;
      source.emit("error");
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      const calls = requests.length;
      const connections = FakeEventSource.instances.length;
      await advance(60_000);
      expect(source.closed).toBe(true);
      expect(requests).toHaveLength(calls);
      expect(FakeEventSource.instances).toHaveLength(connections);
      expect(requests.every(({ init }) => init?.credentials === "include")).toBe(true);
    });

    it.each(["changes", "diff"])("stops automatic refresh and drops stale file authority when %s returns 403", async (endpoint) => {
      vi.stubGlobal("EventSource", undefined);
      let authorized = false;
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/git/repositories")) return response(snapshot(true));
        if (url.pathname.endsWith("/changes")) return endpoint === "changes" && !authorized ? denial() : response(changes());
        if (url.pathname.endsWith("/diff")) return authorized ? response({
          snapshot_id: "snapshot-authorized", repository_id: "repo-authorized", change_id: file.id,
          generation: 1, comparison: "worktree", kind: "text", path: file.path,
          stats: { additions: 1, deletions: 0 }, patch: "@@ -0,0 +1 @@\n+allowed",
          truncated: false, confirm_required: false
        }) : denial("invalid_scope");
        throw new Error(`Unexpected request ${url.pathname}`);
      });
      render(ChangesTestHost);
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      expect(screen.queryByText("allowed.ts")).not.toBeInTheDocument();
      expect(requests.some(({ url }) => url.pathname.endsWith("/git/refresh"))).toBe(false);
      const calls = requests.length;
      await advance(45_000);
      expect(requests).toHaveLength(calls);
      if (endpoint === "changes") {
        authorized = true;
        // Returning the same snapshot must not leave the previous failed area
        // stuck as loaded or blocked; a new read attempts the area again.
        await queryClient.invalidateQueries({ queryKey: ["git", "repositories", "project-race"] });
        await flush();
        expect(requests.some(({ url }) => url.pathname.endsWith("/diff"))).toBe(true);
        expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
        expect(screen.getAllByText("allowed.ts").length).toBeGreaterThan(0);
      }
    });

    it("stops discovery and reconnecting when a lazy untracked prefix is denied", async () => {
      vi.stubGlobal("EventSource", undefined);
      const directory = {
        ...file, id: "vendor-directory", area: "untracked", kind: "directory",
        path: { token: "prefix-vendor", display: "vendor" }, diff_available: false
      };
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/git/repositories")) {
          const data = snapshot(true);
          return response({ ...data, repositories: data.repositories.map((repository) => ({
            ...repository, counts: { untracked: 1 }
          })) });
        }
        if (url.searchParams.get("prefix")) return denial("invalid_scope", 400);
        return response({ ...changes([directory]), area: "untracked" });
      });
      render(ChangesTestHost);
      await flush();
      await fireEvent.input(screen.getByPlaceholderText(t("changes.searchPlaceholder")), { target: { value: "needle" } });
      await flush();
      await fireEvent.click(screen.getByText(t("changes.loadToSearch")));
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      const calls = requests.length;
      await advance(45_000);
      expect(requests).toHaveLength(calls);
      expect(requests.some(({ url }) => url.pathname.endsWith("/git/refresh"))).toBe(false);
    });

    it("does not let a pre-denial repository response clear the block or trigger Query's automatic retry", async () => {
      vi.stubGlobal("EventSource", undefined);
      let repositoryRequests = 0;
      let finishRepositories!: (response: Response) => void;
      let finishChanges!: (response: Response) => void;
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/git/repositories")) {
          repositoryRequests += 1;
          return repositoryRequests === 1 ? response(snapshot(true))
            : new Promise<Response>((resolve) => { finishRepositories = resolve; });
        }
        return new Promise<Response>((resolve) => { finishChanges = resolve; });
      });
      render(ChangesTestHost);
      await flush();
      const refetch = queryClient.invalidateQueries({ queryKey: ["git", "repositories", "project-race"] });
      await flush();
      finishChanges(denial());
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      finishRepositories(response(snapshot(true)));
      await refetch;
      await flush();
      const calls = requests.length;
      await advance(60_000);
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      expect(requests).toHaveLength(calls);
      expect(repositoryRequests).toBe(2);
    });

    it("cancels a discovery retry already queued for an older network failure when a Git diff read denies authorization", async () => {
      vi.stubGlobal("EventSource", undefined);
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/diff")) return denial();
        throw new TypeError("Failed to fetch");
      });
      render(ChangesTestHost);
      await flush();
      await queryClient.fetchQuery({
        queryKey: ["git", "diff", "project-race", "repo-authorized", "change-authorized"],
        queryFn: () => fetchGitDiff({ projectId: "project-race", repositoryId: "repo-authorized", changeId: "change-authorized" }),
        retry: false
      }).catch(() => undefined);
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      const calls = requests.length;
      await advance(60_000);
      expect(requests).toHaveLength(calls);
    });

    it("handles a denied manual Git refresh and clears its cached mutation error after a later successful refresh", async () => {
      vi.stubGlobal("EventSource", undefined);
      let authorized = false;
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/git/refresh")) return authorized ? response({}) : denial();
        return response(snapshot());
      });
      render(ChangesTestHost);
      await flush();
      await fireEvent.click(screen.getByRole("button", { name: t("changes.refresh") }));
      await flush();
      expect(screen.getByText(t("gitAuthorization.paused"))).toBeVisible();
      const calls = requests.length;
      await advance(30_000);
      expect(requests).toHaveLength(calls);
      authorized = true;
      await fireEvent.click(screen.getByRole("button", { name: t("changes.refresh") }));
      await flush();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      expect(screen.getByText(t("changes.noRepositories"))).toBeVisible();
    });

    it("preserves normal snapshot/token expiry recovery without showing a resource authorization warning", async () => {
      vi.stubGlobal("EventSource", undefined);
      let changesRequests = 0;
      const { requests } = installAPI((url) => {
        if (url.pathname.endsWith("/git/repositories")) return response(snapshot(true));
        if (url.pathname.endsWith("/git/refresh")) return response({});
        if (url.pathname.endsWith("/changes")) {
          changesRequests += 1;
          return changesRequests === 1 ? denial("snapshot_expired", 409)
            : response(changes([{ ...file, diff_available: false }]));
        }
        throw new Error(`Unexpected request ${url.pathname}`);
      });
      render(ChangesTestHost);
      await flush();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      expect(screen.getAllByText("allowed.ts")[0]).toBeVisible();
      expect(changesRequests).toBe(2);
      expect(requests.filter(({ url }) => url.pathname.endsWith("/git/refresh"))).toHaveLength(1);
    });

    it("keeps reconnecting real transport failures instead of presenting them as resource authorization", async () => {
      FakeEventSource.failAutomatically = true;
      installAPI(() => { throw new TypeError("Failed to fetch"); });
      render(ChangesTestHost);
      await flush();
      expect(screen.getByText(t("changes.liveRetrying", { seconds: 1 }))).toBeVisible();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      const connections = FakeEventSource.instances.length;
      await advance(1_100);
      expect(FakeEventSource.instances.length).toBeGreaterThan(connections);
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: t("gitAuthorization.goToResources") })).not.toBeInTheDocument();
    });

    it("leaves HTTP 401 to the existing session/API flow, not the resource authorization state", async () => {
      vi.stubGlobal("EventSource", undefined);
      const { requests } = installAPI(() => denial("unauthenticated", 401));
      render(ChangesTestHost);
      await flush();
      await advance(1_100);
      expect(screen.getByText(t("changes.repositoriesLoadFailed"))).toBeVisible();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
      expect(requests.every(({ init }) => init?.credentials === "include" && init.method === "GET")).toBe(true);
    });

    it("does not carry a project's authorization block into a different project", async () => {
      vi.stubGlobal("EventSource", undefined);
      installAPI((url) => url.pathname.includes("/project-blocked/") ? denial() : response(snapshot()));
      const view = render(ChangesTestHost, { props: { projectId: "project-blocked" } });
      await flush();
      expect(screen.getByRole("link", { name: t("gitAuthorization.goToResources") }))
        .toHaveAttribute("href", "/resources?project=project-blocked");
      await view.rerender({ projectId: "project-authorized" });
      await flush();
      expect(screen.getByText(t("changes.noRepositories"))).toBeVisible();
      expect(screen.queryByText(t("gitAuthorization.description"))).not.toBeInTheDocument();
    });
  });
});
