import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// Explicitly NOT native CLI/gateway evidence: real browser + app + auth/config
// HTTP, but graph history, Run reads and SSE are page.route controlled fixtures.
// No fake run/event is inserted into SQLite, and execution POSTs are blocked.
test.use({ trace: "off" });

for (const width of [1280, 390]) test.describe(`provider anomaly UI fixture ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });

  for (const outcome of ["failed", "cancelled", "compact-failed-run-succeeded"]) {
    test(`UI fixture: ${outcome}`, { tag: ["@provider-anomalies", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }, info) => {
      const headers = { Origin: baseURL, "Content-Type": "application/json" };
      const api = async (path, data, method = data === undefined ? "GET" : "POST") => {
        const response = await request.fetch(`/v1${path}`, { method, data, headers: { ...headers, ...(method === "GET" ? {} : { "Idempotency-Key": randomUUID() }) } });
        expect(response.ok(), `${method} ${path}: ${response.status()}`).toBe(true);
        return response.status() === 204 ? undefined : (await response.json()).data;
      };
      headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
      await api("/system/onboarding", { dismissed: true }, "PUT");
      const computer = (await api("/computers")).items[0];
      const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready" && ["codex", "opencode"].includes(item.provider));
      test.skip(!service, "只使用现有 ready CLI 的配置元数据；不伪造可用性，也不调用它执行模型。");
      const project = await api("/projects", { computer_id: computer.id, name: `异常UI fixture-${randomUUID().slice(0, 8)}` });
      const agent = await api(`/computers/${computer.id}/agents`, { name: "UI fixture（不调用模型）", agent_service_id: service.id, model: "fixture/disabled-browser-ui", system_prompt: "仅展示 route fixture，绝不执行。" });
      const initial = await api(`/projects/${project.id}/conversation-graphs`, { title: "受控模型异常 UI fixture", agent_id: agent.id });
      const root = initial.nodes[0], runId = `fixture-anomaly-${randomUUID()}`, now = "2026-11-02T00:00:00Z";
      let settled = false;
      const status = outcome === "compact-failed-run-succeeded" ? "succeeded" : outcome;
      const forbidden = [], errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: computer.id, projectId: project.id });
      await page.route(/\/v1\/(?:.*\/)?(?:runs|run-now|mentions|messages|goals|comments)(?:[/?]|$)/, (route) => {
        if (route.request().method() === "GET") return route.fallback();
        forbidden.push(new URL(route.request().url()).pathname);
        return route.abort("blockedbyclient");
      });
      await page.route(`**/v1/conversation-graphs/${initial.graph.id}`, (route) => route.fulfill({
        contentType: "application/json", body: JSON.stringify({ data: {
          ...initial, nodes: [{ ...root, status: settled ? status === "failed" ? "failed" : "idle" : "running" }],
          messages: [{ id: `${runId}-message`, graph_id: initial.graph.id, node_id: root.id, run_id: runId, author_kind: "agent", status: settled ? status === "succeeded" ? "published" : "failed" : "working", body: "", created_at: now, updated_at: now }]
        } })
      }));
      await page.route(`**/v1/runs/${runId}`, (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: runId, status: settled ? status : "running", project_id: project.id, lease_epoch: 1, version: 1, input: [], created_at: now, updated_at: now } }) }));
      await page.route(`**/v1/runs/${runId}/events/stream?*`, async (route) => {
        const events = [
          ["context.compaction", { id: "compact", status: "running" }],
          ["provider.event", { kind: "error", willRetry: true, error: { data: { message: "Gateway retry fixture 503; API_KEY=fixture-only-key" } } }],
          ...(outcome === "compact-failed-run-succeeded" ? [["context.compaction", { id: "compact", status: "failed", message: "Compaction gateway fixture 502; Cookie: session=fixture-only-cookie" }]] : []),
          ["run.finished", { status: status === "succeeded" ? "completed" : status, ...(status === "failed" ? { error: { message: "Gateway fixture final 429", response: { body: "quota exhausted; Authorization: Bearer fixture-only-auth" } } } : {}) }]
        ];
        const body = events.map(([event_type, payload], index) => `event: ${event_type}\ndata: ${JSON.stringify({ id: `${runId}-${index}`, run_id: runId, seq: index + 1, event_type, payload: { provider_id: service.provider, ...payload }, occurred_at: now })}\n\n`).join("");
        settled = true;
        await route.fulfill({ status: 200, contentType: "text/event-stream", body });
      });
      await page.goto(`/conversations?graph=${initial.graph.id}&node=${root.id}`);
      const activity = page.locator(`[data-run-activity="${runId}"]`);
      await expect(activity.locator('[data-activity-kind="retry"]')).toContainText("Gateway retry fixture 503");
      await expect(activity.locator('[data-activity-status="running"]')).toHaveCount(0);
      if (status === "failed") {
        const row = activity.locator('[data-activity-kind="error"]');
        await expect(row).toContainText("Gateway fixture final 429");
        await row.getByRole("button").click();
        await expect(row.locator("pre")).toContainText("quota exhausted");
      }
      if (outcome === "compact-failed-run-succeeded") await expect(activity.locator('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "failed");
      else await expect(activity.locator('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "interrupted");
      expect(await activity.innerHTML()).not.toContain("fixture-only-");
      expect(forbidden).toEqual([]);
      expect(errors).toEqual([]);
      await page.screenshot({ path: info.outputPath(`event-pipeline-fixture-${outcome}-${width}.png`), animations: "disabled" });
    });
  }
});
