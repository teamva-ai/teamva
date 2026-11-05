import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";

const columns = [
  ["overview", "/workbench", "概览"], ["issues", "/issues", "任务"],
  ["conversations", "/conversations", "分支对话"], ["resources", "/resources", "资源"],
  ["memories", "/memories", "项目记忆"], ["schedules", "/schedules", "定时任务"], ["changes", "/changes", "变更"]
];

async function authenticatedAPI(request, baseURL) {
  const headers = { Origin: baseURL, "Content-Type": "application/json" };
  const bootstrap = await request.post("/v1/auth/local-session", { headers, data: {} });
  expect(bootstrap.status()).toBe(200);
  headers["X-CSRF-Token"] = (await bootstrap.json()).data.csrf_token;
  return async (path) => {
    const response = await request.get(`/v1${path}`, { headers });
    expect(response.ok()).toBe(true);
    return (await response.json()).data;
  };
}

async function playbackSample(animation) {
  return animation.evaluate((root) => {
    const target = root.querySelector(".cursor-horizontal");
    const track = root.getAnimations({ subtree: true }).find((item) => item.effect?.target === target);
    const timing = track?.effect.getTiming();
    const rect = root.querySelector("[data-tour-cursor] path").getBoundingClientRect();
    return { exists: Boolean(track), running: track?.playState === "running", duration: timing?.duration ?? 0,
      infinite: timing?.iterations === Infinity, clock: typeof track?.currentTime === "number" ? track.currentTime : null,
      iteration: track?.effect.getComputedTiming().currentIteration ?? null,
      x: rect.x, y: rect.y, paused: root.dataset.paused, allowed: root.dataset.allowMotion };
  });
}

async function expectNaturalAutoplay(animation) {
  await expect.poll(async () => {
    const sample = await playbackSample(animation);
    return sample.running && sample.clock !== null;
  }).toBe(true);
  const initial = await playbackSample(animation);
  expect(initial.duration).toBe(11200);
  expect(initial.infinite).toBe(true);
  expect(initial.paused).toBe("false");
  expect(initial.allowed).toBe("true");
  // No seeking, replay click, forced clock or playback-rate change: both real
  // elapsed timeline and rendered cursor geometry must advance on their own.
  await expect.poll(async () => {
    const next = await playbackSample(animation);
    return next.running && next.clock - initial.clock >= 350 &&
      Math.hypot(next.x - initial.x, next.y - initial.y) > 1;
  }, { intervals: [100, 150, 250], timeout: 4000 }).toBe(true);
}

test("首次进入七栏目逐一演示，配置引导优先，已看状态刷新及新窗口保留", { tag: ["@feature-tours", "@first-entry"] }, async ({ page, request, baseURL, browser }) => {
  test.skip(process.env.TEAMVA_TEST_UNSEEN_FEATURE_TOURS !== "1", "首进验收需要显式指定全新隔离服务，普通业务回归已完成这些引导");
  const api = await authenticatedAPI(request, baseURL);
  expect((await api("/me/feature-tours")).seen).toEqual([]);
  const seenRequests = [], forbidden = [];
  page.on("request", (request) => {
    if (request.method() !== "POST") return;
    const path = new URL(request.url()).pathname;
    const match = /\/me\/feature-tours\/([^/]+)\/seen$/.exec(path);
    if (match) seenRequests.push(match[1]);
    if (/\/(?:runs|conversation-graphs|messages|goals)(?:\/|$)/.test(path)) forbidden.push(path);
  });
  // Reproduce the user's reduced-motion environment on the true first visit.
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/workbench");
  const setup = page.locator("[data-onboarding-dialog]");
  await expect(setup).toBeVisible();
  await expect(page.locator("[data-feature-tour-dialog]")).toHaveCount(0);
  expect((await api("/me/feature-tours")).seen).toEqual([]);
  await setup.getByRole("button", { name: "暂时跳过", exact: true }).click();
  await expect(setup).toBeHidden();
  // Skip returns to the overview; Finish (covered by the existing setup suite)
  // enters Issues. No section was marked while setup owned the modal.
  for (const [id, path] of columns) {
    if (new URL(page.url()).pathname !== path) await page.goto(path);
    const dialog = page.locator(`[data-feature-tour-dialog="${id}"]`);
    await expect(dialog).toBeVisible();
    await expect(dialog.locator(`[data-feature-tour-animation="${id}"]`)).toBeVisible();
    await expectNaturalAutoplay(dialog.locator(`[data-feature-tour-animation="${id}"]`));
    await expect(dialog.getByText("功能演示 · 不会操作真实数据", { exact: true })).toHaveCount(0);
    await expect.poll(async () => (await api("/me/feature-tours")).seen).toContain(id);
    await dialog.getByRole("button", { name: "开始使用", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const loaded = page.waitForResponse((response) => new URL(response.url()).pathname === "/v1/me/feature-tours" && response.request().method() === "GET");
    await page.reload(); await loaded;
    await expect(page.locator("[data-feature-tour-dialog]")).toHaveCount(0);
  }
  expect([...seenRequests].sort()).toEqual(columns.map(([id]) => id).sort());
  expect((await api("/me/feature-tours")).seen).toEqual(columns.map(([id]) => id));
  const context = await browser.newContext({ baseURL, locale: "zh-CN", viewport: { width: 1280, height: 820 } });
  try {
    const second = await context.newPage();
    const loaded = second.waitForResponse((response) => new URL(response.url()).pathname === "/v1/me/feature-tours" && response.request().method() === "GET");
    await second.goto("/resources"); await loaded;
    await expect(second.locator("[data-feature-tour-dialog]")).toHaveCount(0);
  } finally { await context.close(); }
  expect(forbidden).toEqual([]);
});

for (const width of [1280, 390]) test.describe(`feature tour presentation ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("七套独立动画自然自动播放、暂停重播、键盘与低动态环境", { tag: ["@feature-tours", "@layout"] }, async ({ page }, info) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    for (const [id, path, title] of columns) {
      await page.goto(path);
      const help = page.locator(`[data-feature-tour-trigger="${id}"]`);
      await expect(help).toBeEnabled();
      await help.click();
      const dialog = page.locator(`[data-feature-tour-dialog="${id}"]`);
      await expect(dialog.getByRole("heading", { name: title, exact: true })).toBeVisible();
      const animation = dialog.locator(`[data-feature-tour-animation="${id}"]`);
      await expect(animation).toBeVisible();
      await expect(dialog.locator(".tour-demo-label")).toHaveCount(0);
      await expect(dialog.getByText("功能演示 · 不会操作真实数据", { exact: true })).toHaveCount(0);
      await expect(animation.locator("[data-tour-cursor]")).toHaveCount(1);
      const state = await dialog.evaluate((element) => {
        const visual = element.querySelector("[data-tour-visual]").getBoundingClientRect();
        const explanation = element.querySelector("[data-tour-explanation]").getBoundingClientRect();
        const action = element.querySelector("[data-tour-acknowledge]").getBoundingClientRect();
        const rect = element.getBoundingClientRect();
        return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight,
          visualShare: visual.height / (visual.height + explanation.height), actionRightGap: explanation.right - action.right,
          inFocus: element.contains(document.activeElement) };
      });
      expect(state.left).toBeGreaterThanOrEqual(0); expect(state.right).toBeLessThanOrEqual(state.width);
      expect(state.top).toBeGreaterThanOrEqual(0); expect(state.bottom).toBeLessThanOrEqual(state.height);
      expect(state.visualShare).toBeGreaterThan(0.55); expect(state.visualShare).toBeLessThan(0.80);
      expect(state.inFocus).toBe(true);
      expect(Math.abs(state.actionRightGap)).toBeLessThanOrEqual(1);
      await expectNaturalAutoplay(animation);
      if (width === 1280 && id === "overview") {
        const initial = await playbackSample(animation);
        // Cross a complete real-time cycle before any deterministic screenshot
        // seeks, so one-shot or end-of-timeline freezes cannot pass as looping.
        await expect.poll(async () => {
          const next = await playbackSample(animation);
          return next.running && next.iteration > initial.iteration && next.clock > 11200;
        }, { intervals: [250], timeout: 14000 }).toBe(true);
        await expectNaturalAutoplay(animation);
        await info.attach("natural-loop", { body: JSON.stringify({ initial, next: await playbackSample(animation) }), contentType: "application/json" });
      }
      // Seek the actual CSS timeline and sample rendered cursor border boxes,
      // not merely class names or the opacity-only outer pointer group.
      const moving = await animation.evaluate(async (root) => {
        const animations = root.getAnimations({ subtree: true });
        const cursor = root.querySelector("[data-tour-cursor] path");
        const seek = async (time) => {
          for (const animation of animations) animation.currentTime = time;
          await new Promise(requestAnimationFrame); await new Promise(requestAnimationFrame);
          const rect = cursor.getBoundingClientRect(); return { x: rect.x, y: rect.y };
        };
        const initial = await seek(900), after = await seek(1600);
        return { initial, after, animationCount: animations.length };
      });
      expect(moving.animationCount).toBeGreaterThan(0);
      expect(Math.hypot(moving.after.x - moving.initial.x, moving.after.y - moving.initial.y)).toBeGreaterThan(1);
      const cursorMotion = await animation.evaluate((root) => root.getAnimations({ subtree: true })
        .filter((a) => root.querySelector("[data-tour-cursor]").contains(a.effect?.target))
        .some((a) => a.effect.getKeyframes().filter((frame) => frame.transform?.includes("translate")).length > 2));
      expect(cursorMotion).toBe(true);
      await animation.evaluate(async (root) => {
        for (const item of root.getAnimations({ subtree: true })) item.currentTime = 9900;
        await new Promise(requestAnimationFrame); await new Promise(requestAnimationFrame);
      });
      await dialog.getByRole("button", { name: "暂停动画", exact: true }).click();
      await expect.poll(() => animation.evaluate((root) => root.getAnimations({ subtree: true }).every((a) => a.playState !== "running"))).toBe(true);
      await dialog.screenshot({ path: info.outputPath(`feature-tour-${id}-${width}.png`), animations: "allow" });
      if (width === 1280) {
        const preview = await dialog.evaluate((dialog) => {
          const root = dialog.querySelector("[data-feature-tour-animation]"), clone = root.cloneNode(true);
          clone.dataset.paused = "false";
          const hash = [...root.classList].find((name) => name.startsWith("svelte-")), rules = [];
          for (const sheet of document.styleSheets) {
            try { for (const rule of sheet.cssRules) if (rule.cssText.includes(hash)) rules.push(rule.cssText); } catch {}
          }
          const variables = {};
          for (const key of ["--color-canvas", "--color-surface", "--color-surface-subtle", "--color-border", "--color-text", "--color-secondary", "--color-muted", "--color-accent", "--color-accent-subtle", "--color-success", "--color-danger", "--font-sans", "--font-mono"])
            variables[key] = getComputedStyle(root).getPropertyValue(key).trim();
          return { id: root.dataset.featureTourAnimation, title: dialog.querySelector("h2").textContent,
            description: dialog.querySelector(".tour-description").textContent, html: clone.outerHTML, css: rules.join("\n"), variables };
        });
        await writeFile(info.outputPath(`feature-tour-${id}.json`), JSON.stringify(preview));
      }
      await dialog.getByRole("button", { name: "重新播放", exact: true }).click();
      await expect(dialog.getByRole("button", { name: "暂停动画", exact: true })).toHaveAttribute("aria-pressed", "false");
      await page.emulateMedia({ reducedMotion: "reduce" });
      await expectNaturalAutoplay(animation);
      const otherMotion = await page.evaluate(() => {
        const probe = document.createElement("span");
        probe.style.animation = "unrelated-motion-probe 2s infinite";
        document.body.append(probe);
        const style = getComputedStyle(probe);
        const result = { duration: style.animationDuration, iterations: style.animationIterationCount };
        probe.remove();
        return result;
      });
      expect(parseFloat(otherMotion.duration)).toBeLessThan(0.001);
      expect(otherMotion.iterations).toBe("1");
      await dialog.getByRole("button", { name: "暂停动画", exact: true }).click();
      await expect.poll(() => animation.evaluate((root) => root.getAnimations({ subtree: true }).every((a) => a.playState !== "running"))).toBe(true);
      await page.emulateMedia({ reducedMotion: "no-preference" });
      await expect.poll(() => animation.evaluate((root) => root.getAnimations({ subtree: true }).every((a) => a.playState !== "running"))).toBe(true);
      await dialog.getByRole("button", { name: "播放动画", exact: true }).click();
      await expect.poll(async () => (await playbackSample(animation)).running).toBe(true);
      await page.keyboard.press("Escape"); await expect(dialog).toHaveCount(0);
      await expect(help).toBeFocused();
    }
  });
});
