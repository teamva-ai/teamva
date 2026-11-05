import { describe, expect, it } from "vitest";
import { MAX_ACTIVITY_EVENT_BYTES, projectActivityRunEvent } from "./activityEvents";
import type { RunEvent } from "./types";

const nativeTool = (payload: Record<string, unknown>): RunEvent => ({
  id: "tool-event", run_id: "run", seq: 1, event_type: "provider.event",
  occurred_at: "2026-10-23T00:00:00Z",
  payload: { provider_id: "opencode", kind: "tool", call_id: "shell-1", name: "shell", phase: "success", ...payload }
});

describe("OpenCode native display restoration at the activity ingress", () => {
  it("restores V2 text content as scalar tool output without retaining its raw blocks", () => {
    const source = nativeTool({
      input: { command: "printf actual-stdout" },
      content: [{ type: "text", text: "actual-stdout\n" }],
      metadata: { exit: 0, status: "completed", truncated: false }
    });
    const projected = projectActivityRunEvent(source)!;
    expect(projected.payload).toMatchObject({
      output: "actual-stdout\n", metadata: { exit: 0, status: "completed", truncated: false }
    });
    expect(projected.payload).not.toHaveProperty("content");
    expect(projected.payload.metadata).not.toBe(source.payload.metadata);
    expect(source.payload.content).toEqual([{ type: "text", text: "actual-stdout\n" }]);
    expect(projectActivityRunEvent(projected)?.payload.output).toBe("actual-stdout\n");
  });

  it("copies only text from native text blocks, not binary, nested or secret block fields", () => {
    const projected = projectActivityRunEvent(nativeTool({
      content: [
        { type: "text", text: "safe ", secret: "BLOCK-SECRET", data: "BLOCK-DATA" },
        { type: "image", text: "NOT-STDOUT", data: "BINARY-DATA", content: [{ type: "text", text: "NESTED" }] },
        { type: "text", text: "stdout", raw: "RAW-CONTENT", base64: "BASE64" },
        { text: "UNTYPED" }
      ],
      input: { content: "PRIVATE-INPUT", data: "INPUT-DATA", command: "echo safe" },
      metadata: { data: "METADATA-DATA", exit: 0, status: "completed" }
    }))!;
    expect(projected.payload.output).toBe("safe stdout");
    const retained = JSON.stringify(projected);
    for (const marker of ["BLOCK-SECRET", "BLOCK-DATA", "BINARY-DATA", "NESTED", "RAW-CONTENT", "BASE64", "PRIVATE-INPUT", "INPUT-DATA", "METADATA-DATA"]) {
      expect(retained).not.toContain(marker);
    }
    for (const key of ["content", "data", "raw", "base64"]) expect(retained).not.toContain(`"${key}":`);
  });

  it("bounds long native output without weakening the global cache budgets", () => {
    const projected = projectActivityRunEvent(nativeTool({
      content: [{ type: "text", text: "x".repeat(20 * 1024 * 1024) }]
    }))!;
    expect(typeof projected.payload.output).toBe("string");
    expect((projected.payload.output as string).length).toBeLessThanOrEqual(16 * 1024);
    expect((projected.payload.output as string).endsWith("…")).toBe(true);
    expect(JSON.stringify(projected).length).toBeLessThan(MAX_ACTIVITY_EVENT_BYTES);

    const exhausted = projectActivityRunEvent(nativeTool({
      retained: Array.from({ length: 8 }, () => "b".repeat(16 * 1024)),
      content: [{ type: "text", text: "MUST-NOT-BYPASS-BUDGET" }]
    }))!;
    expect(exhausted.payload.output).toBe("");
    expect(JSON.stringify(exhausted)).not.toContain("MUST-NOT-BYPASS-BUDGET");
  });

  it("does not promote input, progress text or reasoning to tool output", () => {
    expect(projectActivityRunEvent(nativeTool({
      phase: "progress", input: { command: "not output" }, text: "progress description"
    }))?.payload).not.toHaveProperty("output");
    const reasoning = projectActivityRunEvent(nativeTool({
      kind: "reasoning", part_id: "reasoning-1", content: [{ type: "text", text: "thinking" }]
    }))!;
    expect(reasoning.payload.text).toBe("thinking");
    expect(reasoning.payload).not.toHaveProperty("output");
  });
});
