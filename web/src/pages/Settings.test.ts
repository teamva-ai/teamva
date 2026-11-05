import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyLocale, localeInfo, SUPPORTED_LOCALES, t } from "../lib/i18n.svelte";
import { appearanceTranslations } from "../locales/appearance";
import { settingsAutoSaveTranslations } from "../locales/settingsAutoSave";
import { preferences } from "../lib/stores.svelte";
import { APIError } from "../lib/api";
import { applyTheme } from "../lib/theme";
import { queryClient } from "../lib/query";
import { location, navigate } from "../lib/router.svelte";
import type { NetworkAccess, UserPreferences, UserPreferencesPatch } from "../lib/types";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Settings from "./Settings.svelte";

function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function mockFetch(initial: Partial<NetworkAccess> = {}) {
  const finishes: Array<() => void> = [];
  const bodies: Array<{ enabled: boolean; password: string }> = [];
  let failNext = false;
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    if (String(input).includes("/computer-use")) return envelope({ binary: { installed: false } });
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body));
      bodies.push(body);
      if (failNext) { failNext = false; throw new Error("Network save failed"); }
      await new Promise<void>((resolve) => { finishes.push(resolve); });
      return envelope({ port: 41720, external_access: body.enabled, external_urls: [], password_set: true, can_manage: true, listen_error: false });
    }
    return envelope({ port: 41720, external_access: false, external_urls: [], password_set: false, can_manage: true, listen_error: false, ...initial });
  }));
  return { bodies, finish: () => finishes.shift()!(), fail: () => { failNext = true; } };
}
function savedPreferences(patch: UserPreferencesPatch, version = 2): UserPreferences {
  return { user_id: "user-test", locale: "en-US", locale_explicit: true, theme: "light", allow_write: false, version, ...patch };
}
function palette(name: string) {
  return within(screen.getByRole("radiogroup", { name: t("appearance.title") })).getByRole("radio", { name: t(`appearance.${name}`) });
}
const preferenceStatus = () => document.querySelector("[data-preferences-autosave]")! as HTMLElement;
const accessStatus = () => document.querySelector("[data-access-autosave]")! as HTMLElement;
beforeEach(() => {
  applyLocale("en-US");
  preferences.appearance = { palette: "default" };
  applyTheme("light", { palette: "default" });
  window.history.replaceState({}, "", "/settings");
  location.path = "/settings";
  location.search = "";
});
afterEach(async () => { cleanup(); await Promise.resolve(); queryClient.clear(); vi.unstubAllGlobals(); });

describe("Settings remote access automatic apply", () => {
  it("keeps shared password controls and eye focus/visibility changes do not submit", async () => {
    const request = mockFetch();
    render(ConsolePageTestHost, { page: Settings });
    const password = await screen.findByLabelText(t("settings.networkAccessPassword"), { selector: "input" });
    const show = screen.getByRole("button", { name: t("settings.networkAccessShow") });
    expect(password).toHaveClass("am-input");
    expect(password).toHaveAttribute("data-size", "md");
    expect(show).toHaveClass("am-button");
    expect(show).toHaveAttribute("data-size", "md");
    expect(show).toHaveAttribute("data-icon", "true");
    expect(show).toHaveAttribute("type", "button");
    expect(show.closest("label")).toBeNull();
    expect(password.closest(".am-field")?.querySelector("label")).toHaveAttribute("for", password.id);
    await fireEvent.input(password, { target: { value: "test-only-draft" } });
    const pointer = new MouseEvent("pointerdown", { bubbles: true, cancelable: true });
    show.dispatchEvent(pointer);
    expect(pointer.defaultPrevented).toBe(true);
    await fireEvent.focusOut(password, { relatedTarget: show });
    await fireEvent.click(show);
    expect(password).toHaveAttribute("type", "text");
    const hide = screen.getByRole("button", { name: t("settings.networkAccessHide") });
    await fireEvent.focusOut(hide, { relatedTarget: password });
    await fireEvent.click(hide);
    expect(password).toHaveAttribute("type", "password");
    expect(password).toHaveValue("test-only-draft");
    expect(request.bodies).toEqual([]);
  });

  it("first enable waits for a valid password completed with Enter; blur does not repeat the credential", async () => {
    const request = mockFetch();
    render(ConsolePageTestHost, { page: Settings });
    const toggle = await screen.findByRole("switch", { name: t("settings.networkAccessToggle") });
    expect(toggle).not.toBeChecked();
    await fireEvent.click(toggle);
    const password = screen.getByLabelText(t("settings.networkAccessPassword"), { selector: "input" });
    await fireEvent.input(password, { target: { value: "short" } });
    await fireEvent.keyDown(password, { key: "Enter" });
    expect(screen.getByText(t("settingsAutoSave.passwordInvalid"))).toBeInTheDocument();
    expect(request.bodies).toEqual([]);
    await fireEvent.input(password, { target: { value: "test-only-password" } });
    expect(request.bodies).toEqual([]);
    await fireEvent.keyDown(password, { key: "Enter" });
    await fireEvent.focusOut(password, { relatedTarget: null });
    await waitFor(() => expect(request.bodies).toEqual([{ enabled: true, password: "test-only-password" }]));
    request.finish();
    await waitFor(() => expect(password).toHaveValue(""));
    await fireEvent.focusOut(password, { relatedTarget: null });
    expect(request.bodies).toHaveLength(1);
    expect(screen.queryByRole("button", { name: t("settings.networkAccessSave") })).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.networkAccessEnabled"))).toBeInTheDocument();
  });

  it("validates Unicode character count and bcrypt byte maximum before any request", async () => {
    const request = mockFetch();
    render(ConsolePageTestHost, { page: Settings });
    const password = await screen.findByLabelText(t("settings.networkAccessPassword"), { selector: "input" });
    for (const value of ["        ", "😀".repeat(7), "😀".repeat(19)]) {
      await fireEvent.input(password, { target: { value } });
      await fireEvent.focusOut(password, { relatedTarget: null });
      expect(request.bodies).toHaveLength(0);
    }
    await fireEvent.input(password, { target: { value: "😀".repeat(8) } });
    await fireEvent.focusOut(password, { relatedTarget: null });
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    request.finish();
  });

  it("applies a configured toggle immediately with no password and serializes later toggle edits", async () => {
    const request = mockFetch({ external_access: true, password_set: true });
    render(ConsolePageTestHost, { page: Settings });
    const toggle = await screen.findByRole("switch", { name: t("settings.networkAccessToggle") });
    await fireEvent.click(toggle);
    await waitFor(() => expect(request.bodies).toEqual([{ enabled: false, password: "" }]));
    await fireEvent.click(toggle);
    expect(request.bodies).toHaveLength(1);
    request.finish();
    await waitFor(() => expect(request.bodies).toEqual([{ enabled: false, password: "" }, { enabled: true, password: "" }]));
    expect(toggle).toBeChecked();
    request.finish();
    await waitFor(() => expect(within(accessStatus()).getByText(t("settingsAutoSave.saved"))).toBeInTheDocument());
    expect(screen.getByText("41720")).toBeInTheDocument();
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  });

  it("does not clear or automatically submit a newly typed password when the old request completes", async () => {
    const request = mockFetch();
    render(ConsolePageTestHost, { page: Settings });
    const password = await screen.findByLabelText(t("settings.networkAccessPassword"), { selector: "input" });
    await fireEvent.input(password, { target: { value: "test-only-first" } });
    await fireEvent.keyDown(password, { key: "Enter" });
    await waitFor(() => expect(request.bodies).toHaveLength(1));
    await fireEvent.input(password, { target: { value: "test-only-next" } });
    request.finish();
    await waitFor(() => expect(queryClient.getQueryData(["network-access"])).toMatchObject({ password_set: true }));
    expect(queryClient.getQueryData(["network-access"])).not.toHaveProperty("password");
    expect(password).toHaveValue("test-only-next");
    expect(request.bodies).toHaveLength(1);
    await fireEvent.focusOut(password, { relatedTarget: null });
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    request.finish();
    await waitFor(() => expect(password).toHaveValue(""));
  });

  it("retains a failed password securely for explicit retry", async () => {
    const request = mockFetch();
    request.fail();
    render(ConsolePageTestHost, { page: Settings });
    const password = await screen.findByLabelText(t("settings.networkAccessPassword"), { selector: "input" });
    await fireEvent.input(password, { target: { value: "test-only-retry" } });
    await fireEvent.keyDown(password, { key: "Enter" });
    const retry = await within(accessStatus()).findByRole("button", { name: t("settingsAutoSave.retry") });
    expect(password).toHaveValue("test-only-retry");
    expect(password).toHaveAttribute("type", "password");
    expect(document.body.textContent).not.toContain("test-only-retry");
    await fireEvent.click(retry);
    await waitFor(() => expect(request.bodies).toHaveLength(2));
    request.finish();
    await waitFor(() => expect(password).toHaveValue(""));
  });

  it("keeps remote clients read-only and offers a safe listener retry only locally", async () => {
    mockFetch({ external_access: true, password_set: true, can_manage: false });
    render(ConsolePageTestHost, { page: Settings });
    expect(await screen.findByRole("switch", { name: t("settings.networkAccessToggle") })).toBeDisabled();
    expect(screen.getByLabelText(t("settings.networkAccessPassword"), { selector: "input" })).toBeDisabled();
    expect(within(accessStatus()).queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.networkAccessLocalOnly"))).toBeInTheDocument();
  });

  it("reports startup listener failure and retries without replacing its password", async () => {
    const request = mockFetch({ password_set: true, external_access: true, listen_error: true });
    render(ConsolePageTestHost, { page: Settings });
    expect(await screen.findByRole("alert")).toHaveTextContent(t("settings.networkAccessListenError"));
    await fireEvent.click(within(accessStatus()).getByRole("button", { name: t("settingsAutoSave.retry") }));
    await waitFor(() => expect(request.bodies).toEqual([{ enabled: true, password: "" }]));
    request.finish();
  });
});

describe("Settings display automatic apply", () => {
  it("does not write on hydration and has no Save, discard or unsaved-preview UI", async () => {
    mockFetch();
    const savePreferences = vi.fn();
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await screen.findByRole("switch");
    expect(savePreferences).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: t("settings.savePreferences") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("appearance.discard") })).not.toBeInTheDocument();
    expect(screen.queryByText(t("appearance.previewing"))).not.toBeInTheDocument();
    expect(palette("default")).toHaveAttribute("aria-checked", "true");
    expect(screen.getAllByRole("radio")).toHaveLength(7);
    expect(document.querySelector('[data-palette-option="project"]')).toBeNull();
    expect(screen.getByLabelText(t("appearance.control"), { selector: "input" })).toHaveValue("#52763a");
  });

  it("applies language and theme immediately and sends only changed top-level fields", async () => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.language") }), { target: { value: "zh-CN" } });
    expect(localeInfo.value).toBe("zh-CN");
    await waitFor(() => expect(savePreferences).toHaveBeenCalledWith({ locale: "zh-CN" }));
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.theme") }), { target: { value: "dark" } });
    expect(document.documentElement.dataset.theme).toBe("dark");
    await waitFor(() => expect(savePreferences).toHaveBeenLastCalledWith({ theme: "dark" }));
  });

  it("serializes rapid edits, keeps newer selections when an older response lands, and saves them automatically", async () => {
    mockFetch();
    let finish!: (value: UserPreferences) => void;
    const savePreferences = vi.fn<(patch: UserPreferencesPatch) => Promise<UserPreferences>>()
      .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
      .mockImplementation(async (patch) => savedPreferences(patch, 3));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.click(palette("ocean"));
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "ocean" } }));
    await fireEvent.click(palette("forest"));
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.theme") }), { target: { value: "dark" } });
    await fireEvent.input(screen.getByLabelText(t("appearance.accent"), { selector: "input" }), { target: { value: "#135724" } });
    await fireEvent.input(screen.getByLabelText(t("appearance.control"), { selector: "input" }), { target: { value: "#345678" } });
    await fireEvent.change(screen.getByLabelText(t("appearance.control"), { selector: "input" }));
    expect(savePreferences).toHaveBeenCalledTimes(1);
    finish(savedPreferences({ appearance: { palette: "ocean" } }));
    expect(palette("forest")).toHaveAttribute("aria-checked", "true");
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { theme: "dark", appearance: { palette: "forest", accent: "#135724", control: "#345678" } }));
    await waitFor(() => expect(within(preferenceStatus()).getByText(t("settingsAutoSave.saved"))).toBeInTheDocument());
    expect(document.documentElement.dataset.palette).toBe("forest");
  });

  it("previews colors immediately, saves only on commit, and reset preserves the palette", async () => {
    mockFetch();
    let baseline = savedPreferences({}, 1);
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => (baseline = { ...baseline, ...patch, version: baseline.version + 1 }));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.click(palette("ocean"));
    await waitFor(() => expect(savePreferences).toHaveBeenCalledTimes(1));
    const accent = screen.getByLabelText(t("appearance.accent"), { selector: "input" });
    await fireEvent.input(accent, { target: { value: "#123456" } });
    await fireEvent.input(accent, { target: { value: "#246842" } });
    await fireEvent.input(screen.getByLabelText(t("appearance.background"), { selector: "input" }), { target: { value: "#edf4f8" } });
    await fireEvent.input(screen.getByLabelText(t("appearance.control"), { selector: "input" }), { target: { value: "#253a7e" } });
    expect(document.documentElement.style.getPropertyValue("--color-accent")).toBe("#246842");
    expect(savePreferences).toHaveBeenCalledTimes(1);
    expect(document.documentElement.style.getPropertyValue("--color-control")).toBe("#253a7e");
    await fireEvent.change(screen.getByLabelText(t("appearance.control"), { selector: "input" }));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { appearance: { palette: "ocean", accent: "#246842", background: "#edf4f8", control: "#253a7e" } }));
    await fireEvent.click(screen.getByRole("button", { name: t("appearance.resetColors") }));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(3, { appearance: { palette: "ocean" } }));
    expect(palette("ocean")).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("button", { name: t("appearance.resetColors") })).toBeDisabled();
  });

  it("commits an unsubmitted color before navigating, instead of silently rolling back the preview", async () => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.input(screen.getByLabelText(t("appearance.accent"), { selector: "input" }), { target: { value: "#347856" } });
    navigate("/agents");
    expect(savePreferences).toHaveBeenCalledWith({ appearance: { palette: "default", accent: "#347856" } });
    await waitFor(() => expect(location.path).toBe("/agents"));
    expect(document.documentElement.style.getPropertyValue("--color-accent")).toBe("#347856");
  });

  it("saves a control-only edit and clears it when choosing a different preset", async () => {
    mockFetch();
    let baseline = savedPreferences({ appearance: { palette: "default" } }, 1);
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => (baseline = { ...baseline, ...patch, version: baseline.version + 1 }));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.input(screen.getByLabelText(t("appearance.control"), { selector: "input" }), { target: { value: "#254f96" } });
    expect(document.documentElement.style.getPropertyValue("--color-control")).toBe("#254f96");
    expect(document.documentElement.style.getPropertyValue("--color-accent")).toBe("#52763a");
    expect(document.documentElement.style.getPropertyValue("--color-canvas")).toBe("#fbfbfb");
    await fireEvent.change(screen.getByLabelText(t("appearance.control"), { selector: "input" }));
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "default", control: "#254f96" } }));
    await fireEvent.click(palette("forest"));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { appearance: { palette: "forest" } }));
    expect(document.documentElement.style.getPropertyValue("--color-control")).toBe("#365940");
  });

  it("retains draft and preview on failure, blocks silent navigation, and retries explicitly", async () => {
    mockFetch();
    const savePreferences = vi.fn<(patch: UserPreferencesPatch) => Promise<UserPreferences>>()
      .mockRejectedValueOnce(new Error("Appearance save failed"))
      .mockImplementation(async (patch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.click(palette("flexoki"));
    expect(await screen.findByText("Appearance save failed")).toBeInTheDocument();
    expect(document.documentElement.dataset.palette).toBe("flexoki");
    await fireEvent.input(screen.getByLabelText(t("appearance.accent"), { selector: "input" }), { target: { value: "#347856" } });
    navigate("/agents");
    expect(location.path).toBe("/settings");
    expect(savePreferences).toHaveBeenCalledTimes(1);
    await fireEvent.click(within(preferenceStatus()).getByRole("button", { name: t("settingsAutoSave.retry") }));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { appearance: { palette: "flexoki", accent: "#347856" } }));
    await waitFor(() => expect(screen.queryByText("Appearance save failed")).not.toBeInTheDocument());
  });

  it("requires an explicit refresh then retry on a version conflict, without overwriting unrelated newer preferences", async () => {
    mockFetch();
    const savePreferences = vi.fn<(patch: UserPreferencesPatch) => Promise<UserPreferences>>()
      .mockRejectedValueOnce(new APIError(409, { error: { code: "version_conflict" } }))
      .mockImplementation(async (patch) => savedPreferences({ locale: "de-DE", ...patch }, 4));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.theme") }), { target: { value: "dark" } });
    const refresh = await within(preferenceStatus()).findByRole("button", { name: t("settingsAutoSave.refresh") });
    expect(within(preferenceStatus()).queryByRole("button", { name: t("settingsAutoSave.retry") })).not.toBeInTheDocument();
    queryClient.setQueryData(["user-preferences"], savedPreferences({ locale: "de-DE", appearance: { palette: "ocean" } }, 3));
    await fireEvent.click(refresh);
    expect(savePreferences).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("combobox", { name: t("settings.theme") })).toHaveValue("dark");
    expect(palette("ocean")).toHaveAttribute("aria-checked", "true");
    await fireEvent.click(within(preferenceStatus()).getByRole("button", { name: t("settingsAutoSave.retry") }));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { theme: "dark" }));
  });

  it("merges a color-only retry into the refreshed palette and other externally changed appearance fields", async () => {
    mockFetch();
    const savePreferences = vi.fn<(patch: UserPreferencesPatch) => Promise<UserPreferences>>()
      .mockRejectedValueOnce(new APIError(409, { error: { code: "version_conflict" } }))
      .mockImplementation(async (patch) => savedPreferences(patch, 4));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.input(screen.getByLabelText(t("appearance.accent"), { selector: "input" }), { target: { value: "#347856" } });
    await fireEvent.change(screen.getByLabelText(t("appearance.accent"), { selector: "input" }));
    const refresh = await within(preferenceStatus()).findByRole("button", { name: t("settingsAutoSave.refresh") });
    queryClient.setQueryData(["user-preferences"], savedPreferences({ appearance: { palette: "ocean", background: "#eaf5fa", control: "#243675" } }, 3));
    await fireEvent.click(refresh);
    await fireEvent.click(within(preferenceStatus()).getByRole("button", { name: t("settingsAutoSave.retry") }));
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { appearance: { palette: "ocean", background: "#eaf5fa", control: "#243675", accent: "#347856" } }));
  });

  it("keeps roving focus, arrow wrapping, Home/End and system-mode palette preview", async () => {
    mockFetch();
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
    render(ConsolePageTestHost, { page: Settings });
    palette("default").focus();
    for (const [from, key, to] of [["default", "ArrowLeft", "iris"], ["iris", "ArrowRight", "default"], ["default", "End", "iris"], ["iris", "Home", "default"]]) {
      await fireEvent.keyDown(palette(from), { key });
      expect(palette(to)).toHaveAttribute("aria-checked", "true");
      expect(palette(to)).toHaveFocus();
      expect(screen.getAllByRole("radio").filter((item) => item.tabIndex === 0)).toEqual([palette(to)]);
    }
    await fireEvent.click(palette("chamber"));
    await fireEvent.change(screen.getByRole("combobox", { name: t("settings.theme") }), { target: { value: "system" } });
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.dataset.palette).toBe("chamber");
  });

  it.each(["background", "accent", "control"] as const)("previews long %s drags without writes, then change and blur save the final value just once", async (key) => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await screen.findByRole("switch");
    const input = screen.getByLabelText(t(`appearance.${key}`), { selector: "input" });
    vi.useFakeTimers();
    try {
      for (const value of ["#123456", "#234567", "#345678"]) {
        await fireEvent.input(input, { target: { value } });
        expect(document.documentElement.style.getPropertyValue(`--color-${key === "background" ? "canvas" : key}`)).toBe(value);
        await vi.advanceTimersByTimeAsync(1000);
        expect(savePreferences).not.toHaveBeenCalled();
      }
    } finally { vi.useRealTimers(); }
    await fireEvent.change(input);
    await fireEvent.blur(input);
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "default", [key]: "#345678" } }));
    await fireEvent.change(input);
    await fireEvent.blur(input);
    expect(savePreferences).toHaveBeenCalledTimes(1);
  });

  it.each(["background", "accent", "control"] as const)("does not save %s when a drag returns to the initial effective color", async (key) => {
    mockFetch();
    const savePreferences = vi.fn();
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await screen.findByRole("switch");
    const input = screen.getByLabelText(t(`appearance.${key}`), { selector: "input" }) as HTMLInputElement;
    const initial = input.value;
    await fireEvent.input(input, { target: { value: "#234567" } });
    await fireEvent.input(input, { target: { value: initial } });
    await fireEvent.change(input);
    await fireEvent.blur(input);
    expect(savePreferences).not.toHaveBeenCalled();
    expect(input).toHaveValue(initial);
  });

  it.each(["change", "blur"] as const)("commits the final color on %s even without the other browser event", async (event) => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    const input = screen.getByLabelText(t("appearance.control"), { selector: "input" });
    if (event === "change") await fireEvent.change(input, { target: { value: "#345678" } });
    else {
      await fireEvent.input(input, { target: { value: "#345678" } });
      await fireEvent.blur(input);
    }
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "default", control: "#345678" } }));
  });

  it("does not send or overwrite an uncommitted color when an unrelated in-flight save finishes", async () => {
    mockFetch();
    let finish!: (value: UserPreferences) => void;
    const savePreferences = vi.fn<(patch: UserPreferencesPatch) => Promise<UserPreferences>>()
      .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
      .mockImplementation(async (patch) => savedPreferences(patch, 3));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.click(palette("ocean"));
    await waitFor(() => expect(savePreferences).toHaveBeenCalledTimes(1));
    const input = screen.getByLabelText(t("appearance.control"), { selector: "input" });
    await fireEvent.input(input, { target: { value: "#345678" } });
    finish(savedPreferences({ appearance: { palette: "ocean" } }));
    await waitFor(() => expect(input).toHaveValue("#345678"));
    expect(document.documentElement.style.getPropertyValue("--color-control")).toBe("#345678");
    expect(savePreferences).toHaveBeenCalledTimes(1);
    await fireEvent.change(input);
    await waitFor(() => expect(savePreferences).toHaveBeenNthCalledWith(2, { appearance: { palette: "ocean", control: "#345678" } }));
  });

  it("commits an input-only color on window leave and stops warning after the single save completes", async () => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.input(screen.getByLabelText(t("appearance.control"), { selector: "input" }), { target: { value: "#345678" } });
    const leaving = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(leaving);
    expect(leaving.defaultPrevented).toBe(true);
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "default", control: "#345678" } }));
    await waitFor(() => expect(within(preferenceStatus()).getByText(t("settingsAutoSave.saved"))).toBeInTheDocument());
    const saved = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(saved);
    expect(saved.defaultPrevented).toBe(false);
    expect(savePreferences).toHaveBeenCalledTimes(1);
  });

  it("drops an uncommitted color when the user explicitly selects a preset", async () => {
    mockFetch();
    const savePreferences = vi.fn(async (patch: UserPreferencesPatch) => savedPreferences(patch));
    render(ConsolePageTestHost, { page: Settings, savePreferences });
    await fireEvent.input(screen.getByLabelText(t("appearance.control"), { selector: "input" }), { target: { value: "#345678" } });
    await fireEvent.click(palette("forest"));
    await waitFor(() => expect(savePreferences).toHaveBeenCalledExactlyOnceWith({ appearance: { palette: "forest" } }));
    expect(screen.getByLabelText(t("appearance.control"), { selector: "input" })).toHaveValue("#365940");
  });
});

describe("settings translations", () => {
  it("registers appearance and autosave messages for all supported languages", () => {
    const original = localeInfo.value;
    try {
      for (const locale of SUPPORTED_LOCALES) {
        localeInfo.value = locale;
        for (const [key, value] of Object.entries(appearanceTranslations[locale])) expect(t(`appearance.${key}`)).toBe(value);
        for (const [key, value] of Object.entries(settingsAutoSaveTranslations[locale])) {
          expect(value).toBeTruthy();
          expect(t(`settingsAutoSave.${key}`)).toBe(value);
        }
      }
    } finally { localeInfo.value = original; }
  });
});
