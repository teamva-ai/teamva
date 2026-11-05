import type { RunEvent } from "./types";
import { viewsForPayload } from "./providers";
import { diagnosticText, isDiagnosticEvent, sanitizeDiagnosticPayload } from "./runDiagnostics";

export const MAX_ACTIVITY_EVENTS = 10_000;
export const MAX_ACTIVITY_BUFFER_BYTES = 4 * 1024 * 1024;
export const MAX_ACTIVITY_EVENT_BYTES = 64 * 1024;

const MAX_ACTIVITY_ARRAY_ITEMS = 100;
const MAX_ACTIVITY_DEPTH = 5;
const MAX_ACTIVITY_KEYS = 64;
const MAX_ACTIVITY_STRING = 16 * 1024;
const BLOCKED_ACTIVITY_KEYS = new Set([
  "patch", "diff", "content", "data", "raw", "raw_payload", "payload_bytes",
  "base64", "data_url", "image", "images", "blob", "bytes"
]);

interface ProjectionBudget {
  remaining: number;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function boundedString(value: string, budget: ProjectionBudget, maxLength = MAX_ACTIVITY_STRING): string {
  if (budget.remaining <= 0) return "";
  const limit = Math.min(maxLength, budget.remaining);
  const result = value.length <= limit ? value : `${value.slice(0, Math.max(0, limit - 1))}…`;
  budget.remaining -= result.length;
  return result;
}

function activityValue(value: unknown, budget: ProjectionBudget, depth: number): unknown {
  if (budget.remaining <= 0 || depth > MAX_ACTIVITY_DEPTH) return undefined;
  if (typeof value === "string") return boundedString(value, budget);
  if (typeof value === "number") return Number.isFinite(value) ? value : undefined;
  if (typeof value === "boolean" || value === null) return value;
  if (Array.isArray(value)) {
    const result: unknown[] = [];
    for (const item of value.slice(0, MAX_ACTIVITY_ARRAY_ITEMS)) {
      const projected = activityValue(item, budget, depth + 1);
      if (projected !== undefined) result.push(projected);
      if (budget.remaining <= 0) break;
    }
    return result;
  }
  const source = record(value);
  if (!source) return undefined;
  const result: Record<string, unknown> = {};
  let count = 0;
  for (const [key, item] of Object.entries(source)) {
    if (count >= MAX_ACTIVITY_KEYS || budget.remaining <= 0) break;
    if (BLOCKED_ACTIVITY_KEYS.has(key.toLocaleLowerCase())) continue;
    const projected = activityValue(item, budget, depth + 1);
    if (projected === undefined) continue;
    result[key] = projected;
    count += 1;
  }
  return result;
}

function contentText(value: unknown, budget = MAX_ACTIVITY_STRING): string {
  if (budget <= 0) return "";
  if (typeof value === "string") return value.slice(0, budget);
  if (Array.isArray(value)) {
    let result = "";
    for (const item of value.slice(0, MAX_ACTIVITY_ARRAY_ITEMS)) {
      const next = contentText(item, budget - result.length);
      result += next;
      if (result.length >= budget) break;
    }
    return result;
  }
  const source = record(value);
  if (!source) return "";
  if (typeof source.text === "string") return source.text.slice(0, budget);
  if (typeof source.output_text === "string") return source.output_text.slice(0, budget);
  return contentText(source.content, budget);
}

function restoreDisplayText(
  source: Record<string, unknown>,
  target: Record<string, unknown>,
  budget: ProjectionBudget,
  maxLength = MAX_ACTIVITY_STRING
): void {
  const previous = typeof target.text === "string" ? target.text : "";
  if (previous && maxLength === MAX_ACTIVITY_STRING) return;
  const text = typeof source.text === "string" && source.text
    ? source.text
    : contentText(source.content, Math.min(maxLength, budget.remaining + previous.length));
  if (!text) return;
  // Refund only the scalar being replaced. Identity/metadata has already been
  // copied with the ordinary limits, so a large body cannot crowd it out.
  budget.remaining += previous.length;
  target.text = boundedString(text, budget, maxLength);
}

/**
 * Turns one SSE value into the only shape Activity is allowed to retain.
 * No object from the parsed source is reused. Patch/diff/content/data and
 * binary-like fields are dropped recursively, while display text is copied
 * into a bounded scalar. This projection must happen before any cache update.
 */
export function projectActivityRunEvent(
  value: unknown,
  expectedRunId = ""
): RunEvent | undefined {
  const source = record(value);
  if (!source) return undefined;
  const id = typeof source.id === "string" ? source.id : "";
  const runId = typeof source.run_id === "string" ? source.run_id : "";
  const eventType = typeof source.event_type === "string" ? source.event_type : "";
  const occurredAt = typeof source.occurred_at === "string" ? source.occurred_at : "";
  const sequence = typeof source.seq === "number" && Number.isSafeInteger(source.seq)
    ? source.seq
    : Number.NaN;
  if (!id || !runId || !eventType || !occurredAt || sequence < 1 ||
    (expectedRunId && runId !== expectedRunId)) return undefined;

  let sourcePayload = record(source.payload) ?? {};
  if (isDiagnosticEvent(eventType, sourcePayload)) {
    // Native error.data is deliberately not cached as arbitrary data. Extract
    // its safe human diagnostic before the bounded projection drops that key.
    sourcePayload = sanitizeDiagnosticPayload(sourcePayload);
    const message = diagnosticText(sourcePayload);
    if (message) sourcePayload.message = message;
  }
  const budget: ProjectionBudget = { remaining: MAX_ACTIVITY_EVENT_BYTES };
  const payload = (activityValue(sourcePayload, budget, 0) as Record<string, unknown> | undefined) ?? {};
  const event: RunEvent = { id, run_id: runId, seq: sequence, event_type: eventType, payload, occurred_at: occurredAt };
  const views = viewsForPayload(payload);
  // A final assistant snapshot must not regress a reply assembled from small
  // deltas to the generic 16 KiB metadata preview. Only its display text may
  // consume the remaining existing 64 KiB event budget. Native classification
  // goes through its provider view, and only sees the bounded projection, never
  // raw content/attachments. Tools, reasoning and arbitrary strings stay 16 KiB.
  const assistantText = eventType === "output.delta" || eventType === "output.snapshot" ||
    views.some((view) => view.projection([event])(event)?.event_type === "output.snapshot");
  const displayLimit = assistantText ? MAX_ACTIVITY_EVENT_BYTES : MAX_ACTIVITY_STRING;

  for (const view of views) view.restoreDisplay?.(sourcePayload, payload, {
    restoreText: (source, target) => restoreDisplayText(source, target, budget, displayLimit),
    bounded: (value) => boundedString(value, budget)
  });
  restoreDisplayText(sourcePayload, payload, budget, displayLimit);

  return event;
}

function eventKey(event: Pick<RunEvent, "run_id" | "seq">): string {
  return `${event.run_id}\u0000${event.seq}`;
}

function eventWeight(event: RunEvent): number {
  // Projection bounds make stringify deterministic and small. This weight is
  // used only for eviction and never persisted.
  try {
    return JSON.stringify(event).length;
  } catch {
    return MAX_ACTIVITY_EVENT_BYTES;
  }
}

/** O(1) Map + head-indexed queue retention for long-running sessions. */
export class BoundedRunEventBuffer {
  #events = new Map<string, { event: RunEvent; weight: number }>();
  #queue: string[] = [];
  #head = 0;
  #bytes = 0;
  #evictedCount = 0;
  #evictedBytes = 0;

  constructor(
    readonly maxEvents = MAX_ACTIVITY_EVENTS,
    readonly maxBytes = MAX_ACTIVITY_BUFFER_BYTES
  ) {}

  upsert(event: RunEvent): boolean {
    const key = eventKey(event);
    const weight = eventWeight(event);
    const previous = this.#events.get(key);
    if (previous) {
      this.#bytes -= previous.weight;
      this.#events.set(key, { event, weight });
      this.#bytes += weight;
    } else {
      this.#events.set(key, { event, weight });
      this.#queue.push(key);
      this.#bytes += weight;
    }
    this.#evict();
    return this.#events.has(key);
  }

  has(runId: string, sequence: number): boolean {
    return this.#events.has(`${runId}\u0000${sequence}`);
  }

  values(): RunEvent[] {
    const result: RunEvent[] = [];
    for (let index = this.#head; index < this.#queue.length; index += 1) {
      const item = this.#events.get(this.#queue[index]);
      if (item) result.push(item.event);
    }
    return result;
  }

  clear(): void {
    this.#events.clear();
    this.#queue = [];
    this.#head = 0;
    this.#bytes = 0;
    this.#evictedCount = 0;
    this.#evictedBytes = 0;
  }

  get size(): number {
    return this.#events.size;
  }

  get bytes(): number {
    return this.#bytes;
  }

  /** Cumulative evidence that this history is no longer complete. */
  get evictedCount(): number {
    return this.#evictedCount;
  }

  get evictedBytes(): number {
    return this.#evictedBytes;
  }

  get incomplete(): boolean {
    return this.#evictedCount > 0;
  }

  #evict(): void {
    const eventLimit = Math.max(1, this.maxEvents);
    const byteLimit = Math.max(1, this.maxBytes);
    while (this.#events.size > eventLimit || this.#bytes > byteLimit) {
      const key = this.#queue[this.#head++];
      if (!key) break;
      const item = this.#events.get(key);
      if (!item) continue;
      this.#events.delete(key);
      this.#bytes -= item.weight;
      this.#evictedCount += 1;
      this.#evictedBytes += item.weight;
    }
    if (this.#head > 2_048 && this.#head * 2 > this.#queue.length) {
      this.#queue = this.#queue.slice(this.#head);
      this.#head = 0;
    }
  }
}

export interface RunChangesInvalidationSignal {
  runId: string;
  sequence: number;
  eventType: string;
}

export function runChangesInvalidationSignal(event: RunEvent): RunChangesInvalidationSignal | undefined {
  if (event.event_type === "workspace.diff" || event.event_type === "files.changed" || viewsForPayload(event.payload).some((view) => view.isFileChange?.(event))) {
    return { runId: event.run_id, sequence: event.seq, eventType: event.event_type };
  }
  return undefined;
}

/**
 * Shared HTTP/SSE invalidation ingress. It deduplicates by the persisted
 * (run_id, seq) identity, and coalesces a whole HTTP page into one callback so
 * an initial page containing both workspace.diff and fileChange cannot cause a
 * refetch storm. The bounded identity set is independent of Activity eviction.
 */
export class RunChangesInvalidationHandler {
  #seen = new Set<string>();
  #queue: string[] = [];
  #head = 0;

  constructor(
    readonly onInvalidation: (signal: RunChangesInvalidationSignal) => void,
    readonly maxIdentities = 20_000
  ) {}

  ingest(event: RunEvent): RunChangesInvalidationSignal | undefined {
    const signal = this.#accept(event);
    if (signal) this.onInvalidation(signal);
    return signal;
  }

  ingestPage(events: Iterable<RunEvent>): RunChangesInvalidationSignal | undefined {
    let latest: RunChangesInvalidationSignal | undefined;
    for (const event of events) {
      const signal = this.#accept(event);
      if (!signal) continue;
      if (!latest || signal.sequence > latest.sequence) latest = signal;
    }
    if (latest) this.onInvalidation(latest);
    return latest;
  }

  clear(): void {
    this.#seen.clear();
    this.#queue = [];
    this.#head = 0;
  }

  get size(): number {
    return this.#seen.size;
  }

  #accept(event: RunEvent): RunChangesInvalidationSignal | undefined {
    const signal = runChangesInvalidationSignal(event);
    if (!signal) return undefined;
    const key = eventKey(event);
    if (this.#seen.has(key)) return undefined;
    this.#seen.add(key);
    this.#queue.push(key);
    const limit = Math.max(1, this.maxIdentities);
    while (this.#seen.size > limit) {
      const oldest = this.#queue[this.#head++];
      if (oldest) this.#seen.delete(oldest);
    }
    if (this.#head > 2_048 && this.#head * 2 > this.#queue.length) {
      this.#queue = this.#queue.slice(this.#head);
      this.#head = 0;
    }
    return signal;
  }
}
