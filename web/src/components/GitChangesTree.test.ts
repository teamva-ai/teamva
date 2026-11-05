import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GitTreeRow } from "../lib/gitChanges";
import GitChangesTree from "./GitChangesTree.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("GitChangesTree virtualization", () => {
  it("mounts only the visible window for 10,000 files", () => {
    const rows: GitTreeRow[] = Array.from({ length: 10_000 }, (_, index) => ({
      key: `repo\u0000unstaged\u0000change-${index}\u0000token-${index}`,
      type: "change",
      repositoryId: "repo-1",
      level: 1,
      area: "unstaged",
      change: {
        id: `change-${index}`,
        path_id: `path-${index}`,
        area: "unstaged",
        status: "modified",
        kind: "file",
        path: { token: `token-${index}`, display: `src/file-${index}.ts` },
        binary: false,
        submodule: null,
        diff_available: true,
        additions: 1,
        deletions: 0
      }
    }));

    render(GitChangesTree, {
      props: {
        rows,
        selectedKey: "",
        onToggle: vi.fn(),
        onSelect: vi.fn(),
        onLoadMore: vi.fn(),
        onRetry: vi.fn()
      }
    });

    expect(document.querySelector('[data-virtualized="true"]')).toBeInTheDocument();
    expect(screen.getAllByRole("treeitem").length).toBeLessThan(100);
    expect(screen.getAllByRole("treeitem").length).toBeGreaterThan(0);
  });
});
