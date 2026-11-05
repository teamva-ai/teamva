import { test } from "node:test";
import assert from "node:assert/strict";
import { normalizeDisplayText, toolOutput, firstPhasedTool } from "./streaming-text.mjs";

test("normalizes Markdown list markers that are CSS markers rather than textContent", () => {
  assert.equal(normalizeDisplayText("方案比较：\n1. **力导向**：自然。\n2. 层次：清晰。"), normalizeDisplayText("方案比较：力导向：自然。层次：清晰。"));
});
test("does not hide missing response text or stream identifiers", () => {
  assert.notEqual(normalizeDisplayText("段01：完整内容\nSTREAM_FINAL_END"), normalizeDisplayText("段01：内容\nSTREAM_FINAL_END"));
});

test("does not mistake command arguments or reasoning for stdout", () => {
  const command = "STREAM_TOOL_A STREAM_TOOL_B";
  const rows = [
    { kind: "reasoning", text: command, detail: "" },
    { kind: "tool", text: command, detail: `参数\n${command}\n\n返回\nSTREAM_TOOL_B` },
    { kind: "tool", text: "publish summary", detail: `返回\n${command}` }
  ];
  assert.equal(toolOutput(firstPhasedTool(rows)), "STREAM_TOOL_B");
});

test("extracts both supported tool-detail presentations", () => {
  assert.equal(toolOutput({ kind: "tool", detail: '{"input":{"command":"x"},"output":"A"}' }), '"A"');
  assert.equal(toolOutput({ kind: "tool", detail: '参数\n{}\n\n返回\nA\n\n{"exit_code":0}' }), "A");
});
