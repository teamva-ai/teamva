import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { location, navigate } from "../lib/router.svelte";
import type { Agent, ConversationGraph, ConversationGraphDetail } from "../lib/types";
import { deepLinkTranslations } from "../locales/deepLink";
import ConversationsTestHost from "./ConversationsTestHost.svelte";
import { tick } from "svelte";

const base = { version: 1, created_at: "2026-10-23T00:00:00Z", updated_at: "2026-10-23T00:00:00Z" };
const first: ConversationGraph = { ...base, id: "graph-first", project_id: "project-test", computer_id: "computer-test", title: "First graph", node_count: 1 };
const linked: ConversationGraph = { ...first, id: "graph-linked", title: "Linked graph" };
const foreign: ConversationGraph = { ...first, id: "graph-foreign", project_id: "project-other", title: "Foreign graph" };
const text = deepLinkTranslations["en-US"];

function detail(graph: ConversationGraph): ConversationGraphDetail {
  return { graph, nodes: [{ ...base, id: `${graph.id}-node`, graph_id: graph.id, agent_id: "agent-test",
    agent_revision_id: "revision-test", title: `${graph.title} node`, status: "idle", position_x: 40, position_y: 80, width: 380, height: 560,
    locked: false, message_count: 0 }], messages: [], goals: [] };
}
function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function pageCreateButton() {
  return within(screen.getByRole("heading", { name: t("conversations.title") }).closest("header")!)
    .getByRole("button", { name: t("conversations.newGraph") });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => { resolve = complete; });
  return { promise, resolve };
}
function setup(options: {
  list?: Promise<Response>;
  getDetail?: (id: string) => Response | Promise<Response>;
  create?: Promise<Response> | Response;
} = {}) {
  let items = [first, linked];
  let firstList = options.list;
  const requests = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), "http://teamva.test");
    if (url.pathname === "/v1/work-records/navigation") return envelope({});
    if (init?.method === "POST") {
      expect(url.pathname).toBe("/v1/projects/project-test/conversation-graphs");
      const result = options.create ?? envelope({ graph: { ...first, id: "created", title: "新对话", node_count: 0 }, nodes: [], messages: [], goals: [] });
      const created = await result;
      const data = (await created.clone().json()).data as ConversationGraphDetail;
      items = [data.graph, ...items];
      return created;
    }
    if (url.pathname === "/v1/projects/project-test/conversation-graphs") {
      if (firstList) { const pending = firstList; firstList = undefined; return pending; }
      return envelope({ items });
    }
    if (url.pathname === "/v1/projects/project-other/conversation-graphs") return envelope({ items: [foreign] });
    const match = /^\/v1\/conversation-graphs\/([^/]+)$/.exec(url.pathname);
    if (match) {
      if (options.getDetail) return options.getDetail(match[1]);
      const graph = [...items, foreign].find((graph) => graph.id === match[1])!;
      return envelope(graph.node_count ? detail(graph) : { graph, nodes: [], messages: [], goals: [] });
    }
    return envelope({ items: [] });
  });
  vi.stubGlobal("fetch", requests);
  return requests;
}
beforeEach(() => {
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
    matches: false, media: query,
    addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn()
  })));
});
afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("Conversation creation and Agent binding", () => {
  const agent: Agent = { ...base, id: "agent-test", computer_id: "computer-test", agent_service_id: "service-test",
    name: "Fixture Agent", status: "active", current_revision_id: "revision-test" };

  it("initializes an empty graph only after choosing a configured active Agent, retaining the choice on failure", async () => {
    let snapshot: ConversationGraphDetail = { graph: { ...first, node_count: 0 }, nodes: [], messages: [], goals: [] };
    const baseFetch = setup({ getDetail: () => envelope(snapshot) });
    const rootRequests: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith(`/conversation-graphs/${first.id}/root`) && init?.method === "POST") {
        rootRequests.push(JSON.parse(String(init.body)));
        if (rootRequests.length === 1) return new Response(JSON.stringify({ error: { code: "version_conflict" } }), { status: 409 });
        snapshot = detail(first);
        return envelope(snapshot.nodes[0]);
      }
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    render(ConversationsTestHost, { agentList: [agent, { ...agent, id: "unconfigured", current_revision_id: undefined }, { ...agent, id: "disabled", status: "disabled" }] });
    const draft = await screen.findByRole("article", { name: t("conversations.selectAgent") });
    const select = within(draft).getByRole("combobox", { name: t("conversations.agent") });
    expect([...select.querySelectorAll("option")].map((option) => option.value)).toEqual(["", agent.id]);
    expect(screen.queryByRole("textbox", { name: t("conversations.inputLabel") })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: t("issues.goal") })).not.toBeInTheDocument();
    const create = within(draft).getByRole("button", { name: t("conversations.create") });
    expect(create).toBeDisabled();
    await fireEvent.change(select, { target: { value: agent.id } });
    await fireEvent.click(create);
    await screen.findByRole("alert");
    expect(select).toHaveValue(agent.id);
    await waitFor(() => expect(create).toBeEnabled());
    await fireEvent.click(create);
    expect(await screen.findByRole("textbox", { name: t("conversations.inputLabel") })).toBeInTheDocument();
    expect(rootRequests).toEqual([{ agent_id: agent.id, expected_version: 1 }, { agent_id: agent.id, expected_version: 1 }]);
  });

  it("PATCHes the node Agent/version and sends message/Goal with the displayed binding without changing history", async () => {
    let snapshot = detail(first);
    snapshot.messages = [{ ...base, id: "historical", node_id: snapshot.nodes[0].id, author_kind: "user", status: "published", body: "historical input" }];
    const originalHistory = structuredClone(snapshot.messages);
    const other = { ...agent, id: "agent-other", name: "Second Agent", current_revision_id: "revision-other" };
    const baseFetch = setup({ getDetail: () => envelope(snapshot) });
    const writes: { path: string; body: Record<string, unknown>; headers?: HeadersInit }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (/\/conversation-nodes\//.test(path) && ["PATCH", "POST"].includes(init?.method ?? "")) {
        const body = JSON.parse(String(init?.body)); writes.push({ path, body, headers: init?.headers });
        if (init?.method === "PATCH") {
          snapshot = { ...snapshot, nodes: [{ ...snapshot.nodes[0], agent_id: other.id, agent_revision_id: other.current_revision_id, version: 2 }] };
          return envelope(snapshot.nodes[0]);
        }
        if (path.endsWith("/messages")) return envelope({ ...base, id: "sent", node_id: snapshot.nodes[0].id, author_kind: "user", body: body.body, status: "published" });
        return envelope({ ...base, id: "goal", node_id: snapshot.nodes[0].id, agent_revision_id: other.current_revision_id,
          objective: body.objective, status: "active", phase: "running", token_budget: body.token_budget, tokens_used: 0,
          work_runs: 0, run_count: 0, max_work_runs: 21, note: "", status_reason: "" });
      }
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    render(ConversationsTestHost, { agentList: [agent, other] });
    const field = await screen.findByRole("textbox", { name: t("conversations.inputLabel") });
    await fireEvent.input(field, { target: { value: "draft before switching" } });
    await fireEvent.change(screen.getByRole("combobox", { name: t("conversations.agent") }), { target: { value: other.id } });
    await waitFor(() => expect(screen.getByRole("combobox", { name: t("conversations.agent") })).toHaveValue(other.id));
    await waitFor(() => expect(screen.getByRole("button", { name: t("conversations.send") })).toBeEnabled());
    expect(field).toHaveValue("draft before switching");
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.send") }));
    await waitFor(() => expect(field).toHaveValue(""));
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    await fireEvent.input(field, { target: { value: "goal after switching" } });
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.goalStart") }));
    await waitFor(() => expect(writes).toHaveLength(3));
    expect(writes.map((write) => write.body)).toEqual([
      { expected_version: 1, agent_id: other.id },
      { body: "draft before switching", expected_agent_id: other.id, expected_agent_revision_id: "revision-other" },
      { objective: "goal after switching", token_budget: 2_000_000, expected_agent_id: other.id, expected_agent_revision_id: "revision-other" }
    ]);
    expect(snapshot.messages).toEqual(originalHistory);
    expect(screen.getByText("historical input")).toBeInTheDocument();
  });

  it("uses the latest graph version for explicit rename and preserves a rejected title draft", async () => {
    let snapshot = detail(first);
    const baseFetch = setup({ getDetail: () => envelope(snapshot) });
    const renames: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith(`/conversation-graphs/${first.id}`) && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)); renames.push(body);
        if (renames.length === 1) return new Response(JSON.stringify({ error: { code: "version_conflict" } }), { status: 409 });
        snapshot = { ...snapshot, graph: { ...snapshot.graph, title: body.title, version: 3 } };
        return envelope(snapshot.graph);
      }
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    render(ConversationsTestHost);
    await screen.findByText("First graph node");
    queryClient.setQueryData(["conversation-graphs", first.project_id], { items: [{ ...first, version: 2, title: "自动标题" }, linked] });
    await fireEvent.click(screen.getByRole("button", { name: `${t("common.edit")} · ${t("conversations.graphTitle")}` }));
    const field = screen.getByRole("textbox", { name: t("conversations.graphTitle") });
    expect(field).toHaveValue("自动标题");
    await fireEvent.input(field, { target: { value: "  用户标题  " } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await screen.findByRole("alert");
    expect(field).toHaveValue("  用户标题  ");
    expect(renames[0]).toEqual({ title: "用户标题", expected_version: 2 });
    await waitFor(() => expect(screen.getByRole("button", { name: t("common.save") })).toBeEnabled());
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(screen.queryByRole("textbox", { name: t("conversations.graphTitle") })).not.toBeInTheDocument());
    expect(screen.getByRole("heading", { name: "用户标题" })).toBeInTheDocument();
  });

  it.each(["running", "locked"])("PATCHes only title/version for a %s node", async (state) => {
    let snapshot = detail(first);
    snapshot.nodes[0] = { ...snapshot.nodes[0], locked: state === "locked", status: state === "running" ? "running" : "idle" };
    const baseFetch = setup({ getDetail: () => envelope(snapshot) });
    const patches: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith(`/conversation-nodes/${snapshot.nodes[0].id}`) && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body)); patches.push(body);
        snapshot = { ...snapshot, nodes: [{ ...snapshot.nodes[0], title: body.title, version: 2 }] };
        return envelope(snapshot.nodes[0]);
      }
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true }); render(ConversationsTestHost);
    await screen.findByText("First graph node");
    await fireEvent.click(screen.getByRole("button", { name: `${t("common.edit")} · ${t("conversations.forkNodeTitle")}` }));
    await fireEvent.input(screen.getByRole("textbox", { name: t("conversations.forkNodeTitle") }), { target: { value: "持续可改名" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    expect(await screen.findByText("持续可改名")).toBeInTheDocument();
    expect(patches).toEqual([{ expected_version: 1, title: "持续可改名" }]);
  });

  it("polls graph titles every two seconds without resetting the view or overwriting an open title draft", async () => {
    const interval = vi.spyOn(globalThis, "setInterval");
    let listed = first;
    const baseFetch = setup();
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith("/projects/project-test/conversation-graphs") && init?.method === "GET") return envelope({ items: [listed, linked] });
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true }); render(ConversationsTestHost);
    const canvas = await screen.findByRole("application"); await screen.findByText("First graph node");
    await fireEvent.keyDown(canvas, { key: "ArrowRight" });
    const before = (canvas.firstElementChild as HTMLElement).style.transform;
    const poll = interval.mock.calls.find((call) => call[1] === 2000)?.[0];
    expect(poll).toBeTypeOf("function");
    listed = { ...first, version: 2, title: "后台主会话标题" };
    (poll as () => void)();
    expect(await screen.findByRole("heading", { name: listed.title })).toBeInTheDocument();
    expect((canvas.firstElementChild as HTMLElement).style.transform).toBe(before);
    await fireEvent.click(screen.getByRole("button", { name: `${t("common.edit")} · ${t("conversations.graphTitle")}` }));
    const field = screen.getByRole("textbox", { name: t("conversations.graphTitle") });
    await fireEvent.input(field, { target: { value: "尚未保存的标题" } });
    listed = { ...listed, version: 3, title: "后续后台标题" };
    (poll as () => void)();
    await waitFor(() => expect(queryClient.getQueryData<{ items: ConversationGraph[] }>(["conversation-graphs", first.project_id])?.items[0].title).toBe(listed.title));
    expect(field).toHaveValue("尚未保存的标题");
    expect((canvas.firstElementChild as HTMLElement).style.transform).toBe(before);
  });
});

describe("Conversation wheel ownership", () => {
  let frames: Map<number, FrameRequestCallback>;
  beforeEach(() => {
    frames = new Map();
    let id = 0;
    vi.stubGlobal("requestAnimationFrame", vi.fn((callback: FrameRequestCallback) => { frames.set(++id, callback); return id; }));
    vi.stubGlobal("cancelAnimationFrame", vi.fn((id: number) => frames.delete(id)));
  });
  async function flushFrames() {
    let count = 0;
    while (frames.size && count++ < 200) { const queued = [...frames.values()]; frames.clear(); queued.forEach((callback) => callback(0)); await tick(); }
    expect(frames.size).toBe(0);
  }
  function viewTransform(canvas: HTMLElement) {
    const world = canvas.firstElementChild as HTMLElement;
    const values = /translate\(([-\d.e]+)px, ([-\d.e]+)px\) scale\(([-\d.e]+)\)/.exec(world.style.transform)!;
    return { x: Number(values[1]), y: Number(values[2]), scale: Number(values[3]) };
  }

  it("zooms on an unmodified canvas wheel and keeps the point under the cursor anchored", async () => {
    setup();
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("First graph node");
    vi.spyOn(canvas, "getBoundingClientRect").mockReturnValue({ left: 20, top: 30, width: 800, height: 600 } as DOMRect);
    const before = viewTransform(canvas);
    const wheel = new WheelEvent("wheel", { bubbles: true, cancelable: true, clientX: 120, clientY: 160, deltaY: -20 });
    await fireEvent(canvas, wheel);
    expect(viewTransform(canvas)).toEqual(before);
    await flushFrames();
    const after = viewTransform(canvas);
    expect(wheel.defaultPrevented).toBe(true);
    expect(after.scale).toBeCloseTo(before.scale * Math.exp(0.03));
    expect((100 - after.x) / after.scale).toBeCloseTo((100 - before.x) / before.scale);
    expect((130 - after.y) / after.scale).toBeCloseTo((130 - before.y) / before.scale);
  });

  it.each([{}, { ctrlKey: true }, { metaKey: true }])("never zooms the canvas for a wheel inside a card (%j)", async (modifier) => {
    setup();
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("First graph node");
    const world = canvas.firstElementChild as HTMLElement;
    const before = world.style.transform;
    const card = canvas.querySelector(".conversation-node")!;
    for (const target of [card.querySelector("header")!, card.querySelector("[data-conversation-message-scroll]")!, card.querySelector("textarea")!]) {
      const wheel = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 120, ...modifier });
      await fireEvent(target, wheel);
      expect(wheel.defaultPrevented).toBe(false);
      expect(world.style.transform).toBe(before);
      expect(frames.size).toBe(0);
    }
  });

  it.each([{ deltaY: 1, deltaMode: 1, expected: Math.exp(-0.024) }, { deltaY: 1, deltaMode: 2, expected: 1 / 1.1 },
    { deltaY: -10000, deltaMode: 0, expected: 1.1 }, { deltaY: 10000, deltaMode: 0, expected: 1 / 1.1 }])(
    "normalizes wheel units and bounds one event (%j)", async ({ deltaY, deltaMode, expected }) => {
      setup();
      navigate("/conversations", { replace: true });
      render(ConversationsTestHost);
      const canvas = await screen.findByRole("application");
      await screen.findByText("First graph node");
      vi.spyOn(canvas, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, width: 800, height: 600 } as DOMRect);
      await fireEvent(canvas, new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY, deltaMode }));
      await flushFrames();
      expect(viewTransform(canvas).scale).toBeCloseTo(expected);
    }
  );

  it("leaves wheel input to the stacked mobile page", async () => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: query === "(max-width: 767px)", media: query,
      addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn() })));
    setup();
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("First graph node");
    const before = (canvas.firstElementChild as HTMLElement).style.transform;
    const wheel = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 120 });
    await fireEvent(canvas, wheel);
    expect(wheel.defaultPrevented).toBe(false);
    expect((canvas.firstElementChild as HTMLElement).style.transform).toBe(before);
    expect(frames.size).toBe(0);
  });

  it.each(["reset", "keyboard-pan", "pointer-pan", "card-scroll", "switch", "destroy"])("cancels pending zoom when camera ownership changes: %s", async (action) => {
    setup(); navigate(`/conversations?graph=${first.id}`, { replace: true });
    const view = render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("First graph node");
    vi.spyOn(canvas, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, width: 800, height: 600 } as DOMRect);
    await fireEvent(canvas, new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: -120 }));
    expect(frames.size).toBe(1);
    if (action === "reset") await fireEvent.click(screen.getByRole("button", { name: t("conversations.resetView") }));
    if (action === "keyboard-pan") await fireEvent.keyDown(canvas, { key: "ArrowRight" });
    if (action === "pointer-pan") await fireEvent.pointerDown(canvas, { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
    if (action === "card-scroll") await fireEvent(canvas.querySelector("textarea")!, new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 120 }));
    if (action === "switch") { await fireEvent.click(screen.getByRole("button", { name: /Linked graph/ })); await screen.findByText("Linked graph node"); }
    if (action === "destroy") view.unmount();
    expect(frames.size).toBe(0);
    const before = (canvas.firstElementChild as HTMLElement | null)?.style.transform;
    await flushFrames();
    expect((canvas.firstElementChild as HTMLElement | null)?.style.transform).toBe(before);
  });
});

describe("Conversation split history presentation", () => {
  it("shows a frozen prefix parent and two writable children, retaining the full continuation and inherited dimensions", async () => {
    const original = detail(first);
    const source = { ...original.nodes[0], width: 680, height: 840, message_count: 4,
      inherited_messages: [{ author: "user" as const, body: "祖先上下文" }] };
    original.nodes = [source];
    original.messages = [
      { ...base, id: "u1", node_id: source.id, author_kind: "user", status: "published", body: "分叉前的问题" },
      { ...base, id: "a1", node_id: source.id, author_kind: "agent", status: "published", body: "选中的分叉点回复" },
      { ...base, id: "u2", node_id: source.id, author_kind: "user", status: "published", body: "分叉点之后的用户追问" },
      { ...base, id: "a2", node_id: source.id, author_kind: "agent", status: "published", body: "原对话后半段的回复" }
    ];
    const parent = { ...source, id: "prefix-parent", locked: true, message_count: 2 };
    const continuation = { ...source, parent_id: parent.id, fork_message_id: "a1", position_x: 800, position_y: -380 };
    const branch = { ...source, id: "new-route", parent_id: parent.id, fork_message_id: "a1", title: "新路线",
      position_x: 800, position_y: 540, message_count: 0, inherited_messages: [
        ...source.inherited_messages, { author: "user" as const, body: "分叉前的问题" }, { author: "agent" as const, body: "选中的分叉点回复" }
      ] };
    const split: ConversationGraphDetail = { ...original, nodes: [parent, continuation, branch], messages: [
      ...original.messages, ...original.messages.slice(0, 2).map((message) => ({ ...message, id: `parent-${message.id}`, node_id: parent.id }))
    ] };
    let forked = false;
    const baseFetch = setup({ getDetail: () => envelope(forked ? split : original) });
    const forkRequests: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith(`/conversation-nodes/${source.id}/fork`) && init?.method === "POST") {
        forkRequests.push(JSON.parse(String(init.body)));
        forked = true;
        return envelope(branch);
      }
      return baseFetch(input, init);
    }));
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    render(ConversationsTestHost);
    const originalCard = (await screen.findByText("原对话后半段的回复")).closest(".conversation-node")!;
    await fireEvent.click(within(originalCard as HTMLElement).getAllByRole("button", { name: t("conversations.fork") })[0]);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(document.querySelector(`[data-node-id="${branch.id}"]`)).toBeInTheDocument());
    expect(forkRequests).toEqual([{ message_id: "a1" }]);
    for (const node of [parent, continuation, branch]) {
      const card = document.querySelector(`[data-node-id="${node.id}"]`) as HTMLElement;
      expect(card).toHaveStyle({ width: "680px", height: "840px" });
      expect(within(card).getAllByText("祖先上下文")).toHaveLength(1);
      expect(within(card).getAllByText("选中的分叉点回复")).toHaveLength(1);
      if (node.id === parent.id) {
        expect(within(card).queryByRole("textbox", { name: t("conversations.inputLabel") })).not.toBeInTheDocument();
        expect(within(card).getByText(t("conversations.nodeLocked"))).toBeInTheDocument();
      } else {
        const input = within(card).getByRole("textbox", { name: t("conversations.inputLabel") });
        await fireEvent.input(input, { target: { value: "可以继续对话" } });
        expect(within(card).getByRole("button", { name: t("conversations.send") })).toBeEnabled();
      }
      expect(within(card).queryByText("原对话后半段的回复") !== null).toBe(node.id === continuation.id);
    }
    expect(document.getElementById("message-a2")?.closest(".conversation-node")).toHaveAttribute("data-node-id", source.id);
  });
});

describe("Conversation deep-link selection safety", () => {
  it.each([false, true])("creates an empty graph directly from the sidebar plus without an Agent/title dialog (empty=%s)", async (empty) => {
    const requests = setup(empty ? { list: Promise.resolve(envelope({ items: [] })) } : {});
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    const sidebar = await screen.findByRole("complementary", { name: t("conversations.rootNodes") });
    const header = sidebar.querySelector("header")!;
    expect(header.textContent?.trim()).toBe(t("conversations.rootNodes"));
    const plus = within(header).getByRole("button", { name: t("conversations.newGraph") });
    expect(plus).toHaveAttribute("type", "button");
    expect(plus).toHaveAttribute("data-icon", "true");
    await fireEvent.click(plus);
    expect(await screen.findByRole("article", { name: t("conversations.selectAgent") })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: t("conversations.inputLabel") })).not.toBeInTheDocument();
    const posts = requests.mock.calls.filter(([url, init]) => init?.method === "POST" && String(url).endsWith("/conversation-graphs"));
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String(posts[0][1]?.body))).toEqual({});
  });

  it.each(["", "missing", "deleted", "graph-foreign"])("does not fall back to the first graph for explicit unavailable ID %s", async (id) => {
    const requests = setup();
    navigate(`/conversations?graph=${id}`, { replace: true });
    render(ConversationsTestHost);
    expect(await screen.findByRole("alert")).toHaveTextContent(text.graphUnavailable);
    expect(screen.queryByText("First graph node")).not.toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
    expect(requests.mock.calls.some(([url]) => String(url).includes("/v1/conversation-graphs/"))).toBe(false);
  });

  it("waits for asynchronous project/list hydration without fetching a wrong first graph or showing a false missing error", async () => {
    const list = deferred<Response>();
    const requests = setup({ list: list.promise });
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    const view = render(ConversationsTestHost, { projectId: null });
    await view.rerender({ projectId: "project-test" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
    list.resolve(envelope({ items: [first, linked] }));
    expect(await screen.findByText("Linked graph node")).toBeInTheDocument();
    expect(requests.mock.calls.some(([url]) => String(url) === `/v1/conversation-graphs/${first.id}`)).toBe(false);
    expect(screen.queryByText("First graph node")).not.toBeInTheDocument();
  });

  it("defaults to the first graph only when no explicit graph is requested", async () => {
    setup();
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    expect(await screen.findByText("First graph node")).toBeInTheDocument();
  });

  it("updates the URL on manual selection and never reapplies the previous deep link after refresh", async () => {
    setup();
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    render(ConversationsTestHost);
    await screen.findByText("Linked graph node");
    await fireEvent.click(screen.getByRole("button", { name: /First graph/ }));
    expect(await screen.findByText("First graph node")).toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("graph")).toBe(first.id);
    queryClient.setQueryData(["conversation-graphs", "project-test"], { items: [linked, first] });
    await waitFor(() => expect(screen.getByText("First graph node")).toBeInTheDocument());
    expect(screen.queryByText("Linked graph node")).not.toBeInTheDocument();
  });

  it("responds to explicit valid/invalid query changes in the mounted page", async () => {
    setup();
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    render(ConversationsTestHost);
    await screen.findByText("Linked graph node");
    navigate("/conversations?graph=missing", { replace: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(text.graphUnavailable);
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    expect(await screen.findByText("First graph node")).toBeInTheDocument();
  });

  it("does not replace the user's new selection with an old detail response", async () => {
    const pending = deferred<Response>();
    const requests = setup({ getDetail: (id) => id === linked.id ? pending.promise : envelope(detail(first)) });
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    render(ConversationsTestHost);
    await waitFor(() => expect(requests.mock.calls.some(([url]) => String(url).endsWith(`/conversation-graphs/${linked.id}`))).toBe(true));
    await fireEvent.click(screen.getByRole("button", { name: /First graph/ }));
    await screen.findByText("First graph node");
    pending.resolve(envelope(detail(linked)));
    await pending.promise;
    await waitFor(() => expect(queryClient.isFetching({ queryKey: ["conversation-detail", linked.id] })).toBe(0));
    expect(screen.queryByText("Linked graph node")).not.toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("graph")).toBe(first.id);
  });

  it("does not render stale detail from a different project, including late responses", async () => {
    const pending = deferred<Response>();
    const requests = setup({ getDetail: () => pending.promise });
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    const view = render(ConversationsTestHost);
    await waitFor(() => expect(requests.mock.calls.some(([url]) => String(url).endsWith(`/conversation-graphs/${linked.id}`))).toBe(true));
    await view.rerender({ projectId: "project-other" });
    pending.resolve(envelope(detail(linked)));
    expect(await screen.findByRole("alert")).toHaveTextContent(text.graphUnavailable);
    expect(screen.queryByText("Linked graph node")).not.toBeInTheDocument();
    expect(screen.queryByText("Foreign graph node")).not.toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
  });

  it("does not render a graph-detail payload whose project does not match the current project", async () => {
    setup({ getDetail: () => envelope({ ...detail(foreign), graph: { ...foreign, id: linked.id } }) });
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    render(ConversationsTestHost);
    await waitFor(() => expect(queryClient.getQueryState(["conversation-detail", linked.id])?.status).toBe("success"));
    expect(screen.queryByText("Foreign graph node")).not.toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
  });

  it("keeps a manual selection in the displayed project instead of reapplying an old project query", async () => {
    setup();
    navigate(`/conversations?project=project-test&graph=${linked.id}`, { replace: true });
    const view = render(ConversationsTestHost);
    await screen.findByText("Linked graph node");
    await view.rerender({ projectId: "project-other" });
    await screen.findByRole("alert");
    await fireEvent.click(screen.getByRole("button", { name: /Foreign graph/ }));
    expect(await screen.findByText("Foreign graph node")).toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("project")).toBe("project-other");
    expect(new URLSearchParams(location.search).get("graph")).toBe(foreign.id);
  });

  it("reports a real detail 404 only after it arrives and hides previously cached nodes", async () => {
    const pending = deferred<Response>();
    queryClient.setQueryData(["conversation-detail", linked.id], detail(linked), { updatedAt: 1 });
    setup({ getDetail: () => pending.promise.then((response) => response.clone()) });
    navigate(`/conversations?graph=${linked.id}`, { replace: true });
    render(ConversationsTestHost);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    pending.resolve(new Response(JSON.stringify({ error: { code: "not_found", message: "Not found" } }), { status: 404 }));
    await waitFor(() => expect(queryClient.getQueryState(["conversation-detail", linked.id])?.status).toBe("error"), { timeout: 3_000 });
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("Linked graph node")).not.toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
  });

  it("creates a graph with no configured Agent, replacing an invalid graph query without sending a message", async () => {
    const requests = setup();
    navigate("/conversations?graph=missing", { replace: true });
    render(ConversationsTestHost, { agentList: [] });
    const sidebar = await screen.findByRole("complementary", { name: t("conversations.rootNodes") });
    await fireEvent.click(within(sidebar).getByRole("button", { name: t("conversations.newGraph") }));
    const draft = await screen.findByRole("article", { name: t("conversations.selectAgent") });
    expect(within(draft).getByRole("button", { name: t("conversations.create") })).toBeDisabled();
    expect(within(draft).getByRole("link")).toHaveAttribute("href", "/agents");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("graph")).toBe("created");
    expect(requests.mock.calls.filter(([url, init]) => init?.method === "POST" && String(url).endsWith("/conversation-graphs"))).toHaveLength(1);
    const request = requests.mock.calls.find(([url, init]) => init?.method === "POST" && String(url).endsWith("/conversation-graphs"));
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({});
    expect(requests.mock.calls.every(([url]) => !/\/(messages|runs|goals)(?:\/|$)/.test(String(url)))).toBe(true);
  });

  it("does not let a late graph creation steal the selection after a project switch", async () => {
    const pending = deferred<Response>();
    const requests = setup({ create: pending.promise });
    navigate("/conversations?graph=missing", { replace: true });
    const view = render(ConversationsTestHost);
    await fireEvent.click(pageCreateButton());
    await waitFor(() => expect(requests.mock.calls.some(([url, init]) => init?.method === "POST" && String(url).endsWith("/conversation-graphs"))).toBe(true));
    await view.rerender({ projectId: "project-other" });
    navigate(`/conversations?graph=${foreign.id}`, { replace: true });
    await screen.findByText("Foreign graph node");
    pending.resolve(envelope(detail({ ...first, id: "old-created", title: "Old created graph" })));
    await waitFor(() => expect(queryClient.getMutationCache().getAll().at(-1)?.state.status).toBe("success"));
    expect(screen.getByText("Foreign graph node")).toBeInTheDocument();
    expect(screen.queryByText("Old created graph node")).not.toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("graph")).toBe(foreign.id);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("still selects a user-created graph if the initial list finishes while creation is pending", async () => {
    const list = deferred<Response>();
    const creation = deferred<Response>();
    const requests = setup({ list: list.promise, create: creation.promise });
    navigate("/conversations", { replace: true });
    render(ConversationsTestHost);
    await fireEvent.click(pageCreateButton());
    await waitFor(() => expect(requests.mock.calls.some(([url, init]) => init?.method === "POST" && String(url).endsWith("/conversation-graphs"))).toBe(true));
    list.resolve(envelope({ items: [first, linked] }));
    await waitFor(() => expect(queryClient.getQueryState(["conversation-graphs", "project-test"])?.status).toBe("success"));
    creation.resolve(envelope(detail({ ...first, id: "created", title: "Created graph" })));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Created graph node")).toBeInTheDocument();
    expect(new URLSearchParams(location.search).get("graph")).toBe("created");
  });

  it("anchors fork edges to the parent's persisted width instead of the default card width", async () => {
    const graph = detail(first);
    graph.nodes[0].width = 540;
    graph.nodes.push({ ...graph.nodes[0], id: "child", parent_id: graph.nodes[0].id,
      title: "Child", position_x: 700, position_y: 300, width: 380 });
    setup({ getDetail: () => envelope(graph) });
    navigate(`/conversations?graph=${first.id}`, { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("Child");
    const path = canvas.querySelector(":scope > div > svg > path");
    expect(path?.getAttribute("d")?.split(" C")[0]).toBe("M 580 102");
    queryClient.setQueryData(["conversation-detail", first.id], {
      ...graph, nodes: graph.nodes.map((node) => node.parent_id ? node : { ...node, width: 640, version: 2 })
    });
    await waitFor(() => expect(path?.getAttribute("d")?.split(" C")[0]).toBe("M 680 102"));
  });

  it("preserves manual pan and zoom across node updates, but follows a new explicit node deep link", async () => {
    const graph = detail(first);
    graph.nodes.push({ ...graph.nodes[0], id: "child", parent_id: graph.nodes[0].id,
      title: "Child", position_x: 700, position_y: 300 });
    setup({ getDetail: () => envelope(graph) });
    navigate(`/conversations?graph=${first.id}&node=${graph.nodes[0].id}`, { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("Child");
    const world = canvas.firstElementChild as HTMLElement;
    await waitFor(() => expect(world.style.transform).toBe("translate(0px, 0px) scale(1)"));
    await fireEvent.keyDown(canvas, { key: "ArrowRight" });
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.zoomIn") }));
    const manuallyChosen = world.style.transform;
    expect(manuallyChosen).toContain("scale(1.2)");
    queryClient.setQueryData(["conversation-detail", first.id], {
      ...graph, nodes: graph.nodes.map((node) => node.parent_id ? node : {
        ...node, position_x: 123, width: 540, status: "running", version: 2
      })
    });
    await waitFor(() => expect(canvas.querySelector(`[data-node-id="${graph.nodes[0].id}"]`)).toHaveStyle({ width: "540px" }));
    expect(world.style.transform).toBe(manuallyChosen);
    navigate(`/conversations?graph=${first.id}&node=child`, { replace: true });
    await waitFor(() => expect(world.style.transform).toBe("translate(-660px, -220px) scale(1)"));
  });

  it("scrolls a mobile node deep link into its stacked viewport once, without undoing manual scrolling on refresh", async () => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: query === "(max-width: 767px)", media: query,
      addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn()
    })));
    const graph = detail(first);
    graph.nodes.push({ ...graph.nodes[0], id: "mobile-child", parent_id: graph.nodes[0].id,
      title: "Mobile child", position_x: 1200, position_y: 900 });
    setup({ getDetail: () => envelope(graph) });
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    scroll.mockClear();
    navigate(`/conversations?graph=${first.id}&node=mobile-child`, { replace: true });
    render(ConversationsTestHost);
    const canvas = await screen.findByRole("application");
    await screen.findByText("Mobile child");
    await waitFor(() => expect(scroll).toHaveBeenCalledTimes(1));
    expect(scroll.mock.instances[0]).toBe(canvas.querySelector('[data-node-id="mobile-child"]'));
    queryClient.setQueryData(["conversation-detail", first.id], {
      ...graph, nodes: graph.nodes.map((node) => ({ ...node, width: 540, version: 2 }))
    });
    await waitFor(() => expect(canvas.querySelector('[data-node-id="mobile-child"]')).toHaveStyle({ width: "540px" }));
    expect(scroll).toHaveBeenCalledTimes(1);
    navigate(`/conversations?graph=${first.id}&node=${graph.nodes[0].id}`, { replace: true });
    await waitFor(() => expect(scroll).toHaveBeenCalledTimes(2));
    expect(scroll.mock.instances[1]).toBe(canvas.querySelector(`[data-node-id="${graph.nodes[0].id}"]`));
  });

  it.each([false, true])("finishes a pending message deep-link focus even if node data refreshes before the scroll (mobile=%s)", async (mobile) => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: mobile && query === "(max-width: 767px)", media: query,
      addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn()
    })));
    const graph = detail(first);
    graph.messages.push({ ...base, id: "focused-message", node_id: graph.nodes[0].id,
      author_kind: "user", status: "published", body: "Linked message contents" });
    setup({ getDetail: () => envelope(graph) });
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    scroll.mockClear();
    navigate(`/conversations?graph=${first.id}&node=${graph.nodes[0].id}&message=focused-message`, { replace: true });
    render(ConversationsTestHost);
    await screen.findByText("Linked message contents");
    queryClient.setQueryData(["conversation-detail", first.id], {
      ...graph, nodes: graph.nodes.map((node) => ({ ...node, width: 540, version: 2 }))
    });
    await waitFor(() => expect(scroll).toHaveBeenCalledTimes(1));
    expect(scroll.mock.instances[0]).toBe(document.getElementById("message-focused-message"));
  });
});
