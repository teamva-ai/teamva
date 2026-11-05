import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import { writeFile } from "node:fs/promises";

const host = `/@fs${fileURLToPath(new URL("./fixtures/modal/index.html", import.meta.url))}`;
const viewports = [{ width: 1280, height: 820 }, { width: 975, height: 650 }, { width: 800, height: 600 }, { width: 390, height: 844 }];

async function modalGeometry(dialog) {
  return dialog.evaluate((node) => {
    const rect = (element) => {
      const { left, right, top, bottom, width, height } = element.getBoundingClientRect();
      return { left, right, top, bottom, width, height };
    };
    const body = node.querySelector('.am-modal__body, [role="listbox"]');
    const footer = node.querySelector(":scope > footer");
    const visible = (element) => element && element.getClientRects().length > 0;
    return { panel: rect(node), mask: rect(node.parentElement), viewport: { width: innerWidth, height: innerHeight },
      body: body && { ...rect(body), clientHeight: body.clientHeight, scrollHeight: body.scrollHeight, clientWidth: body.clientWidth, scrollWidth: body.scrollWidth },
      footer: visible(footer) ? rect(footer) : null,
      controls: [...node.querySelectorAll(":scope > header button, :scope > footer button")].filter(visible).map(rect) };
  });
}
function centeredAndContained({ panel, mask, viewport, footer, controls }, maxWidth) {
  expect(Math.abs(panel.left + panel.width / 2 - viewport.width / 2)).toBeLessThanOrEqual(1);
  expect(Math.abs(panel.top + panel.height / 2 - viewport.height / 2)).toBeLessThanOrEqual(1);
  expect(mask.left).toBe(0); expect(mask.top).toBe(0);
  expect(mask.width).toBe(viewport.width); expect(mask.height).toBe(viewport.height);
  expect(panel.width).toBeLessThanOrEqual(maxWidth);
  expect(panel.left).toBeGreaterThanOrEqual(8); expect(panel.right).toBeLessThanOrEqual(viewport.width - 8);
  expect(panel.top).toBeGreaterThanOrEqual(8); expect(panel.bottom).toBeLessThanOrEqual(viewport.height - 8);
  for (const box of [...controls, ...(footer ? [footer] : [])]) {
    expect(box.left).toBeGreaterThanOrEqual(panel.left); expect(box.right).toBeLessThanOrEqual(panel.right);
    expect(box.top).toBeGreaterThanOrEqual(panel.top); expect(box.bottom).toBeLessThanOrEqual(panel.bottom);
  }
}

for (const viewport of viewports) test.describe(`Modal geometry ${viewport.width}×${viewport.height}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });

  test("CLI rows fit their actual container and nested long diagnostics keep the footer visible", async ({ page }, info) => {
    await page.goto(`${host}?case=guide`);
    const guide = page.locator("[data-onboarding-dialog]");
    await expect(guide.locator("[data-programming-tool]").first()).toBeVisible();
    const guideBox = await modalGeometry(guide);
    centeredAndContained(guideBox, 860);
    expect(guideBox.body.scrollWidth).toBeLessThanOrEqual(guideBox.body.clientWidth + 1);
    const rows = await guide.locator("[data-programming-tool]").evaluateAll((items) => items.map((row) => {
      const box = row.getBoundingClientRect(), action = row.querySelector("button").getBoundingClientRect();
      return { row: { left: box.left, right: box.right }, action: { left: action.left, right: action.right }, clientWidth: row.clientWidth, scrollWidth: row.scrollWidth };
    }));
    for (const row of rows) {
      expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1);
      expect(row.action.left).toBeGreaterThanOrEqual(row.row.left);
      expect(row.action.right).toBeLessThanOrEqual(row.row.right);
    }
    await guide.locator('[data-programming-tool="opencode"]').getByRole("button", { name: "诊断", exact: true }).click();
    const diagnostic = page.getByRole("dialog", { name: "诊断 OpenCode", exact: true });
    await expect(diagnostic.getByText("24. 诊断步骤 24", { exact: true })).toBeAttached();
    const diagnosticBox = await modalGeometry(diagnostic);
    centeredAndContained(diagnosticBox, 640);
    expect(diagnosticBox.body.scrollHeight).toBeGreaterThan(diagnosticBox.body.clientHeight);
    expect(diagnosticBox.body.scrollWidth).toBeLessThanOrEqual(diagnosticBox.body.clientWidth + 1);
    expect(await diagnostic.evaluate((node) => Number(node.parentElement.style.zIndex))).toBeGreaterThan(await guide.evaluate((node) => Number(node.parentElement.style.zIndex)));
    await diagnostic.locator(":scope > footer").getByRole("button", { name: "关闭", exact: true }).click();
    await expect(diagnostic).toBeHidden();
    await expect(guide).toBeVisible();
    await page.screenshot({ path: info.outputPath("onboarding-contained.png") });
  });

  for (const width of [480, 640, 720, 820, 860]) test(`width ${width}, tall body and hostile transformed ancestor`, async ({ page }) => {
    await page.goto(`${host}?case=generic&width=${width}&rows=60`);
    const dialog = page.getByRole("dialog", { name: "公共弹窗几何", exact: true });
    await expect(dialog).toBeVisible();
    const geometry = await modalGeometry(dialog);
    centeredAndContained(geometry, width);
    expect(geometry.body.scrollHeight).toBeGreaterThan(geometry.body.clientHeight);
    expect(geometry.body.scrollWidth).toBeLessThanOrEqual(geometry.body.clientWidth + 1);
    await dialog.getByRole("button", { name: "完成几何测试" }).click();
    await expect(dialog).toBeHidden();
  });

  test("search dialog is centered as well", async ({ page }) => {
    await page.goto(`${host}?case=search`);
    await page.getByRole("button", { name: "打开全局搜索" }).click();
    const dialog = page.getByRole("dialog", { name: "搜索当前项目" });
    await expect(dialog).toBeVisible();
    // 手机有意隐藏键盘快捷键底栏；其零尺寸盒不属于可见布局。
    if (viewport.width < 768) await expect(dialog.locator(":scope > footer")).toBeHidden();
    else await expect(dialog.locator(":scope > footer")).toBeVisible();
    centeredAndContained(await modalGeometry(dialog), 720);
  });
});

// Install before navigation, not after toBeVisible(): the latter can miss the
// first painted size. MutationObserver only wakes the rAF loop; it never reads
// layout while Svelte is still assembling/moving an inline subtree. These are
// pre-paint rAF border boxes, not a claim that a historical flash was reproduced.
function installFrameProbe(initial) {
  let current;
  const rect = (element) => {
    const { left, right, top, bottom, width, height } = element.getBoundingClientRect();
    return { left, right, top, bottom, width, height };
  };
  const visible = (element) => {
    if (!element?.isConnected || !element.getClientRects().length) return false;
    for (let ancestor = element; ancestor; ancestor = ancestor.parentElement) {
      const style = getComputedStyle(ancestor);
      if (ancestor.hidden || style.display === "none" || style.visibility === "hidden" ||
          style.visibility === "collapse" || Number(style.opacity) === 0) return false;
    }
    const box = rect(element);
    return box.width > 0 && box.height > 0 && box.right > 0 && box.bottom > 0 &&
      box.left < innerWidth && box.top < innerHeight;
  };
  const geometry = (node) => {
    const body = node.querySelector('.am-modal__body, [role="listbox"]');
    const footer = node.querySelector(":scope > footer");
    const style = body && getComputedStyle(body);
    return {
      panel: rect(node), mask: rect(node.parentElement), viewport: { width: innerWidth, height: innerHeight },
      body: body && { ...rect(body), clientHeight: body.clientHeight, scrollHeight: body.scrollHeight,
        clientWidth: body.clientWidth, scrollWidth: body.scrollWidth,
        flexBasis: style.flexBasis, flexGrow: style.flexGrow, flexShrink: style.flexShrink },
      footer: visible(footer) ? rect(footer) : null,
      controls: [...node.querySelectorAll(":scope > header button, :scope > footer button")].filter(visible).map(rect)
    };
  };
  const sameBox = (left, right) => left && right && ["left", "top", "width", "height"].every((key) =>
    Math.abs(left[key] - right[key]) <= 0.1);
  window.__modalFrameProbe = {
    arm(options) {
      current?.stop("rearmed");
      const state = { done: false, reason: "", frames: [], ignoredUnstyledFrames: 0, mutationBatches: 0 };
      let raf = 0, stableFrames = 0;
      const observer = new MutationObserver(() => { state.mutationBatches += 1; schedule(); });
      const stop = (reason) => {
        if (state.done) return;
        state.done = true;
        state.reason = reason;
        observer.disconnect();
        cancelAnimationFrame(raf);
        clearTimeout(timeout);
      };
      const sample = (time) => {
        raf = 0;
        const node = document.querySelector(options.selector);
        if (node && visible(node)) {
          // Ignore unstyled inline nodes before CSS is ready, not a styled
          // collapsed body. Do not wait for portal, focus, children or fonts.
          if (getComputedStyle(node).display !== "flex" || getComputedStyle(node.parentElement).position !== "fixed") {
            state.ignoredUnstyledFrames += 1;
          } else {
            const frame = { time, ...geometry(node) };
            if (options.companionSelector) {
              const companion = document.querySelector(options.companionSelector);
              frame.companion = companion && geometry(companion);
            }
            const previous = state.frames.at(-1);
            stableFrames = previous && sameBox(frame.panel, previous.panel) && sameBox(frame.body, previous.body) ? stableFrames + 1 : 0;
            state.frames.push(frame);
            // Observe well beyond the first equal pair; retain every visible
            // frame, including early small ones, rather than only the tail.
            if (state.frames.length >= 24 && stableFrames >= 8) return stop("stable");
          }
        }
        schedule();
      };
      const schedule = () => { if (!raf && !state.done) raf = requestAnimationFrame(sample); };
      const timeout = setTimeout(() => stop("timeout"), 10_000);
      current = { state, stop };
      observer.observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
      schedule();
    },
    result: () => current?.state
  };
  window.__modalFrameProbe.arm(initial);
}

async function coldFrameProbe(page, browserName, options) {
  // A fresh Playwright context per test plus routing disables the HTTP cache
  // in either engine. No HAR/trace, backend or Provider is involved.
  await page.route("**/*", (route) => route.continue());
  if (browserName === "chromium") {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 6 });
  }
  await page.addInitScript(installFrameProbe, options);
}

async function finishFrameProbe(page, info, label) {
  await page.waitForFunction(() => window.__modalFrameProbe?.result()?.done);
  const result = await page.evaluate(() => window.__modalFrameProbe.result());
  const evidence = info.outputPath(`${label}-raf-geometry.json`);
  await writeFile(evidence, JSON.stringify(result, null, 2));
  await info.attach(`${label}-raf-geometry`, { path: evidence, contentType: "application/json" });
  expect(result.reason, `${label}: sampler must reach a stable tail`).toBe("stable");
  expect(result.frames.length).toBeGreaterThanOrEqual(24);
  expect(result.mutationBatches, `${label}: observer was armed before mounting`).toBeGreaterThan(0);
  return result.frames;
}

function stableGeometry(frames, maxWidth, { long = false, companionWidth } = {}) {
  const final = frames.at(-1);
  for (const [index, frame] of frames.entries()) {
    centeredAndContained(frame, maxWidth);
    expect(frame.body, `frame ${index}: styled body must already exist`).toBeTruthy();
    expect(frame.body.height).toBeGreaterThan(0);
    // The geometry assertions above/below are essential: this contract alone
    // is not evidence that the old basis caused a visible first-frame defect.
    expect(frame.body.flexBasis).toBe("auto");
    expect(frame.body.flexGrow).toBe("0");
    expect(frame.body.flexShrink).toBe("1");
    for (const part of ["panel", "body"]) for (const key of ["left", "top", "width", "height"]) {
      expect(Math.abs(frame[part][key] - final[part][key]), `frame ${index}: ${part}.${key} differs from stable tail`).toBeLessThanOrEqual(1);
    }
    expect(frame.body.left).toBeGreaterThanOrEqual(frame.panel.left);
    expect(frame.body.right).toBeLessThanOrEqual(frame.panel.right);
    expect(frame.body.top).toBeGreaterThanOrEqual(frame.panel.top);
    expect(frame.body.bottom).toBeLessThanOrEqual(frame.panel.bottom);
    expect(frame.body.scrollWidth).toBeLessThanOrEqual(frame.body.clientWidth + 1);
    if (long) expect(frame.body.scrollHeight).toBeGreaterThan(frame.body.clientHeight);
    if (companionWidth) {
      expect(frame.companion).toBeTruthy();
      centeredAndContained(frame.companion, companionWidth);
      for (const key of ["left", "top", "width", "height"]) {
        expect(Math.abs(frame.companion.panel[key] - final.companion.panel[key]), `frame ${index}: parent ${key}`).toBeLessThanOrEqual(1);
      }
    }
  }
  return final;
}

const firstFrameViewports = [{ width: 1280, height: 820 }, { width: 390, height: 480 }];
for (const viewport of firstFrameViewports) test.describe(`Modal first visible frame ${viewport.width}×${viewport.height}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });

  for (const rows of [1, 60]) for (const closed of [false, true]) {
    test(`${closed ? "first click" : "cold initial mount"}, ${rows === 1 ? "short" : "long"} static content and repeated open`, async ({ page, browserName }, info) => {
      const options = { selector: '[data-fixture-modal="outer"]' };
      await coldFrameProbe(page, browserName, options);
      await page.goto(`${host}?case=generic&rows=${rows}&unbroken=0&closed=${closed ? 1 : 0}`);
      if (closed) await page.getByRole("button", { name: "打开几何弹窗", exact: true }).click();
      const first = stableGeometry(await finishFrameProbe(page, info, "first"), 640, { long: rows > 1 });
      const dialog = page.locator(options.selector);
      await dialog.getByRole("button", { name: "完成几何测试", exact: true }).click();
      await expect(dialog).toBeHidden();
      await page.evaluate((value) => window.__modalFrameProbe.arm(value), options);
      await page.getByRole("button", { name: "打开几何弹窗", exact: true }).click();
      const repeated = stableGeometry(await finishFrameProbe(page, info, "repeated"), 640, { long: rows > 1 });
      expect(Math.abs(first.panel.height - repeated.panel.height)).toBeLessThanOrEqual(1);
    });
  }

  test("static nested modal first and repeated open preserve both viewport boxes", async ({ page, browserName }, info) => {
    const outer = '[data-fixture-modal="outer"]';
    const options = { selector: '[data-fixture-modal="nested"]', companionSelector: outer };
    await coldFrameProbe(page, browserName, { selector: outer });
    await page.goto(`${host}?case=generic&rows=1&unbroken=0&closed=1&nested=1`);
    await page.getByRole("button", { name: "打开几何弹窗", exact: true }).click();
    stableGeometry(await finishFrameProbe(page, info, "outer-first"), 640);
    const opener = page.getByRole("button", { name: "打开嵌套弹窗", exact: true });
    for (const label of ["nested-first", "nested-repeated"]) {
      await page.evaluate((value) => window.__modalFrameProbe.arm(value), options);
      // WebKit does not necessarily focus a button on pointer activation.
      // Open by keyboard so restoration has an explicit, portable owner.
      await opener.focus();
      await opener.press("Enter");
      stableGeometry(await finishFrameProbe(page, info, label), 480, { long: true, companionWidth: 640 });
      const nested = page.locator(options.selector);
      expect(await nested.evaluate((node) => Number(node.parentElement.style.zIndex))).toBeGreaterThan(await page.locator(outer).evaluate((node) => Number(node.parentElement.style.zIndex)));
      await nested.getByRole("button", { name: "完成嵌套几何测试", exact: true }).click();
      await expect(nested).toBeHidden();
      await expect(opener).toBeFocused();
    }
  });

  test("empty search first and repeated open keep the first visible geometry", async ({ page, browserName }, info) => {
    const options = { selector: '[role="dialog"][aria-label="搜索当前项目"]' };
    await coldFrameProbe(page, browserName, options);
    await page.goto(`${host}?case=search`);
    for (const label of ["search-first", "search-repeated"]) {
      if (label === "search-repeated") await page.evaluate((value) => window.__modalFrameProbe.arm(value), options);
      await page.getByRole("button", { name: "打开全局搜索", exact: true }).click();
      stableGeometry(await finishFrameProbe(page, info, label), 720);
      const dialog = page.locator(options.selector);
      await expect(dialog.locator('input[type="search"]')).toHaveValue("");
      await dialog.locator("header").getByRole("button", { name: "关闭", exact: true }).click();
      await expect(dialog).toBeHidden();
    }
  });
});
