import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";

test.use({ trace: "off" });

// Real Chrome rectangles, not CSS-string assertions. Only future schedules are
// saved; these fixtures never mention an Agent, create a Run or invoke a model.
let project, agent, issue;
async function authenticatedAPI(request, baseURL) {
  // Request fixtures do not share browser contexts (or one another's cookies).
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const bootstrap = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(bootstrap.ok()).toBeTruthy();
  headers["X-CSRF-Token"] = (await bootstrap.json()).data.csrf_token;
  return async (path, data) => {
    const response = await request.fetch(`/v1${path}`, { headers: { ...headers, ...(data === undefined ? {} : { "Idempotency-Key": randomUUID() }) }, method: data === undefined ? "GET" : "POST", data });
    expect(response.ok(), `${path}: ${response.status()}`).toBeTruthy();
    return (await response.json()).data;
  };
}
test.beforeAll(async ({ request, baseURL }) => {
  const api = await authenticatedAPI(request, baseURL);
  const computer = (await api("/computers")).items[0];
  expect(computer).toBeTruthy();
  let service;
  await expect.poll(async () => {
    service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
    return Boolean(service);
  }, { message: "几何验收需要真实 ready Provider，但不会执行模型", timeout: 30_000, intervals: [100, 250, 500, 1_000] }).toBe(true);
  project = await api("/projects", { computer_id: computer.id, name: "字段几何验收", description: "隔离 UI 测试，不执行 Agent。" });
  agent = await api(`/computers/${computer.id}/agents`, { name: "字段几何 Agent", agent_service_id: service.id, system_prompt: "仅用于表单几何验收。" });
  issue = await api(`/projects/${project.id}/issues`, { title: "字段布局已有 Issue", status: "open" });
});
test.beforeEach(async ({ page }) => {
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: project.computer_id, projectId: project.id });
});

async function geometry(group, count) {
  await expect(group).toHaveCount(1);
  await expect(group).toBeVisible();
  await expect(group.locator(":scope > .am-field")).toHaveCount(count);
  return group.evaluate((element) => {
    const rect = (node) => {
      if (!node) throw new Error("Missing field label/control rectangle");
      const { top, bottom, left, right, width, height } = node.getBoundingClientRect();
      return { top, bottom, left, right, width, height };
    };
    return {
      group: rect(element),
      action: element.querySelector(":scope > .am-field-group__actions > .am-field-group__action-controls > button, :scope > .am-field-group__actions > .am-field-group__action-controls > label") ? rect(element.querySelector(":scope > .am-field-group__actions > .am-field-group__action-controls > button, :scope > .am-field-group__actions > .am-field-group__action-controls > label")) : null,
      fields: [...element.querySelectorAll(":scope > .am-field")].map((field) => ({
        field: rect(field), label: rect(field.querySelector(":scope > .am-field__label")),
        slot: rect(field.querySelector(":scope > .am-field__control")),
        // Select's border belongs to its outer span; ModelPicker's to its input.
        control: rect(field.querySelector(".am-select, .am-input, .am-textarea")),
        hint: field.querySelector(":scope > .am-field__hint") ? rect(field.querySelector(":scope > .am-field__hint")) : null
      }))
    };
  });
}
function close(actual, expected, message) {
  expect(Math.abs(actual - expected), `${message}: ${actual} vs ${expected}`).toBeLessThanOrEqual(1);
}

async function compoundGeometry(page, input, button) {
  const [inputElement, buttonElement] = await Promise.all([input.elementHandle(), button.elementHandle()]);
  expect(inputElement).not.toBeNull();
  expect(buttonElement).not.toBeNull();
  try {
    // Both real border boxes must belong to one layout snapshot. Two awaited
    // boundingBox() calls can straddle a directory load and modal recentering.
    return await page.evaluate(({ input, button }) => {
      const rect = (element) => {
        const { x, y, width, height } = element.getBoundingClientRect();
        return { x, y, width, height };
      };
      return { input: rect(input), button: rect(button), buttonDisabled: button.disabled };
    }, { input: inputElement, button: buttonElement });
  } finally {
    await Promise.all([inputElement.dispose(), buttonElement.dispose()]);
  }
}

async function stableCompoundGeometry(page, input, button) {
  const samples = [];
  let previous, stable = 0, geometry;
  await expect.poll(async () => {
    geometry = await compoundGeometry(page, input, button);
    samples.push(geometry);
    const signature = JSON.stringify(geometry);
    stable = signature === previous ? stable + 1 : 0;
    previous = signature;
    return stable;
  }, { message: "复合控件的真实 border box 必须连续三次同帧采样保持稳定", timeout: 5_000, intervals: [50, 100, 150] }).toBeGreaterThanOrEqual(2);
  return { geometry, samples };
}

async function assertGroup(page, group, count, collapsed, uniformHeight = true) {
  const { fields, group: bounds, action } = await geometry(group, count);
  for (const [index, item] of fields.entries()) {
    expect(item.control.height, `control ${index} has a real border box`).toBeGreaterThan(20);
    expect(item.control.width, `control ${index} has usable width`).toBeGreaterThan(80);
    expect(item.label.bottom, `label ${index} must not overlap control`).toBeLessThanOrEqual(item.control.top + 1);
    if (item.hint) expect(item.control.bottom, `hint ${index} must follow control`).toBeLessThanOrEqual(item.hint.top + 1);
    expect(item.control.left).toBeGreaterThanOrEqual(bounds.left - 1);
    expect(item.control.right).toBeLessThanOrEqual(bounds.right + 1);
    if (uniformHeight) close(item.control.height, fields[0].control.height, `uniform control height ${index}`);
    if (collapsed && index) {
      close(item.control.left, fields[0].control.left, `collapsed control left ${index}`);
      expect(fields[index - 1].field.bottom, `collapsed fields ${index - 1}/${index} must not overlap`).toBeLessThanOrEqual(item.field.top + 1);
    }
  }
  if (!collapsed && fields.length > 1) for (let index = 0; index < fields.length; index += 2) {
    const a = fields[index], b = fields[index + 1];
    expect(b, "paired field must exist").toBeTruthy();
    expect(a.control.right).toBeLessThanOrEqual(b.control.left + 1);
    for (const axis of ["top", "bottom", "height"]) close(a.control[axis], b.control[axis], `paired control ${index}/${index + 1} ${axis}`);
    if (index) expect(fields[index - 1].field.bottom).toBeLessThanOrEqual(a.field.top + 1);
  }
  if (action) {
    if (collapsed) expect(fields.at(-1).field.bottom).toBeLessThanOrEqual(action.top + 1);
    else for (const axis of ["top", "bottom", "height"]) close(action[axis], fields[0].control[axis], `action/control ${axis}`);
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "page must not overflow horizontally").toBe(true);
  expect(await group.evaluate((element) => element.scrollWidth <= element.clientWidth + 1), "group must not overflow horizontally").toBe(true);
}

async function stressGroup(page, group, count, collapsed, uniformHeight = true) {
  // Assert the untouched product before any DOM-only stress; no injected styles
  // or altered control dimensions can conceal a product regression.
  await expect(group).toBeVisible();
  await group.scrollIntoViewIfNeeded();
  await assertGroup(page, group, count, collapsed, uniformHeight);
  const original = await group.evaluate((element) => {
    const lines = (node) => {
      const range = document.createRange(); range.selectNodeContents(node);
      return new Set([...range.getClientRects()].map((rect) => Math.round(rect.top * 10))).size;
    };
    const wrap = (node, target) => {
      node.textContent = "";
      for (let i = 0; i < 600 && lines(node) < target; i++) node.textContent += "字段说明";
      return lines(node);
    };
    return [...element.querySelectorAll(":scope > .am-field")].map((field, index) => {
      const label = field.querySelector(":scope > .am-field__label");
      const hint = field.querySelector(":scope > .am-field__hint");
      const saved = { label: label.textContent, hint: hint?.textContent ?? null, labelLines: null, hintLines: null };
      if (index % 2 === 0) saved.labelLines = wrap(label, 2);
      else {
        const target = hint ?? Object.assign(document.createElement("small"), { className: "am-field__hint" });
        if (!hint) field.append(target);
        saved.hintLines = wrap(target, 3);
      }
      return saved;
    });
  });
  try {
    for (const item of original) {
      if (item.labelLines !== null) { expect(item.labelLines).toBeGreaterThanOrEqual(2); expect(item.labelLines).toBeLessThanOrEqual(3); }
      if (item.hintLines !== null) { expect(item.hintLines).toBeGreaterThanOrEqual(3); expect(item.hintLines).toBeLessThanOrEqual(4); }
    }
    await assertGroup(page, group, count, collapsed, uniformHeight);
  } finally {
    await group.evaluate((element, saved) => {
      [...element.querySelectorAll(":scope > .am-field")].forEach((field, index) => {
        field.querySelector(":scope > .am-field__label").textContent = saved[index].label;
        const hint = field.querySelector(":scope > .am-field__hint");
        if (saved[index].hint === null) hint?.remove();
        else hint.textContent = saved[index].hint;
      });
    }, original);
  }
  await group.scrollIntoViewIfNeeded();
  await assertGroup(page, group, count, collapsed, uniformHeight);
}

for (const viewport of [{ width: 1280, height: 820 }, { width: 800, height: 900 }, { width: 390, height: 844 }]) test.describe(`field geometry ${viewport.width}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });
  test("schedule target Selects align in create/edit and new/existing Issue modes", { tag: ["@field-layout", "@layout"] }, async ({ page }, info) => {
    await page.goto("/schedules");
    await page.locator("main header").getByRole("button", { name: "新建定时任务", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const group = dialog.locator('[data-field-group="schedule-target"]');
    const mode = dialog.getByLabel("Issue 目标", { exact: true });
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await dialog.getByLabel("Agent", { exact: true }).selectOption(agent.id);
    await dialog.getByLabel("提示词", { exact: true }).fill("只保存远期任务，不执行。");
    await dialog.getByLabel("调度类型", { exact: true }).selectOption("once");
    await dialog.locator('input[type="datetime-local"]').fill("2040-01-02T09:30");
    await dialog.getByLabel("时区", { exact: true }).fill("UTC");
    await dialog.getByLabel("时区", { exact: true }).press("Tab");
    await expect(mode).toHaveValue("new");
    await stressGroup(page, group, 2, viewport.width < 640);
    await mode.selectOption("existing");
    await dialog.getByLabel("选择 Issue", { exact: true }).selectOption(issue.id);
    await stressGroup(page, group, 2, viewport.width < 640);
    await page.screenshot({ path: info.outputPath("schedule-create-existing.png"), animations: "disabled" });
    const created = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/v1/projects/${project.id}/scheduled-tasks`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const task = (await response.json()).data;
    expect(task.schedule_kind).toBe("once");
    expect(new Date(task.schedule_expression).getUTCFullYear()).toBe(2040);
    await expect(dialog).toBeHidden();
    await page.locator(`[data-schedule-id="${task.id}"]`).getByRole("button", { name: "编辑", exact: true }).click();
    await expect(mode).toHaveValue("existing");
    await stressGroup(page, group, 2, viewport.width < 640);
    await mode.selectOption("new");
    await stressGroup(page, group, 2, viewport.width < 640);
    await page.screenshot({ path: info.outputPath("schedule-edit-new.png"), animations: "disabled" });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
  });

  test("Agent, display preferences and memory identity share aligned field tracks", { tag: ["@field-layout", "@layout"] }, async ({ page }, info) => {
    await page.goto(`/agents?agent=${agent.id}`);
    const configuration = page.locator('[data-field-group="agent-configuration"]');
    await expect(configuration.locator(".am-input").first()).toBeEnabled();
    await expect(configuration.locator(".am-model-picker .am-input")).toHaveCount(1);
    await stressGroup(page, configuration, 4, viewport.width < 1024);
    await page.screenshot({ path: info.outputPath("agent-configuration.png"), animations: "disabled" });
    await page.goto("/settings");
    await stressGroup(page, page.locator('[data-field-group="display-preferences"]'), 2, viewport.width < 768);
    const password = page.getByLabel("访问密码", { exact: true });
    const reveal = page.getByRole("button", { name: "显示密码", exact: true });
    await expect(password).toBeVisible();
    await expect(reveal).toBeVisible();
    const { geometry: { input: inputBox, button: buttonBox } } = await stableCompoundGeometry(page, password, reveal);
    close(inputBox.y, buttonBox.y, "password/toggle top");
    close(inputBox.height, buttonBox.height, "password/toggle height");
    close(inputBox.y + inputBox.height, buttonBox.y + buttonBox.height, "password/toggle bottom");
    expect(inputBox.x + inputBox.width).toBeLessThanOrEqual(buttonBox.x + 1);
    await reveal.click();
    await expect(password).toHaveAttribute("type", "text");
    await page.getByRole("button", { name: "隐藏密码", exact: true }).click();
    await expect(password).toHaveAttribute("type", "password");
    await page.goto("/memories");
    await page.getByRole("button", { name: "新建记忆", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const identity = dialog.locator("form");
    await expect(identity.locator(":scope > .am-field > .am-field__label")).toHaveText(["类型", "内容"]);
    await expect(identity.getByRole("textbox", { name: "标题", exact: true })).toHaveCount(0);
    await expect(identity.getByRole("combobox", { name: "类型", exact: true })).toHaveCount(1);
    await expect(dialog.getByLabel(/唯一|key|uuid/i)).toHaveCount(0);
    await expect(dialog.locator("input")).toHaveCount(0);
    await expect(identity.getByRole("textbox", { name: "内容", exact: true })).toHaveCount(1);
    await expect(dialog.locator('input[type="hidden"]')).toHaveCount(0);
    // Creation now has a single-column Kind/Content form, not a paired identity
    // group. Keep real border boxes and asymmetric multiline label/hint stress;
    // unequal Select/Textarea heights are intentional, paired groups above
    // still retain their equal-height/aligned-track assertions unchanged.
    await stressGroup(page, identity, 2, true, false);
    const contentBox = await identity.getByRole("textbox", { name: "内容", exact: true }).boundingBox();
    const kindBox = await identity.locator(".am-select").boundingBox();
    close(contentBox.x, kindBox.x, "memory create controls share left border");
    close(contentBox.width, kindBox.width, "memory create controls retain full usable width");
    expect(contentBox.height, "memory body retains an actual multiline writing area").toBeGreaterThan(kindBox.height * 2);
    await page.screenshot({ path: info.outputPath("memory-identity.png"), animations: "disabled" });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await page.goto("/resources");
    await page.getByRole("button", { name: "添加资源", exact: true }).click();
    const path = dialog.getByRole("textbox", { name: "本地目录路径", exact: true });
    const selectDirectory = dialog.getByRole("button", { name: "选择当前目录", exact: true });
    await expect(path).toBeVisible();
    await expect(selectDirectory).toBeVisible();
    const beforeReady = await compoundGeometry(page, path, selectDirectory);
    // Enabled means the real directory request has completed and a selectable
    // target exists. Visibility alone is also true for the loading skeleton.
    await expect(selectDirectory).toBeEnabled();
    const { geometry: { input: pathBox, button: selectBox }, samples } = await stableCompoundGeometry(page, path, selectDirectory);
    const geometryEvidence = info.outputPath("directory-compound-geometry.json");
    await writeFile(geometryEvidence, JSON.stringify({ viewport, beforeReady, stableSamples: samples, centerDelta: Math.abs(pathBox.y + pathBox.height / 2 - selectBox.y - selectBox.height / 2) }, null, 2));
    await info.attach("directory-compound-geometry.json", { path: geometryEvidence, contentType: "application/json" });
    expect(pathBox.width, "directory input retains real editable width").toBeGreaterThan(100);
    expect(pathBox.x + pathBox.width, "directory button never covers input").toBeLessThanOrEqual(selectBox.x + 1);
    close(pathBox.y + pathBox.height / 2, selectBox.y + selectBox.height / 2, "directory compound center line");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.screenshot({ path: info.outputPath("directory-picker.png"), animations: "disabled" });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
  });

  test("work-record filters and inspiration completion align actions to the control track", { tag: ["@field-layout", "@layout"] }, async ({ page, request, baseURL }, info) => {
    await page.goto("/work-records");
    await stressGroup(page, page.locator('[data-field-group="work-search"]'), 2, viewport.width < 768);
    await stressGroup(page, page.locator('[data-field-group="work-dates"]'), 2, viewport.width < 768);
    await page.screenshot({ path: info.outputPath("work-record-fields.png"), animations: "disabled" });
    const api = await authenticatedAPI(request, baseURL);
    const note = await api("/inspirations", { project_id: project.id, title: "灵感字段对齐", markdown: "仅用于字段布局测试。" });
    await page.goto(`/inspirations?note=${note.id}`);
    await stressGroup(page, page.locator('[data-field-group="inspiration-project"]'), 1, viewport.width < 640);
    await page.screenshot({ path: info.outputPath("inspiration-project.png"), animations: "disabled" });
  });
});
