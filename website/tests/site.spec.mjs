import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

const labels = {
  en: { title: "One superpower.", selector: "Workspace views", tasks: "Teamwork", intervention: "Step in", graph: "Graph chats", gallery: "Workspace screenshots", open: "Open navigation", why: "Why Teamva" },
  zh: { title: "协作成一支团队。", selector: "工作台视图", tasks: "多 Agent 协作", intervention: "随时介入", graph: "图对话", gallery: "工作台截图导览", open: "打开导航", why: "为什么选 Teamva" }
};
const screenIDs = ["tasks", "graph", "conversation", "resources", "agents", "skills", "mcp", "memory"];

const heroScale = (page) => page.locator(".hero-view-progress-fill").evaluate((el) => new DOMMatrixReadOnly(getComputedStyle(el).transform).a);
const heroTime = (page) => page.locator(".hero-view-progress-fill").evaluate(async (el) => {
  const animation = el.getAnimations()[0];
  await animation.ready;
  return animation.currentTime;
});
async function finishHeroCycle(page) {
  await page.locator(".hero-view-progress-fill").evaluate((el) => {
    const animation = el.getAnimations()[0];
    animation.currentTime = animation.effect.getTiming().duration - 80;
    animation.updatePlaybackRate(10);
  });
}

for (const language of ["en", "zh"]) for (const width of [1440, 390]) {
  test(`product tour ${language} ${width}: website controls stay outside the application screenshot`, async ({ page }, info) => {
    const t = labels[language];
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width, height: 1000 });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`/?lang=${language}`);
    await expect(page.getByRole("heading", { level: 1 })).toContainText(t.title);
    await expect.poll(() => page.locator(".landing-nav .brand-mark__symbol").evaluate((image) => image.complete && image.naturalWidth)).toBe(256);
    await expect(page.locator('link[rel="icon"]')).toHaveJSProperty("href", new URL("/favicon.png", page.url()).href);
    const favicon = await page.request.get("/favicon.png");
    expect(favicon.ok()).toBeTruthy();
    expect((await favicon.body()).equals(readFileSync(new URL("../public/favicon.png", import.meta.url)))).toBe(true);
    await expect(page.locator(".landing")).toHaveAttribute("data-motion", "off");
    await expect(page.locator(".motion-control, .motion-toggle, .hero-rotation-help, .preview-caption, .preview-heading p, .engine-caveat")).toHaveCount(0);
    const selector = page.getByRole("group", { name: t.selector, exact: true });
    await expect(selector).toBeVisible();
    await expect(selector.getByRole("button")).toHaveCount(3);
    await expect(page.locator(".hero-console button, .traffic-lights")).toHaveCount(0);
    const selectorBox = await selector.boundingBox(), frameBox = await page.locator(".hero-console").boundingBox();
    expect(selectorBox.y + selectorBox.height).toBeLessThan(frameBox.y - 10);
    const progressBox = await page.locator(".hero-view-progress").boundingBox();
    const activeTabBox = await selector.locator('button[aria-pressed="true"]').boundingBox();
    await expect(selector.locator('button[aria-pressed="true"] .hero-view-progress')).toHaveCount(1);
    await expect(page.locator(".hero-view-progress")).toHaveCount(1);
    await expect(page.locator(".hero-view-switch + .hero-view-progress")).toHaveCount(0);
    expect(progressBox.height).toBeGreaterThanOrEqual(3);
    expect(progressBox.x).toBeCloseTo(activeTabBox.x, 1);
    expect(progressBox.width).toBeCloseTo(activeTabBox.width, 1);
    expect(progressBox.y + progressBox.height).toBeCloseTo(activeTabBox.y + activeTabBox.height, 1);
    expect(progressBox.y + progressBox.height).toBeLessThan(frameBox.y - 10);
    for (const button of await selector.getByRole("button").all()) await expect(button).toHaveCSS("border-bottom-width", "0px");
    const hero = page.locator(".hero-image img");
    await expect.poll(() => hero.evaluate((img) => img.complete && img.naturalWidth)).toBe(3200);
    const imageBox = await hero.boundingBox(), imageFrame = await page.locator(".hero-image").boundingBox();
    expect(imageBox.width).toBeLessThanOrEqual(imageFrame.width + 1);
    const original = await hero.getAttribute("src");
    expect(original.includes("/screenshots/zh/")).toBe(language === "zh");
    expect(original).toContain("multitask");
    await selector.getByRole("button", { name: t.intervention, exact: true }).click();
    await expect(hero).toHaveAttribute("src", /agent-conversation/);
    await expect(hero).not.toHaveAttribute("src", original);
    await expect.poll(() => hero.evaluate((img) => img.complete && img.naturalWidth)).toBe(3200);
    await page.locator(".product-preview").screenshot({ path: info.outputPath(`intervention-${language}-${width}.png`) });
    await selector.getByRole("button", { name: t.graph, exact: true }).click();
    await expect(hero).toHaveAttribute("src", /graph/);
    await expect(hero).not.toHaveAttribute("src", original);
    await expect.poll(() => hero.evaluate((img) => img.complete && img.naturalWidth)).toBe(3200);
    await selector.getByRole("button", { name: t.tasks, exact: true }).click();
    await expect(hero).toHaveAttribute("src", original);
    await page.evaluate(() => scrollTo({ top: 0, behavior: "instant" }));
    await page.screenshot({ path: info.outputPath(`homepage-${language}-${width}.png`) });
    await page.locator(".product-preview").screenshot({ path: info.outputPath(`preview-${language}-${width}.png`) });

    // DeepSeek remains visible in the hero, and research precedes the engine logos/copy.
    await expect(page.locator(".era-badge")).toContainText(/deepseek/i);
    expect(await page.locator("#super-agent").evaluate((el) => Boolean(el.compareDocumentPosition(document.querySelector("#coding-tools")) & Node.DOCUMENT_POSITION_FOLLOWING))).toBe(true);
    await expect(page.locator(".engine-section")).toContainText(language === "zh" ? "自动检测" : "automatically detects");
    for (const selector of [".research-chip", ".era-copy > small", ".screenshot-note", ".footer-support"]) {
      expect(await page.locator(selector).evaluate((el) => parseFloat(getComputedStyle(el).fontSize)), selector).toBeGreaterThanOrEqual(12);
    }
    await expect(page.locator("#local-first")).toContainText(language === "zh" ? "本地" : /local/i);
    await expect(page.locator("#local-first")).toContainText(language === "zh" ? "隐私" : /privacy/i);
    await expect(page.locator("#local-first")).toContainText(language === "zh" ? "远程访问" : /remote access/i);
    await expect(page.locator("#local-first")).not.toContainText(/Svelte|Go HTTP|SSE|SQLite|Runtime|Provider-native|Docker|PostgreSQL/);
    const bodyText = await page.locator("body").innerText();
    expect(bodyText).not.toMatch(/点击切换下方截图|点击任一 Tab|自动轮播|不代表现在就已支持|switch screenshots below|click either tab|auto-switching/i);
    expect(bodyText).not.toMatch(language === "zh" ? /[\u3400-\u9fff][,:;!?]/ : /[，。！？；：]/);
    await page.locator("#super-agent").screenshot({ path: info.outputPath(`research-${language}-${width}.png`) });
    await page.locator("#coding-tools").screenshot({ path: info.outputPath(`coding-tools-${language}-${width}.png`) });
    await page.locator("#local-first").screenshot({ path: info.outputPath(`privacy-${language}-${width}.png`) });
    const stories = page.locator("#possibilities");
    await expect(stories.locator(".feature-bento, .feature-tile")).toHaveCount(0);
    await expect(stories.getByRole("heading", { level: 3 })).toHaveCount(3);
    const themes = language === "zh" ? ["并行 · 交接", "探索 · 记忆", "节奏 · 控制"] : ["PARALLEL / CONNECTED", "EXPLORE / REMEMBER", "REPEAT / STAY IN CONTROL"];
    for (const [index, chapter] of (await stories.locator(".work-chapter").all()).entries()) {
      await expect(chapter).toContainText(themes[index]);
      await expect(chapter).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
      await expect(chapter).toHaveCSS("border-radius", "0px");
      await expect(chapter).toHaveCSS("border-left-width", "0px");
      const copy = await chapter.locator(".work-chapter-copy").boundingBox();
      const art = await chapter.locator(".work-chapter-art").boundingBox();
      if (width < 600) expect(art.y).toBeGreaterThan(copy.y + copy.height);
      else if (index === 1) expect(art.x + art.width).toBeLessThan(copy.x);
      else expect(art.x).toBeGreaterThan(copy.x + copy.width);
      for (const label of await chapter.locator(".work-chapter-point span").all()) {
        const box = await label.boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(art.x - 1);
        expect(box.x + box.width).toBeLessThanOrEqual(art.x + art.width + 1);
      }
    }
    await stories.screenshot({ path: info.outputPath(`work-stories-${language}-${width}.png`), style: ".landing-nav, .skip-link { visibility: hidden !important; }" });

    const gallery = page.getByRole("group", { name: t.gallery, exact: true });
    for (const id of screenIDs) {
      const button = gallery.locator(`[data-screen="${id}"]`);
      await button.click();
      await expect(button).toHaveAttribute("aria-pressed", "true");
      await expect.poll(() => page.locator(".showcase-panel > img").evaluate((img) => img.complete && img.naturalWidth)).toBe(3200);
      expect((await page.locator(".showcase-panel > img").getAttribute("src")).includes("/screenshots/zh/")).toBe(language === "zh");
      expect(await page.locator(".showcase-caption > a").getAttribute("href")).toBe(await page.locator(".showcase-panel > img").getAttribute("src"));
    }
    await page.locator(".faq-layout summary").first().click();
    await expect(page.locator(".faq-layout details[open] p")).toBeVisible();
    if (width < 1000) {
      await page.getByRole("button", { name: t.open, exact: true }).click();
      await page.getByRole("navigation").getByRole("link", { name: t.why, exact: true }).click();
      await expect(page.getByRole("button", { name: t.open, exact: true })).toHaveAttribute("aria-expanded", "false");
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    for (const href of await page.locator('a[href^="#"]').evaluateAll((links) => [...new Set(links.map((a) => a.getAttribute("href")))])) {
      expect(await page.locator(href).count(), `Anchor target ${href}`).toBe(1);
    }
    expect(errors).toEqual([]);
  });
}

for (const [language, width] of [["en", 1440], ["zh", 390]]) {
  test(`hero carousel ${language} ${width}: the active underline fills and cycles all three screenshots`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/?lang=${language}`);
    const preview = page.locator(".product-preview");
    const tabs = page.getByRole("group", { name: labels[language].selector, exact: true }).getByRole("button");
    await tabs.first().scrollIntoViewIfNeeded();
    await expect(preview).toHaveAttribute("data-autoplay", "running");
    await expect(page.locator(".hero-rotation-help")).toHaveCount(0);
    expect(await page.locator(".hero-view-progress-fill").evaluate((el) => getComputedStyle(el).animationDuration)).toBe("7s");
    const initial = await heroScale(page);
    expect(initial).toBeGreaterThanOrEqual(.04);
    expect(initial).toBeLessThan(.5);
    await page.locator(".hero-view-progress-fill").evaluate((el) => el.getAnimations()[0].updatePlaybackRate(10));
    await expect.poll(() => heroScale(page)).toBeGreaterThan(initial + .05);
    const tasksImage = await page.locator(".hero-image img").getAttribute("src");
    for (const [next, file] of [[1, "agent-conversation"], [2, "graph"], [0, "multitask"]]) {
      await finishHeroCycle(page);
      await expect(tabs.nth(next)).toHaveAttribute("aria-pressed", "true");
      await expect(tabs.nth(next).locator(".hero-view-progress-fill")).toHaveCount(1);
      await expect(page.locator(".hero-image img")).toHaveAttribute("src", new RegExp(file));
      expect(await heroScale(page)).toBeLessThan(.15);
    }
    await expect(page.locator(".hero-image img")).toHaveAttribute("src", tasksImage);
  });
}

for (const tabIndex of [0, 1, 2]) {
  test(`hero carousel: selecting tab ${tabIndex + 1} permanently stops autoplay and leaves one solid underline`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto("/?lang=en");
    const preview = page.locator(".product-preview");
    const tabs = page.locator(".hero-view-switch button");
    await tabs.first().scrollIntoViewIfNeeded();
    await expect(preview).toHaveAttribute("data-autoplay", "running");
    await tabs.nth(tabIndex).click();
    await expect(preview).toHaveAttribute("data-autoplay", "stopped");
    await expect(page.locator(".hero-view-progress-fill")).toHaveCount(0);
    await expect(tabs.nth(tabIndex).locator(".hero-view-progress")).toHaveCSS("background-color", "rgb(194, 255, 117)");
    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(page.locator(".landing")).toHaveAttribute("data-motion", "off");
    await expect(preview).toHaveAttribute("data-autoplay", "stopped");
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await expect(page.locator(".landing")).toHaveAttribute("data-motion", "on");
    await page.getByRole("button", { name: "简体中文", exact: true }).click();
    await tabs.first().scrollIntoViewIfNeeded();
    await expect(preview).toHaveAttribute("data-autoplay", "stopped");
    await expect(tabs.nth(tabIndex)).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator(".hero-view-progress-fill")).toHaveCount(0);
    await expect(page.locator(".hero-rotation-help")).toHaveCount(0);
    // Keyboard activation uses the same permanent stop, without preventing later manual browsing.
    const next = (tabIndex + 1) % 3;
    await tabs.nth(next).focus();
    await page.keyboard.press("Enter");
    await expect(tabs.nth(next)).toHaveAttribute("aria-pressed", "true");
    await expect(preview).toHaveAttribute("data-autoplay", "stopped");
  });
}

test("hero carousel: system preferences and offscreen pauses preserve progress automatically", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?lang=en");
  const preview = page.locator(".product-preview");
  const fill = page.locator(".hero-view-progress-fill");
  const tabs = page.locator(".hero-view-switch button");
  await tabs.first().scrollIntoViewIfNeeded();
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await fill.evaluate((el) => { el.getAnimations()[0].currentTime = 2800; });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(preview).toHaveAttribute("data-autoplay", "paused");
  const paused = await heroTime(page);
  expect(paused).toBeGreaterThanOrEqual(2800);
  // Browser frames continue, but the same CSS animation remains frozen.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await heroTime(page)).toBe(paused);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await expect.poll(() => heroTime(page)).toBeGreaterThan(paused);
  await page.locator("#start").scrollIntoViewIfNeeded();
  await expect(preview).toHaveAttribute("data-autoplay", "paused");
  const offscreen = await heroTime(page);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await heroTime(page)).toBe(offscreen);
  await tabs.first().scrollIntoViewIfNeeded();
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(preview).toHaveAttribute("data-autoplay", "paused");
  await expect(fill).toHaveCSS("animation-play-state", "paused");
  const reduced = await heroTime(page);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await heroTime(page)).toBe(reduced);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await finishHeroCycle(page);
  await expect(tabs.nth(1)).toHaveAttribute("aria-pressed", "true");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.reload();
  await expect(preview).toHaveAttribute("data-autoplay", "paused");
  await expect(tabs.first()).toHaveAttribute("aria-pressed", "true");
});

test("hero carousel: background visibility events pause progress without a missed slide", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?lang=en");
  const preview = page.locator(".product-preview");
  await page.locator(".hero-view-switch").scrollIntoViewIfNeeded();
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", { configurable: true, value: true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(preview).toHaveAttribute("data-autoplay", "paused");
  const hidden = await heroTime(page);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await heroTime(page)).toBe(hidden);
  await expect(page.locator(".hero-view-switch button").first()).toHaveAttribute("aria-pressed", "true");
  await page.evaluate(() => {
    delete document.hidden;
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(preview).toHaveAttribute("data-autoplay", "running");
  await finishHeroCycle(page);
  await expect(page.locator(".hero-view-switch button").nth(1)).toHaveAttribute("aria-pressed", "true");
});

test("language switching covers content, metadata, selected gallery, reloads and documentation", async ({ page }) => {
  await page.goto("/?lang=en");
  const languages = page.getByRole("group", { name: "Website language" });
  await expect(languages).toBeInViewport();
  await page.locator(".hero-view-switch button").first().click();
  const englishHero = await page.locator(".hero-image img").getAttribute("src");
  const englishGallery = [];
  for (const id of screenIDs) {
    await page.locator(`.product-tabs [data-screen="${id}"]`).click();
    englishGallery.push(await page.locator(".showcase-panel > img").getAttribute("src"));
  }
  await page.locator('.product-tabs [data-screen="mcp"]').click();
  await languages.getByRole("button", { name: "简体中文", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page).toHaveTitle("Teamva — 让你的 Agents，协作成一支团队。");
  await expect(page.locator('meta[name="description"]')).toHaveCount(1);
  await expect(page.locator('meta[name="description"]')).toHaveAttribute("content", /自动检测/);
  await expect(page.locator('.product-tabs [data-screen="mcp"]')).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".hero-image img")).not.toHaveAttribute("src", englishHero);
  for (const [index, id] of screenIDs.entries()) {
    await page.locator(`.product-tabs [data-screen="${id}"]`).click();
    await expect(page.locator(".showcase-panel > img")).not.toHaveAttribute("src", englishGallery[index]);
    await expect.poll(() => page.locator(".showcase-panel > img").evaluate((img) => img.complete && img.naturalWidth)).toBe(3200);
  }
  await expect(page.getByRole("button", { name: "复制浏览器快速启动命令" })).toBeAttached();
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 })).toContainText(labels.zh.title);
  await page.goto("/"); // Stored preference is used when no shareable language is specified.
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await page.getByRole("link", { name: "阅读配置指南", exact: true }).click();
  await expect(page).toHaveURL(/\/docs\/\?lang=zh$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("组织团队，看清工作。");
  for (const href of await page.locator(".docs-sidebar nav a").evaluateAll((links) => links.map((a) => a.getAttribute("href")))) {
    await expect(page.locator(href)).toHaveCount(1);
  }
  await page.locator('.docs-sidebar a[href="#providers"]').click();
  await expect(page).toHaveURL(/lang=zh#providers$/);
  await page.getByRole("button", { name: "English", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("heading", { level: 1 })).not.toContainText(/[\u3400-\u9fff]/);
  await expect(page.locator(".site-footer__bottom")).toContainText(/more/i);
  await expect(page.locator(".site-footer__bottom")).not.toContainText("Built for Codex + OpenCode on macOS");
});

test("page effects run automatically and respect system preferences without a site control", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.goto("/?lang=en");
  await expect(page.locator(".motion-control, .motion-toggle")).toHaveCount(0);
  const orbit = page.locator(".orbital i").first();
  const before = await orbit.evaluate((el) => getComputedStyle(el).transform);
  await expect.poll(() => orbit.evaluate((el) => getComputedStyle(el).transform)).not.toBe(before);
  const workflowLine = page.locator(".work-chapter-flow").first();
  const lineBefore = await workflowLine.evaluate((el) => getComputedStyle(el).strokeDashoffset);
  await expect.poll(() => workflowLine.evaluate((el) => getComputedStyle(el).strokeDashoffset)).not.toBe(lineBefore);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(page.locator(".landing")).toHaveAttribute("data-motion", "off");
  await expect.poll(() => page.locator(".landing").evaluate((el) => el.getAnimations({ subtree: true }).filter((a) => a.playState === "running").length)).toBe(0);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(page.locator(".landing")).toHaveAttribute("data-motion", "on");
  await expect.poll(() => orbit.evaluate((el) => getComputedStyle(el).transform)).not.toBe("none");
});

for (const language of ["en", "zh"]) for (const width of [1440, 390]) {
  test(`documentation ${language} ${width}: readable layout and working section navigation`, async ({ page }, info) => {
    const zh = language === "zh";
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/docs/?lang=${language}`);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect.poll(() => page.locator(".site-header .brand-mark__symbol").evaluate((image) => image.complete && image.naturalWidth)).toBe(256);
    await expect(page.getByRole("button", { name: zh ? "简体中文" : "English", exact: true })).toBeInViewport();
    await page.screenshot({ path: info.outputPath(`docs-${language}-${width}.png`) });
    if (width < 1000) await page.locator(".docs-mobile-bar button").click();
    await page.locator('.docs-sidebar a[href="#providers"]').click();
    await expect(page).toHaveURL(new RegExp(`lang=${language}#providers$`));
    await expect(page.locator("#providers h2")).toBeInViewport();
    expect(await page.locator("#providers > p").first().evaluate((el) => parseFloat(getComputedStyle(el).fontSize))).toBeGreaterThanOrEqual(16);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width < 1000) await expect(page.locator(".docs-sidebar")).not.toHaveClass(/docs-sidebar--open/);
    for (const href of await page.locator(".docs-sidebar nav a").evaluateAll((links) => links.map((a) => a.getAttribute("href")))) {
      await expect(page.locator(href)).toHaveCount(1);
    }
  });
}

test("narrow and tablet layouts keep both language choices visible without clipping", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const width of [320, 768, 1024]) for (const language of ["en", "zh"]) {
    await page.setViewportSize({ width, height: 950 });
    await page.goto(`/?lang=${language}`);
    const languages = page.getByRole("group", { name: "Website language" });
    await expect(languages.getByRole("button", { name: "English", exact: true })).toBeInViewport();
    await expect(languages.getByRole("button", { name: "简体中文", exact: true })).toBeInViewport();
    const box = await languages.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    const logo = await page.locator(".landing-nav .wordmark").boundingBox();
    expect(logo.x + logo.width).toBeLessThanOrEqual(box.x - 5);
    for (const chapter of await page.locator(".work-chapter").all()) {
      const copy = await chapter.locator(".work-chapter-copy").boundingBox();
      const art = await chapter.locator(".work-chapter-art").boundingBox();
      expect(copy.x).toBeGreaterThanOrEqual(0);
      expect(copy.x + copy.width).toBeLessThanOrEqual(width);
      if (width < 600) expect(art.y).toBeGreaterThan(copy.y + copy.height);
      for (const label of await chapter.locator(".work-chapter-point span").all()) {
        const labelBox = await label.boundingBox();
        expect(labelBox.x).toBeGreaterThanOrEqual(art.x - 1);
        expect(labelBox.x + labelBox.width).toBeLessThanOrEqual(art.x + art.width + 1);
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
});

test("reduced motion, keyboard access, clipboard and real docs route", async ({ page, context }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/?lang=en");
  await expect(page.locator(".landing")).toHaveAttribute("data-motion", "off");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to content", exact: true })).toBeFocused();
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy browser quick-start command" }).click();
  await expect(page.getByRole("status")).toHaveText("Copied. Run in your Teamva source checkout.");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("pnpm install && pnpm build && pnpm start:browser");
  await page.getByRole("link", { name: "Read the setup guide", exact: true }).click();
  await expect(page).toHaveURL(/\/docs\/\?lang=en$/);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
});

test("clipboard failure leaves a selectable command and an honest status", async ({ page }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "clipboard", { value: { writeText: async () => { throw new Error("denied"); } } }));
  await page.goto("/?lang=en");
  await page.getByRole("button", { name: "Copy browser quick-start command" }).click();
  await expect(page.getByRole("status")).toHaveText("Clipboard unavailable. Select and copy the command above.");
  await expect(page.locator(".install-command code")).toBeVisible();
});
