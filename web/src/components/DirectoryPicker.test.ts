import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tick } from "svelte";
import { localeInfo, t } from "../lib/i18n.svelte";
import DirectoryPicker from "./DirectoryPicker.svelte";
import DirectoryPickerTestHost from "./DirectoryPickerTestHost.svelte";

const initialLocale = localeInfo.value;

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localeInfo.value = initialLocale;
});

const HOME = "/home/test";
type Entry = { name: string; path: string; is_dir: boolean };
type Result = { status: string; result?: { home?: string; path?: string; items?: Entry[]; error?: string } };
type BrowseRequest = { path: string; show_hidden: boolean; signal?: AbortSignal | null };

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}

function completed(path = HOME, names: string[] = []): Result {
  return { status: "completed", result: { home: HOME, path, items: names.map((name) => ({
    name, path: `${path}/${name}`, is_dir: true
  })) } };
}

function mockBrowser(result: (request: BrowseRequest) => Result | Promise<Result> = (request) => completed(request.path || HOME)) {
  const requests: BrowseRequest[] = [];
  const fetchMock = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "POST") {
      requests.push({ ...JSON.parse(String(init.body)), signal: init.signal });
      return envelope({ command_id: String(requests.length - 1) });
    }
    const id = Number(String(url).split("/").at(-1));
    return envelope(await result(requests[id]));
  });
  vi.stubGlobal("fetch", fetchMock);
  return { requests, fetchMock };
}

async function flush() {
  await vi.advanceTimersByTimeAsync(0);
  await tick();
}

async function advance(ms = 140) {
  await vi.advanceTimersByTimeAsync(ms);
  await tick();
}

const pathInput = () => screen.getByRole("textbox") as HTMLInputElement;
const selectCurrent = () => screen.getByRole("button", { name: t("resources.selectCurrent") });

describe("DirectoryPicker path controls", () => {
  it("reserves real grid space for selection and keeps only the path in the Field context", async () => {
    const onSelect = vi.fn();
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => new Response(JSON.stringify({
      data: init?.method === "POST" ? { command_id: "browse" } : {
        status: "completed", result: { home: "/home/test", path: "/home/test", items: [] }
      }
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    render(DirectoryPickerTestHost, { onSelect });
    const input = screen.getByLabelText("Directory");
    expect(input).toHaveAttribute("id", "directory-path");
    expect(input).toHaveAccessibleDescription("Choose a local directory");
    const hidden = screen.getByRole("checkbox", { name: t("resources.showHidden") });
    expect(hidden).not.toHaveAttribute("id", "directory-path");
    expect(hidden).not.toHaveAttribute("aria-describedby");
    expect(hidden.parentElement?.parentElement).toHaveClass("flex-wrap");
    const select = screen.getByRole("button", { name: t("resources.selectCurrent") });
    expect(select).toHaveClass("am-button");
    expect(select).toHaveAttribute("data-size", "sm");
    expect(select).toHaveAttribute("type", "button");
    expect(select).not.toHaveClass("absolute");
    expect(select.parentElement).toHaveClass("grid-cols-[minmax(0,1fr)_auto]");
    expect(input).not.toHaveClass("pr-36");
    await waitFor(() => expect(select).toBeEnabled());
    await fireEvent.click(select);
    expect(onSelect).toHaveBeenCalledWith("/home/test");
    await fireEvent.click(hidden);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      "/v1/computers/computer-picker-test/directory-list",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ path: "/home/test", show_hidden: true }) })
    ));
  });
});

describe("DirectoryPicker failures and request ownership", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  it.each(["failed", "expired"])("distinguishes %s without rendering backend error details", async (status) => {
    localeInfo.value = "zh-CN";
    mockBrowser(() => ({ status, result: { error: "directory_unavailable: /private/secret-location: permission denied" } }));
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    const expected = status === "failed" ? "directoryPicker.unavailable" : "directoryPicker.expired";
    const other = status === "failed" ? "directoryPicker.expired" : "directoryPicker.unavailable";
    expect(screen.getByText(t(expected))).toBeVisible();
    expect(screen.queryByText(t(other))).not.toBeInTheDocument();
    expect(screen.getByText("目录浏览范围仅限当前用户主目录。")).toBeVisible();
    expect(document.body.textContent).not.toMatch(/secret-location|directory_unavailable|permission denied/);
    expect(selectCurrent()).toBeDisabled();
  });

  it("sanitizes transport errors too", async () => {
    mockBrowser(() => { throw new Error("/private/secret-location: not readable"); });
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    expect(screen.getByText(t("directoryPicker.unavailable"))).toBeVisible();
    expect(document.body.textContent).not.toContain("secret-location");
  });

  it("keeps a real polling timeout distinct from failed and expired", async () => {
    mockBrowser(() => ({ status: "pending" }));
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await advance(10_000);
    expect(screen.getByText(t("resources.pickerTimeout"))).toBeVisible();
    expect(screen.queryByText(t("directoryPicker.expired"))).not.toBeInTheDocument();
  });

  it("retries the failed input directory, preserving both input and the existing selection", async () => {
    const forbidden = "/private/qa/temprepo";
    const onSelect = vi.fn();
    const { requests } = mockBrowser(({ path }) => path === forbidden
      ? { status: "failed", result: { error: "directory_unavailable: /private/backend-detail" } }
      : completed(path || HOME, ["old-entry"]));
    render(DirectoryPicker, { computerId: "computer-picker-test", selectedPath: `${HOME}/chosen`, onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: `${forbidden}/` } });
    expect(selectCurrent()).toBeDisabled();
    await fireEvent.keyDown(pathInput(), { key: "Enter", metaKey: true });
    expect(onSelect).not.toHaveBeenCalled();
    await advance();
    expect(screen.getByText(t("directoryPicker.unavailable"))).toBeVisible();
    await fireEvent.click(screen.getByRole("button", { name: t("common.retry") }));
    await flush();
    expect(requests.map(({ path }) => path)).toEqual(["", forbidden, forbidden]);
    expect(pathInput()).toHaveValue(`${forbidden}/`);
    expect(screen.getByText(`${HOME}/chosen`)).toBeVisible();
    expect(screen.getByText(t("directoryPicker.unavailable"))).toBeVisible();
    expect(screen.queryByRole("button", { name: "old-entry" })).not.toBeInTheDocument();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("retains explicit navigation intent when retrying a failed entry click", async () => {
    let attempts = 0;
    const onSelect = vi.fn();
    const { requests } = mockBrowser(({ path }) => {
      if (!path) return completed(HOME, ["projects"]);
      return ++attempts === 1 ? { status: "failed" } : completed(path);
    });
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.click(screen.getByRole("button", { name: "projects" }));
    await flush();
    expect(screen.getByText(t("directoryPicker.unavailable"))).toBeVisible();
    await fireEvent.click(screen.getByRole("button", { name: t("common.retry") }));
    await flush();
    expect(requests.map(({ path }) => path)).toEqual(["", `${HOME}/projects`, `${HOME}/projects`]);
    expect(pathInput()).toHaveValue("~/projects/");
    await fireEvent.click(selectCurrent());
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/projects`);
  });

  it.each([`${HOME}/new/`, "~/new/"])("late initial home discovery does not overwrite typed %s or enable stale selection", async (value) => {
    const initial = deferred<Result>();
    const onSelect = vi.fn();
    const { requests } = mockBrowser(({ path }) => path === "" ? initial.promise : completed(path, ["fresh"]));
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value } });
    initial.resolve(completed(HOME, ["stale"]));
    await flush();
    expect(pathInput()).toHaveValue(value);
    expect(selectCurrent()).toBeDisabled();
    expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
    await fireEvent.keyDown(pathInput(), { key: "Enter", metaKey: true });
    expect(onSelect).not.toHaveBeenCalled();
    expect(requests).toHaveLength(1);
    await advance();
    expect(requests.map(({ path }) => path)).toEqual(["", `${HOME}/new`]);
    expect(pathInput()).toHaveValue(value);
    expect(screen.getByRole("button", { name: "fresh" })).toBeVisible();
    await fireEvent.click(selectCurrent());
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/new`);
  });

  it("resolves a typed ~/ subdirectory even when home arrives after the debounce fires", async () => {
    const initial = deferred<Result>();
    const { requests } = mockBrowser(({ path }) => path === "" ? initial.promise : completed(path));
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/new/" } });
    await advance();
    expect(requests).toHaveLength(1);
    initial.resolve(completed(HOME, ["old-entry"]));
    await flush();
    expect(requests.map(({ path }) => path)).toEqual(["", `${HOME}/new`]);
    expect(pathInput()).toHaveValue("~/new/");
    expect(selectCurrent()).toBeEnabled();
  });

  it("ignores the initial response even when the newer absolute-path browse has already completed", async () => {
    const initial = deferred<Result>();
    const onSelect = vi.fn();
    mockBrowser(({ path }) => path ? completed(path, ["fresh"]) : initial.promise);
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: `${HOME}/new/` } });
    await advance();
    expect(screen.getByRole("button", { name: "fresh" })).toBeVisible();
    initial.resolve(completed(HOME, ["stale"]));
    await flush();
    expect(pathInput()).toHaveValue(`${HOME}/new/`);
    expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
    await fireEvent.click(selectCurrent());
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/new`);
  });

  it("does not run the old autofocus/caret update after the user starts typing", async () => {
    mockBrowser(() => completed(HOME, ["projects"]));
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    const input = pathInput();
    const focus = vi.spyOn(input, "focus");
    await fireEvent.input(input, { target: { value: "~/pro" } });
    input.setSelectionRange(1, 3);
    await advance(32);
    expect(focus).not.toHaveBeenCalled();
    expect(input.selectionStart).toBe(1);
    expect(input.selectionEnd).toBe(3);
    expect(input).toHaveValue("~/pro");
  });

  it.each(["completed", "failed"])("ignores an old %s response during the next input's debounce window", async (status) => {
    const old = deferred<Result>();
    const onSelect = vi.fn();
    const { requests } = mockBrowser(({ path }) => path === `${HOME}/old` ? old.promise : completed(path || HOME, ["item"]));
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/old/" } });
    await advance();
    await fireEvent.input(pathInput(), { target: { value: "~/new/" } });
    expect(requests[1].signal?.aborted).toBe(true);
    old.resolve(status === "completed" ? completed(`${HOME}/old`, ["stale"]) : { status: "failed" });
    await flush();
    expect(pathInput()).toHaveValue("~/new/");
    expect(selectCurrent()).toBeDisabled();
    expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
    expect(screen.queryByText(t("directoryPicker.unavailable"))).not.toBeInTheDocument();
    await fireEvent.keyDown(pathInput(), { key: "Enter", ctrlKey: true });
    expect(onSelect).not.toHaveBeenCalled();
    await advance();
    await fireEvent.click(selectCurrent());
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/new`);
  });

  it("cancels superseded debounce timers, including when the user returns to the previous directory", async () => {
    const { requests } = mockBrowser();
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/first/" } });
    await advance(100);
    await fireEvent.input(pathInput(), { target: { value: "~/second/" } });
    await advance(100);
    await fireEvent.input(pathInput(), { target: { value: "~/" } });
    await advance(200);
    expect(requests.map(({ path }) => path)).toEqual(["", HOME]);
    expect(pathInput()).toHaveValue("~/");
    expect(selectCurrent()).toBeEnabled();
  });

  it("reloading hidden entries cancels the debounce and uses the typed path, not the old directory", async () => {
    const { requests } = mockBrowser(({ path }) => completed(path || HOME, ["folder"]));
    render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await fireEvent.click(screen.getByRole("button", { name: "folder" }));
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/other/" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: t("resources.showHidden") }));
    await flush();
    await advance(200);
    expect(requests.map(({ path }) => path)).toEqual(["", `${HOME}/folder`, `${HOME}/other`]);
    expect(requests.at(-1)?.show_hidden).toBe(true);
    expect(pathInput()).toHaveValue("~/other/");
  });

  it.each(["metaKey", "ctrlKey"])("keeps HOME child filtering, Enter navigation and %s+Enter selection working", async (modifier) => {
    const onSelect = vi.fn();
    const { requests } = mockBrowser(({ path }) => completed(path || HOME, path ? [] : ["projects", "pictures"]));
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/pro" } });
    expect(screen.getByRole("button", { name: "projects" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "pictures" })).not.toBeInTheDocument();
    expect(selectCurrent()).toBeDisabled();
    await advance(200);
    expect(requests).toHaveLength(1);
    await fireEvent.keyDown(pathInput(), { key: "Enter" });
    await flush();
    expect(pathInput()).toHaveValue("~/projects/");
    await fireEvent.keyDown(pathInput(), { key: "Enter", [modifier]: true });
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/projects`);
  });

  it("selects an exact HOME child with Meta+Enter without navigating", async () => {
    const onSelect = vi.fn();
    const { requests } = mockBrowser(() => completed(HOME, ["projects"]));
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/projects" } });
    await fireEvent.keyDown(pathInput(), { key: "Enter", metaKey: true });
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/projects`);
    expect(requests).toHaveLength(1);
  });

  it("accepts the canonical HOME child result without rewriting a valid typed path", async () => {
    const onSelect = vi.fn();
    mockBrowser(({ path }) => path ? completed(`${HOME}/projects`, ["child"]) : completed());
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/work/../projects/" } });
    await advance();
    expect(pathInput()).toHaveValue("~/work/../projects/");
    expect(screen.getByRole("button", { name: "child" })).toBeVisible();
    await fireEvent.keyDown(pathInput(), { key: "Enter", metaKey: true });
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/projects`);
  });

  it("keeps quick selection working for current entries", async () => {
    const onSelect = vi.fn();
    mockBrowser(() => completed(HOME, ["projects"]));
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.click(screen.getByTitle(t("resources.quickSelect")));
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/projects`);
    expect(pathInput()).toHaveValue("~/projects");
  });

  it("keeps native HOME directory selection working and cancels any pending typed-path load", async () => {
    const onSelect = vi.fn();
    const binding = vi.fn(async () => `${HOME}/native`);
    vi.stubGlobal("go", { main: { desktopApp: { ChooseDirectory: binding } } });
    const { requests } = mockBrowser();
    render(DirectoryPickerTestHost, { onSelect });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/pending/" } });
    await fireEvent.click(screen.getByRole("button", { name: t("resources.chooseInFinder") }));
    await flush();
    await advance(200);
    expect(binding).toHaveBeenCalledOnce();
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(`${HOME}/native`);
    expect(requests).toHaveLength(1);
    expect(pathInput()).toHaveValue("~/native");
    expect(screen.queryByText(t("common.loadingBlock"))).not.toBeInTheDocument();
  });

  it("aborts initial command submission on unmount and does not start polling a late response", async () => {
    const post = deferred<Response>();
    const fetchMock = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) => post.promise);
    vi.stubGlobal("fetch", fetchMock);
    const { unmount } = render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    const signal = fetchMock.mock.calls[0][1]?.signal as AbortSignal;
    unmount();
    expect(signal.aborted).toBe(true);
    post.resolve(envelope({ command_id: "late" }));
    await flush();
    await advance(1000);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("aborts a pending initial poll and ignores its late response after unmount", async () => {
    const initial = deferred<Result>();
    const onSelect = vi.fn();
    const { requests, fetchMock } = mockBrowser(() => initial.promise);
    const { unmount } = render(DirectoryPickerTestHost, { onSelect });
    await flush();
    unmount();
    expect(requests[0].signal?.aborted).toBe(true);
    initial.resolve(completed(HOME, ["late"]));
    await flush();
    await advance(10_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("cancels a debounced load on unmount", async () => {
    const { requests } = mockBrowser();
    const { unmount } = render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/pending/" } });
    unmount();
    await advance(1000);
    expect(requests).toHaveLength(1);
  });

  it("cancels active polling and its sleep timer on unmount", async () => {
    const { requests, fetchMock } = mockBrowser(({ path }) => path ? { status: "pending" } : completed());
    const { unmount } = render(DirectoryPickerTestHost, { onSelect: vi.fn() });
    await flush();
    await fireEvent.input(pathInput(), { target: { value: "~/pending/" } });
    await advance();
    expect(fetchMock).toHaveBeenCalledTimes(4);
    unmount();
    expect(requests[1].signal?.aborted).toBe(true);
    await advance(10_000);
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });
});
