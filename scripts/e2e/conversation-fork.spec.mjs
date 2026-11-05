import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";

// Real Chrome and the normal authenticated app shell. Only this graph's
// history/fork responses are deterministic UI fixtures; no model is invoked.
// The real SQLite/HTTP split and prompt contexts have separate backend tests.
for (const width of [1280, 390]) test.describe(`conversation split ${width}`, () => {
  test.use({ viewport: { width, height: width === 390 ? 844 : 820 }, hasTouch: width === 390 });
  test("分叉保留完整续聊、两个子分支可写且继承尺寸；框外滚轮缩放、框内只滚动", { tag: ["@smoke", "@conversation-fork"] }, async ({ page, request, baseURL }, info) => {
    const headers = { Origin: baseURL, "Content-Type": "application/json" };
    async function api(path, data) {
      const response = await request.fetch(`/v1${path}`, { method: data === undefined ? "GET" : "POST", headers, data });
      expect(response.ok(), `${path}: ${response.status()}`).toBeTruthy();
      return (await response.json()).data;
    }
    headers["X-CSRF-Token"] = (await api("/auth/local-session", {})).csrf_token;
    const computer = (await api("/computers")).items[0];
    const service = (await api(`/computers/${computer.id}/agent-services`)).items.find((item) => item.status === "ready");
    test.skip(!service, "没有可配置 CLI；不伪造模型可用性");
    const suffix = randomUUID().slice(0, 8);
    const agent = await api(`/computers/${computer.id}/agents`, { name: `分叉UI-${suffix}`, agent_service_id: service.id, model: "" });
    const project = await api("/projects", { computer_id: computer.id, name: `分叉UI-${suffix}` });
    const initial = await api(`/projects/${project.id}/conversation-graphs`, { title: `分叉图-${suffix}`, agent_id: agent.id, node_title: "源对话" });
    const source = { ...initial.nodes[0], width: 540.5, height: 710.25, position_x: 40, position_y: 80, locked: false,
      inherited_messages: [{ author: "user", body: "祖先继承上下文" }], message_count: 4 };
    const graph = { ...initial.graph, node_count: 1 };
    const stamp = "2026-10-28T00:00:00Z";
    const message = (id, author_kind, body) => ({ id, version: 1, created_at: stamp, updated_at: stamp, node_id: source.id, author_kind, body, status: "published" });
    const history = [
      message("fixture-user-before", "user", "分叉前的用户问题"),
      message("fixture-agent-boundary", "agent", "选中的分叉点回复"),
      message("fixture-user-after", "user", "分叉点之后的用户追问"),
      message("fixture-agent-after", "agent", "完整续聊必须保留的后续回复\n\n" + Array.from({ length: 40 }, (_, index) => `后续历史第 ${index + 1} 段：用于真实原生滚动验收。`).join("\n\n"))
    ];
    const parent = { ...source, id: "fixture-frozen-prefix", locked: true, message_count: 2 };
    const gap = source.height / 2 + 40, childX = source.position_x + source.width + 80;
    const continuation = { ...source, version: source.version + 1, parent_id: parent.id, fork_message_id: history[1].id, position_x: childX, position_y: source.position_y - gap };
    const branch = { ...source, id: "fixture-prefix-branch", parent_id: parent.id, fork_message_id: history[1].id, title: "新对话",
      position_x: childX, position_y: source.position_y + gap, message_count: 0,
      inherited_messages: [...source.inherited_messages, ...history.slice(0, 2).map((item) => ({ author: item.author_kind, body: item.body }))] };
    let forked = false;
    const forbidden = [], forkRequests = [];
    const reply = (route, data) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
    await page.addInitScript((selection) => sessionStorage.setItem("teamva-selection", JSON.stringify(selection)), { computerId: computer.id, projectId: project.id });
    await page.route("**/v1/work-records/navigation", (route) => route.fulfill({ status: 204 }));
    await page.route(`**/v1/projects/${project.id}/conversation-graphs`, (route) => reply(route, { items: [{ ...graph, node_count: forked ? 3 : 1 }] }));
    await page.route(`**/v1/conversation-graphs/${graph.id}`, (route) => reply(route, {
      graph: { ...graph, node_count: forked ? 3 : 1 }, nodes: forked ? [parent, continuation, branch] : [source],
      messages: forked ? [...history, ...history.slice(0, 2).map((item) => ({ ...item, id: `parent-${item.id}`, node_id: parent.id }))] : history, goals: []
    }));
    await page.route(`**/v1/conversation-nodes/${source.id}/fork`, async (route) => {
      forkRequests.push(route.request().postDataJSON());
      forked = true;
      await reply(route, branch);
    });
    await page.route(/\/v1\/(?:conversation-nodes\/[^/]+\/(?:messages|goals)|runs)(?:[/?]|$)/, async (route) => {
      if (route.request().method() !== "GET") { forbidden.push(route.request().url()); await route.abort("blockedbyclient"); }
      else await route.fallback();
    });
    await page.goto(`/conversations?graph=${graph.id}`);
    const canvas = page.getByRole("application", { name: "对话画布", exact: true });
    const sourceCard = page.locator(`[data-node-id="${source.id}"]`);
    const scroller = sourceCard.locator("[data-conversation-message-scroll]");
    await expect(sourceCard.getByText("完整续聊必须保留的后续回复", { exact: false })).toBeVisible();
    const camera = () => canvas.evaluate((element) => {
      const matrix = new DOMMatrix(getComputedStyle(element.firstElementChild).transform);
      return { x: matrix.e, y: matrix.f, scale: matrix.a };
    });
    async function settledCamera() {
      let previous, stable = 0, current;
      await expect.poll(async () => {
        current = await camera();
        const signature = JSON.stringify(current);
        stable = signature === previous ? stable + 1 : 0;
        previous = signature;
        return stable;
      }, { message: "缩放 RAF 必须在有界时间内结束，不能在首帧就采样成最终相机", timeout: 2_000, intervals: [30] }).toBeGreaterThanOrEqual(3);
      return current;
    }
    if (width > 767) {
      const box = await canvas.boundingBox(), before = await camera(), px = 20, py = 20;
      await page.mouse.move(box.x + px, box.y + py);
      await page.mouse.wheel(0, -10);
      await expect.poll(async () => (await camera()).scale).toBeGreaterThan(before.scale);
      const after = await settledCamera();
      expect((px - after.x) / after.scale).toBeCloseTo((px - before.x) / before.scale, 3);
      expect((py - after.y) / after.scale).toBeCloseTo((py - before.y) / before.scale, 3);
    }
    const cameraBeforeScroll = await settledCamera();
    await scroller.evaluate(async (element) => { await new Promise((resolve) => requestAnimationFrame(resolve)); element.scrollTop = 0; });
    await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(0);
    const scrollBox = await scroller.boundingBox();
    await page.mouse.move(scrollBox.x + scrollBox.width / 2, scrollBox.y + 40);
    await page.mouse.wheel(0, 140);
    await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    expect(await settledCamera()).toEqual(cameraBeforeScroll);
    await scroller.evaluate((element) => { element.scrollTop = element.scrollHeight; });
    await page.mouse.wheel(0, 140);
    expect(await settledCamera()).toEqual(cameraBeforeScroll);
    await sourceCard.getByRole("button", { name: "分叉", exact: true }).first().click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("textbox", { name: "新对话分支标题", exact: true })).toHaveCount(0);
    await expect(canvas.locator(".conversation-node")).toHaveCount(3);
    expect(forkRequests).toEqual([{ message_id: history[1].id }]);
    const frozen = page.locator(`[data-node-id="${parent.id}"]`), alternative = page.locator(`[data-node-id="${branch.id}"]`);
    for (const card of [frozen, sourceCard, alternative]) {
      await expect(card.getByText("祖先继承上下文", { exact: true })).toHaveCount(1);
      await expect(card.getByText("选中的分叉点回复", { exact: true })).toHaveCount(1);
      expect(await card.evaluate((element) => [parseFloat(element.style.width), parseFloat(element.style.height)])).toEqual([source.width, source.height]);
    }
    await expect(frozen.getByRole("textbox", { name: "消息输入", exact: true })).toHaveCount(0);
    await expect(frozen.getByRole("combobox", { name: "Agent", exact: true })).toHaveCount(0);
    await expect(frozen.getByText("分叉点之后的用户追问", { exact: true })).toHaveCount(0);
    await expect(alternative.getByText("分叉点之后的用户追问", { exact: true })).toHaveCount(0);
    await expect(sourceCard.getByText("分叉点之后的用户追问", { exact: true })).toHaveCount(1);
    await expect(sourceCard.locator(`#message-${history[3].id}`)).toHaveCount(1);
    for (const card of [sourceCard, alternative]) {
      await card.getByRole("textbox", { name: "消息输入", exact: true }).fill("两个子分支都可以继续");
      await expect(card.getByRole("button", { name: "发送", exact: true })).toBeEnabled();
      await card.getByRole("textbox", { name: "消息输入", exact: true }).fill("");
    }
    if (width > 767) {
      const scale = (await settledCamera()).scale;
      for (const card of [frozen, sourceCard, alternative]) {
        const box = await card.boundingBox();
        expect(Math.abs(box.width / scale - source.width)).toBeLessThanOrEqual(1);
        expect(Math.abs(box.height / scale - source.height)).toBeLessThanOrEqual(1);
      }
    }
    await page.reload();
    await expect(sourceCard.getByText("分叉点之后的用户追问", { exact: true })).toHaveCount(1);
    await expect(sourceCard.getByRole("textbox", { name: "消息输入", exact: true })).toBeEnabled();
    await expect(alternative.getByRole("textbox", { name: "消息输入", exact: true })).toBeEnabled();
    expect(forbidden).toEqual([]);
    await page.screenshot({ path: info.outputPath(`conversation-split-${width}.png`) });
  });
});
