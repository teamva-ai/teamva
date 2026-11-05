import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// Real Chrome and HTTP shell. Only the selected conversation's execution GETs
// and SSE are deterministic fixtures; no Run or model request is created.
test("旧侧栏断流后补回终止事件、内部工具逐条显示", { tag: ["@smoke", "@stream-recovery"] }, async ({ page, request, baseURL }) => {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  async function api(path, data) {
    const response = await request.fetch(`/v1${path}`, { method: data === undefined ? "GET" : "POST", headers, data });
    expect(response.ok(), `${path}: ${response.status()}`).toBeTruthy();
    return (await response.json()).data;
  }
  headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
  const computer = (await api("/computers")).items[0];
  const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.provider === "opencode" && item.status === "ready");
  expect(service, "需要已就绪 CLI 渲染真实表单，但本例绝不调用模型").toBeTruthy();
  const agent = await api(`/computers/${computer.id}/agents`, { name: `断流回归-${randomUUID().slice(0, 8)}`, agent_service_id: service.id, model: "fixture/model" });
  const project = await api("/projects", { computer_id: computer.id, name: `断流项目-${randomUUID().slice(0, 8)}` });
  const issue = await api(`/projects/${project.id}/issues`, { title: "失败之后不能一直生成", status: "open" });
  const stamp = "2026-10-28T00:00:00Z";
  const versioned = { version: 1, created_at: stamp, updated_at: stamp };
  const instance = { ...versioned, id: "stream-recovery-instance", issue_id: issue.id, agent_id: agent.id,
    agent_revision_id: agent.current_revision_id, status: "running", pending_mention_count: 0, last_activity_at: stamp };
  const run = { ...versioned, id: "stream-recovery-run", issue_id: issue.id, issue_agent_instance_id: instance.id,
    issue_agent_generation_id: "stream-recovery-generation", agent_id: agent.id, project_id: project.id,
    agent_service_id: service.id, status: "running", model: "fixture/model", purpose: "work", read_only: false,
    lease_epoch: 1, started_at: stamp, input: [{ type: "text", text: "继续" }] };
  const event = (seq, event_type, payload) => ({ id: `stream-recovery-event-${seq}`, run_id: run.id,
    seq, event_type, occurred_at: stamp, payload: { provider_id: "opencode", ...payload } });
  const ipc = ".teamva/tool-requests/private-transport-marker/request.json";
  const all = [
    event(1, "run.started", { status: "running" }),
    event(2, "reasoning.snapshot", { id: "reasoning-start", text: "先读取最新任务上下文。", status: "completed" }),
    event(3, "output.snapshot", { id: "reply-before-tool", text: "我先读取一下任务的最新上下文。" }),
    event(4, "provider.event", { kind: "tool", call_id: "native-write", name: "write", phase: "input.started" }),
    event(5, "provider.event", { kind: "tool", call_id: "native-write", phase: "called", input: { filePath: ipc, content: "private-request-body-marker" } }),
    event(6, "provider.event", { kind: "tool", call_id: "native-write", phase: "success", content: [{ type: "text", text: `Created file at: ${ipc}` }] }),
    event(7, "provider.tool", { id: "issue_get:req-1", provider: "teamva", tool: "issue_get", status: "completed", input: { issue_id: issue.id }, output: { title: issue.title } }),
    event(8, "provider.event", { kind: "tool", call_id: "native-read", name: "read", phase: "input.started" }),
    event(9, "provider.event", { kind: "tool", call_id: "native-read", phase: "called", input: { filePath: ipc } }),
    event(10, "provider.event", { kind: "tool", call_id: "native-read", phase: "success", content: [{ type: "text", text: `Response for ${ipc}: private-result-body-marker` }] }),
    event(11, "reasoning.snapshot", { id: "reasoning-after-tool", text: "已经取得任务上下文，准备继续。", status: "completed" }),
    // Deliberately no run.finished: this matches the real provider failure.
    event(12, "run.error", { status: "failed", error: { error: { message: "Upstream request failed: Endpoint is unavailable.", status: 503, type: "provider.internal" } } })
  ];
  let terminal = false;
  let eventReads = 0;
  let release;
  const interrupted = new Promise((resolve) => { release = resolve; });
  const reply = (route, data) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: computer.id, projectId: project.id });
  await page.route("**/v1/work-records/navigation", (route) => route.fulfill({ status: 204 }));
  await page.route(`**/v1/issues/${issue.id}/agent-instances`, (route) => reply(route, { items: [{ ...instance, status: terminal ? "idle" : "running" }] }));
  const runs = (route) => reply(route, { items: [{ ...run, status: terminal ? "failed" : "running", ...(terminal ? { terminal_reason: "provider_failed", finished_at: stamp } : {}) }] });
  await page.route(`**/v1/issues/${issue.id}/runs*`, runs);
  await page.route(`**/v1/agent-instances/${instance.id}/runs*`, runs);
  await page.route(`**/v1/agent-instances/${instance.id}/goal`, (route) => reply(route, null));
  await page.route(`**/v1/runs/${run.id}`, (route) => reply(route, { ...run, status: terminal ? "failed" : "running" }));
  await page.route(`**/v1/runs/${run.id}/events?*`, (route) => {
    eventReads++;
    const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
    return reply(route, { items: (terminal ? all : all.slice(0, 3)).filter((item) => item.seq > after), truncated: false });
  });
  await page.route(`**/v1/runs/${run.id}/events/stream?*`, async (route) => {
    await interrupted;
    terminal = true;
    // The server is terminal, but the disconnected SSE omits the last event.
    await route.fulfill({ status: 200, contentType: "text/event-stream", body: all.slice(3, -1).map((item) => `event: ${item.event_type}\ndata: ${JSON.stringify(item)}\n\n`).join("") });
  });
  try {
    await page.goto(`/issues?issue=${issue.id}&session=${instance.id}`);
    const dialog = page.getByRole("dialog", { name: `${agent.name} · 会话 1`, exact: true });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("正在生成…", { exact: true })).toBeVisible();
    const readsBeforeDisconnect = eventReads;
    release();
    await expect(dialog.getByText(/HTTP 503/)).toBeVisible({ timeout: 15_000 });
    await expect(dialog.getByText("正在生成…", { exact: true })).toHaveCount(0);
    await expect(dialog.getByText("读取任务", { exact: true })).toBeVisible();
    const tools = dialog.locator('[data-activity-kind="tool"]');
    await expect(tools).toHaveCount(3);
    await expect(tools.nth(0)).toContainText("修改文件");
    await expect(tools.nth(1)).toContainText("读取任务");
    await expect(tools.nth(2)).toContainText("读取文件");
    await tools.nth(0).getByRole("button").click();
    // The shared ingress field allowlist still applies; native write content
    // is not a display scalar, but its path and returned text remain readable.
    await expect(tools.nth(0).locator("pre")).toContainText("private-transport-marker");
    await expect(tools.nth(0).locator("pre")).toContainText("Created file at:");
    await tools.nth(2).getByRole("button").click();
    await expect(tools.nth(2).locator("pre")).toContainText("private-result-body-marker");
    await expect(dialog.getByRole("textbox", { name: "会话输入", exact: true })).toHaveAttribute("placeholder", "继续向该 Agent 提问……");
    expect(eventReads, "终止状态恢复必须补读持久化事件，不能只合成失败文案").toBeGreaterThan(readsBeforeDisconnect);
    await expect(dialog).not.toContainText("内部工具传输已折叠");
  } finally {
    release();
  }
});

for (const width of [1280, 390]) test.describe(`Codex initial stream ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("被@后首段尚未返回也显示运行状态，原生搜索在正文前显示并按生命周期更新", {
    tag: ["@smoke", "@codex-initial-stream", width === 390 ? "@mobile" : "@desktop"]
  }, async ({ page, request, baseURL }, info) => {
    const headers = { Origin: baseURL, "Content-Type": "application/json" };
    async function api(path, data) {
      const response = await request.fetch(`/v1${path}`, { method: data === undefined ? "GET" : "POST", headers, data });
      expect(response.ok(), `${path}: ${response.status()}`).toBeTruthy();
      return (await response.json()).data;
    }
    headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
    const computer = (await api("/computers")).items[0];
    const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
    expect(service, "仅配置真实表单，不调用模型").toBeTruthy();
    const suffix = randomUUID().slice(0, 8);
    const agent = await api(`/computers/${computer.id}/agents`, { name: `Codex首段-${suffix}`, agent_service_id: service.id, model: "fixture/model" });
    const project = await api("/projects", { computer_id: computer.id, name: `首段显示-${suffix}` });
    const issue = await api(`/projects/${project.id}/issues`, { title: "异步委派后的首段状态", status: "open" });
    const stamp = "2026-11-02T00:00:00Z", versioned = { version: 1, created_at: stamp, updated_at: stamp };
    const instance = { ...versioned, id: `initial-instance-${suffix}`, issue_id: issue.id, agent_id: agent.id,
      agent_revision_id: agent.current_revision_id, status: "running", pending_mention_count: 0, last_activity_at: stamp };
    const run = { ...versioned, id: `initial-run-${suffix}`, issue_id: issue.id, issue_agent_instance_id: instance.id,
      agent_id: agent.id, project_id: project.id, status: "running", model: "fixture/model", purpose: "work",
      read_only: false, lease_epoch: 1, input: [], started_at: stamp };
    const event = (seq, event_type, payload) => ({ id: `initial-event-${suffix}-${seq}`, run_id: run.id,
      seq, event_type, occurred_at: stamp, payload: { provider_id: "codex", ...payload } });
    const initial = [event(1, "run.queued", {}), event(2, "run.started", {}),
      event(3, "provider.item", { internal: true, item: { id: "private-context", type: "userMessage",
        content: [{ text: "private-activation-context-must-not-render" }] } })];
    const searchStarted = event(4, "provider.item", { method: "item/started", item: {
      id: "search-before-answer", type: "webSearch", action: null, query: null, results: null
    } });
    const searchCompleted = event(5, "provider.item", { method: "item/completed", item: {
      id: "search-before-answer", type: "webSearch", action: { type: "search", query: null, queries: ["synthetic-search-before-output"] },
      query: null, results: []
    } });
    const answer = event(6, "output.delta", { item_id: "first-answer", text: "正文的第一个流式片段" });
    const completed = event(7, "run.completed", { status: "completed" });
    let phase = 0, streamRequests = 0;
    let releaseSearch, releaseCompletion;
    const searchGate = new Promise((resolve) => { releaseSearch = resolve; });
    const completionGate = new Promise((resolve) => { releaseCompletion = resolve; });
    const events = () => [...initial, ...(phase >= 1 ? [searchStarted] : []),
      ...(phase >= 2 ? [searchCompleted, answer, completed] : [])];
    const reply = (route, data) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
    await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), {
      computerId: computer.id, projectId: project.id
    });
    await page.route("**/v1/work-records/navigation", (route) => route.fulfill({ status: 204 }));
    await page.route(`**/v1/issues/${issue.id}/agent-instances`, (route) => reply(route, { items: [{ ...instance, status: phase === 2 ? "idle" : "running" }] }));
    const runs = (route) => reply(route, { items: [{ ...run, status: phase === 2 ? "succeeded" : "running" }] });
    await page.route(`**/v1/issues/${issue.id}/runs*`, runs);
    await page.route(`**/v1/agent-instances/${instance.id}/runs*`, runs);
    await page.route(`**/v1/agent-instances/${instance.id}/goal`, (route) => reply(route, { goal: null }));
    await page.route(`**/v1/runs/${run.id}`, (route) => reply(route, { ...run, status: phase === 2 ? "succeeded" : "running" }));
    await page.route(`**/v1/runs/${run.id}/events?*`, (route) => {
      const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
      return reply(route, { items: events().filter((item) => item.seq > after) });
    });
    await page.route(`**/v1/runs/${run.id}/events/stream?*`, async (route) => {
      streamRequests++;
      const after = Number(new URL(route.request().url()).searchParams.get("after_seq") || 0);
      if (after < 4) await searchGate;
      else await completionGate;
      const body = events().filter((item) => item.seq > after).map((item) => `event: ${item.event_type}\ndata: ${JSON.stringify(item)}\n\n`).join("");
      await route.fulfill({ status: 200, contentType: "text/event-stream", body });
    });
    try {
      await page.goto(`/issues?issue=${issue.id}&session=${instance.id}`);
      const dialog = page.getByRole("dialog", { name: `${agent.name} · 会话 1`, exact: true });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByText("正在生成…", { exact: true })).toBeVisible();
      await expect.poll(() => streamRequests).toBeGreaterThan(0);
      await expect(dialog.locator('[data-activity-kind="tool"]')).toHaveCount(0);
      await expect(dialog).not.toContainText("private-activation-context-must-not-render");
      await page.screenshot({ path: info.outputPath(`codex-waiting-first-event-${width}.png`) });

      phase = 1;
      releaseSearch();
      const search = dialog.locator('[data-activity-kind="tool"]');
      await expect(search).toHaveCount(1);
      await expect(search).toHaveAttribute("data-activity-status", /running|inProgress|started/);
      await expect(dialog.getByText("正在生成…", { exact: true })).toBeVisible();
      await expect(dialog.getByText("正文的第一个流式片段", { exact: true })).toHaveCount(0);

      phase = 2;
      releaseCompletion();
      await expect(search).toHaveCount(1);
      await expect(search).toHaveAttribute("data-activity-status", "completed");
      await expect(search).toContainText("synthetic-search-before-output");
      await expect(dialog.getByText("正文的第一个流式片段", { exact: true })).toBeVisible();
      await expect(dialog.getByText("正在生成…", { exact: true })).toHaveCount(0);
      await expect(dialog).not.toContainText("private-activation-context-must-not-render");
      await page.screenshot({ path: info.outputPath(`codex-search-and-output-${width}.png`) });
    } finally {
      releaseSearch();
      releaseCompletion();
    }
  });
});
