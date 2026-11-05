import { fromMarkdown } from "mdast-util-from-markdown";
import type { Agent } from "./types";

/** Offsets cover the visible @name, not the following separator. */
export interface MentionRange {
  agentId: string;
  name: string;
  start: number;
  end: number;
}

export function canMentionAgent(agent: Agent): boolean {
  return agent.status === "active" && Boolean(agent.agent_service_id?.trim()) &&
    Boolean(agent.name.trim()) && !/[\r\n@]/.test(agent.name);
}

const leftBoundary = /[\s([{"'“‘（【「『，。,.!?！？；;：:、]/u;
const rightBoundary = /[\s)\]}"'”’）】」』，。,.!?！？；;：:、]/u;

export function hasMentionBoundaries(text: string, start: number, end: number): boolean {
  return (start === 0 || leftBoundary.test(text[start - 1])) &&
    (end === text.length || rightBoundary.test(text[end]));
}

interface MarkdownNode {
  type: string;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: MarkdownNode[];
}

function protectedRanges(text: string): Array<{ start: number; end: number }> {
  const ranges: Array<{ start: number; end: number }> = [];
  const visit = (node: MarkdownNode) => {
    if (["code", "inlineCode", "html", "link", "image"].includes(node.type)) {
      const start = node.position?.start.offset;
      const end = node.position?.end.offset;
      if (start !== undefined && end !== undefined) ranges.push({ start, end });
      return;
    }
    node.children?.forEach(visit);
  };
  visit(fromMarkdown(text));
  return ranges;
}

function overlaps(a: { start: number; end: number }, b: { start: number; end: number }) {
  return a.start < b.end && b.start < a.end;
}

export function validMentionRanges(text: string, ranges: MentionRange[]): MentionRange[] {
  const protectedText = protectedRanges(text);
  return ranges.filter((range) => range.start >= 0 && range.end <= text.length &&
    text.slice(range.start, range.end) === `@${range.name}` &&
    hasMentionBoundaries(text, range.start, range.end) &&
    !protectedText.some((protectedRange) => overlaps(range, protectedRange)));
}

/** Keep identities through edits before/after a selection; edits inside it invalidate it. */
export function reconcileMentionRanges(previous: string, next: string, ranges: MentionRange[]): MentionRange[] {
  if (previous === next) return validMentionRanges(next, ranges);
  let start = 0;
  while (start < previous.length && start < next.length && previous[start] === next[start]) start++;
  let previousEnd = previous.length;
  let nextEnd = next.length;
  while (previousEnd > start && nextEnd > start && previous[previousEnd - 1] === next[nextEnd - 1]) {
    previousEnd--;
    nextEnd--;
  }
  const delta = nextEnd - previousEnd;
  const shifted = ranges.flatMap((range) => {
    if (range.end <= start) return [{ ...range }];
    if (range.start >= previousEnd) return [{ ...range, start: range.start + delta, end: range.end + delta }];
    return [];
  });
  return validMentionRanges(next, shifted);
}

export function insertAgentMention(
  text: string, ranges: MentionRange[], start: number, end: number, agent: Agent
): { text: string; ranges: MentionRange[]; caret: number } {
  if (!canMentionAgent(agent)) return { text, ranges, caret: end };
  const separator = start > 0 && !leftBoundary.test(text[start - 1]) ? " " : "";
  const token = `@${agent.name}`;
  const insertion = `${separator}${token} `;
  const next = text.slice(0, start) + insertion + text.slice(end);
  const delta = insertion.length - (end - start);
  const retained = ranges.flatMap((range) => {
    if (range.end <= start) return [{ ...range }];
    if (range.start >= end) return [{ ...range, start: range.start + delta, end: range.end + delta }];
    return [];
  });
  retained.push({ agentId: agent.id, name: agent.name, start: start + separator.length, end: start + separator.length + token.length });
  return { text: next, ranges: validMentionRanges(next, retained), caret: start + insertion.length };
}

export function mentionQueryAt(text: string, cursor: number): { start: number; query: string } | undefined {
  const before = text.slice(0, cursor);
  const start = before.lastIndexOf("@");
  if (start < 0 || (start > 0 && !leftBoundary.test(text[start - 1]))) return;
  const query = before.slice(start + 1);
  if (/[\r\n@]/.test(query) || query.length > 64) return;
  if (protectedRanges(text).some((range) => start >= range.start && start < range.end)) return;
  return { start, query };
}

/** Explicit selections win. Legacy typed mentions only resolve unique, configured names. */
export function resolveMentionedAgentIds(text: string, ranges: MentionRange[], agents: Agent[]): string[] {
  const protectedText = protectedRanges(text);
  const claimed = validMentionRanges(text, ranges);
  const found: Array<{ start: number; id: string }> = [];
  for (const range of claimed) {
    const agent = agents.find((item) => item.id === range.agentId);
    if (agent && agent.name === range.name && canMentionAgent(agent)) found.push({ start: range.start, id: agent.id });
  }
  const byName = new Map<string, Agent[]>();
  for (const agent of agents) {
    if (!agent.name.trim() || /[\r\n@]/.test(agent.name)) continue;
    byName.set(agent.name, [...(byName.get(agent.name) ?? []), agent]);
  }
  const occupied: Array<{ start: number; end: number }> = [...claimed, ...protectedText];
  for (const [name, namedAgents] of [...byName].sort((a, b) => b[0].length - a[0].length)) {
    const token = `@${name}`;
    let from = 0;
    while (from < text.length) {
      const start = text.indexOf(token, from);
      if (start < 0) break;
      const range = { start, end: start + token.length };
      from = range.end;
      if (!hasMentionBoundaries(text, range.start, range.end) || occupied.some((other) => overlaps(range, other))) continue;
      // Even an ambiguous/disabled long name claims its text; never fall back to a shorter name.
      occupied.push(range);
      if (namedAgents.length === 1 && canMentionAgent(namedAgents[0])) found.push({ start, id: namedAgents[0].id });
    }
  }
  return [...new Set(found.sort((a, b) => a.start - b.start).map((item) => item.id))];
}
