import { test as base, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// 独立 fixture：真实隔离 loopback HTTP/Chrome，不创建 Agent、Run 或 mention。
// Cookie/CSRF 不输出，不录制 trace/HAR；不复用其他 spec 的任务或项目。
const suffix = randomUUID().slice(0, 8);
let project, otherProject, linked, second, foreign;

async function authenticatedAPI(request, baseURL) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const bootstrap = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(bootstrap.status()).toBe(200);
  headers["X-CSRF-Token"] = (await bootstrap.json()).data.csrf_token;
  return async (path, data, method = data === undefined ? "GET" : "POST") => {
    const response = await request.fetch(`/v1${path}`, { method, headers, data });
    expect(response.ok(), `${method} ${path}: HTTP ${response.status()}`).toBe(true);
    return response.status() === 204 ? undefined : (await response.json()).data;
  };
}

const test = base.extend({
  localAPI: async ({ request, baseURL }, use) => use(await authenticatedAPI(request, baseURL)),
  noExecution: [async ({ page }, use) => {
    const denied = [];
    await page.route("**/v1/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (request.method() !== "GET" && ![
        "/v1/auth/local-session", "/v1/work-records/navigation"
      ].includes(path)) {
        denied.push(`${request.method()} ${path}`);
        await route.abort("blockedbyclient");
        return;
      }
      await route.fallback();
    });
    await use();
    expect(denied, "深链回归不得触发模型、Run 或 mention").toEqual([]);
  }, { auto: true }]
});
test.use({ trace: "off", video: "off" });

test.beforeAll(async ({ request, baseURL }) => {
  const api = await authenticatedAPI(request, baseURL);
  const computer = (await api("/computers")).items[0];
  expect(computer).toBeTruthy();
  project = await api("/projects", { computer_id: computer.id, name: `任务深链-${suffix}` });
  otherProject = await api("/projects", { computer_id: computer.id, name: `任务深链另一项目-${suffix}` });
  linked = await api(`/projects/${project.id}/issues`, { title: `深链任务-${suffix}`, description: "无模型的真实刷新回归。" });
  second = await api(`/projects/${project.id}/issues`, { title: `手选任务-${suffix}` });
  foreign = await api(`/projects/${otherProject.id}/issues`, { title: `另一项目任务-${suffix}` });
});

test.beforeEach(async ({ page, localAPI }) => {
  await page.addInitScript((selection) => {
    sessionStorage.setItem("teamva-selection", JSON.stringify(selection));
  }, { computerId: project.computer_id, projectId: project.id });
  const preferences = await localAPI("/me/preferences");
  await localAPI("/me/preferences", { expected_version: preferences.version, locale: "zh-CN" }, "PATCH");
});

const detail = (page) => page.getByTestId("issue-detail-pane");

async function expectCurrentProject(page, expected) {
  // 任务页是无项目标题的主从布局；当前项目由真实侧栏选择器展示。
  // 同时核对持久化选择，不能用一个并不存在的项目 h1 判断是否切换。
  await expect(page.getByRole("button", { name: "选择项目", exact: true })).toHaveText(expected.name);
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem("teamva-selection")).projectId)).toBe(expected.id);
}

test("同项目 issue 深链刷新保留 URL、列表选中和真实详情", { tag: ["@issue-deeplink", "@smoke"] }, async ({ page }, info) => {
  await page.goto(`/issues?issue=${encodeURIComponent(linked.id)}`);
  await expect(detail(page).getByRole("heading", { level: 1, name: linked.title, exact: true })).toBeVisible();
  await page.reload();
  await expect(detail(page).getByRole("heading", { level: 1, name: linked.title, exact: true })).toBeVisible();
  await expect(page.getByRole("complementary", { name: "任务列表", exact: true })
    .getByRole("button", { name: new RegExp(linked.title) })).toHaveAttribute("aria-current", "page");
  expect(new URL(page.url()).searchParams.get("issue")).toBe(linked.id);
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem("teamva-selection")).projectId)).toBe(project.id);
  await page.screenshot({ path: info.outputPath("issue-deeplink-reloaded.png"), animations: "disabled" });
});

test("显式 query 变化和手选任务不重挂载也能切换", { tag: "@issue-deeplink" }, async ({ page }) => {
  await page.goto(`/issues?issue=${encodeURIComponent(linked.id)}`);
  await expect(detail(page).getByRole("heading", { level: 1, name: linked.title, exact: true })).toBeVisible();
  await page.evaluate((id) => {
    history.pushState({}, "", `/issues?issue=${encodeURIComponent(id)}`);
    dispatchEvent(new PopStateEvent("popstate"));
  }, second.id);
  await expect(detail(page).getByRole("heading", { level: 1, name: second.title, exact: true })).toBeVisible();
  await page.getByRole("complementary", { name: "任务列表", exact: true })
    .getByRole("button", { name: new RegExp(linked.title) }).click();
  await expect(detail(page).getByRole("heading", { level: 1, name: linked.title, exact: true })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("issue")).toBe(linked.id);
});

test("真实 SQL 分页、全项目过滤与百条外任务深链", { tag: "@issue-deeplink" }, async ({ page, localAPI }) => {
  const boundedProject = await localAPI("/projects", { computer_id: project.computer_id, name: `百条任务深链-${suffix}` });
  const target = await localAPI(`/projects/${boundedProject.id}/issues`, { title: `百条以外任务-${suffix}` });
  for (let index = 0; index < 100; index++) {
    await localAPI(`/projects/${boundedProject.id}/issues`, { title: `较新任务-${suffix}-${index}` });
  }
  const listPath = `/v1/projects/${boundedProject.id}/issues`;
  const listRequests = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === listPath && request.method() === "GET") listRequests.push(url);
  });
  const pageResponse = (offset, search = "", status = "") => page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === listPath && url.searchParams.get("limit") === "100" &&
      url.searchParams.get("offset") === String(offset) && url.searchParams.get("search") === search &&
      url.searchParams.get("status") === status;
  });
  // 不截断、不模拟列表；直接验证真实 endpoint 的 SQL 分页结果及总数。
  const firstPage = pageResponse(0);
  const fetched = page.waitForResponse((response) => new URL(response.url()).pathname === `/v1/issues/${target.id}` && response.request().method() === "GET");
  await page.goto(`/issues?project=${encodeURIComponent(boundedProject.id)}&issue=${encodeURIComponent(target.id)}`);
  const first = (await (await firstPage).json()).data;
  expect(first.items).toHaveLength(100);
  expect(first.total).toBe(101);
  expect(first.items.some((item) => item.id === target.id)).toBe(false);
  expect((await fetched).status()).toBe(200);
  await expect(detail(page).getByRole("heading", { level: 1, name: target.title, exact: true })).toBeVisible();
  const list = page.getByRole("complementary", { name: "任务列表", exact: true });
  await expect(list.getByRole("button", { name: new RegExp(target.title) })).toHaveCount(0);
  await expect(list.getByText("1–100 / 101", { exact: true })).toBeVisible();
  const nextPage = pageResponse(100);
  await list.getByRole("button", { name: "任务列表 +", exact: true }).click();
  const next = (await (await nextPage).json()).data;
  expect(next.items.map((item) => item.id)).toEqual([target.id]);
  expect(next.total).toBe(101);
  await expect(list.getByText("101–101 / 101", { exact: true })).toBeVisible();
  await expect(list.getByRole("button", { name: new RegExp(target.title) })).toHaveAttribute("aria-current", "page");

  const searchResponse = pageResponse(0, target.title);
  await list.getByRole("searchbox").fill(target.title);
  const searched = (await (await searchResponse).json()).data;
  expect(searched.items.map((item) => item.id)).toEqual([target.id]);
  expect(searched.total).toBe(1);
  const statusResponse = pageResponse(0, target.title, "resolved");
  await list.getByRole("combobox", { name: "状态", exact: true }).selectOption("resolved");
  const filtered = (await (await statusResponse).json()).data;
  expect(filtered).toMatchObject({ items: [], total: 0 });
  await expect(list.getByRole("button", { name: new RegExp(target.title) })).toHaveCount(0);
  await expect(detail(page).getByRole("heading", { level: 1, name: target.title, exact: true })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("issue")).toBe(target.id);
  expect(listRequests.length).toBeGreaterThanOrEqual(4);
  expect(listRequests.every((url) => url.searchParams.get("limit") === "100")).toBe(true);

  const overviewPage = (status) => page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === listPath && url.searchParams.get("limit") === "8" &&
      (url.searchParams.get("status") ?? "") === status;
  });
  const recentOverview = overviewPage("");
  const openOverview = overviewPage("open");
  await localAPI("/system/onboarding", { dismissed: true }, "PUT");
  await page.goto(`/workbench?project=${encodeURIComponent(boundedProject.id)}`);
  for (const response of [await recentOverview, await openOverview]) {
    const overview = (await response.json()).data;
    expect(overview.items).toHaveLength(8);
    expect(overview.total).toBe(101);
  }
  await expect(page.locator('nav[aria-label] a[href="/issues"] strong')).toHaveText("101");
  await expect(page.locator("section").filter({ has: page.getByRole("heading", { name: "任务", level: 2, exact: true }) })
    .locator('a[href^="/issues?issue="]')).toHaveCount(8);
});

test("切换项目后不展示旧 issue，不自动跨项目选择", { tag: "@issue-deeplink" }, async ({ page }) => {
  await page.goto(`/issues?issue=${encodeURIComponent(linked.id)}`);
  await expect(detail(page).getByRole("heading", { level: 1, name: linked.title, exact: true })).toBeVisible();
  await page.getByRole("button", { name: "选择项目", exact: true }).click();
  await page.getByRole("dialog", { name: "选择项目", exact: true })
    .locator(`[data-project-id="${otherProject.id}"]`).getByRole("button", { name: otherProject.name, exact: true }).click();
  await expectCurrentProject(page, otherProject);
  await expect(page.getByRole("complementary", { name: "任务列表", exact: true }).getByText(foreign.title, { exact: true })).toBeVisible();
  await expect(detail(page).getByRole("heading", { level: 1 })).toHaveCount(0);
  await expect(page.getByText(linked.title, { exact: true })).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get("issue")).toBe(linked.id);
});

test("404 和非当前项目深链不显示其他任务或泄漏详情", { tag: "@issue-deeplink" }, async ({ page }) => {
  const unavailable = `iss_${randomUUID()}`;
  const failed = page.waitForResponse((response) => new URL(response.url()).pathname === `/v1/issues/${unavailable}`);
  await page.goto(`/issues?issue=${unavailable}`);
  expect((await failed).status()).toBe(404);
  await expect(detail(page).getByRole("heading", { level: 1 })).toHaveCount(0);
  await expect(detail(page).getByText("选择或创建一个任务", { exact: true })).toBeVisible();
  const fetched = page.waitForResponse((response) => new URL(response.url()).pathname === `/v1/issues/${foreign.id}`);
  await page.goto(`/issues?issue=${encodeURIComponent(foreign.id)}`);
  expect((await fetched).status()).toBe(200);
  await expectCurrentProject(page, project);
  await expect(detail(page).getByRole("heading", { level: 1 })).toHaveCount(0);
  await expect(page.getByText(foreign.title, { exact: true })).toHaveCount(0);
});

test("记忆无效与跨项目深链不打开首条，手选和归档视图准确定位", { tag: "@object-deeplink" }, async ({ page, localAPI }) => {
  const entry = await localAPI(`/projects/${project.id}/memories`, {
    key: `deep-link-${suffix}`, kind: "fact", title: `深链记忆-${suffix}`, content: "只能精确定位才能编辑的正文", source_type: "user"
  });
  const other = await localAPI(`/projects/${otherProject.id}/memories`, {
    key: `foreign-deep-link-${suffix}`, kind: "fact", title: `外项目记忆-${suffix}`, content: "不能在错误项目显示的正文", source_type: "user"
  });
  for (const id of [`missing-${suffix}`, other.id]) {
    await page.goto(`/memories?memory=${encodeURIComponent(id)}`);
    await expect(page.getByRole("alert")).toContainText("无法打开指定的记忆");
    await expect(page.getByLabel("内容", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "保存", exact: true })).toHaveCount(0);
  }
  await page.locator(`[data-memory-id="${entry.id}"]`).click();
  await expect(page.getByLabel("内容", { exact: true })).toHaveValue(entry.content);
  expect(new URL(page.url()).searchParams.get("memory")).toBe(entry.id);
  await page.reload();
  await expect(page.getByLabel("内容", { exact: true })).toHaveValue(entry.content);
  await localAPI(`/projects/${project.id}/memories/${entry.id}/archive`, { expected_version: entry.version });
  await page.goto(`/memories?memory=${encodeURIComponent(entry.id)}`);
  await expect(page.getByRole("alert")).toContainText("无法打开指定的记忆");
  await expect(page.getByLabel("内容", { exact: true })).toHaveCount(0);
  await page.goto(`/memories?archived=true&memory=${encodeURIComponent(entry.id)}`);
  await expect(page.getByLabel("内容", { exact: true })).toHaveValue(entry.content);
  await expect(page.getByLabel("内容", { exact: true })).toHaveAttribute("readonly", "");
  await expect(page.getByRole("button", { name: "保存", exact: true })).toHaveCount(0);
});

test("分支对话无效、已删除与跨项目深链不打开首图，正常手选保留 URL", { tag: "@object-deeplink" }, async ({ page, localAPI }) => {
  const agent = (await localAPI(`/computers/${project.computer_id}/agents`)).items
    .find((item) => item.status === "active" && item.current_revision_id);
  test.skip(!agent, "此真实 Graph 用例需要一个已有配置 Agent；不会创建 Run 或调用模型。组件 fixture 已覆盖无 Agent 环境。");
  const graph = await localAPI(`/projects/${project.id}/conversation-graphs`, { title: `深链分支对话-${suffix}`, agent_id: agent.id });
  const other = await localAPI(`/projects/${otherProject.id}/conversation-graphs`, { title: `外项目分支对话-${suffix}`, agent_id: agent.id });
  const removed = await localAPI(`/projects/${project.id}/conversation-graphs`, { title: `已删除分支对话-${suffix}`, agent_id: agent.id });
  await localAPI(`/conversation-nodes/${removed.nodes[0].id}?expected_version=${removed.nodes[0].version}`, undefined, "DELETE");
  for (const id of [`missing-${suffix}`, other.graph.id, removed.graph.id]) {
    await page.goto(`/conversations?graph=${encodeURIComponent(id)}`);
    await expect(page.getByRole("alert")).toContainText("无法打开指定的分支对话");
    await expect(page.getByRole("application")).toHaveCount(0);
    await expect(page.locator(".conversation-node")).toHaveCount(0);
  }
  await page.getByRole("button", { name: new RegExp(graph.graph.title) }).click();
  await expect(page.locator(`[data-node-id="${graph.nodes[0].id}"]`)).toBeVisible();
  expect(new URL(page.url()).searchParams.get("graph")).toBe(graph.graph.id);
  await page.reload();
  await expect(page.locator(`[data-node-id="${graph.nodes[0].id}"]`)).toBeVisible();
});
