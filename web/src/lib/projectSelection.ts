import type { Project } from "./types";

export function preferredProjectId(
  projects: readonly Pick<Project, "id" | "is_default">[],
  selectedId?: string
): string {
  if (selectedId && projects.some((project) => project.id === selectedId)) return selectedId;
  return projects.find((project) => project.is_default)?.id ?? projects[0]?.id ?? "";
}
