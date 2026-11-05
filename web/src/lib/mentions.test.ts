import { describe, expect, it } from "vitest";
import { insertAgentMention, mentionQueryAt, reconcileMentionRanges, resolveMentionedAgentIds } from "./mentions";
import type { Agent } from "./types";

const agent = (id: string, name = "Builder", status: Agent["status"] = "active"): Agent => ({
  id, name, status, agent_service_id: "service", computer_id: "computer", version: 1,
  created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
});

describe("structured composer mentions", () => {
  it("keeps the selected identity when names are duplicated, including after surrounding edits", () => {
    const first = agent("first");
    const second = agent("second");
    const selected = insertAgentMention("@", [], 0, 1, second);
    expect(resolveMentionedAgentIds(selected.text, selected.ranges, [first, second])).toEqual(["second"]);
    const text = `请 ${selected.text}继续`;
    const ranges = reconcileMentionRanges(selected.text, text, selected.ranges);
    // A single browser input can replace the entire string; an ordinary prefix
    // insertion and suffix append are separate edits in the actual editor.
    expect(ranges).toEqual([]);
    const prefixed = `请 ${selected.text}`;
    const shifted = reconcileMentionRanges(selected.text, prefixed, selected.ranges);
    const appended = `${prefixed}继续`;
    const retained = reconcileMentionRanges(prefixed, appended, shifted);
    expect(resolveMentionedAgentIds(appended, retained, [first, second])).toEqual(["second"]);
    expect(resolveMentionedAgentIds("@Builder ", [], [first, second])).toEqual([]);
  });

  it("never authorizes email, code, escaped, URL, partial, disabled, or unconfigured mentions", () => {
    const builder = agent("builder");
    const disabled = agent("disabled", "Paused", "disabled");
    const unconfigured = { ...agent("lumi", "Lumi"), agent_service_id: "" };
    const texts = [
      "owner@Builder", "owner@Builder.example.test", "prefix@Builder ", "@BuilderSuffix",
      "`@Builder`", "```ts\n@Builder\n```", "    @Builder", "\\@Builder",
      "https://example.test/@Builder", "[@Builder](https://example.test)", "@Paused @Lumi"
    ];
    for (const text of texts) expect(resolveMentionedAgentIds(text, [], [builder, disabled, unconfigured]), text).toEqual([]);
    expect(mentionQueryAt("owner@Builder", 13)).toBeUndefined();
    expect(mentionQueryAt("`@Builder`", 9)).toBeUndefined();
    expect(resolveMentionedAgentIds("请 (@Builder)，继续", [], [builder])).toEqual(["builder"]);
  });

  it("invalidates an edited selection and never substitutes the first same-name agent", () => {
    const agents = [agent("first"), agent("second")];
    const selected = insertAgentMention("", [], 0, 0, agents[1]);
    const edited = "@BuilXder ";
    expect(reconcileMentionRanges(selected.text, edited, selected.ranges)).toEqual([]);
    const suffixed = "@BuilderSuffix ";
    expect(reconcileMentionRanges(selected.text, suffixed, selected.ranges)).toEqual([]);
    const replaced = insertAgentMention(selected.text, selected.ranges, 0, selected.text.length, agents[0]);
    expect(resolveMentionedAgentIds(replaced.text, replaced.ranges, agents)).toEqual(["first"]);
  });

  it("supports longest spaced names without using a shorter fallback for an ambiguous name", () => {
    const agents = [agent("short", "Codex"), agent("long", "Codex 架构师")];
    expect(resolveMentionedAgentIds("请 @Codex 架构师 看一下", [], agents)).toEqual(["long"]);
    expect(resolveMentionedAgentIds("请 @Codex 架构师 看一下", [], [...agents, agent("other", "Codex 架构师")])).toEqual([]);
  });
});
