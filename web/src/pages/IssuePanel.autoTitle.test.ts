import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { t } from "../lib/i18n.svelte";
import type { Issue } from "../lib/types";
import IssuePanelTestHost from "./IssuePanelTestHost.svelte";

afterEach(() => { cleanup(); clearComposerDrafts(); queryClient.clear(); vi.unstubAllGlobals(); });

describe("Issue manual title ownership", () => {
  const issue: Issue = { id: "issue-title", project_id: "project-test", version: 1, title: "新任务", description: "",
    status: "open", worktree_hint: false, created_by_user_id: "user-test", created_at: "2026-11-02T00:00:00Z", updated_at: "2026-11-02T00:00:00Z" };
  function setup() {
    const patches: Record<string, unknown>[] = [];
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      let data: unknown = { items: [] };
      if (init?.method === "PATCH") { const body = JSON.parse(String(init.body)); patches.push(body); data = { ...issue, ...body, version: 2 }; }
      return new Response(JSON.stringify({ data, meta: {} }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    return { host: render(IssuePanelTestHost, { repositories: [], issue }), patches };
  }

  it("persists explicit editing back to the same default title", async () => {
    const { patches } = setup();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.editTitle") }));
    const input = screen.getByRole("textbox", { name: t("issues.titleField") });
    await fireEvent.input(input, { target: { value: "用户标题" } });
    await fireEvent.input(input, { target: { value: issue.title } });
    await fireEvent.blur(input);
    await waitFor(() => expect(patches).toEqual([{ title: issue.title, expected_version: 1 }]));
  });

  it("keeps a typed title through automatic refresh and uses the latest version", async () => {
    const { host, patches } = setup();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.editTitle") }));
    const input = screen.getByRole("textbox", { name: t("issues.titleField") });
    await fireEvent.input(input, { target: { value: "用户正在输入" } });
    await host.rerender({ repositories: [], issue: { ...issue, version: 2, title: "后台生成的标题" } });
    expect(input).toHaveValue("用户正在输入");
    await fireEvent.blur(input);
    await waitFor(() => expect(patches).toEqual([{ title: "用户正在输入", expected_version: 2 }]));
  });

  it("does not overwrite a newly generated title after opening and leaving an untouched editor", async () => {
    const { host, patches } = setup();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.editTitle") }));
    const input = screen.getByRole("textbox", { name: t("issues.titleField") });
    await host.rerender({ repositories: [], issue: { ...issue, version: 2, title: "后台生成的标题" } });
    await fireEvent.blur(input);
    expect(patches).toEqual([]);
    expect(screen.getByRole("heading", { name: "后台生成的标题" })).toBeInTheDocument();
  });
});
