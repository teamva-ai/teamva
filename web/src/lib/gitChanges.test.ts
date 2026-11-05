import { afterEach, describe, expect, it, vi } from "vitest";
import {
  GitRequestGate, buildGitTreeRows, computeVirtualWindow, createStructuredPatchIndex,
  diffFallbackReason, emptyGitRepositoryLoadState,
  fetchGitChanges, fetchGitRepositories, gitDiffLifecycleKey, gitDiffRetainedBytes, mergeGitChanges,
  isGitResourceAuthorizationError, isRecoverableGitSnapshotError,
  isWatchFallbackWarning, requiresLargeDiffConfirmation, resetGitRepositoryCoordinator,
  nextAutomaticDiffPrefetchBytes, patchChangedLineCount, retryGitSnapshotRequest, stackedRenderLimit,
  structuredPatchLineCount, structurePatchLines, structurePatchWindow,
  type GitChange, type GitRepositoriesSnapshot, type GitRepositorySummary
} from "./gitChanges";
import { APIError } from "./api";
import { queryClient } from "./query";

afterEach(() => {
  resetGitRepositoryCoordinator();
  queryClient.clear();
  vi.unstubAllGlobals();
});

function repository(): GitRepositorySummary {
  return {
    id: "repo-1",
    label: "app",
    root_display: "app",
    resource_ids: ["resource-1"],
    discovery_origin: "resource",
    worktree_kind: "main",
    scope: "full",
    branch: { name: "main", detached: false, unborn: false },
    operation: null,
    generation: 3,
    state: "dirty",
    counts: { conflict: 1, staged: 2, unstaged: 1, untracked: 0, total: 4 },
    warnings: []
  };
}

function change(id: string, area: GitChange["area"], path: string): GitChange {
  return {
    id,
    path_id: `path-${path}`,
    area,
    status: "modified",
    kind: "file",
    path: { token: `token-${path}`, display: path, encoding: "utf-8" },
    binary: false,
    submodule: false,
    diff_available: true,
    additions: 1,
    deletions: 1
  };
}

describe("git changes tree helpers", () => {
  it("requests one explicit area with bounded pagination parameters", async () => {
    let requested = new URL("http://teamva.test");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      requested = new URL(raw, "http://teamva.test");
      return new Response(JSON.stringify({
        data: {
          snapshot_id: "snapshot-1", repository_generation: 3, items: [],
          next_cursor: null, truncated: false
        },
        meta: { request_id: "request-1" }
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    await fetchGitChanges("project-1", "repo-1", {
      area: "staged", cursor: "cursor-1", limit: 500, prefix: "", expandUntracked: false
    });

    expect(requested.pathname).toBe("/v1/projects/project-1/git/repositories/repo-1/changes");
    expect(requested.searchParams.get("area")).toBe("staged");
    expect(requested.searchParams.get("cursor")).toBe("cursor-1");
    expect(requested.searchParams.get("limit")).toBe("500");
    expect(requested.searchParams.get("expand_untracked")).toBe("false");
  });

  it("normalizes nullable Git list fields at the HTTP boundary", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      data: {
        snapshot_id: "snapshot-null-lists",
        repositories: [{
          ...repository(),
          resource_ids: null,
          warnings: null
        }],
        truncated: false,
        warnings: null
      },
      meta: { request_id: "request-null-lists" }
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const snapshot = await fetchGitRepositories("project-1");

    expect(snapshot.warnings).toEqual([]);
    expect(snapshot.repositories).toHaveLength(1);
    expect(snapshot.repositories[0].resource_ids).toEqual([]);
    expect(snapshot.repositories[0].warnings).toEqual([]);
  });

  it("shares repository flights and makes superseded empty responses follow the refresh", async () => {
    const fresh = {
      snapshot_id: "snapshot-fresh",
      repositories: [{ ...repository(), warnings: null }],
      truncated: false,
      warnings: null
    };
    const stale = { snapshot_id: "snapshot-stale", repositories: [], truncated: false, warnings: null };
    let resolveInitial!: (value: Response) => void;
    let resolveRefresh!: (value: Response) => void;
    let initialRequests = 0;
    let refreshRequests = 0;
    const envelope = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-coordinator" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      if (url.searchParams.get("refresh") === "true") {
        refreshRequests += 1;
        return new Promise<Response>((resolve) => (resolveRefresh = resolve));
      }
      initialRequests += 1;
      return new Promise<Response>((resolve) => (resolveInitial = resolve));
    }));

    const fromChanges = fetchGitRepositories("project-shared");
    const fromIssues = fetchGitRepositories("project-shared");
    expect(initialRequests).toBe(1);

    const fromSchedules = fetchGitRepositories("project-shared", { refresh: true });
    expect(refreshRequests).toBe(1);
    resolveRefresh(envelope(fresh));
    expect((await fromSchedules).snapshot_id).toBe("snapshot-fresh");

    // The transport ignores abort and settles stale/empty last. Every old
    // consumer must follow the replacement instead of returning that payload.
    resolveInitial(envelope(stale));
    const followed = await Promise.all([fromChanges, fromIssues]);
    expect(followed.map((snapshot) => snapshot.snapshot_id)).toEqual([
      "snapshot-fresh", "snapshot-fresh"
    ]);
    expect(initialRequests).toBe(1);
    expect(queryClient.getQueryData<{ snapshot_id: string }>(
      ["git", "repositories", "project-shared"]
    )?.snapshot_id).toBe("snapshot-fresh");
  });

  it("isolates repository publication and late responses by project", async () => {
    const resolvers = new Map<string, (value: Response) => void>();
    const envelope = (data: unknown) => new Response(JSON.stringify({
      data, meta: { request_id: "request-project-isolation" }
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const projectId = new URL(raw, "http://teamva.test").pathname.split("/").at(-3)!;
      return new Promise<Response>((resolve) => resolvers.set(projectId, resolve));
    }));

    const oldProject = fetchGitRepositories("project-old");
    const newProject = fetchGitRepositories("project-new");
    await vi.waitFor(() => expect(resolvers.size).toBe(2));
    resolvers.get("project-new")!(envelope({
      snapshot_id: "snapshot-new", repositories: [{ ...repository(), id: "repo-new", label: "New" }],
      truncated: false, warnings: []
    }));
    expect((await newProject).snapshot_id).toBe("snapshot-new");
    resolvers.get("project-old")!(envelope({
      snapshot_id: "snapshot-old-empty", repositories: [], truncated: false, warnings: []
    }));
    await oldProject;

    expect(queryClient.getQueryData<GitRepositoriesSnapshot>(
      ["git", "repositories", "project-new"]
    )?.repositories[0]?.id).toBe("repo-new");
    expect(queryClient.getQueryData<GitRepositoriesSnapshot>(
      ["git", "repositories", "project-old"]
    )?.repositories).toEqual([]);
  });

  it("uses the fixed area order and preserves stable API order inside an area", () => {
    const state = {
      ...emptyGitRepositoryLoadState(),
      loaded: true,
      items: [
        change("u-1", "unstaged", "later.ts"),
        change("s-1", "staged", "z.ts"),
        change("c-1", "conflict", "conflict.ts"),
        change("s-2", "staged", "a.ts")
      ]
    };
    const rows = buildGitTreeRows({
      repositories: [repository()],
      states: { "repo-1": state },
      expandedRepositories: { "repo-1": true },
      collapsedAreas: {},
      expandedDirectories: {},
      view: "tree",
      search: "",
      onlyDirty: false
    });

    expect(rows.filter((row) => row.type === "area").map((row) => row.area)).toEqual([
      "conflict", "staged", "unstaged"
    ]);
    expect(rows.filter((row) => row.type === "change").map((row) => row.change?.path.display)).toEqual([
      "conflict.ts", "z.ts", "a.ts", "later.ts"
    ]);
  });

  it("keeps watcher fallback operational without presenting it as a tree error", () => {
    const watchFallback = {
      code: "watch_limit_exceeded",
      message: "Filesystem watching is limited; repository status will be reconciled periodically."
    };
    const unavailable = { code: "status_unavailable", message: "Status unavailable." };
    const item = { ...repository(), warnings: [watchFallback, unavailable] };
    const rows = buildGitTreeRows({
      repositories: [item],
      states: { "repo-1": { ...emptyGitRepositoryLoadState(), loaded: true } },
      expandedRepositories: { "repo-1": true },
      collapsedAreas: {}, expandedDirectories: {}, view: "tree", search: "", onlyDirty: false
    });

    expect(isWatchFallbackWarning(watchFallback)).toBe(true);
    expect(isWatchFallbackWarning(unavailable)).toBe(false);
    expect(rows.filter((row) => row.type === "warning")).toHaveLength(1);
    expect(rows.some((row) => row.warning?.includes("watch"))).toBe(false);
  });

  it("keeps the same path in staged and unstaged areas", () => {
    const staged = change("same", "staged", "src/shared.ts");
    const unstaged = { ...change("same", "unstaged", "src/shared.ts"), path_id: staged.path_id, path: staged.path };
    const merged = mergeGitChanges([], [staged, unstaged]);
    expect(merged).toHaveLength(2);

    const directoryKey = "directory:repo-1:staged:src";
    const rows = buildGitTreeRows({
      repositories: [{ ...repository(), counts: { staged: 1, unstaged: 1, total: 2 } }],
      states: { "repo-1": { ...emptyGitRepositoryLoadState(), loaded: true, items: merged } },
      expandedRepositories: { "repo-1": true },
      collapsedAreas: {},
      expandedDirectories: {
        [directoryKey]: true,
        "directory:repo-1:unstaged:src": true
      },
      view: "tree",
      search: "",
      onlyDirty: false
    });
    expect(rows.filter((row) => row.type === "change").map((row) => row.area)).toEqual(["staged", "unstaged"]);
  });

  it("does not present truncated summary counts as authoritative", () => {
    const rows = buildGitTreeRows({
      repositories: [repository()],
      states: { "repo-1": { ...emptyGitRepositoryLoadState(), loaded: true, items: [] } },
      expandedRepositories: { "repo-1": false }, collapsedAreas: {}, expandedDirectories: {},
      view: "tree", search: "", onlyDirty: false, repositoryListTruncated: true
    });
    expect(rows[0]).toMatchObject({ type: "repository", countsAuthoritative: false });
  });

  it("keeps area pagination and a partial-search row when a match may be on a later page", () => {
    const initial = change("first", "staged", "src/current.ts");
    const state = {
      ...emptyGitRepositoryLoadState(), loaded: true, items: [initial],
      areas: {
        staged: {
          items: [initial], nextCursor: "page-2", truncated: true,
          loaded: true, loading: false, loadingMore: false
        }
      }
    };
    const options = {
      repositories: [repository()], states: { "repo-1": state },
      expandedRepositories: { "repo-1": true }, collapsedAreas: {}, expandedDirectories: {},
      view: "tree" as const, search: "later-match", onlyDirty: false
    };
    const firstRows = buildGitTreeRows(options);
    expect(firstRows.some((row) => row.type === "area" && row.area === "staged")).toBe(true);
    expect(firstRows.some((row) => row.type === "load-more" && row.cursor === "page-2")).toBe(true);
    expect(firstRows.some((row) => row.type === "empty" && row.partialSearch)).toBe(true);

    const later = change("later", "staged", "src/later-match.ts");
    const loadedRows = buildGitTreeRows({
      ...options,
      states: {
        "repo-1": {
          ...state, items: [initial, later],
          areas: { staged: { ...state.areas.staged, items: [initial, later], nextCursor: undefined, truncated: false } }
        }
      }
    });
    expect(loadedRows.some((row) => row.type === "change" && row.change?.id === "later")).toBe(true);
    expect(loadedRows.some((row) => row.type === "empty")).toBe(false);
  });

  it("emits prefix load-more rows for expanded flat untracked directories", () => {
    const directory = {
      ...change("directory", "untracked", "vendor"),
      kind: "directory", path: { token: "prefix-vendor", display: "vendor" }
    };
    const state = {
      ...emptyGitRepositoryLoadState(), loaded: true, items: [directory],
      prefixes: {
        "prefix-vendor": {
          items: [], nextCursor: "prefix-page-2", truncated: true,
          loaded: true, loading: false, loadingMore: false
        }
      }
    };
    const rows = buildGitTreeRows({
      repositories: [{ ...repository(), counts: { untracked: 1 } }],
      states: { "repo-1": state }, expandedRepositories: { "repo-1": true },
      collapsedAreas: {}, expandedDirectories: {
        "directory:repo-1:untracked:prefix-vendor": true
      },
      view: "flat", search: "", onlyDirty: false
    });
    expect(rows.some((row) =>
      row.type === "load-more" && row.prefixToken === "prefix-vendor" && row.cursor === "prefix-page-2"
    )).toBe(true);
  });

  it.each(["tree", "flat"] as const)(
    "keeps a never-loaded lazy untracked directory reachable while searching in %s view",
    (view) => {
      const directory = {
        ...change("directory-never-loaded", "untracked", "vendor"),
        kind: "directory", path: { token: "prefix-vendor", display: "vendor" }
      };
      const rows = buildGitTreeRows({
        repositories: [{ ...repository(), counts: { untracked: 1 } }],
        states: {
          "repo-1": {
            ...emptyGitRepositoryLoadState(), loaded: true, items: [directory],
            areas: {
              untracked: {
                items: [directory], truncated: false, loaded: true,
                loading: false, loadingMore: false
              }
            }
          }
        },
        expandedRepositories: { "repo-1": true }, collapsedAreas: {}, expandedDirectories: {},
        view, search: "needle.ts", onlyDirty: false
      });

      expect(rows.some((row) => row.type === "directory" && row.prefixToken === undefined &&
        row.directoryToken === "prefix-vendor")).toBe(true);
      expect(rows.some((row) =>
        row.type === "load-prefix" && row.prefixToken === "prefix-vendor"
      )).toBe(true);
      expect(rows.some((row) => row.type === "empty" && row.partialSearch)).toBe(true);
    }
  );

  it.each(["tree", "flat"] as const)(
    "keeps prefix pagination authoritative while a %s search has no loaded match",
    (view) => {
      const directory = {
        ...change("directory-paged", "untracked", "vendor"),
        kind: "directory", path: { token: "prefix-vendor", display: "vendor" }
      };
      const loadedChild = change("loaded-child", "untracked", "vendor/already-loaded.ts");
      const rows = buildGitTreeRows({
        repositories: [{ ...repository(), counts: { untracked: 1 } }],
        states: {
          "repo-1": {
            ...emptyGitRepositoryLoadState(), loaded: true, items: [directory],
            areas: {
              untracked: {
                items: [directory], truncated: false, loaded: true,
                loading: false, loadingMore: false
              }
            },
            prefixes: {
              "prefix-vendor": {
                items: [loadedChild], nextCursor: "prefix-next", truncated: true,
                loaded: true, loading: false, loadingMore: false
              }
            }
          }
        },
        expandedRepositories: { "repo-1": true }, collapsedAreas: {}, expandedDirectories: {},
        view, search: "not-loaded-yet.ts", onlyDirty: false
      });

      expect(rows.some((row) =>
        row.type === "load-more" && row.prefixToken === "prefix-vendor" && row.cursor === "prefix-next"
      )).toBe(true);
      expect(rows.some((row) => row.type === "empty" && row.partialSearch)).toBe(true);
    }
  );
});

describe("stacked diff thresholds", () => {
  it.each([
    [0, 0], [1, 1], [6, 6], [7, 6], [12, 6], [13, 4], [25, 4], [26, 2], [100, 2]
  ])("limits %i files to %i mounted cards", (total, expected) => {
    expect(stackedRenderLimit(total)).toBe(expected);
  });
});

describe("large-list and large-diff guards", () => {
  it("computes a 34px row window with 12 rows of overscan", () => {
    expect(computeVirtualWindow(2_000, 3_400, 340, 34, 12)).toEqual({
      start: 88,
      end: 122,
      offset: 2_992,
      totalSize: 68_000
    });
  });

  it("requires confirmation above 500 changed lines and releases after confirmation", () => {
    const diff = {
      confirm_required: false,
      stats: { additions: 300, deletions: 201 },
      patch: ""
    };
    expect(requiresLargeDiffConfirmation(diff, false)).toBe(true);
    expect(requiresLargeDiffConfirmation(diff, true)).toBe(false);
    expect(requiresLargeDiffConfirmation({ ...diff, stats: { additions: 300, deletions: 200 } }, false)).toBe(false);
    expect(requiresLargeDiffConfirmation({ ...diff, confirm_required: true }, false)).toBe(true);
  });

  it("keys diff lifecycle by repository, change area, and path token", () => {
    const first = { repository: repository(), change: change("same", "staged", "src/a.ts") };
    const second = {
      repository: repository(),
      change: { ...first.change, area: "unstaged" as const, path: { ...first.change.path, token: "new-token" } }
    };
    expect(gitDiffLifecycleKey(first)).not.toBe(gitDiffLifecycleKey(second));
  });

  it("indexes a high-line-count fallback compactly and materializes only the visible window", () => {
    const body = Array.from({ length: 100_000 }, (_, index) => index % 2 ? "+x" : "-x").join("\n");
    const patch = `@@ -1,50000 +1,50000 @@\n${body}`;
    const index = createStructuredPatchIndex(patch, 256);
    const total = structuredPatchLineCount(index);
    const visible = structurePatchWindow(patch, index, 90_000, 90_040);

    expect(total).toBe(100_001);
    expect(visible).toHaveLength(40);
    expect(visible[0]).toMatchObject({ kind: "addition", newNumber: 45_000 });
    expect(visible[1]).toMatchObject({ kind: "deletion", oldNumber: 45_001 });
    expect(index.lineStarts.byteLength).toBe((total + 1) * Uint32Array.BYTES_PER_ELEMENT);
    expect(index.checkpointOldNumbers.length).toBeLessThan(400);
    expect(diffFallbackReason(patch, 1_024).reason).toBe("bytes");
  });

  it("treats +++i and ---i as source inside a hunk but file headers as metadata", () => {
    const patch = [
      "diff --git a/example.txt b/example.txt",
      "--- a/example.txt",
      "+++ b/example.txt",
      "@@ -1,2 +1,2 @@",
      "---i",
      "+++i",
      " context"
    ].join("\n");
    expect(patchChangedLineCount(patch)).toBe(2);
    const lines = structurePatchLines(patch);
    expect(lines[1]).toMatchObject({ kind: "metadata" });
    expect(lines[2]).toMatchObject({ kind: "metadata" });
    expect(lines[4]).toMatchObject({ kind: "deletion", oldNumber: 1 });
    expect(lines[5]).toMatchObject({ kind: "addition", newNumber: 1 });
    expect(lines[6]).toMatchObject({ kind: "context", oldNumber: 2, newNumber: 2 });

    const index = createStructuredPatchIndex(patch, 2);
    expect(structurePatchWindow(patch, index, 4, 7)).toEqual(lines.slice(4, 7));
    expect(index.checkpointInHunk.length).toBeGreaterThan(1);
  });

  it("accounts for UTF-16 patch and image payloads in the automatic prefetch budget", () => {
    expect(gitDiffRetainedBytes({
      snapshot_id: "snapshot", comparison: "worktree", kind: "text",
      path: { token: "path", display: "src/a.ts" }, stats: { bytes: 2 },
      patch: "😀".repeat(100), truncated: false, confirm_required: false,
      images: { after: { data_url: "data:image/png;base64," + "a".repeat(200) } }
    })).toBeGreaterThan(1_024 + 400 + 400);
  });

  it("refuses a 25-file automatic prefetch set once its cumulative byte budget is full", () => {
    const response = {
      snapshot_id: "snapshot", comparison: "worktree", kind: "text",
      path: { token: "path", display: "src/a.ts" }, stats: {},
      patch: "x".repeat(1024 * 1024), truncated: false, confirm_required: false
    };
    let retained = 0;
    let accepted = 0;
    for (let index = 0; index < 25; index += 1) {
      const next = nextAutomaticDiffPrefetchBytes(retained, response);
      if (next === undefined) break;
      retained = next;
      accepted += 1;
    }
    expect(accepted).toBe(1);
    expect(retained).toBeLessThanOrEqual(4 * 1024 * 1024);
  });
});

describe("snapshot request recovery and stale response guards", () => {
  it.each([
    [404, "change_not_found"],
    [400, "invalid_cursor"],
    [409, "snapshot_expired"]
  ])("refreshes exactly once for %i %s", async (status, code) => {
    const request = vi.fn()
      .mockRejectedValueOnce(new APIError(status, { error: { code, message: "stale", retryable: true } }))
      .mockResolvedValueOnce("fresh");
    const refresh = vi.fn().mockResolvedValue(undefined);
    await expect(retryGitSnapshotRequest(request, refresh)).resolves.toBe("fresh");
    expect(request).toHaveBeenCalledTimes(2);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it.each([
    [403, "path_out_of_scope"],
    [403, "forbidden"],
    [403, "invalid_scope"],
    [400, "invalid_scope"]
  ])("does not automatically refresh/retry authorization denial %i %s", async (status, code) => {
    const error = new APIError(status, { error: { code, retryable: true } });
    const request = vi.fn().mockRejectedValue(error);
    const refresh = vi.fn();
    expect(isGitResourceAuthorizationError(error)).toBe(true);
    expect(isRecoverableGitSnapshotError(error)).toBe(false);
    await expect(retryGitSnapshotRequest(request, refresh)).rejects.toBe(error);
    expect(request).toHaveBeenCalledOnce();
    expect(refresh).not.toHaveBeenCalled();
  });

  it.each([
    new TypeError("Failed to fetch"),
    new APIError(401, { error: { code: "unauthenticated" } }),
    new APIError(401, { error: { code: "invalid_scope" } }),
    new APIError(403, { error: { code: "csrf_failed" } }),
    new APIError(409, { error: { code: "snapshot_expired" } }),
    new APIError(400, { error: { code: "invalid_cursor" } })
  ])("does not misclassify network/session/CSRF/expired authority as resource authorization: %s", (error) => {
    expect(isGitResourceAuthorizationError(error)).toBe(false);
  });

  it("aborts and rejects an old response token when a newer scope request starts", () => {
    const gate = new GitRequestGate();
    const first = gate.begin("area:staged");
    const parallel = gate.begin("area:unstaged");
    expect(first.isCurrent()).toBe(true);
    expect(parallel.isCurrent()).toBe(true);

    const replacement = gate.begin("area:staged");
    expect(first.signal.aborted).toBe(true);
    expect(first.isCurrent()).toBe(false);
    expect(replacement.isCurrent()).toBe(true);
    expect(parallel.isCurrent()).toBe(true);

    gate.invalidate();
    expect(replacement.signal.aborted).toBe(true);
    expect(parallel.signal.aborted).toBe(true);
  });
});
