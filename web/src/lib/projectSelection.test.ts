import { describe, expect, it } from "vitest";
import type { Project } from "./types";
import { preferredProjectId } from "./projectSelection";

describe("preferredProjectId", () => {
  const projects = [
    { id: "regular", is_default: false },
    { id: "default", is_default: true }
  ];

  it("prefers the default project without an explicit window selection", () => {
    expect(preferredProjectId(projects)).toBe("default");
    expect(preferredProjectId(projects, "")).toBe("default");
  });

  it("falls back to the default when the selected project was deleted", () => {
    expect(preferredProjectId(projects, "deleted")).toBe("default");
  });

  it("keeps a valid explicit project selection", () => {
    expect(preferredProjectId(projects, "regular")).toBe("regular");
    expect(preferredProjectId(projects, "default")).toBe("default");
  });

  it("uses the flag rather than recognizing a project's name", () => {
    const sameNameProjects = [
      { id: "ordinary-same-name", name: "默认项目", is_default: false },
      { id: "actual-default", name: "默认项目", is_default: true }
    ];
    expect(preferredProjectId(sameNameProjects)).toBe("actual-default");
    expect(preferredProjectId(sameNameProjects, "ordinary-same-name")).toBe("ordinary-same-name");
  });

  it("falls back to the first item for fixtures without a default flag", () => {
    const fixtures = [{ id: "first" }, { id: "second" }] as Pick<Project, "id" | "is_default">[];
    expect(preferredProjectId(fixtures)).toBe("first");
    expect(preferredProjectId(fixtures, "deleted")).toBe("first");
    expect(preferredProjectId(fixtures, "second")).toBe("second");
  });

  it("returns an empty selection for an empty list", () => {
    expect(preferredProjectId([])).toBe("");
    expect(preferredProjectId([], "deleted")).toBe("");
  });
});
