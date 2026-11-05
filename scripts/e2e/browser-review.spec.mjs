import { mkdtemp, rm, readdir, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// These tests exercise the real HTTP service without invoking a model. All
// projects/agents created here belong to the caller's isolated test data dir.
let project;
let agent;
let graph;
let node;
let memory;
let secondProject;
let headers;
const failures = [];

async function api(request, path, body, method = body === undefined ? "GET" : "POST") {
  const response = await request.fetch(`/v1${path}`, { method, data: body, headers });
  expect(response.ok(), `${method} ${path}: HTTP ${response.status()}`).toBeTruthy();
  return response.status() === 204 ? undefined : (await response.json()).data;
}

test.beforeAll(async ({ request, baseURL }) => {
  headers = { Origin: baseURL, "Content-Type": "application/json" };
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  const computers = await api(request, "/computers");
  const computer = computers.items[0];
  // Unique names keep reruns against the same isolated data dir deterministic.
  const runSuffix = Date.now();
  project = await api(request, "/projects", { computer_id: computer.id, name: `浏览器回归项目-${runSuffix}`, description: "隔离的真实 HTTP 与 Chrome 验收，不执行模型。" });
  const services = await api(request, `/computers/${computer.id}/agent-services`);
  const service = services.items.find((item) => item.status === "ready");
  if (service) {
    agent = await api(request, `/computers/${computer.id}/agents`, { name: "浏览器验收 Agent", agent_service_id: service.id, model: "", system_prompt: "仅用于界面配置验收。" });
    graph = await api(request, `/projects/${project.id}/conversation-graphs`, { title: "浏览器对话图", agent_id: agent.id });
    node = graph.nodes[0];
  }
  await api(request, `/projects/${project.id}/issues`, { title: "浏览器验收任务", description: "检查评论编辑、导航与响应式布局。", status: "open" });
  await api(request, `/projects/${project.id}/resources`, { kind: "url", name: "验收文档", url: "https://example.test/docs" });
  memory = await api(request, `/projects/${project.id}/memories`, { key: "review-architecture", kind: "fact", title: "本地架构", content: "浏览器与 Wails 使用同源 HTTP API。", source_type: "user" });
  await api(request, `/projects/${project.id}/memories`, { key: "review-second", kind: "fact", title: "切换目标", content: "第二条记忆。", source_type: "user" });
  secondProject = await api(request, "/projects", {
    computer_id: computer.id, name: `第二个项目-${Date.now()}`, description: "跨项目切换验收。"
  });
});

test.beforeEach(async ({ page }) => {
  failures.length = 0;
  page.on("pageerror", (error) => failures.push(error.message));
  page.on("console", (message) => { if (message.type() === "error" && !message.text().includes("503")) failures.push(message.text()); });
  await page.addInitScript(({ computerId, projectId }) => {
    sessionStorage.setItem("teamva-selection", JSON.stringify({ computerId, projectId }));
  }, { computerId: project.computer_id, projectId: project.id });
});

test.afterEach(() => { expect(failures).toEqual([]); });

test("根节点加号和移动入口直接创建空图，选择 Agent 后才创建根节点且不启动模型", { tag: ["@smoke", "@conversation-create"] }, async ({ page, request }, info) => {
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  const mutations = [];
  page.on("request", (request) => { if (request.method() === "POST") mutations.push(new URL(request.url()).pathname); });
  await page.goto("/conversations");
  const sidebar = page.getByRole("complementary", { name: "根节点", exact: true });
  const plus = sidebar.locator("header").getByRole("button", { name: "新建分支对话", exact: true });
  await expect(plus).toBeVisible();
  await expect(sidebar.locator("header")).toHaveText("根节点");
  await expect(plus).toHaveAttribute("type", "button");
  await expect(plus).toHaveAttribute("data-icon", "true");
  const plusBox = await plus.boundingBox(), headerBox = await sidebar.locator("header").boundingBox();
  expect(plusBox.y).toBeGreaterThanOrEqual(headerBox.y);
  expect(plusBox.y + plusBox.height).toBeLessThanOrEqual(headerBox.y + headerBox.height);
  const mainHeader = page.locator("header").filter({ has: page.getByRole("heading", { name: "分支对话", exact: true }) });
  const createPath = `/v1/projects/${project.id}/conversation-graphs`;
  const createdGraphs = [];
  async function createEmpty(action) {
    const pending = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === createPath);
    await action();
    const response = await pending;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toEqual({});
    const created = (await response.json()).data;
    createdGraphs.push(created.graph);
    expect(created.graph).toMatchObject({ title: "新对话", node_count: 0 });
    expect(created.nodes).toEqual([]);
    expect(created.messages).toEqual([]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect.poll(() => new URL(page.url()).searchParams.get("graph")).toBe(created.graph.id);
    expect(new URL(page.url()).searchParams.has("node")).toBe(false);
    await expect(page.getByLabel("图标题", { exact: true })).toHaveCount(0);
    const canvas = page.getByRole("application", { name: "对话画布", exact: true });
    await expect(canvas.locator("[data-node-id]")).toHaveCount(0);
    await expect(canvas.getByRole("combobox", { name: "Agent", exact: true })).toHaveValue("");
    await expect(canvas.getByRole("textbox", { name: "消息输入", exact: true })).toHaveCount(0);
    await expect(canvas.getByRole("button", { name: "创建", exact: true })).toBeDisabled();
    return created;
  }
  await plus.focus();
  const created = await createEmpty(() => page.keyboard.press("Enter"));
  await expect(sidebar.locator('button[aria-current="page"]')).toContainText("新对话");
  if (agent) {
    const canvas = page.getByRole("application", { name: "对话画布", exact: true });
    await canvas.getByRole("combobox", { name: "Agent", exact: true }).selectOption(agent.id);
    const rootResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/v1/conversation-graphs/${created.graph.id}/root`);
    await canvas.getByRole("button", { name: "创建", exact: true }).click();
    const response = await rootResponse;
    expect(response.ok()).toBe(true);
    expect(response.request().postDataJSON()).toEqual({ agent_id: agent.id, expected_version: created.graph.version });
    const root = (await response.json()).data;
    expect(root).toMatchObject({ graph_id: created.graph.id, agent_id: agent.id });
    await expect(page.locator(`[data-node-id="${root.id}"]`).getByRole("combobox", { name: "Agent", exact: true })).toHaveValue(agent.id);
  }
  await createEmpty(() => mainHeader.getByRole("button", { name: "新建分支对话", exact: true }).click());
  expect(mutations.some((path) => /\/(messages|runs|goals)(?:\/|$)/.test(path))).toBe(false);
  await page.screenshot({ path: info.outputPath("conversation-root-plus.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(sidebar).toBeHidden();
  await createEmpty(() => page.getByRole("button", { name: "新建分支对话", exact: true }).click());
  // This spec's later canvas tests own the beforeAll graph. Remove only the
  // metadata graphs created by this test so their newer timestamps cannot steal
  // their default graph selection.
  for (const item of createdGraphs) {
    const latest = await api(request, `/conversation-graphs/${item.id}`);
    await api(request, `/conversation-graphs/${item.id}?expected_version=${latest.graph.version}`, undefined, "DELETE");
  }
});

async function assertModelAPISetupGeometry(dialog, { short = false, wrapped = false } = {}) {
  const geometry = await dialog.evaluate((node) => {
    const rect = (element) => {
      const { left, right, top, bottom, width, height } = element.getBoundingClientRect();
      return { left, right, top, bottom, width, height };
    };
    const header = node.querySelector(":scope > header"), body = node.querySelector(".am-modal__body");
    const form = body.querySelector("form"), content = form.parentElement;
    const actions = form.querySelector('button[type="submit"]').parentElement;
    const headerBox = rect(header), headerStyle = getComputedStyle(header);
    return {
      panel: rect(node), viewport: { width: innerWidth, height: innerHeight },
      title: rect(header.querySelector("h2")),
      edges: { left: headerBox.left + parseFloat(headerStyle.paddingLeft), right: headerBox.right - parseFloat(headerStyle.paddingRight) },
      // All measurements are real border boxes, not class-name/overflow proxies.
      fullWidth: [content, content.querySelector(":scope > p"), form, form.querySelector("fieldset"), actions,
        ...form.querySelectorAll(".am-field, .am-select, input")].map(rect),
      actions: [...actions.querySelectorAll("button")].map(rect),
      body: { clientHeight: body.clientHeight, scrollHeight: body.scrollHeight, clientWidth: body.clientWidth, scrollWidth: body.scrollWidth }
    };
  });
  const { panel, viewport, title, edges, fullWidth, actions, body } = geometry;
  expect(Math.abs(panel.left + panel.width / 2 - viewport.width / 2)).toBeLessThanOrEqual(1);
  expect(Math.abs(panel.top + panel.height / 2 - viewport.height / 2)).toBeLessThanOrEqual(1);
  expect(panel.left).toBeGreaterThanOrEqual(8); expect(panel.right).toBeLessThanOrEqual(viewport.width - 8);
  expect(panel.top).toBeGreaterThanOrEqual(8); expect(panel.bottom).toBeLessThanOrEqual(viewport.height - 8);
  expect(Math.abs(title.left - edges.left)).toBeLessThanOrEqual(1);
  for (const box of fullWidth) {
    expect(Math.abs(box.left - edges.left)).toBeLessThanOrEqual(1);
    expect(Math.abs(box.right - edges.right)).toBeLessThanOrEqual(1);
  }
  expect(body.scrollWidth).toBeLessThanOrEqual(body.clientWidth + 1);
  if (short) expect(body.scrollHeight).toBeGreaterThan(body.clientHeight);
  expect(actions).toHaveLength(2);
  expect(Math.abs(actions.at(-1).right - edges.right)).toBeLessThanOrEqual(1);
  for (const box of actions) {
    expect(box.left).toBeGreaterThanOrEqual(edges.left - 1);
    expect(box.right).toBeLessThanOrEqual(edges.right + 1);
  }
  if (wrapped) {
    expect(actions[1].top).toBeGreaterThanOrEqual(actions[0].bottom);
    expect(Math.abs(actions[0].right - edges.right)).toBeLessThanOrEqual(1);
  } else {
    expect(Math.abs(actions[0].top - actions[1].top)).toBeLessThanOrEqual(1);
    expect(Math.abs(actions[0].bottom - actions[1].bottom)).toBeLessThanOrEqual(1);
  }
  // A short modal may scroll its body, but every field/action must remain reachable.
  for (const control of await dialog.locator("form .am-select, form input, form button[type=submit], form button[type=button]").all()) {
    await control.scrollIntoViewIfNeeded();
    const position = await control.evaluate((element) => {
      const box = element.getBoundingClientRect(), body = element.closest(".am-modal__body").getBoundingClientRect();
      return { top: box.top, bottom: box.bottom, bodyTop: body.top, bodyBottom: body.bottom };
    });
    expect(position.top).toBeGreaterThanOrEqual(position.bodyTop - 1);
    expect(position.bottom).toBeLessThanOrEqual(position.bodyBottom + 1);
  }
  const heading = await dialog.locator(":scope > header h2").boundingBox();
  expect(heading.y).toBeGreaterThanOrEqual(panel.top);
  expect(heading.y + heading.height).toBeLessThanOrEqual(panel.bottom);
}

test("only the current log route is supported", { tag: "@smoke" }, async ({ page, baseURL }) => {
  await page.goto("/activity");
  await expect(page.locator("main").getByText("找不到这个页面", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(`${baseURL}/activity`);
  await page.goto("/settings?tab=logs");
  await expect(page.getByRole("tab", { name: "日志", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("heading", { name: "日志", exact: true })).toBeVisible();
});

test("programming tools follows discovered source manifests", { tag: "@provider" }, async ({ page }) => {
  const root = new URL("../../node/provider/", import.meta.url);
  const manifests = [];
  for (const entry of await readdir(root, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    try { manifests.push(JSON.parse(await readFile(new URL(`${entry.name}/provider.json`, root), "utf8"))); }
    catch (error) { if (error.code !== "ENOENT") throw error; }
  }
  await page.goto("/programming-tools");
  // The header can show the same Provider name; assertions stay scoped to the
  // discovered tool list.
  const tools = page.getByRole("list", { name: "支持的编程工具" });
  for (const manifest of manifests) await expect(tools.getByText(manifest.display_name, { exact: true })).toBeVisible();
  await expect(page.locator("main").getByRole("button", { name: "诊断", exact: true })).toHaveCount(manifests.length);
});

test("programming tool executable path applies immediately and persists", { tag: "@provider" }, async ({ page, request }) => {
  // 每次测试自行引导会话：本地会话可能被浏览器启动流程轮换。
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  // 以一个干净的默认值开始，保证重复运行确定。
  await api(request, "/system/programming-tools/opencode", { path: "" }, "PUT");
  await page.goto("/settings?tab=cli");
  const section = page.getByRole("region", { name: "编程工具运行时" });
  await expect(section).toBeVisible();
  const openCodeEditor = section.locator('[data-provider-executable="opencode"]');
  const openCodeInput = openCodeEditor.getByRole("textbox", { name: "OpenCode 外部可执行文件路径" });
  await expect(openCodeInput).toHaveValue("");
  await expect(openCodeEditor.getByText("当前使用：", { exact: false })).toBeVisible();

  await openCodeInput.fill("/usr/bin/true");
  await expect(openCodeEditor.getByRole("button", { name: "保存路径", exact: true })).toHaveCount(0);
  await openCodeInput.press("Enter");
  await expect(openCodeEditor.getByText("当前使用：自定义路径", { exact: false })).toBeVisible();
  await expect(openCodeEditor.getByText("/usr/bin/true", { exact: false })).toBeVisible();

  // 重新加载后设置仍然存在，说明已持久化到控制面数据库。
  await page.reload();
  const reloaded = page
    .getByRole("region", { name: "编程工具运行时" })
    .locator('[data-provider-executable="opencode"]');
  await expect(reloaded.getByRole("textbox", { name: "OpenCode 外部可执行文件路径" })).toHaveValue("/usr/bin/true");

  // 失焦同样立即应用完整路径，并在重载后保留。
  const reloadedInput = reloaded.getByRole("textbox", { name: "OpenCode 外部可执行文件路径" });
  await reloadedInput.fill("/usr/bin/false");
  await reloadedInput.blur();
  await expect(reloaded.getByText("/usr/bin/false", { exact: false })).toBeVisible();
  await page.reload();
  await expect(reloadedInput).toHaveValue("/usr/bin/false");

  // 恢复默认会清除自定义路径并回到自动解析。
  await reloadedInput.fill("/unsubmitted/cli-draft");
  await reloaded.getByRole("button", { name: "恢复默认", exact: true }).click();
  await expect(reloaded.getByRole("textbox", { name: "OpenCode 外部可执行文件路径" })).toHaveValue("");
  await expect(reloaded.getByRole("button", { name: "恢复默认", exact: true })).toHaveCount(0);
});

test("generic Provider API events render without a native frontend codec", { tag: "@provider" }, async ({ page }, info) => {
  // Synthetic graph IDs are not persisted business objects. Navigation
  // admission is covered separately by the real @work-history flow.
  await page.route("**/v1/work-records/navigation", route=>route.fulfill({status:204}));
  // Real Chrome/SSE parser with protocol fixtures, not a real model invocation.
  const stamp = "2026-09-14T00:00:00Z", versioned = { version: 1, created_at: stamp, updated_at: stamp };
  const graphFixture = { ...versioned, id: "provider-ui-graph", title: "Provider API", project_id: project.id, node_count: 1 };
  const nodeFixture = { ...versioned, id: "provider-ui-node", graph_id: graphFixture.id, agent_id: "provider-ui-agent", title: "通用 CLI",
    status: "running", locked: false, width: 380, height: 560, position_x: 0, position_y: 0, message_count: 1 };
  const message = { ...versioned, id: "provider-ui-message", node_id: nodeFixture.id, author_kind: "agent", status: "working", run_id: "provider-ui-run", body: "" };
  const reply = (route, data) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
  await page.route("**/v1/computers/*/agents", (route) => reply(route, { items: [{ ...versioned, id: nodeFixture.agent_id, name: "通用 CLI", status: "active" }] }));
  await page.route("**/v1/projects/*/conversation-graphs", (route) => reply(route, { items: [graphFixture] }));
  await page.route("**/v1/conversation-graphs/provider-ui-graph", (route) => reply(route, { graph: graphFixture, nodes: [nodeFixture], messages: [message], goals: [] }));
  const events = [
    ["output.delta", { message_id: "m1", text: "通用 Provider " }],
    ["reasoning.snapshot", { id: "r1", text: "检查执行结果", status: "completed" }],
    ["provider.tool", { id: "t1", tool: "bash", input: { command: "test-command" }, output: "失败详情", exit_code: 7, status: "failed" }],
    ["output.snapshot", { id: "m1", text: "通用 Provider 回复", final: true }],
    ["run.finished", { status: "succeeded" }]
  ].map(([event_type, payload], index) => ({ id: `provider-event-${index}`, run_id: message.run_id, seq: index + 1,
    event_type, payload: { ...payload, provider_id: "futurecli" }, occurred_at: stamp }));
  await page.route("**/v1/runs/provider-ui-run/events/stream?*", (route) => route.fulfill({
    status: 200, contentType: "text/event-stream", body: events.map((event) => `event: ${event.event_type}\ndata: ${JSON.stringify(event)}\n\n`).join("")
  }));
  await page.goto("/conversations");
  const card = page.locator('[data-node-id="provider-ui-node"]');
  await expect(card.locator(".conversation-stream-reply")).toHaveText("通用 Provider 回复");
  await expect(card.locator('[data-activity-kind="reasoning"]')).toContainText("检查执行结果");
  const tool = card.locator('[data-activity-kind="tool"]');
  await expect(tool).toHaveAttribute("data-activity-status", "failed");
  await tool.getByRole("button").click();
  await expect(tool).toContainText("失败详情");
  await expect(tool).toContainText('"exit_code": 7');
  await page.screenshot({ path: info.outputPath("provider-semantic-events.png") });
});

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) {
  test.describe(`read-only issue description ${viewport.width}`, () => {
    test.use({ viewport, hasTouch: viewport.width === 390 });
    test("task description stays read-only while clicking the title still edits it", { tag: ["@smoke", "@issue-title"] }, async ({ page, request }, info) => {
      const session = await api(request, "/auth/local-session", {});
      headers["X-CSRF-Token"] = session.csrf_token;
      const description = "创建时保存的任务描述，此后保持只读。";
      const issue = await api(request, `/projects/${project.id}/issues`, { title: `原标题-${Date.now()}`, description });
      const patches = [];
      page.on("request", (request) => {
        if (request.method() === "PATCH" && new URL(request.url()).pathname === `/v1/issues/${issue.id}`) patches.push(request.postDataJSON());
      });
      await page.goto(`/issues?issue=${issue.id}`);
      await expect(page.getByRole("heading", { name: issue.title, exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: "编辑标题与描述", exact: true })).toHaveCount(0);
      const body = page.locator(`[data-issue-description="${issue.id}"]`);
      await expect(body).toContainText(description);
      await expect(body.locator("input,textarea,[contenteditable=true],button")).toHaveCount(0);
      await expect(page.getByLabel("描述", { exact: true })).toHaveCount(0);
      await page.getByRole("button", { name: "编辑任务标题", exact: true }).click();
      const title = page.getByRole("textbox", { name: "标题", exact: true });
      await expect(title).toBeFocused();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await title.fill("点击标题后仍可修改");
      const firstSave = page.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === `/v1/issues/${issue.id}`);
      await title.press("Enter");
      expect((await firstSave).ok()).toBe(true);
      await expect(page.getByRole("heading", { name: "点击标题后仍可修改", exact: true })).toBeVisible();
      expect(patches).toEqual([{ title: "点击标题后仍可修改", expected_version: issue.version }]);
      await page.getByRole("button", { name: "编辑任务标题", exact: true }).click();
      await title.fill("不应保存的标题草稿");
      await title.press("Escape");
      await expect(page.getByRole("heading", { name: "点击标题后仍可修改", exact: true })).toBeVisible();
      expect(patches).toHaveLength(1);
      await page.getByRole("button", { name: "编辑任务标题", exact: true }).click();
      await title.fill("失焦也可以保存标题");
      const secondSave = page.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === `/v1/issues/${issue.id}`);
      await body.click();
      expect((await secondSave).ok()).toBe(true);
      await expect(page.getByRole("heading", { name: "失焦也可以保存标题", exact: true })).toBeVisible();
      const saved = await api(request, `/issues/${issue.id}`);
      expect(saved.description).toBe(description);
      expect(patches[1]).toEqual({ title: saved.title, expected_version: issue.version + 1 });
      const blocked = await request.patch(`/v1/issues/${issue.id}`, { headers, data: { expected_version: saved.version, description: "禁止覆盖的描述", title: "不能部分提交标题" } });
      expect(blocked.status()).toBe(400);
      expect(await api(request, `/issues/${issue.id}`)).toMatchObject({ title: saved.title, description, version: saved.version });
      await page.reload();
      await expect(page.getByRole("heading", { name: saved.title, exact: true })).toBeVisible();
      await expect(body).toContainText(description);
      await page.screenshot({ path: info.outputPath(`issue-read-only-description-${viewport.width}.png`) });
    });
  });
}

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) {
test.describe(`${viewport.width}px interaction stability`, () => {
test.use({ viewport });

test("global inspiration board edits and persists Markdown", { tag:["@inspirations","@smoke"] }, async ({page,request,baseURL},info)=>{
  const session=await api(request,"/auth/local-session",{});headers["X-CSRF-Token"]=session.csrf_token;
  const title=`灵感-${viewport.width}-${Date.now()}`;
  const response=await request.post("/v1/inspirations",{headers:{...headers,"Idempotency-Key":randomUUID()},data:{project_id:secondProject.id,title,markdown:"# 初始标题\n\n- [ ] 核对想法\n\n[文档](https://example.test)\n"}});
  expect(response.status()).toBe(201);const note=(await response.json()).data;
  await page.goto(`/inspirations?note=${note.id}`);
  await expect(page.getByRole("heading",{name:"灵感",exact:true})).toBeVisible();
  const workspace=page.getByRole("region",{name:"Markdown 所见即所得编辑器",exact:true});
  const editor=page.getByRole("textbox",{name:"灵感正文",exact:true});
  await expect(editor).toBeVisible();
  await expect(editor).toBeEditable();
  await expect(editor.locator("h1")).toHaveText("初始标题");
  // Crepe renders task checkboxes as its own list-item component, not inputs.
  await expect(editor.getByRole("listitem")).toContainText("核对想法");
  await editor.click();await page.keyboard.press("ControlOrMeta+End");await page.keyboard.press("Enter");await page.keyboard.type("所见即所得编辑",{delay:15});
  for(const _ of "所见即所得编辑")await page.keyboard.press("Shift+ArrowLeft");
  expect(await page.evaluate(()=>window.getSelection()?.toString())).toBe("所见即所得编辑");
  await page.keyboard.press("ControlOrMeta+b");
  await expect(editor.locator("strong,b")).toContainText("所见即所得编辑");
  await workspace.getByRole("button",{name:"Markdown 源码",exact:true}).click();
  const source=workspace.getByRole("textbox",{name:"Markdown 源码",exact:true});
  await expect(source).toHaveValue(/\*\*所见即所得编辑\*\*/);
  const markdown=await source.inputValue();
  expect(markdown).toMatch(/[-*+]\s+\[ \]\s+核对想法/);
  const saveNote=workspace.getByRole("button",{name:"保存",exact:true});if(await saveNote.isEnabled())await saveNote.click();
  await expect.poll(async()=> (await api(request,`/inspirations/${note.id}`)).markdown).toBe(markdown);
  await workspace.getByRole("button",{name:"所见即所得",exact:true}).click();
  await expect(editor.locator("strong,b")).toContainText("所见即所得编辑");
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath("inspiration-editor.png")});
  await page.getByRole("complementary",{name:"灵感",exact:true}).getByRole("button",{name:"关闭",exact:true}).click();
  const group=page.locator(`[data-inspiration-group="${secondProject.id}"]`);const card=group.locator(`[data-inspiration="${note.id}"]`);
  await expect(card).toBeVisible();await card.getByRole("button",{name:"标记已完成",exact:true}).click();
  await expect(card).toHaveAttribute("data-completed","true");await page.screenshot({path:info.outputPath("inspiration-completed.png")});await page.reload();
  await expect(group.locator(`[data-inspiration="${note.id}"]`)).toHaveAttribute("data-completed","true");
  // Deep links reopen the editor after reload; close it to use the board.
  const panel=page.getByRole("complementary",{name:"灵感",exact:true});if(await panel.isVisible())await panel.getByRole("button",{name:"关闭",exact:true}).click();
  await card.getByRole("button",{name:"恢复未完成",exact:true}).click();await expect(card).toHaveAttribute("data-completed","false");
  await page.screenshot({path:info.outputPath("inspiration-board.png")});
  await card.getByRole("button",{name:title,exact:true}).click();await page.getByRole("tab",{name:"灵感助手",exact:true}).click();
  const assistant=page.getByRole("complementary",{name:"灵感助手",exact:true});await expect(assistant).toBeVisible();await expect(assistant.getByText(title,{exact:true})).toBeVisible();
  await assistant.getByRole("button",{name:"新建会话",exact:true}).click();
  await expect.poll(async()=> (await api(request,"/work-assistant/sessions?scope=inspirations")).items[0]?.scope).toBe("inspirations");
  const exported=await request.get(`${baseURL}/v1/inspirations/${note.id}/markdown`);expect(await exported.text()).toBe(markdown);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.goto("/inspirations");
  await page.locator('[data-inspiration-group="ungrouped"]').getByRole("button",{name:"新建灵感 · 未分组",exact:true}).click();
  await expect(page).toHaveURL(/\/inspirations\?note=ins_/);
  const quickID=new URL(page.url()).searchParams.get("note");expect(quickID).toBeTruthy();
  await expect(editor).toBeVisible();
  await workspace.getByRole("textbox",{name:"标题",exact:true}).fill(`快速便签-${viewport.width}`);
  await editor.fill("正在编辑的中文想法");
  await page.getByRole("tab",{name:"灵感助手",exact:true}).click();
  await expect(page.getByRole("tab",{name:"灵感助手",exact:true})).toHaveAttribute("aria-selected","true");
  const quick=await api(request,`/inspirations/${quickID}`);
  expect(quick.markdown).toContain("正在编辑的中文想法");expect(quick.title).toBe(`快速便签-${viewport.width}`);expect(quick.project_id).toBe("");
  await page.getByRole("tab",{name:"编辑",exact:true}).click();
  await workspace.getByRole("combobox",{name:"项目",exact:true}).selectOption(secondProject.id);
  await expect.poll(async()=> (await api(request,`/inspirations/${quickID}`)).project_id).toBe(secondProject.id);
});

test("work history filters, links, logs and protected assistant", { tag: ["@work-history", "@smoke"] }, async ({page,request,baseURL},info) => {
  const session=await api(request,"/auth/local-session",{});headers["X-CSRF-Token"]=session.csrf_token;
  const title=`工作轨迹-${viewport.width}-${Date.now()}`;
  const issue=await api(request,`/projects/${secondProject.id}/issues`,{title,description:"test-only-body-not-in-work-history",status:"open"});
  const journal=await api(request,"/work-records/query",{search:issue.id});
  expect(journal.items.some(item=>item.object_id===issue.id&&item.outcome==="succeeded")).toBe(true);
  expect(JSON.stringify(journal).includes("test-only-body-not-in-work-history")).toBe(false);
  await page.goto("/work-records");
  await expect(page.getByRole("heading",{name:"工作记录",exact:true})).toBeVisible();
  await page.getByPlaceholder("关键词或完整 ID",{exact:true}).fill(issue.id);
  await page.getByRole("button",{name:"查询",exact:true}).click();
  const row=page.locator("[data-work-record]").filter({hasText:title});
  await expect(row).toHaveCount(1);
  await row.getByRole("button",{name:/详情/}).click();
  await expect(page.locator("[data-work-detail]")).toBeVisible();
  await page.getByPlaceholder("筛选名称",{exact:true}).fill(`本轮-${viewport.width}`);
  await page.getByRole("button",{name:"保存筛选",exact:true}).click();
  await expect(page.getByRole("combobox",{name:"已保存筛选",exact:true})).toContainText(`本轮-${viewport.width}`);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath("work-history.png")});
  await page.locator("[data-work-detail]").getByRole("button",{name:new RegExp(`issue · ${title}`)}).click();
  await expect(page).toHaveURL(new RegExp(`/issues\\?issue=${issue.id}.*project=${secondProject.id}`));
  await expect(page.getByRole("heading",{name:title,exact:true})).toBeVisible();
  const nav=await api(request,"/work-records/query",{include_changes:false,filter:{field:"kind",operator:"eq",value:"navigation"}});
  expect(nav.items.length).toBeGreaterThan(0);
  await page.goto("/settings?tab=logs");
  await expect(page.getByRole("tab",{name:"日志",exact:true})).toHaveAttribute("aria-selected","true");
  await expect(page.getByRole("heading",{name:"日志",exact:true})).toBeVisible();
  await page.goto("/work-records");
  const assistant=page.getByRole("complementary",{name:"工作回顾助手",exact:true});
  const toggleAssistant=page.getByRole("button",{name:"工作回顾助手",exact:true});
  await expect(toggleAssistant).toBeVisible();
  if((await toggleAssistant.getAttribute("aria-pressed")) === "false") await toggleAssistant.click();
  await expect(assistant).toBeVisible();
  await expect(assistant.getByText("只读查询 · Lumi",{exact:true})).toBeVisible();
  await assistant.getByRole("button",{name:"新建会话",exact:true}).click();
  await expect(assistant.getByRole("combobox",{name:"历史会话",exact:true})).toContainText("工作回顾助手");
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath("work-assistant.png")});
  const general=await api(request,"/work-assistant");
  expect(general.system_role).toBe("general");
  const rejected=await request.delete(`/v1/agents/${general.id}?expected_version=${general.version}`,{headers});
  expect(rejected.status()).toBe(403);
  await assistant.getByRole("button",{name:"关闭",exact:true}).click();
  await page.getByPlaceholder("关键词或完整 ID",{exact:true}).fill(issue.id);
  await page.getByRole("button",{name:"查询",exact:true}).click();
  // Creating the task and visiting it are two distinct, intended user actions.
  await expect(page.locator("[data-work-record]").filter({hasText:title})).toHaveCount(2);
  await page.getByRole("button",{name:"清除记录",exact:true}).click();
  await page.getByRole("dialog").getByRole("button",{name:"清除记录",exact:true}).click();
  await expect(page.getByText("没有匹配的工作记录",{exact:true})).toBeVisible();
  expect((await request.get(`${baseURL}/v1/issues/${issue.id}`)).status()).toBe(200);
});

test("remote access toggle, password and local-only settings", { tag: ["@smoke", "@remote"] }, async ({ page, request, browser, baseURL }, info) => {
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  // Passwords are generated in memory; no traces, screenshots of credentials,
  // URLs containing credentials, or real user data are used by this test.
  const password = `test-only-${randomUUID()}`;
  const nextPassword = `test-only-${randomUUID()}`;
  let remoteContext;
  const submit = async (action = () => page.getByLabel("访问密码", { exact: true }).press("Enter")) => {
    const pending = page.waitForResponse((response) => response.url().endsWith("/v1/system/network-access") && response.request().method() === "PUT");
    await action();
    const response = await pending;
    expect(response.status()).toBe(200);
    const text = await response.text();
    expect(text.includes(password) || text.includes(nextPassword) || text.includes("password_hash")).toBe(false);
    return JSON.parse(text).data;
  };
  try {
    const current = await api(request, "/system/network-access");
    await api(request, "/system/network-access", { enabled: false }, "PUT");
    await page.goto("/settings");
    const toggle = page.getByRole("switch", { name: "允许远程访问", exact: true });
    const passwordInput = page.getByLabel("访问密码", { exact: true });
    await expect(toggle).not.toBeChecked();
    await toggle.check();
    await expect(page.getByRole("button", { name: "保存远程访问设置", exact: true })).toHaveCount(0);
    if (!current.password_set) expect((await api(request, "/system/network-access")).external_access).toBe(false);
    await expect(page.getByRole("spinbutton")).toHaveCount(0);
    await passwordInput.fill(password);
    const enabled = await submit();
    expect(enabled.external_access).toBe(true);
    expect(enabled.password_set).toBe(true);
    await expect(passwordInput).toHaveValue("");
    await expect(page).toHaveURL(`${baseURL}/settings`);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.locator("section").filter({ has: toggle }).screenshot({ path: info.outputPath("remote-settings.png"), animations: "disabled" });
    // One port, two security boundaries: loopback stays passwordless while
    // the same port on the LAN address always requires remote authentication.
    expect(String(enabled.port)).toBe(new URL(baseURL).port);
    expect(enabled.external_urls.length).toBeGreaterThan(0);
    const remoteOrigin = enabled.external_urls[0];
    expect((await request.get(`${baseURL}/readyz`)).status()).toBe(200);
    const denied = await request.get(`${remoteOrigin}/readyz`);
    expect(denied.status()).toBe(401);
    expect(denied.headers()["www-authenticate"]).toBeUndefined();
    remoteContext = await browser.newContext({ locale: "zh-CN", viewport });
    const remotePage = await remoteContext.newPage();
    const destination = `${remoteOrigin}/settings?source=remote-test`;
    await remotePage.goto(destination);
    await expect(remotePage.getByRole("heading", { name: "登录远程工作台", exact: true })).toBeVisible();
    await expect(remotePage.locator('input[type="password"]')).toHaveCount(1);
    await expect(remotePage.locator('input[name="username"]')).toHaveCount(0);
    await expect(remotePage.getByRole("checkbox", { name: "信任此设备", exact: true })).not.toBeChecked();
    await expect(remotePage.getByText(/直到访问密码被修改/)).toBeVisible();
    expect(new URL(remotePage.url()).pathname).toBe("/remote/login");
    expect(await remotePage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await remotePage.screenshot({ path: info.outputPath("remote-login.png"), animations: "disabled" });
    const enter = async (value) => {
      await remotePage.getByLabel("访问密码", { exact: true }).fill(value);
      await remotePage.getByRole("button", { name: "登录并继续", exact: true }).click();
    };
    await enter("test-only-wrong-password");
    await expect(remotePage.getByRole("alert")).toHaveText("密码不正确，请重新输入。");
    await enter(password);
    await expect(remotePage).toHaveURL(destination);
    expect(await remotePage.evaluate(() => document.cookie.includes("am_remote_"))).toBe(false);
    await remotePage.reload();
    await expect(remotePage).toHaveURL(destination);
    await expect(remotePage.getByRole("switch", { name: "允许远程访问", exact: true })).toBeDisabled();
    await expect(remotePage.getByLabel("访问密码", { exact: true })).toBeDisabled();
    await expect(remotePage.getByText("请在本机窗口或 localhost 页面修改远程访问设置。", { exact: true })).toBeVisible();
    expect(await remotePage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await passwordInput.fill(nextPassword);
    await submit();
    const stale = await remoteContext.request.get(`${remoteOrigin}/readyz`);
    expect(stale.status()).toBe(401);
    // A rejected API request returns the running SPA to its own login route.
    await remotePage.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(remotePage.getByRole("heading", { name: "登录远程工作台", exact: true })).toBeVisible();
    await enter(password);
    await expect(remotePage.getByRole("alert")).toHaveText("密码不正确，请重新输入。");
    await remotePage.getByRole("checkbox", { name: "信任此设备", exact: true }).check();
    await enter(nextPassword);
    await expect(remotePage).toHaveURL(destination);
    await expect(remotePage.getByRole("switch", { name: "允许远程访问", exact: true })).toBeChecked();
    const remembered = await remoteContext.storageState(); // Memory only; never write cookie values to artifacts.
    const trustCookie = remembered.cookies.find((cookie) => cookie.name === `am_remote_${enabled.port}`);
    // This is Chrome's renewable cookie retention, not a server trust TTL.
    expect(Boolean(trustCookie?.httpOnly && trustCookie.sameSite === "Strict" && trustCookie.expires > Date.now() / 1000 + 399 * 86400)).toBe(true);
    await remoteContext.close();
    remoteContext = await browser.newContext({ locale: "zh-CN", viewport, storageState: remembered });
    const reopened = await remoteContext.newPage();
    await reopened.goto(destination);
    await expect(reopened.getByRole("switch", { name: "允许远程访问", exact: true })).toBeChecked();
    await expect(reopened).toHaveURL(destination);
    // Even a bookmark of the login page should honor this browser's trust.
    await reopened.goto(`${remoteOrigin}/remote/login?next=${encodeURIComponent("/settings?source=remote-test")}`);
    await expect(reopened).toHaveURL(destination);
    const disabled = await submit(() => toggle.uncheck());
    expect(disabled.external_access).toBe(false);
    expect(disabled.external_urls).toEqual([]);
    await expect(page).toHaveURL(`${baseURL}/settings`);
    expect((await request.get(`${baseURL}/readyz`)).status()).toBe(200);
    let connected = false;
    try { await request.get(`${remoteOrigin}/readyz`, { timeout: 1500 }); connected = true; } catch { /* Expected: LAN listener closed; loopback remains. */ }
    expect(connected).toBe(false);
    await submit(() => toggle.check());
    await reopened.goto(destination);
    await expect(reopened).toHaveURL(destination);
    await expect(reopened.getByRole("switch", { name: "允许远程访问", exact: true })).toBeChecked();
    // Only a real password change, not disabling/re-enabling LAN, forgets it.
    await passwordInput.fill(password);
    await submit();
    expect((await remoteContext.request.get(`${remoteOrigin}/readyz`)).status()).toBe(401);
  } finally {
    await remoteContext?.close();
    await api(request, "/system/network-access", { enabled: false }, "PUT");
  }
});

test("memory drafts and expanded history survive save/refetch", { tag: "@stability" }, async ({ page, request }, info) => {
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  const entry = await api(request, `/projects/${project.id}/memories`, {
    key: `stability-${Date.now()}`, kind: "fact", title: "稳定性记忆", content: "初始内容", source_type: "user"
  });
  await page.goto(`/memories?memory=${entry.id}`);
  const content = page.getByRole("textbox", { name: "内容", exact: true });
  await expect(content).toHaveValue("初始内容");
  await content.fill("第一次保存");
  await page.locator(`[data-memory-id="${entry.id}"]`).click();
  await expect(content).toHaveValue("第一次保存");
  await page.getByRole("button", { name: "历史", exact: true }).click();
  const history = page.getByText("v1 · 稳定性记忆", { exact: true });
  await expect(history).toBeVisible();

  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let intercepted = false;
  await page.route(`**/v1/projects/${project.id}/memories/${entry.id}`, async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    intercepted = true;
    await gate;
    await route.continue();
  });
  try {
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect.poll(() => intercepted).toBe(true);
    await content.fill("请求期间继续输入");
    const saved = page.waitForResponse((response) => response.url().endsWith(`/memories/${entry.id}`) && response.request().method() === "PATCH");
    release();
    expect((await saved).ok()).toBe(true);
    await expect(page.getByText("v2 · 稳定性记忆", { exact: true })).toBeVisible();
    await expect(history).toBeVisible();
    await expect(content).toHaveValue("请求期间继续输入");
    const nextSave = page.waitForResponse((response) => response.url().endsWith(`/memories/${entry.id}`) && response.request().method() === "PATCH");
    await page.getByRole("button", { name: "保存", exact: true }).click();
    expect((await nextSave).ok()).toBe(true);
    await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
    await page.reload();
    await expect(content).toHaveValue("请求期间继续输入");
    await page.screenshot({ path: info.outputPath("memory-stable.png") });
  } finally { release(); }
});

test("agent revision loading and save do not move editor or list rows", { tag: "@stability" }, async ({ page, request }, info) => {
  test.skip(!agent, "本机没有 ready Provider，跳过真实 Agent 配置界面验收。");
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  await api(request, `/computers/${project.computer_id}/agents`, {
    name: "第二个稳定性 Agent", agent_service_id: agent.agent_service_id, model: "", system_prompt: "只检查配置，不执行模型。"
  });
  let releaseRevision;
  const revisionGate = new Promise((resolve) => { releaseRevision = resolve; });
  await page.route("**/v1/agent-revisions/*", async (route) => { await revisionGate; await route.continue(); });
  await page.goto(`/agents?agent=${agent.id}`);
  const name = page.getByRole("textbox", { name: "名称", exact: true });
  try {
    await expect(name).toBeDisabled();
    const loadingBox = await name.boundingBox();
    releaseRevision();
    await expect(name).toBeEnabled();
    const loadedBox = await name.boundingBox();
    expect(Math.abs(loadedBox.y - loadingBox.y)).toBeLessThanOrEqual(1);
    const rows = page.locator("[data-agent-id]");
    const initialOrder = await rows.evaluateAll((elements) => elements.map((element) => element.dataset.agentId));
    let releaseRefresh;
    const refreshGate = new Promise((resolve) => { releaseRefresh = resolve; });
    let intercepted = false;
    await page.route(`**/v1/computers/${project.computer_id}/agents`, async (route) => {
      intercepted = true;
      await refreshGate;
      await route.continue();
    });
    try {
      await name.fill(`${viewport.width}px 改名后不跳到列表末尾`);
      await page.getByRole("button", { name: "保存", exact: true }).click();
      await expect.poll(() => intercepted).toBe(true);
      expect(await rows.evaluateAll((elements) => elements.map((element) => element.dataset.agentId))).toEqual(initialOrder);
      releaseRefresh();
      await expect(name).toBeEnabled();
      expect(await rows.evaluateAll((elements) => elements.map((element) => element.dataset.agentId))).toEqual(initialOrder);
      await info.attach("editor-geometry", { body: JSON.stringify({ loadingBox, loadedBox, initialOrder }), contentType: "application/json" });
      await page.screenshot({ path: info.outputPath("agent-stable.png") });
    } finally { releaseRefresh(); }
  } finally { releaseRevision(); }
});

test("settings preserves and automatically saves a newer theme while saving", { tag: "@stability" }, async ({ page, request }) => {
  await page.goto("/settings");
  const theme = page.getByRole("combobox", { name: "主题", exact: true });
  await expect(theme).toBeVisible();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let intercepted = false;
  await page.route("**/v1/me/preferences", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    intercepted = true;
    await gate;
    await route.continue();
  });
  try {
    await theme.selectOption("dark");
    await expect.poll(() => intercepted).toBe(true);
    await theme.selectOption("light");
    release();
    await expect(theme).toHaveValue("light");
    await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.theme).toBe("light");
    await expect(page.getByRole("button", { name: "保存设置", exact: true })).toHaveCount(0);
  } finally { release(); }
});

});
}

test("fresh browser entry, refresh and direct history route", { tag: "@smoke" }, async ({ page }) => {
  await page.goto("/workbench");
  await expect(page.getByRole("button", { name: project.name, exact: true })).toBeVisible();
  const before = (await page.context().cookies()).find((cookie) => cookie.name === "am_session");
  expect(Boolean(before?.httpOnly) && before?.sameSite === "Lax").toBeTruthy();
  await page.reload();
  await expect(page.getByRole("button", { name: project.name, exact: true })).toBeVisible();
  const after = (await page.context().cookies()).find((cookie) => cookie.name === "am_session");
  expect(Boolean(before?.value) && before.value === after?.value, "refresh should reuse the HttpOnly session").toBeTruthy();
  await page.goto("/does-not-exist");
  await expect(page.getByText("找不到这个页面")).toBeVisible();
});

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) {
  for (const path of ["workbench", "issues", "conversations", "resources", "memories", "schedules", "agents", "skills", "programming-tools", "settings", "work-records", "inspirations", "onboarding", "changes"]) {
    const tags = ["@layout", viewport.width < 768 ? "@mobile" : "@desktop"];
    if (viewport.width === 1280 && ["workbench", "issues", "settings"].includes(path)) tags.push("@smoke");
    test(`${viewport.width}px ${path}: no page overflow, usable content`, { tag: tags }, async ({ page }, info) => {
      await page.setViewportSize(viewport);
      await page.goto(`/${path}`);
      if (path === "changes") await expect(page.getByRole("heading", { name: "没有 Git 仓库" })).toBeVisible();
      else await page.waitForLoadState("networkidle");
      await expect(page.locator("main")).toBeVisible();
      await expect(page.locator("main")).not.toBeEmpty();
      await expect(page.getByRole("alert")).toHaveCount(0);
      await page.screenshot({ path: info.outputPath(`${path}-${viewport.width}.png`), fullPage: true, animations: "disabled" });
      const geometry = await page.evaluate(() => {
        const root = document.documentElement;
        const main = document.querySelector("main").getBoundingClientRect();
        return { scroll: root.scrollWidth, width: innerWidth, bottom: main.bottom, height: innerHeight };
      });
      expect(geometry.scroll).toBeLessThanOrEqual(geometry.width + 1);
      expect(geometry.bottom).toBeLessThanOrEqual(geometry.height - (viewport.width < 768 ? 58 : 0) + 1);
    });
  }
}

test("graph drag persists and failed send preserves draft", { tag: "@smoke" }, async ({ page }) => {
  test.skip(!agent, "本机尚无 ready Provider；不伪造 Agent 执行证据。");
  await page.goto("/conversations");
  const card = page.locator(`[data-node-id="${node.id}"]`);
  await expect(card).toBeVisible();
  const grip = card.getByRole("button", { name: "拖动节点" });
  const box = await grip.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + 100, box.y + box.height / 2 + 60, { steps: 8 });
  await expect(card).toHaveCSS("left", "100px");
  await page.mouse.up();
  await page.reload();
  await expect(page.locator(`[data-node-id="${node.id}"]`)).toHaveCSS("left", "100px");
  await page.route("**/v1/conversation-nodes/*/messages", (route) => route.fulfill({
    status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "runtime_unavailable", message: "unavailable" } })
  }));
  const field = card.getByRole("textbox", { name: "消息输入" });
  await field.fill("失败后这段草稿必须保留");
  await card.getByRole("button", { name: "发送", exact: true }).click();
  await expect(card.getByRole("alert")).toBeVisible();
  await expect(field).toHaveValue("失败后这段草稿必须保留");
});

test("mobile can create and manage projects", { tag: "@mobile" }, async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/workbench");
  await page.getByRole("button", { name: "更多", exact: true }).click();
  await page.getByRole("button", { name: "新建项目", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const mobileProjectName = `移动端创建项目-${Date.now()}`;
  await dialog.getByRole("textbox", { name: "项目名称" }).fill(mobileProjectName);
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("button", { name: mobileProjectName, exact: true })).toBeVisible();
  await page.getByRole("button", { name: "更多", exact: true }).click();
  await page.getByRole("button", { name: "管理项目", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("switching projects clears cross-project selection state", async ({ page }) => {
  await page.goto(`/memories?memory=${memory.id}`);
  const title = page.getByRole("textbox", { name: "标题", exact: true });
  await expect(title).toHaveValue("本地架构");
  await page.getByRole("button", { name: "选择项目", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: secondProject.name, exact: true }).click();
  await expect(page.getByRole("button", { name: "选择项目", exact: true })).toHaveText(secondProject.name);
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem("teamva-selection")).projectId)).toBe(secondProject.id);
  // 显式深链在切项目后仍保留 URL。新行为必须明确显示不可用，而不是
  // 回退首条可编辑记忆或把跨项目链接伪装成普通首次创建空态。
  await expect(page.getByText("暂无项目记忆", { exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("无法打开指定的记忆");
  await expect(title).toHaveCount(0);
  await expect(page.getByRole("button", { name: "保存", exact: true })).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get("memory")).toBe(memory.id);
  await page.getByRole("button", { name: "选择项目", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: project.name, exact: true }).click();
  await expect(title).toHaveValue("本地架构");
  expect(new URL(page.url()).searchParams.get("memory")).toBe(memory.id);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("memory default selection protects unsaved edits", async ({ page }) => {
  await page.goto("/memories");
  const title = page.getByRole("textbox", { name: "标题", exact: true });
  await expect(title).toHaveValue("本地架构");
  await title.fill("必须保留的草稿");
  await page.getByRole("button", { name: /切换目标/ }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await expect(title).toHaveValue("必须保留的草稿");
});

test("memory archive and restore is a complete browser flow", { tag: "@smoke" }, async ({ page }, info) => {
  await page.goto("/memories");
  await page.locator(`[data-memory-id="${memory.id}"]`).click();
  await page.getByRole("button", { name: "归档", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "归档", exact: true }).click();
  await expect(page.locator(`[data-memory-id="${memory.id}"]`)).toHaveCount(0);
  await page.getByRole("button", { name: "已归档", exact: true }).click();
  await expect(page.locator(`[data-memory-id="${memory.id}"]`)).toBeVisible();
  await expect(page.getByRole("textbox", { name: "内容", exact: true })).toHaveAttribute("readonly", "");
  await page.screenshot({ path: info.outputPath("memory-archived.png") });
  await page.getByRole("button", { name: "恢复", exact: true }).click();
  await expect(page.getByRole("button", { name: "当前记忆", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("textbox", { name: "标题", exact: true })).toHaveValue("本地架构");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.screenshot({ path: info.outputPath("memory-restored.png") });
});

test("Agent editor protects drafts and publishes saved configuration immediately", async ({ page }) => {
  test.skip(!agent, "本机尚无 ready Provider。");
  await page.goto("/agents");
  await page.locator(`[data-agent-id="${agent.id}"]`).click();
  const name = page.getByRole("textbox", { name: "名称", exact: true });
  await expect(name).toBeEnabled();
  await name.fill("保留的 Agent 草稿");
  await page.locator("main header").getByRole("button", { name: "添加 Agent", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "取消", exact: true }).click();
  await expect(name).toHaveValue("保留的 Agent 草稿");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
  await expect(page.locator(`[data-agent-id="${agent.id}"]`)).toContainText("保留的 Agent 草稿");
  await page.goto("/conversations");
  await expect(page.locator(`[data-node-id="${node.id}"]`)).toContainText("保留的 Agent 草稿");
});

test("Agent cannot save an incomplete revision while it loads", async ({ page }) => {
  test.skip(!agent, "本机尚无 ready Provider。");
  let release;
  const loading = new Promise((resolve) => { release = resolve; });
  await page.route("**/v1/agent-revisions/*", async (route) => { await loading; await route.continue(); });
  await page.goto(`/agents?agent=${agent.id}`);
  await expect(page.getByRole("textbox", { name: "名称", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
  release();
  await expect(page.getByRole("textbox", { name: "名称", exact: true })).toBeEnabled();
});

test("Computer Use configuration toggles without installing or running it", async ({ page }) => {
  await page.goto("/settings");
  await page.getByRole("button", { name: "启用", exact: true }).click();
  await expect(page.getByRole("button", { name: "禁用", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "禁用", exact: true }).click();
  await expect(page.getByRole("button", { name: "启用", exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("local directory resource commands run through the durable dispatcher", async ({ page }) => {
  const directory = await mkdtemp(join(tmpdir(), "am-browser-review-"));
  try {
    await page.goto("/workbench");
    // page.request shares the browser context's HttpOnly session cookie.
    // Wait for browser bootstrap, then read its token without minting another
    // session while the app is recording the first navigation in parallel.
    await expect(page.getByRole("heading", { name: project.name, exact: true })).toBeVisible();
    const origin = new URL(page.url()).origin;
    const session = await (await page.request.get("/v1/auth/csrf", {
      headers: { Origin: origin }
    })).json();
    let csrf = session.data.csrf_token;
    const call = async (method, path, data) => {
      const send = () => page.request.fetch(`/v1${path}`, {
        method, data,
        headers: { Origin: origin, "X-CSRF-Token": csrf, "Content-Type": "application/json" }
      });
      let response = await send();
      // Match the application's one safe retry: browser bootstrap may rotate
      // the shared cookie after this helper acquired its in-memory CSRF token.
      // csrf_failed is rejected before dispatch; other 403s are never retried.
      if (response.status() === 403 && (await response.json()).error?.code === "csrf_failed") {
        const restored = await page.request.get("/v1/auth/csrf", { headers: { Origin: origin } });
        expect(restored.ok()).toBeTruthy();
        csrf = (await restored.json()).data.csrf_token;
        response = await send();
      }
      expect(response.ok(), `${method} ${path}: HTTP ${response.status()}`).toBeTruthy();
      return response.status() === 204 ? undefined : (await response.json()).data;
    };
    const computer = (await call("GET", "/computers")).items[0];
    const registered = await call("POST", `/computers/${computer.id}/resource-register`, { path: directory });
    let command;
    await expect.poll(async () => {
      command = await call("GET", `/computers/${computer.id}/directory-list/${registered.command_id}`);
      return command.status;
    }, { timeout: 15_000 }).toBe("completed");
    // The local runtime canonicalizes symlinks (macOS /var -> /private/var).
    expect(command.result.path.endsWith(directory.split("/").pop())).toBeTruthy();
    const resource = await call("POST", `/projects/${project.id}/resources`, {
      kind: "code_directory", name: "验收目录", path: command.result.path
    });
    await call("POST", `/projects/${project.id}/resources/${resource.id}/validate`, { expected_version: resource.version });
    await expect.poll(async () => {
      const items = (await call("GET", `/projects/${project.id}/resources`)).items;
      return items.find((item) => item.id === resource.id)?.status;
    }, { timeout: 15_000 }).toBe("ready");
    await page.goto("/resources");
    // Both the mobile card list and desktop table render; assert the visible one.
    await expect(page.getByText("验收目录").locator("visible=true").first()).toBeVisible();
    await expect(page.getByText("可用").locator("visible=true").first()).toBeVisible();
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("graph node goals expose budget, audit and stop controls", { tag: "@goal" }, async ({ page }, info) => {
  await page.route("**/v1/work-records/navigation", route=>route.fulfill({status:204}));
  // Browser UI contract fixture only: no Provider/model calls. The backend
  // dispatcher lifecycle is tested independently with the real SQLite outbox.
  const stamp = "2026-09-14T00:00:00Z";
  const versioned = { version: 1, created_at: stamp, updated_at: stamp };
  const graphFixture = { ...versioned, id: "goal-ui-graph", title: "节点目标验收", project_id: project.id, node_count: 1 };
  const nodeFixture = { ...versioned, id: "goal-ui-node", graph_id: graphFixture.id, agent_id: "goal-ui-agent", agent_revision_id: "revision", title: "目标节点",
    status: "idle", locked: false, width: 380, height: 560, position_x: 0, position_y: 0, message_count: 0 };
  let goal = null;
  const reply = (route, data) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
  await page.route("**/v1/computers/*/agents", (route) => reply(route, { items: [{ ...versioned, id: nodeFixture.agent_id, current_revision_id: "revision", name: "目标助手", status: "active" }] }));
  await page.route("**/v1/projects/*/conversation-graphs", (route) => reply(route, { items: [graphFixture] }));
  await page.route("**/v1/conversation-graphs/goal-ui-graph", (route) => reply(route, {
    graph: graphFixture, nodes: [nodeFixture], messages: [], goals: goal ? [goal] : []
  }));
  await page.route("**/v1/conversation-nodes/goal-ui-node/goals", async (route) => {
    expect(route.request().method()).toBe("POST");
    const input = route.request().postDataJSON();
    expect(input).toEqual({ objective: "核对本节点目标", token_budget: 2000000,
      expected_agent_id: nodeFixture.agent_id, expected_agent_revision_id: nodeFixture.agent_revision_id });
    expect(route.request().headers()["idempotency-key"]).toBeTruthy();
    goal = { ...versioned, id: "goal-ui", node_id: nodeFixture.id, agent_revision_id: "revision", objective: input.objective,
      status: "active", phase: "evaluating", token_budget: input.token_budget, tokens_used: 120, work_runs: 1, run_count: 2,
      max_work_runs: 21, current_run_id: "audit-ui", note: "正在核对结果", status_reason: "" };
    await reply(route, goal);
  });
  await page.route("**/v1/conversation-nodes/goal-ui-node/goals/goal-ui/*", async (route) => {
    expect(route.request().postDataJSON()).toEqual({ expected_version: goal.version });
    const action = route.request().url().split("/").at(-1);
    goal = { ...goal, version: goal.version + 1, status: { pause: "paused", resume: "active", cancel: "cancelled" }[action] };
    await reply(route, goal);
  });
  await page.goto("/conversations");
  const card = page.locator('[data-node-id="goal-ui-node"]');
  await expect(card).toBeVisible();
  await card.getByRole("checkbox", { name: "持续目标", exact: true }).check();
  await card.getByRole("textbox", { name: "消息输入" }).fill("核对本节点目标");
  await card.getByRole("button", { name: "启动目标" }).click();
  await expect(card.getByText("正在独立评估…", { exact: true })).toBeVisible();
  await expect(card.getByRole("textbox")).toHaveCount(0);
  await page.screenshot({ path: info.outputPath("node-goal-auditing.png"), animations: "disabled" });
  await card.getByRole("button", { name: "暂停目标" }).click();
  await card.getByRole("button", { name: "继续目标" }).click();
  await expect(card.getByRole("button", { name: "暂停目标" })).toBeVisible();
  await card.getByRole("button", { name: "取消目标" }).click();
  await expect(card.getByRole("textbox", { name: "消息输入" })).toBeVisible();
});

test("dark theme keeps the brand readable", { tag: "@smoke" }, async ({ page, request }, info) => {
  await page.goto("/settings");
  await page.getByRole("combobox", { name: "主题", exact: true }).selectOption("dark");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.theme).toBe("dark");
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: info.outputPath("dark-settings.png"), animations: "disabled" });
  const contrast = await page.locator("header a[href='/workbench'] > span").first().evaluate((element) => {
    const style = getComputedStyle(element);
    const luminance = (color) => {
      const rgb = color.match(/[\d.]+/g).slice(0, 3).map((part) => Number(part) / 255)
        .map((part) => part <= 0.04045 ? part / 12.92 : ((part + 0.055) / 1.055) ** 2.4);
      return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
    };
    const text = luminance(style.color), background = luminance(style.backgroundColor);
    return (Math.max(text, background) + 0.05) / (Math.min(text, background) + 0.05);
  });
  try { expect(contrast).toBeGreaterThanOrEqual(4.5); }
  finally {
    await page.getByRole("combobox", { name: "主题", exact: true }).selectOption("light");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.theme).toBe("light");
  }
});

test("first-run guide detects CLIs and configures agents", { tag: ["@onboarding", "@smoke"] }, async ({ page, request, baseURL }, info) => {
  const session = await api(request, "/auth/local-session", {});
  headers["X-CSRF-Token"] = session.csrf_token;
  const computers = await api(request, "/computers");
  const computer = computers.items[0];
  const available = (await api(request, `/computers/${computer.id}/agent-services`)).items;
  test.skip(!available.some((service) => service.status === "ready"), "本机没有已就绪的编程 CLI；不伪造引导完成证据。");

  // Settings embeds the same detection panel and reopens the guide as an
  // overlay over the current page without changing the URL.
  await page.goto("/settings?tab=cli");
  await expect(page.getByRole("tab", { name: "编程 CLI", exact: true })).toHaveAttribute("aria-selected", "true");
  expect(await page.locator("[data-programming-tool]").count()).toBeGreaterThanOrEqual(1);
  await page.getByRole("button", { name: "重新运行引导", exact: true }).click();
  const dialog = page.locator("[data-onboarding-dialog]");
  await expect(dialog).toBeVisible();
  await expect(page).toHaveURL(`${baseURL}/settings?tab=cli`);
  // The mask covers the full window; the dialog stays centered inside it.
  const mask = await dialog.evaluate((element) => {
    const overlay = element.parentElement?.getBoundingClientRect();
    const panel = element.getBoundingClientRect();
    return {
      overlay: overlay && { left: overlay.left, top: overlay.top, width: overlay.width, height: overlay.height },
      panel: { left: panel.left, top: panel.top, width: panel.width, height: panel.height }
    };
  });
  expect(mask.overlay.left).toBeLessThanOrEqual(1);
  expect(Math.abs(mask.overlay.width - (await page.evaluate(() => window.innerWidth))) ).toBeLessThanOrEqual(2);
  expect(Math.abs((mask.panel.left + mask.panel.width / 2) - mask.overlay.width / 2)).toBeLessThanOrEqual(2);
  await page.screenshot({ path: info.outputPath("onboarding-overlay.png") });

  // Reset idle fixture Agents when possible. A repeat run can have enabled
  // scheduled tasks bound to an Agent: that deletion MUST remain protected,
  // so retain it and exercise the existing manual-guide fallback below.
  const agents = await api(request, `/computers/${computer.id}/agents`);
  for (const item of agents.items.filter((agent) => agent.system_role !== "general")) {
    const deletion = await request.delete(`/v1/agents/${item.id}?expected_version=${item.version}`, { headers });
    if (deletion.status() === 409) {
      expect((await deletion.json()).error?.code).toBe("agent_in_use");
      info.annotations.push({ type: "note", description: "保留启用计划引用的 Agent，验证手动引导而不破坏已有计划。" });
    } else {
      expect(deletion.status()).toBe(204);
    }
  }
  await api(request, "/system/onboarding", { dismissed: false }, "PUT");
  const state = await api(request, "/system/onboarding");
  if (state.needed) {
    // Direct links show the setup banner; the banner opens the same overlay
    // over the current page.
    await page.goto("/settings");
    await expect(page.getByText("初始配置尚未完成：还没有可用的 Agent。", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "开始配置", exact: true }).click();
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(`${baseURL}/settings`);
    // The default entry point opens the overlay over the workbench.
    await page.goto("/workbench");
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(`${baseURL}/workbench`);
    // /onboarding is an alias for the same overlay over the workbench.
    await page.goto("/onboarding");
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(`${baseURL}/workbench`);
  } else {
    // Lumi stays configured across reruns, so the derived empty state cannot
    // be reproduced; the manual flow below still runs fully.
    info.annotations.push({ type: "note", description: "Lumi 已配置（重复运行）：本次只验证手动引导流程。" });
  }

  // Step 1: CLIs are detected automatically. All step locators stay scoped to
  // the dialog: the settings panel behind it renders the same tool rows.
  await expect(dialog.locator('[data-programming-tool="codex"]')).toBeVisible();
  await dialog.getByRole("button", { name: "下一步", exact: true }).click();
  // The guide introduces Lumi but never hosts a second configuration editor.
  await expect(dialog.getByRole("heading", { name: "认识 Lumi", exact: true })).toBeVisible();
  await expect(dialog.locator("ol > li")).toHaveCount(2);
  await expect(dialog.getByLabel("选择 Provider", { exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "添加大模型供应商", exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "创建 Agent", exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: "进入任务", exact: true }).click();
  const lumi=await api(request,"/work-assistant");
  await expect(page).toHaveURL(`${baseURL}/issues`);
  await expect(dialog).toBeHidden();
  // Configuration is an explicit later action, never the guide's destination.
  await page.goto(`/agents?agent=${lumi.id}`);
  const agentForm=page.locator("form").filter({has:page.getByLabel("名称",{exact:true})});
  await expect(agentForm.getByLabel("名称",{exact:true})).toHaveValue(lumi.name);
  // A deliberately non-callable model: later auto-title creation must never
  // turn this UI configuration fixture into a real background model call.
  const setupModel=`fixture/disabled-setup-${Date.now()}`;
  await agentForm.getByLabel("模型",{exact:true}).fill(setupModel);
  await agentForm.getByRole("button",{name:"保存",exact:true}).click();
  await expect(agentForm.getByRole("button",{name:"保存",exact:true})).toBeDisabled();
  await expect.poll(async ()=>(await api(request,"/work-assistant")).model).toBe(setupModel);
  await expect(agentForm.getByRole("button",{name:"删除",exact:true})).toHaveCount(0);
  const finished = await api(request, "/system/onboarding");
  expect(finished.dismissed).toBe(true);
  expect(finished.needed).toBe(false);
  // A configured instance can still reopen the guide; it stays open until the
  // user skips or finishes (regression: it used to close immediately).
  await page.goto("/settings?tab=cli");
  await page.getByRole("button", { name: "重新运行引导", exact: true }).click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "下一步", exact: true }).click();
  await expect(dialog.locator("ol > li")).toHaveCount(2);
  await dialog.getByRole("button", { name: "进入任务", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/issues`);
  await page.goto(`/agents?agent=${lumi.id}`);
  await agentForm.getByRole("button", { name: "添加大模型供应商", exact: true }).click();
  const apiDialog=page.getByRole("dialog",{name:"配置大模型 API",exact:true});
  await apiDialog.getByLabel("模型供应商", { exact: true }).selectOption("deepseek");
  await expect(apiDialog.getByLabel("API 地址（Base URL）", { exact: true })).toHaveValue("https://api.deepseek.com/v1");
  await expect(apiDialog.getByLabel("API Key", { exact: true })).toHaveAttribute("type", "password");
  await expect(apiDialog.getByRole("button", { name: "测试连接（发送 Hello）", exact: true })).toBeVisible();
  // Do not test a real vendor or save real credentials in the browser suite.
  await apiDialog.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(apiDialog).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) {
  test.describe(`shared geometry ${viewport.width}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });
  test(`shared controls and central Lumi editor ${viewport.width}`, { tag: "@ui" }, async ({ page, request }, info) => {
    await page.setViewportSize(viewport);
    const session = await api(request, "/auth/local-session", {});
    headers["X-CSRF-Token"] = session.csrf_token;
    const lumi = await api(request, "/work-assistant");
    await page.goto(`/agents?agent=${lumi.id}`);
    const form = page.locator("form").filter({ has: page.getByLabel("名称", { exact: true }) });
    await expect(form.getByLabel("名称", { exact: true })).toHaveValue(lumi.name);
    await expect(form.getByRole("button", { name: "删除", exact: true })).toHaveCount(0);
    const geometry = async () => page.evaluate(() => {
      const visible = (e) => e.getBoundingClientRect().width > 0;
      const rows = [...document.querySelectorAll(".am-select")].filter(visible).map((e) => {
        const control = e.querySelector("select"), icon = e.querySelector(".am-select__icon");
        const box = e.getBoundingClientRect(), arrow = icon.getBoundingClientRect();
        return { height: box.height, size: e.dataset.size, gap: box.right - arrow.right, padding: parseFloat(getComputedStyle(control).paddingRight) };
      });
      return { rows, coarse: matchMedia("(pointer:coarse)").matches, overflow: document.documentElement.scrollWidth > innerWidth,
        inputs: [...document.querySelectorAll(".am-input")].filter(visible).map((e) => ({ height: e.getBoundingClientRect().height, size: e.dataset.size })) };
    });
    const assertGeometry = async () => {
      const result = await geometry();
      expect(result.overflow).toBe(false);
      expect(result.coarse).toBe(viewport.width === 390);
      const expected = (size) => result.coarse ? 44 : ({ sm: 32, md: 36, lg: 40 })[size];
      for (const row of result.rows) { expect(row.height).toBe(expected(row.size)); expect(row.gap).toBeGreaterThanOrEqual(12); expect(row.gap).toBeLessThanOrEqual(14); expect(row.padding).toBe(38); }
      for (const input of result.inputs) expect(input.height).toBe(expected(input.size));
    };
    await assertGeometry();
    await page.screenshot({ path: info.outputPath(`lumi-editor-${viewport.width}.png`) });
    await form.getByRole("button", { name: "添加大模型供应商", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "配置大模型 API", exact: true });
    await dialog.getByLabel("模型供应商", { exact: true }).selectOption("deepseek");
    await expect(dialog.getByLabel("模型 ID", { exact: true })).toHaveValue("deepseek-chat");
    await assertGeometry();
    const modalViewports = viewport.width === 390
      ? [viewport, { width: 390, height: 420 }, { width: 320, height: 420 }]
      : [viewport, { width: 800, height: 600 }, { width: 1280, height: 420 }];
    for (const size of modalViewports) {
      await page.setViewportSize(size);
      await assertModelAPISetupGeometry(dialog, { short: size.height === 420, wrapped: size.width <= 390 });
      await page.screenshot({ path: info.outputPath(`model-api-modal-${size.width}x${size.height}.png`) });
    }
    await page.setViewportSize(viewport);
    const choose = dialog.getByRole("button", { name: "选择模型", exact: true });
    await expect(choose).toHaveAttribute("type", "button");
    await choose.click();
    await expect(dialog.getByLabel("模型 ID", { exact: true })).toBeFocused();
    await page.keyboard.press("Escape");
    // Escape first dismisses the browser-owned suggestions; close the app
    // dialog explicitly if that native popup consumed the key.
    if (await dialog.isVisible()) await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    await expect(dialog).toBeHidden();
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "设置", exact: true })).toBeVisible();
    await assertGeometry();
  });
  });
}
