import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Root from "../Root.svelte";
import "./ConsoleShell.svelte";
import { applyLocale, t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { clearSession, setComputer, setProject } from "../lib/stores.svelte";
import * as theme from "../lib/theme";
import type { Project } from "../lib/types";

const now = "2026-10-28T00:00:00Z";
const computer = {
  id: "computer-project-test", name: "This Mac", user_id: "user-project-test", version: 1,
  platform: "darwin", architecture: "arm64", fingerprint: "test", status: "online"
};
const user = {
  id: computer.user_id, email: "owner@example.test", display_name: "Owner", status: "active",
  version: 1, created_at: now, updated_at: now
};

function stubProjectAPI() {
  let projects: Project[] = [
    {
      id: "project-default", computer_id: computer.id, name: "默认项目", is_default: true,
      description: "Default description", version: 1, created_at: now, updated_at: now
    },
    {
      id: "project-ordinary", computer_id: computer.id, name: "Ordinary project",
      description: "Ordinary description", version: 1, created_at: now, updated_at: now
    }
  ];
  const writes: { path: string; method: string; body: Record<string, unknown> }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, "http://teamva.test").pathname;
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    let data: unknown = { items: [], next_cursor: null };
    if (path === "/v1/auth/local-session") {
      data = { csrf_token: "csrf-project-test", user };
    } else if (path === "/v1/me/preferences") {
      data = {
        user_id: user.id, locale: "en-US", locale_explicit: true, theme: "light",
        appearance: { palette: "forest", accent: "#246842" }, allow_write: false, version: 1
      };
    } else if (path === "/v1/system/onboarding") {
      data = { needed: false, dismissed: true };
    } else if (path === "/v1/me/feature-tours") {
      data = { seen: ["overview"] };
    } else if (path === "/v1/computers") {
      data = { items: [computer] };
    } else if (path.endsWith("/agent-services")) {
      data = { items: [{ id: "service-header-test", computer_id: computer.id, provider: "codex", provider_version: "9.8.7-header", status: "ready" }] };
    } else if (path === "/v1/projects" && method === "GET") {
      data = { items: projects };
    } else if (path === "/v1/projects" && method === "POST") {
      writes.push({ path, method, body });
      const created: Project = {
        id: "project-created", computer_id: computer.id, name: String(body.name),
        description: String(body.description), version: 1, created_at: now, updated_at: now
      };
      projects = [...projects, created];
      data = created;
    } else if (path.startsWith("/v1/projects/") && method === "PATCH") {
      writes.push({ path, method, body });
      const id = path.split("/").at(-1);
      projects = projects.map((project) => project.id === id ? {
        ...project, name: String(body.name), description: String(body.description), version: project.version + 1
      } : project);
      data = projects.find((project) => project.id === id);
    }
    return new Response(JSON.stringify({ data, meta: { request_id: "project-test" } }), {
      headers: { "Content-Type": "application/json" }
    });
  }));
  return writes;
}

async function startConsole() {
  render(Root);
  await screen.findByRole("heading", { name: "默认项目", level: 1 }, { timeout: 5_000 });
  await waitFor(() => expect(document.documentElement.dataset.palette).toBe("forest"));
}

async function openProjectManager(id: string) {
  await fireEvent.click(screen.getByRole("button", { name: t("shell.selectProject") }));
  const picker = screen.getByRole("dialog", { name: t("shell.selectProject") });
  const row = picker.querySelector(`[data-project-id="${id}"]`)! as HTMLElement;
  await fireEvent.click(within(row).getByRole("button", { name: t("shell.manageProject") }));
  return screen.getByRole("dialog", { name: t("shell.manageProject") });
}

describe("project management without project colors", () => {
  beforeEach(() => {
    queryClient.clear();
    clearSession();
    setComputer(computer.id);
    setProject("");
    applyLocale("en-US");
    window.history.replaceState({}, "", "/workbench");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  afterEach(() => {
    cleanup();
    queryClient.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    clearSession();
    setComputer("");
    setProject("");
    window.history.replaceState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  it("does not render coding CLI names or versions in the header and keeps help next to the brand", async () => {
    stubProjectAPI();
    await startConsole();
    await waitFor(() => expect(queryClient.getQueryData<{ items: unknown[] }>(["agent-services", computer.id])?.items).toHaveLength(1));
    const header = document.querySelector("header")!;
    expect(within(header).queryByLabelText(t("shell.codingEngines"))).not.toBeInTheDocument();
    expect(header).not.toHaveTextContent("Codex");
    expect(header).not.toHaveTextContent("9.8.7-header");
    expect(header).not.toHaveTextContent(t("shell.noReadyAgent"));
    const help = header.querySelector('[data-feature-tour-trigger="overview"]');
    expect(header.firstElementChild).toContainElement(help as HTMLElement);
    expect(header.querySelectorAll("[data-feature-tour-trigger]")).toHaveLength(1);
  });

  it("creates a project with only its name and description, without a palette selector or theme payload", async () => {
    const writes = stubProjectAPI();
    await startConsole();
    const applyTheme = vi.spyOn(theme, "applyTheme");
    await fireEvent.click(screen.getByRole("button", { name: t("shell.selectProject") }));
    const picker = screen.getByRole("dialog", { name: t("shell.selectProject") });
    await fireEvent.click(within(picker).getByRole("button", { name: t("shell.newProject") }));
    const dialog = screen.getByRole("dialog", { name: t("shell.newProject") });
    expect(within(dialog).queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("radio")).not.toBeInTheDocument();
    await fireEvent.input(within(dialog).getByLabelText(t("settings.projectName")), { target: { value: "New project" } });
    await fireEvent.input(within(dialog).getByLabelText(t("settings.description")), { target: { value: "New description" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("settings.create") }));
    await screen.findByRole("heading", { name: "New project", level: 1 });
    expect(writes).toEqual([{
      path: "/v1/projects", method: "POST",
      body: { computer_id: computer.id, name: "New project", description: "New description" }
    }]);
    expect(applyTheme).not.toHaveBeenCalled();
    expect(document.documentElement.dataset.palette).toBe("forest");
  });

  it("keeps default-project protection and saves its editable description without a theme field", async () => {
    const writes = stubProjectAPI();
    await startConsole();
    const applyTheme = vi.spyOn(theme, "applyTheme");
    const dialog = await openProjectManager("project-default");
    expect(within(dialog).getByLabelText(t("settings.projectName"))).toHaveAttribute("readonly");
    expect(within(dialog).getByText(t("defaultProject.hint"))).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: t("settings.deleteProject") })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("radiogroup")).not.toBeInTheDocument();
    await fireEvent.input(within(dialog).getByLabelText(t("settings.description")), { target: { value: "Updated description" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t("shell.manageProject") })).not.toBeInTheDocument());
    expect(writes).toEqual([{
      path: "/v1/projects/project-default", method: "PATCH",
      body: { expected_version: 1, name: "默认项目", description: "Updated description" }
    }]);
    expect(applyTheme).not.toHaveBeenCalled();
  });

  it("does not reapply or change the global appearance when switching projects or cancelling project management", async () => {
    stubProjectAPI();
    await startConsole();
    const root = document.documentElement;
    const appearanceBefore = { style: root.style.cssText, palette: root.dataset.palette, theme: root.dataset.theme };
    const applyTheme = vi.spyOn(theme, "applyTheme");
    await fireEvent.click(screen.getByRole("button", { name: t("shell.selectProject") }));
    const picker = screen.getByRole("dialog", { name: t("shell.selectProject") });
    await fireEvent.click(within(picker).getByRole("button", { name: "Ordinary project" }));
    await screen.findByRole("heading", { name: "Ordinary project", level: 1 });
    const dialog = await openProjectManager("project-ordinary");
    expect(within(dialog).getByLabelText(t("settings.projectName"))).not.toHaveAttribute("readonly");
    expect(within(dialog).queryByRole("radio")).not.toBeInTheDocument();
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.cancel") }));
    await tick();
    expect(applyTheme).not.toHaveBeenCalled();
    expect({ style: root.style.cssText, palette: root.dataset.palette, theme: root.dataset.theme }).toEqual(appearanceBefore);
  });
});
