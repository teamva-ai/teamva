import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// 真实 Chrome + 隔离 HTTP 服务；不执行模型、不触发任务、不记录 Cookie/CSRF。
// fixture 仅补足原生 type/ARIA，不代替真实 Svelte Input/Select/编辑器验收。
test.use({ trace: "off" });
let project, agent, note;
const palettes = ["default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris"];
const reset = { outlineStyle: "none", outlineWidth: "0px", outlineOffset: "0px", boxShadow: "none" };

async function authenticatedAPI(request, baseURL) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const session = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(session.ok()).toBeTruthy();
  headers["X-CSRF-Token"] = (await session.json()).data.csrf_token;
  return async (path, data, method = data === undefined ? "GET" : "POST") => {
    const response = await request.fetch(`/v1${path}`, {
      method, data, headers: { ...headers, ...(method === "GET" ? {} : { "Idempotency-Key": randomUUID() }) }
    });
    expect(response.ok(), `${method} ${path}: HTTP ${response.status()}`).toBeTruthy();
    return (await response.json()).data;
  };
}

test.beforeAll(async ({ request, baseURL }) => {
  const api = await authenticatedAPI(request, baseURL);
  const computer = (await api("/computers")).items[0];
  expect(computer).toBeTruthy();
  const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
  expect(service, "需要 ready CLI 展示真实 Agent 表单，但绝不调用模型").toBeTruthy();
  project = await api("/projects", { computer_id: computer.id, name: "输入焦点验收", description: "仅界面验收，不执行 Run。" });
  agent = await api(`/computers/${computer.id}/agents`, {
    name: "输入焦点 Agent", agent_service_id: service.id, system_prompt: "仅用于真实输入控件验收，不调用模型。"
  });
  note = await api("/inspirations", {
    project_id: project.id, title: "输入焦点笔记", markdown: "真实富文本输入。\n\n```javascript\nconst focus = true;\n```\n"
  });
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), {
    computerId: project.computer_id, projectId: project.id
  });
  // 安全保险：即便未来页面行为变化，也不允许这套验收发送真实执行请求。
  await page.route(/\/v1\/.*(?:\/runs|\/run-now|\/mentions)(?:\?|$)/, async (route) => {
    if (route.request().method() === "POST") {
      await route.abort("blockedbyclient");
      throw new Error("输入焦点验收禁止创建 Run/mention 或 run-now");
    }
    await route.continue();
  });
});

async function noRing(locator, label = "input target") {
  await expect(locator, label).toHaveCount(1);
  await expect.poll(() => locator.evaluate((element) => {
    const style = getComputedStyle(element);
    return Object.fromEntries(["outlineStyle", "outlineWidth", "outlineOffset", "boxShadow"].map((key) => [key, style[key]]));
  }), { message: label }).toEqual(reset);
}

async function inputFamily(control) {
  await noRing(control);
  const ancestors = control.locator("xpath=ancestor::*[contains(concat(' ', normalize-space(@class), ' '), ' am-select ') or contains(concat(' ', normalize-space(@class), ' '), ' am-model-picker ') or contains(concat(' ', normalize-space(@class), ' '), ' am-field__control ') or contains(concat(' ', normalize-space(@class), ' '), ' am-switch ')]");
  for (const wrapper of await ancestors.all()) {
    expect(await wrapper.evaluate((element) => element.matches(":focus-within")), "真实 wrapper 必须处于 focus-within").toBe(true);
    await noRing(wrapper, "focus-within wrapper");
  }
}

async function focusedBorder(control) {
  const border = await control.evaluate((element) => {
    const target = element.closest(".am-select") ?? element;
    const probe = document.createElement("span");
    probe.style.color = "var(--color-control-focus)";
    target.parentElement.append(probe);
    const accent = getComputedStyle(probe).color;
    probe.remove();
    const style = getComputedStyle(target);
    return { width: style.borderTopWidth, color: style.borderTopColor, accent };
  });
  expect(border.width, "焦点反馈仍保留 1px 控件边框").toBe("1px");
  expect(border.color, "用边框颜色而不是外框传达焦点").toBe(border.accent);
}

async function invalidBorder(control) {
  expect(await control.evaluate((element) => {
    const target = element.closest(".am-select") ?? element;
    const probe = document.createElement("span");
    probe.style.color = "var(--color-danger)";
    target.parentElement.append(probe);
    const expected = getComputedStyle(probe).color;
    probe.remove();
    return getComputedStyle(target).borderTopColor === expected;
  }), "移除外圈后，错误状态仍保留红色边框").toBe(true);
}

// 用真正的 Tab 进入目标；不能仅调用 focus() 后就声称覆盖了键盘焦点。
// 临时前置哨兵不改变生产路由/组件，不覆盖任何被测样式。
async function keyboardFocus(page, target) {
  await target.scrollIntoViewIfNeeded();
  await target.evaluate((element) => {
    document.querySelector("[data-focus-sentinel]")?.remove();
    const sentinel = document.createElement("button");
    sentinel.type = "button";
    sentinel.dataset.focusSentinel = "";
    sentinel.textContent = "键盘起点";
    sentinel.style.cssText = "position:fixed;left:0;top:0;width:1px;height:1px;opacity:0;";
    element.before(sentinel);
    sentinel.focus({ preventScroll: true });
  });
  try {
    // The native Tab must start at the sentinel, not at a nested editor which
    // restored its selection after our DOM mutation. Never retarget focus here.
    await expect(page.locator("[data-focus-sentinel]")).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(target).toBeFocused();
    expect(await target.evaluate((element) => element.matches(":focus-visible")), "Tab 必须真的产生 :focus-visible").toBe(true);
  } finally {
    await page.locator("[data-focus-sentinel]").evaluateAll((nodes) => nodes.forEach((node) => node.remove()));
  }
}

async function agentForm(page) {
  await page.goto(`/agents?agent=${agent.id}`);
  const group = page.locator('[data-field-group="agent-configuration"]');
  const name = group.locator("input").first();
  await expect(name).toBeEnabled();
  await expect(name).toHaveValue(agent.name);
  return {
    name, note: group.locator("input").nth(1), select: group.locator("select"),
    model: group.locator(".am-model-picker input"), prompt: page.locator("textarea.am-textarea")
  };
}

for (const mobile of [false, true]) test.describe(`input focus ${mobile ? "390 touch" : "1280 desktop"}`, () => {
  test.use({ viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 820 }, hasTouch: mobile });
  test("真实 Agent Input/Select/Textarea/ModelPicker 的鼠标、程序与 Tab 焦点", { tag: ["@input-focus", mobile ? "@mobile" : "@desktop"] }, async ({ page }, info) => {
    const controls = await agentForm(page);
    for (const [name, control] of Object.entries(controls)) {
      // Field 自动关联与可访问名称必须仍然存在。
      expect(await control.evaluate((element) => [...element.labels ?? []].some((label) => label.textContent.trim()) || Boolean(element.getAttribute("aria-label"))), name).toBe(true);
      await noRing(control, `${name} idle`);
      await control.hover();
      await noRing(control, `${name} hover`);
      await control.click();
      // select 只检查页面内元素，不检查 OS/浏览器的下拉弹层。
      if (name === "select") await page.keyboard.press("Escape");
      await expect(control).toBeFocused();
      await inputFamily(control);
      await control.blur();
      await control.focus();
      await expect(control).toBeFocused();
      await inputFamily(control);
      await keyboardFocus(page, control);
      await inputFamily(control);
      await focusedBorder(control);
    }
    await controls.name.hover();
    await page.mouse.down();
    try {
      expect(await controls.name.evaluate((element) => element.matches(":active"))).toBe(true);
      await inputFamily(controls.name);
    } finally { await page.mouse.up(); }
    const before = await controls.note.inputValue();
    await controls.note.fill("键盘与输入绑定仍然正常");
    await expect(controls.note).toHaveValue("键盘与输入绑定仍然正常");
    await expect(page.getByRole("button", { name: "保存", exact: true })).toBeEnabled();
    await controls.note.fill(before); // 不保存 Agent，不创建修订或 Run。
    await controls.select.selectOption(agent.agent_service_id);
    await expect(controls.select).toHaveValue(agent.agent_service_id);
    const trigger = page.locator(".am-model-picker__trigger");
    if (await trigger.count()) {
      await keyboardFocus(page, trigger);
      await inputFamily(trigger); // 不点击 showPicker，避免浏览器原生弹层。
    } else info.annotations.push({ type: "coverage", description: "此 ready CLI 无模型建议；真实 ModelPicker 输入已覆盖，trigger 由 DOM fixture 补足。" });
    await keyboardFocus(page, controls.name);
    await expect.poll(() => controls.name.evaluate((element) => Number.parseFloat(getComputedStyle(element).borderTopWidth))).toBeGreaterThan(0);
    await noRing(controls.name);
    await page.screenshot({ path: info.outputPath(`agent-input-keyboard-${mobile ? "touch" : "desktop"}.png`), animations: "disabled" });
  });
});

async function nativeFixture(page) {
  await page.evaluate(() => {
    const host = document.createElement("section");
    host.id = "input-focus-fixture";
    host.setAttribute("aria-label", "仅测试的原生输入边界");
    host.style.cssText = "position:relative;z-index:100;background:var(--color-surface);color:var(--color-text);padding:16px;display:grid;gap:8px;";
    for (const type of ["text", "search", "password", "email", "url", "tel", "number", "date", "time", "datetime-local", "month", "week", "file", "range", "color", "checkbox", "radio"]) {
      const label = document.createElement("label");
      label.textContent = `native ${type} `;
      const input = document.createElement("input");
      input.type = type;
      input.dataset.case = type;
      label.append(input); host.append(label);
    }
    host.insertAdjacentHTML("beforeend", `
      <label>readonly <input data-case="readonly" readonly value="保留原值"></label>
      <label>disabled <input data-case="disabled" disabled value="不可聚焦"></label>
      <label>invalid <input data-case="invalid" type="email" required value="invalid-email" aria-invalid="true"></label>
      <label>native select <select data-case="select"><option>a</option><option>b</option></select></label>
      <label>native textarea <textarea data-case="textarea">正文</textarea></label>
      <span class="am-switch"><input class="am-switch__input" data-case="switch" type="checkbox" role="switch" aria-label="fixture switch"><span class="am-switch__track"></span></span>
      <div class="am-model-picker"><button type="button" class="am-model-picker__button am-model-picker__trigger" data-case="model-trigger">fixture trigger</button></div>
      <div class="am-field__control"><button type="button" data-case="field-button">fixture field button</button></div>
      <div contenteditable="true" data-case="editable" aria-label="fixture editable">富文本</div>
      <button type="button" data-ordinary-button>普通按钮保留焦点</button><a href="#input-focus-fixture" data-ordinary-link>普通链接保留焦点</a>
    `);
    for (const role of ["textbox", "searchbox", "combobox", "listbox", "option", "checkbox", "radio", "switch", "spinbutton"]) {
      const control = document.createElement("div");
      control.role = role; control.tabIndex = 0; control.dataset.case = `role-${role}`;
      control.setAttribute("aria-label", `fixture ${role}`);
      if (["checkbox", "radio", "switch"].includes(role)) control.setAttribute("aria-checked", "false");
      if (role === "combobox") control.setAttribute("aria-expanded", "false");
      control.textContent = role; host.append(control);
    }
    document.body.append(host);
  });
  return page.locator("#input-focus-fixture");
}

test("原生 type / ARIA / invalid / readonly / disabled 状态完整边界", { tag: "@input-focus" }, async ({ page }) => {
  await agentForm(page);
  const fixture = await nativeFixture(page);
  for (const control of await fixture.locator("[data-case]").all()) {
    const type = await control.getAttribute("data-case");
    await noRing(control, `${type} idle`);
    if (type === "disabled") {
      await control.focus();
      await expect(control).not.toBeFocused();
      await noRing(control, "disabled 不可聚焦也不出现装饰");
      continue;
    }
    await control.hover();
    await noRing(control, `${type} hover`);
    // file/color 的点击会开启 OS UI，这不属于应用 CSS 的作用范围。
    if (!["file", "color"].includes(type)) {
      await control.click();
      if (["select", "date", "time", "datetime-local", "month", "week"].includes(type)) await page.keyboard.press("Escape");
      await noRing(control, `${type} mouse`);
    }
    await control.focus();
    await expect(control).toBeFocused();
    await inputFamily(control);
    await keyboardFocus(page, control);
    await inputFamily(control);
    if (type === "readonly") {
      await expect(control).not.toBeEditable();
      await page.keyboard.type("不可写入");
      await expect(control).toHaveValue("保留原值");
    }
    if (type === "invalid") {
      expect(await control.evaluate((element) => element.matches(":invalid"))).toBe(true);
      await expect(control).toHaveAttribute("aria-invalid", "true");
    }
    if (["checkbox", "radio", "switch"].includes(type)) {
      await page.keyboard.press("Space");
      await noRing(control, `${type} checked/unchecked`);
      if (type === "switch") await noRing(fixture.locator(".am-switch__track"));
    }
  }
  const disabled = fixture.locator('[data-case="disabled"]');
  await disabled.evaluate((element) => {
    const after = document.createElement("button"); after.type = "button";
    after.dataset.afterDisabled = ""; after.textContent = "disabled 后继"; element.after(after);
  });
  await disabled.evaluate((element) => {
    const before = document.createElement("button"); before.type = "button";
    before.textContent = "disabled 前继"; element.before(before); before.focus();
  });
  await page.keyboard.press("Tab");
  await expect(fixture.locator("[data-after-disabled]")).toBeFocused();
});

test("真实控件的 invalid/readonly/disabled 和 important 层叠优先级，普通按钮链接仍有键盘 outline", { tag: "@input-focus" }, async ({ page }) => {
  const controls = await agentForm(page);
  await controls.name.fill("");
  expect(await controls.name.evaluate((element) => element.matches(":invalid"))).toBe(true);
  await controls.name.evaluate((element) => element.setAttribute("aria-invalid", "true"));
  await keyboardFocus(page, controls.name);
  await inputFamily(controls.name);
  await invalidBorder(controls.name);
  await controls.name.fill(agent.name);
  await controls.name.evaluate((element) => { element.removeAttribute("aria-invalid"); element.readOnly = true; });
  await keyboardFocus(page, controls.name);
  await expect(controls.name).not.toBeEditable();
  await inputFamily(controls.name);
  await controls.name.evaluate((element) => { element.readOnly = false; element.disabled = true; element.blur(); });
  await controls.name.focus();
  await expect(controls.name).not.toBeFocused();
  await noRing(controls.name);
  await controls.name.evaluate((element) => { element.disabled = false; });
  for (const control of [controls.select, controls.prompt, controls.model]) {
    await control.evaluate((element) => element.setAttribute("aria-invalid", "true"));
    await keyboardFocus(page, control);
    await inputFamily(control);
    await invalidBorder(control);
    await control.evaluate((element) => { element.removeAttribute("aria-invalid"); element.disabled = true; element.blur(); });
    await control.focus();
    await expect(control).not.toBeFocused();
    await noRing(control, "真实 Select/Textarea/ModelPicker disabled");
    await control.evaluate((element) => { element.disabled = false; });
  }
  const fixture = await nativeFixture(page);
  const conflict = await page.addStyleTag({ content: `
    @layer utilities {
      :is(input, select, textarea, [contenteditable], [data-case], .am-select, .am-model-picker, .am-field__control, .am-switch, .am-switch__track):is(:focus, :focus-visible, :focus-within, :hover, :invalid, [readonly], :disabled) {
        outline: 11px solid red !important; outline-offset: 9px !important; box-shadow: 0 0 0 13px red !important;
      }
    }
    :focus-visible { outline: 17px solid blue !important; outline-offset: 8px !important; box-shadow: 0 0 0 19px blue !important; }
  ` });
  try {
    for (const control of Object.values(controls)) { await keyboardFocus(page, control); await inputFamily(control); }
    for (const control of await fixture.locator("[data-case]:not(:disabled)").all()) {
      await keyboardFocus(page, control); await inputFamily(control);
    }
    await noRing(fixture.locator('[data-case="disabled"]'));
    await noRing(fixture.locator(".am-switch__track"));
  } finally { await conflict.evaluate((element) => element.remove()); }
  // 阴性对照必须在移除冲突样式之后检查产品自己的 outline，而非注入的蓝框。
  for (const target of [fixture.locator("[data-ordinary-button]"), fixture.locator("[data-ordinary-link]"), page.getByRole("button", { name: "取消", exact: true }), page.locator('a[href="/skills"]').last()]) {
    await keyboardFocus(page, target);
    const style = await target.evaluate((element) => {
      const value = getComputedStyle(element); return { style: value.outlineStyle, width: parseFloat(value.outlineWidth) };
    });
    expect(style.style).not.toBe("none");
    expect(style.width).toBe(2);
  }
});

test("七套配色 × 明暗下的真实输入焦点及 forced-colors", { tag: "@input-focus" }, async ({ page, request, baseURL }) => {
  test.setTimeout(120_000);
  const api = await authenticatedAPI(request, baseURL);
  const original = await api("/me/preferences");
  let preferences = original;
  try {
    for (const palette of palettes) for (const theme of ["light", "dark"]) {
      preferences = await api("/me/preferences", { expected_version: preferences.version, theme, appearance: { palette } }, "PATCH");
      const controls = await agentForm(page);
      await expect(page.locator("html")).toHaveAttribute("data-palette", palette);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      for (const control of [controls.name, controls.select, controls.model]) {
        await keyboardFocus(page, control); await inputFamily(control);
        await focusedBorder(control);
      }
    }
    await page.emulateMedia({ forcedColors: "active" });
    expect(await page.evaluate(() => matchMedia("(forced-colors: active)").matches)).toBe(true);
    const controls = await agentForm(page);
    for (const control of Object.values(controls)) { await keyboardFocus(page, control); await inputFamily(control); }
    const fixture = await nativeFixture(page);
    for (const control of await fixture.locator("[data-case]:not(:disabled)").all()) {
      await keyboardFocus(page, control); await inputFamily(control);
    }
  } finally {
    await page.emulateMedia({ forcedColors: "none" });
    await api("/me/preferences", { expected_version: preferences.version, theme: original.theme, appearance: original.appearance }, "PATCH");
  }
});

test("真实灵感 ProseMirror/contenteditable、代码块 CodeMirror 与源码 Textarea", { tag: "@input-focus" }, async ({ page }, info) => {
  await page.goto(`/inspirations?note=${note.id}`);
  const editor = page.locator(`[data-inspiration-editor="${note.id}"]`);
  const rich = editor.locator('.ProseMirror[contenteditable="true"]');
  await expect(rich).toBeVisible();
  // The root's center can be inside its CodeMirror node view. In that case a
  // later selection sync legitimately restores CodeMirror focus, so a style
  // check on the outer root never established the ProseMirror precondition.
  // Click actual prose and observe both focus and its selection before testing
  // the root. CodeMirror gets its own mouse/native-Tab checks below.
  await rich.locator(":scope > p").first().click();
  await expect.poll(() => rich.evaluate((element) => {
    const paragraph = element.querySelector(":scope > p");
    return document.activeElement === element &&
      Boolean(paragraph?.contains(window.getSelection()?.anchorNode ?? null));
  }), { message: "ProseMirror 鼠标聚焦及正文选区必须就绪，而非嵌套 CodeMirror" }).toBe(true);
  await noRing(rich, "ProseMirror mouse");
  await rich.focus();
  await expect(rich).toBeFocused();
  await noRing(rich, "ProseMirror programmatic");
  await keyboardFocus(page, rich);
  await noRing(rich, "ProseMirror keyboard");
  const code = editor.locator('.cm-content[contenteditable="true"]');
  if (await code.count()) {
    await code.first().click();
    await expect(code.first()).toBeFocused();
    await noRing(code.first());
    await keyboardFocus(page, code.first());
    await noRing(code.first());
    const wrapper = code.first().locator("xpath=ancestor::*[contains(concat(' ', normalize-space(@class), ' '), ' cm-editor ')]");
    expect(await wrapper.evaluate((element) => element.matches(":focus-within"))).toBe(true);
    await noRing(wrapper, "真实 CodeMirror focus-within");
  } else info.annotations.push({ type: "coverage", description: "当前编辑器未挂载 CodeMirror，未伪造代码编辑器；真实 ProseMirror 与源码 Textarea 已覆盖。" });
  const checkbox = page.locator('[data-field-group="inspiration-project"]').locator('input[type="checkbox"]');
  await keyboardFocus(page, checkbox);
  await noRing(checkbox); // 不切换完成状态，避免额外业务写入。
  await page.getByRole("button", { name: "Markdown 源码", exact: true }).click();
  const source = editor.locator("textarea");
  await keyboardFocus(page, source);
  await inputFamily(source);
  await expect(source).toHaveValue(/const focus = true;/);
});
