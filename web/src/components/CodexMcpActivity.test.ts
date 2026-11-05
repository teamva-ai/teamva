import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import ConversationRunActivity from "./ConversationRunActivity.svelte";

class NativeEventSource {
  static instances: NativeEventSource[] = [];
  listeners = new Map<string, EventListener[]>();
  close = vi.fn();
  constructor(public url: string) { NativeEventSource.instances.push(this); }
  addEventListener(type: string, callback: EventListener) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), callback]); }
  emit(seq: number, type: string, payload: Record<string, unknown>) {
    const value = { id: `codex-${seq}`, run_id: "run", seq, event_type: type,
      payload: { provider_id: "codex", ...payload }, occurred_at: "2026-10-23T00:00:00Z" };
    for (const callback of this.listeners.get(type) ?? []) callback(new MessageEvent(type, { data: JSON.stringify(value) }));
  }
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); NativeEventSource.instances = []; });
const item = (status: string) => ({ id: "mcp-call", type: "mcpToolCall", server: "amqa-local", tool: "qa_echo",
  arguments: { text: "AMQA_GOAL_MCP_CODEX_20261023" }, status, result: null, error: null, durationMs: null });

it.each(["completed", "failed"])("renders a real Codex MCP %s tool after SSE ingress instead of hiding it", async status => {
  vi.stubGlobal("EventSource", NativeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: true });
  await waitFor(() => expect(NativeEventSource.instances).toHaveLength(1));
  const stream = NativeEventSource.instances[0];
  stream.emit(1, "provider.item", { item: item("inProgress") });
  stream.emit(2, "provider.event", { method: "item/mcpToolCall/progress", itemId: "mcp-call", message: "waiting for receipt" });
  await waitFor(() => expect(view.container.querySelector('[data-activity-kind="tool"]')).toHaveAttribute("data-activity-status", "inProgress"));
  const row = view.container.querySelector('[data-activity-kind="tool"]')!;
  expect(row).toHaveTextContent("amqa-local/qa_echo");
  await fireEvent.click(row.querySelector("button")!);
  expect(row.querySelector("pre")).toHaveTextContent("AMQA_GOAL_MCP_CODEX_20261023");
  expect(row.querySelector("pre")).toHaveTextContent("waiting for receipt");
  stream.emit(3, "provider.item", { item: { ...item(status), durationMs: 42,
    result: status === "completed" ? { content: [{ type: "text", text: "AMQA_MCP_TOOL_RESULT" }] } : null,
    error: status === "failed" ? { message: "user rejected MCP tool call" } : null } });
  await waitFor(() => expect(row).toHaveAttribute("data-activity-status", status));
  expect(view.container.querySelectorAll('[data-activity-kind="tool"]')).toHaveLength(1);
  expect(row.querySelector("pre")).toHaveTextContent(status === "completed" ? "AMQA_MCP_TOOL_RESULT" : "user rejected MCP tool call");
  expect(row.querySelector("pre")).not.toHaveTextContent("exit_code");
  await view.rerender({ active: false });
  expect(row).toHaveAttribute("data-activity-status", status);
});

it("restores a failed final MCP snapshot on demand and settles an unfinished MCP call as interrupted", async () => {
  vi.stubGlobal("EventSource", NativeEventSource);
  const view = render(ConversationRunActivity, { runId: "run", active: false });
  await fireEvent.click(screen.getByRole("button", { name: t("issues.sessionOutput") }));
  await waitFor(() => expect(NativeEventSource.instances).toHaveLength(1));
  const stream = NativeEventSource.instances[0];
  stream.emit(1, "provider.item", { item: { ...item("failed"), error: { message: "user rejected MCP tool call" }, durationMs: 15 } });
  stream.emit(2, "provider.item", { item: { ...item("inProgress"), id: "interrupted-mcp" } });
  await waitFor(() => expect(view.container.querySelectorAll('[data-activity-kind="tool"]')).toHaveLength(2));
  expect(view.container.querySelector('[data-activity-status="failed"]')).toHaveTextContent("amqa-local/qa_echo");
  expect(view.container.querySelector('[data-activity-status="interrupted"]')).toHaveTextContent("amqa-local/qa_echo");
  const failed = view.container.querySelector('[data-activity-status="failed"]')!;
  await fireEvent.click(failed.querySelector("button")!);
  expect(failed.querySelector("pre")).toHaveTextContent("user rejected MCP tool call");
});
