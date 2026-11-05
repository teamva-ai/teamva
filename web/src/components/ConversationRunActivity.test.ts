import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import ConversationRunActivity from "./ConversationRunActivity.svelte";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners = new Map<string, EventListener[]>();
  close = vi.fn();
  constructor(public url: string) { FakeEventSource.instances.push(this); }
  addEventListener(type: string, fn: EventListener) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]); }
  emit(seq: number, type: string, payload: Record<string, unknown>) {
    const event = new MessageEvent(type, { data: JSON.stringify({ id: `event-${seq}`, run_id: "run", seq, event_type: type, payload: { provider_id: "opencode", ...payload }, occurred_at: "2026-09-14T00:00:00Z" }) });
    for (const fn of this.listeners.get(type) ?? []) fn(event);
  }
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); FakeEventSource.instances = []; });

it("shows retry and nested gateway errors without ending early or retaining credentials", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const settled = vi.fn();
  const view = render(ConversationRunActivity, { runId: "run", active: true, onSettled: settled });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "provider.event", { willRetry: true, error: { data: { message: "Gateway 503 reconnecting; API_KEY=fixture-only-retry" } } });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="retry"]')).toHaveTextContent("Gateway 503 reconnecting"));
  expect(settled).not.toHaveBeenCalled();
  expect(stream.close).not.toHaveBeenCalled();
  stream.emit(2, "run.finished", { status: "failed", error: { data: { message: "Gateway 429 quota exceeded; Authorization: Bearer fixture-only-auth" } } });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="error"]')).toHaveTextContent("Gateway 429 quota exceeded"));
  expect(view.container.innerHTML).not.toContain("fixture-only-");
  expect(settled).toHaveBeenCalledTimes(1);
});

it("displays compaction failure detail and settles on the streamed terminal without a parent refresh", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "context.compaction", { id: "compact", status: "started" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "running"));
  stream.emit(2, "context.compaction", { id: "compact", status: "failed", message: "Compaction gateway returned 502" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveTextContent("Compaction gateway returned 502"));
  stream.emit(3, "context.compaction", { id: "other", status: "running" });
  stream.emit(4, "run.finished", { status: "cancelled" });
  await waitFor(() => expect(view.container.querySelectorAll('[data-activity-status="running"]')).toHaveLength(0));
});

it("streams thought and reply chunks separately then suppresses the persisted final duplicate", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const settled = vi.fn();
  const view = render(ConversationRunActivity, { runId: "run", active: true, onSettled: settled });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "provider.event", { kind: "reasoning", part_id: "reasoning:msg-1:0", message_id: "msg-1", delta: "核对参数" });
  await waitFor(() => expect(view.container.querySelector('[title="核对参数"]')).toBeInTheDocument());
  expect(view.container.querySelector(".conversation-stream-reply")).not.toBeInTheDocument();
  stream.emit(2, "output.delta", { part_id: "text:msg-1:0", message_id: "msg-1", text: "第一段" });
  expect(await screen.findByText("第一段")).toBeInTheDocument();
  stream.emit(3, "output.delta", { part_id: "text:msg-1:0", message_id: "msg-1", text: "第二段" });
  expect(await screen.findByText("第一段第二段")).toBeInTheDocument();
  stream.emit(4, "run.completed", {});
  await waitFor(() => expect(settled).toHaveBeenCalledTimes(1));
  await view.rerender({ active: false, publishedBody: "第一段第二段" });
  expect(view.container.querySelector(".conversation-stream-reply")).not.toBeInTheDocument();
});

it("loads completed run details only on demand", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  render(ConversationRunActivity, { runId: "run", active: false, publishedBody: "答案" });
  expect(FakeEventSource.instances).toHaveLength(0);
  await fireEvent.click(screen.getByRole("button", { name: t("issues.sessionOutput") }));
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
});

it("updates one compaction row through running, completed and failed states", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "context.compaction", { id: "compact-1", status: "running" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "running"));
  stream.emit(2, "context.compaction", { id: "compact-1", status: "completed" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "completed"));
  expect(view.container.querySelectorAll('[data-activity-kind="compaction"]')).toHaveLength(1);
  stream.emit(3, "context.compaction", { id: "compact-2", status: "failed" });
  await waitFor(() => expect(view.container.querySelectorAll('[data-activity-kind="compaction"]')).toHaveLength(2));
  expect(view.container.querySelector('[data-activity-status="failed"]')).toHaveTextContent(t("issues.sessionCompactionFailed"));
});

it("does not leave compaction spinning when the Run ends without a completion event", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  FakeEventSource.instances[0].emit(1, "context.compaction", { id: "unfinished", status: "running" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "running"));
  await view.rerender({ active: false });
  expect(view.container.querySelector('[data-activity-kind="compaction"]')).toHaveAttribute("data-activity-status", "interrupted");
  expect(screen.getByText(t("issues.sessionCompactionInterrupted"))).toBeInTheDocument();
});

it("settles unfinished thinking and tool indicators after cancellation without inventing results", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "reasoning.snapshot", { id: "r", status: "running", text: "Still thinking" });
  stream.emit(2, "provider.tool", { id: "t", tool: "bash", status: "running", input: { command: "test" } });
  await waitFor(() => expect(view.container.querySelectorAll('[data-activity-status="running"]')).toHaveLength(2));
  await view.rerender({ active: false });
  expect(view.container.querySelectorAll('[data-activity-status="running"]')).toHaveLength(0);
  expect(view.container.querySelectorAll('[data-activity-status="interrupted"]')).toHaveLength(2);
  expect(view.container.querySelector('[data-activity-kind="tool"]')).not.toHaveTextContent("succeeded");
});

it.each([
  { exit: 0, status: "completed" },
  { exit: 3, status: "failed" },
  { exit: null, status: "unknown" }
])("renders native tool stdout and the actual exit $exit after SSE ingress", async ({ exit, status }) => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "provider.event", {
    kind: "tool", phase: "called", call_id: "shell-1", name: "shell",
    input: { command: "printf actual-stdout" }
  });
  stream.emit(2, "provider.event", {
    kind: "tool", phase: "success", call_id: "shell-1", name: "shell",
    content: [{ type: "text", text: "actual-stdout\nEXIT_CODE=3\n" }],
    metadata: { exit, status: "completed", truncated: false }
  });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="tool"]')).toHaveAttribute("data-activity-status", status));
  expect(view.container.querySelectorAll('[data-activity-kind="tool"]')).toHaveLength(1);
  const row = view.container.querySelector('[data-activity-kind="tool"]')!;
  await fireEvent.click(row.querySelector("button")!);
  expect(row.querySelector("pre")).toHaveTextContent("actual-stdout");
  expect(row.querySelector("pre")).toHaveTextContent("EXIT_CODE=3");
  expect(row.querySelector("pre")).toHaveTextContent(`"exit_code": ${exit}`);
  expect(view.container.querySelector(".conversation-stream-reply")).not.toBeInTheDocument();
});
