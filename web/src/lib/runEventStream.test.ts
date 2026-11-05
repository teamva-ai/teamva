import { afterEach, describe, expect, it, vi } from "vitest";
import {
  BoundedRunEventBuffer, projectActivityRunEvent, RunChangesInvalidationHandler
} from "./activityEvents";
import { subscribeRunEvents } from "./runEventStream";
import type { RunEvent } from "./types";

function event(id: string, seq: number, text: string): RunEvent {
  return {
    id,
    run_id: "run-1",
    seq,
    event_type: "output.delta",
    payload: { text },
    occurred_at: `2026-08-09T00:00:0${seq}Z`
  };
}

describe("run event stream cache", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  it("projects a 20 MiB SSE patch and nested content before retaining the event", () => {
    const marker = "DO-NOT-RETAIN-PATCH";
    const projected = projectActivityRunEvent({
      id: "event-large",
      run_id: "run-1",
      seq: 3,
      event_type: "workspace.diff",
      occurred_at: "2026-08-09T00:00:03Z",
      payload: {
        data: { patch: marker + "x".repeat(20 * 1024 * 1024) },
        patch: marker,
        content: [{ text: "display summary" }],
        paths: ["src/safe.ts"]
      }
    });

    expect(projected).toBeDefined();
    const retained = JSON.stringify(projected);
    expect(retained).not.toContain(marker);
    expect(retained).not.toContain('"patch"');
    expect(retained).not.toContain('"content"');
    expect(retained.length).toBeLessThan(70 * 1024);
    expect(projected?.payload.paths).toEqual(["src/safe.ts"]);
  });

  it("does not reconstruct FileBridge requests from native file-operation content", () => {
    const projected = projectActivityRunEvent({
      id: "event-bridge", run_id: "run-1", seq: 5, event_type: "provider.event",
      occurred_at: "2026-08-09T00:00:05Z",
      payload: {
        provider_id: "opencode",
        part: {
          id: "prt-bridge", type: "tool", tool: "write",
          state: {
            status: "completed",
            input: {
              content: '{"body":"验收结论","mentioned_agent_ids":[]}',
              filePath: "/tmp/tool-requests/run-1/issue_comment_create/req-1.json"
            }
          }
        }
      }
    });

    const input = ((projected?.payload.part as Record<string, unknown>)?.state as Record<string, unknown>)
      ?.input as Record<string, unknown>;
    expect(input).not.toHaveProperty("request");
    expect(input).not.toHaveProperty("content");
    expect(input.filePath).toBe("/tmp/tool-requests/run-1/issue_comment_create/req-1.json");
  });

  it("drops inline attachment data URLs before caching activity", () => {
    const projected = projectActivityRunEvent({
      id: "event-attachment", run_id: "run-1", seq: 4, event_type: "user.steer",
      occurred_at: "2026-08-09T00:00:04Z",
      payload: {
        text: "inspect", attachments: [{
          mime_type: "image/png", filename: "capture.png",
          data_url: "data:image/png;base64,DO-NOT-RETAIN"
        }]
      }
    });
    expect(projected?.payload.attachments).toEqual([{
      mime_type: "image/png", filename: "capture.png"
    }]);
    expect(JSON.stringify(projected)).not.toContain("data:image");
  });

  it("uses a bounded Map/queue for long Runs", () => {
    const buffer = new BoundedRunEventBuffer(100, 128 * 1024);
    for (let seq = 1; seq <= 10_000; seq += 1) {
      buffer.upsert(event(`event-${seq}`, seq, `delta-${seq}`));
    }
    expect(buffer.size).toBeLessThanOrEqual(100);
    expect(buffer.bytes).toBeLessThanOrEqual(128 * 1024);
    expect(buffer.values().at(-1)?.seq).toBe(10_000);
    expect(buffer.values()[0]?.seq).toBeGreaterThan(9_800);
    expect(buffer.incomplete).toBe(true);
    expect(buffer.evictedCount).toBeGreaterThan(9_800);
    expect(buffer.evictedBytes).toBeGreaterThan(0);
  });

  it("invalidates once for an HTTP page and ignores the same SSE change identity", () => {
    const invalidate = vi.fn();
    const handler = new RunChangesInvalidationHandler(invalidate);
    const workspace = {
      ...event("workspace", 4, ""), event_type: "workspace.diff", payload: { paths: ["src/a.ts"] }
    };
    const fileChange = {
      ...event("file-change", 5, ""), event_type: "provider.item",
      payload: { provider_id: "codex", item: { type: "fileChange", paths: ["src/b.ts"] } }
    };

    expect(handler.ingestPage([workspace, fileChange])).toMatchObject({ sequence: 5 });
    expect(invalidate).toHaveBeenCalledTimes(1);
    handler.ingest(fileChange);
    handler.ingest(workspace);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(handler.size).toBe(2);
  });

  it("reconnects with after_seq when an SSE sequence gap is observed", async () => {
    vi.useFakeTimers();
    class FakeEventSource {
      static instances: FakeEventSource[] = [];
      listeners = new Map<string, Array<(event: Event) => void>>();
      constructor(public url: string) {
        FakeEventSource.instances.push(this);
      }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
        const callback = typeof listener === "function" ? listener : (value: Event) => listener.handleEvent(value);
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), callback]);
      }
      close() {}
      emit(type: string, value: RunEvent) {
        for (const listener of this.listeners.get(type) ?? []) {
          listener(new MessageEvent(type, { data: JSON.stringify(value) }));
        }
      }
    }
    vi.stubGlobal("EventSource", FakeEventSource);
    const received: number[] = [];
    const unsubscribe = subscribeRunEvents({ runId: "run-1", onEvent: (item) => received.push(item.seq) });
    FakeEventSource.instances[0].emit("output.delta", event("event-1", 1, "A"));
    FakeEventSource.instances[0].emit("output.delta", event("event-3", 3, "C"));
    await vi.runAllTimersAsync();

    expect(received).toEqual([1]);
    expect(new URL(FakeEventSource.instances[1].url, "http://teamva.test").searchParams.get("after_seq")).toBe("1");
    unsubscribe();
  });

  it("keeps retryable errors and diagnostics in sequence, then settles on run.error alone", () => {
    class FakeEventSource {
      static latest: FakeEventSource;
      listeners = new Map<string, EventListenerOrEventListenerObject>();
      closed = false;
      constructor() { FakeEventSource.latest = this; }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) { this.listeners.set(type, listener); }
      close() { this.closed = true; }
      emit(value: RunEvent) {
        const listener = this.listeners.get(value.event_type);
        const message = new MessageEvent(value.event_type, { data: JSON.stringify(value) });
        if (typeof listener === "function") listener(message);
        else listener?.handleEvent(message);
      }
    }
    vi.stubGlobal("EventSource", FakeEventSource);
    const onEvent = vi.fn(), onTerminal = vi.fn();
    const stop = subscribeRunEvents({ runId: "run-1", onEvent, onTerminal });
    const source = FakeEventSource.latest;
    source.emit({ ...event("retry", 1, ""), event_type: "run.error", payload: { willRetry: true } });
    expect(source.closed).toBe(false);
    expect(onTerminal).not.toHaveBeenCalled();
    source.emit({ ...event("diagnostic", 2, ""), event_type: "node.diagnostic" });
    const terminal = { ...event("failed", 3, ""), event_type: "run.error", payload: { status: "failed" } };
    source.emit(terminal);
    expect(onEvent.mock.calls.map(([value]) => value.seq)).toEqual([1, 2, 3]);
    expect(source.closed).toBe(true);
    expect(onTerminal).toHaveBeenCalledExactlyOnceWith(terminal);
    stop();
  });
});
