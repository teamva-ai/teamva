import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// Real isolated HTTP and Chrome; no external site, Agent or model is required.
// Successful HTML title extraction is covered by the server's controlled-network
// tests. Here a blocked loopback URL verifies the actual browser/API fallback.
// Opt in to a real public-site browser check without making CI depend on it.
for (const width of [1280, 390]) test.describe(`URL resource names ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("网址名称可留空、占位提示明确，手填名称优先且刷新保留", { tag: ["@smoke", "@resource-url-name"] }, async ({ page, request, baseURL }, info) => {
    const headers = { Origin: baseURL, "Content-Type": "application/json" };
    const api = async (path, data) => {
      const response = await request.fetch(`/v1${path}`, { method: data === undefined ? "GET" : "POST", headers, data });
      expect(response.ok(), `${path}: ${response.status()}`).toBe(true);
      return (await response.json()).data;
    };
    headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
    const computer = (await api("/computers")).items[0];
    const project = await api("/projects", { computer_id: computer.id, name: `网址名称验收-${randomUUID().slice(0, 8)}` });
    await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)),
      { computerId: computer.id, projectId: project.id });
    const forbidden = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && /\/v1\/(?:runs|[^?]*\/(?:messages|goals))(?:[/?]|$)/.test(new URL(request.url()).pathname)) forbidden.push(request.url());
    });
    await page.goto("/resources");
    const saved = [];
    const cases = [
      ["  我手填的网站名称  ", "https://example.invalid/custom-name", "我手填的网站名称"],
      ["", "http://127.0.0.1:9/not-fetched", "http://127.0.0.1:9/not-fetched"]
    ];
    if (process.env.TEAMVA_RESOURCE_TITLE_PUBLIC_TEST === "1") cases.push(["", "https://example.com/", "Example Domain"]);
    for (const [name, url, expected] of cases) {
      await page.getByRole("button", { name: "添加资源", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "添加项目资源", exact: true });
      await dialog.getByRole("radio", { name: /^网址/ }).click();
      const nameInput = dialog.getByRole("textbox", { name: "显示名称", exact: true });
      const urlInput = dialog.getByRole("textbox", { name: "网址", exact: true });
      await expect(nameInput).not.toHaveAttribute("required");
      await expect(nameInput).toHaveAttribute("placeholder", "不填将默认使用读取到的网站名称");
      await expect(nameInput).toHaveAccessibleDescription("无法读取网站名称时，将使用网址作为显示名称。");
      await expect(urlInput).toHaveAttribute("placeholder", "网址");
      await expect(dialog.getByRole("button", { name: "添加", exact: true })).toBeDisabled();
      await urlInput.fill(url);
      await nameInput.fill(name);
      await expect(dialog.getByRole("button", { name: "添加", exact: true })).toBeEnabled();
      const geometry = await dialog.evaluate((element) => {
        const inputs = [...element.querySelectorAll("input")].map((input) => { const r = input.getBoundingClientRect(); return { left: r.left, right: r.right, height: r.height }; });
        const rect = element.getBoundingClientRect();
        return { inputs, left: rect.left, right: rect.right, viewport: innerWidth };
      });
      expect(geometry.left).toBeGreaterThanOrEqual(0);
      expect(geometry.right).toBeLessThanOrEqual(geometry.viewport);
      for (const input of geometry.inputs) {
        expect(input.left).toBeGreaterThanOrEqual(geometry.left);
        expect(input.right).toBeLessThanOrEqual(geometry.right);
        expect(input.height).toBeGreaterThanOrEqual(32);
      }
      if (!name) await page.screenshot({ path: info.outputPath(`resource-url-name-${width}.png`) });
      const responsePromise = page.waitForResponse((response) => response.request().method() === "POST" &&
        new URL(response.url()).pathname === `/v1/projects/${project.id}/resources`);
      await dialog.getByRole("button", { name: "添加", exact: true }).click();
      const response = await responsePromise;
      expect(response.status()).toBe(201);
      expect(response.request().postDataJSON()).toEqual({ kind: "url", name: name.trim(), url });
      const created = (await response.json()).data;
      expect(created.name).toBe(expected);
      expect(created.url).toBe(url);
      saved.push(created);
      await expect(dialog).toBeHidden();
      await expect(page.locator(`[data-resource-id="${created.id}"]:visible`)).toContainText(expected);
    }
    await page.reload();
    for (const item of saved) await expect(page.locator(`[data-resource-id="${item.id}"]:visible`)).toContainText(item.name);
    const persisted = (await api(`/projects/${project.id}/resources`)).items;
    for (const item of saved) expect(persisted.find((record) => record.id === item.id).name).toBe(item.name);
    await page.getByRole("button", { name: "添加资源", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "添加项目资源", exact: true });
    await dialog.getByRole("radio", { name: /^网址/ }).click();
    await dialog.getByRole("radio", { name: /^资料目录/ }).click();
    await expect(dialog.getByRole("textbox", { name: "显示名称", exact: true })).toHaveAttribute("required");
    await expect(dialog.getByText("无法读取网站名称时，将使用网址作为显示名称。", { exact: true })).toHaveCount(0);
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    expect(forbidden).toEqual([]);
  });
});
