import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ProviderExecutableEditorTestHost from "../test/ProviderExecutableEditorTestHost.svelte";

type Executable = { provider: string; path: string; resolved: string; source: string };

function envelope(data: unknown) {
  return new Response(JSON.stringify({ data, meta: { request_id: "req-executable" } }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function mock(initial: Executable, put?: (path: string) => Response | Promise<Response>) {
  const calls: Array<{ method: string; path: string; body: unknown }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path: url.pathname, body });
      if (url.pathname.endsWith("/system/programming-tools") && method === "GET") {
        return envelope({ items: [initial] });
      }
      if (url.pathname.endsWith("/system/programming-tools/opencode") && method === "PUT") {
        const path = (body as { path: string }).path;
        if (put) return put(path);
        return envelope(
          path === ""
            ? { provider: "opencode", path: "", resolved: initial.resolved, source: "bundled" }
            : { provider: "opencode", path, resolved: path, source: "custom" }
        );
      }
      return envelope({ items: [] });
    })
  );
  return calls;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("ProviderExecutableEditor", () => {
  it.each(["Enter", "blur"])("shows the resolved executable and applies a complete path on %s without a Save button", async (trigger) => {
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle/opencode", source: "bundled" });
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });

    expect(await screen.findByText("/bundle/opencode")).toBeInTheDocument();
    expect(
      await screen.findByText(
        t("programmingTools.executableCurrent", { source: t("programmingTools.executableSourceBundled") })
      )
    ).toBeInTheDocument();

    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    expect(screen.queryByRole("button", { name: t("programmingTools.executableSave") })).not.toBeInTheDocument();
    expect(screen.getByText(t("executableAutosave.hint"), { exact: false })).toBeInTheDocument();
    await fireEvent.blur(input);
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);
    await fireEvent.input(input, { target: { value: "/opt" } });
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);
    await fireEvent.input(input, { target: { value: "/opt/tools/opencode" } });
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);
    if (trigger === "Enter") await fireEvent.keyDown(input, { key: "Enter" });
    else await fireEvent.blur(input);

    await waitFor(() => {
      const update = calls.find((call) => call.method === "PUT");
      expect(update?.path).toBe("/v1/system/programming-tools/opencode");
      expect(update?.body).toEqual({ path: "/opt/tools/opencode" });
    });
    expect(await screen.findByText(t("programmingTools.executableSaved"))).toBeInTheDocument();
    await waitFor(() => expect(input).toHaveValue("/opt/tools/opencode"));
    await fireEvent.keyDown(input, { key: "Enter" });
    await fireEvent.blur(input);
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1);
  });

  it("keeps pending input editable and serializes only the latest queued complete value", async () => {
    const deferred: Array<(response: Response) => void> = [];
    const calls = mock({ provider: "opencode", path: "/original", resolved: "/original", source: "custom" }, () => new Promise<Response>((resolve) => deferred.push(resolve)));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toHaveValue("/original"));
    await fireEvent.input(input, { target: { value: "/first" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(deferred).toHaveLength(1));
    expect(input).toBeEnabled();
    expect(screen.getByRole("button", { name: t("programmingTools.executableReset") })).toBeDisabled();
    await fireEvent.input(input, { target: { value: "/second" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await fireEvent.input(input, { target: { value: "/latest complete value" } });
    await fireEvent.blur(input);
    expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([{ path: "/first" }]);
    deferred[0](envelope({ provider: "opencode", path: "/first", resolved: "/first", source: "custom" }));
    await waitFor(() => expect(deferred).toHaveLength(2));
    expect(input).toHaveValue("/latest complete value");
    expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([{ path: "/first" }, { path: "/latest complete value" }]);
    deferred[1](envelope({ provider: "opencode", path: "/latest complete value", resolved: "/latest complete value", source: "custom" }));
    await screen.findByText(t("programmingTools.executableSaved"));
    expect(input).toHaveValue("/latest complete value");
    expect(queryClient.getQueryData(["programming-tool-executables"])).toMatchObject({ items: [{ path: "/latest complete value" }] });
  });

  it("does not overwrite or apply an unsubmitted edit when an older request succeeds", async () => {
    let finish!: (value: Response) => void;
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, () => new Promise<Response>((resolve) => { finish = resolve; }));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/submitted" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    await fireEvent.input(input, { target: { value: "/still/typing" } });
    finish(envelope({ provider: "opencode", path: "/normalized", resolved: "/normalized", source: "custom" }));
    await waitFor(() => expect(screen.queryByText(t("programmingTools.executableSaving"))).not.toBeInTheDocument());
    expect(input).toHaveValue("/still/typing");
    expect(screen.queryByText(t("programmingTools.executableSaved"))).not.toBeInTheDocument();
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1);
  });

  it("adopts normalized server paths only when the submitted draft was not edited again", async () => {
    mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, () => envelope({ provider: "opencode", path: "/normalized", resolved: "/normalized", source: "custom" }));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/original/../normalized" } });
    await fireEvent.blur(input);
    await waitFor(() => expect(input).toHaveValue("/normalized"));
  });

  it("does not queue duplicate Enter/blur submissions while normalizing the same draft", async () => {
    let finish!: (value: Response) => void;
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, () => new Promise<Response>((resolve) => { finish = resolve; }));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/original/../normalized" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    await fireEvent.blur(input);
    finish(envelope({ provider: "opencode", path: "/normalized", resolved: "/normalized", source: "custom" }));
    await waitFor(() => expect(input).toHaveValue("/normalized"));
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1);
  });

  it.each(["Enter", "blur"])("preserves a failed draft and retries it on %s", async (trigger) => {
    let attempts = 0;
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, (path) => ++attempts === 1
      ? new Response(JSON.stringify({ error: { code: "internal_error" } }), { status: 500, headers: { "Content-Type": "application/json" } })
      : envelope({ provider: "opencode", path, resolved: path, source: "custom" }));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/retry-me" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await screen.findByRole("alert");
    expect(input).toHaveValue("/retry-me");
    expect(input).toBeEnabled();
    expect(screen.getByText(t("executableAutosave.retryHint"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("programmingTools.executableSave") })).not.toBeInTheDocument();
    if (trigger === "Enter") await fireEvent.keyDown(input, { key: "Enter" });
    else await fireEvent.blur(input);
    await screen.findByText(t("programmingTools.executableSaved"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([{ path: "/retry-me" }, { path: "/retry-me" }]);
  });

  it("continues with the latest queued edit after a pending request fails", async () => {
    let finish!: (value: Response) => void;
    let attempts = 0;
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, (path) => ++attempts === 1
      ? new Promise<Response>((resolve) => { finish = resolve; })
      : envelope({ provider: "opencode", path, resolved: path, source: "custom" }));
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/first" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    await fireEvent.input(input, { target: { value: "/second" } });
    await fireEvent.blur(input);
    finish(new Response(JSON.stringify({ error: { code: "internal_error" } }), { status: 500, headers: { "Content-Type": "application/json" } }));
    await screen.findByText(t("programmingTools.executableSaved"));
    expect(input).toHaveValue("/second");
    expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([{ path: "/first" }, { path: "/second" }]);
  });

  it("syncs external changes only into a clean draft and never writes on initial load/refetch", async () => {
    const calls = mock({ provider: "opencode", path: "/initial", resolved: "/initial", source: "custom" });
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toHaveValue("/initial"));
    queryClient.setQueryData(["programming-tool-executables"], { items: [{ provider: "opencode", path: "/external", resolved: "/external", source: "custom" }] });
    await waitFor(() => expect(input).toHaveValue("/external"));
    await fireEvent.input(input, { target: { value: "/my-dirty-draft" } });
    queryClient.setQueryData(["programming-tool-executables"], { items: [{ provider: "opencode", path: "/another-external", resolved: "/another-external", source: "custom" }] });
    await waitFor(() => expect(screen.getByText("/another-external")).toBeInTheDocument());
    expect(input).toHaveValue("/my-dirty-draft");
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);
  });

  it("ignores Enter during IME composition", async () => {
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" });
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/still-composing" } });
    await fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);
  });

  it("clears the saved-status timer on unmount", async () => {
    mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" });
    const view = render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    const timeout = vi.spyOn(globalThis, "setTimeout");
    await fireEvent.input(input, { target: { value: "/new" } });
    await fireEvent.blur(input);
    await screen.findByText(t("programmingTools.executableSaved"));
    const timerIndex = timeout.mock.calls.map(([, delay]) => delay).lastIndexOf(2_000);
    expect(timerIndex).toBeGreaterThanOrEqual(0);
    const handle = timeout.mock.results[timerIndex].value;
    const clear = vi.spyOn(globalThis, "clearTimeout");
    view.unmount();
    expect(clear).toHaveBeenCalledWith(handle);
    clear.mockRestore();
    timeout.mockRestore();
  });

  it("does not start a queued write after unmount", async () => {
    let finish!: (value: Response) => void;
    const calls = mock({ provider: "opencode", path: "", resolved: "/bundle", source: "bundled" }, () => new Promise<Response>((resolve) => { finish = resolve; }));
    const view = render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });
    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toBeEnabled());
    await fireEvent.input(input, { target: { value: "/first" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    await fireEvent.input(input, { target: { value: "/queued" } });
    await fireEvent.blur(input);
    view.unmount();
    finish(envelope({ provider: "opencode", path: "/first", resolved: "/first", source: "custom" }));
    await waitFor(() => expect(queryClient.getQueryData(["programming-tool-executables"])).toMatchObject({ items: [{ path: "/first" }] }));
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1);
  });

  it("clears the custom path back to the default resolution", async () => {
    const calls = mock({ provider: "opencode", path: "/opt/tools/opencode", resolved: "/opt/tools/opencode", source: "custom" });
    render(ProviderExecutableEditorTestHost, { provider: "opencode", name: "OpenCode" });

    const input = screen.getByLabelText(`OpenCode ${t("programmingTools.executableLabel")}`);
    await waitFor(() => expect(input).toHaveValue("/opt/tools/opencode"));
    const reset = await screen.findByRole("button", { name: t("programmingTools.executableReset") });
    await fireEvent.click(reset);

    await waitFor(() => {
      const update = calls.find((call) => call.method === "PUT");
      expect(update?.body).toEqual({ path: "" });
    });
    await waitFor(() => expect(input).toHaveValue(""));
  });
});
