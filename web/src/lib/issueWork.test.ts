import { describe, expect, it } from "vitest";
import { copyIssueWorkItems, maximumIssueWorkPlanBytes, newIssueWorkItem, readIssueWorkPlan, validateIssueWorkItems, workPlannerAgents } from "./issueWork";
import type { Agent, AgentService, IssueWorkItemSpec } from "./types";

const item = (key = "a", patch: Partial<IssueWorkItemSpec> = {}): IssueWorkItemSpec => ({ key, agent_id: "agent", title: "Work", instruction: "", after: [], ...patch });

describe("issue work plans", () => {
  it("uses stable distinct work keys, supports repeated owners and copies dependencies", () => {
    expect(newIssueWorkItem("agent").key).not.toBe(newIssueWorkItem("agent").key);
    const items = [item(), item("b", { after: ["a"] })];
    expect(validateIssueWorkItems(items, ["agent"])).toBeUndefined();
    const copied = copyIssueWorkItems(items);
    copied[1].after.push("c");
    expect(items[1].after).toEqual(["a"]);
  });

  it("validates item count, identities, Unicode titles and UTF-8 instruction bytes", () => {
    expect(validateIssueWorkItems([], ["agent"])).toBe("empty");
    expect(validateIssueWorkItems(Array.from({ length: 33 }, (_, i) => item(String(i))), ["agent"])).toBe("tooMany");
    expect(validateIssueWorkItems([item(" ")], ["agent"])).toBe("key");
    expect(validateIssueWorkItems([item(), item()], ["agent"])).toBe("duplicateKey");
    expect(validateIssueWorkItems([item()], [])).toBe("agent");
    expect(validateIssueWorkItems([item("a", { title: " " })], ["agent"])).toBe("title");
    expect(validateIssueWorkItems([item("a", { title: "😀".repeat(120), instruction: "😀".repeat(4096) })], ["agent"])).toBeUndefined();
    expect(validateIssueWorkItems([item("a", { title: "😀".repeat(121) })], ["agent"])).toBe("title");
    expect(validateIssueWorkItems([item("a", { instruction: "😀".repeat(4096) + "a" })], ["agent"])).toBe("instruction");
  });

  it("rejects unknown and duplicate dependency keys, self-dependency and indirect cycles", () => {
    expect(validateIssueWorkItems([item("a", { after: ["missing"] })], ["agent"])).toBe("dependency");
    expect(validateIssueWorkItems([item(), item("b", { after: ["a", "a"] })], ["agent"])).toBe("dependency");
    expect(validateIssueWorkItems([item("a", { after: ["a"] })], ["agent"])).toBe("cycle");
    expect(validateIssueWorkItems([item("a", { after: ["c"] }), item("b", { after: ["a"] }), item("c", { after: ["b"] })], ["agent"])).toBe("cycle");
  });

  it("validates generated plans at the response boundary and rejects unknown fields", () => {
    for (const value of [null, {}, { work_items: {}, extra: 1 }, { work_items: [item()], extra: 1 }, { work_items: [null] }, { work_items: [{ ...item(), run_id: "forged" }] }, { work_items: [item("a", { after: [3] as unknown as string[] })] }, { work_items: [item("a", { agent_id: "other" })] }, { work_items: [item(), item()] }]) {
      expect(readIssueWorkPlan(value, ["agent"])).toBeUndefined();
    }
    expect(readIssueWorkPlan({ work_items: [item()] }, ["agent"])).toEqual([item()]);
  });

  it("only offers current configured active agents with a ready authenticated no-tools service", () => {
    const agent = (id: string, service: string, status: Agent["status"] = "active") => ({ id, name: id, status, agent_service_id: service, current_revision_id: `revision-${id}` } as Agent);
    const service = (id: string, noTools: unknown): AgentService => ({ id, computer_id: "computer", provider: id, provider_version: "fixture", login_status: "authenticated", status: "ready", capabilities: { no_tools: noTools }, capability_version: 1, version: 1, created_at: "2026-11-02T00:00:00Z", updated_at: "2026-11-02T00:00:00Z" });
    const services = [service("opencode", true), service("codex", false), service("string", "true"), { ...service("offline", true), status: "offline" }, { ...service("signed-out", true), login_status: "unauthenticated" }];
    expect(workPlannerAgents([agent("planner", "opencode"), agent("executor", "codex"), agent("paused", "opencode", "disabled"), agent("not-configured", ""), agent("wrong-capability", "string"), agent("offline", "offline"), agent("signed-out", "signed-out"), { ...agent("no-revision", "opencode"), current_revision_id: "" }], services).map((agent) => agent.id)).toEqual(["planner"]);
  });

  it("mirrors the protocol single-line and valid-text rules without normalizing invalid input", () => {
    for (const text of ["a\nb", "a\rb", "a\tb", "a\u0085b", "a\u200bb", "a\u2028b", "a\u2029b", "a\uFFFDb", "a\ud800b", "a\udc00b"]) {
      expect(validateIssueWorkItems([item(text)], ["agent"]), `key ${JSON.stringify(text)}`).toBe("key");
      expect(validateIssueWorkItems([item("a", { title: text })], ["agent"]), `title ${JSON.stringify(text)}`).toBe("title");
      expect(validateIssueWorkItems([item("a", { agent_id: text })], [text]), `agent ${JSON.stringify(text)}`).toBe("agent");
      expect(readIssueWorkPlan({ work_items: [item("a", { title: text })] }, ["agent"])).toBeUndefined();
    }
    expect(validateIssueWorkItems([item(" a")], ["agent"])).toBe("key");
    expect(validateIssueWorkItems([item("a ")], ["agent"])).toBe("key");
    expect(validateIssueWorkItems([item("a", { agent_id: " agent" })], [" agent"])).toBe("agent");
    for (const text of ["\u0000", "\u000b", "\u0085", "\uFFFD", "\ud800"]) expect(validateIssueWorkItems([item("a", { instruction: text })], ["agent"])).toBe("instruction");
    expect(validateIssueWorkItems([item("a", { title: " 😀 ", instruction: "\n\r\t😀\u200b\u2028\u2029" })], ["agent"])).toBeUndefined();
  });

  it("bounds the whole JSON at exactly 128 KiB, including Go JSON escaping", () => {
    const items = Array.from({ length: 8 }, (_, index) => item(String(index), { instruction: index < 7 ? "x".repeat(16_384) : "" }));
    const overhead = new TextEncoder().encode(JSON.stringify({ work_items: items })).length;
    items[7].instruction = "x".repeat(maximumIssueWorkPlanBytes - overhead);
    expect(validateIssueWorkItems(items, ["agent"])).toBeUndefined();
    items[7].instruction += "x";
    expect(validateIssueWorkItems(items, ["agent"])).toBe("tooLarge");
    expect(readIssueWorkPlan({ work_items: items }, ["agent"])).toBeUndefined();
    for (const character of ["<", ">", "&", "\u2028", "\u2029"]) {
      const escaping = Array.from({ length: 9 }, (_, index) => item(String(index), { instruction: character.repeat(2500) }));
      expect(validateIssueWorkItems(escaping, ["agent"]), character).toBe("tooLarge");
      expect(readIssueWorkPlan({ work_items: escaping }, ["agent"])).toBeUndefined();
    }
  });
});
