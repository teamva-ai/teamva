import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { providerDefinitions } from "../lib/providers";
import { queryClient } from "../lib/query";
import type { AgentService, Computer } from "../lib/types";
import ProgrammingToolsPanelTestHost from "../test/ProgrammingToolsPanelTestHost.svelte";

const computer: Computer = {
  id: "computer-test",
  version: 1,
  created_at: "2026-09-14T00:00:00Z",
  updated_at: "2026-09-14T00:00:00Z",
  user_id: "user-test",
  name: "This Mac",
  platform: "darwin",
  architecture: "arm64",
  fingerprint: "test",
  computer_use_enabled: false,
  status: "online"
};

function service(provider: string, overrides: Partial<AgentService> = {}): AgentService {
  return {
    id: `service-${provider}`,
    version: 1,
    created_at: "2026-09-14T00:00:00Z",
    updated_at: "2026-09-14T00:00:00Z",
    computer_id: computer.id,
    provider,
    provider_version: "1.0.0",
    login_status: "authenticated",
    status: "ready",
    capabilities: { read_only: true, file_bridge: true },
    capability_version: 1,
    ...overrides
  };
}

function envelope(data: unknown) {
  return new Response(JSON.stringify({ data, meta: { request_id: "req-tools" } }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function mock(services: AgentService[]) {
  let diagnose = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-services")) return envelope({ items: services });
      if (path.includes("/programming-tools/") && path.endsWith("/diagnose")) {
        diagnose += 1;
        return envelope({
          provider: "codex",
          provider_version: "1.0.0",
          available: true,
          authenticated: true,
          models: [],
          capability_version: 1,
          started_at: "2026-09-14T00:00:00Z",
          finished_at: "2026-09-14T00:00:01Z",
          steps: []
        });
      }
      return envelope({ items: [] });
    })
  );
  return () => diagnose;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
});

describe("ProgrammingToolsPanel auto-detection", () => {
  it("reuses stored results instead of diagnosing ready tools again", async () => {
    const calls = mock(providerDefinitions.map((definition) => service(definition.id)));
    render(ProgrammingToolsPanelTestHost, { computer, autoDetect: true });
    expect(await screen.findAllByText(t("programmingTools.state.connected"))).toHaveLength(
      providerDefinitions.length
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(calls()).toBe(0);
  });

  it("auto-detects tools that are not connected yet", async () => {
    const calls = mock([]);
    render(ProgrammingToolsPanelTestHost, { computer, autoDetect: true });
    expect(await screen.findAllByText(t("programmingTools.state.unknown"))).toHaveLength(
      providerDefinitions.length
    );
    await waitFor(() => expect(calls()).toBe(providerDefinitions.length));
  });

  it("keeps diagnostic actions in the persistent modal footer rather than the scrolling log", async () => {
    mock(providerDefinitions.map((definition) => service(definition.id)));
    render(ProgrammingToolsPanelTestHost, { computer });
    const row = document.querySelector<HTMLElement>('[data-programming-tool="opencode"]')!;
    await fireEvent.click(within(row).getByRole("button", { name: t("programmingTools.diagnose") }));
    const dialog = await screen.findByRole("dialog", { name: t("programmingTools.diagnosticTitle", { tool: "OpenCode" }) });
    const footer = dialog.querySelector(":scope > footer")!;
    expect(footer).not.toBeNull();
    expect(within(footer as HTMLElement).getByRole("button", { name: t("programmingTools.runAgain") })).toBeInTheDocument();
    expect(within(footer as HTMLElement).getByRole("button", { name: t("common.close") })).toBeInTheDocument();
    expect(dialog.querySelector(".am-modal__body")?.contains(footer)).toBe(false);
    await fireEvent.click(within(footer as HTMLElement).getByRole("button", { name: t("common.close") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
