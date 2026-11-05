import { test, expect } from "@playwright/test";

// This suite only saves future schedules. Run-now is a browser contract fixture;
// real Issue/comment/Run atomicity is covered by the SQLite scheduler tests.
let project, agent, issue, closed, foreign;
test.beforeAll(async ({ request, baseURL }) => {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const bootstrap = await request.post("/v1/auth/local-session", { headers, data: {} });
  headers["X-CSRF-Token"] = (await bootstrap.json()).data.csrf_token;
  const api = async (path, data) => {
    const response = await request.fetch(`/v1${path}`, { headers, method: data === undefined ? "GET" : "POST", data });
    expect(response.ok(), `${path}: ${response.status()}`).toBeTruthy(); return (await response.json()).data;
  };
  const computer = (await api("/computers")).items[0];
  const services = (await api(`/computers/${computer.id}/agent-services`)).items;
  const service = services.find((item) => item.status === "ready");
  expect(service).toBeTruthy();
  project = await api("/projects", { computer_id: computer.id, name: "定时任务验收", description: "只配置未来计划，不调用真实模型。" });
  const other = await api("/projects", { computer_id: computer.id, name: "目标隔离项目" });
  agent = await api(`/computers/${computer.id}/agents`, { name: "定时巡检 Agent", agent_service_id: service.id, system_prompt: "界面验收专用。" });
  issue = await api(`/projects/${project.id}/issues`, { title: "固定巡检 Issue", status: "open" });
  closed = await api(`/projects/${project.id}/issues`, { title: "已关闭 Issue", status: "closed" });
  foreign = await api(`/projects/${other.id}/issues`, { title: "其他项目 Issue", status: "open" });
});
test.beforeEach(async ({ page }) => {
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: project.computer_id, projectId: project.id });
});

async function editorGeometry(page) {
  return page.getByRole("dialog").evaluate((dialog) => {
    const rect = (element) => {
      if (!element) throw new Error("Missing schedule editor border box");
      const { top, bottom, left, right, width, height } = element.getBoundingClientRect();
      return { top, bottom, left, right, width, height };
    };
    const body = dialog.querySelector(".am-modal__body");
    const layout = dialog.querySelector("[data-schedule-editor-layout]");
    const configuration = layout.querySelector("[data-schedule-editor-configuration]");
    const prompt = layout.querySelector("[data-schedule-editor-prompt]");
    const textarea = prompt.querySelector("textarea");
    const footer = dialog.querySelector("footer");
    body.scrollTop = 0;
    const result = {
      dialog: rect(dialog), header: rect(dialog.querySelector("header")), body: rect(body),
      layout: rect(layout), configuration: rect(configuration), prompt: rect(prompt),
      promptLabel: rect(prompt.querySelector("label")), textarea: rect(textarea), footer: rect(footer),
      controls: [...configuration.querySelectorAll('input:not([type="hidden"]), select')]
        .filter((element) => element.getBoundingClientRect().width && element.getBoundingClientRect().height)
        .map((element) => ({ name: element.getAttribute("aria-label") ?? element.tagName, ...rect(element) })),
      horizontalOverflow: [document.documentElement, dialog, body, layout, configuration, prompt, textarea]
        .map((element) => ({ name: element.tagName, scrollWidth: element.scrollWidth, clientWidth: element.clientWidth })),
      bodyScrollHeight: body.scrollHeight, bodyClientHeight: body.clientHeight
    };
    body.scrollTop = body.scrollHeight;
    result.bodyScrollTop = body.scrollTop;
    result.footerAfterBodyScroll = rect(footer);
    result.footerButtons = [...footer.querySelectorAll("button")].map((button) => {
      const bounds = rect(button);
      const hit = document.elementFromPoint(bounds.left + bounds.width / 2, bounds.top + bounds.height / 2);
      return { ...bounds, reachable: button === hit || button.contains(hit), disabled: button.disabled };
    });
    const bodyScrollTop = body.scrollTop;
    const textareaBeforeScroll = rect(textarea);
    textarea.scrollTop = textarea.scrollHeight;
    result.promptScroll = {
      scrollTop: textarea.scrollTop, scrollHeight: textarea.scrollHeight, clientHeight: textarea.clientHeight,
      bodyScrollTopBefore: bodyScrollTop, bodyScrollTopAfter: body.scrollTop,
      before: textareaBeforeScroll, after: rect(textarea)
    };
    return result;
  });
}

async function assertEditorGeometry(page, viewport, state) {
  const dialog = page.getByRole("dialog");
  const configuration = dialog.locator("[data-schedule-editor-configuration]");
  const prompt = dialog.locator("[data-schedule-editor-prompt]");
  await expect(configuration).toBeVisible();
  await expect(prompt.locator("textarea")).toHaveCount(1);
  await expect(prompt.locator("input, select, button")).toHaveCount(0);
  // Wait for the real preview before sampling: loading text and results have
  // different heights, and the natural-height Modal can recenter between them.
  await expect(configuration.locator("time").first()).toHaveAttribute("datetime", /.+/);
  const geometry = await editorGeometry(page);
  const { configuration: left, prompt: right, textarea, layout, body, footer } = geometry;
  const close = (actual, expected, message) => expect(Math.abs(actual - expected), `${state}: ${message}`).toBeLessThanOrEqual(1);
  if (viewport.width >= 960) {
    expect(right.left - left.right, `${state}: panes have a real horizontal gap`).toBeGreaterThanOrEqual(12);
    expect(right.left - left.right, `${state}: gap does not consume the prompt pane`).toBeLessThanOrEqual(48);
    close(left.top, right.top, "desktop panes start on the same row");
  } else {
    expect(right.top - left.bottom, `${state}: prompt follows all configuration content`).toBeGreaterThanOrEqual(12);
    close(left.left, right.left, "stacked panes share their left border");
    close(left.right, right.right, "stacked panes retain full usable width");
  }
  expect(textarea.height, `${state}: prompt is a substantial editor, not four rows`).toBeGreaterThanOrEqual(viewport.width >= 960 ? 300 : 200);
  expect(textarea.width, `${state}: prompt keeps usable writing width`).toBeGreaterThanOrEqual(200);
  expect(geometry.promptLabel.bottom, `${state}: label never covers prompt`).toBeLessThanOrEqual(textarea.top + 1);
  expect(textarea.left).toBeGreaterThanOrEqual(right.left - 1);
  expect(textarea.right).toBeLessThanOrEqual(right.right + 1);
  expect(textarea.top).toBeGreaterThanOrEqual(right.top - 1);
  expect(textarea.bottom).toBeLessThanOrEqual(right.bottom + 1);
  for (const pane of [left, right]) {
    expect(pane.left).toBeGreaterThanOrEqual(layout.left - 1);
    expect(pane.right).toBeLessThanOrEqual(layout.right + 1);
    expect(pane.top).toBeGreaterThanOrEqual(layout.top - 1);
    expect(pane.bottom).toBeLessThanOrEqual(layout.bottom + 1);
  }
  for (const control of geometry.controls) {
    expect(control.left, `${state}: ${control.name} stays inside configuration`).toBeGreaterThanOrEqual(left.left - 1);
    expect(control.right, `${state}: ${control.name} stays inside configuration`).toBeLessThanOrEqual(left.right + 1);
    expect(control.top).toBeGreaterThanOrEqual(left.top - 1);
    expect(control.bottom).toBeLessThanOrEqual(left.bottom + 1);
  }
  expect(layout.left).toBeGreaterThanOrEqual(body.left - 1);
  expect(layout.right).toBeLessThanOrEqual(body.right + 1);
  expect(geometry.dialog.left).toBeGreaterThanOrEqual(0);
  expect(geometry.dialog.right).toBeLessThanOrEqual(viewport.width);
  expect(geometry.dialog.top).toBeGreaterThanOrEqual(0);
  expect(geometry.dialog.bottom).toBeLessThanOrEqual(viewport.height);
  expect(body.top).toBeGreaterThanOrEqual(geometry.header.bottom - 1);
  expect(body.bottom).toBeLessThanOrEqual(footer.top + 1);
  expect(body.height, `${state}: shared Modal keeps a usable scrolling body`).toBeGreaterThan(100);
  if (geometry.bodyScrollHeight > geometry.bodyClientHeight + 1) expect(geometry.bodyScrollTop).toBeGreaterThan(0);
  for (const axis of ["top", "bottom", "height"]) close(geometry.footerAfterBodyScroll[axis], footer[axis], `fixed footer ${axis} while body scrolls`);
  expect(geometry.footerButtons).toHaveLength(2);
  for (const button of geometry.footerButtons) {
    expect(button.reachable, `${state}: footer action is not clipped or covered`).toBe(true);
    expect(button.disabled, `${state}: valid form retains usable footer actions`).toBe(false);
    expect(button.top).toBeGreaterThanOrEqual(footer.top - 1);
    expect(button.bottom).toBeLessThanOrEqual(Math.min(footer.bottom, viewport.height) + 1);
    expect(button.left).toBeGreaterThanOrEqual(footer.left - 1);
    expect(button.right).toBeLessThanOrEqual(footer.right + 1);
  }
  for (const element of geometry.horizontalOverflow) expect(element.scrollWidth, `${state}: ${element.name} has no horizontal overflow`).toBeLessThanOrEqual(element.clientWidth + 1);
  expect(geometry.promptScroll.scrollHeight).toBeGreaterThan(geometry.promptScroll.clientHeight);
  expect(geometry.promptScroll.scrollTop, `${state}: long prompt scrolls inside textarea`).toBeGreaterThan(0);
  close(geometry.promptScroll.bodyScrollTopBefore, geometry.promptScroll.bodyScrollTopAfter, "scrolling prompt does not move Modal body");
  for (const axis of ["top", "left", "width", "height"]) close(geometry.promptScroll.before[axis], geometry.promptScroll.after[axis], `prompt border box stays fixed on internal scroll: ${axis}`);
  return { state, ...geometry };
}

const editorViewports = [{ width: 1280, height: 820 }, { width: 1024, height: 820 }, { width: 800, height: 900 }, { width: 390, height: 844 }];
for (const viewport of editorViewports) test.describe(`schedule editor geometry ${viewport.width}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });
  test("create/edit panes retain border-box geometry across Issue modes and dynamic schedules", { tag: ["@schedules", "@layout"] }, async ({ page }, info) => {
    let runNowRequests = 0;
    await page.route(/\/v1\/scheduled-tasks\/[^/]+\/run-now(?:\?|$)/, async (route) => {
      runNowRequests++;
      await route.abort("blockedbyclient");
    });
    await page.goto("/schedules");
    await page.locator("main header").getByRole("button", { name: "新建定时任务", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const kind = dialog.getByLabel("调度类型", { exact: true });
    const mode = dialog.getByLabel("Issue 目标", { exact: true });
    const prompt = dialog.getByLabel("提示词", { exact: true });
    const longPrompt = Array.from({ length: 120 }, (_, index) => `第 ${index + 1} 项：检查待办、测试与风险，在 Issue 中给出可追溯的总结。`).join("\n") + "\n" + "LONG_UNBROKEN_PROMPT_".repeat(180);
    const evidence = [];
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await dialog.getByLabel("Agent", { exact: true }).selectOption(agent.id);
    await prompt.fill(longPrompt);
    await dialog.getByLabel("时区", { exact: true }).fill("UTC");
    await dialog.getByLabel("时区", { exact: true }).press("Tab");
    async function exerciseEditor(phase) {
      await expect(prompt).toHaveValue(longPrompt);
      for (const issueMode of ["new", "existing"]) {
        await mode.selectOption(issueMode);
        if (issueMode === "existing") await dialog.getByLabel("选择 Issue", { exact: true }).selectOption(issue.id);
        for (const scheduleKind of ["daily", "weekly", "once", "cron"]) {
          await kind.selectOption(scheduleKind);
          if (scheduleKind === "daily") {
            const times = dialog.locator('input[type="time"]');
            await times.first().fill("09:15");
            if (await times.count() === 1) await dialog.getByRole("button", { name: "添加时间", exact: true }).click();
            await times.nth(1).fill("17:45");
            await expect(times).toHaveCount(2);
          } else if (scheduleKind === "weekly") {
            const weekdays = dialog.getByRole("checkbox");
            await expect(weekdays).toHaveCount(7);
            for (let index = 0; index < 7; index++) await weekdays.nth(index).setChecked(index === 0 || index === 2);
            await expect(dialog.locator('input[type="time"]')).toHaveCount(2);
          } else if (scheduleKind === "once") {
            await dialog.locator('input[type="datetime-local"]').fill("2040-01-02T09:30");
          } else {
            await dialog.getByLabel("Cron 表达式", { exact: true }).fill("0 9 * * 1-5");
          }
          evidence.push(await assertEditorGeometry(page, viewport, `${phase}/${issueMode}/${scheduleKind}`));
        }
      }
      await page.screenshot({ path: info.outputPath(`${phase}-panes-${viewport.width}.png`), animations: "disabled" });
    }
    await exerciseEditor("create");
    // Daily/weekly/Cron variants above are drafts only. The single persisted
    // task cannot become due during QA; edit variants are cancelled, not saved.
    await mode.selectOption("new");
    await kind.selectOption("once");
    await dialog.locator('input[type="datetime-local"]').fill("2040-01-02T09:30");
    const created = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/v1/projects/${project.id}/scheduled-tasks`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).not.toHaveProperty("name");
    const task = (await response.json()).data;
    expect(task).toMatchObject({ name: "新定时任务", schedule_kind: "once", timezone: "UTC", issue_mode: "new", agent_id: agent.id });
    expect(new Date(task.schedule_expression).getUTCFullYear()).toBe(2040);
    await expect(dialog).toBeHidden();
    await page.locator(`[data-schedule-id="${task.id}"]`).getByRole("button", { name: "编辑", exact: true }).click();
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveValue(task.name);
    await exerciseEditor("edit");
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeHidden();
    expect(runNowRequests).toBe(0);
    await info.attach(`schedule-pane-border-boxes-${viewport.width}.json`, { body: JSON.stringify({ viewport, samples: evidence }, null, 2), contentType: "application/json" });
  });
});

for (const viewport of [{ width: 1280, height: 820 }, { width: 390, height: 844 }]) test.describe(`schedule editor ${viewport.width}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });
  test("project timer supports paired daily times, weekly weekdays and Issue modes", { tag: ["@schedules", "@smoke"] }, async ({ page }, info) => {
    await page.goto("/schedules");
    await expect(page.getByRole("heading", { name: "定时任务", exact: true })).toBeVisible();
    await page.locator("main header").getByRole("button", { name: "新建定时任务", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("定时任务名称", { exact: true })).toHaveCount(0);
    await dialog.getByLabel("Agent", { exact: true }).selectOption(agent.id);
    await dialog.getByLabel("提示词", { exact: true }).fill("检查项目的待办、测试与风险，并在 Issue 发布总结。");
    const kind = dialog.getByLabel("调度类型", { exact: true });
    await kind.selectOption("daily");
    const times = dialog.locator('input[type="time"]');
    // Keep both real recurring saves well outside the QA window, regardless of
    // the current UTC time. The run-now contract fixture below stays UI-only.
    const futureTimes = [12, 13].map((hours) => new Date(Date.now() + hours * 3_600_000).toISOString().slice(11, 16)).sort();
    await times.first().fill(futureTimes[0]);
    await dialog.getByRole("button", { name: "添加时间", exact: true }).click();
    await times.nth(1).fill(futureTimes[1]);
    await dialog.getByLabel("时区", { exact: true }).fill("UTC");
    await dialog.getByLabel("时区", { exact: true }).press("Tab");
    await expect(dialog.locator("time").first()).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`daily-editor-${viewport.width}.png`), animations: "disabled" });
    const created = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/v1/projects/${project.id}/scheduled-tasks`);
    await dialog.getByRole("button", { name: "创建", exact: true }).click();
    const response = await created; expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).not.toHaveProperty("name");
    const task = (await response.json()).data;
    expect(task).toMatchObject({ name: "新定时任务", schedule_kind: "daily", times: futureTimes, timezone: "UTC", issue_mode: "new", agent_id: agent.id });
    await expect(dialog).toBeHidden();
    const row = page.locator(`[data-schedule-id="${task.id}"]`);
    await expect(row).toContainText(futureTimes[0]); await expect(row).toContainText(futureTimes[1]);
    await row.getByRole("button", { name: "编辑", exact: true }).click();
    await dialog.getByLabel("定时任务名称", { exact: true }).fill(`每日巡检-${viewport.width}-手动改名`);
    await dialog.getByLabel("Issue 目标", { exact: true }).selectOption("existing");
    const target = dialog.getByLabel("选择 Issue", { exact: true });
    await target.selectOption(issue.id);
    await expect(target.locator(`option[value="${closed.id}"]`)).toHaveCount(0);
    await expect(target.locator(`option[value="${foreign.id}"]`)).toHaveCount(0);
    await kind.selectOption("weekly");
    const weekdays = dialog.getByRole("checkbox");
    await expect(weekdays).toHaveCount(7);
    // Monday is the first day in the zh-CN editor; choose Monday/Wednesday only.
    for (let index = 0; index < 7; index++) await weekdays.nth(index).setChecked(index === 0 || index === 2);
    const patched = page.waitForResponse((r) => r.request().method() === "PATCH" && new URL(r.url()).pathname === `/v1/scheduled-tasks/${task.id}`);
    await dialog.getByRole("button", { name: "保存", exact: true }).click();
    const updated = await patched; expect(updated.ok()).toBeTruthy();
    expect((await updated.json()).data).toMatchObject({ name: `每日巡检-${viewport.width}-手动改名`, schedule_kind: "weekly", weekdays: [1, 3], times: futureTimes, issue_mode: "existing", issue_id: issue.id });
    await expect(dialog).toBeHidden();
    await page.reload();
    await row.getByRole("button", { name: "编辑", exact: true }).click();
    await expect(target).toHaveValue(issue.id);
    await expect(kind).toHaveValue("weekly");
    await expect(times.nth(1)).toHaveValue(futureTimes[1]);
    await page.screenshot({ path: info.outputPath(`weekly-existing-${viewport.width}.png`), animations: "disabled" });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    let key = "";
    await page.route(`**/v1/scheduled-tasks/${task.id}/run-now`, async (route) => {
      key = route.request().headers()["idempotency-key"] ?? "";
      await route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ data: { id: "ui-trigger", scheduled_task_id: task.id, status: "running", issue_id: issue.id } }) });
    });
    await row.getByRole("button", { name: "立即运行", exact: true }).click();
    await expect.poll(() => key).not.toBe("");
  });
});
