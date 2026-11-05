import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { describe, expect, it, vi, afterEach } from "vitest";
import Root from "./Root.svelte";
import { t } from "./lib/i18n.svelte";
import { setComputer } from "./lib/stores.svelte";
import { queryClient } from "./lib/query";

const defaultProject = (computerId: string) => ({
  id: `proj_default_${computerId}`, computer_id: computerId, name: "默认项目",
  is_default: true, version: 1,
  created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z"
});

describe("application root", () => {
  afterEach(() => {
    cleanup();
    queryClient.clear();
    vi.unstubAllGlobals();
    window.history.replaceState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  it("renders the authenticated console after restoring a session", async () => {
    const empty = { items: [], next_cursor: null };
    const computer = {
      id: "cmp_test", name: "测试电脑", version: 1, user_id: "usr_test",
      platform: "darwin", architecture: "arm64", fingerprint: "fp", status: "online",
      computer_use_enabled: false
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string"
        ? input
        : input instanceof URL
          ? input.href
          : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      let data: unknown = empty;
      if (path.endsWith("/auth/local-session")) {
        data = {
          csrf_token: "csrf-test",
          user: {
            id: "usr_test", email: "owner@example.test", display_name: "Owner",
            version: 1, created_at: "2026-08-01T00:00:00Z"
          }
        };
      } else if (path.endsWith("/me/preferences")) {
        data = {
          user_id: "usr_test", locale: "zh-CN", locale_explicit: false, theme: "system",
          allow_write: false, version: 1
        };
      } else if (path.endsWith("/v1/computers")) {
        data = { items: [computer], next_cursor: null };
      } else if (path === "/v1/projects") {
        data = { items: [defaultProject(computer.id)], next_cursor: null };
      } else if (path.endsWith("/computer-use")) {
        data = {
          binary: { installed: false, error: "not installed (test)" },
          platform: "darwin", supported: true
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "req-test" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    render(Root);

    await waitFor(() => {
      expect(screen.getAllByText(t("shell.workbench")).length).toBeGreaterThan(0);
    }, { timeout: 5_000 });
    const projectNavigation = screen.getAllByRole("navigation", { name: t("shell.projectNav") })[0];
    const localNavigation = screen.getByRole("navigation", { name: t("shell.local") });
    expect(projectNavigation).toHaveTextContent(t("shell.issues"));
    expect(projectNavigation).not.toHaveTextContent(t("shell.agents"));
    expect(localNavigation).toHaveTextContent(t("shell.agents"));
    expect(localNavigation).toHaveTextContent(t("shell.skills"));
    expect(localNavigation.querySelector('a[href="/programming-tools"]')).toBeNull();
    expect(Array.from(projectNavigation.querySelectorAll("a"), (link) => link.getAttribute("href"))).toEqual([
      "/workbench", "/issues", "/conversations", "/resources", "/memories", "/schedules", "/changes"
    ]);
    const mobileNavigation = screen.getAllByRole("navigation", { name: t("shell.projectNav") })[1];
    expect(Array.from(mobileNavigation.querySelectorAll("a"), (link) => link.getAttribute("href"))).toEqual([
      "/workbench", "/issues", "/conversations", "/resources", "/memories"
    ]);
  });

  it("uses the managed singleton computer and its real default project", async () => {
    const empty = { items: [], next_cursor: null };
    const computer = {
      id: "cmp_local", name: "本地电脑", version: 1, user_id: "usr_local",
      platform: "darwin", architecture: "arm64", fingerprint: "local-fp", status: "online"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string"
        ? input
        : input instanceof URL
          ? input.href
          : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      let data: unknown = empty;
      if (path.endsWith("/auth/local-session")) {
        data = {
          csrf_token: "csrf-local",
          user: {
            id: "usr_local", email: "local@teamva.invalid", display_name: "本地用户",
            version: 1, created_at: "2026-08-08T00:00:00Z"
          }
        };
      } else if (path.endsWith("/me/preferences")) {
        data = {
          user_id: "usr_local", locale: "zh-CN", locale_explicit: false, theme: "system",
          allow_write: false, version: 1
        };
      } else if (path.endsWith("/v1/computers")) {
        data = { items: [computer], next_cursor: null };
      } else if (path === "/v1/projects") {
        data = { items: [defaultProject(computer.id)], next_cursor: null };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "req-local" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    render(Root);

    expect(await screen.findByRole("heading", { name: "默认项目", level: 1 }, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.getAllByText(computer.name).length).toBeGreaterThan(0);
  });

  it("shows only local display preferences in settings", async () => {
    window.history.replaceState({}, "", "/settings");
    window.dispatchEvent(new PopStateEvent("popstate"));
    const empty = { items: [], next_cursor: null };
    const computer = {
      id: "cmp_test", name: "测试电脑", version: 1, user_id: "usr_test",
      platform: "darwin", architecture: "arm64", fingerprint: "fp", status: "online",
      computer_use_enabled: false
    };
    const preferenceWrites: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string"
        ? input
        : input instanceof URL
          ? input.href
          : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      let data: unknown = empty;
      if (path.endsWith("/auth/local-session")) {
        data = {
          csrf_token: "csrf-test",
          user: {
            id: "usr_test", email: "owner@example.test", display_name: "Owner",
            version: 1, created_at: "2026-08-01T00:00:00Z"
          }
        };
      } else if (path.endsWith("/me/preferences")) {
        const patch = init?.method === "PATCH" ? JSON.parse(String(init.body)) : {};
        if (init?.method === "PATCH") preferenceWrites.push(patch);
        data = {
          user_id: "usr_test", locale: "zh-CN", locale_explicit: false, theme: "system",
          allow_write: false, version: init?.method === "PATCH" ? 2 : 1, ...patch
        };
      } else if (path.endsWith("/v1/computers")) {
        data = { items: [computer], next_cursor: null };
      } else if (path === "/v1/projects") {
        data = { items: [defaultProject(computer.id)], next_cursor: null };
      } else if (path.endsWith("/computer-use")) {
        data = {
          binary: { installed: false, error: "not installed (test)" },
          platform: "darwin", supported: true
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "req-test" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    setComputer("cmp_test");

    render(Root);

    await waitFor(() => {
      expect(screen.getByText(t("settings.personalPreferences"))).toBeInTheDocument();
    }, { timeout: 5_000 }); // Same lazy-console import budget as the root-entry test.
    expect(screen.getByRole("combobox", { name: t("settings.language") })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: t("settings.theme") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.savePreferences") })).not.toBeInTheDocument();
    expect(preferenceWrites).toEqual([]);
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.theme") }), { target: { value: "dark" } });
    await waitFor(() => expect(preferenceWrites).toEqual([{ expected_version: 1, theme: "dark" }]));
    await waitFor(() => expect(screen.getByText(t("settingsAutoSave.saved"))).toBeInTheDocument());
  });
});
