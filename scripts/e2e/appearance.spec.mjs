import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";

// These are real local HTTP/Chrome appearance checks. They never send a Run or
// test a vendor connection. Screenshot data belongs only to the isolated dir.
const palettes = [
  ["default", "默认主题"],
  ["hallmark", "奶油炭黑"], ["chamber", "纸白极简"], ["flexoki", "复古纸墨"],
  ["ocean", "雾海蓝"], ["forest", "鼠尾草绿"], ["iris", "鸢尾紫"]
];
let project, agent, graph;
test.beforeAll(async ({ request, baseURL }, info) => {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const session = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(session.ok()).toBeTruthy();
  headers["X-CSRF-Token"] = (await session.json()).data.csrf_token;
  const call = async (path, data) => {
    const response = await request.fetch(`/v1${path}`, { headers, method: data === undefined ? "GET" : "POST", data });
    expect(response.ok()).toBeTruthy(); return (await response.json()).data;
  };
  const computer = (await call("/computers")).items[0];
  project = await call("/projects", { computer_id: computer.id, name: "设计工作室", description: "把灵感变为清晰、可靠的协作。" });
  // Local startup deliberately does not wait for CLI probes. A real configured
  // Agent is a fixture precondition, not an optional step or a reason to hide the
  // first-run guide. Poll the actual catalog; do not fake readiness or dismiss it.
  const probeStarted = Date.now();
  let ready, initialStatuses;
  await expect.poll(async () => {
    const services = (await call(`/computers/${computer.id}/agent-services`)).items;
    initialStatuses ??= services.map(({ status }) => status);
    ready = services.find((service) => service.status === "ready");
    return Boolean(ready);
  }, { message: "外观 fixture 需要真实 CLI 探测收敛为 ready（不执行模型）", timeout: 30_000, intervals: [100, 250, 500, 1_000] }).toBe(true);
  agent = await call(`/computers/${computer.id}/agents`, { name: "产品设计师", agent_service_id: ready.id, system_prompt: "仅用于主题界面展示，不执行模型。" });
  expect(agent.agent_service_id).toBe(ready.id);
  expect(agent.current_revision_id).toBeTruthy();
  graph = await call(`/projects/${project.id}/conversation-graphs`, { title: "明暗文字验收", agent_id: agent.id, node_title: "对话文字" });
  const onboarding = await call("/system/onboarding");
  expect(onboarding.needed, "真实用户 Agent 创建后不应再判定为首次空状态").toBe(false);
  const readinessEvidence = info.outputPath("appearance-fixture-readiness.json");
  await writeFile(readinessEvidence, JSON.stringify({ initialStatuses, readyStatus: ready.status, probeWaitMilliseconds: Date.now() - probeStarted, configuredAgent: Boolean(agent.current_revision_id), onboardingNeeded: onboarding.needed }, null, 2));
  await info.attach("appearance-fixture-readiness.json", { path: readinessEvidence, contentType: "application/json" });
  for (const [title, status] of [["整理新版本的设计方向", "open"], ["检查组件与排版的一致性", "open"], ["补齐产品说明与验收记录", "closed"]]) {
    await call(`/projects/${project.id}/issues`, { title, status, description: "保持清晰的层次，让重要内容更容易被看见。" });
  }
  await call(`/projects/${project.id}/memories`, { key: "design-principles", kind: "fact", title: "设计原则", content: "清晰、安静、易读；让信息本身成为主角。", source_type: "user" });
  await call(`/projects/${project.id}/resources`, { kind: "url", name: "设计参考文档", url: "https://example.test/design" });
});
test.beforeEach(async ({ page }) => {
  await page.addInitScript((value) => sessionStorage.setItem("teamva-selection", JSON.stringify(value)), { computerId: project.computer_id, projectId: project.id });
});

async function save(page) {
  const theme = await page.getByLabel("主题", { exact: true }).inputValue();
  const palette = await page.locator('[data-palette-option][aria-checked="true"]').getAttribute("data-palette-option");
  await expect.poll(async () => {
    const response = await page.request.get("/v1/me/preferences");
    const value = (await response.json()).data;
    return { theme: value.theme, palette: value.appearance.palette };
  }).toEqual({ theme, palette });
  await expect(page.locator("[data-preferences-autosave]")).toContainText("已生效");
  await expect(page.getByRole("button", { name: "保存设置", exact: true })).toHaveCount(0);
}
async function readable(page) {
  const geometry = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth > innerWidth, canvas: getComputedStyle(document.documentElement).getPropertyValue("--color-canvas").trim(), accent: getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim(), control: getComputedStyle(document.documentElement).getPropertyValue("--color-control").trim() }));
  expect(geometry.overflow).toBe(false);
  expect(geometry.canvas).toMatch(/^#[\da-f]{6}$/i);
  expect(geometry.accent).toMatch(/^#[\da-f]{6}$/i);
  expect(geometry.control).toMatch(/^#[\da-f]{6}$/i);
}

for (const [id, label] of palettes) for (const mode of ["light", "dark"]) {
  test(`appearance ${id} ${mode}`, { tag: "@appearance" }, async ({ page }, info) => {
    const errors = []; page.on("pageerror", (e) => errors.push(e.message));
    await page.goto("/settings");
    await expect(page.locator('[data-palette-option="project"]')).toHaveCount(0);
    await expect(page.locator('[data-palette-option]')).toHaveCount(7);
    await page.getByLabel("主题", { exact: true }).selectOption(mode);
    await page.locator(`[data-palette-option="${id}"]`).click();
    await expect(page.locator("html")).toHaveAttribute("data-palette", id);
    await save(page);
    await page.goto("/workbench");
    await expect(page.getByRole("heading", { name: project.name, exact: true })).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-palette", id);
    await expect(page.locator("html")).toHaveAttribute("data-theme", mode);
    if (id === "default") {
      const colors = await page.evaluate(() => {
        const style = getComputedStyle(document.documentElement);
        return { canvas: style.getPropertyValue("--color-canvas").trim(), accent: style.getPropertyValue("--color-accent").trim() };
      });
      expect(colors).toEqual(mode === "light" ? { canvas: "#fbfbfb", accent: "#52763a" } : { canvas: "#111114", accent: "#c2ff75" });
    }
    await readable(page);
    await page.screenshot({ path: info.outputPath(`${id}-${mode}.png`), animations: "disabled" });
    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-palette", id);
    await expect(page.locator("html")).toHaveAttribute("data-theme", mode);
    expect(errors).toEqual([]);
    info.annotations.push({ type: "palette", description: `${label} · ${mode}` });
  });
}

test("appearance applies immediately and persists across navigation and custom colors", { tag: "@appearance" }, async ({ page }, info) => {
  await page.goto("/settings");
  await page.getByLabel("主题", { exact: true }).selectOption("light");
  await page.locator('[data-palette-option="ocean"]').click();
  await save(page);
  await page.locator('[data-palette-option="iris"]').click();
  await expect(page.locator("html")).toHaveAttribute("data-palette", "iris");
  await page.getByRole("link", { name: "概览", exact: true }).first().click();
  await expect(page.locator("html")).toHaveAttribute("data-palette", "iris");
  await page.goto("/settings");
  await page.getByLabel("强调色", { exact: true }).evaluate((e) => { e.value = "#a94b35"; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true })); });
  await page.getByLabel("背景色", { exact: true }).evaluate((e) => { e.value = "#f5f0e8"; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true })); });
  await page.getByLabel("控件色", { exact: true }).evaluate((e) => { e.value = "#254f96"; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true })); });
  await save(page);
  await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.appearance)
    .toEqual({ palette: "iris", accent: "#a94b35", background: "#f5f0e8", control: "#254f96" });
  await page.reload();
  await expect(page.getByLabel("强调色", { exact: true })).toHaveValue("#a94b35");
  await expect(page.getByLabel("背景色", { exact: true })).toHaveValue("#f5f0e8");
  await expect(page.getByLabel("控件色", { exact: true })).toHaveValue("#254f96");
  await readable(page);
  await page.screenshot({ path: info.outputPath("custom-settings.png"), animations: "disabled" });
  // Leave the isolated test user's default orderly for any subsequent suite.
  await page.locator('[data-palette-option="default"]').click();
  await save(page);
});

for (const [id] of palettes) test(`appearance ${id} independent controls and reset`, { tag: "@appearance" }, async ({ page }) => {
  await page.goto("/settings");
  await page.getByLabel("主题", { exact: true }).selectOption("light");
  await page.locator(`[data-palette-option="${id}"]`).click();
  await save(page);
  const before = await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement);
    return { canvas: style.getPropertyValue("--color-canvas"), accent: style.getPropertyValue("--color-accent") };
  });
  await page.getByLabel("控件色", { exact: true }).evaluate((e) => { e.value = "#254f96"; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true })); });
  await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.appearance.control).toBe("#254f96");
  await page.reload();
  await expect(page.getByLabel("控件色", { exact: true })).toHaveValue("#254f96");
  const after = await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement), input = document.querySelector('input[type="color"]');
    const toRGB = (color) => { const el = document.createElement("span"); el.style.color = color; document.body.append(el); const value = getComputedStyle(el).color; el.remove(); return value; };
    return { canvas: style.getPropertyValue("--color-canvas"), accent: style.getPropertyValue("--color-accent"),
      inputBackground: getComputedStyle(input).backgroundColor, expectedInput: toRGB(style.getPropertyValue("--color-control-subtle")) };
  });
  expect(after.canvas).toBe(before.canvas); expect(after.accent).toBe(before.accent);
  expect(after.inputBackground).toBe(after.expectedInput);
  const boxes = await page.locator('[data-field-group="appearance-colors"] input[type="color"]').evaluateAll((inputs) => inputs.map((input) => { const r = input.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, height: r.height }; }));
  expect(boxes).toHaveLength(3);
  expect(Math.max(...boxes.map((b) => b.top)) - Math.min(...boxes.map((b) => b.top))).toBeLessThanOrEqual(1);
  expect(Math.max(...boxes.map((b) => b.bottom)) - Math.min(...boxes.map((b) => b.bottom))).toBeLessThanOrEqual(1);
  await page.goto("/resources");
  await page.getByRole("button", { name: "添加资源", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator('.am-button[data-variant="primary"]')).toHaveCSS("background-color", "rgb(37, 79, 150)");
  await expect(dialog.locator('.am-button[data-variant="primary"]')).toHaveCSS("color", "rgb(255, 255, 255)");
  await page.keyboard.press("Escape");
  await page.goto("/settings");
  await page.getByRole("button", { name: "恢复默认颜色", exact: true }).click();
  await expect.poll(async () => (await (await page.request.get("/v1/me/preferences")).json()).data.appearance).toEqual({ palette: id });
});

test.describe("touch appearance settings", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });
  test("palette cards and controls remain usable on mobile", { tag: ["@appearance", "@mobile"] }, async ({ page }, info) => {
    await page.goto("/settings");
    await page.locator('[data-palette-option="forest"]').click();
    await expect(page.locator("html")).toHaveAttribute("data-palette", "forest");
    await readable(page);
    await page.locator('[data-palette-option="forest"]').scrollIntoViewIfNeeded();
    await page.screenshot({ path: info.outputPath("forest-mobile.png"), animations: "disabled" });
  });
});

for (const mode of ["light", "dark"]) test(`appearance ${mode} inherited text and dark-surface Markdown`, { tag: "@appearance" }, async ({ page }, info) => {
  await page.goto("/settings");
  await page.getByLabel("主题", { exact: true }).selectOption(mode);
  await page.locator('[data-palette-option="default"]').click();
  await save(page);
  const text = mode === "dark" ? "rgb(250, 250, 250)" : "rgb(9, 9, 11)";
  for (const [path, selector] of [
    ["/workbench", "main strong"], ["/issues", "main strong"], ["/conversations", "main h1"],
    ["/resources", "main tbody strong"], ["/memories", "main h1"], ["/agents", "main h1"],
    ["/skills", "main h1"], ["/settings", "main h1"], ["/work-records", "main h1"], ["/inspirations", "main h1"]
  ]) {
    await page.goto(path);
    await expect(page.locator("body")).toHaveCSS("color", text);
    await expect(page.locator(selector).first()).toHaveCSS("color", text);
  }
  // Only this graph's GET history is a UI fixture; no message or Run is sent.
  const source = { ...graph.nodes[0], width: 620, height: 650,
    inherited_messages: [{ author: "user", body: "继承的 **粗体** 与 `inline`" }], message_count: 1 };
  const body = "**普通粗体** 与 `inline`\n\n> 引用正文\n\n| 表头 |\n| --- |\n| 正文 |\n\n```text\ncode block\n```";
  await page.route(`**/v1/conversation-graphs/${graph.graph.id}`, (route) => route.fulfill({
    status: 200, contentType: "application/json", body: JSON.stringify({ data: {
      graph: graph.graph, nodes: [source], goals: [], messages: [{ id: "theme-user-fixture", node_id: source.id,
        author_kind: "user", body, status: "published", version: 1, created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z" }]
    } })
  }));
  await page.goto(`/conversations?graph=${graph.graph.id}`);
  const card = page.locator(`[data-node-id="${source.id}"]`), bubble = card.locator('div.user-message[class~="bg-ink"]');
  const inverse = mode === "dark" ? text : "rgb(251, 251, 251)";
  await expect(bubble).toHaveCSS("background-color", mode === "dark" ? "rgb(39, 39, 42)" : "rgb(11, 11, 12)");
  await expect(bubble).toHaveCSS("color", inverse);
  for (const selector of ["strong", "p code", "blockquote", "th"]) await expect(bubble.locator(selector)).toHaveCSS("color", inverse);
  await expect(bubble.locator("th")).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
  await expect(bubble.locator("pre")).toHaveCSS("color", "rgb(255, 255, 255)");
  const unreadable = await card.evaluate((root) => {
    const canvas = document.createElement("canvas"); canvas.width = canvas.height = 1;
    const ctx = canvas.getContext("2d");
    const rgba = (value) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = value; ctx.fillRect(0, 0, 1, 1); return [...ctx.getImageData(0, 0, 1, 1).data]; };
    const luminance = (color) => color.slice(0, 3).reduce((sum, c, i) => {
      c /= 255; return sum + (c <= .04045 ? c / 12.92 : ((c + .055) / 1.055) ** 2.4) * [.2126, .7152, .0722][i];
    }, 0);
    return [...root.querySelectorAll("div.user-message")].flatMap((bubble) => [...bubble.querySelectorAll("p, strong, code, blockquote, th, td")]).flatMap((element) => {
      const ancestors = []; for (let e = element; e; e = e.parentElement) ancestors.unshift(e);
      let background = [255, 255, 255];
      for (const e of ancestors) { const color = rgba(getComputedStyle(e).backgroundColor), alpha = color[3] / 255;
        background = background.map((c, i) => color[i] * alpha + c * (1 - alpha)); }
      const foreground = rgba(getComputedStyle(element).color), a = luminance(foreground), b = luminance(background);
      const ratio = (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
      return ratio < 4.5 ? [{ tag: element.tagName, text: element.textContent, ratio }] : [];
    });
  });
  expect(unreadable, "对比度必须按实际合成底色计算，不能仅检查 token 或前景色").toEqual([]);
  const inherited = card.locator('[class~="bg-ink/70"]');
  if (mode === "dark") await expect(inherited).toHaveCSS("background-color", "rgb(39, 39, 42)");
  await expect(inherited.locator("strong")).toHaveCSS("color", inverse);
  await expect(inherited.locator("code")).toHaveCSS("color", inverse);
  await page.screenshot({ path: info.outputPath(`readable-dialogue-${mode}.png`), animations: "disabled" });
  if (mode === "light") {
    // A dark custom canvas must not regress to white bubbles just because the preference says light.
    await page.goto("/settings");
    await page.getByLabel("背景色", { exact: true }).evaluate((e) => {
      e.value = "#111114"; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await save(page);
    await page.goto(`/conversations?graph=${graph.graph.id}`);
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    expect(await bubble.evaluate((element) => {
      const rgb = (value) => { const probe = document.createElement("span"); probe.style.color = value; document.body.append(probe);
        const color = getComputedStyle(probe).color; probe.remove(); return color; };
      const tokens = getComputedStyle(document.documentElement), style = getComputedStyle(element);
      return { background: style.backgroundColor === rgb(tokens.getPropertyValue("--color-surface-hover")),
        foreground: style.color === rgb(tokens.getPropertyValue("--color-text")), markdown: getComputedStyle(element.querySelector("strong")).color === style.color };
    })).toEqual({ background: true, foreground: true, markdown: true });
    await page.goto("/settings");
    await page.getByRole("button", { name: "恢复默认颜色", exact: true }).click();
    await save(page);
  }
});

for (const mobile of [false, true]) test.describe(`Silken controls ${mobile ? "touch" : "desktop"}`, () => {
  test.use({ viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 }, hasTouch: mobile });
  for (const mode of ["light", "dark"]) test(`default ${mode} themed input states`, { tag: "@appearance" }, async ({ page }, info) => {
    await page.goto("/settings");
    await page.getByLabel("主题", { exact: true }).selectOption(mode);
    await page.locator('[data-palette-option="default"]').click();
    await save(page);
    await page.goto(`/agents?agent=${agent.id}`);
    const group = page.locator('[data-field-group="agent-configuration"]');
    const name = group.locator("input").first();
    await expect(name).toHaveValue(agent.name);
    const controls = [name, group.locator("input").nth(1), group.locator("select"), group.locator(".am-model-picker input"), page.locator("textarea.am-textarea")];
    const assertSurface = async (control) => {
      expect(await control.evaluate((element) => {
        const target = element.closest(".am-select") ?? element;
        const style = getComputedStyle(target), root = getComputedStyle(document.documentElement);
        const rgb = (value) => { const p = document.createElement("span"); p.style.color = value; document.body.append(p); const color = getComputedStyle(p).color; p.remove(); return color; };
        return {
          background: style.backgroundColor === rgb(root.getPropertyValue("--color-control-subtle")),
          foreground: getComputedStyle(element).color === rgb(root.getPropertyValue("--color-text")),
          appearance: getComputedStyle(element).appearance,
          scheme: getComputedStyle(element).colorScheme,
          shadow: style.boxShadow, outline: style.outlineStyle
        };
      })).toEqual({ background: true, foreground: true, appearance: "none", scheme: mode, shadow: "none", outline: "none" });
    };
    for (const control of controls) {
      await assertSurface(control);
      await control.focus();
      await assertSurface(control);
    }
    await name.fill("产品设计师 · 品牌绿输入预览");
    await assertSurface(name);
    for (const attribute of ["readonly", "disabled", "aria-invalid"]) {
      await name.evaluate((e, key) => e.setAttribute(key, "true"), attribute);
      await assertSurface(name);
      await name.evaluate((e, key) => e.removeAttribute(key), attribute);
    }
    // Force Chromium's real autofill pseudo-state, never read saved profile data.
    const cdp = await page.context().newCDPSession(page);
    const { root } = await cdp.send("DOM.getDocument");
    const { nodeId } = await cdp.send("DOM.querySelector", { nodeId: root.nodeId, selector: '[data-field-group="agent-configuration"] input' });
    try {
      await cdp.send("CSS.enable");
      await cdp.send("CSS.forcePseudoState", { nodeId, forcedPseudoClasses: ["autofill"] });
      expect(await name.evaluate((e) => {
        const probe = document.createElement("span"); probe.style.color = "var(--color-text)"; document.body.append(probe);
        const text = getComputedStyle(probe).color; probe.remove();
        const s = getComputedStyle(e);
        return { autofill: e.matches(":autofill"), clip: s.backgroundClip, themedText: s.webkitTextFillColor === text, shadow: s.boxShadow };
      })).toEqual({ autofill: true, clip: "text", themedText: true, shadow: "none" });
    } finally {
      await cdp.send("CSS.forcePseudoState", { nodeId, forcedPseudoClasses: [] });
      await cdp.detach();
    }
    await name.focus();
    await readable(page);
    await page.screenshot({ path: info.outputPath(`silken-controls-${mode}-${mobile ? "touch" : "desktop"}.png`), animations: "disabled" });
    // This remains an unsaved draft: do not create an Agent revision or a Run.
  });
});
