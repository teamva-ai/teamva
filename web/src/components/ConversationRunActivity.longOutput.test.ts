import { cleanup, render, waitFor } from "@testing-library/svelte";
import { afterEach, expect, it, vi } from "vitest";
import ConversationRunActivity from "./ConversationRunActivity.svelte";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners = new Map<string, EventListener[]>();
  close = vi.fn();
  constructor(public url: string) { FakeEventSource.instances.push(this); }
  addEventListener(type: string, fn: EventListener) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]); }
  emit(seq: number, type: string, payload: Record<string, unknown>) {
    const event = new MessageEvent(type, { data: JSON.stringify({ id: `event-${seq}`, run_id: "run", seq, event_type: type,
      payload: { provider_id: "opencode", ...payload }, occurred_at: "2026-10-24T00:00:00Z" }) });
    for (const fn of this.listeners.get(type) ?? []) fn(event);
  }
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); FakeEventSource.instances = []; });

it.each([16 * 1024 + 1, 22425, 32 * 1024 + 1])("does not leave a stale 16 KiB reply after publishing %i native characters", async (length) => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const settled = vi.fn();
  const view = render(ConversationRunActivity, { runId: "run", active: true, onSettled: settled });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  const text = "a".repeat(length - 4) + "LAST";
  let seq = 0;
  for (let start = 0; start < text.length; start += 512) {
    stream.emit(++seq, "output.delta", { part_id: "text:message:0", message_id: "message", text: text.slice(start, start + 512) });
  }
  await waitFor(() => expect(view.container.querySelector(".conversation-stream-reply p")?.textContent?.length).toBe(length));
  stream.emit(++seq, "provider.event", { kind: "text", part_id: "text:message:0", message_id: "message", text });
  stream.emit(++seq, "run.completed", {});
  await waitFor(() => expect(settled).toHaveBeenCalledOnce());
  expect(view.container.querySelectorAll(".conversation-stream-reply")).toHaveLength(1);
  expect(view.container.querySelector(".conversation-stream-reply p")?.textContent?.length).toBe(length);
  await view.rerender({ active: false, publishedBody: text });
  expect(view.container.querySelectorAll(".conversation-stream-reply")).toHaveLength(0);
});

it("keeps other assistant messages, including a repeated message, when deduplicating the one published answer", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  stream.emit(1, "output.delta", { part_id: "text:before:0", message_id: "before", text: "same answer" });
  stream.emit(2, "provider.event", { kind: "text", part_id: "text:before:0", message_id: "before", text: "same answer" });
  stream.emit(3, "reasoning.snapshot", { id: "before", text: "thinking independently", status: "completed" });
  stream.emit(4, "provider.tool", { id: "before", tool: "bash", input: { command: "echo tool" }, output: "stdout", status: "completed" });
  stream.emit(5, "output.delta", { part_id: "text:middle:0", message_id: "middle", text: "same answer but distinct" });
  stream.emit(6, "provider.event", { kind: "text", part_id: "text:middle:0", message_id: "middle", text: "same answer but distinct" });
  stream.emit(7, "output.delta", { part_id: "text:final:0", message_id: "final", text: "same answer" });
  stream.emit(8, "provider.event", { kind: "text", part_id: "text:final:0", message_id: "final", text: "same answer" });
  stream.emit(9, "run.completed", {});
  await waitFor(() => expect(view.container.querySelectorAll(".conversation-stream-reply")).toHaveLength(3));
  await view.rerender({ active: false, publishedBody: "same answer" });
  expect([...view.container.querySelectorAll(".conversation-stream-reply p")].map((row) => row.textContent)).toEqual([
    "same answer", "same answer but distinct"
  ]);
  expect(view.container.querySelectorAll('[data-activity-kind="reasoning"]')).toHaveLength(1);
  expect(view.container.querySelectorAll('[data-activity-kind="tool"]')).toHaveLength(1);
});

it("replaces cumulative standard snapshots in one row before and after publication", async () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const stream = FakeEventSource.instances[0];
  const text = "b".repeat(32 * 1024) + "LAST";
  stream.emit(1, "output.snapshot", { provider_id: "futurecli", id: "message", text: text.slice(0, 16 * 1024) });
  await waitFor(() => expect(view.container.querySelector(".conversation-stream-reply p")?.textContent?.length).toBe(16 * 1024));
  stream.emit(2, "reasoning.snapshot", { provider_id: "futurecli", id: "message", text: "separate thought", status: "completed" });
  stream.emit(3, "output.snapshot", { provider_id: "futurecli", id: "message", text });
  await waitFor(() => expect(view.container.querySelector(".conversation-stream-reply p")?.textContent?.length).toBe(text.length));
  expect(view.container.querySelectorAll(".conversation-stream-reply")).toHaveLength(1);
  stream.emit(4, "run.completed", {});
  await view.rerender({ active: false, publishedBody: text });
  expect(view.container.querySelectorAll(".conversation-stream-reply")).toHaveLength(0);
  expect(view.container.querySelectorAll('[data-activity-kind="reasoning"]')).toHaveLength(1);
});
