import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const host = `/@fs${fileURLToPath(new URL("./fixtures/rich-text/index.html", import.meta.url))}`;

async function geometry(parent) {
  return parent.evaluate((container) => {
    const root = container.firstElementChild;
    const box = (node) => {
      const { left, right, top, bottom, width, height } = node.getBoundingClientRect();
      return { left, right, top, bottom, width, height };
    };
    const grids = [root, ...root.querySelectorAll("*")].filter((node) => getComputedStyle(node).display === "grid").map((node) => {
      const style = getComputedStyle(node);
      return { tag: node.tagName, box: box(node), clientWidth: node.clientWidth, scrollWidth: node.scrollWidth, columns: style.gridTemplateColumns, overflowX: style.overflowX };
    });
    const blocks = [...root.querySelectorAll("pre,blockquote,ul,ol")].map((node) => ({ tag: node.tagName, box: box(node), clientWidth: node.clientWidth, scrollWidth: node.scrollWidth, overflowX: getComputedStyle(node).overflowX }));
    const typography = [root, ...root.querySelectorAll("p,pre,blockquote,ol,ul,hr")].map((node) => {
      const style = getComputedStyle(node);
      return { tag: node.tagName, box: box(node), fontSize: style.fontSize, lineHeight: style.lineHeight, fontWeight: style.fontWeight, gap: style.gap, paddingLeft: style.paddingLeft, paddingTop: style.paddingTop, marginTop: style.marginTop, marginBottom: style.marginBottom };
    });
    return { parent: box(container), root: box(root), rootOverflowX: getComputedStyle(root).overflowX, grids, blocks, typography, text: root.textContent, tableCount: root.querySelectorAll("table").length, footnoteCount: root.querySelectorAll("sup").length };
  });
}

for (const viewport of [{ width: 1280, height: 820 }, { width: 800, height: 820 }, { width: 390, height: 844 }]) test.describe(`RichText ${viewport.width}`, () => {
  test.use({ viewport, hasTouch: viewport.width === 390 });
  for (const width of [146, 420]) for (const fixture of ["mixed", "nested", "normal"]) {
    test(`${fixture} parent ${width}: real border boxes and reachable final cell`, async ({ page }, info) => {
      const prohibited = [];
      await page.route("**/*", async (route) => {
        const request = route.request(), url = new URL(request.url());
        if (!["GET", "HEAD", "OPTIONS"].includes(request.method()) || url.pathname.startsWith("/v1/")) {
          prohibited.push({ method: request.method(), path: url.pathname });
          return route.abort("blockedbyclient");
        }
        return route.continue();
      });
      await page.goto(`${host}?fixture=${fixture}&width=${width}`);
      const parent = page.getByTestId("richtext-parent");
      await expect(parent).toBeVisible();
      await expect(parent).toContainText(fixture === "normal" ? "结论保持正常排版" : fixture === "nested" ? "多段脚注" : "脚注引用");
      await page.evaluate(async () => { await document.fonts.ready; });
      const before = await geometry(parent);
      const tableResults = [];
      for (const table of await parent.locator("table").all()) {
        await table.evaluate((node) => { node.parentElement.scrollLeft = node.parentElement.scrollWidth; });
        const lastCell = table.locator("tr").last().locator(":scope > :last-child");
        // Scroll only the viewport vertically. scrollIntoView() could also move
        // an overflow:hidden ancestor horizontally and conceal the old defect.
        await lastCell.evaluate((node) => window.scrollTo({ top: window.scrollY + node.getBoundingClientRect().top - 16, left: 0, behavior: "instant" }));
        tableResults.push(await table.evaluate((node) => {
          const box = (element) => { const { left, right, top, bottom, width, height } = element.getBoundingClientRect(); return { left, right, top, bottom, width, height }; };
          const owner = node.parentElement, root = document.querySelector('[data-testid="richtext-parent"] > div');
          const rootBox = box(root);
          let visibleLeft = Math.max(0, rootBox.left), visibleRight = Math.min(innerWidth, rootBox.right);
          const ancestors = [];
          for (let ancestor = owner; ancestor; ancestor = ancestor.parentElement) {
            const style = getComputedStyle(ancestor), rect = box(ancestor);
            ancestors.push({ tag: ancestor.tagName, box: rect, overflowX: style.overflowX, clientWidth: ancestor.clientWidth, scrollWidth: ancestor.scrollWidth });
            if (["hidden", "clip", "auto", "scroll"].includes(style.overflowX)) {
              visibleLeft = Math.max(visibleLeft, rect.left + ancestor.clientLeft);
              visibleRight = Math.min(visibleRight, rect.left + ancestor.clientLeft + ancestor.clientWidth);
            }
          }
          const lastHeader = node.rows[0].cells[node.rows[0].cells.length - 1];
          const lastBody = node.rows[node.rows.length - 1].cells[node.rows[node.rows.length - 1].cells.length - 1];
          const textRange = document.createRange();
          textRange.selectNodeContents(lastBody);
          const lastBodyTextRects = [...textRange.getClientRects()].map(({ left, right, top, bottom }) => ({ left, right, top, bottom }));
          return { owner: box(owner), scrollLeft: owner.scrollLeft, maxScroll: owner.scrollWidth - owner.clientWidth, lastHeader: box(lastHeader), lastBody: box(lastBody), lastBodyText: lastBody.textContent, lastBodyTextRects, visibleLeft, visibleRight, ancestors };
        }));
        await page.screenshot({ path: info.outputPath(`table-${tableResults.length}-maximum-right.png`), animations: "disabled" });
      }
      const evidence = { viewport, requestedParentWidth: width, fixture, before, tableResults, prohibitedRequests: prohibited };
      const path = info.outputPath("geometry.json");
      await writeFile(path, JSON.stringify(evidence, null, 2));
      await info.attach("real-border-boxes", { path, contentType: "application/json" });
      await page.evaluate(() => window.scrollTo({ top: 0, left: 0, behavior: "instant" }));
      await page.screenshot({ path: info.outputPath("component.png"), animations: "disabled" });
      if (await page.evaluate(() => document.documentElement.scrollHeight > innerHeight + 1)) {
        await page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, left: 0, behavior: "instant" }));
        await page.screenshot({ path: info.outputPath("component-bottom.png"), animations: "disabled" });
      }
      // These assertions deliberately precede any document-overflow check: a
      // clipping ancestor can hide a real Grid track defect from that shortcut.
      expect(before.parent.width).toBe(Math.min(width, viewport.width - 32));
      expect(before.root.width).toBe(before.parent.width);
      for (const grid of before.grids) {
        expect(grid.scrollWidth, `${grid.tag} grid content must shrink inside its real border box`).toBeLessThanOrEqual(grid.clientWidth + 1);
        expect(grid.box.right).toBeLessThanOrEqual(before.root.right + 1);
      }
      for (const block of before.blocks) {
        expect(block.box.right, `${block.tag} border box must remain inside RichText`).toBeLessThanOrEqual(before.root.right + 1);
        expect(block.scrollWidth, `${block.tag} long tokens must wrap, not be hidden`).toBeLessThanOrEqual(block.clientWidth + 1);
      }
      for (const result of tableResults) {
        expect(Math.abs(result.scrollLeft - result.maxScroll), "table must really be at its own maximum horizontal scroll").toBeLessThanOrEqual(1);
        expect(result.owner.right, "table scroll owner itself must fit its ancestors").toBeLessThanOrEqual(before.root.right + 1);
        for (const cell of [result.lastHeader, result.lastBody]) {
          expect(cell.left, "last column must actually be reachable after maximum scroll").toBeGreaterThanOrEqual(result.visibleLeft - 1);
          expect(cell.right, "last column must not be cut by a clipping ancestor").toBeLessThanOrEqual(result.visibleRight + 1);
        }
        expect(result.lastBodyTextRects.length, "last cell must contain actually rendered text").toBeGreaterThan(0);
        for (const text of result.lastBodyTextRects) {
          expect(text.left).toBeGreaterThanOrEqual(result.visibleLeft - 1);
          expect(text.right).toBeLessThanOrEqual(result.visibleRight + 1);
          expect(text.top).toBeGreaterThanOrEqual(-1);
          expect(text.bottom).toBeLessThanOrEqual(viewport.height + 1);
        }
      }
      expect(prohibited).toEqual([]);
    });
  }
});
