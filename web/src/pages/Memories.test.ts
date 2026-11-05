import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { location, navigate } from "../lib/router.svelte";
import { confirmProjectChange } from "../lib/unsavedChanges.svelte";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Memories from "./Memories.svelte";
import { deepLinkTranslations } from "../locales/deepLink";

const original = {
  id: "memory-1", project_id: "project-test", key: "testing", kind: "fact", source_type: "user",
  title: "Memory one", content: "Original content", version: 1,
  created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:00Z"
};
function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function setup(options: { failSave?: boolean; failArchive?: boolean } = {}) {
  navigate("/memories", { replace: true });
  let stored = { ...original };
  let finishSave: (() => void) | undefined;
  const bodies: Record<string, unknown>[] = [];
  const archives: Record<string, unknown>[] = [];
  let archived = false;
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/projects/project-other/")) return envelope({ items: [{ ...original,
      id: "memory-other", project_id: "project-other", title: "Other project", content: "Other content" }] });
    if (init?.method === "PATCH") {
      const body = JSON.parse(String(init.body));
      bodies.push(body);
      await new Promise<void>((resolve) => { finishSave = resolve; });
      if (options.failSave) return new Response(JSON.stringify({ error: { code: "version_conflict", message: "Save conflict" } }), { status: 409 });
      stored = { ...stored, ...(body.title === undefined ? {} : { title: body.title }), content: body.content, version: stored.version + 1 };
      return envelope(stored);
    }
    if (url.endsWith("/archive")) {
      archives.push(JSON.parse(String(init?.body)));
      if (options.failArchive) return new Response(JSON.stringify({ error: { code: "conflict", message: "Archive conflict" } }), { status: 409 });
      archived = true;
      return envelope({});
    }
    if (url.includes("/versions")) return envelope({ items: [{ id: "version-1", version: 1, title: "History record", created_at: original.created_at }] });
    return envelope({ items: archived ? [] : [stored] });
  }));
  const view = render(ConsolePageTestHost, { page: Memories });
  return { bodies, archives, view, finish: () => finishSave!() };
}
afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("Memory editor refresh stability", () => {
  it("omits an untouched title and still sends explicit input intent when typed back to the default", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Body only" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    expect(request.bodies[0]).not.toHaveProperty("title");
    request.finish();
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
    const title = screen.getByLabelText(t("memories.title"));
    await fireEvent.input(title, { target: { value: "Temporary title" } });
    await fireEvent.input(title, { target: { value: original.title } });
    expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled();
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    expect(request.bodies[1]).toMatchObject({ title: original.title, expected_version: 2 });
    request.finish();
  });

  it.each([false, true])("merges background title/version into a dirty memory without replacing manual title intent (%s)", async (manual) => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    const title = screen.getByLabelText(t("memories.title"));
    await fireEvent.input(content, { target: { value: "Unsaved content survives" } });
    if (manual) await fireEvent.input(title, { target: { value: "My manual title" } });
    queryClient.setQueryData(["memories", "project-test", false], { items: [{ ...original, title: "Automatic title", version: 2 }] });
    await waitFor(() => expect(title).toHaveValue(manual ? "My manual title" : "Automatic title"));
    expect(screen.getByLabelText(t("memories.content"))).toBe(content);
    expect(content).toHaveValue("Unsaved content survives");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    expect(request.bodies[0]).toMatchObject({ expected_version: 2, content: "Unsaved content survives" });
    if (manual) expect(request.bodies[0]).toHaveProperty("title", "My manual title");
    else expect(request.bodies[0]).not.toHaveProperty("title");
    request.finish();
  });

  it.each([false, true])("only retries a 409 proven to be a title-only change (body conflict: %s)", async (bodyConflict) => {
    navigate("/memories", { replace: true });
    let stored = { ...original };
    const bodies: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        const body = JSON.parse(String(init.body));
        bodies.push(body);
        if (bodies.length === 1) {
          stored = { ...stored, title: "Automatic title", content: bodyConflict ? "Concurrent body" : original.content, version: 2 };
          return new Response(JSON.stringify({ error: { code: "version_conflict" } }), { status: 409 });
        }
        stored = { ...stored, ...body, version: stored.version + 1 };
        return envelope(stored);
      }
      if (String(input).endsWith(`/memories/${original.id}`)) return envelope(stored);
      return envelope({ items: [stored] });
    }));
    render(ConsolePageTestHost, { page: Memories });
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Protected local body" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    if (bodyConflict) {
      await screen.findByRole("alert");
      expect(content).toHaveValue("Protected local body");
      expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled();
      expect(bodies).toHaveLength(1);
    } else {
      await waitFor(() => expect(bodies).toHaveLength(2));
      expect(bodies[1]).toEqual({ content: "Protected local body", expected_version: 2 });
      await waitFor(() => expect(screen.getByLabelText(t("memories.title"))).toHaveValue("Automatic title"));
      expect(content).toHaveValue("Protected local body");
      expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled();
    }
  });

  it("keeps title input made during an outstanding content save", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Submitted content" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
    const title = screen.getByLabelText(t("memories.title"));
    await fireEvent.input(title, { target: { value: "Typed while saving" } });
    request.finish();
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled());
    expect(title).toHaveValue("Typed while saving");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    expect(request.bodies[1]).toMatchObject({ title: "Typed while saving", expected_version: 2 });
    request.finish();
  });

  it("keeps history open when the selected memory refreshes", async () => {
    setup();
    await screen.findByDisplayValue(original.content);
    await fireEvent.click(screen.getByRole("button", { name: t("memories.history") }));
    await screen.findByText("v1 · History record");
    queryClient.setQueryData(["memories", "project-test", false], { items: [{ ...original, version: 2 }] });
    await waitFor(() => expect(screen.getByText(t("memories.versionInfo", { key: original.key, version: 2, source: t("issues.you") }))).toBeInTheDocument());
    expect(screen.getByText("v1 · History record")).toBeInTheDocument();
  });

  it("preserves typing made during save and uses the saved version for the next save", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "First draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
    await fireEvent.input(content, { target: { value: "Still typing after save" } });
    request.finish();
    await waitFor(() => expect(queryClient.getQueryData(["memories", "project-test", false])).toMatchObject({ items: [{ version: 2 }] }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled());
    expect(content).toHaveValue("Still typing after save");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
    expect(request.bodies[1]).toMatchObject({ content: "Still typing after save", expected_version: 2 });
    request.finish();
    await waitFor(() => expect(queryClient.getQueryData(["memories", "project-test", false])).toMatchObject({ items: [{ version: 3 }] }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
  });

  it("does not discard a draft when the already selected list item is clicked", async () => {
    setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Unsaved draft" } });
    await fireEvent.click(screen.getByRole("button", { name: /Memory one/ }));
    expect(content).toHaveValue("Unsaved draft");
    expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled();
  });

  it("keeps a dirty editor's baseline when another client updates the memory", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Local draft" } });
    queryClient.setQueryData(["memories", "project-test", false], { items: [{ ...original, content: "External edit", version: 2 }] });
    await waitFor(() => expect(screen.getByText(t("memories.versionInfo", { key: original.key, version: 2, source: t("issues.you") }))).toBeInTheDocument());
    expect(content).toHaveValue("Local draft");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    // Only our own successful save may advance a dirty draft's version;
    // otherwise the server must retain its opportunity to reject a conflict.
    expect(request.bodies[0]).toMatchObject({ expected_version: 1 });
    request.finish();
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeDisabled());
  });

  it("does not apply an old save's baseline to another selected memory", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "First memory draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.save") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    queryClient.setQueryData(["memories", "project-test", false], { items: [original, { ...original, id: "memory-2", title: "Memory two", content: "Second memory" }] });
    await fireEvent.click(await screen.findByRole("button", { name: /Memory two/ }));
    await fireEvent.click(screen.getByRole("button", { name: t("common.confirm") }));
    await waitFor(() => expect(content).toHaveValue("Second memory"));
    await fireEvent.input(content, { target: { value: "Second memory draft" } });
    // Keep the same two-item list when onSuccess invalidates it.
    vi.mocked(fetch).mockImplementationOnce(async () => envelope({ items: [original, { ...original, id: "memory-2", title: "Memory two", content: "Second memory" }] }));
    request.finish();
    await waitFor(() => expect(queryClient.getMutationCache().getAll().at(-1)?.state.status).toBe("success"));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.save") })).toBeEnabled());
    expect(content).toHaveValue("Second memory draft");
  });

  it("protects a dirty memory from routes, reloads and same-route project switches", async () => {
    setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Keep this draft" } });
    navigate("/agents");
    expect(location.path).toBe("/memories");
    expect(confirmProjectChange("other-project")).toBe(false);
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    expect(content).toHaveValue("Keep this draft");
    confirm.mockReturnValue(true);
    navigate("/agents");
    expect(location.path).toBe("/agents");
  });

  it("requires an explicit save/discard/cancel choice before archiving a dirty memory", async () => {
    const request = setup();
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Unsaved draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("button", { name: t("memories.saveAndArchive") })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("common.discardChanges") })).toBeInTheDocument();
    expect(request.archives).toHaveLength(0);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.cancel") }));
    expect(content).toHaveValue("Unsaved draft");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    await fireEvent.click(screen.getByRole("button", { name: t("common.discardChanges") }));
    await waitFor(() => expect(request.archives).toHaveLength(1));
    expect(request.bodies).toHaveLength(0);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("does not archive newer typing that arrives during save-and-archive", async () => {
    const request = setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Submitted draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    await fireEvent.click(screen.getByRole("button", { name: t("memories.saveAndArchive") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.saveAndArchive") })).toBeDisabled());
    await fireEvent.input(content, { target: { value: "Newer typing" } });
    request.finish();
    await waitFor(() => expect(screen.getByRole("button", { name: t("memories.saveAndArchive") })).toBeEnabled());
    expect(request.archives).toHaveLength(0);
    expect(content).toHaveValue("Newer typing");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.saveAndArchive") }));
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    expect(request.bodies[1]).toMatchObject({ expected_version: 2, content: "Newer typing" });
    request.finish();
    await waitFor(() => expect(request.archives).toHaveLength(1));
    expect(request.archives[0]).toMatchObject({ expected_version: 3 });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(confirm).not.toHaveBeenCalled();
  });

  it("keeps the archive choice and draft recoverable after a save conflict", async () => {
    const request = setup({ failSave: true });
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Conflicted draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    await fireEvent.click(screen.getByRole("button", { name: t("memories.saveAndArchive") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    request.finish();
    await within(screen.getByRole("dialog")).findByText(t("errors.codes.version_conflict"));
    expect(content).toHaveValue("Conflicted draft");
    expect(request.archives).toHaveLength(0);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("does not throw away the draft if explicitly discarding and archiving fails", async () => {
    const request = setup({ failArchive: true });
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Recoverable draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    await fireEvent.click(screen.getByRole("button", { name: t("common.discardChanges") }));
    await within(screen.getByRole("dialog")).findByText(/Archive conflict/);
    expect(request.archives).toHaveLength(1);
    expect(content).toHaveValue("Recoverable draft");
  });

  it("fences save-and-archive when a confirmed project change replaces the editor", async () => {
    const request = setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Old project draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.archive") }));
    await fireEvent.click(screen.getByRole("button", { name: t("memories.saveAndArchive") }));
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    expect(confirmProjectChange("project-other")).toBe(true);
    await request.view.rerender({ page: Memories, projectId: "project-other" });
    const nextContent = await screen.findByDisplayValue("Other content");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await fireEvent.input(nextContent, { target: { value: "New project draft" } });
    request.finish();
    await waitFor(() => expect(queryClient.getQueryData(["memories", "project-test", false])).toMatchObject({ items: [{ version: 2 }] }));
    expect(nextContent).toHaveValue("New project draft");
    expect(request.archives).toHaveLength(0);
  });

  it("also guards a new-memory draft and does not silently replace an existing dirty editor", async () => {
    setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Existing draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(content).toHaveValue("Existing draft");
    confirm.mockReturnValue(true);
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    const dialog = screen.getByRole("dialog");
    const contentDraft = within(dialog).getByLabelText(t("memories.content"));
    await fireEvent.input(contentDraft, { target: { value: "New memory draft" } });
    confirm.mockReturnValue(false);
    expect(confirmProjectChange("other-project")).toBe(false);
    navigate("/agents");
    expect(location.path).toBe("/memories");
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.cancel") }));
    expect(contentDraft).toHaveValue("New memory draft");
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
  });
});

describe("Memory creation with automatic keys", () => {
  const generatedKey = /^memory-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

  function creationAPI(failures = 0) {
    navigate("/memories", { replace: true });
    const entries: typeof original[] = [];
    const bodies: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === "POST" && String(input).endsWith("/memories")) {
        const body = JSON.parse(String(init.body));
        bodies.push(body);
        if (failures-- > 0) return new Response(JSON.stringify({ error: { code: "internal_error", retryable: true } }), { status: 500 });
        const entry = { ...original, ...body, id: `created-${bodies.length}` };
        entries.push(entry);
        return envelope(entry);
      }
      return envelope({ items: entries });
    }));
    render(ConsolePageTestHost, { page: Memories });
    return { entries, bodies };
  }

  async function openCreation() {
    await fireEvent.click(await screen.findByRole("button", { name: t("memories.newMemory") }));
    return screen.findByRole("dialog", { name: t("memories.createTitle") });
  }
  async function fillCreation(dialog: HTMLElement) {
    await fireEvent.input(within(dialog).getByLabelText(t("memories.content")), { target: { value: "Remember the project decisions." } });
  }

  it("shows only type and content, and closing an untouched generated-key draft never asks to discard", async () => {
    const request = creationAPI();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const dialog = await openCreation();
    expect(within(dialog).getAllByRole("textbox")).toHaveLength(1);
    expect(within(dialog).getAllByRole("combobox")).toHaveLength(1);
    expect(within(dialog).getByLabelText(t("memories.kind"))).toHaveFocus();
    expect(dialog.querySelectorAll("input")).toHaveLength(0);
    expect(within(dialog).queryByLabelText(t("memories.title"))).not.toBeInTheDocument();
    expect(dialog.querySelector('input[type="hidden"]')).toBeNull();
    expect(dialog).not.toHaveTextContent(/unique\s*key|唯一\s*key|memory-[0-9a-f]{8}-/i);
    expect(within(dialog).getByLabelText(t("memories.kind"))).toBeInTheDocument();
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.cancel") }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(confirm).not.toHaveBeenCalled();
    expect(request.bodies).toEqual([]);
  });

  it("creates without a key field and generates a different UUID for each new draft even with duplicate titles", async () => {
    const request = creationAPI();
    for (let index = 0; index < 2; index++) {
      const dialog = await openCreation();
      await fillCreation(dialog);
      await fireEvent.change(within(dialog).getByLabelText(t("memories.kind")), { target: { value: "decision" } });
      await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(request.bodies).toHaveLength(index + 1);
      expect(request.bodies[index]).toMatchObject({ kind: "decision", content: "Remember the project decisions.", source_type: "user" });
      expect(request.bodies[index]).not.toHaveProperty("title");
      expect(request.bodies[index].key).toMatch(generatedKey);
      expect(request.entries[index].key).toBe(request.bodies[index].key);
    }
    expect(request.bodies[0].key).not.toBe(request.bodies[1].key);
  });

  it("keeps the same hidden key and visible draft after a failed creation and retry", async () => {
    const request = creationAPI(1);
    const dialog = await openCreation();
    await fillCreation(dialog);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await within(dialog).findByRole("alert");
    expect(within(dialog).queryByLabelText(t("memories.title"))).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText(t("memories.content"))).toHaveValue("Remember the project decisions.");
    expect(request.bodies[0].key).toMatch(generatedKey);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(request.bodies).toHaveLength(2);
    expect(request.bodies[1]).toEqual(request.bodies[0]);
    expect(request.entries).toHaveLength(1);
  });

  it("discards a cancelled failed draft's generated key before starting the next one", async () => {
    const request = creationAPI(1);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let dialog = await openCreation();
    await fillCreation(dialog);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await within(dialog).findByRole("alert");
    const abandoned = request.bodies[0].key;
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.cancel") }));
    dialog = await openCreation();
    expect(within(dialog).getByLabelText(t("memories.content"))).toHaveValue("");
    await fillCreation(dialog);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(request.bodies[1].key).toMatch(generatedKey);
    expect(request.bodies[1].key).not.toBe(abandoned);
  });
});

describe("Memory deep-link selection safety", () => {
  const second = { ...original, id: "memory-2", key: "second", title: "Memory two", content: "Second content" };
  const archived = { ...original, id: "memory-archived", title: "Archived memory", content: "Archived content" };
  const foreign = { ...original, id: "memory-foreign", project_id: "project-other", title: "Foreign memory", content: "Private content" };
  const text = deepLinkTranslations["en-US"];

  function selectionAPI(options: { list?: Promise<Response>; created?: typeof original } = {}) {
    let activeItems = [original, second];
    const requests = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input), "http://teamva.test");
      if (url.pathname.endsWith("/versions")) return envelope({ items: [{ id: "version-1", version: 1, title: "History record", created_at: original.created_at }] });
      if (init?.method === "POST" && options.created) {
        activeItems = [...activeItems, options.created];
        return envelope(options.created);
      }
      if (url.pathname === "/v1/projects/project-other/memories") return envelope({ items: [foreign] });
      if (url.searchParams.get("archived") === "true") return envelope({ items: [archived] });
      return options.list ?? envelope({ items: activeItems });
    });
    vi.stubGlobal("fetch", requests);
    return requests;
  }

  it.each(["", "missing", "deleted", "memory-foreign"])("does not open the first memory for explicit unavailable ID %s", async (id) => {
    const requests = selectionAPI();
    navigate(`/memories?memory=${id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("memories.save") })).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(original.content)).not.toBeInTheDocument();
    expect(requests.mock.calls.every(([, init]) => init?.method === "GET")).toBe(true);
  });

  it("waits for the list before reporting absence and selects the exact non-first target", async () => {
    let resolve!: (value: Response) => void;
    const list = new Promise<Response>((complete) => { resolve = complete; });
    selectionAPI({ list });
    navigate(`/memories?memory=${second.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
    resolve(envelope({ items: [original, second] }));
    expect(await screen.findByDisplayValue(second.content)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("allows a manual selection to replace an invalid link and survives a list refresh", async () => {
    selectionAPI();
    navigate("/memories?memory=missing", { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    await screen.findByRole("alert");
    await fireEvent.click(screen.getByRole("button", { name: /Memory two/ }));
    expect(await screen.findByDisplayValue(second.content)).toBeInTheDocument();
    queryClient.setQueryData(["memories", "project-test", false], { items: [original, second] });
    await waitFor(() => expect(screen.getByLabelText(t("memories.content"))).toHaveValue(second.content));
    expect(location.search).toBe(`?memory=${second.id}`);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("tracks explicit valid and invalid query changes rather than retaining another editor", async () => {
    selectionAPI();
    navigate(`/memories?memory=${second.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    await screen.findByDisplayValue(second.content);
    navigate("/memories?memory=missing", { replace: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
    navigate(`/memories?memory=${original.id}`, { replace: true });
    expect(await screen.findByDisplayValue(original.content)).toBeInTheDocument();
  });

  it("retains a dirty editor if a query change is rejected, then shows absence after confirmed discard", async () => {
    selectionAPI();
    navigate(`/memories?memory=${original.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    const content = await screen.findByDisplayValue(original.content);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await fireEvent.input(content, { target: { value: "Keep deep-link draft" } });
    navigate("/memories?memory=missing");
    expect(content).toHaveValue("Keep deep-link draft");
    expect(location.search).toBe(`?memory=${original.id}`);
    confirm.mockReturnValue(true);
    navigate("/memories?memory=missing");
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
  });

  it("rejects a link in the wrong archive view and keeps a valid archived memory readonly", async () => {
    selectionAPI();
    navigate(`/memories?memory=${archived.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailableHint);
    navigate(`/memories?archived=true&memory=${archived.id}`, { replace: true });
    expect(await screen.findByDisplayValue(archived.content)).toHaveAttribute("readonly");
    expect(screen.getByLabelText(t("memories.title"))).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: t("memories.save") })).not.toBeInTheDocument();
    navigate(`/memories?archived=true&memory=${original.id}`, { replace: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
  });

  it("does not replace a deleted selected memory with another list item", async () => {
    selectionAPI();
    navigate(`/memories?memory=${original.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    await screen.findByDisplayValue(original.content);
    queryClient.setQueryData(["memories", "project-test", false], { items: [second] });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
  });

  it("keeps discard protection if the deep-linked memory is deleted while its draft is dirty", async () => {
    selectionAPI();
    navigate(`/memories?memory=${original.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    const content = await screen.findByDisplayValue(original.content);
    await fireEvent.input(content, { target: { value: "Protected deleted-memory draft" } });
    queryClient.setQueryData(["memories", "project-test", false], { items: [second] });
    await screen.findByRole("alert");
    vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(confirmProjectChange("project-other")).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: /Memory two/ }));
    await fireEvent.click(screen.getByRole("button", { name: t("common.cancel") }));
    expect(location.search).toBe(`?memory=${original.id}`);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
    expect(confirmProjectChange("project-other")).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: /Memory two/ }));
    await fireEvent.click(screen.getByRole("button", { name: t("common.confirm") }));
    expect(await screen.findByDisplayValue(second.content)).toBeInTheDocument();
  });

  it("clears history and never falls back to another project's first memory", async () => {
    selectionAPI();
    navigate(`/memories?memory=${original.id}`, { replace: true });
    const view = render(ConsolePageTestHost, { page: Memories });
    await screen.findByDisplayValue(original.content);
    await fireEvent.click(screen.getByRole("button", { name: t("memories.history") }));
    await screen.findByText("v1 · History record");
    await view.rerender({ page: Memories, projectId: "project-other" });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.memoryUnavailable);
    expect(screen.queryByLabelText(t("memories.content"))).not.toBeInTheDocument();
    expect(screen.queryByText("v1 · History record")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(foreign.content)).not.toBeInTheDocument();
  });

  it("keeps a manual selection in the displayed project when the URL still names the previous project", async () => {
    selectionAPI();
    navigate(`/memories?project=project-test&memory=${original.id}`, { replace: true });
    const view = render(ConsolePageTestHost, { page: Memories });
    await screen.findByDisplayValue(original.content);
    await view.rerender({ page: Memories, projectId: "project-other" });
    await screen.findByRole("alert");
    await fireEvent.click(screen.getByRole("button", { name: /Foreign memory/ }));
    expect(await screen.findByDisplayValue(foreign.content)).toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("project")).toBe("project-other");
    expect(new URLSearchParams(location.search).get("memory")).toBe(foreign.id);
  });

  it("selects a newly created memory from an invalid link without an old-query fallback", async () => {
    const created = { ...original, id: "created", key: "created-key", title: "New exact memory", content: "New exact content" };
    selectionAPI({ created });
    navigate("/memories?memory=missing", { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    await screen.findByRole("button", { name: t("memories.newMemory") });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    const dialog = screen.getByRole("dialog");
    await fireEvent.input(within(dialog).getByLabelText(t("memories.content")), { target: { value: created.content } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByDisplayValue(created.content)).toBeInTheDocument();
    expect(location.search).toBe(`?memory=${created.id}`);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not let an old restore response replace a newer archived selection", async () => {
    const anotherArchived = { ...archived, id: "archived-other", title: "Another archived", content: "Another archived content" };
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    const requests = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith("/restore")) return pending;
      return envelope({ items: String(input).includes("archived=true") ? [archived, anotherArchived] : [original, archived] });
    });
    vi.stubGlobal("fetch", requests);
    navigate(`/memories?archived=true&memory=${archived.id}`, { replace: true });
    render(ConsolePageTestHost, { page: Memories });
    await screen.findByDisplayValue(archived.content);
    await fireEvent.click(screen.getByRole("button", { name: t("memories.restore") }));
    await waitFor(() => expect(requests.mock.calls.some(([url]) => String(url).endsWith("/restore"))).toBe(true));
    await fireEvent.click(screen.getByRole("button", { name: /Another archived/ }));
    await screen.findByDisplayValue(anotherArchived.content);
    finish(envelope(archived));
    await waitFor(() => expect(queryClient.getMutationCache().getAll().at(-1)?.state.status).toBe("success"));
    expect(screen.getByLabelText(t("memories.content"))).toHaveValue(anotherArchived.content);
    expect(screen.getByLabelText(t("memories.content"))).toHaveAttribute("readonly");
    expect(new URLSearchParams(location.search).get("memory")).toBe(anotherArchived.id);
    expect(new URLSearchParams(location.search).get("archived")).toBe("true");
  });

  it("fences an old creation response and clears its modal draft after a project switch", async () => {
    const created = { ...original, id: "old-created", title: "Old creation", content: "Old creation content" };
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    const requests = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === "POST") return pending;
      return envelope({ items: String(input).includes("/project-other/") ? [foreign] : [original, created] });
    });
    vi.stubGlobal("fetch", requests);
    navigate("/memories?memory=missing", { replace: true });
    const view = render(ConsolePageTestHost, { page: Memories });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    const dialog = screen.getByRole("dialog");
    await fireEvent.input(within(dialog).getByLabelText(t("memories.content")), { target: { value: created.content } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await waitFor(() => expect(requests.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    expect(confirmProjectChange("project-other")).toBe(true);
    await view.rerender({ page: Memories, projectId: "project-other" });
    navigate("/memories", { replace: true });
    await screen.findByDisplayValue(foreign.content);
    finish(envelope(created));
    await waitFor(() => expect(queryClient.getMutationCache().getAll().at(-1)?.state.status).toBe("success"));
    expect(screen.getByLabelText(t("memories.content"))).toHaveValue(foreign.content);
    expect(location.search).toBe("");
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    expect(within(screen.getByRole("dialog")).getByLabelText(t("memories.content"))).toHaveValue("");
    expect(within(screen.getByRole("dialog")).getAllByRole("textbox")).toHaveLength(1);
  });

  it("still selects a new memory if the initial list loads during its creation", async () => {
    const created = { ...original, id: "created", key: "created-key", title: "New exact memory", content: "New exact content" };
    let finishList!: (response: Response) => void;
    let finishCreate!: (response: Response) => void;
    const list = new Promise<Response>((resolve) => { finishList = resolve; });
    const creation = new Promise<Response>((resolve) => { finishCreate = resolve; });
    let firstList = true;
    const requests = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      if (init?.method === "POST") return creation;
      if (firstList) { firstList = false; return list; }
      return envelope({ items: [original, created] });
    });
    vi.stubGlobal("fetch", requests);
    navigate("/memories", { replace: true });
    const view = render(ConsolePageTestHost, { page: Memories });
    await fireEvent.click(screen.getByRole("button", { name: t("memories.newMemory") }));
    const dialog = screen.getByRole("dialog");
    await fireEvent.input(within(dialog).getByLabelText(t("memories.content")), { target: { value: created.content } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("memories.create") }));
    await waitFor(() => expect(requests.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true));
    finishList(envelope({ items: [original] }));
    await waitFor(() => expect(queryClient.getQueryState(["memories", "project-test", false])?.status).toBe("success"));
    await view.rerender({ page: Memories, projectName: "Refreshed project label" });
    finishCreate(envelope(created));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByDisplayValue(created.content)).toBeInTheDocument();
    expect(location.search).toBe(`?memory=${created.id}`);
  });
});
