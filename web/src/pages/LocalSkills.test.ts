import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import { applyLocale, SUPPORTED_LOCALES, t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import LocalSkills from "./LocalSkills.svelte";

function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  applyLocale("en-US");
  navigate("/workbench", { replace: true });
});

describe("Skill uninstall conflict", () => {
  it.each(SUPPORTED_LOCALES)("shows the unbind/stop guidance in %s, keeps the installation and allows a later retry", async (locale) => {
    applyLocale(locale);
    navigate("/skills", { replace: true });
    let inUse = true;
    let installed = true;
    const installation = { id: "csk-test", skill_package_id: "skill-hallmark", skill_package_version_id: "skv-test", version: 7 };
    const calls: Array<{ path: string; method: string }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      calls.push({ path, method });
      if (path === "/v1/skill-packages" && method === "GET") {
        return json({ items: [{ id: "skill-hallmark", slug: "hallmark", name: "Hallmark" }] });
      }
      if (path === "/v1/computers/computer-test/skill-packages" && method === "GET") {
        return json({ items: installed ? [installation] : [] });
      }
      if (path === "/v1/computers/computer-test/skill-packages/csk-test?expected_version=7" && method === "DELETE") {
        if (inUse) {
          return new Response(JSON.stringify({ error: {
            code: "skill_in_use", message: "private-runtime-payload", retryable: false
          } }), { status: 409, headers: { "Content-Type": "application/json" } });
        }
        installed = false;
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${method} ${path}`);
    }));

    render(ConsolePageTestHost, { page: LocalSkills });
    const installedList = screen.getByRole("region", { name: t("localSkills.installed") });
    await within(installedList).findByText("Hallmark");
    await fireEvent.click(within(installedList).getByRole("button", { name: t("localSkills.uninstall") }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(t("errors.codes.skill_in_use"));
    expect(alert).not.toHaveTextContent(t("errors.codes.invalid_argument"));
    expect(alert).not.toHaveTextContent("private-runtime-payload");
    expect(within(installedList).getByText("Hallmark")).toBeInTheDocument();
    expect(installed).toBe(true);
    expect(calls.filter((call) => call.method === "DELETE")).toHaveLength(1);

    // Simulate the user having unbound the skill and stopped its runs elsewhere.
    // The rejected mutation must not alter the installation's CAS version.
    inUse = false;
    await waitFor(() => expect(within(installedList).getByRole("button", { name: t("localSkills.uninstall") })).toBeEnabled());
    await fireEvent.click(within(installedList).getByRole("button", { name: t("localSkills.uninstall") }));
    await waitFor(() => expect(within(installedList).queryByText("Hallmark")).not.toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(calls.filter((call) => call.method === "DELETE")).toEqual([
      { path: "/v1/computers/computer-test/skill-packages/csk-test?expected_version=7", method: "DELETE" },
      { path: "/v1/computers/computer-test/skill-packages/csk-test?expected_version=7", method: "DELETE" }
    ]);
  });
});
