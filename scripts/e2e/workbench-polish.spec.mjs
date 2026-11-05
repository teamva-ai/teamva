import { test as base, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// 真实 loopback HTTP + Chrome，无 Run/模型调用，不复用其他 spec 的 fixture。
// 密码专项仅截获本页请求：不启用真实外部监听，不记录正文，不截图口令。
const projectLinks = [
  ["/workbench", "概览"], ["/issues", "任务"], ["/conversations", "分支对话"],
  ["/resources", "资源"], ["/memories", "项目记忆"], ["/schedules", "定时任务"], ["/changes", "变更"]
];
const localLinks = [["/agents", "Agents"], ["/skills", "技能 & MCP"]];
const preferenceURL = "**/v1/me/preferences";
const customColors = [
  { key: "background", label: "背景色", property: "--color-canvas", values: ["#f7ece2", "#e8f1f7", "#f3eddf"] },
  { key: "accent", label: "强调色", property: "--color-accent", values: ["#123456", "#246842", "#347856"] },
  { key: "control", label: "控件色", property: "--color-control", values: ["#254f96", "#7c3152", "#254f87"] }
];
let project, readyAgent, defaultProject, lumi;
const fixtureSuffix = randomUUID().slice(0, 8);
const noteMarker = `列表专属备注-${fixtureSuffix}`;
const modelMarker = `polish-model-marker-${fixtureSuffix}`;

async function authenticatedAPI(request, baseURL) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const response = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(response.status()).toBe(200);
  headers["X-CSRF-Token"] = (await response.json()).data.csrf_token;
  return async (path, data, method = data === undefined ? "GET" : "POST") => {
    const result = await request.fetch(`/v1${path}`, { method, headers, data });
    // 不把响应正文、认证头或密码放入断言/日志。
    expect(result.ok(), `${method} ${path}: HTTP ${result.status()}`).toBe(true);
    return result.status() === 204 ? undefined : (await result.json()).data;
  };
}

const test = base.extend({
  localAPI: async ({ request, baseURL }, use) => use(await authenticatedAPI(request, baseURL)),
  noExecution: [async ({ page }, use) => {
    const denied = [];
    await page.route("**/v1/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      // 导航审计是既有无模型遥测；只放行明确的本页无执行 POST，
      // Run/mention/run-now/Hello 仍由默认拒绝策略拦截。
      if (request.method() === "POST" && ![
        "/v1/auth/local-session", "/v1/scheduled-tasks/preview", "/v1/work-records/navigation"
      ].includes(path)) {
        denied.push(path);
        await route.abort("blockedbyclient");
        return;
      }
      await route.fallback();
    });
    await use();
    expect(denied, "本回归禁止执行 Run、mention、run-now、Provider Hello 或其他主动 POST").toEqual([]);
  }, { auto: true }]
});
// 禁用自动失败截图，避免密码专项的明文可见状态被记录；布局用例显式截图。
test.use({ screenshot: "off", trace: "off", video: "off" });

test.beforeAll(async ({ request, baseURL }) => {
  const api = await authenticatedAPI(request, baseURL);
  const computer = (await api("/computers")).items[0];
  expect(computer).toBeTruthy();
  defaultProject = (await api(`/projects?computer_id=${computer.id}`)).items.find((item) => item.is_default);
  expect(defaultProject).toMatchObject({ name: "默认项目", is_default: true });
  lumi = await api("/work-assistant");
  expect(lumi).toMatchObject({ system_role: "general", status: "active" });
  expect(Boolean(lumi.note?.trim()), "真实默认 Lumi 必须自带非空备注").toBe(true);
  const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
  expect(service, "需要 ready CLI 创建普通配置 Agent，但不会运行它").toBeTruthy();
  project = await api("/projects", {
    computer_id: computer.id, name: `工作台细节回归-${fixtureSuffix}`,
    description: "隔离 HTTP/Chrome 界面回归，不创建计划或主动执行 Agent。"
  });
  readyAgent = await api(`/computers/${computer.id}/agents`, {
    name: `细节验收 Agent-${fixtureSuffix}`, note: noteMarker, model: modelMarker,
    agent_service_id: service.id, system_prompt: "仅供无模型界面回归；不发送 Run。"
  });
  expect(readyAgent).toMatchObject({ status: "active", note: noteMarker, model: modelMarker });
  expect(Boolean(readyAgent.current_revision_id)).toBe(true);
  expect((await api(`/projects/${project.id}/scheduled-tasks`)).items).toEqual([]);
});

test.beforeEach(async ({ page, localAPI }) => {
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), {
    computerId: project.computer_id, projectId: project.id
  });
  const current = await localAPI("/me/preferences");
  await localAPI("/me/preferences", {
    expected_version: current.version, locale: "zh-CN", theme: "light", appearance: { palette: "default" }
  }, "PATCH");
});

async function noOverflow(page) {
  expect(await page.evaluate(() => ({
    root: document.documentElement.scrollWidth <= innerWidth,
    body: document.body.scrollWidth <= innerWidth
  }))).toEqual({ root: true, body: true });
}
async function navigationItems(nav) {
  return nav.locator("a").evaluateAll((links) => links.map((link) => [link.getAttribute("href"), link.textContent.trim()]));
}
async function keyboardFocus(page, input) {
  await input.scrollIntoViewIfNeeded();
  await input.evaluate((element) => {
    const sentinel = document.createElement("button");
    sentinel.type = "button";
    sentinel.dataset.polishFocusSentinel = "";
    sentinel.style.cssText = "position:fixed;width:1px;height:1px;opacity:0;";
    element.before(sentinel);
    sentinel.focus({ preventScroll: true });
  });
  try {
    await page.keyboard.press("Tab");
    await expect(input).toBeFocused();
    expect(await input.evaluate((element) => element.matches(":focus-visible"))).toBe(true);
  } finally {
    await page.locator("[data-polish-focus-sentinel]").evaluateAll((nodes) => nodes.forEach((node) => node.remove()));
  }
}
async function noInputRing(input) {
  const styles = await input.evaluate((element) => {
    const targets = [element, element.closest(".am-select"), element.closest(".am-field__control")].filter(Boolean);
    return targets.map((target) => {
      const style = getComputedStyle(target);
      return { outline: style.outlineStyle, width: style.outlineWidth, shadow: style.boxShadow };
    });
  });
  for (const style of styles) expect(style).toEqual({ outline: "none", width: "0px", shadow: "none" });
}
async function saved(page, localAPI, expected) {
  await expect.poll(async () => {
    const value = await localAPI("/me/preferences");
    return { theme: value.theme, appearance: value.appearance };
  }).toMatchObject(expected);
  await expect(page.locator("[data-preferences-autosave]")).not.toContainText("正在应用");
}

for (const width of [1280, 390]) test.describe(`workbench polish ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });

  test("顶栏不再显示工具版本，右侧留空，左侧审批与帮助仍可用", { tag: ["@workbench-polish", "@layout"] }, async ({ page }, info) => {
    await page.route("**/v1/approvals?status=pending", (route) => route.fulfill({
      status: 200, contentType: "application/json", body: JSON.stringify({ data: { items: [{ id: "header-approval-fixture", status: "pending", action_kind: "UI fixture" }] } })
    }));
    await page.goto("/workbench");
    const header = page.locator("header").first(), help = header.locator('[data-feature-tour-trigger="overview"]');
    await expect(header.getByLabel("本机编程工具", { exact: true })).toHaveCount(0);
    await expect(header).not.toContainText(/Claude Code|Codex|OpenCode/);
    await expect(help).toBeEnabled();
    const approval = header.getByRole("button", { name: "等待执行确认", exact: true });
    await expect(approval).toBeVisible();
    const positions = await header.evaluate((element) => {
      const search = element.querySelector('[aria-label="打开全局搜索"]').getBoundingClientRect();
      const help = element.querySelector("[data-feature-tour-trigger]").getBoundingClientRect();
      const bounds = element.getBoundingClientRect();
      return { helpRight: help.right, searchLeft: search.left, overflow: document.documentElement.scrollWidth > innerWidth,
        rightEmpty: innerWidth < 768 || document.elementFromPoint(bounds.right - 20, bounds.top + bounds.height / 2) === element };
    });
    expect(positions.helpRight).toBeLessThanOrEqual(positions.searchLeft);
    expect(positions.overflow).toBe(false);
    expect(positions.rightEmpty).toBe(true);
    await help.click();
    await expect(page.locator('[data-feature-tour-dialog="overview"]')).toBeVisible();
    await page.keyboard.press("Escape");
    await approval.click();
    await expect(page.getByRole("dialog")).toContainText("UI fixture");
    await page.keyboard.press("Escape");
    await page.screenshot({ path: info.outputPath(`empty-header-right-${width}.png`), animations: "disabled" });
  });

  for (const { key, label, property, values } of customColors) test(`${label}拖动只预览，结束仅一次真实保存/审计，重复提交与拖回原色不写入`, {
    tag: ["@workbench-polish", "@color-commit", width === 390 ? "@mobile" : "@desktop"]
  }, async ({ page, localAPI }) => {
    let writes = 0;
    await page.route(preferenceURL, async (route) => {
      if (route.request().method() === "PATCH") writes++;
      // 只计数，仍走真实 HTTP；不伪造偏好成功响应或审计列表。
      await route.fallback();
    });
    await page.goto("/settings");
    const input = page.getByLabel(label, { exact: true });
    await expect(input).toBeVisible();
    await expect(page.locator('[data-palette-option="default"]')).toHaveAttribute("aria-checked", "true");
    const baseline = await localAPI("/me/preferences");
    const original = await input.inputValue();
    // 最近事件列表有数量上限，不能比较总条数。ID 仅用于集合差；
    // 断言只输出 action/计数/测试色值，不记录详情、正文或认证数据。
    const action = "user.preferences_updated";
    const beforeIDs = new Set((await localAPI("/audit-events")).items.filter((event) => event.action === action).map((event) => event.id));
    const addedEvents = async () => (await localAPI("/audit-events")).items.filter((event) => event.action === action && !beforeIDs.has(event.id)).length;
    const persistence = async (expectedWrites, expectedVersion, expectedEvents) => {
      expect(writes, "真实偏好 PATCH 计数").toBe(expectedWrites);
      expect((await localAPI("/me/preferences")).version, "偏好版本").toBe(expectedVersion);
      expect(await addedEvents(), `${action} 新增计数`).toBe(expectedEvents);
    };
    const observeStable = async (expectedWrites, expectedVersion, expectedEvents) => {
      // 使用真实时间连续观察至少 350ms，明确跨过旧 200ms 防抖边界；
      // 不修改动画/浏览器时钟，也不以首次成立的零计数结束观察。
      const started = performance.now();
      do {
        await persistence(expectedWrites, expectedVersion, expectedEvents);
        if (performance.now() - started >= 350) break;
        await new Promise((resolve) => setTimeout(resolve, 75));
      } while (true);
    };
    const preview = async (value) => {
      const actual = await input.evaluate((element, { value, property }) => {
        element.value = value;
        element.dispatchEvent(new Event("input", { bubbles: true }));
        return getComputedStyle(document.documentElement).getPropertyValue(property).trim();
      }, { value, property });
      expect(actual, "input 同步更新真实 CSS 预览").toBe(value);
    };
    const changeAndBlur = () => input.evaluate((element) => {
      element.dispatchEvent(new Event("change", { bubbles: true }));
      element.focus({ preventScroll: true });
      element.blur();
    });

    // 默认有效色尚未显式存入 appearance，拖回它也不应创建自定义值。
    await preview(values[0]);
    await observeStable(0, baseline.version, 0);
    await preview(original);
    await changeAndBlur();
    await observeStable(0, baseline.version, 0);

    for (const value of values) {
      await preview(value);
      await observeStable(0, baseline.version, 0);
    }
    await input.dispatchEvent("change");
    const final = values.at(-1);
    await saved(page, localAPI, { appearance: { [key]: final } });
    await expect.poll(addedEvents).toBe(1);
    await persistence(1, baseline.version + 1, 1);

    // 原生 change 后的重复 change + 真实失焦不再生成 PATCH/日志。
    await changeAndBlur();
    await observeStable(1, baseline.version + 1, 1);
    await page.reload();
    await expect(input).toHaveValue(final);
    expect(await page.locator("html").evaluate((element, property) => getComputedStyle(element).getPropertyValue(property).trim(), property)).toBe(final);
    await observeStable(1, baseline.version + 1, 1);

    // 已保存的自定义色同样按有效值去重，而不是留下拖动中间色。
    await preview(values[0]);
    await observeStable(1, baseline.version + 1, 1);
    await preview(final);
    await changeAndBlur();
    await observeStable(1, baseline.version + 1, 1);
  });

  test("导航中文命名、默认项目与 Agent 备注/同行 CLI badge", { tag: ["@workbench-polish", "@layout", width === 390 ? "@mobile" : "@desktop"] }, async ({ page }, info) => {
    await page.goto(`/workbench?project=${encodeURIComponent(defaultProject.id)}`);
    await expect(page.getByRole("heading", { name: "默认项目", level: 1, exact: true })).toBeVisible();
    if (width === 1280) {
      const projectNav = page.getByRole("navigation", { name: "项目内容", exact: true }).first();
      const localNav = page.getByRole("navigation", { name: "本机", exact: true });
      await expect(projectNav).toBeVisible();
      expect(await navigationItems(projectNav)).toEqual(projectLinks);
      expect(await navigationItems(localNav)).toEqual(localLinks);
      await expect(localNav.locator('a[href="/programming-tools"]')).toHaveCount(0);
    } else {
      expect(await navigationItems(page.getByRole("navigation", { name: "项目内容", exact: true }))).toEqual(projectLinks.slice(0, 5));
      await page.getByRole("button", { name: "更多", exact: true }).click();
      await expect(page.getByRole("link", { name: "技能 & MCP", exact: true })).toBeVisible();
      await expect(page.getByRole("link", { name: "编程工具", exact: true })).toHaveCount(0);
      await page.keyboard.press("Escape");
    }
    await noOverflow(page);
    await page.screenshot({ path: info.outputPath(`polish-default-${width}.png`), animations: "disabled" });

    // 若其他 spec 已配置 Lumi，仅覆盖 GET 列表配置投影重现“未配置”。
    // 保留真实 ID/名称/备注，普通 Agent 完全来自真实 API；不修改持久化 Lumi。
    await page.route(`**/v1/computers/${project.computer_id}/agents`, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const envelope = await response.json();
      envelope.data.items = envelope.data.items.map((item) => item.id === lumi.id
        ? { ...item, agent_service_id: "", current_revision_id: "", model: "" } : item);
      await route.fulfill({ response, json: envelope });
    });
    info.annotations.push({ type: "boundary", description: "真实 Lumi 备注；仅 GET 列表配置投影模拟未配置，未修改持久化 Lumi。" });
    await page.goto(`/agents?agent=${encodeURIComponent(readyAgent.id)}`);
    const row = page.locator(`button[data-agent-id="${readyAgent.id}"]`);
    await row.scrollIntoViewIfNeeded();
    await expect(row.getByText(noteMarker, { exact: true })).toBeVisible();
    await expect(row).not.toContainText(modelMarker);
    const name = row.locator("strong");
    const badge = name.locator("..").locator(":scope > span").first();
    await expect(badge).not.toBeEmpty();
    const [nameBox, badgeBox, rowBox] = await Promise.all([name.boundingBox(), badge.boundingBox(), row.boundingBox()]);
    expect(Boolean(nameBox && badgeBox && rowBox)).toBe(true);
    expect(Math.abs(nameBox.y + nameBox.height / 2 - badgeBox.y - badgeBox.height / 2), "CLI badge 与名称真实 border box 同行").toBeLessThanOrEqual(2);
    expect(badgeBox.x).toBeGreaterThanOrEqual(nameBox.x + nameBox.width - 1);
    expect(badgeBox.x + badgeBox.width).toBeLessThanOrEqual(rowBox.x + rowBox.width);
    const lumiRow = page.locator(`button[data-agent-id="${lumi.id}"]`);
    await expect(lumiRow.locator("[title]")).toHaveText(lumi.note);
    await expect(lumiRow).toContainText("未配置");
    await expect(lumiRow).not.toContainText("OpenCode");
    await noOverflow(page);
    await page.screenshot({ path: info.outputPath(`polish-agents-${width}.png`), animations: "disabled" });
    if (width === 1280) {
      await page.goto("/programming-tools");
      await expect(page.getByRole("heading", { name: "本机编程工具", exact: true })).toBeVisible();
      await expect(page.getByText("找不到这个页面", { exact: true })).toHaveCount(0);
    }
  });

  test("空定时任务中央 CTA、Lumi 普通可配置与未配置引导", { tag: ["@workbench-polish", "@layout", width === 390 ? "@mobile" : "@desktop"] }, async ({ page }, info) => {
    // 只投影 GET 响应，不修改持久化 Lumi；两种状态都由同一真实 Agent 驱动。
    let lumiConfigured = false;
    await page.route(`**/v1/computers/${project.computer_id}/agents`, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const envelope = await response.json();
      envelope.data.items = envelope.data.items.map((item) => item.id === lumi.id
        ? {
            ...item,
            agent_service_id: lumiConfigured ? readyAgent.agent_service_id : "",
            current_revision_id: lumiConfigured ? "revision-lumi-polish" : "",
            model: lumiConfigured ? "fixture/model" : ""
          }
        : item);
      await route.fulfill({ response, json: envelope });
    });
    await page.goto("/schedules");
    const empty = page.getByText("暂无定时任务", { exact: true }).locator("..");
    await expect(empty).toBeVisible();
    const centralCTA = empty.getByRole("button", { name: "新建定时任务", exact: true });
    await expect(centralCTA).toBeVisible();
    await expect(page.getByRole("button", { name: "新建定时任务", exact: true })).toHaveCount(2);
    const [emptyBox, ctaBox] = await Promise.all([empty.boundingBox(), centralCTA.boundingBox()]);
    expect(Math.abs(emptyBox.x + emptyBox.width / 2 - ctaBox.x - ctaBox.width / 2), "CTA 位于 EmptyState 中央而不是顶部工具栏").toBeLessThanOrEqual(2);
    await noOverflow(page);
    await page.screenshot({ path: info.outputPath(`polish-schedule-empty-${width}.png`), animations: "disabled" });
    await centralCTA.click();
    let dialog = page.getByRole("dialog", { name: "新建定时任务", exact: true });
    let create = dialog.getByRole("button", { name: "创建", exact: true });
    const prompt = dialog.getByLabel("提示词", { exact: true });
    await expect(create).toBeDisabled();
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await expect(prompt).toHaveValue("");
    await keyboardFocus(page, prompt);
    await noInputRing(prompt);
    await page.keyboard.type(`未来配置-${width}`);
    let agents = dialog.getByLabel("Agent", { exact: true });
    const unconfiguredLumi = agents.locator(`option[value="${lumi.id}"]`);
    await expect(unconfiguredLumi).toHaveCount(1);
    await expect(unconfiguredLumi).toBeDisabled();
    await expect(unconfiguredLumi).toContainText("未配置");
    await expect(unconfiguredLumi).not.toContainText("只读助手");
    await expect(dialog).toContainText("当前电脑有尚未配置的 Agent，请先保存其配置后再用于定时任务。");
    await expect(agents.locator(`option[value="${readyAgent.id}"]`)).toBeEnabled();
    await agents.selectOption(readyAgent.id);
    await dialog.getByLabel("提示词", { exact: true }).fill("仅验证表单可创建，不提交计划，也不运行模型。");
    await dialog.getByLabel("时区", { exact: true }).fill("UTC");
    await dialog.getByLabel("时区", { exact: true }).press("Tab");
    await expect(create).toBeEnabled();
    await noOverflow(page);
    await page.screenshot({ path: info.outputPath(`polish-schedule-editor-${width}.png`), animations: "disabled" });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeHidden();
    // 配置后的 Lumi 与普通 Agent 一样可选；不点击创建，避免留下真实计划任务。
    lumiConfigured = true;
    await page.reload();
    await page.getByRole("button", { name: "新建定时任务", exact: true }).first().click();
    dialog = page.getByRole("dialog", { name: "新建定时任务", exact: true });
    create = dialog.getByRole("button", { name: "创建", exact: true });
    agents = dialog.getByLabel("Agent", { exact: true });
    const configuredLumi = agents.locator(`option[value="${lumi.id}"]`);
    await expect(configuredLumi).toBeEnabled();
    await expect(configuredLumi).toHaveText("Lumi");
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await dialog.getByLabel("提示词", { exact: true }).fill("仅验证 Lumi 可作为普通 Agent 选择，不提交计划。");
    await agents.selectOption(lumi.id);
    await dialog.getByLabel("时区", { exact: true }).fill("UTC");
    await dialog.getByLabel("时区", { exact: true }).press("Tab");
    await expect(create).toBeEnabled();
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeHidden();
  });
});

test("设置快速选择串行自动保存，旧响应不覆盖新选择且刷新仍保留", { tag: ["@workbench-polish", "@smoke"] }, async ({ page, localAPI }) => {
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  let writes = 0, active = 0, maxActive = 0;
  await page.route(preferenceURL, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const index = ++writes;
    maxActive = Math.max(maxActive, ++active);
    let response;
    try {
      if (index === 1) await held;
      response = await route.fetch();
    } finally { active--; }
    await route.fulfill({ response });
  });
  try {
    await page.goto("/settings");
    await expect(page.getByRole("button", { name: /^(保存设置|放弃预览)$/ })).toHaveCount(0);
    const theme = page.getByLabel("主题", { exact: true });
    await expect(theme).toHaveValue("light");
    await expect(page.locator('[data-palette-option="default"]')).toHaveAttribute("aria-checked", "true");
    expect(writes, "启动 hydration 不应写偏好").toBe(0);
    await theme.selectOption("dark");
    await expect.poll(() => writes).toBe(1);
    await page.locator('[data-palette-option="ocean"]').click();
    await theme.selectOption("light");
    await page.locator('[data-palette-option="iris"]').click();
    await expect(page.locator("html")).toHaveAttribute("data-palette", "iris");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    expect(writes, "首个请求未完成时不能并发发送后续选择").toBe(1);
    release();
    await saved(page, localAPI, { theme: "light", appearance: { palette: "iris" } });
    expect(writes).toBe(2);
    expect(maxActive).toBe(1);
    await page.reload();
    await expect(theme).toHaveValue("light");
    await expect(page.locator('[data-palette-option="iris"]')).toHaveAttribute("aria-checked", "true");
  } finally { release(); await page.unroute(preferenceURL); }
});

test("颜色连续输入立即离页仍 flush，并通过真实 HTTP/刷新持久化", { tag: "@workbench-polish" }, async ({ page, localAPI }) => {
  await page.goto("/settings");
  const accent = page.getByLabel("强调色", { exact: true });
  await expect(accent).toBeVisible();
  const preview = await accent.evaluate((element) => {
    for (const value of ["#123456", "#246842", "#347856"]) {
      element.value = value;
      element.dispatchEvent(new Event("input", { bubbles: true }));
    }
    const color = document.documentElement.style.getPropertyValue("--color-accent");
    // 故意只发 input：同一 JS turn 点真实 SPA 链接，验证离页提交最终草稿，
    // 不依赖 change/blur，也不再把等待去抖当作颜色保存边界。
    document.querySelector('a[href="/issues"]').click();
    return color;
  });
  expect(preview).toBe("#347856");
  await expect(page).toHaveURL(/\/issues$/);
  await expect.poll(async () => (await localAPI("/me/preferences")).appearance?.accent).toBe("#347856");
  await page.reload();
  await expect.poll(() => page.locator("html").evaluate((element) => element.style.getPropertyValue("--color-accent"))).toBe("#347856");
  await page.goto("/settings");
  await expect(accent).toHaveValue("#347856");
});

test("409 不静默重试或丢弃预览，明确刷新后仅重试用户修改字段", { tag: "@workbench-polish" }, async ({ page, localAPI }) => {
  let writes = 0;
  await page.route(preferenceURL, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    writes++;
    if (writes === 1) {
      await route.fulfill({ status: 409, json: { error: { code: "version_conflict", message: "Settings changed elsewhere", retryable: false } } });
    } else await route.fulfill({ response: await route.fetch() });
  });
  await page.goto("/settings");
  await page.getByLabel("主题", { exact: true }).selectOption("dark");
  const status = page.locator("[data-preferences-autosave]");
  const refresh = status.getByRole("button", { name: "刷新设置", exact: true });
  await expect(refresh).toBeVisible();
  await expect(page.getByLabel("主题", { exact: true })).toHaveValue("dark");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(status.getByRole("button", { name: "重试", exact: true })).toHaveCount(0);
  const latest = await localAPI("/me/preferences");
  await localAPI("/me/preferences", {
    expected_version: latest.version, appearance: { palette: "ocean", background: "#eaf5fa" }
  }, "PATCH");
  expect(writes).toBe(1);
  await refresh.click();
  await expect(status.getByRole("button", { name: "重试", exact: true })).toBeVisible();
  expect(writes, "刷新只读，不得自动覆盖外部较新设置").toBe(1);
  await expect(page.getByLabel("主题", { exact: true })).toHaveValue("dark");
  await expect(page.locator('[data-palette-option="ocean"]')).toHaveAttribute("aria-checked", "true");
  await status.getByRole("button", { name: "重试", exact: true }).click();
  await saved(page, localAPI, { theme: "dark", appearance: { palette: "ocean", background: "#eaf5fa" } });
  expect(writes).toBe(2);
});

test.describe("remote password commit boundary", () => {
  // 即便失败也不截图切换成明文的测试口令，不录制 trace/HAR/video。
  test("密码只在有效 Enter/离开组后提交；眼睛按钮不提交，已配置开关保留密码", { tag: "@workbench-polish" }, async ({ page }) => {
    const calls = [];
    let passwordSet = false, enabled = false, maySubmit = false, unexpected = 0;
    await page.route("**/v1/system/network-access", async (route) => {
      if (route.request().method() === "PUT") {
        const draft = route.request().postDataJSON();
        if (!maySubmit) unexpected++;
        const hasPassword = typeof draft.password === "string" && draft.password !== "";
        // 仅保留布尔元数据，不保存/输出原始口令或请求正文。
        calls.push({ enabled: Boolean(draft.enabled), hasPassword });
        passwordSet ||= hasPassword;
        enabled = Boolean(draft.enabled);
      }
      await route.fulfill({ json: { data: {
        external_access: enabled, password_set: passwordSet, can_manage: true,
        port: 41720, external_urls: [], listen_error: false
      } } });
    });
    const stable = () => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    await page.goto("/settings");
    const password = page.getByLabel("访问密码", { exact: true });
    const toggle = page.getByRole("switch", { name: "允许远程访问", exact: true });
    const status = page.locator("[data-access-autosave]");
    await toggle.click();
    await password.fill("short");
    await page.getByRole("button", { name: "显示密码", exact: true }).click();
    await page.getByRole("button", { name: "隐藏密码", exact: true }).click();
    await password.press("Enter");
    await expect(page.getByRole("alert").filter({ hasText: "密码至少 8 个字符" })).toBeVisible();
    await stable();
    expect(calls).toEqual([]);
    await password.fill(randomUUID());
    await page.getByRole("button", { name: "显示密码", exact: true }).click();
    await page.getByRole("button", { name: "隐藏密码", exact: true }).click();
    await stable();
    expect(calls).toEqual([]);
    maySubmit = true;
    await password.press("Enter");
    await expect.poll(() => calls.length).toBe(1);
    await expect(status).toContainText(/已生效|已自动保存/);
    expect((await password.inputValue()).length).toBe(0);
    maySubmit = false;
    await password.press("Tab");
    await page.keyboard.press("Tab");
    await stable();
    expect(calls.length).toBe(1);
    await password.fill(randomUUID());
    await password.press("Tab"); // 进入同组眼睛按钮，不提交。
    await stable();
    expect(calls.length).toBe(1);
    maySubmit = true;
    await page.keyboard.press("Tab"); // 离开密码组，提交完整替换口令。
    await expect.poll(() => calls.length).toBe(2);
    await expect(status).toContainText(/已生效|已自动保存/);
    await toggle.click();
    await expect.poll(() => calls.length).toBe(3);
    await expect(toggle).not.toBeChecked();
    await toggle.click();
    await expect.poll(() => calls.length).toBe(4);
    await expect(toggle).toBeChecked();
    expect(calls).toEqual([
      { enabled: true, hasPassword: true }, { enabled: true, hasPassword: true },
      { enabled: false, hasPassword: false }, { enabled: true, hasPassword: false }
    ]);
    expect(unexpected, "未完成输入、眼睛按钮或同组焦点移动不得提交密码").toBe(0);
    await expect(page.getByRole("button", { name: /^保存远程访问(?:设置)?$/ })).toHaveCount(0);
    // 仅受控 HTTP 响应，不验证真实 LAN socket/长期设备信任撤销。
  });
});
