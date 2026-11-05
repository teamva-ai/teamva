import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// Boundary: creation/root/Agent binding/manual rename use the isolated real HTTP
// service. Delayed automatic titles and execution failures are page.route UI
// fixtures, not evidence of Lumi/model execution. Never configure a real Lumi
// model, mention an Agent, run a schedule, or forward a message/Goal POST.
test.use({ trace: "off" });

async function setup({ page, request, baseURL }, { agents = false } = {}) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  async function api(path, data, method = data === undefined ? "GET" : "POST") {
    const response = await request.fetch(`/v1${path}`, { headers: { ...headers, ...(method === "GET" ? {} : { "Idempotency-Key": randomUUID() }) }, method, data });
    expect(response.ok(), `${method} ${path}: HTTP ${response.status()}`).toBe(true);
    return response.status() === 204 ? undefined : (await response.json()).data;
  }
  headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
  const lumi = await api("/work-assistant");
  expect(!lumi.current_revision_id || (lumi.model ?? "").startsWith("fixture/"), "隔离验收中的 Lumi 必须未配置或使用不可调用的 fixture 模型").toBe(true);
  await api("/system/onboarding", { dismissed: true }, "PUT");
  const computer = (await api("/computers")).items[0];
  const suffix = randomUUID().slice(0, 8);
  const project = await api("/projects", { computer_id: computer.id, name: `自动标题UI-${suffix}` });
  const choices = [];
  if (agents) {
    const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
    test.skip(!service, "需要真实 ready CLI 配置元数据，不伪造 Provider 可用性");
    for (const name of ["甲", "乙"]) choices.push(await api(`/computers/${computer.id}/agents`, {
      name: `标题验收-${name}-${suffix}`, agent_service_id: service.id, model: "fixture/disabled-browser-ui", system_prompt: "只验证 UI，不调用模型。"
    }));
  }
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: computer.id, projectId: project.id });
  const forbidden = [], errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route(/\/v1\/(?:.*\/)?(?:runs|run-now|mentions|messages|goals|comments)(?:[/?]|$)/, async (route) => {
    if (route.request().method() === "GET") return route.fallback();
    forbidden.push(new URL(route.request().url()).pathname);
    return route.abort("blockedbyclient");
  });
  return { api, project, choices, forbidden, errors };
}

const reply = (route, data, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
const fail = (route, status = 503) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ error: { code: status === 409 ? "version_conflict" : "runtime_unavailable", message: "确定性验收失败；不会调用模型。" } }) });
const mutation = (page, method, path) => page.waitForResponse((response) => response.request().method() === method && new URL(response.url()).pathname === `/v1${path}`);
const canvasFor = (page) => page.getByRole("application", { name: "对话画布", exact: true });
const camera = (canvas) => canvas.evaluate((element) => {
  const matrix = new DOMMatrix(getComputedStyle(element.firstElementChild).transform);
  return { x: matrix.e, y: matrix.f, scale: matrix.a };
});
async function settledCamera(canvas) {
  let previous, stable = 0, current;
  await expect.poll(async () => {
    current = await camera(canvas);
    const signature = JSON.stringify(current);
    stable = signature === previous ? stable + 1 : 0;
    previous = signature;
    return stable;
  }, { message: "等待有界缩放动画结束，而非只等待首个 RAF", timeout: 2_000, intervals: [30] }).toBeGreaterThanOrEqual(3);
  return current;
}

for (const width of [1280, 390]) test.describe(`automatic titles ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });

  test("真实 HTTP：任务/记忆/定时任务创建不输入标题，灵感直接创建不传标题", { tag: ["@auto-title", "@smoke", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }, info) => {
    const { api, project, choices, forbidden, errors } = await setup({ page, request, baseURL }, { agents: true });
    await page.goto("/issues");
    await page.getByRole("button", { name: "新建任务", exact: true }).click();
    let dialog = page.getByRole("dialog", { name: "创建任务", exact: true });
    await expect(dialog.getByRole("textbox", { name: "标题", exact: true })).toHaveCount(0);
    await expect(dialog.getByRole("combobox", { name: "Agent", exact: true })).toHaveCount(0);
    await dialog.getByRole("textbox", { name: "描述", exact: true }).fill("无需起名的任务说明");
    await expect(dialog.getByRole("checkbox", { name: /创建 Git Worktree/ })).toBeDisabled();
    let pending = mutation(page, "POST", `/projects/${project.id}/issues`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    let response = await pending;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toEqual({ description: "无需起名的任务说明", worktree_hint: false });
    expect((await response.json()).data.title).toBe("新任务");
    await expect(page.getByRole("heading", { name: "新任务", exact: true })).toBeVisible();

    await page.goto("/memories");
    await page.getByRole("button", { name: "新建记忆", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "新建项目记忆", exact: true });
    await expect(dialog.getByRole("textbox", { name: "标题", exact: true })).toHaveCount(0);
    await expect(dialog.locator("input")).toHaveCount(0);
    await dialog.getByRole("combobox", { name: "类型", exact: true }).selectOption("decision");
    await dialog.getByRole("textbox", { name: "内容", exact: true }).fill("无需起名的记忆内容");
    pending = mutation(page, "POST", `/projects/${project.id}/memories`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    response = await pending;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).not.toHaveProperty("title");
    const memory = (await response.json()).data;
    expect(memory).toMatchObject({ title: "新记忆", kind: "decision", content: "无需起名的记忆内容" });
    await expect(dialog).toBeHidden();
    await expect(page.getByRole("textbox", { name: "标题", exact: true })).toHaveValue("新记忆");

    await page.goto("/schedules");
    await page.locator("main header").getByRole("button", { name: "新建定时任务", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "新建定时任务", exact: true });
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await dialog.getByLabel("Agent", { exact: true }).selectOption(choices[0].id);
    await dialog.getByLabel("提示词", { exact: true }).fill("仅保存远期计划，绝不执行。");
    await dialog.getByLabel("调度类型", { exact: true }).selectOption("once");
    await dialog.locator('input[type="datetime-local"]').fill("2040-01-02T09:30");
    pending = mutation(page, "POST", `/projects/${project.id}/scheduled-tasks`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    response = await pending;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).not.toHaveProperty("name");
    const schedule = (await response.json()).data;
    expect(schedule.name).toBe("新定时任务");
    expect(new Date(schedule.schedule_expression).getUTCFullYear()).toBe(2040);
    await expect(dialog).toBeHidden();

    await page.goto("/inspirations");
    pending = mutation(page, "POST", "/inspirations");
    await page.locator("[data-inspirations] > header").getByRole("button", { name: "新建灵感", exact: true }).click();
    response = await pending;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toEqual({ project_id: project.id, markdown: "" });
    const inspiration = (await response.json()).data;
    expect(inspiration.title).toBe("新灵感");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Markdown 所见即所得编辑器", exact: true }).getByRole("textbox", { name: "标题", exact: true })).toHaveValue("新灵感");
    expect((await api(`/inspirations/${inspiration.id}`)).title).toBe("新灵感");
    expect(forbidden).toEqual([]);
    expect(errors).toEqual([]);
    await page.screenshot({ path: info.outputPath(`default-titles-${width}.png`), animations: "disabled" });
  });

  test("真实 HTTP：空图无需标题/Agent 创建，节点切换 Agent 成败均保留消息/目标/预算草稿和手动标题", { tag: ["@auto-title", "@conversation-create", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }, info) => {
    const { api, project, choices, forbidden, errors } = await setup({ page, request, baseURL }, { agents: true });
    await page.goto("/conversations");
    const pending = mutation(page, "POST", `/projects/${project.id}/conversation-graphs`);
    await page.locator("main header").filter({ has: page.getByRole("heading", { name: "分支对话", exact: true }) }).getByRole("button", { name: "新建分支对话", exact: true }).click();
    const created = await pending;
    expect(created.request().postDataJSON()).toEqual({});
    const initial = (await created.json()).data;
    expect(initial.graph).toMatchObject({ title: "新对话", node_count: 0 });
    expect(initial.nodes).toEqual([]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const canvas = canvasFor(page);
    await expect(canvas.locator("[data-node-id]")).toHaveCount(0);
    await expect(canvas.getByRole("textbox")).toHaveCount(0);
    await expect(page.getByLabel("图标题", { exact: true })).toHaveCount(0);
    const choose = canvas.getByRole("combobox", { name: "Agent", exact: true });
    await expect(choose).toHaveValue("");
    await expect(canvas.getByRole("button", { name: "创建", exact: true })).toBeDisabled();
    await choose.selectOption(choices[0].id);
    const rootPending = mutation(page, "POST", `/conversation-graphs/${initial.graph.id}/root`);
    await canvas.getByRole("button", { name: "创建", exact: true }).click();
    const rootResponse = await rootPending;
    expect(rootResponse.request().postDataJSON()).toEqual({ agent_id: choices[0].id, expected_version: initial.graph.version });
    const root = (await rootResponse.json()).data;
    expect(root).toMatchObject({ graph_id: initial.graph.id, agent_id: choices[0].id });
    const card = page.locator(`[data-node-id="${root.id}"]`);
    const field = card.getByRole("textbox", { name: "消息输入", exact: true });
    const selector = card.getByRole("combobox", { name: "Agent", exact: true });
    const goal = card.getByRole("checkbox", { name: "持续目标", exact: true });
    const budget = card.getByRole("spinbutton", { name: "Token 预算", exact: true });
    await field.fill("切换前的消息与目标草稿");
    await goal.check();
    await budget.fill("5000");
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    const switches = [];
    await page.route(`**/v1/conversation-nodes/${root.id}`, async (route) => {
      const request = route.request();
      if (request.method() !== "PATCH" || !request.postDataJSON().agent_id) return route.fallback();
      switches.push(request.postDataJSON());
      if (switches.length === 1) return fail(route); // Synthetic failure only.
      await gate;
      return route.fallback(); // Success is the real optimistic-versioned PATCH.
    });
    try {
      await selector.selectOption(choices[1].id);
      await expect(card.getByRole("alert")).toBeVisible();
      await expect(selector).toBeEnabled();
      await expect(selector).toHaveValue(choices[0].id);
      await expect(field).toHaveValue("切换前的消息与目标草稿");
      await expect(goal).toBeChecked();
      await expect(budget).toHaveValue("5000");
      await selector.selectOption(choices[1].id);
      await expect.poll(() => switches.length).toBe(2);
      await expect(selector).toBeDisabled();
      await expect(card.getByRole("button", { name: "启动目标", exact: true })).toBeDisabled();
      await field.fill("切换请求中继续输入也必须保留");
      release();
      await expect(selector).toBeEnabled();
      await expect(selector).toHaveValue(choices[1].id);
      await expect(field).toHaveValue("切换请求中继续输入也必须保留");
      await expect(goal).toBeChecked();
      await expect(budget).toHaveValue("5000");
      expect(switches).toEqual(Array(2).fill({ agent_id: choices[1].id, expected_version: root.version }));
      const persisted = await api(`/conversation-nodes/${root.id}`);
      expect(persisted.agent_id).toBe(choices[1].id);
      expect(persisted.agent_revision_id).toBe(choices[1].current_revision_id);

      // Fixture failure validates the newly displayed binding without sending
      // any execution request to the actual service or a Provider.
      const sends = [];
      await page.route(`**/v1/conversation-nodes/${root.id}/goals`, (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        sends.push(route.request().postDataJSON()); return fail(route);
      });
      await card.getByRole("button", { name: "启动目标", exact: true }).click();
      await expect.poll(() => sends.length).toBe(1);
      expect(sends[0]).toEqual({ objective: "切换请求中继续输入也必须保留", token_budget: 5000, expected_agent_id: choices[1].id, expected_agent_revision_id: choices[1].current_revision_id });
      await expect(card.getByRole("alert")).toBeVisible();
      await expect(card.getByRole("button", { name: "启动目标", exact: true })).toBeEnabled();
      await expect(field).toHaveValue("切换请求中继续输入也必须保留");
      await expect(budget).toHaveValue("5000");
      await page.route(`**/v1/conversation-nodes/${root.id}/messages`, (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        sends.push(route.request().postDataJSON()); return fail(route);
      });
      await goal.uncheck();
      await card.getByRole("button", { name: "发送", exact: true }).click();
      await expect.poll(() => sends.length).toBe(2);
      expect(sends[1]).toEqual({ body: "切换请求中继续输入也必须保留", expected_agent_id: choices[1].id, expected_agent_revision_id: choices[1].current_revision_id });
      await expect(card.getByRole("button", { name: "发送", exact: true })).toBeEnabled();
      await expect(field).toHaveValue("切换请求中继续输入也必须保留");
      await goal.check();
      await expect(budget).toHaveValue("5000");

      await card.getByRole("button", { name: "编辑 · 新对话分支标题", exact: true }).click();
      await card.getByRole("textbox", { name: "新对话分支标题", exact: true }).fill("手动节点标题");
      await card.locator("header").getByRole("button", { name: "保存", exact: true }).click();
      await expect(card.locator("header strong")).toHaveText("手动节点标题");
      await page.getByRole("button", { name: "编辑 · 图标题", exact: true }).click();
      await page.getByRole("textbox", { name: "图标题", exact: true }).fill("手动对话图标题");
      await page.locator("main header form").getByRole("button", { name: "保存", exact: true }).click();
      await expect(page.getByRole("heading", { name: "手动对话图标题", exact: true })).toBeVisible();
      await expect(field).toHaveValue("切换请求中继续输入也必须保留");
      await expect(goal).toBeChecked();
      await expect(budget).toHaveValue("5000");
      const final = await api(`/conversation-graphs/${initial.graph.id}`);
      expect(final.graph.title).toBe("手动对话图标题");
      expect(final.nodes[0]).toMatchObject({ title: "手动节点标题", agent_id: choices[1].id });
      expect(final.messages).toEqual([]);
      expect(final.goals ?? []).toEqual([]);
      expect(forbidden).toEqual([]);
      expect(errors).toEqual([]);
      await page.screenshot({ path: info.outputPath(`node-agent-drafts-${width}.png`), animations: "disabled" });
    } finally { release(); }
  });

  test("UI fixture：后台对话图标题轮询不重置相机、不覆盖手动标题或消息草稿", { tag: ["@auto-title", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }) => {
    const { api, project, choices, forbidden, errors } = await setup({ page, request, baseURL }, { agents: true });
    const initial = await api(`/projects/${project.id}/conversation-graphs`, { title: "新对话", agent_id: choices[0].id });
    let graph = initial.graph;
    const renames = [];
    await page.route(`**/v1/projects/${project.id}/conversation-graphs`, (route) => reply(route, { items: [graph] }));
    await page.route(`**/v1/conversation-graphs/${graph.id}`, (route) => {
      if (route.request().method() === "GET") return reply(route, { ...initial, graph });
      const body = route.request().postDataJSON();
      renames.push(body);
      if (body.expected_version !== graph.version) return fail(route, 409);
      graph = { ...graph, title: body.title, version: graph.version + 1 };
      return reply(route, graph);
    });
    await page.goto(`/conversations?graph=${graph.id}`);
    const canvas = canvasFor(page);
    const field = page.locator(`[data-node-id="${initial.nodes[0].id}"]`).getByRole("textbox", { name: "消息输入", exact: true });
    await field.fill("自动起名不能清空消息草稿");
    if (width > 767) { await canvas.focus(); await page.keyboard.press("ArrowRight"); }
    const before = await camera(canvas);
    graph = { ...graph, title: "后台生成的对话图标题", version: graph.version + 1 };
    await expect(page.getByRole("heading", { name: graph.title, exact: true })).toBeVisible({ timeout: 5_000 });
    expect(await camera(canvas)).toEqual(before);
    await expect(field).toHaveValue("自动起名不能清空消息草稿");
    await page.getByRole("button", { name: "编辑 · 图标题", exact: true }).click();
    const title = page.getByRole("textbox", { name: "图标题", exact: true });
    await title.fill("  用户明确输入的图标题  ");
    graph = { ...graph, title: "稍后到达的另一个自动标题", version: graph.version + 1 };
    if (width > 767) await expect(page.getByRole("complementary", { name: "根节点", exact: true }).getByRole("button", { name: new RegExp(graph.title) })).toBeVisible({ timeout: 5_000 });
    else await expect(page.getByRole("combobox", { name: "分支对话", exact: true }).locator(`option[value="${graph.id}"]`)).toHaveText(graph.title, { timeout: 5_000 });
    await expect(title).toHaveValue("  用户明确输入的图标题  ");
    expect(await camera(canvas)).toEqual(before);
    await page.locator("main header form").getByRole("button", { name: "保存", exact: true }).click();
    await expect(page.getByRole("heading", { name: "用户明确输入的图标题", exact: true })).toBeVisible();
    expect(renames).toEqual([{ title: "用户明确输入的图标题", expected_version: 3 }]);
    await expect(field).toHaveValue("自动起名不能清空消息草稿");
    expect(await camera(canvas)).toEqual(before);
    expect(forbidden).toEqual([]);
    expect(errors).toEqual([]);
  });

  test("UI fixture：延迟自动记忆标题合并正文草稿，显式手改默认名也不能被下一次刷新覆盖", { tag: ["@auto-title", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }) => {
    const { api, project, forbidden, errors } = await setup({ page, request, baseURL });
    let memory = await api(`/projects/${project.id}/memories`, { key: `auto-title-${randomUUID()}`, kind: "fact", title: "新记忆", content: "服务端原正文", source_type: "user" });
    const patches = [];
    await page.route((url) => url.pathname === `/v1/projects/${project.id}/memories`, (route) => reply(route, { items: [memory] }));
    await page.route(`**/v1/projects/${project.id}/memories/${memory.id}`, (route) => {
      if (route.request().method() === "GET") return reply(route, memory);
      const body = route.request().postDataJSON(); patches.push(body);
      if (body.expected_version !== memory.version) return fail(route, 409);
      memory = { ...memory, title: body.title ?? memory.title, content: body.content, version: memory.version + 1 };
      return reply(route, memory);
    });
    await page.goto(`/memories?memory=${memory.id}`);
    const title = page.getByRole("textbox", { name: "标题", exact: true });
    const content = page.getByRole("textbox", { name: "内容", exact: true });
    await expect(title).toHaveValue("新记忆");
    await content.fill("未保存的本地正文草稿");
    await page.getByRole("button", { name: "历史", exact: true }).click();
    await expect(page.getByText("v1 · 新记忆", { exact: true })).toBeVisible();
    memory = { ...memory, title: "后台根据正文生成的记忆标题", version: 2 };
    await expect(title).toHaveValue(memory.title, { timeout: 5_000 });
    await expect(content).toHaveValue("未保存的本地正文草稿");
    await expect(page.getByText("v1 · 新记忆", { exact: true })).toBeVisible();
    await title.fill("新记忆"); // Explicit intent, even though it equals the default.
    memory = { ...memory, title: "后到的自动记忆标题", version: 3 };
    await expect(page.locator(`[data-memory-id="${memory.id}"] strong`)).toHaveText(memory.title, { timeout: 5_000 });
    await expect(title).toHaveValue("新记忆");
    await expect(content).toHaveValue("未保存的本地正文草稿");
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
    expect(patches).toEqual([{ title: "新记忆", content: "未保存的本地正文草稿", expected_version: 3 }]);
    await page.reload();
    await expect(title).toHaveValue("新记忆");
    await expect(content).toHaveValue("未保存的本地正文草稿");
    expect(forbidden).toEqual([]);
    expect(errors).toEqual([]);
  });
});

for (const width of [1280, 390]) test.describe(`issue work assignments ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("UI fixture：第二 Agent 转工作分配、手动同 Agent 多项、幂等重试和依赖状态", {
    tag: ["@issue-work", "@smoke", "@layout", width === 390 ? "@mobile" : "@desktop"]
  }, async ({ page, request, baseURL }, info) => {
    const { api, project, choices, forbidden, errors } = await setup({ page, request, baseURL }, { agents: true });
    const issue = await api(`/projects/${project.id}/issues`, { title: "工作分配浏览器回归" });
    const versioned = { version: 1, created_at: issue.created_at, updated_at: issue.updated_at };
    const instance = { ...versioned, id: `work-instance-${issue.id}`, issue_id: issue.id, agent_id: choices[0].id,
      agent_revision_id: choices[0].current_revision_id, status: "idle", pending_mention_count: 0 };
    const comments = [], sends = [];
    let failNextWork = true, completed = false, plans = 0;
    // Only UI projections and execution POSTs are fixtures. They never reach a Provider.
    await page.route("**/v1/computers/*/agent-services", async (route) => {
      const response = await route.fetch(), envelope = await response.json();
      await route.fulfill({ response, json: { ...envelope, data: { ...envelope.data, items: envelope.data.items.map((service) => ({
        ...service, capabilities: { ...service.capabilities, no_tools: false }
      })) } } });
    });
    await page.route(`**/v1/issues/${issue.id}/work-plan`, (route) => { plans++; return fail(route); });
    await page.route(`**/v1/issues/${issue.id}/comments`, (route) => {
      if (route.request().method() === "GET") return reply(route, { items: comments.map((comment) => ({ ...comment,
        work_items: comment.work_items?.map((item, index) => ({ ...item,
          status: index === 0 ? "waiting_result" : completed ? "blocked" : "waiting_dependencies",
          blocked_by: index === 0 ? [] : [comment.work_items[0].key],
          issue_agent_instance_id: index === 0 ? instance.id : undefined })) })) });
      const body = route.request().postDataJSON();
      sends.push({ body, key: route.request().headers()["idempotency-key"] });
      if (body.work_items && failNextWork) { failNextWork = false; return fail(route); }
      const comment = { ...versioned, id: `work-comment-${comments.length}`, issue_id: issue.id, author_kind: "user", status: "published", body: body.body || "工作分配",
        work_items: body.work_items?.map((item, index) => ({ ...item, status: index === 0 ? "waiting_result" : "waiting_dependencies", blocked_by: item.after,
          issue_agent_instance_id: index === 0 ? instance.id : undefined })) };
      comments.push(comment);
      return reply(route, comment, 201);
    });
    await page.route(`**/v1/issues/${issue.id}/agent-instances`, (route) => reply(route, { items: [instance] }));
    await page.route(`**/v1/issues/${issue.id}/runs*`, (route) => reply(route, { items: [] }));
    await page.route(`**/v1/agent-instances/${instance.id}/runs*`, (route) => reply(route, { items: [] }));
    await page.route(`**/v1/agent-instances/${instance.id}/goal`, (route) => reply(route, { goal: null }));
    await page.goto(`/issues?issue=${issue.id}`);
    const input = page.getByRole("textbox", { name: "评论输入", exact: true });
    const composer = page.getByTestId("issue-work-composer");
    await input.fill(`@${choices[0].name} @`);
    await page.locator(`button[data-agent-id="${choices[1].id}"]`).click();
    await expect(input).toHaveValue(`@${choices[0].name} @`);
    await expect(composer).toBeVisible();
    await expect(composer.getByLabel("负责人", { exact: true }).nth(0)).toHaveValue(choices[0].id);
    await expect(composer.getByLabel("负责人", { exact: true }).nth(1)).toHaveValue(choices[1].id);
    await input.press("Control+Enter");
    expect(sends).toEqual([]);
    await composer.getByRole("button", { name: "取消工作分配", exact: true }).click();
    await input.fill(`@${choices[0].name} @${choices[1].name} 粘贴背景`);
    await expect(composer).toBeVisible();
    await input.press("Meta+Enter");
    expect(sends).toEqual([]);
    await composer.getByRole("button", { name: "取消工作分配", exact: true }).click();
    await input.fill(`@${choices[0].name} @${choices[0].name} 单 Agent 保持直发`);
    await input.press("Control+Enter");
    await expect.poll(() => sends.length).toBe(1);
    expect(sends[0].body.mentioned_agent_ids).toEqual([choices[0].id]);
    expect(sends[0].body).not.toHaveProperty("work_items");
    await expect(input).toHaveValue("");
    await input.fill("@");
    const multiple = page.getByRole("button", { name: `给 ${choices[0].name} 分配多项工作`, exact: true });
    expect(await multiple.evaluate((button) => Boolean(button.parentElement.closest("button")))).toBe(false);
    await multiple.click();
    await composer.getByLabel("标题", { exact: true }).nth(0).fill("前置工作");
    await composer.getByLabel("标题", { exact: true }).nth(1).fill("后续工作");
    const second = composer.locator("fieldset").nth(1);
    await second.locator("details").nth(1).locator("summary").click();
    await second.getByRole("checkbox", { name: "前置工作", exact: true }).check();
    const geometry = await composer.locator('[data-field-group="issue-work-owner-title"]').first().evaluate((group) => {
      const boxes = [...group.querySelectorAll(".am-select, .am-input")].map((control) => {
        const { left, right, top, bottom, height } = control.getBoundingClientRect(); return { left, right, top, bottom, height };
      });
      return { boxes, width: innerWidth, scroll: document.documentElement.scrollWidth };
    });
    expect(geometry.scroll).toBeLessThanOrEqual(geometry.width + 1);
    expect(geometry.boxes[0].height).toBeGreaterThanOrEqual(width === 390 ? 44 : 36);
    if (width === 1280) for (const edge of ["top", "bottom", "height"]) expect(Math.abs(geometry.boxes[0][edge] - geometry.boxes[1][edge])).toBeLessThanOrEqual(1);
    else expect(geometry.boxes[1].top).toBeGreaterThanOrEqual(geometry.boxes[0].bottom);
    await page.screenshot({ path: info.outputPath(`issue-work-form-${width}.png`), animations: "disabled" });
    await expect(composer.locator("fieldset").first().getByRole("button", { name: "移除第 1 项工作", exact: true })).toBeDisabled();
    await expect(composer.locator("fieldset").first()).toContainText("被以下工作依赖：后续工作");
    await expect(second.locator("summary").nth(1)).toHaveText("等待 1 项全部成功");
    await expect(page.getByText("立即执行 1 项 · 等待 1 项", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "确认并启动 2 项", exact: true }).click();
    await expect(page.getByTestId("issue-composer-error")).toBeVisible();
    await expect(composer.getByLabel("标题", { exact: true }).nth(0)).toHaveValue("前置工作");
    await page.getByRole("button", { name: "确认并启动 2 项", exact: true }).click();
    await expect.poll(() => sends.length).toBe(3);
    expect(sends[1].key).toBeTruthy(); expect(sends[2].key).toBe(sends[1].key);
    expect(sends[2].body).toEqual(sends[1].body);
    expect(sends[2].body.mentioned_agent_ids).toEqual([]);
    const items = sends[2].body.work_items;
    expect(items.map((item) => item.agent_id)).toEqual([choices[0].id, choices[0].id]);
    expect(items[1].after).toEqual([items[0].key]);
    const list = page.getByRole("list", { name: "工作分配", exact: true });
    await expect(list.getByText("等待发布结果", { exact: true })).toBeVisible();
    completed = true;
    await expect(list.getByText("依赖阻塞", { exact: true })).toBeVisible({ timeout: 5_000 });
    await list.getByRole("button", { name: "打开会话", exact: true }).click();
    await expect(page.getByRole("dialog", { name: `${choices[0].name} · 会话 1`, exact: true })).toBeVisible();
    expect(plans).toBe(0); expect(forbidden).toEqual([]); expect(errors).toEqual([]);
    // The real server never received any fixture comment or execution request.
    expect((await api(`/issues/${issue.id}/comments`)).items).toEqual([]);
  });
});

test("桌面缩放：70% 单次原生 wheel -120 有界，框内滚动不缩放，切图取消旧 RAF", { tag: ["@auto-title", "@desktop", "@conversation-zoom"] }, async ({ page, request, baseURL }, info) => {
  const { api, project, choices, forbidden, errors } = await setup({ page, request, baseURL }, { agents: true });
  const first = await api(`/projects/${project.id}/conversation-graphs`, { title: "缩放图甲", agent_id: choices[0].id });
  const second = await api(`/projects/${project.id}/conversation-graphs`, { title: "缩放图乙", agent_id: choices[1].id });
  // Only long display history is synthetic; graph dimensions and navigation
  // remain real HTTP. No messages are posted to the service.
  await page.route(`**/v1/conversation-graphs/${first.graph.id}`, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const envelope = await response.json();
    envelope.data.messages = [{ id: "zoom-scroll-fixture", version: 1, created_at: first.graph.created_at, updated_at: first.graph.updated_at,
      node_id: first.nodes[0].id, author_kind: "user", status: "published", body: Array.from({ length: 90 }, (_, index) => `框内滚动第 ${index + 1} 段，不应影响画布。`).join("\n\n") }];
    await route.fulfill({ response, json: envelope });
  });
  await page.goto(`/conversations?graph=${first.graph.id}`);
  const canvas = canvasFor(page);
  const card = page.locator(`[data-node-id="${first.nodes[0].id}"]`);
  await expect(card).toBeVisible();
  const bounds = await canvas.boundingBox();
  const nodeHeight = (bounds.height - 100) / 0.7;
  expect(nodeHeight).toBeGreaterThanOrEqual(260); expect(nodeHeight).toBeLessThanOrEqual(1600);
  await api(`/conversation-nodes/${first.nodes[0].id}`, { expected_version: first.nodes[0].version, height: nodeHeight }, "PATCH");
  await page.reload();
  await expect.poll(async () => Math.abs(await card.evaluate((element) => parseFloat(element.style.height)) - nodeHeight)).toBeLessThan(0.01);
  await page.getByRole("button", { name: "重置视图", exact: true }).click();
  const before = await settledCamera(canvas);
  expect(before.scale).toBeCloseTo(0.7, 5);
  await expect(page.locator("main").getByText("70%", { exact: true })).toBeVisible();
  await canvas.evaluate((element) => {
    window.__zoomSamples = [];
    window.__zoomSampling = true;
    function sample() {
      const matrix = new DOMMatrix(getComputedStyle(element.firstElementChild).transform);
      window.__zoomSamples.push({ x: matrix.e, y: matrix.f, scale: matrix.a });
      if (window.__zoomSampling) requestAnimationFrame(sample);
    }
    requestAnimationFrame(sample);
  });
  const box = await canvas.boundingBox(), anchor = { x: 16, y: 16 };
  await page.mouse.move(box.x + anchor.x, box.y + anchor.y);
  await page.mouse.wheel(0, -120);
  await expect.poll(async () => (await camera(canvas)).scale).toBeGreaterThan(before.scale);
  const after = await settledCamera(canvas);
  const samples = await page.evaluate(() => { window.__zoomSampling = false; return window.__zoomSamples; });
  expect(samples.length).toBeGreaterThan(2);
  expect(after.scale).toBeLessThanOrEqual(before.scale * 1.1 + 0.00001);
  expect(Math.max(...samples.map((sample) => sample.scale))).toBeLessThanOrEqual(0.77001);
  for (let index = 1; index < samples.length; index++) expect(samples[index].scale / samples[index - 1].scale, "每帧增幅也有界，不能首帧跳到 200%").toBeLessThanOrEqual(1.04001);
  expect((anchor.x - after.x) / after.scale).toBeCloseTo((anchor.x - before.x) / before.scale, 3);
  expect((anchor.y - after.y) / after.scale).toBeCloseTo((anchor.y - before.y) / before.scale, 3);
  const scroller = card.locator("[data-conversation-message-scroll]");
  await scroller.evaluate((element) => { element.scrollTop = 0; });
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(0);
  const scrollBox = await scroller.boundingBox();
  await page.mouse.move(scrollBox.x + scrollBox.width / 2, scrollBox.y + 30);
  await page.mouse.wheel(0, 160);
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  expect(await settledCamera(canvas)).toEqual(after);
  await scroller.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await page.mouse.wheel(0, 160);
  expect(await settledCamera(canvas)).toEqual(after);

  // A scheduler fixture holds only RAFs synchronously requested by this wheel.
  // Restore requestAnimationFrame immediately; navigation/rendering is normal.
  // This proves cancellation even on fast machines where a real click would
  // otherwise race the final animation frame.
  const queued = await canvas.evaluate((element) => {
    const request = window.requestAnimationFrame.bind(window), cancel = window.cancelAnimationFrame.bind(window);
    const pending = new Map(), cancelled = [];
    window.requestAnimationFrame = (callback) => { const id = request(() => {}); pending.set(id, callback); return id; };
    window.cancelAnimationFrame = (id) => { if (pending.has(id)) cancelled.push(id); cancel(id); };
    window.__heldZoomFrames = { pending, cancelled, cancel };
    try { element.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, clientX: element.getBoundingClientRect().left + 16, clientY: element.getBoundingClientRect().top + 16, bubbles: true, cancelable: true })); }
    finally { window.requestAnimationFrame = request; }
    return [...pending.keys()];
  });
  expect(queued).toHaveLength(1);
  await page.getByRole("complementary", { name: "根节点", exact: true }).getByRole("button", { name: /缩放图乙/ }).click();
  await expect(page.locator(`[data-node-id="${second.nodes[0].id}"]`)).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.__heldZoomFrames.cancelled)).toEqual(queued);
  const switched = await camera(canvas);
  expect(switched).toEqual({ x: 40, y: 80, scale: 1 });
  const afterNavigation = await canvas.evaluate(async (element) => {
    const frames = [];
    for (let index = 0; index < 20; index++) {
      await new Promise((resolve) => requestAnimationFrame(resolve));
      const matrix = new DOMMatrix(getComputedStyle(element.firstElementChild).transform);
      frames.push({ x: matrix.e, y: matrix.f, scale: matrix.a });
    }
    window.cancelAnimationFrame = window.__heldZoomFrames.cancel;
    delete window.__heldZoomFrames;
    return frames;
  });
  for (const view of afterNavigation) expect(view).toEqual(switched);
  expect(forbidden).toEqual([]);
  expect(errors).toEqual([]);
  await info.attach("bounded-zoom-ui-fixture.json", { body: JSON.stringify({ before, after, samples, switched, afterNavigation }, null, 2), contentType: "application/json" });
});
