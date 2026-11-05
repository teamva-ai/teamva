import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

const uuidV4 = /[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/i;
const memoryKey = /^memory-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

async function assertNoKeyInputs(scope) {
  await expect(scope.locator('input[type="hidden"]')).toHaveCount(0);
  await expect(scope.getByLabel(/唯一|key|uuid/i)).toHaveCount(0);
  const values = await scope.locator("input, textarea, select").evaluateAll((elements) => elements.map((element) =>
    [element.value, element.defaultValue ?? "", element.getAttribute("placeholder") ?? ""].join("\n")));
  for (const value of values) expect(value, "自动生成的 UUID 不得转移到可见或隐藏输入中").not.toMatch(uuidV4);
}

async function assertCreateFields(dialog) {
  await expect(dialog.locator("form > .am-field > .am-field__label")).toHaveText(["类型", "内容"]);
  await expect(dialog.getByRole("textbox", { name: "标题", exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("combobox", { name: "类型", exact: true })).toHaveValue("fact");
  await expect(dialog.locator("input")).toHaveCount(0);
  await expect(dialog.locator("textarea")).toHaveCount(1);
  await expect(dialog.getByRole("textbox", { name: "内容", exact: true })).toHaveValue("");
  await expect(dialog).not.toContainText(uuidV4);
  await assertNoKeyInputs(dialog);
}

async function assertReadonlyKey(page, item) {
  const row = page.locator(`[data-memory-id="${item.id}"]`);
  await expect(row.locator("code")).toHaveText(item.key);
  await row.scrollIntoViewIfNeeded();
  // Generated keys are longer than legacy semantic keys. Measure their actual
  // text border box as well as overflow, including the 280px desktop sidebar.
  const geometry = await row.evaluate((element) => {
    const rect = (node) => {
      const { left, right, width } = node.getBoundingClientRect();
      return { left, right, width };
    };
    return { row: rect(element), key: rect(element.querySelector("code")), scrollWidth: element.scrollWidth, clientWidth: element.clientWidth, viewport: innerWidth };
  });
  expect(geometry.key.width).toBeGreaterThan(20);
  expect(geometry.key.left).toBeGreaterThanOrEqual(geometry.row.left - 1);
  expect(geometry.key.right).toBeLessThanOrEqual(geometry.row.right + 1);
  expect(geometry.row.left).toBeGreaterThanOrEqual(-1);
  expect(geometry.row.right).toBeLessThanOrEqual(geometry.viewport + 1);
  expect(geometry.scrollWidth).toBeLessThanOrEqual(geometry.clientWidth + 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await assertNoKeyInputs(page.locator("main"));
}

// All successes use the isolated real HTTP service. Only the first POST fails
// synthetically; no existing records are deleted and no Agent/Run is created.
for (const width of [1280, 390]) test.describe(`memory automatic key ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("无需 Key 即可创建，同草稿重试稳定且编辑归档恢复保留 Key", { tag: ["@memory-auto-key", width === 390 ? "@mobile" : "@desktop"] }, async ({ page, request, baseURL }, info) => {
    const headers = { Origin: baseURL, "Content-Type": "application/json" };
    const api = async (path, data, method = data === undefined ? "GET" : "POST") => {
      const response = await request.fetch(`/v1${path}`, { method, headers, data });
      expect(response.ok(), `${path}: HTTP ${response.status()}`).toBe(true);
      return (await response.json()).data;
    };
    headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
    // Memory creation needs no configured Agent. Explicitly skip the unrelated
    // first-run guide in this isolated fixture instead of faking CLI readiness.
    await api("/system/onboarding", { dismissed: true }, "PUT");
    const computer = (await api("/computers")).items[0];
    const project = await api("/projects", { computer_id: computer.id, name: `自动记忆 Key-${width}-${randomUUID().slice(0, 8)}` });
    await page.addInitScript((selection) => {
      sessionStorage.setItem("teamva-selection", JSON.stringify(selection));
      // Simulate the crypto surface of HTTP LAN without opening a LAN listener.
      // uuid.v4 must use getRandomValues, not require secure-context randomUUID.
      Object.defineProperty(crypto, "randomUUID", { configurable: true, value: undefined });
      const getRandomValues = crypto.getRandomValues.bind(crypto);
      window.__memoryRandomValuesCalls = 0;
      Object.defineProperty(crypto, "getRandomValues", { configurable: true, value: (array) => {
        window.__memoryRandomValuesCalls++;
        return getRandomValues(array);
      } });
    }, { computerId: computer.id, projectId: project.id });
    const nativeDialogs = [], pageErrors = [], forbidden = [];
    page.on("dialog", async (dialog) => { nativeDialogs.push(dialog.type()); await dialog.dismiss(); });
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (request.method() === "POST" && /\/v1\/(?:runs|[^?]*\/(?:runs|messages|goals|comments))(?:\/|$)/.test(path)) forbidden.push(path);
    });
    const createPath = `/v1/projects/${project.id}/memories`;
    const attempts = [];
    await page.route((url) => url.pathname === createPath, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      attempts.push(route.request().postDataJSON());
      if (attempts.length === 1) return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "internal_error", message: "临时创建失败，请重试。" } }) });
      return route.fallback();
    });
    const waitForMutation = (method, path) => page.waitForResponse((response) => response.request().method() === method && new URL(response.url()).pathname === path);
    await page.goto("/memories");
    await expect(page.getByRole("heading", { name: "项目记忆", exact: true })).toBeVisible();
    expect(await page.evaluate(() => typeof crypto.randomUUID)).toBe("undefined");
    const open = page.getByRole("button", { name: "新建记忆", exact: true });
    const dialog = page.getByRole("dialog", { name: "新建项目记忆", exact: true });
    const randomCalls = await page.evaluate(() => window.__memoryRandomValuesCalls);
    await open.click();
    await assertCreateFields(dialog);
    expect(await page.evaluate(() => window.__memoryRandomValuesCalls)).toBeGreaterThan(randomCalls);
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeHidden();
    expect(nativeDialogs, "空草稿的内部 Key 不得触发未保存确认").toEqual([]);
    expect(attempts).toHaveLength(0);

    const title = "新记忆";
    const created = [];
    for (let index = 0; index < 2; index++) {
      await open.click();
      await assertCreateFields(dialog);
      await dialog.getByRole("textbox", { name: "内容", exact: true }).fill(`第 ${index + 1} 条独立内容`);
      await assertNoKeyInputs(dialog);
      if (index === 0) {
        const failed = waitForMutation("POST", createPath);
        await dialog.getByRole("button", { name: "创建", exact: true }).click();
        expect((await failed).status()).toBe(500);
        await expect(dialog.getByRole("alert")).toBeVisible();
        await expect(dialog.getByRole("button", { name: "创建", exact: true })).toBeEnabled();
        await expect(dialog.getByRole("textbox", { name: "标题", exact: true })).toHaveCount(0);
        await expect(dialog.getByRole("textbox", { name: "内容", exact: true })).toHaveValue("第 1 条独立内容");
        expect(attempts).toHaveLength(1);
        expect(attempts[0].key).toMatch(memoryKey);
        expect((await api(`/projects/${project.id}/memories`)).items).toEqual([]);
        await assertNoKeyInputs(dialog);
      }
      await page.screenshot({ path: info.outputPath(`memory-create-${index + 1}-${width}.png`), animations: "disabled" });
      const pending = waitForMutation("POST", createPath);
      await dialog.getByRole("button", { name: "创建", exact: true }).click();
      const response = await pending;
      expect(response.status()).toBe(201);
      const sent = response.request().postDataJSON();
      const item = (await response.json()).data;
      expect(sent.key).toMatch(memoryKey);
      expect(item).toMatchObject({ key: sent.key, title, kind: "fact", source_type: "user", project_id: project.id });
      expect(sent).toEqual({ key: item.key, kind: "fact", content: `第 ${index + 1} 条独立内容`, source_type: "user" });
      created.push(item);
      await expect(dialog).toBeHidden();
      await assertReadonlyKey(page, item);
    }
    expect(attempts).toHaveLength(3);
    expect(attempts[0].key, "失败和真实 HTTP 重试必须属于同一草稿").toBe(attempts[1].key);
    expect(created[0].id).not.toBe(created[1].id);
    expect(created[0].key, "相同默认标题的新草稿必须获得不同 Key").not.toBe(created[1].key);
    await page.reload();
    for (const item of created) await assertReadonlyKey(page, item);
    const persisted = (await api(`/projects/${project.id}/memories`)).items;
    expect(persisted).toHaveLength(2);
    for (const item of created) expect(persisted.find((entry) => entry.id === item.id)).toMatchObject({ key: item.key, title });

    const original = created[0];
    const itemPath = `${createPath}/${original.id}`;
    await page.locator(`[data-memory-id="${original.id}"]`).click();
    const editor = page.locator("main");
    await expect(editor.getByText(new RegExp(`^${original.key} · v\\d+ ·`))).toBeVisible();
    await expect(editor.locator("input")).toHaveCount(1);
    await editor.getByRole("textbox", { name: "标题", exact: true }).fill(`${title}-编辑后`);
    await editor.getByRole("textbox", { name: "内容", exact: true }).fill("编辑内容不应更换内部 Key。");
    const patched = waitForMutation("PATCH", itemPath);
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    const patchResponse = await patched;
    expect(patchResponse.status()).toBe(200);
    expect(patchResponse.request().postDataJSON()).not.toHaveProperty("key");
    expect((await patchResponse.json()).data.key).toBe(original.key);
    await expect(editor.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
    await assertReadonlyKey(page, original);

    await editor.getByRole("button", { name: "归档", exact: true }).click();
    const archived = waitForMutation("POST", `${itemPath}/archive`);
    await page.getByRole("dialog", { name: "归档", exact: true }).getByRole("button", { name: "归档", exact: true }).click();
    expect((await archived).status()).toBe(204);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const archivedItems = (await api(`/projects/${project.id}/memories?archived=true`)).items;
    expect(archivedItems.find((item) => item.id === original.id).key).toBe(original.key);
    await page.getByRole("navigation", { name: "项目记忆", exact: true }).getByRole("button", { name: "已归档", exact: true }).click();
    await assertReadonlyKey(page, original);
    await expect(editor.getByRole("textbox", { name: "内容", exact: true })).not.toBeEditable();
    const restored = waitForMutation("POST", `${itemPath}/restore`);
    await editor.getByRole("button", { name: "恢复", exact: true }).click();
    const restoreResponse = await restored;
    expect(restoreResponse.status()).toBe(200);
    expect((await restoreResponse.json()).data.key).toBe(original.key);
    await expect(editor.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
    await page.reload();
    for (const item of created) await assertReadonlyKey(page, item);
    const finalItems = (await api(`/projects/${project.id}/memories`)).items;
    for (const item of created) expect(finalItems.find((entry) => entry.id === item.id).key).toBe(item.key);
    await expect(editor.getByRole("textbox", { name: "标题", exact: true })).toHaveValue(`${title}-编辑后`);
    await page.screenshot({ path: info.outputPath(`memory-restored-${width}.png`), animations: "disabled" });
    expect(await page.evaluate(() => typeof crypto.randomUUID)).toBe("undefined");
    expect(nativeDialogs).toEqual([]);
    expect(pageErrors).toEqual([]);
    expect(forbidden).toEqual([]);
  });
});
