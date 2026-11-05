import { v4 } from "uuid";
import { canMentionAgent } from "./mentions";
import type { Agent, AgentService, IssueWorkItemSpec } from "./types";

export const maximumIssueWorkItems = 32;
export const maximumIssueWorkPlanBytes = 128 * 1024;
export type WorkValidation = "empty" | "tooMany" | "tooLarge" | "key" | "duplicateKey" | "agent" | "title" | "instruction" | "dependency" | "cycle";

export function newIssueWorkItem(agentId: string): IssueWorkItemSpec {
  return { key: v4(), agent_id: agentId, title: "", instruction: "", after: [] };
}

export function copyIssueWorkItems(items: IssueWorkItemSpec[]): IssueWorkItemSpec[] {
  return items.map((item) => ({ ...item, after: [...item.after] }));
}

export function workPlannerAgents(agents: Agent[], services: AgentService[]): Agent[] {
  return agents.filter((agent) => canMentionAgent(agent) && Boolean(agent.current_revision_id?.trim()) && services.some((service) =>
    service.id === agent.agent_service_id && service.status === "ready" && service.login_status === "authenticated" && service.capabilities?.no_tools === true));
}

// Match protocol/node/work_plan.go, including invalid UTF-16 and invisible line separators.
function workPlanLine(text: string): boolean {
  return Boolean(text.trim()) && !/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Cs}\uFFFD]/u.test(text);
}

function workPlanText(text: string): boolean {
  return !/[\p{Cs}\uFFFD]|(?![\n\r\t])\p{Cc}/u.test(text);
}

export function validateIssueWorkItems(items: IssueWorkItemSpec[], agentIds: string[]): WorkValidation | undefined {
  if (!items.length) return "empty";
  if (items.length > maximumIssueWorkItems) return "tooMany";
  if (!agentIds.length || new Set(agentIds).size !== agentIds.length || agentIds.some((id) => !workPlanLine(id) || id.trim() !== id)) return "agent";
  const keys = new Set<string>();
  for (const item of items) {
    if (!workPlanLine(item.key) || item.key.trim() !== item.key) return "key";
    if (keys.has(item.key)) return "duplicateKey";
    keys.add(item.key);
    if (!workPlanLine(item.agent_id) || item.agent_id.trim() !== item.agent_id || !agentIds.includes(item.agent_id)) return "agent";
    if (!workPlanLine(item.title) || Array.from(item.title).length > 120) return "title";
    if (!workPlanText(item.instruction) || new TextEncoder().encode(item.instruction).length > 16_384) return "instruction";
  }
  // Go's json.Marshal also escapes HTML characters and U+2028/U+2029 before counting bytes.
  const encoded = JSON.stringify({ work_items: items }).replace(/[<>&\u2028\u2029]/g, (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`);
  if (new TextEncoder().encode(encoded).length > maximumIssueWorkPlanBytes) return "tooLarge";
  for (const item of items) {
    if (item.after.includes(item.key)) return "cycle";
    if (item.after.some((key) => !keys.has(key)) || new Set(item.after).size !== item.after.length) return "dependency";
  }
  const byKey = new Map(items.map((item) => [item.key, item]));
  const visiting = new Set<string>(), visited = new Set<string>();
  function cycle(key: string): boolean {
    if (visiting.has(key)) return true;
    if (visited.has(key)) return false;
    visiting.add(key);
    if (byKey.get(key)!.after.some(cycle)) return true;
    visiting.delete(key);
    visited.add(key);
    return false;
  }
  if (items.some((item) => cycle(item.key))) return "cycle";
}

/** Treat the generated plan as untrusted input, not an execution instruction. */
export function readIssueWorkPlan(value: unknown, agentIds: string[]): IssueWorkItemSpec[] | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).length !== 1 || !Object.hasOwn(value, "work_items")) return;
  const items = (value as { work_items?: unknown } | null)?.work_items;
  const fields = ["key", "agent_id", "title", "instruction", "after"];
  if (!Array.isArray(items) || !items.every((item) => item && typeof item === "object" && !Array.isArray(item) && Object.keys(item).length === 5 &&
    Object.keys(item).every((field) => fields.includes(field)) &&
    ["key", "agent_id", "title", "instruction"].every((field) => typeof item[field] === "string") &&
    Array.isArray(item.after) && item.after.every((key: unknown) => typeof key === "string"))) return;
  const result = items.map(({ key, agent_id, title, instruction, after }) => ({ key, agent_id, title, instruction, after: [...after] })) as IssueWorkItemSpec[];
  return validateIssueWorkItems(result, agentIds) ? undefined : result;
}
