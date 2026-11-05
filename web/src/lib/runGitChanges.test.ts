import { cleanup, render, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import RunGitDiffViewer from "../components/RunGitDiffViewer.svelte";
import {
  acquireRunGitTargetMetadata, buildRunGitFileTargets, buildRunGitTreeRows,
  collectUnverifiedProviderReports, fetchAllRunGitChanges, MAX_RUN_GIT_DERIVED_BYTES,
  MAX_RUN_GIT_DERIVED_LINES, MAX_RUN_GIT_RETAINED_BYTES, MAX_RUN_GIT_RETAINED_PATHS,
  parseRunPatchFiles, providerBinaryPaths,
  runGitDiffLifecycleKey, runGitResultRetainedBytes, runHasGitChanges, safeRunPath,
  type GitRunChangesResult
} from "./runGitChanges";
import type { Run } from "./types";

const now = "2026-08-11T10:00:00Z";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function run(id: string, _repositoryId: string, minute: number): Run {
  return {
    id, version: 1, issue_id: "issue", issue_agent_instance_id: "instance",
    project_id: "project", status: "succeeded",
    read_only: false, lease_epoch: 1, purpose: "work", input: [],
    created_at: `2026-08-11T10:${String(minute).padStart(2, "0")}:00Z`,
    updated_at: now
  };
}

function patch(path: string, before = "before", after = "after"): string {
  return [
    `diff --git a/${path} b/${path}`,
    `--- a/${path}`,
    `+++ b/${path}`,
    "@@ -1 +1 @@",
    `-${before}`,
    `+${after}`
  ].join("\n");
}

describe("run git changes", () => {
  it("parses aggregate Provider patches with Pierre", () => {
    const files = parseRunPatchFiles(`${patch("src/a.ts")}\n${patch("src/b.ts")}`, "provider");
    expect(files.map((file) => file.name)).toEqual(["src/a.ts", "src/b.ts"]);
    expect(files.every((file) => file.hunks.length === 1)).toBe(true);
  });

  it("streams binary aggregate sections under the shared path bound", () => {
    const aggregate = Array.from({ length: 5_000 }, (_, index) => {
      const path = index === 1_999 ? "assets/final image.png" : `assets/${index}.bin`;
      const headerPath = path.includes(" ") ? `\"a/${path}\" \"b/${path}\"` : `a/${path} b/${path}`;
      return `diff --git ${headerPath}\nBinary files a/${path} and b/${path} differ`;
    }).join("\n");
    const indexed = providerBinaryPaths(aggregate);
    expect(indexed.size).toBe(MAX_RUN_GIT_RETAINED_PATHS);
    expect(indexed.has("assets/final image.png")).toBe(true);
    expect(indexed.has("assets/4999.bin")).toBe(false);
    expect(indexed.limited).toBe(true);
    expect(indexed.scannedLines).toBeLessThanOrEqual(4_000);
  });

  it("never displays absolute or parent-traversal Provider paths", () => {
    expect(safeRunPath("/Users/private/project/secret.ts")).toBe("secret.ts");
    expect(safeRunPath("file:///Users/private/project/secret.ts")).toBe("secret.ts");
    expect(safeRunPath("../../private/secret.ts")).toBe("secret.ts");
    expect(safeRunPath("\u0001/Users/private/project/secret.ts")).toBe("secret.ts");
  });

  it("keeps Agent reports and start/end observations honestly separate", () => {
    const currentRun = run("run-1", "repo-1", 1);
    const result: GitRunChangesResult = {
      run_id: currentRun.id,
      provider_reports_truncated: false,
      snapshots: [
        {
          repository_id: "repo-1", label: "App", root_display: "App", phase: "start",
          captured_at: "2026-08-11T10:01:01Z", content_hash: "start", truncated: false,
          warnings: [], untracked: [],
          areas: [{ area: "unstaged", patch: patch("src/shared.ts", "base", "start"), paths: ["src/shared.ts"], binary_paths: [], truncated: false }]
        },
        {
          repository_id: "repo-1", label: "App", root_display: "App", phase: "end",
          captured_at: "2026-08-11T10:02:01Z", content_hash: "end", truncated: false,
          warnings: [], untracked: [],
          areas: [{ area: "unstaged", patch: patch("src/shared.ts", "base", "end"), paths: ["src/shared.ts"], binary_paths: [], truncated: false }]
        }
      ],
      provider_reports: [{
        id: "provider-1", seq: 4, occurred_at: "2026-08-11T10:01:30Z",
        source: "workspace.diff", patch: patch("src/shared.ts", "base", "agent"),
        paths: ["src/shared.ts"], paths_verified: true, scope: "repository", truncated: false
      }]
    };
    const targets = buildRunGitFileTargets([result], [currentRun]);
    expect(targets).toHaveLength(3);
    expect(targets.map((target) => target.source)).toEqual(["observation", "provider", "observation"]);
    expect(targets.map((target) => target.phase)).toEqual(["start", undefined, "end"]);
    expect(new Set(targets.map((target) => target.id)).size).toBe(3);
  });

  it("aggregates multiple repositories and keeps ambiguous Provider reports separate", () => {
    const runs = [run("run-a", "repo-a", 1), run("run-b", "repo-b", 2)];
    const results: GitRunChangesResult[] = [
      {
        run_id: "run-a", provider_reports: [], provider_reports_truncated: false, snapshots: [{
          repository_id: "repo-a", label: "Alpha", root_display: "Alpha", phase: "end",
          captured_at: now, content_hash: "a", warnings: [], truncated: false, untracked: [],
          areas: [
            { area: "staged", patch: patch("same.txt", "a", "staged"), paths: ["same.txt"], binary_paths: [], truncated: false },
            { area: "unstaged", patch: patch("same.txt", "staged", "working"), paths: ["same.txt"], binary_paths: [], truncated: false }
          ]
        }]
      },
      {
        run_id: "run-b", snapshots: [], provider_reports_truncated: false, provider_reports: [{
          id: "report-b", seq: 3, occurred_at: now, source: "provider.item.fileChange",
          paths: ["web/b.ts"], paths_verified: true, scope: "repository", truncated: false
        }]
      }
    ];
    const targets = buildRunGitFileTargets(results, runs, [
      { id: "repo-a", label: "Alpha", root_display: "Alpha" },
      { id: "repo-b", label: "Beta", root_display: "Beta" }
    ]);
    expect(targets.filter((target) => target.path === "same.txt").map((target) => target.area)).toEqual(["staged", "unstaged"]);
    expect(new Set(targets.map((target) => target.repository.id))).toEqual(new Set(["repo-a", "reported:run-b"]));
    const rows = buildRunGitTreeRows({ targets, source: "all", search: "", collapsed: {} });
    expect(rows.filter((row) => row.type === "repository").map((row) => row.label)).toEqual(["Alpha", "Repository"]);
    expect(rows.filter((row) => row.type === "area").map((row) => row.area)).toEqual(["staged", "unstaged", "reported"]);
    expect(runHasGitChanges(results[0])).toBe(true);
  });

  it("shows a safe path placeholder when a verified fileChange has no patch", () => {
    const currentRun = run("run-live", "repo-live", 3);
    const result: GitRunChangesResult = {
      run_id: currentRun.id, snapshots: [], provider_reports_truncated: false,
      provider_reports: [{
        id: "report-live", seq: 8, occurred_at: now, source: "provider.item.fileChange",
        paths: ["src/live.ts"], paths_verified: true, scope: "repository", truncated: false
      }]
    };
    const targets = buildRunGitFileTargets([result], [currentRun]);
    expect(targets[0]).toMatchObject({ source: "provider", placeholder: "no_patch", path: "src/live.ts" });
  });

  it("keeps a visible placeholder for a truncated Provider report without paths", () => {
    const currentRun = run("run-truncated", "repo-truncated", 4);
    const result: GitRunChangesResult = {
      run_id: currentRun.id,
      snapshots: [],
      provider_reports_truncated: false,
      provider_reports: [{
        id: "report-truncated", seq: 9, occurred_at: now,
        source: "workspace.diff", paths: [], paths_verified: true, scope: "repository", truncated: true
      }]
    };
    expect(runHasGitChanges(result)).toBe(true);
    expect(buildRunGitFileTargets([result], [currentRun])[0]).toMatchObject({
      source: "provider", path: "workspace.diff", kind: "report",
      placeholder: "truncated", truncated: true
    });
  });

  it("keeps an unparseable explicit Provider report visible without raw JSON", () => {
    const currentRun = run("run-unparseable", "repo-unparseable", 5);
    const result: GitRunChangesResult = {
      run_id: currentRun.id,
      snapshots: [],
      provider_reports_truncated: false,
      provider_reports: [{
        id: "report-unparseable", seq: 11, occurred_at: now,
        source: "provider.item.fileChange", patch: "not a unified patch",
        paths: [], paths_verified: true, scope: "repository", truncated: false
      }]
    };
    expect(buildRunGitFileTargets([result], [currentRun])[0]).toMatchObject({
      path: "provider.item.fileChange", kind: "report", placeholder: "no_patch"
    });
  });

  it("isolates unverified reports from repositories and renderable targets", () => {
    const currentRun = run("run-unverified", "repo-private", 6);
    const result: GitRunChangesResult = {
      run_id: currentRun.id, snapshots: [], provider_reports_truncated: false,
      provider_reports: [{
        id: "unverified-1", seq: 12, occurred_at: now, source: "workspace.diff",
        patch: patch("secret.ts"), paths: ["secret.ts"], paths_verified: false,
        scope: "unverified", truncated: false
      }]
    };
    expect(buildRunGitFileTargets([result], [currentRun])).toEqual([]);
    expect(collectUnverifiedProviderReports([result], [currentRun])).toEqual([
      expect.objectContaining({ id: "unverified-1", displayNames: ["secret.ts"] })
    ]);
  });

  it("resets Run diff lifecycle when the target path/area changes", () => {
    const currentRun = run("run-target", "repo-target", 7);
    const base: GitRunChangesResult = {
      run_id: currentRun.id, snapshots: [], provider_reports_truncated: false,
      provider_reports: [{
        id: "report-target", seq: 1, occurred_at: now, source: "workspace.diff",
        paths: ["src/one.ts"], paths_verified: true, scope: "repository", truncated: false
      }]
    };
    const first = buildRunGitFileTargets([base], [currentRun])[0];
    const second = { ...first, path: "src/two.ts", area: "unstaged" };
    expect(runGitDiffLifecycleKey(first)).not.toBe(runGitDiffLifecycleKey(second));
  });

  it("follows 101+ Provider reports using the opaque scan cursor without duplicates", async () => {
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      requests.push(url.searchParams.get("after_provider_seq") ?? "");
      const second = url.searchParams.get("after_provider_seq") === "scan-500";
      const indexes = second ? [99, 100, 101] : Array.from({ length: 100 }, (_, index) => index);
      const data: GitRunChangesResult = {
        run_id: "run-pages", snapshots: [],
        provider_reports: indexes.map((index) => ({
          id: `report-${index}`, seq: index + 1, occurred_at: now,
          source: "provider.item.fileChange", paths: [`src/${index}.ts`],
          paths_verified: true, scope: "repository", truncated: false
        })),
        provider_reports_truncated: !second,
        next_provider_cursor: second ? undefined : "scan-500"
      };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-pages" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    const result = await fetchAllRunGitChanges("run-pages");
    expect(requests).toEqual(["", "scan-500"]);
    expect(result.provider_reports).toHaveLength(102);
    expect(new Set(result.provider_reports.map((report) => report.id)).size).toBe(102);
    expect(result.provider_reports_truncated).toBe(false);
  });

  it("merges started/completed reports across pages by stable report ID and keeps the latest sequence/status", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const second = Boolean(new URL(raw, "http://teamva.test").searchParams.get("after_provider_seq"));
      const data: GitRunChangesResult = {
        run_id: "run-report-lifecycle", snapshots: [],
        provider_reports: [second ? {
          id: "event-completed", report_id: "report-stable", seq: 102,
          occurred_at: "2026-08-11T10:02:00Z", source: "provider.item.fileChange",
          provider_turn_id: "turn-1", paths: [], paths_verified: false,
          scope: "unverified", status: "completed", truncated: false
        } : {
          id: "event-started", report_id: "report-stable", seq: 99,
          occurred_at: "2026-08-11T10:01:00Z", source: "provider.item.fileChange",
          provider_turn_id: "turn-1", patch: patch("src/lifecycle.ts"),
          paths: ["src/lifecycle.ts"], paths_verified: true,
          scope: "repository", status: "started", truncated: false
        }],
        provider_reports_truncated: !second,
        next_provider_cursor: second ? undefined : "scan-100"
      };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-lifecycle" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    const result = await fetchAllRunGitChanges("run-report-lifecycle");
    expect(result.provider_reports).toHaveLength(1);
    expect(result.provider_reports[0]).toMatchObject({
      id: "report-stable", report_id: "report-stable", seq: 102,
      status: "completed", paths: ["src/lifecycle.ts"], scope: "repository"
    });
    expect(result.provider_reports[0].patch).toContain("src/lifecycle.ts");
  });

  it("merges by Provider turn/source item without collapsing independent file changes", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const second = Boolean(new URL(raw, "http://teamva.test").searchParams.get("after_provider_seq"));
      const reports = second ? [{
        id: "event-a-completed", seq: 3, occurred_at: "2026-08-11T10:03:00Z",
        source: "provider.item.fileChange", provider_turn_id: "turn-shared",
        source_item_id: "item-a", paths: [], paths_verified: false,
        scope: "unverified", status: "completed", truncated: false
      }] : [
        {
          id: "event-a-started", seq: 1, occurred_at: "2026-08-11T10:01:00Z",
          source: "provider.item.fileChange", provider_turn_id: "turn-shared",
          source_item_id: "item-a", paths: ["src/a.ts"], paths_verified: true,
          scope: "repository", status: "started", truncated: false
        },
        {
          id: "event-b-started", seq: 2, occurred_at: "2026-08-11T10:02:00Z",
          source: "provider.item.fileChange", provider_turn_id: "turn-shared",
          source_item_id: "item-b", paths: ["src/b.ts"], paths_verified: true,
          scope: "repository", status: "started", truncated: false
        }
      ];
      const data: GitRunChangesResult = {
        run_id: "run-items", snapshots: [], provider_reports: reports,
        provider_reports_truncated: !second,
        next_provider_cursor: second ? undefined : "scan-2"
      };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-items" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    const result = await fetchAllRunGitChanges("run-items");
    expect(result.provider_reports).toHaveLength(2);
    expect(result.provider_reports.find((report) => report.source_item_id === "item-a"))
      .toMatchObject({ seq: 3, status: "completed", paths: ["src/a.ts"] });
    expect(result.provider_reports.find((report) => report.source_item_id === "item-b"))
      .toMatchObject({ seq: 2, status: "started", paths: ["src/b.ts"] });
  });

  it("stops Provider pagination when the retained patch budget is reached", async () => {
    const requests: string[] = [];
    const largePatch = "x".repeat(1_500_000);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const cursor = new URL(raw, "http://teamva.test").searchParams.get("after_provider_seq") ?? "";
      requests.push(cursor);
      const page = cursor ? 2 : 1;
      const data: GitRunChangesResult = {
        run_id: "run-budget", snapshots: [], provider_reports_truncated: true,
        next_provider_cursor: `scan-${page}`,
        provider_reports: [{
          id: `report-${page}`, seq: page, occurred_at: now, source: "workspace.diff",
          patch: largePatch, paths: [`src/${page}.ts`], paths_verified: true,
          scope: "repository", truncated: false
        }]
      };
      return new Response(JSON.stringify({ data, meta: { request_id: `request-${page}` } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    const result = await fetchAllRunGitChanges("run-budget");
    expect(requests).toEqual(["", "scan-1"]);
    expect(result.provider_reports).toHaveLength(2);
    expect(result.provider_reports[0].patch).toBe(largePatch);
    expect(result.provider_reports[1]).toMatchObject({ patch: undefined, truncated: true });
    expect(result.provider_reports_truncated).toBe(true);
    expect(result.next_provider_cursor).toBe("scan-2");
    expect(runGitResultRetainedBytes(result)).toBeLessThanOrEqual(MAX_RUN_GIT_RETAINED_BYTES);
  });

  it("indexes at most 2000 lightweight targets across two 4000-file patches and mounts bounded metadata", async () => {
    const currentRun = run("run-many", "repo-many", 8);
    const aggregate = (start: number) => Array.from({ length: 2_000 }, (_, index) => {
      const pathName = `src/file-${start + index}.ts`;
      return patch(pathName);
    }).join("\n");
    const result: GitRunChangesResult = {
      run_id: currentRun.id,
      snapshots: [],
      provider_reports_truncated: false,
      provider_reports: [0, 2_000].map((start, index) => ({
        id: `report-${index}`, seq: index + 1, occurred_at: now,
        source: "workspace.diff", patch: aggregate(start), paths: [],
        paths_verified: true, scope: "repository", truncated: false
      }))
    };

    const targets = buildRunGitFileTargets([result], [currentRun]);
    expect(targets).toHaveLength(MAX_RUN_GIT_RETAINED_PATHS);
    expect(targets.buildLimited).toBe(true);
    expect(targets.retainedPathCount).toBe(MAX_RUN_GIT_RETAINED_PATHS);
    expect(targets.every((target) => target.metadata === undefined)).toBe(true);
    expect(targets.every((target) => target.patchSection?.patch === result.provider_reports[0].patch)).toBe(true);

    // Stacked mode mounts at most two cards for this target count. Those two
    // leases share one derived-byte/line budget and release it on unmount.
    const leases = targets.slice(0, 2).map(acquireRunGitTargetMetadata);
    expect(leases.filter((lease) => lease.metadata).length).toBe(2);
    expect(targets.derivedBudget.usedBytes).toBeLessThanOrEqual(MAX_RUN_GIT_DERIVED_BYTES);
    expect(targets.derivedBudget.usedLines).toBeLessThanOrEqual(MAX_RUN_GIT_DERIVED_LINES);
    for (const lease of leases) lease.release();
    expect(targets.derivedBudget.usedBytes).toBe(0);
    expect(targets.derivedBudget.usedLines).toBe(0);

    vi.stubGlobal("IntersectionObserver", undefined);
    const viewer = render(RunGitDiffViewer, {
      props: { target: targets[0], targets, mode: "stacked" }
    });
    await waitFor(() => expect(
      viewer.container.querySelectorAll('[data-metadata-mounted="true"]').length
    ).toBe(2));
    expect(targets.derivedBudget.usedBytes).toBeLessThanOrEqual(MAX_RUN_GIT_DERIVED_BYTES);
    expect(targets.derivedBudget.usedLines).toBeLessThanOrEqual(MAX_RUN_GIT_DERIVED_LINES);
    viewer.unmount();
    expect(targets.derivedBudget.usedBytes).toBe(0);
    expect(targets.derivedBudget.usedLines).toBe(0);
  });

  it("refuses Pierre derivation for one section with too many short lines without retaining metadata", () => {
    const currentRun = run("run-lines", "repo-lines", 9);
    const manyLines = Array.from({ length: MAX_RUN_GIT_DERIVED_LINES + 1 }, () => " x").join("\n");
    const hugeSection = [
      "diff --git a/src/lines.ts b/src/lines.ts",
      "--- a/src/lines.ts", "+++ b/src/lines.ts",
      `@@ -1,${MAX_RUN_GIT_DERIVED_LINES + 1} +1,${MAX_RUN_GIT_DERIVED_LINES + 1} @@`,
      manyLines
    ].join("\n");
    const result: GitRunChangesResult = {
      run_id: currentRun.id, snapshots: [], provider_reports_truncated: false,
      provider_reports: [{
        id: "many-lines", seq: 1, occurred_at: now, source: "workspace.diff",
        patch: hugeSection, paths: [], paths_verified: true, scope: "repository", truncated: false
      }]
    };
    const targets = buildRunGitFileTargets([result], [currentRun]);
    const lease = acquireRunGitTargetMetadata(targets[0]);
    expect(lease.metadata).toBeUndefined();
    expect(lease.limited).toBe(true);
    expect(targets.derivedBudget.usedBytes).toBe(0);
  });
});
