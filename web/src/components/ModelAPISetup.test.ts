import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import ModelAPISetupTestHost from "./ModelAPISetupTestHost.svelte";

const settings = {
  presets: [{ id: "deepseek", name: "DeepSeek", base_url: "https://api.deepseek.com/v1", protocol: "openai", models: ["deepseek-chat"] }],
  configured: []
};
const saved = { preset_id: "deepseek", name: "DeepSeek", base_url: "https://api.deepseek.com/v1", model: "deepseek-chat", model_ref: "teamva-deepseek/deepseek-chat", key_configured: true };
function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function mockAPI(testResponse = async () => envelope({ ok: true, code: "success", duration_ms: 1 })) {
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/test")) return testResponse();
    return envelope(init?.method === "PUT" ? saved : settings);
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
});

describe("ModelAPISetup", () => {
  it("uses the shared labelled controls and associates the model hint without triggering requests", async () => {
    const fetch = mockAPI();
    render(ModelAPISetupTestHost);
    const dialog = screen.getByRole("dialog", { name: t("onboarding.stepAPI") });
    const provider = within(dialog).getByLabelText(t("onboarding.apiProvider"));
    await waitFor(() => expect(provider).toBeEnabled());
    await fireEvent.change(provider, { target: { value: "deepseek" } });
    expect(provider).toHaveClass("am-select__control");
    expect(within(dialog).getByLabelText(t("onboarding.apiBaseURL"))).toHaveClass("am-input");
    const model = within(dialog).getByLabelText(t("onboarding.apiModel"));
    expect(model).toHaveValue("deepseek-chat");
    expect(model).toHaveAccessibleDescription(t("onboarding.apiModelHint"));
    const key = within(dialog).getByLabelText("API Key");
    expect(key).toHaveAttribute("type", "password");
    expect(key).toHaveClass("am-input");
    expect(within(dialog).getByRole("button", { name: t("onboarding.apiTest") })).toHaveAttribute("type", "button");
    expect(within(dialog).getByRole("button", { name: t("onboarding.apiSave") })).toHaveAttribute("type", "submit");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][1]?.method).toBe("GET");
  });

  it("keeps explicit test/save actions working and never stores the entered key in query data", async () => {
    let finishTest!: (value: Response) => void;
    const fetch = mockAPI(() => new Promise<Response>((resolve) => { finishTest = resolve; }));
    const onSaved = vi.fn(async () => {}), onReadyChange = vi.fn(), onBusyChange = vi.fn();
    render(ModelAPISetupTestHost, { onSaved, onReadyChange, onBusyChange });
    const provider = screen.getByLabelText(t("onboarding.apiProvider"));
    await waitFor(() => expect(provider).toBeEnabled());
    await fireEvent.change(provider, { target: { value: "deepseek" } });
    const key = screen.getByLabelText("API Key");
    await fireEvent.input(key, { target: { value: "layout-test-placeholder" } });
    await fireEvent.click(screen.getByRole("button", { name: t("onboarding.apiTest") }));
    await waitFor(() => expect(onBusyChange).toHaveBeenLastCalledWith(true));
    expect(provider).toBeDisabled();
    expect(screen.getByRole("button", { name: t("onboarding.apiSave") })).toBeDisabled();
    finishTest(envelope({ ok: true, code: "success", duration_ms: 1 }));
    await screen.findByText(t("onboarding.apiTest_success"), { exact: false });
    await waitFor(() => expect(onBusyChange).toHaveBeenLastCalledWith(false));
    expect(fetch.mock.calls.filter(([path]) => String(path).endsWith("/test"))).toHaveLength(1);
    expect(onSaved).not.toHaveBeenCalled();
    await fireEvent.submit(key.closest("form")!);
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(saved));
    await waitFor(() => expect(onReadyChange).toHaveBeenLastCalledWith(true));
    expect(key).toHaveValue("");
    expect(fetch.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1);
    expect(JSON.stringify(queryClient.getQueryData(["model-provider-settings", "opencode"]))).not.toContain("layout-test-placeholder");
  });
});
