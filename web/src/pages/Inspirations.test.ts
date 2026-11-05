import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import { t } from "../lib/i18n.svelte";
import type { Inspiration } from "../lib/inspirations";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Inspirations from "./Inspirations.svelte";

const initial: Inspiration = {
  id: "inspiration-one", version: 1, title: "新灵感", markdown: "", project_id: "project-test",
  completed: false, completed_at: null, created_at: "2026-11-02T00:00:00Z", updated_at: "2026-11-02T00:00:00Z"
};
function response(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  navigate("/", { replace: true });
});

function setup(options: { pendingSave?: boolean; failSave?: boolean } = {}) {
  navigate("/inspirations", { replace: true });
  let stored = { ...initial };
  const creates: Record<string, unknown>[] = [];
  const patches: Record<string, unknown>[] = [];
  let finish!: () => void;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/v1/inspirations" && init?.method === "POST") {
      creates.push(JSON.parse(String(init.body)));
      return response(stored);
    }
    if (init?.method === "PATCH") {
      const patch = JSON.parse(String(init.body));
      patches.push(patch);
      if (options.pendingSave && patches.length === 1) await new Promise<void>((resolve) => { finish = resolve; });
      if (options.failSave) return new Response(JSON.stringify({ error: { code: "internal_error" } }), { status: 500 });
      stored = { ...stored, ...patch, version: stored.version + 1 };
      return response(stored);
    }
    if (path === `/v1/inspirations/${initial.id}`) return response(stored);
    return response({ items: [] });
  }));
  const view = render(ConsolePageTestHost, { page: Inspirations });
  return { creates, patches, view, finish: () => finish(), rename: () => {
    stored = { ...stored, title: "Automatic inspiration title", version: 2 };
    queryClient.setQueryData(["inspiration-detail", initial.id], stored);
  } };
}
async function createAndEdit() {
  await fireEvent.click(await screen.findByRole("button", { name: t("inspirations.add") }));
  const title = await screen.findByLabelText(t("inspirations.heading"));
  await fireEvent.click(screen.getByRole("button", { name: t("inspirations.source") }));
  const body = await screen.findByRole("textbox", { name: t("inspirations.source") });
  return { title, body };
}

describe("Inspiration automatic naming and editor stability", () => {
  it("creates without a title, then merges an asynchronous title/version without remounting its dirty editor", async () => {
    const request = setup();
    const { title, body } = await createAndEdit();
    expect(request.creates).toEqual([{ project_id: "project-test", markdown: "" }]);
    await fireEvent.input(body, { target: { value: "# Unsaved markdown" } });
    request.rename();
    await waitFor(() => expect(title).toHaveValue("Automatic inspiration title"));
    expect(screen.getByLabelText(t("inspirations.heading"))).toBe(title);
    expect(screen.getByRole("textbox", { name: t("inspirations.source") })).toBe(body);
    expect(body).toHaveValue("# Unsaved markdown");
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(request.patches).toHaveLength(1));
    expect(request.patches[0]).toEqual({ markdown: "# Unsaved markdown", project_id: "project-test", completed: false, expected_version: 2 });
    await waitFor(() => expect(screen.getByRole("button", { name: t("common.save") })).toBeDisabled());
  });

  it("preserves title input during an autosave, including input back to the old default", async () => {
    const request = setup({ pendingSave: true });
    const { title, body } = await createAndEdit();
    await fireEvent.input(body, { target: { value: "First body" } });
    await waitFor(() => expect(request.patches).toHaveLength(1), { timeout: 2000 });
    await fireEvent.input(title, { target: { value: "My draft title" } });
    await fireEvent.input(title, { target: { value: initial.title } });
    await fireEvent.input(body, { target: { value: "Later body" } });
    request.finish();
    await waitFor(() => expect(request.patches).toHaveLength(2));
    expect(request.patches[0]).not.toHaveProperty("title");
    expect(request.patches[1]).toMatchObject({ title: initial.title, markdown: "Later body", expected_version: 2 });
    expect(title).toHaveValue(initial.title);
    expect(body).toHaveValue("Later body");
  });

  it("retains the panel and markdown/title drafts when saving before close fails", async () => {
    const request = setup({ failSave: true });
    const { title, body } = await createAndEdit();
    await fireEvent.input(title, { target: { value: "Keep this manual title" } });
    await fireEvent.input(body, { target: { value: "Keep this markdown" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.close") }));
    await waitFor(() => expect(request.patches).toHaveLength(1));
    await waitFor(() => expect(screen.getAllByRole("alert").length).toBeGreaterThan(0));
    expect(title).toHaveValue("Keep this manual title");
    expect(body).toHaveValue("Keep this markdown");
    expect(screen.getByRole("button", { name: t("common.save") })).toBeEnabled();
  });

  it("does not apply a late selection response after the note deep link changed", async () => {
    navigate("/inspirations?note=old", { replace: true });
    let finish!: (value: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    const requests = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === "/v1/inspirations/old") return pending;
      if (String(input) === "/v1/inspirations/new") return response({ ...initial, id: "new", title: "New selection", markdown: "New content" });
      return response({ items: [] });
    });
    vi.stubGlobal("fetch", requests);
    render(ConsolePageTestHost, { page: Inspirations });
    await waitFor(() => expect(requests.mock.calls.some(([path]) => String(path).endsWith("/old"))).toBe(true));
    navigate("/inspirations?note=new", { replace: true });
    finish(response({ ...initial, id: "old", title: "Old selection", markdown: "Old content" }));
    expect(await screen.findByLabelText(t("inspirations.heading"))).toHaveValue("New selection");
    expect(screen.queryByDisplayValue("Old selection")).not.toBeInTheDocument();
    expect(window.location.search).toBe("?note=new");
  });
});
