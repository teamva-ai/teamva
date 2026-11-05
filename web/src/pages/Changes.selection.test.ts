import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { resetGitRepositoryCoordinator, type GitChange, type GitChangeArea } from "../lib/gitChanges";
import { queryClient } from "../lib/query";
import ChangesTestHost from "./ChangesTestHost.svelte";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners = new Map<string, EventListener[]>();
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
    queueMicrotask(() => this.emit("open"));
  }
  addEventListener(type: string, listener: EventListener) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  close() { this.closed = true; }
  emit(type: string, data?: unknown) {
    if (this.closed) return;
    const event = data === undefined ? new Event(type) : new MessageEvent(type, { data: JSON.stringify(data) });
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

const files: Record<string, Array<[GitChangeArea, string]>> = {
  "repo-a": [["staged", "shared.txt"], ["unstaged", "shared.txt"], ["untracked", "preserve-user-note.txt"]],
  "repo-b": [["staged", "qa-ui-staged.txt"], ["staged", "shared.txt"], ["unstaged", "README.md"], ["unstaged", "shared.txt"], ["untracked", "proof.txt"]],
  "repo-c": [["untracked", "qa-next.txt"]],
  "repo-d": [["unstaged", "README.md"]]
};
const response = (data: unknown) => new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
const change = (repository: string, area: GitChangeArea, path: string, epoch: number): GitChange => ({
  id: `${repository}-${area}-${path}-${epoch}`, path_id: `path-${repository}-${area}-${path}-${epoch}`,
  area, status: area === "untracked" ? "untracked" : "modified", kind: "file",
  path: { token: `token-${repository}-${area}-${path}-${epoch}`, display: path },
  binary: false, submodule: false, diff_available: true, additions: 1, deletions: 0
});

async function flush(ms = 0) {
  await vi.advanceTimersByTimeAsync(ms);
  for (let index = 0; index < 8; index++) {
    await tick();
    await vi.advanceTimersByTimeAsync(0);
  }
}

function installAPI(options: { holdOldDiff?: boolean; stableSnapshot?: boolean; lazyUntracked?: boolean; paginatedStaged?: boolean; ambiguousDisplay?: boolean } = {}) {
  let epoch = 1;
  let holdNewB = false;
  let heldArea: GitChangeArea = "staged";
  let removeSelectedFile = false;
  const pendingB: Array<() => void> = [];
  const pendingDiffs: Array<{ signal?: AbortSignal; finish: () => void }> = [];
  const requests: Array<{ url: URL; signal?: AbortSignal }> = [];
  const snapshotId = (project: string, requestedEpoch = epoch) => `${project}-snapshot-${options.stableSnapshot ? 1 : requestedEpoch}`;
  const snapshot = (project: string) => ({
    snapshot_id: snapshotId(project), truncated: false, warnings: [],
    repositories: Object.entries(files).map(([id, items]) => ({
      id, label: id, root_display: id, resource_ids: [`resource-${id}`], discovery_origin: "resource", worktree_kind: "main",
      scope: "full", branch: { name: "main" }, operation: null, generation: epoch,
      state: "dirty", counts: Object.fromEntries(["staged", "unstaged", "untracked"].map((area) => [area, items.filter(([value]) => value === area).length])), warnings: []
    }))
  });
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://teamva.test");
    requests.push({ url, signal: init?.signal ?? undefined });
    const project = url.pathname.split("/projects/")[1]?.split("/")[0] ?? "";
    if (url.pathname.endsWith("/git/repositories")) return Promise.resolve(response(snapshot(project)));
    if (url.pathname.endsWith("/git/refresh")) return Promise.resolve(response({}));
    const repository = url.pathname.split("/git/repositories/")[1]?.split("/")[0] ?? "";
    const requestedEpoch = epoch;
    if (url.pathname.endsWith("/changes")) {
      const area = url.searchParams.get("area") ?? "";
      const prefix = url.searchParams.get("prefix");
      const cursor = url.searchParams.get("cursor");
      let items = files[repository].filter(([value, path]) => value === area && !(removeSelectedFile && path === "qa-ui-staged.txt"))
        .map(([value, path]) => change(repository, value, path, requestedEpoch));
      if (repository === "repo-b" && area === "untracked" && options.lazyUntracked) {
        items = prefix ? [change(repository, area, "vendor/proof.txt", requestedEpoch)]
          : [{ ...change(repository, area, "vendor", requestedEpoch), kind: "directory", diff_available: false }];
      }
      const paginated = options.paginatedStaged && repository === "repo-b" && area === "staged";
      if (paginated) items = items.filter((item) => cursor ? item.path.display === "qa-ui-staged.txt" : item.path.display === "shared.txt");
      if (options.ambiguousDisplay && repository === "repo-b" && requestedEpoch > 1 && area === "staged") {
        items.push({ ...change(repository, area, "qa-ui-staged.txt", requestedEpoch), id: "ambiguous-change", path: { display: "qa-ui-staged.txt", token: "ambiguous-token" } });
      }
      const data = {
        snapshot_id: snapshotId(project, requestedEpoch), repository_id: repository, repository_generation: requestedEpoch,
        area, next_cursor: paginated && !cursor ? `cursor-b-${requestedEpoch}` : null, truncated: false, warnings: [], items
      };
      if (holdNewB && repository === "repo-b" && requestedEpoch > 1 && area === heldArea) {
        // Deliberately ignore AbortSignal: stale-response protection must not
        // depend on a cooperative transport.
        return new Promise<Response>((resolve) => pendingB.push(() => resolve(response(data))));
      }
      return Promise.resolve(response(data));
    }
    if (url.pathname.endsWith("/diff")) {
      const id = decodeURIComponent(url.pathname.split("/changes/")[1]?.split("/")[0] ?? "");
      const paths = options.lazyUntracked && repository === "repo-b" ? [...files[repository], ["untracked", "vendor/proof.txt"] as [GitChangeArea, string]] : files[repository];
      const match = paths.flatMap(([area, path]) => [change(repository, area, path, requestedEpoch), change(repository, area, path, 1)])
        .find((item) => item.id === id)!;
      const data = {
        snapshot_id: snapshotId(project, requestedEpoch), repository_id: repository, change_id: id, generation: requestedEpoch,
        comparison: match.area === "staged" ? "index" : "worktree", kind: "text", path: match.path,
        stats: { additions: 1, deletions: 0 }, patch: `diff --git a/${match.path.display} b/${match.path.display}\n--- /dev/null\n+++ b/${match.path.display}\n@@ -0,0 +1 @@\n+MARKER_${project}_${id}`,
        truncated: false, confirm_required: false
      };
      if (options.holdOldDiff && repository === "repo-b" && requestedEpoch === 1) {
        return new Promise<Response>((resolve) => pendingDiffs.push({ signal: init?.signal ?? undefined, finish: () => resolve(response(data)) }));
      }
      return Promise.resolve(response(data));
    }
    throw new Error(`Unexpected request ${url.pathname}${url.search}`);
  }));
  return {
    requests, pendingDiffs,
    refresh({ holdB = false, removeFile = false, area = "staged" }: { holdB?: boolean; removeFile?: boolean; area?: GitChangeArea } = {}) {
      epoch++; holdNewB = holdB; removeSelectedFile = removeFile; heldArea = area;
    },
    releaseB() { holdNewB = false; for (const finish of pendingB.splice(0)) finish(); },
    notifyB() { FakeEventSource.instances.at(-1)!.emit("git.changed", { repository_id: "repo-b", generation: epoch, reason: "change_snapshot_evicted" }); }
  };
}

function fileRow(repository: string, area: GitChangeArea, path: string, epoch: number) {
  const id = change(repository, area, path, epoch);
  return [...screen.getByRole("tree").querySelectorAll<HTMLButtonElement>('[role="treeitem"]')]
    .find((row) => row.dataset.rowKey === `${repository}\0${area}\0${id.id}\0${id.path.token}`)!;
}
function article() { return document.querySelector("article[data-diff-change-id]"); }
async function search(value: string) {
  await fireEvent.input(screen.getByPlaceholderText(t("changes.searchPlaceholder")), { target: { value } });
  await flush();
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});
afterEach(() => {
  cleanup();
  resetGitRepositoryCoordinator();
  queryClient.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Git changes selection intent across authority refresh", () => {
  it("does not replace a filtered click with repo A while its diff and refreshed repo B are pending", async () => {
    const cancel = vi.spyOn(queryClient, "cancelQueries");
    const api = installAPI({ holdOldDiff: true });
    render(ChangesTestHost);
    await flush();
    await search("qa-ui-staged");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-staged-qa-ui-staged.txt-1");
    expect(within(article() as HTMLElement).getByText(t("changes.loadingDiff"))).toBeInTheDocument();

    api.refresh({ holdB: true });
    api.notifyB();
    await flush(200);
    expect(cancel.mock.results.every((result) => result.type === "return")).toBe(true);
    expect(article()?.getAttribute("data-diff-change-id")).toBeUndefined(); // No unrelated default may take over.
    expect(api.pendingDiffs.some((request) => request.signal?.aborted)).toBe(true);
    expect(screen.getByPlaceholderText(t("changes.searchPlaceholder"))).toHaveValue("qa-ui-staged");
    api.releaseB();
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-staged-qa-ui-staged.txt-2");
    expect(article()).toHaveTextContent("MARKER_project-race_repo-b-staged-qa-ui-staged.txt-2");
    expect(fileRow("repo-b", "staged", "qa-ui-staged.txt", 2)).toHaveAttribute("aria-selected", "true");
    for (const pending of api.pendingDiffs) pending.finish();
    await flush();
    expect(article()).toHaveTextContent("MARKER_project-race_repo-b-staged-qa-ui-staged.txt-2");
    expect(article()).not.toHaveTextContent("MARKER_project-race_repo-b-staged-qa-ui-staged.txt-1");
  });

  it.each(["staged", "unstaged"])("rebinds the same %s path only within the selected repository and area", async (area) => {
    const api = installAPI();
    render(ChangesTestHost);
    await flush();
    await search("shared.txt");
    await fireEvent.click(fileRow("repo-b", area, "shared.txt", 1));
    await flush();
    api.refresh({ holdB: true, area });
    api.notifyB();
    await flush(200);
    expect(article()?.getAttribute("data-diff-change-id")).toBeUndefined();
    api.releaseB();
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", `repo-b-${area}-shared.txt-2`);
    expect(fileRow("repo-b", area, "shared.txt", 2)).toHaveAttribute("aria-selected", "true");
    expect(fileRow("repo-a", area, "shared.txt", 2)).toHaveAttribute("aria-selected", "false");
  });

  it.each(["reconnect", "poll", "manual"])("preserves a filtered selection across %s snapshot replacement", async (source) => {
    const api = installAPI();
    render(ChangesTestHost);
    await flush();
    await search("qa-ui-staged");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    api.refresh({ holdB: true });
    if (source === "reconnect") FakeEventSource.instances.at(-1)!.emit("open");
    else if (source === "manual") await fireEvent.click(screen.getByRole("button", { name: t("changes.refresh") }));
    else await flush(15_000);
    await flush(200);
    expect(article()?.getAttribute("data-diff-change-id")).toBeUndefined();
    api.releaseB();
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-staged-qa-ui-staged.txt-2");
    expect(article()).toHaveTextContent("MARKER_project-race_repo-b-staged-qa-ui-staged.txt-2");
  });

  it("rebinds after a repository generation changes without replacing the project snapshot ID", async () => {
    const api = installAPI({ stableSnapshot: true });
    render(ChangesTestHost);
    await flush();
    await search("qa-ui-staged");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    api.refresh({ holdB: true });
    api.notifyB();
    await flush(200);
    expect(article()?.getAttribute("data-diff-change-id")).toBeUndefined();
    api.releaseB();
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-staged-qa-ui-staged.txt-2");
  });

  it("does not guess between fresh changes with ambiguous display names", async () => {
    const api = installAPI({ ambiguousDisplay: true });
    render(ChangesTestHost);
    await flush();
    await search("qa-ui-staged");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    api.refresh();
    api.notifyB();
    await flush(200);
    expect(article()?.getAttribute("data-diff-change-id")).toBeUndefined();
    expect(screen.getByRole("heading", { name: t("changes.selectChange") })).toBeInTheDocument();
  });

  it("restores a lazy untracked child only by loading its fresh prefix token", async () => {
    const api = installAPI({ lazyUntracked: true });
    render(ChangesTestHost);
    await flush();
    await search("proof");
    await fireEvent.click(screen.getByText(t("changes.loadToSearch")));
    await flush();
    await fireEvent.click(fileRow("repo-b", "untracked", "vendor/proof.txt", 1));
    await flush();
    api.refresh();
    api.notifyB();
    await flush(200);
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-untracked-vendor/proof.txt-2");
    expect(article()).toHaveTextContent("MARKER_project-race_repo-b-untracked-vendor/proof.txt-2");
    expect(api.requests.map(({ url }) => url.searchParams.get("prefix")).filter(Boolean)).toEqual([
      "token-repo-b-untracked-vendor-1", "token-repo-b-untracked-vendor-2"
    ]);
  });

  it("restores a selected file from a later page using the new cursor rather than declaring it absent", async () => {
    const api = installAPI({ paginatedStaged: true });
    render(ChangesTestHost);
    await flush();
    await search("qa-ui-staged");
    await fireEvent.click(screen.getByText(t("changes.loadMore")));
    await flush();
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    api.refresh();
    api.notifyB();
    await flush(200);
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-b-staged-qa-ui-staged.txt-2");
    expect(api.requests.map(({ url }) => url.searchParams.get("cursor")).filter(Boolean)).toEqual(["cursor-b-1", "cursor-b-2"]);
  });

  it("does not let a pending restoration override a newer explicit click", async () => {
    const api = installAPI();
    render(ChangesTestHost);
    await flush();
    await search("qa-");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    api.refresh({ holdB: true });
    api.notifyB();
    await flush(200);
    await fireEvent.click(fileRow("repo-c", "untracked", "qa-next.txt", 2));
    await flush();
    api.releaseB();
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-c-untracked-qa-next.txt-2");
    expect(article()).toHaveTextContent("MARKER_project-race_repo-c-untracked-qa-next.txt-2");
  });

  it("only chooses a visible search match after the selected file is authoritatively absent", async () => {
    const api = installAPI();
    render(ChangesTestHost);
    await flush();
    await search("qa-");
    await fireEvent.click(fileRow("repo-b", "staged", "qa-ui-staged.txt", 1));
    await flush();
    api.refresh({ removeFile: true });
    api.notifyB();
    await flush(200);
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-c-untracked-qa-next.txt-2");
  });

  it("does not carry selection intent or late file responses into another project with the same repo/path names", async () => {
    const api = installAPI({ holdOldDiff: true });
    const view = render(ChangesTestHost, { projectId: "project-old" });
    await flush();
    await search("shared.txt");
    await fireEvent.click(fileRow("repo-b", "staged", "shared.txt", 1));
    await flush();
    api.refresh({ holdB: true });
    api.notifyB();
    await flush(200);
    await view.rerender({ projectId: "project-new" });
    await flush();
    expect(article()).toHaveAttribute("data-diff-change-id", "repo-a-staged-shared.txt-2");
    expect(article()).toHaveTextContent("MARKER_project-new_repo-a-staged-shared.txt-2");
    api.releaseB();
    for (const pending of api.pendingDiffs) pending.finish();
    await flush();
    expect(article()).toHaveTextContent("MARKER_project-new_repo-a-staged-shared.txt-2");
    expect(article()).not.toHaveTextContent("MARKER_project-old");
  });
});
