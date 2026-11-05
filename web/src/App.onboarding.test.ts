import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import Root from "./Root.svelte";
import { applyLocale, t } from "./lib/i18n.svelte";
import { clearOnboardingRequest, requestOnboarding } from "./lib/onboardingOverlay.svelte";
import { queryClient } from "./lib/query";

const user = {
  id: "usr_test",
  email: "owner@example.test",
  display_name: "Owner",
  version: 1,
  created_at: "2026-08-01T00:00:00Z"
};
const computer = {
  id: "cmp_test",
  name: "测试电脑",
  version: 1,
  user_id: "usr_test",
  platform: "darwin",
  architecture: "arm64",
  fingerprint: "fp",
  status: "online",
  computer_use_enabled: false
};

function mockFetch(needed = true, computersGate?: Promise<void>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      let data: unknown = { items: [] };
      if (path.endsWith("/auth/local-session")) {
        data = { csrf_token: "csrf-test", user };
      } else if (path.endsWith("/system/onboarding")) {
        if (init?.method === "PUT") needed = false;
        data = { dismissed: !needed, needed };
      } else if (path.endsWith("/me/preferences")) {
        data = {
          user_id: "usr_test",
          locale: "en-US",
          locale_explicit: true,
          theme: "system",
          allow_write: false,
          version: 1
        };
      } else if (path.endsWith("/v1/computers")) {
        await computersGate;
        data = { items: [computer] };
      } else if (path.includes("/computer-use")) {
        data = {
          binary: { installed: false, error: "not installed (test)" },
          platform: "darwin",
          supported: true
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "req-test" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    })
  );
}

// Vite's cold transformation of the shell's entire editor graph is not a UI
// transition. Await compilation before mounting instead of racing findBy's
// one-second DOM window (the first test used to pay this cost alone).
beforeAll(async () => {
  await Promise.all([import("./pages/ConsoleShell.svelte"), import("./pages/Onboarding.svelte")]);
});
beforeEach(() => applyLocale("en-US"));

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  clearOnboardingRequest();
  window.history.replaceState({}, "", "/");
  window.dispatchEvent(new PopStateEvent("popstate"));
});

describe("first-run overlay", () => {
  it("opens the guide as a dialog over the workbench on the default entry", async () => {
    window.history.replaceState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
    mockFetch();
    render(Root);
    await waitFor(() => expect(window.location.pathname).toBe("/workbench"));
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    const dialog = screen.getByRole("dialog", { name: t("onboarding.title") });
    expect(dialog).toBeInTheDocument();
    expect(document.querySelector("[data-console-background]")?.closest("[inert]")).not.toBeNull();
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
  });

  it("keeps a direct subpage link and shows the setup banner instead", async () => {
    window.history.replaceState({}, "", "/settings");
    window.dispatchEvent(new PopStateEvent("popstate"));
    mockFetch();
    render(Root);
    await waitFor(() =>
      expect(queryClient.getQueryData(["onboarding"])).toMatchObject({ needed: true })
    );
    expect(await screen.findByText(t("onboarding.banner"))).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings");
  });

  it("treats /onboarding as a workbench overlay alias", async () => {
    window.history.replaceState({}, "", "/onboarding");
    window.dispatchEvent(new PopStateEvent("popstate"));
    mockFetch();
    render(Root);
    await waitFor(() => expect(window.location.pathname).toBe("/workbench"));
    expect(await screen.findByRole("dialog", { name: t("onboarding.title") })).toBeInTheDocument();
  });

  it("keeps a manually reopened guide open on an already configured instance", async () => {
    window.history.replaceState({}, "", "/workbench");
    window.dispatchEvent(new PopStateEvent("popstate"));
    mockFetch(false);
    requestOnboarding();
    render(Root);
    await waitFor(() =>
      expect(queryClient.getQueryData(["onboarding"])).toMatchObject({ needed: false })
    );
    expect(await screen.findByRole("dialog", { name: t("onboarding.title") })).toBeInTheDocument();
  });

  it("keeps the automatic guide at its current step after Lumi configuration clears needed", async () => {
    window.history.replaceState({}, "", "/workbench");
    window.dispatchEvent(new PopStateEvent("popstate"));
    mockFetch();
    render(Root);
    await screen.findByRole("heading", { name: t("onboarding.stepCli") });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.next") }));
    await screen.findByRole("heading", { name: t("onboarding.meetLumi") });
    queryClient.setQueryData(["onboarding"], { needed: false, dismissed: false });
    await waitFor(() => expect(queryClient.getQueryData(["onboarding"])).toMatchObject({ needed: false }));
    expect(screen.getByRole("heading", { name: t("onboarding.meetLumi") })).toBeInTheDocument();
    expect(document.querySelector("[data-console-background]")?.closest("[inert]")).not.toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.skipAll") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("survives late server locale hydration and releases the background after an explicit skip", async () => {
    applyLocale("zh-CN");
    mockFetch();
    render(Root);
    // Resolve the accessible name on every retry: the persisted locale can
    // arrive after both lazy chunks, rather than capturing yesterday's locale.
    await waitFor(() => expect(screen.getByRole("dialog", { name: t("onboarding.title") })).toBeInTheDocument());
    await waitFor(() => expect(screen.getByRole("button", { name: t("onboarding.skipAll") })).toBeInTheDocument());
    const skip = screen.getByRole("button", { name: t("onboarding.skipAll") });
    await fireEvent.click(skip);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(document.querySelector("[data-console-background]")?.closest("[inert]")).toBeNull();
  });

  it("owns focus while Computer loading is delayed, before the step-aware guide mounts", async () => {
    let release!: () => void;
    mockFetch(true, new Promise<void>((resolve) => { release = resolve; }));
    render(Root);
    const loadingDialog = await screen.findByRole("dialog", { name: t("onboarding.title") });
    await waitFor(() => expect(loadingDialog).toHaveFocus());
    const tab = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    document.dispatchEvent(tab);
    expect(tab.defaultPrevented).toBe(true);
    release();
    await waitFor(() => expect(screen.getByRole("heading", { name: t("onboarding.stepCli") })).toHaveFocus());
  });
});
