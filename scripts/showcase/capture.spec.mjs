import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";

test("capture localized real Teamva UI with demo data; never invoke a model", async ({ page }, testInfo) => {
  const { demo } = testInfo.project.metadata;
  const text = (en, zh) => demo.locale === "zh" ? zh : en;
  const locale = text("en-US", "zh-CN");
  const strings = JSON.parse(readFileSync(new URL(`../../web/src/locales/${locale}.json`, import.meta.url), "utf8"));
  const ui = (key) => key.split(".").reduce((value, part) => value[part], strings);
  const directory = new URL(`../../docs/screenshots/${demo.locale === "zh" ? "zh/" : ""}`, import.meta.url);
  await mkdir(directory, { recursive: true });
  const names = text(["Product Manager", "Frontend Developer", "Backend Developer", "QA Engineer"], ["产品经理", "前端开发", "后端开发", "测试工程师"]);
  const obsoleteNames = /\b(?:Scout|Builder|Sentinel|Verifier)\b|规划师|构建师|守护师|验证师/;
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript((d) => sessionStorage.setItem("teamva-selection", JSON.stringify({ computerId: d.computer, projectId: d.project })), demo);
  const forbidden = [];
  await page.route("**/v1/**", async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (!["GET", "HEAD"].includes(request.method()) && !["/v1/auth/local-session", "/v1/work-records/navigation"].includes(path)) {
      forbidden.push(path); await route.abort();
    } else await route.continue();
  });
  const capture = async (name) => {
    await page.evaluate(() => document.fonts.ready);
    await expect(page.locator("html")).toHaveAttribute("lang", locale);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.locator("header").first()).not.toContainText(/Claude Code|Codex|OpenCode/);
    expect(await page.locator("header").first().evaluate((header) => {
      // The nonmodal session sheet may cover the header; inspect its own layout.
      const search = header.children[1], edge = search.getBoundingClientRect().right;
      return [...header.children].every((child) => getComputedStyle(child).display === "none" || child.getBoundingClientRect().right <= edge);
    })).toBe(true);
    const brand = page.locator('header a[href="/workbench"] img');
    await expect(brand).toBeVisible();
    await expect.poll(() => brand.evaluate((image) => image.complete && image.naturalWidth)).toBe(256);
    const brandResponse = await page.request.get(await brand.getAttribute("src"));
    expect(brandResponse.ok()).toBeTruthy();
    expect(await brandResponse.body()).toEqual(readFileSync(new URL("../../assets/brand/icon.png", import.meta.url)));
    expect(await page.locator("body").innerText()).not.toMatch(obsoleteNames);
    const png = await page.screenshot({ path: fileURLToPath(new URL(`${name}.png`, directory)), animations: "disabled" });
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([3200, 2200]);
  };
  await page.goto(`/issues?project=${demo.project}&issue=${demo.issue}`);
  await expect(page.locator("[data-work-item-key]")).toHaveCount(4);
  await expect(page.getByText(text("Demo data.", "演示数据。"), { exact: true }).first()).toBeVisible();
  const preferences = await page.request.get("/v1/me/preferences");
  expect(preferences.ok()).toBeTruthy();
  expect((await preferences.json()).data.locale).toBe(locale);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const firstComment = page.locator(`[data-comment-id="${demo.publicComments[0].id}"]`);
  await firstComment.evaluate((element) => element.scrollIntoView({ block: "start" }));
  let previousBottom = 0;
  for (const comment of demo.publicComments) {
    const card = page.locator(`[data-comment-id="${comment.id}"]`);
    await expect(card).toHaveCSS("background-color", "rgb(255, 255, 255)");
    await expect(card.locator(":scope > span")).toHaveCSS("background-color", "rgb(244, 244, 245)");
    await expect(card.locator("header strong")).toHaveText(ui("issues.agentSessionLabel").replace("{{agent}}", comment.author).replace("{{index}}", "1"));
    for (const target of comment.targets) await expect(card).toContainText(`@${target}`);
    const geometry = await card.evaluate((element) => {
      let clip = element.parentElement;
      while (clip && !["auto", "scroll"].includes(getComputedStyle(clip).overflowY)) clip = clip.parentElement;
      const box = element.getBoundingClientRect(), viewport = clip.getBoundingClientRect();
      return { top: box.top, bottom: box.bottom, clipTop: viewport.top, clipBottom: viewport.bottom };
    });
    expect(geometry.top).toBeGreaterThanOrEqual(Math.max(previousBottom, geometry.clipTop) - 1);
    expect(geometry.bottom).toBeLessThanOrEqual(geometry.clipBottom + 1);
    previousBottom = geometry.bottom;
  }
  await expect(page.getByText(text("Deep analysis · Scope", "深度分析·需求范围"), { exact: true })).toBeVisible();
  await expect(page.getByText(text("Efficient execution · UI", "高效执行·前端实现"), { exact: true })).toBeVisible();
  await capture("multitask");
  for (const [index, name] of names.entries()) {
    await page.locator(`[data-comment-id="${demo.publicComments[index].id}"]`).getByRole("button", { name: ui("issues.openAgentSession"), exact: true }).click();
    const panel = page.getByRole("dialog", { name: ui("issues.agentSessionLabel").replace("{{agent}}", name).replace("{{index}}", "1"), exact: true });
    await expect(panel).toBeVisible();
    await expect(panel.getByRole("tab", { name: text("Activity", "活动"), exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(panel.getByText(text("Demo data.", "演示数据。"), { exact: true }).first()).toBeVisible();
    if (index === 1) {
      await expect(panel.getByText(text("Keep this keyboard-first. Move advanced options to Review, not the first step.", "优先支持键盘操作。高级选项移到确认步骤，不要放在第一步。"), { exact: true })).toBeVisible();
      await expect(panel.getByText(text("Direction updated", "方向已调整"), { exact: true })).toBeVisible();
      const historyOrder = await panel.evaluate((element, titles) => {
        const body = element.textContent;
        return body.indexOf(titles[0]) < body.indexOf(titles[1]);
      }, text(["Frontend plan", "Direction updated"], ["前端方案", "方向已调整"]));
      expect(historyOrder).toBeTruthy();
      const composer = panel.getByRole("textbox", { name: ui("issues.sessionInput"), exact: true });
      await composer.fill(text("Before handing off to QA Engineer, add a clear retry path for API validation errors.", "交给测试工程师前，请为接口校验错误补充清晰的重试路径。"));
      await expect(panel.getByRole("button", { name: ui("issues.send"), exact: true })).toBeEnabled();
      const resizer = panel.getByRole("button", { name: ui("issues.resizeSession"), exact: true });
      await resizer.press("Home");
      const sheet = await panel.locator(".session-sheet-panel").boundingBox();
      expect(sheet.width).toBeCloseTo(620, 3);
      for (const title of text(["Frontend plan", "Direction updated"], ["前端方案", "方向已调整"])) {
        await expect(panel.getByText(title, { exact: true })).toBeInViewport({ ratio: 1 });
      }
      await expect(composer).toBeInViewport({ ratio: 1 });
      await expect(page.getByText(text("Atlas onboarding · One task, four agents", "Atlas 新手引导：一个任务，四位协作者"), { exact: true }).first()).toBeInViewport();
      await composer.focus();
      await capture("agent-conversation");
    }
    await panel.getByRole("button", { name: ui("common.close"), exact: true }).last().click();
  }
  await page.goto(`/conversations?project=${demo.project}&graph=${demo.graph}`);
  await expect(page.locator("[data-node-id]")).toHaveCount(3);
  await page.getByRole("button", { name: ui("conversations.resetView"), exact: true }).click();
  await expect(page.getByText(text("01 · Shared starting point", "01：共同起点"), { exact: true })).toBeVisible();
  await expect(page.getByText(text("02 · Guided setup", "02：分步引导"), { exact: true })).toBeVisible();
  await expect(page.getByText(text("03 · Command center", "03：操作中心"), { exact: true })).toBeVisible();
  await capture("graph");
  for (const [path, name, expectedText] of [
    ["resources", "resources", "atlas-design-system"],
    [`agents?agent=${demo.agents[1]}`, "agents", text("Efficient execution · accessible interfaces and draft-preserving error states", "高效执行：实现无障碍界面与保留草稿的错误反馈")],
    ["skills", "skills", ui("agents.skillStore")],
    ["skills?tab=mcp", "mcp", "atlas-project-tools"],
    ["memories", "memories", text("One product. Three repositories.", "一个产品，三个代码仓库。")]
  ]) {
    await page.goto("/" + path);
    await expect(page.getByText(expectedText, { exact: true }).filter({ visible: true }).first()).toBeVisible();
    if (name === "resources") await expect(page.locator("table").getByText(ui("status.ready"), { exact: true })).toHaveCount(7);
    if (name === "agents") {
      const response = await page.request.get(`/v1/agents/${demo.agents[1]}`);
      expect(response.ok()).toBeTruthy();
      const agent = (await response.json()).data;
      await expect(page.getByRole("textbox", { name: ui("agents.name"), exact: true })).toHaveValue(names[1]);
      await expect(page.getByRole("textbox", { name: ui("agents.systemPrompt"), exact: true })).not.toHaveValue("");
      await expect(page.getByRole("combobox", { name: ui("agents.software"), exact: true })).toHaveValue(agent.agent_service_id);
      await expect(page.getByRole("combobox", { name: ui("agents.model"), exact: true })).toHaveValue(agent.model);
      await expect(page.locator(".am-model-picker")).toHaveAttribute("data-has-models", "true");
      await expect(page.getByText(text("Built-in assistant for project work and ideas.", "内置助手，协助项目工作与灵感整理。"), { exact: true })).toBeVisible();
    }
    await capture(name);
  }
  for (const [index, id] of demo.agents.entries()) {
    const response = await page.request.get(`/v1/agents/${id}`);
    expect(response.ok()).toBeTruthy();
    const agent = (await response.json()).data;
    expect(agent.name).toBe(names[index]);
    expect(JSON.stringify(agent)).not.toMatch(obsoleteNames);
    const revision = await page.request.get(`/v1/agent-revisions/${agent.current_revision_id}`);
    expect(revision.ok()).toBeTruthy();
    const { revision: config } = (await revision.json()).data;
    expect(config.system_prompt).toContain(names[index]);
    expect(JSON.stringify(config)).not.toMatch(obsoleteNames);
  }
  expect(pageErrors).toEqual([]);
  expect(forbidden).toEqual([]);
});
