import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyLocale, t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Onboarding from "./Onboarding.svelte";

const now = "2026-10-22T00:00:00Z";
const computer = { id: "computer-test", version: 1, created_at: now, updated_at: now, user_id: "user-test", name: "This Mac", platform: "darwin", architecture: "arm64", fingerprint: "test", computer_use_enabled: false, status: "online" };
const service = { id: "service-opencode", computer_id: computer.id, provider: "opencode", provider_version: "2.0.18", status: "ready", login_status: "authenticated", capabilities: { read_only: true, file_bridge: true, models: ["native/default"] } };

function mock(options: { configured?: boolean; failAgent?: boolean; failDismiss?: boolean } = {}) {
  const calls: Array<{ path: string; method: string; body?: unknown }> = [];
  let failDismiss = options.failDismiss;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input), method = init?.method ?? "GET";
    calls.push({ path, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    let data: unknown = { items: [] };
    if (path.endsWith("/computers")) data = { items: [computer] };
    else if (path.endsWith("/agent-services")) data = { items: [service] };
    else if (path.endsWith("/work-assistant")) {
      if (method !== "GET") throw new Error("Onboarding must never configure Lumi");
      if (options.failAgent) return new Response(JSON.stringify({ error: { code: "service_unavailable" } }), { status: 503 });
      data = { id: "agt-lumi", name: "Lumi", version: 1, computer_id: computer.id, current_revision_id: options.configured ? "revision-lumi" : "", system_role: "general" };
    } else if (path.endsWith("/system/onboarding")) {
      if (failDismiss) return new Response(JSON.stringify({ error: { code: "service_unavailable" } }), { status: 503 });
      data = { needed: false, dismissed: true };
    }
    else if (path.includes("/diagnose")) data = { provider: "codex", available: false, authenticated: false, steps: [] };
    return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
  }));
  return { calls, recover: () => { failDismiss = false; } };
}

beforeEach(() => applyLocale("en-US"));
afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); window.history.replaceState({}, "", "/"); });

describe("onboarding introduces Lumi without a second editor", () => {
  it("does not depend on loading the Lumi configuration to finish the introduction", async () => {
    const { calls } = mock({ failAgent: true });
    render(ConsolePageTestHost, { page: Onboarding });
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
    expect(screen.getByRole("button", { name: t("onboarding.finish") })).toBeEnabled();
    expect(calls.some((call) => call.path.endsWith("/work-assistant"))).toBe(false);
  });

  it("has two steps and finishes on tasks without opening or configuring Lumi", async () => {
    const { calls } = mock();
    render(ConsolePageTestHost, { page: Onboarding });
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    expect(screen.getAllByRole("listitem").filter((item) => item.closest("ol"))).toHaveLength(2);
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
    await screen.findByRole("heading", { name: t("onboarding.meetLumi") });
    expect(screen.getByText(t("onboarding.lumiBuiltin"))).toBeInTheDocument();
    expect(screen.queryByLabelText(t("onboarding.selectProvider"))).not.toBeInTheDocument();
    expect(screen.queryByLabelText(t("onboarding.model"))).not.toBeInTheDocument();
    expect(screen.queryByLabelText("API Key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("onboarding.addModelProvider") })).not.toBeInTheDocument();
    const open = screen.getByRole("button", { name: t("onboarding.finish") });
    await waitFor(() => expect(open).toBeEnabled());
    await fireEvent.click(open);
    await waitFor(() => expect(window.location.pathname + window.location.search).toBe("/issues"));
    expect(calls.filter((call) => call.path.endsWith("/system/onboarding") && call.method === "PUT").map((call) => call.body)).toEqual([{ dismissed: true }]);
    expect(calls.some((call) => call.path.endsWith("/work-assistant"))).toBe(false);
    expect(calls.some((call) => call.path.includes("/model-providers"))).toBe(false);
  });

  it("only introduces an already configured Lumi without editing its configuration", async () => {
    const { calls } = mock({ configured: true });
    render(ConsolePageTestHost, { page: Onboarding });
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
    await screen.findByRole("heading", { name: t("onboarding.meetLumi") });
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(calls.filter((call) => call.method === "PATCH" || call.method === "PUT")).toHaveLength(0);
  });

  it("persists Skip without configuring or creating an Agent", async () => {
    const { calls } = mock();
    render(ConsolePageTestHost, { page: Onboarding });
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.skipAll") }));
    await waitFor(() => expect(window.location.pathname).toBe("/workbench"));
    expect(calls.filter((call) => ["PUT", "PATCH"].includes(call.method)).every((call) => call.path.endsWith("/system/onboarding"))).toBe(true);
  });

  it("keeps focus, traps Tab, restores inert background, and does not treat Escape as Skip", async () => {
    const { calls } = mock();
    const opener = document.createElement("button"); document.body.append(opener); opener.focus();
    const view = render(ConsolePageTestHost, { page: Onboarding });
    try {
      const heading = await screen.findByRole("heading", { name: t("onboarding.stepCli") });
      await waitFor(() => expect(heading).toHaveFocus());
      expect(opener).toHaveAttribute("inert");
      const dialog = screen.getByRole("dialog");
      const focusables = [...dialog.querySelectorAll<HTMLElement>("a[href],button,input,select,textarea")].filter((element) => !element.matches(":disabled") && element.tabIndex >= 0);
      const first = focusables[0], last = focusables.at(-1)!;
      last.focus(); await fireEvent.keyDown(last, { key: "Tab" }); expect(first).toHaveFocus();
      first.focus(); await fireEvent.keyDown(first, { key: "Tab", shiftKey: true }); expect(last).toHaveFocus();
      await fireEvent.keyDown(last, { key: "Escape" });
      await fireEvent.mouseDown(dialog.parentElement!);
      expect(calls.some((call) => call.path.endsWith("/system/onboarding"))).toBe(false);
      await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
      await waitFor(() => expect(screen.getByRole("heading", { name: t("onboarding.meetLumi") })).toHaveFocus());
    } finally { view.unmount(); expect(opener).not.toHaveAttribute("inert"); expect(opener).toHaveFocus(); opener.remove(); }
  });

  it("keeps the guide open if completion cannot be persisted and allows an explicit retry", async () => {
    const state = mock({ failDismiss: true });
    render(ConsolePageTestHost, { page: Onboarding });
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.finish") }));
    await screen.findByRole("alert");
    expect(window.location.pathname).not.toBe("/issues");
    expect(screen.getByRole("button", { name: t("onboarding.finish") })).toBeEnabled();
    state.recover(); await fireEvent.click(screen.getByRole("button", { name: t("onboarding.finish") }));
    await waitFor(() => expect(window.location.pathname).toBe("/issues"));
  });
});
