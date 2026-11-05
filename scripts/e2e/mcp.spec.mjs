import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

// Entirely offline: explicit connections spawn the tiny fixture, not a model.
test.use({ trace: "off" });
let project, headers;
const suffix = randomUUID().slice(0, 8);
const fixture = fileURLToPath(new URL("./fixtures/mcp-server.mjs", import.meta.url));
async function api(request, path, body, method = body === undefined ? "GET" : "POST") {
  const response = await request.fetch(`/v1${path}`, { method, headers, data: body });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return (await response.json()).data;
}
test.beforeAll(async ({ request, baseURL }) => {
  headers = { Origin: baseURL, "Content-Type": "application/json" };
  headers["X-CSRF-Token"] = (await api(request, "/auth/local-session", {})).csrf_token;
  const computer = (await api(request, "/computers")).items[0];
  project = await api(request, "/projects", { computer_id: computer.id, name: `MCP 离线验收 ${suffix}` });
});
test.beforeEach(async ({ page, request }) => {
  headers["X-CSRF-Token"] = (await api(request, "/auth/local-session", {})).csrf_token;
  await page.addInitScript((value) => sessionStorage.setItem("teamva-selection", JSON.stringify(value)), { computerId: project.computer_id, projectId: project.id });
});

test("MCP Tab 创建、真实 stdio 连接、目录、断开及删除 @mcp @smoke", async ({ page, request }, info) => {
  const name = `fixture-${suffix}`;
  await page.goto("/skills");
  await expect(page.getByRole("tab", { name: "技能", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "MCP", exact: true }).click();
  await expect(page).toHaveURL(/tab=mcp/);
  await page.getByRole("button", { name: "新增服务", exact: true }).click();
  const editor = page.getByRole("dialog");
  await editor.getByLabel("服务名称", { exact: true }).fill(name);
  await editor.getByLabel("命令 argv（每行一个参数）", { exact: true }).fill(`${process.execPath}\n${fixture}`);
  await editor.getByLabel("作用域", { exact: true }).selectOption("project");
  await editor.getByRole("button", { name: "创建服务", exact: true }).click();
  await expect(editor.getByRole("status")).toHaveText("已保存");
  await editor.locator("footer").getByRole("button", { name: "关闭", exact: true }).click();
  const card = page.getByRole("article", { name: `${name} (当前项目)`, exact: true });
  await expect(card.getByRole("status")).toHaveText("未连接");
  await card.getByRole("button", { name: "连接", exact: true }).click();
  await expect(card.getByRole("status")).toHaveText("已连接");
  await card.locator("summary").click();
  await expect(card.getByText("fixture_echo", { exact: true })).toBeVisible();
  await expect(card.getByText("fixture_prompt", { exact: true })).toBeVisible();
  await expect(card.getByText("Fixture resource", { exact: true })).toBeVisible();
  await page.screenshot({ path: info.outputPath("mcp-connected.png"), animations: "disabled" });
  await card.getByRole("button", { name: "断开连接", exact: true }).click();
  await expect(card.getByRole("status")).toHaveText("未连接");
  page.once("dialog", (dialog) => dialog.accept());
  await card.getByRole("button", { name: "删除", exact: true }).click();
  await expect(card).toHaveCount(0);
  expect((await api(request, `/mcp/servers?project_id=${project.id}`)).items.some((item) => item.name === name)).toBe(false);
});

test("JSON 导入、遮蔽秘密、编辑自动保存不丢秘密、项目覆盖 @mcp", async ({ page, request }, info) => {
  const name = `import-${suffix}`;
  const value = `fixture-only-${randomUUID()}`;
  await page.goto("/skills?tab=mcp");
  await page.getByRole("button", { name: "导入 JSON", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("粘贴 MCP JSON", { exact: true }).fill(JSON.stringify({ mcpServers: { [name]: { command: process.execPath, args: [fixture], env: { FIXTURE_VALUE: value } } } }));
  await dialog.getByRole("button", { name: "读取配置", exact: true }).click();
  await dialog.getByRole("button", { name: "导入所选", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  const card = page.getByRole("article", { name: `${name} (个人)`, exact: true });
  await card.getByRole("button", { name: "编辑", exact: true }).click();
  const editor = page.getByRole("dialog");
  await expect(editor.getByLabel("值 1", { exact: true })).toHaveValue("");
  await expect(editor.getByLabel("值 1", { exact: true })).toHaveAttribute("placeholder", "已设置；留空保留");
  await editor.getByLabel("执行超时（毫秒）", { exact: true }).fill("12000");
  await expect(editor.getByRole("status")).toHaveText("已保存");
  await editor.locator("footer").getByRole("button", { name: "关闭", exact: true }).click();
  const views = (await api(request, `/mcp/servers?project_id=${project.id}`)).items;
  const entry = views.find((item) => item.name === name && item.scope === "user");
  expect(entry.config.environment.FIXTURE_VALUE === "__TEAMVA_KEEP_SECRET__").toBe(true);
  expect(JSON.stringify(views).includes(value)).toBe(false);
  expect(entry.config.timeout.execution).toBe(12000);
  await api(request, "/mcp/servers", { name, scope: "project", project_id: project.id, version: 0, config: { type: "local", command: [process.execPath, fixture], disabled: true } });
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(card.getByText("已被当前项目同名服务覆盖", { exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: "连接", exact: true })).toBeDisabled();
  await expect(page.getByRole("article", { name: `${name} (当前项目)`, exact: true }).getByRole("status")).toHaveText("已停用");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: info.outputPath("mcp-mobile.png"), animations: "disabled" });
  for (const item of (await api(request, `/mcp/servers?project_id=${project.id}`)).items.filter((item) => item.name === name)) {
    const params = new URLSearchParams({ scope: item.scope, version: String(item.version), ...(item.project_id ? { project_id: item.project_id } : {}) });
    await api(request, `/mcp/servers/${name}?${params}`, undefined, "DELETE");
  }
});
