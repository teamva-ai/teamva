import { describe, expect, it } from "vitest";
import { gitChangeSelectionIntent, restoreGitChangeSelection, type GitChange } from "./gitChanges";

const file = (id: string, display = "src/shared.txt", area = "staged"): GitChange => ({
  id, path_id: `path-${id}`, area, status: "modified", kind: "file", path: { token: `token-${id}`, display },
  binary: false, submodule: false, diff_available: true
});

describe("Git change selection is intent, not retained authority", () => {
  it("stores no old change/path token and resolves only to fresh authority", () => {
    const old = file("old");
    const intent = gitChangeSelectionIntent("project", "repo", old);
    expect(JSON.stringify(intent)).not.toContain(old.path.token);
    expect(JSON.stringify(intent)).not.toContain(old.id);
    const fresh = file("fresh");
    expect(restoreGitChangeSelection(intent, "project", "repo", [fresh])).toBe(fresh);
  });

  it("isolates full paths, areas, repositories, projects and path encodings", () => {
    const intent = gitChangeSelectionIntent("project-a", "repo-b", file("old"));
    expect(restoreGitChangeSelection(intent, "project-b", "repo-b", [file("fresh")])).toBeUndefined();
    expect(restoreGitChangeSelection(intent, "project-a", "repo-a", [file("fresh")])).toBeUndefined();
    expect(restoreGitChangeSelection(intent, "project-a", "repo-b", [file("fresh", "other/shared.txt")])).toBeUndefined();
    expect(restoreGitChangeSelection(intent, "project-a", "repo-b", [file("fresh", "src/shared.txt", "unstaged")])).toBeUndefined();
    const encoded = file("encoded");
    encoded.path.encoding = "escaped";
    expect(restoreGitChangeSelection(intent, "project-a", "repo-b", [encoded])).toBeUndefined();
  });

  it("does not guess a fresh path from ambiguous display names or a directory", () => {
    const intent = gitChangeSelectionIntent("project", "repo", file("old"));
    expect(restoreGitChangeSelection(intent, "project", "repo", [file("first"), file("second")])).toBeUndefined();
    expect(restoreGitChangeSelection(intent, "project", "repo", [{ ...file("directory"), kind: "directory" }])).toBeUndefined();
  });
});
