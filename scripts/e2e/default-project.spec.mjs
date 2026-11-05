import { test as base, expect } from "@playwright/test";

// 仅在隔离服务中操作真实公共 HTTP API；不创建 Run、不调用模型，
// 不录制包含 Cookie/CSRF 的 trace 或 HAR。每个 request context 单独引导认证。
const selectionKey = "teamva-selection";
const defaultName = "默认项目";
let ordinaryProject;
let defaultProject;

async function bootstrapAPI(request, baseURL) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const bootstrap = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(bootstrap.status()).toBe(200);
  headers["X-CSRF-Token"] = (await bootstrap.json()).data.csrf_token;
  const raw = (path, data, method = data === undefined ? "GET" : "POST") =>
    request.fetch(`/v1${path}`, { method, headers, data });
  const api = async (path, data, method) => {
    const response = await raw(path, data, method);
    expect(response.ok(), `${method ?? (data === undefined ? "GET" : "POST")} ${path}: HTTP ${response.status()}`).toBeTruthy();
    return response.status() === 204 ? undefined : (await response.json()).data;
  };
  return { api, raw };
}

const test = base.extend({
  localAPI: async ({ request, baseURL }, use) => {
    await use(await bootstrapAPI(request, baseURL));
  }
});

test.beforeAll(async ({ request, baseURL }) => {
  const { api } = await bootstrapAPI(request, baseURL);
  const computer = (await api("/computers")).items[0];
  const projects = (await api(`/projects?computer_id=${computer.id}`)).items;
  expect(projects.filter((item) => item.is_default)).toHaveLength(1);
  defaultProject = projects.find((item) => item.is_default);
  expect(defaultProject).toMatchObject({ name: defaultName, computer_id: computer.id });
  ordinaryProject = await api("/projects", {
    computer_id: computer.id, name: `默认项目窗口验收-${Date.now()}`,
    description: "仅验收默认选择与项目保护，不执行 Agent。"
  });
  const services = (await api(`/computers/${computer.id}/agent-services`)).items;
  const service = services.find((item) => item.status === "ready");
  expect(service, "隔离浏览器验收需要一个 ready 编程 CLI，仅用于创建 Agent 绕过空状态引导").toBeTruthy();
  await api(`/computers/${computer.id}/agents`, {
    name: "默认项目验收 Agent", agent_service_id: service.id,
    system_prompt: "仅用于界面验收，不创建 Run 或发送模型请求。"
  });
});

async function defaultHeading(page) {
  const heading = page.getByRole("heading", { name: defaultName, level: 1, exact: true });
  await expect(heading).toBeVisible();
  expect(await heading.evaluate((element) => element.closest("button") === null)).toBe(true);
  await expect(heading.locator("..").getByText("默认", { exact: true })).toBeVisible();
}

async function chooseProject(page, project) {
  await page.getByRole("button", { name: "选择项目", exact: true }).click();
  const picker = page.getByRole("dialog", { name: "选择项目", exact: true });
  await picker.locator(`[data-project-id="${project.id}"]`).getByRole("button", { name: project.name, exact: true }).click();
  await expect(picker).toBeHidden();
  await expect(page.getByRole("heading", { name: project.name, level: 1, exact: true })).toBeVisible();
}

async function manageProject(page, project, mobile = false) {
  if (mobile) {
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("button", { name: "管理项目", exact: true }).click();
  } else {
    await page.getByRole("button", { name: "选择项目", exact: true }).click();
    await page.getByRole("dialog", { name: "选择项目", exact: true })
      .locator(`[data-project-id="${project.id}"]`).getByRole("button", { name: "管理项目", exact: true }).click();
  }
  const dialog = page.getByRole("dialog", { name: "管理项目", exact: true });
  await expect(dialog).toBeVisible();
  return dialog;
}

test("新窗口忽略旧 localStorage 项目并默认进入唯一置顶的真实默认项目", { tag: ["@default-project", "@smoke"] }, async ({ page, localAPI }, info) => {
  await page.addInitScript(({ key, project }) => {
    sessionStorage.removeItem(key);
    localStorage.setItem(key, JSON.stringify({ computerId: project.computer_id, projectId: project.id }));
  }, { key: selectionKey, project: ordinaryProject });
  await page.goto("/workbench");
  await defaultHeading(page);
  const projects = (await localAPI.api(`/projects?computer_id=${ordinaryProject.computer_id}`)).items;
  expect(projects.filter((item) => item.is_default)).toHaveLength(1);
  expect(projects[0]).toMatchObject({ id: defaultProject.id, name: defaultName, is_default: true });
  await page.getByRole("button", { name: "选择项目", exact: true }).click();
  const rows = page.getByRole("dialog", { name: "选择项目", exact: true }).locator("[data-project-id]");
  await expect(rows.first()).toHaveAttribute("data-project-id", defaultProject.id);
  await expect(rows.first()).toHaveAttribute("data-default-project", "true");
  await expect(rows.first().getByText("默认", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.screenshot({ path: info.outputPath("default-overview-desktop.png"), animations: "disabled" });
});

test("默认项目受 API 保护但说明可改，项目不再提供配色，普通同名项目不受保护", { tag: "@default-project" }, async ({ page, localAPI }) => {
  const { api, raw } = localAPI;
  await page.goto("/workbench");
  await defaultHeading(page);
  const dialog = await manageProject(page, defaultProject);
  const name = dialog.getByLabel("项目名称", { exact: true });
  await expect(name).toHaveValue(defaultName);
  await expect(name).toHaveAttribute("readonly", "");
  await expect(name).not.toBeEditable();
  const hintId = await name.getAttribute("aria-describedby");
  expect(hintId).toBeTruthy();
  await expect(dialog.locator(`[id="${hintId}"]`)).not.toBeEmpty();
  await expect(dialog.getByRole("button", { name: "删除项目", exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("radiogroup")).toHaveCount(0);
  const description = `默认项目仍可保存说明-${Date.now()}`;
  await dialog.getByLabel("备注", { exact: true }).fill(description);
  const saved = page.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === `/v1/projects/${defaultProject.id}`);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  const response = await saved;
  expect(response.ok()).toBeTruthy();
  expect(response.request().postDataJSON()).not.toHaveProperty("theme");
  await expect(dialog).toBeHidden();
  let current = await api(`/projects/${defaultProject.id}`);
  expect(current).toMatchObject({ name: defaultName, is_default: true, description });
  expect(current).not.toHaveProperty("theme");
  const deletion = await raw(`/projects/${current.id}?expected_version=${current.version}`, undefined, "DELETE");
  expect(deletion.status()).toBe(403);
  expect((await deletion.json()).error.code).toBe("protected_project");
  const rename = await raw(`/projects/${current.id}`, { expected_version: current.version, name: "试图改名" }, "PATCH");
  expect(rename.status()).toBe(403);
  expect((await rename.json()).error.code).toBe("protected_project");
  current = await api(`/projects/${defaultProject.id}`);
  expect(current).toMatchObject({ name: defaultName, is_default: true, description });
  expect(current).not.toHaveProperty("theme");

  const sameName = await api("/projects", { computer_id: ordinaryProject.computer_id, name: defaultName });
  expect(sameName.is_default ?? false).toBe(false);
  const renamed = await api(`/projects/${sameName.id}`, { expected_version: sameName.version, name: "普通同名项目可改名" }, "PATCH");
  expect(renamed.name).toBe("普通同名项目可改名");
  // 删除时仍恢复为相同名称，证明保护依据 ID/标记，而非名称。
  const restored = await api(`/projects/${sameName.id}`, { expected_version: renamed.version, name: defaultName }, "PATCH");
  const removed = await raw(`/projects/${sameName.id}?expected_version=${restored.version}`, undefined, "DELETE");
  expect(removed.status()).toBe(204);
  expect((await raw(`/projects/${sameName.id}`)).status()).toBe(404);
});

test("手动选择仅保留当前窗口，刷新保留而新窗口默认，显式 URL 深链可覆盖", { tag: ["@default-project", "@smoke"] }, async ({ page, context }) => {
  await page.goto("/workbench");
  await defaultHeading(page);
  const appearance = () => page.evaluate(() => ({
    theme: document.documentElement.dataset.theme,
    palette: document.documentElement.dataset.palette,
    tokens: document.documentElement.style.cssText
  }));
  const originalAppearance = await appearance();
  await chooseProject(page, ordinaryProject);
  expect(await appearance()).toEqual(originalAppearance);
  await expect.poll(() => page.evaluate((key) => JSON.parse(sessionStorage.getItem(key)).projectId, selectionKey)).toBe(ordinaryProject.id);
  expect(await page.evaluate((key) => JSON.parse(localStorage.getItem(key)).projectId, selectionKey)).toBeUndefined();
  await page.reload();
  await expect(page.getByRole("heading", { name: ordinaryProject.name, level: 1, exact: true })).toBeVisible();

  // 同一 browser context 的独立新页共享 Cookie/localStorage，但没有 opener，
  // 因而不复制旧窗口 sessionStorage，比全新 context 更直接覆盖跨窗口隔离。
  const newWindow = await context.newPage();
  try {
    await newWindow.goto("/workbench");
    await defaultHeading(newWindow);
    await expect(page.getByRole("heading", { name: ordinaryProject.name, level: 1, exact: true })).toBeVisible();
    await newWindow.goto(`/workbench?project=${encodeURIComponent(ordinaryProject.id)}`);
    await expect(newWindow.getByRole("heading", { name: ordinaryProject.name, level: 1, exact: true })).toBeVisible();
    await newWindow.goto("/workbench");
    await expect(newWindow.getByRole("heading", { name: ordinaryProject.name, level: 1, exact: true })).toBeVisible();
    await newWindow.evaluate((key) => sessionStorage.removeItem(key), selectionKey);
    await newWindow.reload();
    await defaultHeading(newWindow);
  } finally {
    await newWindow.close();
  }
});

test("当前普通项目仍可编辑删除，删除后返回默认项目", { tag: "@default-project" }, async ({ page, localAPI }) => {
  const temporary = await localAPI.api("/projects", { computer_id: ordinaryProject.computer_id, name: `待删除普通项目-${Date.now()}` });
  await page.goto(`/workbench?project=${encodeURIComponent(temporary.id)}`);
  await expect(page.getByRole("heading", { name: temporary.name, level: 1, exact: true })).toBeVisible();
  let dialog = await manageProject(page, temporary);
  await expect(dialog.getByLabel("项目名称", { exact: true })).toBeEditable();
  const renamed = `${temporary.name}-已编辑`;
  await dialog.getByLabel("项目名称", { exact: true }).fill(renamed);
  const saved = page.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === `/v1/projects/${temporary.id}`);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  expect((await saved).ok()).toBeTruthy();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading", { name: renamed, level: 1, exact: true })).toBeVisible();
  dialog = await manageProject(page, temporary);
  await dialog.getByRole("button", { name: "删除项目", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "删除项目", exact: true });
  const removed = page.waitForResponse((response) => response.request().method() === "DELETE" && new URL(response.url()).pathname === `/v1/projects/${temporary.id}`);
  await confirmation.getByRole("button", { name: "删除项目", exact: true }).click();
  expect((await removed).status()).toBe(204);
  await expect(confirmation).toBeHidden();
  await defaultHeading(page);
  await page.goto("/workbench");
  await defaultHeading(page);
  expect((await localAPI.raw(`/projects/${temporary.id}`)).status()).toBe(404);
});

test("默认项目支持真实 Issue CRUD 和概览读取，不运行 Agent", { tag: "@default-project" }, async ({ page, localAPI }) => {
  const { api, raw } = localAPI;
  const title = `默认项目真实任务-${Date.now()}`;
  const issue = await api(`/projects/${defaultProject.id}/issues`, { title, description: "真实项目中的无 Agent 任务。", status: "open" });
  expect(issue.project_id).toBe(defaultProject.id);
  expect((await api(`/issues/${issue.id}`)).title).toBe(title);
  await page.goto("/workbench");
  await defaultHeading(page);
  await expect(page.getByRole("link", { name: new RegExp(title) }).first()).toBeVisible();
  const updated = await api(`/issues/${issue.id}`, { expected_version: issue.version, title: `${title}-已更新`, status: "closed" }, "PATCH");
  expect(updated).toMatchObject({ project_id: defaultProject.id, title: `${title}-已更新`, description: issue.description, status: "closed" });
  await page.reload();
  await expect(page.getByRole("link", { name: new RegExp(`${title}-已更新`) }).first()).toBeVisible();
  expect((await raw(`/issues/${issue.id}?expected_version=${updated.version}`, undefined, "DELETE")).status()).toBe(204);
  expect((await raw(`/issues/${issue.id}`)).status()).toBe(404);
  expect((await api(`/projects/${defaultProject.id}/issues`)).items.some((item) => item.id === issue.id)).toBe(false);
  await page.reload();
  await expect(page.getByRole("link", { name: new RegExp(title) })).toHaveCount(0);
});

for (const width of [1280, 390]) test.describe(`默认项目截图 ${width}px`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("默认概览与管理保护保持可用且无横向溢出", { tag: ["@default-project", "@layout", width === 390 ? "@mobile" : "@desktop"] }, async ({ page }, info) => {
    await page.goto("/workbench");
    await defaultHeading(page);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`default-overview-${width}.png`), animations: "disabled" });
    const dialog = await manageProject(page, defaultProject, width === 390);
    await expect(dialog.getByLabel("项目名称", { exact: true })).not.toBeEditable();
    await expect(dialog.getByLabel("项目名称", { exact: true })).toHaveValue(defaultName);
    await expect(dialog.getByLabel("备注", { exact: true })).toBeEditable();
    await expect(dialog.getByRole("button", { name: "删除项目", exact: true })).toHaveCount(0);
    await dialog.getByRole("button", { name: "保存", exact: true }).scrollIntoViewIfNeeded();
    await expect(dialog.getByRole("button", { name: "保存", exact: true })).toBeInViewport();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`default-management-${width}.png`), animations: "disabled" });
  });
});
