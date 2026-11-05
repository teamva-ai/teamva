import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import type { Run, RunEvent } from "../lib/types";
import AgentSideSheetHarness from "../test/AgentSideSheetHarness.svelte";

const stamp = "2026-11-02T00:00:00Z";
function envelope(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function run(status: Run["status"]): Run {
  return {
    id: "codex-mentioned-run", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
    agent_id: "agent-1", project_id: "project-1", status,
    purpose: "work", read_only: false, lease_epoch: 1, version: 1,
    input: [{ type: "text", internal: true, text: "private-issue-context-never-render" }],
    created_at: stamp, updated_at: stamp
  };
}
function event(seq: number, event_type: string, payload: Record<string, unknown>): RunEvent {
  return { id: `native-${seq}`, run_id: "codex-mentioned-run", seq, event_type, payload, occurred_at: stamp };
}

class NativeStream {
  static instances: NativeStream[] = [];
  listeners = new Map<string, EventListener[]>();
  closed = false;
  constructor(readonly url: string) { NativeStream.instances.push(this); }
  addEventListener(name: string, listener: EventListener) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), listener]);
  }
  close() { this.closed = true; }
  emit(value: RunEvent) {
    for (const listener of this.listeners.get(value.event_type) ?? []) {
      listener(new MessageEvent(value.event_type, { data: JSON.stringify(value) }));
    }
  }
}

describe("AgentSideSheet initial native stream", () => {
  afterEach(() => {
    cleanup();
    clearComposerDrafts();
    queryClient.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    NativeStream.instances = [];
    localStorage.clear();
  });

  it.each(["queued", "starting", "running"] as const)("shows an active %s mention before any public content exists", async (status) => {
    vi.stubGlobal("EventSource", undefined);
    const current = run(status);
    const history = [
      event(1, "run.queued", { source: "mention" }),
      event(2, "run.started", { provider_id: "codex" }),
      event(3, "provider.item", { provider_id: "codex", internal: true,
        item: { id: "private-context", type: "userMessage", content: [{ text: "private-native-context-never-render" }] } })
    ];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [current] });
      if (path.endsWith(`/runs/${current.id}/events`)) return envelope({ items: history });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    expect(await screen.findByText(t("issues.sessionGenerating"))).toBeVisible();
    expect(screen.getByRole("list", { name: t("issues.sessionTimeline") })).toBeVisible();
    expect(screen.queryByText(t("issues.sessionHint"))).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private-issue-context-never-render");
    expect(document.body).not.toHaveTextContent("private-native-context-never-render");
  });

  it("renders each native stage before completed, then removes the busy indicator on terminal reconciliation", async () => {
    vi.stubGlobal("EventSource", NativeStream);
    let current = run("running");
    const history = [event(1, "run.queued", { source: "mention" }), event(2, "run.started", { provider_id: "codex" })];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://teamva.test");
      if (url.pathname.endsWith("/goal")) return envelope({ goal: null });
      if (url.pathname.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [current] });
      if (url.pathname.endsWith(`/runs/${current.id}/events`)) {
        return envelope({ items: history.filter((item) => item.seq > Number(url.searchParams.get("after") ?? 0)) });
      }
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    expect(await screen.findByText(t("issues.sessionGenerating"))).toBeVisible();
    await waitFor(() => expect(NativeStream.instances.length).toBeGreaterThan(0));
    const stream = NativeStream.instances[0];
    const append = (item: RunEvent) => { history.push(item); stream.emit(item); };

    append(event(3, "provider.event", { provider_id: "codex", willRetry: true, error: { message: "Reconnecting... 2/5" } }));
    expect(await screen.findByText("Reconnecting... 2/5")).toBeVisible();

    append(event(4, "provider.item", { provider_id: "codex", method: "item/started",
      item: { id: "reasoning-first", type: "reasoning", summary: [], content: [] } }));
    expect(await screen.findByText(t("issues.sessionThinking"))).toBeVisible();
    expect(document.querySelector('[data-activity-kind="reasoning"]')).not.toHaveTextContent("summary-stream-marker");

    append(event(5, "provider.event", { provider_id: "codex", method: "item/reasoning/summaryTextDelta",
      itemId: "reasoning-first", summaryIndex: 0, delta: "summary-stream-marker" }));
    await waitFor(() => expect(document.querySelector('[data-activity-kind="reasoning"]')).toHaveTextContent("summary-stream-marker"));

    append(event(6, "provider.item", { provider_id: "codex", method: "item/started", item: {
      id: "command-first", type: "commandExecution", status: "inProgress", command: "printf native-command-marker", aggregatedOutput: null
    } }));
    expect(await screen.findByText("printf native-command-marker")).toBeVisible();

    append(event(7, "output.delta", { provider_id: "codex", item_id: "answer-first", text: "first-token-before-completed" }));
    expect(await screen.findByText("first-token-before-completed")).toBeVisible();
    expect(screen.getByText(t("issues.sessionGenerating"))).toBeVisible();
    expect(stream.closed).toBe(false);

    current = { ...current, status: "succeeded", version: 2, finished_at: stamp };
    append(event(8, "run.completed", { provider_id: "codex", status: "completed" }));
    await waitFor(() => expect(screen.queryByText(t("issues.sessionGenerating"))).not.toBeInTheDocument());
    expect(screen.getByText("first-token-before-completed")).toBeVisible();
    expect(stream.closed).toBe(true);
  });

  it("does not hide available output behind an unrelated pending approvals request", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolveApprovals: ((response: Response) => void) | undefined;
    const pendingApprovals = new Promise<Response>((resolve) => { resolveApprovals = resolve; });
    const current = run("succeeded");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [current] });
      if (path.endsWith(`/runs/${current.id}/events`)) return envelope({ items: [
        event(1, "output.delta", { provider_id: "codex", item_id: "available-reply", text: "available-output-with-approvals-pending" })
      ] });
      if (path.endsWith("/approvals")) return pendingApprovals;
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    try {
      expect(await screen.findByText("available-output-with-approvals-pending")).toBeVisible();
      expect(screen.queryByText(t("issues.sessionGenerating"))).not.toBeInTheDocument();
    } finally {
      resolveApprovals?.(envelope({ items: [] }));
    }
  });
});
