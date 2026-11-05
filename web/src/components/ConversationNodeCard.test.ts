import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ConversationNodeCard from "./ConversationNodeCard.svelte";
import ConversationNodeCardTestHost from "../test/ConversationNodeCardTestHost.svelte";
import { queryClient } from "../lib/query";
import { t } from "../lib/i18n.svelte";
import type { Agent, ConversationGoal, ConversationNode, ConversationMessage } from "../lib/types";

const versioned = { version: 1, created_at: "2026-09-11T00:00:00Z", updated_at: "2026-09-11T00:00:00Z" };
const agent: Agent = { ...versioned, id: "agent", computer_id: "computer", agent_service_id: "service", name: "Review", status: "active", current_revision_id: "revision" };
const node: ConversationNode = {
  ...versioned, id: "node", graph_id: "graph", agent_id: agent.id, agent_revision_id: "revision", title: "Branch", status: "idle",
  position_x: 0, position_y: 0, width: 380, height: 560, locked: false, message_count: 0
};

function props() {
  return {
    node, position: { x: 120, y: 80 }, messages: [] as ConversationMessage[], agents: [agent], selected: false, busy: false,
    onSelect: vi.fn(), onSend: vi.fn().mockResolvedValue(undefined), onFork: vi.fn(), onDelete: vi.fn(),
    onStop: vi.fn().mockResolvedValue(undefined), onDragStart: vi.fn(), onDragMove: vi.fn(), onDragEnd: vi.fn(),
    onResize: vi.fn()
  };
}

describe("conversation node", () => {
  afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); });

  it("renders an explicitly cancelled Run as cancelled, including a cold history render", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { id: "cancelled-run", status: "cancelled" } }), {
      headers: { "Content-Type": "application/json" }
    })));
    const input = props();
    input.messages = [{ ...versioned, id: "cancelled-message", node_id: node.id, author_kind: "agent",
      body: "...", status: "failed", run_id: "cancelled-run" }];
    render(ConversationNodeCardTestHost, { card: input });
    expect(await screen.findByText(t("status.cancelled"))).toBeInTheDocument();
    expect(screen.queryByText(t("conversations.failed"))).not.toBeInTheDocument();
  });

  it("keeps actual failed Runs labelled as failed", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { id: "failed-run", status: "failed" } }), {
      headers: { "Content-Type": "application/json" }
    })));
    const input = props();
    input.messages = [{ ...versioned, id: "failed-message", node_id: node.id, author_kind: "agent",
      body: "...", status: "failed", run_id: "failed-run" }];
    render(ConversationNodeCardTestHost, { card: input });
    expect(await screen.findByText(t("conversations.failed"))).toBeInTheDocument();
    expect(screen.queryByText(t("status.cancelled"))).not.toBeInTheDocument();
  });

  it("follows cancelling to cancelled without a transient failed label", async () => {
    let status = "cancelling";
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { id: "stopping-run", status } }), {
      headers: { "Content-Type": "application/json" }
    })));
    const input = props();
    input.messages = [{ ...versioned, id: "stopping-message", node_id: node.id, author_kind: "agent",
      body: "...", status: "failed", run_id: "stopping-run" }];
    render(ConversationNodeCardTestHost, { card: input });
    expect(await screen.findByText(t("status.cancelling"))).toBeInTheDocument();
    expect(screen.queryByText(t("conversations.failed"))).not.toBeInTheDocument();
    status = "cancelled";
    await queryClient.invalidateQueries({ queryKey: ["conversation-message-outcome", "stopping-run"] });
    expect(await screen.findByText(t("status.cancelled"))).toBeInTheDocument();
    expect(screen.queryByText(t("conversations.failed"))).not.toBeInTheDocument();
  });

  it("starts a persistent goal and reuses the same idempotency key after failure", async () => {
    const onCreateGoal = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(undefined);
    const input = props();
    render(ConversationNodeCard, { ...input, onCreateGoal });
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    await fireEvent.input(screen.getByRole("textbox"), { target: { value: "完成目标并验证" } });
    const send = screen.getByRole("button", { name: t("conversations.goalStart") });
    await fireEvent.click(send);
    expect(await screen.findByRole("alert")).toHaveTextContent("offline");
    expect(screen.getByRole("textbox")).toHaveValue("完成目标并验证");
    await fireEvent.click(send);
    await waitFor(() => expect(onCreateGoal).toHaveBeenCalledTimes(2));
    expect(onCreateGoal.mock.calls[0]).toEqual(onCreateGoal.mock.calls[1]);
    expect(onCreateGoal.mock.calls[0].slice(0, 2)).toEqual(["完成目标并验证", 2_000_000]);
    expect(input.onSend).not.toHaveBeenCalled();
  });

  it("validates the goal budget without blocking ordinary messages", async () => {
    render(ConversationNodeCard, { ...props(), onCreateGoal: vi.fn() });
    await fireEvent.input(screen.getByRole("textbox"), { target: { value: "objective" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    await fireEvent.input(screen.getByRole("spinbutton", { name: t("issues.goalTokenBudget") }), { target: { value: "1" } });
    expect(screen.getByRole("button", { name: t("conversations.goalStart") })).toBeDisabled();
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    expect(screen.getByRole("button", { name: t("conversations.send") })).toBeEnabled();
  });

  it.each([2_000_000, 60_000, 1_001])("accepts every API-valid integer goal budget, including %s", async (value) => {
    const onCreateGoal = vi.fn().mockResolvedValue(undefined);
    render(ConversationNodeCard, { ...props(), onCreateGoal });
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    const budget = screen.getByRole("spinbutton", { name: t("issues.goalTokenBudget") }) as HTMLInputElement;
    await fireEvent.input(budget, { target: { value: String(value) } });
    expect(budget.checkValidity()).toBe(true);
    await fireEvent.input(screen.getByRole("textbox"), { target: { value: "检查合法整数预算" } });
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.goalStart") }));
    await waitFor(() => expect(onCreateGoal).toHaveBeenCalled());
    expect(onCreateGoal.mock.calls[0].slice(0, 2)).toEqual(["检查合法整数预算", value]);
  });

  it("shows audit progress with pause/cancel controls, not a second message input", async () => {
    const goal: ConversationGoal = {
      ...versioned, id: "goal", node_id: node.id, agent_revision_id: "revision", objective: "目标",
      status: "active", phase: "evaluating", token_budget: 10000, tokens_used: 400,
      work_runs: 2, run_count: 4, max_work_runs: 21, note: "核对证据", status_reason: "", current_run_id: "audit"
    };
    const onGoalAction = vi.fn().mockResolvedValue(undefined);
    const view = render(ConversationNodeCard, { ...props(), goal, onGoalAction });
    expect(screen.getByText(t("issues.goalEvaluating"))).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("conversations.deleteNode") })).toBeDisabled();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.goalPause") }));
    expect(onGoalAction).toHaveBeenCalledWith(goal, "pause");
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.goalCancel") })).toBeEnabled());
    await view.rerender({ goal: { ...goal, status: "paused" } });
    await fireEvent.click(screen.getByRole("button", { name: t("issues.goalResume") }));
    expect(onGoalAction).toHaveBeenLastCalledWith(expect.objectContaining({ status: "paused" }), "resume");
  });

  it.each(["paused", "blocked", "budget_reached"] as const)("does not resume a %s branch Goal after its token budget is spent", async (status) => {
    const goal: ConversationGoal = {
      ...versioned, id: "goal-spent", node_id: node.id, agent_revision_id: "revision",
      objective: "spent", status, phase: "settled", token_budget: 1000, tokens_used: 1001,
      work_runs: 1, run_count: 1, max_work_runs: 21, note: "", status_reason: ""
    };
    const onGoalAction = vi.fn().mockResolvedValue(undefined);
    render(ConversationNodeCard, { ...props(), goal, onGoalAction });
    const resume = screen.queryByRole("button", { name: t("issues.goalResume") });
    if (status === "budget_reached") expect(resume).not.toBeInTheDocument();
    else expect(resume).toBeDisabled();
    expect(screen.getByText(t("goalBudget.exhausted"))).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: t("issues.goal") })).not.toBeInTheDocument();
    if (resume) await fireEvent.click(resume);
    expect(onGoalAction).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: t("issues.goalCancel") })).toBeEnabled();
  });

  it.each(["completed", "cancelled"] as const)("allows ordinary continuation and a new branch Goal after an over-budget %s Goal", async (status) => {
    const goal: ConversationGoal = {
      ...versioned, id: "goal-spent", node_id: node.id, agent_revision_id: "revision",
      objective: "finished", status, phase: "settled", token_budget: 300_000, tokens_used: 419_410,
      work_runs: 2, run_count: 4, max_work_runs: 21, note: "", status_reason: ""
    };
    const input = props();
    const onCreateGoal = vi.fn().mockResolvedValue(undefined);
    render(ConversationNodeCard, { ...input, goal, onCreateGoal });
    expect(screen.getByText(t(status === "completed" ? "issues.goalCompleted" : "issues.goalCancelled"))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(`${goal.tokens_used.toLocaleString()} / ${goal.token_budget!.toLocaleString()} Token`))).toBeInTheDocument();
    expect(screen.queryByText(t("goalBudget.exhausted"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("issues.goalCancel") })).not.toBeInTheDocument();
    const field = screen.getByRole("textbox", { name: t("conversations.inputLabel") });
    expect(field).toBeEnabled();
    await fireEvent.input(field, { target: { value: "普通继续" } });
    expect(screen.getByRole("button", { name: t("conversations.send") })).toBeEnabled();
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.send") }));
    expect(input.onSend).toHaveBeenCalledWith("普通继续", expect.any(String), { expected_agent_id: agent.id, expected_agent_revision_id: "revision" });
    const arm = screen.getByRole("checkbox", { name: t("issues.goal") });
    await waitFor(() => expect(arm).toBeEnabled());
    await fireEvent.click(arm);
    await fireEvent.input(field, { target: { value: "新目标" } });
    expect(screen.getByRole("button", { name: t("conversations.goalStart") })).toBeEnabled();
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.goalStart") }));
    expect(onCreateGoal).toHaveBeenCalledWith("新目标", 2_000_000, expect.any(String), { expected_agent_id: agent.id, expected_agent_revision_id: "revision" });
  });

  it("keeps an over-budget active branch Goal locked to its existing lifecycle", () => {
    const goal: ConversationGoal = {
      ...versioned, id: "goal-spent", node_id: node.id, agent_revision_id: "revision",
      objective: "working", status: "active", phase: "running", token_budget: 1000, tokens_used: 1001,
      work_runs: 1, run_count: 1, max_work_runs: 21, note: "", status_reason: ""
    };
    render(ConversationNodeCard, { ...props(), goal, onCreateGoal: vi.fn(), onGoalAction: vi.fn() });
    expect(screen.getByText(t("goalBudget.exhausted"))).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: t("issues.goal") })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("issues.goalPause") })).toBeEnabled();
    expect(screen.getByRole("button", { name: t("issues.goalCancel") })).toBeEnabled();
  });

  it.each([
    { busy: true, status: "idle" as const },
    { busy: false, status: "running" as const }
  ])("does not unlock a busy branch after a historical over-budget Goal ($status, busy=$busy)", async ({ busy, status }) => {
    const goal: ConversationGoal = {
      ...versioned, id: "goal-spent", node_id: node.id, agent_revision_id: "revision",
      objective: "finished", status: "completed", phase: "settled", token_budget: 1000, tokens_used: 1001,
      work_runs: 1, run_count: 1, max_work_runs: 21, note: "", status_reason: ""
    };
    const input = props();
    const onCreateGoal = vi.fn();
    render(ConversationNodeCard, { ...input, busy, node: { ...node, status }, goal, onCreateGoal });
    await fireEvent.input(screen.getByRole("textbox"), { target: { value: "不能并行启动" } });
    const send = screen.getByRole("button", { name: t("conversations.send") });
    expect(send).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: t("issues.goal") })).toBeDisabled();
    await fireEvent.submit(send.closest("form")!);
    expect(input.onSend).not.toHaveBeenCalled();
    expect(onCreateGoal).not.toHaveBeenCalled();
  });

  it("renders the optimistic drag position rather than stale server coordinates", async () => {
    const view = render(ConversationNodeCard, props());
    expect(view.container.querySelector("article")).toHaveStyle({ left: "120px", top: "80px" });
    await view.rerender({ position: { x: 240, y: 160 } });
    expect(view.container.querySelector("article")).toHaveStyle({ left: "240px", top: "160px" });
  });

  it("keeps a failed draft and reuses its idempotency key on retry", async () => {
    const input = props();
    input.onSend.mockRejectedValueOnce(new Error("offline"));
    render(ConversationNodeCard, input);
    const field = screen.getByRole("textbox", { name: t("conversations.inputLabel") });
    await fireEvent.input(field, { target: { value: "keep this draft" } });
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.send") }));
    expect(await screen.findByRole("alert")).toHaveTextContent("offline");
    expect(field).toHaveValue("keep this draft");
    const firstKey = input.onSend.mock.calls[0][1];
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.send") }));
    await waitFor(() => expect(field).toHaveValue(""));
    expect(input.onSend.mock.calls[1]).toEqual(["keep this draft", firstKey, { expected_agent_id: agent.id, expected_agent_revision_id: "revision" }]);
  });

  it("offers only configured active Agents above the composer and preserves text/Goal options on a failed switch", async () => {
    const other = { ...agent, id: "other", name: "Other", current_revision_id: "other-revision" };
    const onChangeAgent = vi.fn().mockRejectedValue(new Error("switch offline"));
    render(ConversationNodeCard, { ...props(), agents: [agent, other,
      { ...agent, id: "unconfigured", name: "Unconfigured", current_revision_id: undefined },
      { ...agent, id: "inactive", name: "Inactive", status: "disabled" }], onChangeAgent, onCreateGoal: vi.fn() });
    const select = screen.getByRole("combobox", { name: t("conversations.agent") });
    expect([...select.querySelectorAll("option")].map((option) => option.value)).toEqual([agent.id, other.id]);
    const field = screen.getByRole("textbox", { name: t("conversations.inputLabel") });
    expect(select.compareDocumentPosition(field) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await fireEvent.input(field, { target: { value: "保留当前输入" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    await fireEvent.input(screen.getByRole("spinbutton"), { target: { value: "75001" } });
    await fireEvent.change(select, { target: { value: other.id } });
    expect(await screen.findByRole("alert")).toHaveTextContent("switch offline");
    expect(onChangeAgent).toHaveBeenCalledWith(other.id);
    expect(select).toHaveValue(agent.id);
    expect(field).toHaveValue("保留当前输入");
    expect(screen.getByRole("checkbox", { name: t("issues.goal") })).toBeChecked();
    expect(screen.getByRole("spinbutton")).toHaveValue(75001);
  });

  it.each([false, true])("binds retries to Agent/revision and creates a new key after switching (Goal=%s)", async (goalMode) => {
    const input = props();
    const other = { ...agent, id: "other", name: "Other", current_revision_id: "other-revision" };
    const send = vi.fn().mockRejectedValue(new Error("retry later"));
    const onCreateGoal = goalMode ? send : undefined;
    const onChangeAgent = vi.fn().mockResolvedValue(undefined);
    const view = render(ConversationNodeCard, { ...input, agents: [agent, other], onSend: send, onCreateGoal, onChangeAgent });
    if (goalMode) await fireEvent.click(screen.getByRole("checkbox", { name: t("issues.goal") }));
    const field = screen.getByRole("textbox", { name: t("conversations.inputLabel") });
    await fireEvent.input(field, { target: { value: "same intent body" } });
    const button = screen.getByRole("button", { name: t(goalMode ? "conversations.goalStart" : "conversations.send") });
    await fireEvent.click(button);
    await screen.findByRole("alert");
    await fireEvent.click(button);
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls[0]).toEqual(send.mock.calls[1]);
    await fireEvent.change(screen.getByRole("combobox"), { target: { value: other.id } });
    await waitFor(() => expect(onChangeAgent).toHaveBeenCalledWith(other.id));
    await view.rerender({ node: { ...node, agent_id: other.id, agent_revision_id: other.current_revision_id, version: 2 } });
    expect(field).toHaveValue("same intent body");
    await fireEvent.click(button);
    await waitFor(() => expect(send).toHaveBeenCalledTimes(3));
    const keyIndex = goalMode ? 2 : 1;
    expect(send.mock.calls[2][keyIndex]).not.toBe(send.mock.calls[0][keyIndex]);
    expect(send.mock.calls[2].at(-1)).toEqual({ expected_agent_id: other.id, expected_agent_revision_id: "other-revision" });
    await view.rerender({ node: { ...node, agent_id: other.id, agent_revision_id: "new-revision", version: 3 } });
    await fireEvent.click(button);
    await waitFor(() => expect(send).toHaveBeenCalledTimes(4));
    expect(send.mock.calls[3][keyIndex]).not.toBe(send.mock.calls[2][keyIndex]);
    expect(send.mock.calls[3].at(-1)).toEqual({ expected_agent_id: other.id, expected_agent_revision_id: "new-revision" });
  });

  it.each(["busy", "running", "paused", "blocked", "budget_reached", "active", "locked"])("does not allow Agent switching while %s", async (state) => {
    const goal: ConversationGoal = { ...versioned, id: "goal", node_id: node.id, agent_revision_id: "revision", objective: "goal",
      status: "paused", phase: "settled", token_budget: null, tokens_used: 0, work_runs: 1, run_count: 1, max_work_runs: 21, note: "", status_reason: "" };
    const onChangeAgent = vi.fn();
    render(ConversationNodeCard, { ...props(), onChangeAgent, busy: state === "busy",
      node: { ...node, status: state === "running" ? "running" : "idle", locked: state === "locked" },
      goal: ["paused", "blocked", "budget_reached", "active"].includes(state) ? { ...goal, status: state as ConversationGoal["status"] } : null });
    const select = screen.queryByRole("combobox", { name: t("conversations.agent") });
    if (state === "locked") expect(select).not.toBeInTheDocument();
    else expect(select).toBeDisabled();
    expect(onChangeAgent).not.toHaveBeenCalled();
  });

  it.each(["running", "locked"])("allows title editing while %s and preserves a rejected title draft", async (state) => {
    const onRename = vi.fn().mockRejectedValueOnce(new Error("rename conflict")).mockResolvedValue(undefined);
    render(ConversationNodeCard, { ...props(), node: { ...node, status: state === "running" ? "running" : "idle", locked: state === "locked" }, onRename });
    await fireEvent.click(screen.getByRole("button", { name: `${t("common.edit")} · ${t("conversations.forkNodeTitle")}` }));
    const field = screen.getByRole("textbox", { name: t("conversations.forkNodeTitle") });
    await fireEvent.input(field, { target: { value: "  新标题  " } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    expect(await screen.findByRole("alert")).toHaveTextContent("rename conflict");
    expect(field).toHaveValue("  新标题  ");
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(screen.queryByRole("textbox", { name: t("conversations.forkNodeTitle") })).not.toBeInTheDocument());
    expect(onRename).toHaveBeenNthCalledWith(2, "新标题");
  });

  it("does not erase text typed while a request is pending", async () => {
    const input = props();
    let complete!: () => void;
    input.onSend.mockImplementation(() => new Promise<void>((resolve) => { complete = resolve; }));
    render(ConversationNodeCard, input);
    const field = screen.getByRole("textbox");
    await fireEvent.input(field, { target: { value: "first" } });
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.send") }));
    await fireEvent.input(field, { target: { value: "next draft" } });
    complete();
    await waitFor(() => expect(screen.getByRole("button", { name: t("conversations.send") })).toBeEnabled());
    expect(field).toHaveValue("next draft");
  });

  it("locks a forked node to branching only", async () => {
    const input = props();
    input.node = { ...node, locked: true };
    input.messages = [
      { ...versioned, id: "reply", node_id: node.id, author_kind: "agent", body: "final answer", status: "published" }
    ];
    render(ConversationNodeCard, input);
    expect(screen.getByText(t("conversations.nodeLocked"))).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("conversations.fork") }));
    expect(input.onFork).toHaveBeenCalledWith(expect.objectContaining({ id: "reply" }));
  });

  it("shows the inherited chat history carried by a branch", () => {
    const input = props();
    input.node = {
      ...node,
      inherited_messages: [
        { author: "user", body: "question before the branch" },
        { author: "agent", body: "answer before the branch" }
      ]
    };
    render(ConversationNodeCard, input);
    expect(screen.getByText(t("conversations.inheritedHistory"))).toBeInTheDocument();
    expect(screen.getByText("question before the branch")).toBeInTheDocument();
    expect(screen.getByText("answer before the branch")).toBeInTheDocument();
  });

  it("submits the dragged node size when the resize handle is released", async () => {
    const input = props();
    render(ConversationNodeCard, input);
    const handle = screen.getByRole("button", { name: t("conversations.resizeNode") });
    await fireEvent.pointerDown(handle, { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
    await fireEvent.pointerMove(handle, { clientX: 300, clientY: 350, pointerId: 1 });
    await fireEvent.pointerUp(handle, { clientX: 300, clientY: 350, pointerId: 1 });
    await waitFor(() => expect(input.onResize).toHaveBeenCalledTimes(1));
    const size = input.onResize.mock.calls[0][0] as { width: number; height: number };
    expect(size.width).toBeCloseTo(580, 0);
    expect(size.height).toBeCloseTo(810, 0);
  });

  it("exposes stop instead of allowing a second send or deleting a running node", async () => {
    const input = props();
    input.node = { ...node, status: "running" };
    input.messages = [{ ...versioned, id: "working", node_id: node.id, author_kind: "agent", body: "...", status: "working", run_id: "run" }];
    render(ConversationNodeCard, input);
    expect(screen.getByRole("button", { name: t("conversations.deleteNode") })).toBeDisabled();
    expect(screen.queryByRole("button", { name: t("conversations.send") })).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.stop") }));
    expect(input.onStop).toHaveBeenCalledWith("run");
  });
});
