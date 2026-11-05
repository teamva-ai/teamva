import { api } from "./api";
import type { Page, Run } from "./types";

export const INSTANCE_RUN_PAGE_SIZE = 25;
export const MAX_RETAINED_INSTANCE_RUNS = 200;
export const MAX_RETAINED_INSTANCE_RUN_BYTES = 4 * 1024 * 1024;
const MAX_RETAINED_RUN_INPUT_CHARS = 64 * 1024;

export interface InstanceRunsPage extends Page<Run> {
  history_limited?: boolean;
  content_limited?: boolean;
  retained_bytes: number;
}

export function instanceRunsQueryKey(instanceId: string) {
  return ["instance-runs", instanceId] as const;
}

function boundedText(value: string | undefined, limit: number): string | undefined {
  if (!value || value.length <= limit) return value;
  if (limit <= 0) return "";
  if (limit <= 3) return "…".slice(0, limit);
  const first = Math.floor((limit - 3) / 2);
  const last = limit - 3 - first;
  return `${value.slice(0, first)}\n…\n${value.slice(-last)}`;
}

/** Run input is the only unbounded field needed by the timeline. Keep both
 * edges so an injected prompt retains its header and the final instruction. */
function normalizeInstanceRunWithLimit(run: Run): { run: Run; limited: boolean } {
  let remaining = MAX_RETAINED_RUN_INPUT_CHARS;
  let inputLimited = false;
  const input: Run["input"] = [];
  for (const block of run.input ?? []) {
    if (remaining <= 0) {
      inputLimited = true;
      break;
    }
    const text = block.type === "text" ? block.text ?? "" : "";
    const retained = boundedText(text, remaining) ?? "";
    if (retained.length < text.length) inputLimited = true;
    input.push({ ...block, text: retained || undefined, data_url: undefined });
    remaining -= retained.length;
  }
  if (inputLimited && input.length === 0) input.push({ type: "text", text: "…" });
  const terminalReason = boundedText(run.terminal_reason, 16 * 1024);
  return {
    run: {
      ...run,
      input,
      terminal_reason: terminalReason
    },
    limited: inputLimited || terminalReason !== run.terminal_reason
  };
}

export function instanceRunRetainedBytes(run: Run): number {
  let bytes = 768;
  for (const value of Object.values(run)) {
    if (typeof value === "string") bytes += value.length * 2;
  }
  for (const block of run.input ?? []) {
    bytes += 96 + (block.text?.length ?? 0) * 2 + (block.filename?.length ?? 0) * 2;
  }
  return bytes;
}

export async function fetchInstanceRunsPage(
  instanceId: string,
  cursor = "",
  signal?: AbortSignal
): Promise<InstanceRunsPage> {
  const query = new URLSearchParams({ limit: String(INSTANCE_RUN_PAGE_SIZE) });
  if (cursor) query.set("cursor", cursor);
  const page = await api<Page<Run>>(
    `/agent-instances/${encodeURIComponent(instanceId)}/runs?${query}`,
    { signal }
  );
  // The raw response must die in this stack frame. In particular, never hand a
  // Page containing Provider-sized input strings to TanStack Query.
  return compactInstanceRunsPage(page);
}

/** Compacts one wire page before it is eligible for any Query cache. */
export function compactInstanceRunsPage(page: Page<Run>): InstanceRunsPage {
  return mergeInstanceRunPages(undefined, page, { nextCursor: page.next_cursor });
}

/**
 * Merges one explicit page while enforcing the session's retained Run budget.
 * `prepend` is used by the recent-run poll; `append` follows the older cursor.
 */
export function mergeInstanceRunPages(
  current: InstanceRunsPage | undefined,
  incoming: Page<Run>,
  options: { mode?: "append" | "prepend"; nextCursor?: string } = {}
): InstanceRunsPage {
  const mode = options.mode ?? "append";
  let normalizedFieldLimited = false;
  const normalizedIncoming = incoming.items.map((item) => {
    const normalized = normalizeInstanceRunWithLimit(item);
    normalizedFieldLimited ||= normalized.limited;
    return normalized.run;
  });
  const currentItems = current?.items ?? [];
  const order = mode === "prepend"
    ? [...normalizedIncoming, ...currentItems]
    : [...currentItems, ...normalizedIncoming];
  const latestByID = new Map<string, Run>();
  for (const item of [...currentItems, ...normalizedIncoming]) {
    const existing = latestByID.get(item.id);
    // A polling response can arrive after a newer SSE/HTTP terminal update.
    // Never resurrect a completed Run using an older revision.
    if (!existing || item.version >= existing.version) latestByID.set(item.id, item);
  }

  const retained: Run[] = [];
  const seen = new Set<string>();
  let retainedBytes = 0;
  let clipped = false;
  for (const ordered of order) {
    if (seen.has(ordered.id)) continue;
    seen.add(ordered.id);
    const item = latestByID.get(ordered.id) ?? ordered;
    const weight = instanceRunRetainedBytes(item);
    if (retained.length >= MAX_RETAINED_INSTANCE_RUNS ||
      retainedBytes + weight > MAX_RETAINED_INSTANCE_RUN_BYTES) {
      clipped = true;
      continue;
    }
    retained.push(item);
    retainedBytes += weight;
  }

  const requestedNextCursor = options.nextCursor ?? incoming.next_cursor;
  const incomingPage = incoming as Partial<InstanceRunsPage>;
  const historyLimited = Boolean(current?.history_limited || incomingPage.history_limited || clipped);
  const contentLimited = Boolean(current?.content_limited || incomingPage.content_limited || normalizedFieldLimited);
  return {
    items: retained,
    next_cursor: historyLimited ? undefined : requestedNextCursor,
    history_limited: historyLimited || undefined,
    content_limited: contentLimited || undefined,
    retained_bytes: retainedBytes
  };
}
