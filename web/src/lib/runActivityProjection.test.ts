import { describe, expect, it } from "vitest";
import { materializeRunActivity, projectRunActivity } from "./runActivityProjection";
import { projectActivityRunEvent } from "./activityEvents";
import type { RunEvent } from "./types";

const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
  id: `event-${seq}`,
  run_id: "run",
  seq,
  event_type,
  payload: { provider_id: "opencode", ...payload },
  occurred_at: "2026-09-14T00:00:00Z"
});
const codexEvent = (seq: number, type: string, payload: Record<string, unknown>) =>
  event(seq, type, { ...payload, provider_id: "codex" });

describe("native Run activity projection", () => {
  it("omits private input echoes without removing genuine user steering or assistant output", () => {
    const internal = event(1, "user.steer", { text: "private generated instruction", internal: true });
    const echo = codexEvent(2, "provider.item", { internal: true });
    const user = event(3, "user.steer", { text: "actual follow-up", internal: false });
    const reply = event(4, "output.snapshot", { id: "reply", text: "actual answer" });
    expect(projectRunActivity([internal, echo, user, reply])).toMatchObject([
      { seq: 3, event_type: "user.steer", payload: { text: "actual follow-up" } },
      { seq: 4, event_type: "output.snapshot", payload: { text: "actual answer" } }
    ]);
    // Display filtering must not alter the sequence-bearing source history.
    expect(internal.payload.text).toBe("private generated instruction");
  });

  it("keeps OpenCode reasoning and text in separate stable parts", () => {
    const events = [
      event(1, "provider.event", {
        kind: "reasoning",
        part_id: "reasoning:msg_1:0",
        message_id: "msg_1",
        delta: "先核对"
      }),
      event(2, "output.delta", {
        part_id: "text:msg_1:0",
        message_id: "msg_1",
        text: "回答"
      })
    ];
    const rows = materializeRunActivity(events);
    // Two distinct parts: the reasoning stream never merges into the reply.
    expect(rows).toHaveLength(2);
    const reasoning = rows.find((row) => row.payload.part_id === "reasoning:msg_1:0");
    expect(reasoning).toMatchObject({ payload: { kind: "reasoning", text: "先核对" } });
    const textRow = rows.find((row) => row.payload.part_id === "text:msg_1:0");
    expect(textRow).toMatchObject({ event_type: "provider.event", payload: { kind: "text", text: "回答" } });
  });
  it("streams reasoning in its own stable part before the final snapshot", () => {
    const events = [
      event(1, "provider.event", {
        kind: "reasoning",
        part_id: "reasoning:msg_1:0",
        message_id: "msg_1",
        delta: "先核对"
      }),
      event(2, "provider.event", {
        kind: "reasoning",
        part_id: "reasoning:msg_1:0",
        message_id: "msg_1",
        delta: "参数"
      }),
      event(3, "provider.event", {
        kind: "reasoning",
        part_id: "reasoning:msg_1:0",
        message_id: "msg_1",
        text: "先核对参数"
      })
    ];
    expect(materializeRunActivity(events.slice(0, 2))[0]).toMatchObject({
      seq: 1,
      payload: { kind: "reasoning", part_id: "reasoning:msg_1:0", text: "先核对参数" }
    });
    const projected = projectRunActivity(materializeRunActivity(events));
    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({
      event_type: "reasoning.snapshot",
      payload: { id: "reasoning:msg_1:0", status: "completed", text: "先核对参数" }
    });
  });

  it("keeps separate replies across tools instead of a run-wide snapshot switch", () => {
    const events = [
      codexEvent(1, "provider.item", { item: { id: "a", type: "agentMessage", text: "" } }),
      codexEvent(2, "output.delta", { item_id: "a", text: "before tool" }),
      codexEvent(3, "provider.item", {
        item: { id: "a", type: "agentMessage", text: "before tool" },
        completedAtMs: 10
      }),
      codexEvent(4, "provider.item", {
        item: { id: "cmd", type: "commandExecution", command: "echo test", status: "running" }
      }),
      codexEvent(5, "provider.event", {
        itemId: "cmd",
        delta: "test\n",
        method: "item/commandExecution/outputDelta"
      }),
      codexEvent(6, "output.delta", { item_id: "b", text: "after tool" })
    ];
    let materialized = materializeRunActivity(events);
    expect(materialized.map((e) => (e.payload.item as Record<string, unknown>).text)).toEqual([
      "before tool",
      undefined,
      "after tool"
    ]);
    expect(materialized[1]).toMatchObject({ payload: { item: { aggregatedOutput: "test\n" } } });
    events.push(
      codexEvent(7, "provider.item", {
        item: { id: "b", type: "agentMessage", text: "after tool" }
      })
    );
    materialized = materializeRunActivity([...events].reverse());
    expect(materialized).toHaveLength(3);
    expect(materialized[2]).toMatchObject({ seq: 6, payload: { item: { text: "after tool" } } });
  });

  it("does not collapse different collaboration tools sharing a request filename", () => {
    const events = [
      event(1, "provider.tool", {
        id: "req-stream",
        provider: "teamva",
        tool: "issue_get",
        status: "running",
        input: {}
      }),
      event(2, "provider.tool", {
        id: "req-stream",
        provider: "teamva",
        tool: "issue_get",
        status: "completed",
        output: { title: "test" }
      }),
      event(3, "provider.tool", {
        id: "req-stream",
        provider: "teamva",
        tool: "issue_comment_create",
        status: "completed",
        output: { comment_id: "comment" }
      })
    ];
    const rows = materializeRunActivity([...events, events[1]]);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      seq: 1,
      payload: { input: {}, status: "completed", output: { title: "test" } }
    });
    expect(rows[0].payload.id).not.toBe(rows[1].payload.id);
  });

  it("keeps native IPC calls as ordinary tools with independent identities, details and statuses", () => {
    const rows = projectRunActivity([
      event(1, "provider.event", {
        kind: "tool", name: "write", call_id: "ipc-write", phase: "called",
        input: { path: "/Users/private/.teamva/tool-requests/run/issue_get/req.json", body: "private-request-marker" }
      }),
      event(2, "provider.event", {
        kind: "tool", name: "write", call_id: "ipc-write", phase: "failed",
        output: "private-response-marker", error: "private-error-marker"
      }),
      event(3, "provider.tool", {
        provider: "teamva", id: "req", tool: "issue_get", status: "completed",
        input: { summary: "Read the issue" }, output: { title: "Visible business result" }
      }),
      event(4, "provider.event", {
        kind: "tool", name: "read", call_id: "ipc-read", phase: "success",
        input: { path: ".teamva/tool-results/run/issue_get/result.json" }, output: "tool result"
      })
    ]);
    expect(rows).toHaveLength(3);
    expect(rows[0].payload).toMatchObject({
      id: "write:ipc-write", tool: "write", status: "failed",
      input: { path: "/Users/private/.teamva/tool-requests/run/issue_get/req.json", body: "private-request-marker" },
      output: "private-response-marker", error: "private-error-marker"
    });
    expect(rows[1].payload.output).toEqual({ title: "Visible business result" });
    expect(rows[2].payload).toMatchObject({ id: "read:ipc-read", tool: "read", status: "completed", output: "tool result" });
    expect(rows.every((row) => !("internal_transport" in row.payload))).toBe(true);
  });

  it("keeps Codex summary deltas even when its closing item does not disclose text", () => {
    const events = [
      codexEvent(1, "provider.item", {
        item: { id: "r", type: "reasoning", summary: [] },
        startedAtMs: 10
      }),
      codexEvent(2, "provider.event", {
        itemId: "r",
        summaryIndex: 0,
        delta: "Visible summary",
        method: "item/reasoning/summaryTextDelta"
      }),
      codexEvent(3, "provider.item", {
        item: { id: "r", type: "reasoning", summary: [] },
        completedAtMs: 20
      })
    ]
      .map((e) => projectActivityRunEvent(e)!)
      .filter(Boolean);
    expect(materializeRunActivity(events)[0]).toMatchObject({
      payload: { item: { status: "completed", summary: ["Visible summary"] } }
    });
  });

  it("marks an OpenCode tool failure from its phase instead of treating completion as success", () => {
    const rows = materializeRunActivity([
      event(1, "provider.event", {
        kind: "tool",
        phase: "failed",
        call_id: "call_1",
        name: "bash",
        input: { command: "exit 3" },
        error: "expected error"
      })
    ]);
    expect(rows[0]).toMatchObject({
      payload: { kind: "tool", phase: "failed", call_id: "call_1" }
    });
    const projected = projectRunActivity(rows);
    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({
      event_type: "provider.tool",
      payload: { status: "failed", tool: "bash", error: "expected error" }
    });
  });

  it("shows native V2 final stdout after ingress and preserves the first tool identity", () => {
    const events = [
      event(1, "provider.event", {
        kind: "tool", phase: "called", call_id: "shell-1", name: "shell",
        input: { command: "printf actual-stdout" }
      }),
      event(2, "provider.event", {
        kind: "tool", phase: "success", call_id: "shell-1", name: "shell",
        content: [{ type: "text", text: "actual-stdout\n" }],
        metadata: { exit: 0, status: "completed", truncated: false }
      })
    ].map((value) => projectActivityRunEvent(value)!);
    const rows = projectRunActivity([...events, events[1]].reverse());
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      id: "event-1", seq: 1, occurred_at: events[0].occurred_at, event_type: "provider.tool",
      payload: { id: "shell:shell-1", tool: "shell", input: { command: "printf actual-stdout" },
        output: "actual-stdout\n", status: "completed", exit_code: 0 }
    });
  });

  it.each([
    { phase: "success", metadata: { exit: 3, status: "completed" }, status: "failed", exit: 3 },
    { phase: "success", metadata: { exit: 0, status: "completed" }, status: "completed", exit: 0 },
    { phase: "success", metadata: { exit: 0, status: "failed" }, status: "failed", exit: 0 },
    { phase: "success", metadata: { exit: null, status: "interrupted" }, status: "interrupted", exit: null },
    { phase: "success", metadata: { exit: null, status: "completed" }, status: "unknown", exit: null },
    { phase: "success", metadata: { exit: null }, status: "unknown", exit: null },
    { phase: "success", metadata: { exit: "3", status: "completed" }, status: "unknown", exit: undefined },
    { phase: "failed", metadata: { exit: 0, status: "completed" }, status: "failed", exit: 0 }
  ])("maps native exit/status without inferring success: $phase $metadata", ({ phase, metadata, status, exit }) => {
    const source = event(1, "provider.event", {
      kind: "tool", phase, call_id: "shell-1", name: "shell",
      content: [{ type: "text", text: "EXIT_CODE=3\n" }], metadata
    });
    const projected = projectRunActivity([projectActivityRunEvent(source)!])[0];
    expect(projected.payload).toMatchObject({ status, output: "EXIT_CODE=3\n" });
    expect(projected.payload.exit_code).toBe(exit);
  });

  it("displays provided progress metadata.output snapshots without inventing stdout deltas", () => {
    const events = [
      event(1, "provider.event", { kind: "tool", phase: "called", call_id: "shell-1", name: "shell", input: { command: "test" }, text: "not stdout" }),
      event(2, "provider.event", { kind: "tool", phase: "progress", call_id: "shell-1", name: "shell", metadata: { output: "first\n" } }),
      event(3, "provider.event", { kind: "tool", phase: "progress", call_id: "shell-1", name: "shell", metadata: { output: "first\nsecond\n" } }),
      event(4, "provider.event", { kind: "tool", phase: "success", call_id: "shell-1", name: "shell", metadata: { exit: 3, status: "completed" } })
    ].map((value) => projectActivityRunEvent(value)!);
    expect(projectRunActivity(events.slice(0, 1))[0].payload.output).toBeUndefined();
    expect(projectRunActivity(events.slice(0, 2))[0].payload).toMatchObject({ output: "first\n", status: "running" });
    expect(projectRunActivity(events.slice(0, 3))[0].payload).toMatchObject({ output: "first\nsecond\n", status: "running" });
    expect(projectRunActivity(events)[0].payload).toMatchObject({ output: "first\nsecond\n", status: "failed", exit_code: 3 });
    const final = projectActivityRunEvent(event(5, "provider.event", {
      kind: "tool", phase: "success", call_id: "shell-1", name: "shell",
      content: [{ type: "text", text: "final stdout\n" }], metadata: { exit: 3, status: "completed" }
    }))!;
    expect(projectRunActivity([...events, final])[0].payload.output).toBe("final stdout\n");
  });

  it("does not merge same-call tools across Runs or other native tool calls", () => {
    const tool = event(1, "provider.event", {
      kind: "tool", phase: "success", call_id: "shell-1", name: "shell",
      content: [{ type: "text", text: "one result" }], metadata: { exit: 0, status: "completed" }
    });
    const rows = projectRunActivity([
      tool,
      { ...tool, id: "other-run-event", run_id: "other-run" },
      { ...tool, id: "other-call-event", seq: 2, payload: { ...tool.payload, call_id: "shell-2" } }
    ].map((value) => projectActivityRunEvent(value)!));
    expect(rows).toHaveLength(3);
    expect(rows.filter((row) => row.run_id === "run").map((row) => row.payload.id)).toEqual(["shell:shell-1", "shell:shell-2"]);
  });

  it("keeps one OpenCode reply when deltas and the final text both arrive", () => {
    const opencodeEvent = (seq: number, type: string, payload: Record<string, unknown>) =>
      event(seq, type, { ...payload, provider_id: "opencode" });
    const events = [
      opencodeEvent(1, "output.delta", {
        part_id: "text:msg_1:0",
        message_id: "msg_1",
        text: "补充"
      }),
      opencodeEvent(2, "output.delta", {
        part_id: "text:msg_1:0",
        message_id: "msg_1",
        text: "轮已完成"
      }),
      opencodeEvent(3, "provider.event", {
        kind: "text",
        part_id: "text:msg_1:0",
        message_id: "msg_1",
        text: "补充轮已完成"
      })
    ];
    const materialized = materializeRunActivity(events);
    expect(materialized).toHaveLength(1);
    const projected = projectRunActivity(materialized);
    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({
      event_type: "output.snapshot",
      payload: { id: "text:msg_1:0", text: "补充轮已完成" }
    });
  });
});
