import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { location, navigate } from "../lib/router.svelte";
import { confirmProjectChange } from "../lib/unsavedChanges.svelte";
import type { AgentService } from "../lib/types";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import Agents from "./Agents.svelte";

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" }
  });
}
const readyService: AgentService = {
  id: "service-test",
  version: 1,
  created_at: "2026-09-14T00:00:00Z",
  updated_at: "2026-09-14T00:00:00Z",
  computer_id: "computer-test",
  provider: "codex",
  provider_version: "1.0.0",
  login_status: "authenticated",
  status: "ready",
  capabilities: { read_only: true, file_bridge: true, models: ["gpt-5"] },
  capability_version: 1
};
describe("Agent list stability", () => {
  it("shows CLI beside each name and notes underneath, never model text in list rows", async () => {
    navigate("/agents", { replace: true });
    const items = [
      { id: "noted", name: "Noted Agent", note: "Reviews incoming requests", agent_service_id: readyService.id, model: "secret-list-model" },
      { id: "blank", name: "Blank Agent", note: "  ", agent_service_id: "service-opencode", model: "another-list-model" },
      { id: "lumi", name: "Lumi", note: "", agent_service_id: "", system_role: "general" }
    ].map((item) => ({ ...item, computer_id: "computer-test", current_revision_id: item.id === "lumi" ? "" : `revision-${item.id}`, status: "active", version: 1 }));
    const fetch = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const path = String(input);
      if (path.endsWith("/agents")) return envelope({ items });
      if (path.includes("/agent-revisions/")) return envelope({ revision: { system_prompt: "Prompt" }, skills: [], skill_packages: {} });
      return envelope({ items: [] });
    });
    vi.stubGlobal("fetch", fetch);
    const { container } = render(ConsolePageTestHost, { page: Agents, services: [readyService, { ...readyService, id: "service-opencode", provider: "opencode" }] });
    await screen.findByDisplayValue("Noted Agent");
    const noted = container.querySelector<HTMLElement>('[data-agent-id="noted"]')!;
    const name = within(noted).getByText("Noted Agent");
    const cli = within(noted).getByText("Codex");
    const note = within(noted).getByText("Reviews incoming requests");
    expect(cli.parentElement).toBe(name.parentElement);
    expect(note.parentElement).toBe(name.parentElement!.parentElement);
    expect(within(noted).getByLabelText(t("agents.active"))).toBeInTheDocument();
    expect(noted).not.toHaveTextContent("secret-list-model");
    const blank = container.querySelector<HTMLElement>('[data-agent-id="blank"]')!;
    expect(within(blank).getByText("OpenCode").parentElement).toBe(within(blank).getByText("Blank Agent").parentElement);
    expect(blank).toHaveTextContent(t("scheduleEditor.noAgentNote"));
    expect(blank).not.toHaveTextContent("another-list-model");
    expect(blank).not.toHaveTextContent(t("agents.defaultModel"));
    const lumi = container.querySelector<HTMLElement>('[data-agent-id="lumi"]')!;
    expect(lumi).toHaveTextContent(t("scheduleEditor.builtinAgentNote"));
    expect(lumi).toHaveTextContent(t("scheduleEditor.unconfiguredAgent"));
    expect(lumi).not.toHaveTextContent("OpenCode");
    expect(fetch.mock.calls.some(([, init]) => ["PUT", "PATCH"].includes(init?.method ?? "GET"))).toBe(false);
  });

  it("updates an edited agent in place before and after the list refetch", async () => {
    let items = ["one", "two"].map((id) => ({
      id,
      name: `Agent ${id}`,
      computer_id: "computer-test",
      agent_service_id: "service-test",
      current_revision_id: `revision-${id}`,
      status: "active",
      version: 1
    }));
    let changed = false;
    let finishRefresh!: () => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/agent-revisions/"))
          return envelope({
            revision: { system_prompt: "Test prompt" },
            skills: [],
            skill_packages: {}
          });
        if (init?.method === "PATCH") {
          items = [{ ...items[0], name: "Renamed agent", version: 2 }, items[1]];
          changed = true;
          return envelope(items[0]);
        }
        if (url.endsWith("/agents")) {
          if (changed)
            await new Promise<void>((resolve) => {
              finishRefresh = resolve;
            });
          return envelope({ items });
        }
        return envelope({ items: [] });
      })
    );
    const { container } = render(ConsolePageTestHost, { page: Agents });
    const name = await screen.findByDisplayValue("Agent one");
    await waitFor(() => expect(name).toBeEnabled());
    const firstRow = container.querySelector('[data-agent-id="one"]');
    await fireEvent.input(name, { target: { value: "Renamed agent" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(finishRefresh).toBeTypeOf("function"));
    const order = () =>
      [...container.querySelectorAll("[data-agent-id]")].map((row) =>
        row.getAttribute("data-agent-id")
      );
    expect(order()).toEqual(["one", "two"]);
    expect(container.querySelector('[data-agent-id="one"]')).toBe(firstRow);
    finishRefresh();
    await waitFor(() => expect(queryClient.isFetching({ queryKey: ["agents"] })).toBe(0));
    expect(order()).toEqual(["one", "two"]);
  });

  it("opens the create editor from the button under the agent list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string | URL | Request) => {
        const url = String(input);
        if (url.endsWith("/agents")) return envelope({ items: [] });
        return envelope({ items: [] });
      })
    );
    render(ConsolePageTestHost, { page: Agents, services: [readyService] });
    const buttons = await screen.findAllByRole("button", { name: t("agents.newAgent") });
    expect(buttons.length).toBeGreaterThanOrEqual(2);
    const bottom = buttons[buttons.length - 1];
    await waitFor(() => expect(bottom).toBeEnabled());
    await fireEvent.click(bottom);
    const software = await screen.findByLabelText(t("agents.software"), { selector: "select" });
    expect(software).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: /Codex/ })).toBeInTheDocument();
  });

  it("keeps ordinary Agent creation disabled without a ready CLI", async () => {
    navigate("/agents", { replace: true });
    vi.stubGlobal("fetch", vi.fn(async () => envelope({ items: [] })));
    render(ConsolePageTestHost, { page: Agents, services: [{ ...readyService, status: "unavailable" }] });
    await screen.findByText(t("agents.noReadyService"));
    for (const button of screen.getAllByRole("button", { name: t("agents.newAgent") })) {
      expect(button).toBeDisabled();
    }
    expect(screen.queryByRole("button", { name: t("common.save") })).not.toBeInTheDocument();
  });

  it("protects dirty agent settings from routes, reloads and project switching", async () => {
    navigate("/agents", { replace: true });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    vi.stubGlobal("fetch", vi.fn(async () => envelope({ items: [] })));
    render(ConsolePageTestHost, { page: Agents, services: [readyService] });
    const name = await screen.findByLabelText(t("agents.name"));
    await waitFor(() => expect(name).toBeEnabled());
    await fireEvent.input(name, { target: { value: "Unsaved agent" } });
    navigate("/memories");
    expect(location.path).toBe("/agents");
    expect(confirmProjectChange("another-project")).toBe(false);
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    expect(name).toHaveValue("Unsaved agent");
    confirm.mockReturnValue(true);
    navigate("/memories");
    expect(location.path).toBe("/memories");
    expect(confirm).toHaveBeenCalledTimes(3);
  });
});

describe("one shared editor for Lumi and other Agents", () => {
  const builtinService: AgentService = { ...readyService, id: "service-opencode", provider: "opencode", capabilities: { read_only: true, file_bridge: true, models: ["native/default"] } };
  function stubEditor(unconfigured = false) {
    const calls: Array<{ path: string; method: string; body?: Record<string, unknown> }> = [];
    let items = [
      { id: "lumi", name: "Lumi", note: "", computer_id: "computer-test", agent_service_id: unconfigured ? "" : builtinService.id, current_revision_id: unconfigured ? "" : "revision-lumi", model: unconfigured ? "" : "native/default", status: "active", system_role: "general", version: 1 },
      { id: "ordinary", name: "Ordinary", note: "", computer_id: "computer-test", agent_service_id: readyService.id, current_revision_id: "revision-ordinary", model: "gpt-5", status: "active", system_role: "", version: 1 }
    ];
    const prompts: Record<string, string> = { lumi: "Lumi prompt", ordinary: "Ordinary prompt" };
    let configured: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, method, body });
      if (path.endsWith("/model-providers/test")) return envelope({ ok: true, code: "success", duration_ms: 1 });
      if (path.endsWith("/model-providers")) {
        if (method === "PUT") {
          const result = { preset_id: body.preset_id, name: "DeepSeek", base_url: body.base_url, model: body.model, model_ref: `teamva-${body.preset_id}/${body.model}`, key_configured: true };
          configured = [result]; return envelope(result);
        }
        return envelope({ presets: [{ id: "deepseek", name: "DeepSeek", base_url: "https://api.deepseek.com/v1", protocol: "openai", models: ["deepseek-chat"] }], configured });
      }
      if (path.includes("/agent-revisions/")) {
        const target = items.find((item) => item.current_revision_id === path.split("/").at(-1))!;
        return envelope({ revision: { model: target.model, system_prompt: prompts[target.id] }, skills: [], skill_packages: {} });
      }
      if (method === "PATCH" && path.includes("/agents/")) {
        const id = path.split("/").at(-1)!;
        items = items.map((item) => item.id === id ? { ...item, ...body, version: item.version + 1, current_revision_id: `revision-${id}-updated` } : item);
        prompts[id] = body.system_prompt;
        return envelope(items.find((item) => item.id === id));
      }
      if (path.endsWith("/agents")) return envelope({ items });
      return envelope({ items: [] });
    }));
    return calls;
  }

  it("edits all ordinary fields on Lumi using PATCH /agents, with no separate editor and no Delete", async () => {
    navigate("/agents?agent=lumi", { replace: true });
    const calls = stubEditor();
    const host = render(ConsolePageTestHost, { page: Agents, services: [readyService, builtinService] });
    const name = await screen.findByLabelText(t("agents.name"));
    await waitFor(() => expect(name).toBeEnabled());
    expect(name).toHaveValue("Lumi");
    expect(screen.getByLabelText(t("agents.systemPrompt"))).toHaveValue("Lumi prompt");
    expect(screen.queryByRole("button", { name: t("common.delete") })).not.toBeInTheDocument();
    await fireEvent.input(name, { target: { value: "Roomie" } });
    await fireEvent.change(screen.getByLabelText(t("agents.software")), { target: { value: readyService.id } });
    expect(screen.getByLabelText(t("agents.model"), { selector: "input" })).toHaveValue("");
    await fireEvent.input(screen.getByLabelText(t("agents.model"), { selector: "input" }), { target: { value: "gpt-5" } });
    await fireEvent.input(screen.getByLabelText(t("agents.systemPrompt")), { target: { value: "My instructions" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(calls.filter((call) => call.method === "PATCH")).toHaveLength(1));
    expect(calls.find((call) => call.method === "PATCH")).toMatchObject({ path: "/v1/agents/lumi", body: { name: "Roomie", note: "", agent_service_id: readyService.id, model: "gpt-5", system_prompt: "My instructions", expected_version: 1 } });
    expect(calls.some((call) => call.path.endsWith("/work-assistant") && call.method === "PUT")).toBe(false);
    await waitFor(() => expect(screen.getByRole("button", { name: t("common.save") })).toBeDisabled());
    await fireEvent.click(host.container.querySelector<HTMLElement>('[data-agent-id="ordinary"]')!);
    await waitFor(() => expect(screen.getByLabelText(t("agents.name"))).toHaveValue("Ordinary"));
    await waitFor(() => expect(screen.getByLabelText(t("agents.systemPrompt"))).toHaveValue("Ordinary prompt"));
    expect(screen.getByRole("button", { name: t("common.delete") })).toBeInTheDocument();
    expect(screen.getByLabelText(t("agents.name"))).toHaveClass("am-input");
    expect(screen.getByLabelText(t("agents.software"))).toHaveClass("am-select__control");
  });

  it("prefills the built-in runtime for an unconfigured Lumi without silently writing configuration", async () => {
    navigate("/agents?agent=lumi", { replace: true });
    const calls = stubEditor(true);
    const { container } = render(ConsolePageTestHost, { page: Agents, services: [{ ...builtinService, status: "unavailable", login_status: "required" }] });
    await screen.findByLabelText(t("agents.name"));
    await waitFor(() => expect(screen.getByLabelText(t("agents.software"))).toHaveValue(builtinService.id));
    expect(screen.getByLabelText(t("agents.model"), { selector: "input" })).toHaveValue("");
    expect(screen.getByRole("button", { name: t("common.save") })).toBeEnabled();
    const lumi = container.querySelector<HTMLElement>('[data-agent-id="lumi"]')!;
    expect(lumi).toHaveTextContent(t("scheduleEditor.builtinAgentNote"));
    expect(lumi).toHaveTextContent(t("scheduleEditor.unconfiguredAgent"));
    expect(lumi).not.toHaveTextContent("OpenCode");
    expect(calls.some((call) => call.method === "PATCH" || call.method === "PUT")).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(calls.find((call) => call.method === "PATCH")?.path).toBe("/v1/agents/lumi"));
  });

  it("uses model API setup for any Agent and leaves the selected model as a draft until common Save", async () => {
    navigate("/agents?agent=ordinary", { replace: true });
    const calls = stubEditor();
    render(ConsolePageTestHost, { page: Agents, services: [readyService, builtinService] });
    const add = await screen.findByRole("button", { name: t("onboarding.addModelProvider") });
    await waitFor(() => expect(add).toBeEnabled());
    await fireEvent.click(add);
    await waitFor(() => expect(screen.getByLabelText(t("onboarding.apiProvider"))).toBeEnabled());
    await fireEvent.change(screen.getByLabelText(t("onboarding.apiProvider")), { target: { value: "deepseek" } });
    await fireEvent.input(screen.getByLabelText("API Key"), { target: { value: "private-ui-fixture" } });
    expect(calls.some((call) => call.path.endsWith("/model-providers/test"))).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.apiTest") }));
    await screen.findByText(t("onboarding.apiTest_success"), { exact: false });
    expect(calls.filter((call) => call.path.endsWith("/model-providers/test"))).toHaveLength(1);
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.apiSave") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByLabelText(t("agents.model"), { selector: "input" })).toHaveValue("teamva-deepseek/deepseek-chat");
    expect(calls.filter((call) => call.method === "PATCH")).toHaveLength(0);
    expect(JSON.stringify(queryClient.getQueryData(["model-provider-settings", "opencode"]))).not.toContain("private-ui-fixture");
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(calls.filter((call) => call.method === "PATCH")).toHaveLength(1));
    expect(calls.find((call) => call.method === "PATCH")).toMatchObject({ path: "/v1/agents/ordinary", body: { agent_service_id: builtinService.id, model: "teamva-deepseek/deepseek-chat" } });
  });
});
