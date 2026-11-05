import { mount } from "svelte";
import Host from "./Host.svelte";
import { providerDefinitions } from "../../../../web/src/lib/providers";
import { applyLocale } from "../../../../web/src/lib/i18n.svelte";
import "../../../../web/src/app.css";

// UI-only fixture: no backend, account, executable or real Provider is touched.
const services = providerDefinitions.map(({ id }, index) => ({
  id: `fixture-${id}`, provider: id, provider_version: "2.0.18-fixture",
  status: index ? "unavailable" : "ready", login_status: index ? "required" : "authenticated"
}));
const nativeFetch = window.fetch.bind(window);
window.fetch = async (input, init) => {
  const path = new URL(typeof input === "string" ? input : input.url, location.origin).pathname;
  if (!path.startsWith("/v1/")) return nativeFetch(input, init);
  let data;
  if (path.endsWith("/agent-services")) data = { items: services };
  else if (path.endsWith("/diagnose")) data = {
    provider: path.split("/").at(-2), available: true, authenticated: true,
    provider_version: "2.0.18-fixture", finished_at: "2026-10-26T00:00:00Z",
    steps: Array.from({ length: 24 }, (_, index) => ({
      id: `step-${index}`, label: `诊断步骤 ${index + 1}`, status: "success", duration_ms: 10,
      message: "用于验证正文滚动与底栏可见的超长诊断内容。".repeat(5),
      detail: "/fixture/long-command-path/".repeat(8)
    }))
  };
  else if (path === "/v1/system/programming-tools") data = { items: [] };
  else if (path === "/v1/system/onboarding") data = { needed: false, dismissed: true };
  else throw new Error(`Unexpected fixture API: ${path}`);
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
};
applyLocale("zh-CN");
mount(Host, { target: document.getElementById("app") });
