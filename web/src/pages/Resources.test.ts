import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Resources from "./Resources.svelte";
import ResourcesTestHost from "./ResourcesTestHost.svelte";
import { navigate } from "../lib/router.svelte";
import type { ProjectResource } from "../lib/types";

function json(data: unknown): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: "request-resources" } }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

beforeEach(() => navigate("/resources", { replace: true }));
afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  navigate("/workbench", { replace: true });
});

function resource(id: string, name: string): ProjectResource {
  return {
    id, name, version: 7, project_id: "project-test", kind: "url", status: "ready",
    url: "https://example.test/docs", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z"
  };
}
function deferredResponse() {
  let resolve!: (value: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}
async function fillResource(name: string) {
  await fireEvent.click(screen.getByRole("button", { name: t("resources.addResource") }));
  await fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("resourceKind.url")) }));
  await fireEvent.input(screen.getByLabelText(t("resources.displayName")), { target: { value: name } });
  await fireEvent.input(screen.getByPlaceholderText(t("resources.urlPlaceholder")), { target: { value: "https://example.test/docs" } });
}

describe("URL resource saved names", () => {
  it.each(["", "用户填写的名称"])("shows the saved server name after creation without guessing or replacing it (name=%j)", async (name) => {
    let saved: ProjectResource | undefined;
    const submitted: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/resources") && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        submitted.push(body);
        saved = { ...resource("new-url-resource", body.name || "读取到的网站名称"), version: 1 };
        return json(saved);
      }
      return json({ items: saved ? [saved] : [] });
    }));
    render(ResourcesTestHost);
    await fillResource(name);
    await fireEvent.click(screen.getByRole("button", { name: t("resources.add") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(submitted).toEqual([{ kind: "url", name, url: "https://example.test/docs" }]);
    await waitFor(() => expect(screen.getAllByText(name || "读取到的网站名称")).toHaveLength(2));
  });
});

describe("resource request isolation", () => {
  it.each([["success", "Resource A"], ["failure", "Resource A"], ["success", ""], ["failure", ""]])(
    "does not let a late A %s (submitted name=%j) close, clear, or poison a reopened B dialog", async (outcome, submittedName) => {
    const oldRequest = deferredResponse();
    const posts: string[] = [];
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST" && String(input).endsWith("/resources")) {
        posts.push(String(input));
        return oldRequest.promise;
      }
      return json({ items: [] });
    }));
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    render(ResourcesTestHost);
    await fillResource(submittedName);
    await fireEvent.click(screen.getByRole("button", { name: t("resources.add") }));
    await waitFor(() => expect(posts).toHaveLength(1));
    await fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t("common.cancel") }));
    await fillResource("Resource B");
    oldRequest.resolve(outcome === "success" ? json(resource("resource-a", "Resource A")) : new Response(JSON.stringify({ error: { code: "conflict", message: "Old A failed" } }), { status: 409 }));
    if (outcome === "success") await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["resources", "project-test"] }));
    // Wait for A's callbacks, not just the fetch response, without relying on a timer.
    await waitFor(() => expect(queryClient.getMutationCache().getAll()
      .find((mutation) => (mutation.state.variables as { name?: string })?.name === submittedName)?.state.status)
      .toBe(outcome === "success" ? "success" : "error"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByLabelText(t("resources.displayName"))).toHaveValue("Resource B");
    expect(screen.getByPlaceholderText(t("resources.urlPlaceholder"))).toHaveValue("https://example.test/docs");
    expect(screen.getByRole("button", { name: t("resources.add") })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("refreshes A's project after switching to another project and opening B", async () => {
    const oldRequest = deferredResponse();
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST" && String(input).endsWith("/resources")) {
        posts.push(String(input));
        return oldRequest.promise;
      }
      return json({ items: [] });
    }));
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const view = render(ResourcesTestHost);
    await fillResource("Resource A");
    await fireEvent.click(screen.getByRole("button", { name: t("resources.add") }));
    await waitFor(() => expect(posts).toHaveLength(1));
    await view.rerender({ projectId: "project-other" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await fillResource("Resource B");
    invalidate.mockClear();
    oldRequest.resolve(json(resource("resource-a", "Resource A")));
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["resources", "project-test"] }));
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["resources", "project-other"] });
    expect(posts).toEqual(["/v1/projects/project-test/resources"]);
    expect(screen.getByLabelText(t("resources.displayName"))).toHaveValue("Resource B");
  });
});

describe("resource rename and deep links", () => {
  it.each([0, 1])("renames through the shared PATCH/version contract from action %i", async (actionIndex) => {
    let current = resource("resource-target", "Old name");
    const patches: Array<{ path: string; body: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        const body = JSON.parse(String(init.body));
        patches.push({ path: String(input), body });
        current = { ...current, name: body.name, version: 8 };
        return json(current);
      }
      return json({ items: [current] });
    }));
    render(ResourcesTestHost);
    const edit = await screen.findAllByRole("button", { name: t("common.edit") });
    expect(edit).toHaveLength(2); // mobile and desktop use the same mutation.
    await fireEvent.click(edit[actionIndex]);
    await fireEvent.input(within(screen.getByRole("dialog")).getByRole("textbox", { name: t("resources.name") }), { target: { value: "  New name  " } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(patches).toEqual([{ path: "/v1/projects/project-test/resources/resource-target", body: { name: "New name", expected_version: 7 } }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getAllByText("New name")).toHaveLength(2));
  });

  it("focuses the requested resource instead of the first item", async () => {
    navigate("/resources?resource=resource-target", { replace: true });
    vi.stubGlobal("fetch", vi.fn(async () => json({ items: [resource("resource-first", "First"), resource("resource-target", "Target")] })));
    render(ResourcesTestHost);
    await waitFor(() => expect(document.activeElement).toHaveAttribute("data-resource-id", "resource-target"));
    expect(document.querySelector('[data-resource-id="resource-first"]')).not.toHaveClass("search-target-flash");
    expect(document.querySelector('[data-resource-id="resource-target"]')).toHaveClass("search-target-flash");
  });

  it("does not focus the first resource if the requested ID is absent", async () => {
    navigate("/resources?resource=missing", { replace: true });
    vi.stubGlobal("fetch", vi.fn(async () => json({ items: [resource("resource-first", "First")] })));
    render(ResourcesTestHost);
    expect(await screen.findByText(t("resources.notFound"))).toBeInTheDocument();
    expect(document.activeElement).not.toHaveAttribute("data-resource-id", "resource-first");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("resource type cards", () => {
  function stubResources(): Array<Record<string, unknown>> {
    const created: Array<Record<string, unknown>> = [];
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.pathname.endsWith("/directory-list") && method === "POST") {
          return json({ command_id: "cmd-test" });
        }
        if (url.pathname.endsWith("/directory-list/cmd-test")) {
          return json({
            status: "completed",
            result: { items: [], path: "/Users/tester", home: "/Users/tester" }
          });
        }
        if (url.pathname === "/v1/projects/project-test/resources" && method === "POST") {
          created.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return json({
            id: "resource-test",
            version: 1,
            project_id: "project-test",
            kind: "url",
            name: "Docs",
            status: "ready",
            url: "https://example.test/docs",
            created_at: "2026-09-28T00:00:00Z",
            updated_at: "2026-09-28T00:00:00Z"
          });
        }
        return json({ items: [] });
      })
    );
    return created;
  }

  it("lists every resource type as a card and swaps the form below", async () => {
    stubResources();
    render(ConsolePageTestHost, { page: Resources });
    await fireEvent.click(await screen.findByRole("button", { name: t("resources.addResource") }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();

    const codeCard = screen.getByRole("radio", {
      name: new RegExp(t("resourceKind.code_directory"))
    });
    const documentCard = screen.getByRole("radio", {
      name: new RegExp(t("resourceKind.document_directory"))
    });
    const urlCard = screen.getByRole("radio", { name: new RegExp(t("resourceKind.url")) });
    expect(codeCard).toHaveAttribute("aria-checked", "true");
    expect(documentCard).toHaveAttribute("aria-checked", "false");
    expect(urlCard).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText(t("resources.typeDesc.document_directory"))).toBeInTheDocument();
    expect(screen.getByLabelText(t("resources.pathLabel"))).toBeInTheDocument();
    expect(screen.queryByPlaceholderText(t("resources.urlPlaceholder"))).not.toBeInTheDocument();

    await fireEvent.click(urlCard);
    await waitFor(() => expect(urlCard).toHaveAttribute("aria-checked", "true"));
    expect(screen.getByPlaceholderText(t("resources.urlPlaceholder"))).toBeInTheDocument();
    expect(screen.queryByLabelText(t("resources.pathLabel"))).not.toBeInTheDocument();

    await fireEvent.click(documentCard);
    await waitFor(() => expect(documentCard).toHaveAttribute("aria-checked", "true"));
    expect(screen.getByLabelText(t("resources.pathLabel"))).toBeInTheDocument();
    expect(screen.queryByPlaceholderText(t("resources.urlPlaceholder"))).not.toBeInTheDocument();
  });

  it("submits the form for the selected type", async () => {
    const created = stubResources();
    render(ConsolePageTestHost, { page: Resources });
    await fireEvent.click(await screen.findByRole("button", { name: t("resources.addResource") }));
    await screen.findByRole("dialog");

    await fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("resourceKind.url")) }));
    await fireEvent.input(screen.getByLabelText(t("resources.displayName")), {
      target: { value: "Docs" }
    });
    await fireEvent.input(screen.getByPlaceholderText(t("resources.urlPlaceholder")), {
      target: { value: "https://example.test/docs" }
    });
    await fireEvent.click(screen.getByRole("button", { name: t("resources.add") }));

    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toEqual({
      kind: "url",
      name: "Docs",
      url: "https://example.test/docs"
    });
  });

  it.each(["", "  \t  ", "  用户自定义网站名称  "])("allows an optional website name and submits the user's captured choice (%j)", async (name) => {
    const created = stubResources();
    render(ConsolePageTestHost, { page: Resources });
    await fireEvent.click(await screen.findByRole("button", { name: t("resources.addResource") }));
    await fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("resourceKind.url")) }));
    const nameInput = screen.getByLabelText(t("resources.displayName"));
    expect(nameInput).not.toBeRequired();
    expect(nameInput).toHaveAttribute("placeholder", t("resources.urlNamePlaceholder"));
    expect(nameInput).toHaveAccessibleDescription(t("resources.urlNameHint"));
    expect(screen.getByLabelText(t("resources.urlLabel"))).toHaveAttribute("placeholder", t("resources.urlPlaceholder"));
    const add = screen.getByRole("button", { name: t("resources.add") });
    expect(add).toBeDisabled();
    await fireEvent.input(nameInput, { target: { value: name } });
    await fireEvent.input(screen.getByLabelText(t("resources.urlLabel")), { target: { value: "  https://example.test/docs  " } });
    expect(add).toBeEnabled();
    expect(created).toHaveLength(0); // No lookup or save while the user is still editing.
    await fireEvent.click(add);
    await waitFor(() => expect(created).toEqual([{ kind: "url", name: name.trim(), url: "https://example.test/docs" }]));
  });

  it.each(["code_directory", "document_directory"])("still requires a display name for %s after switching away from URL", async (kind) => {
    const created = stubResources();
    render(ConsolePageTestHost, { page: Resources });
    await fireEvent.click(await screen.findByRole("button", { name: t("resources.addResource") }));
    await fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("resourceKind.url")) }));
    await fireEvent.click(screen.getByRole("radio", { name: new RegExp(t(`resourceKind.${kind}`)) }));
    const nameInput = screen.getByLabelText(t("resources.displayName"));
    expect(nameInput).toBeRequired();
    expect(nameInput).toHaveAttribute("placeholder", t("resources.displayName"));
    expect(screen.queryByText(t("resources.urlNameHint"))).not.toBeInTheDocument();
    const select = screen.getByRole("button", { name: t("resources.selectCurrent") });
    await waitFor(() => expect(select).toBeEnabled());
    await fireEvent.click(select);
    await fireEvent.input(nameInput, { target: { value: "  " } });
    expect(screen.getByRole("button", { name: t("resources.add") })).toBeDisabled();
    await fireEvent.submit(nameInput.closest("form")!);
    expect(created).toHaveLength(0);
  });
});
