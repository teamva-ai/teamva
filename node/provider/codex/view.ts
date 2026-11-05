import { contentText, duration, record, text, type ProviderView } from "../view";

// MCP text blocks are display text, not recursively traversable attachments.
// Bound extraction before joining, then consume the shared ingress budget.
function mcpResultText(value: unknown): string | undefined {
  if (!Array.isArray(value)) return undefined;
  const limit = 16 * 1024 + 1;
  let output: string | undefined;
  for (const valueItem of value.slice(0, 100)) {
    const block = record(valueItem);
    if (block.type !== "text" || typeof block.text !== "string") continue;
    output = (output ?? "") + block.text.slice(0, limit - (output?.length ?? 0));
    if (output.length >= limit) break;
  }
  return output;
}

// Only disclosed search input is displayable. Do not spread native actions or
// results (which may carry headers, page bodies or binary/transport metadata).
function webSearchInput(item: Record<string, unknown>): Record<string, unknown> {
  const input: Record<string, unknown> = {};
  const action = record(item.action);
  // Leave room for JSON escaping and a duplicate summary within Activity's
  // event budget. The shared ingress already bounds the native source too.
  const limit = 4 * 1024;
  let remaining = limit;
  const bounded = (value: string) => {
    const result = value.slice(0, remaining);
    remaining -= result.length;
    return result;
  };
  if (typeof item.query === "string") input.query = bounded(item.query);
  else if (item.query === null) input.query = null;
  if (item.action === null) input.action = null;
  else if (action.type === "search") {
    const search: Record<string, unknown> = { type: "search" };
    if (typeof action.query === "string") search.query = bounded(action.query);
    else if (action.query === null) search.query = null;
    if (Array.isArray(action.queries)) {
      search.queries = action.queries.slice(0, 100).filter((query): query is string => typeof query === "string")
        .map(bounded).filter(Boolean);
    }
    input.action = search;
  }
  const search = record(input.action);
  const queries = [text(input.query), text(search.query), ...(Array.isArray(search.queries) ? search.queries.map(text) : [])];
  const summary = [...new Set(queries.filter((query) => query.trim()))].join("\n").slice(0, limit);
  if (summary) input.summary = summary;
  return input;
}

export default {
  isFileChange(event) { return event.event_type === "provider.item" && record(event.payload.item).type === "fileChange"; },
  restoreDisplay(source, target, { restoreText, bounded }) {
    if (source.item && target.item) restoreText(record(source.item), record(target.item));
    const item = record(source.item), projected = record(target.item);
    if (item.type !== "mcpToolCall" || !target.item) return;
    if (item.result !== null && item.result !== undefined) {
      const result = { ...record(projected.result) };
      const display = mcpResultText(record(item.result).content);
      if (display !== undefined) result.text = bounded(display);
      projected.result = result;
    }
    const error = record(item.error), projectedError = record(projected.error);
    if (typeof error.message === "string" && typeof projectedError.message !== "string") {
      projected.error = { ...projectedError, message: bounded(error.message) };
    }
  },
  materialize(event, { rows, key, limited }) {
    const p = event.payload, item = record(p.item);
    if (text(item.id)) {
      const id = key(event, "item", text(item.id));
      const previous = rows.get(id), old = record(previous?.payload.item);
      const merged = { ...old, ...item };
      if (typeof item.text !== "string" || item.text === "" && text(old.text)) merged.text = old.text;
      if (merged.type === "mcpToolCall") merged.status = text(item.status) || text(old.status);
      else if (p.method === "item/completed" || typeof p.completedAtMs === "number") merged.status = text(item.status) || "completed";
      else if (p.method === "item/started" || typeof p.startedAtMs === "number") merged.status = text(item.status) || "running";
      if (merged.type === "reasoning") {
        const summary = Array.isArray(item.summary) ? item.summary.map(contentText).join("\n") : text(item.summary);
        if (summary) merged.summary = [summary];
        else if (Array.isArray(old.summary)) merged.summary = old.summary;
      }
      rows.set(id, { ...event, id: previous?.id ?? event.id, seq: previous?.seq ?? event.seq,
        occurred_at: previous?.occurred_at ?? event.occurred_at, payload: { ...previous?.payload, ...p, item: merged } });
      return true;
    }
    if (event.event_type === "provider.event" && p.method === "item/mcpToolCall/progress" && text(p.itemId) && typeof p.message === "string") {
      const id = key(event, "item", text(p.itemId)), previous = rows.get(id), old = record(previous?.payload.item);
      rows.set(id, { ...(previous ?? event), event_type: "provider.item", payload: { ...previous?.payload, provider_id: p.provider_id,
        item: { ...old, id: p.itemId, type: "mcpToolCall", status: text(old.status) || "inProgress", progress: limited(p.message) } } });
      return true;
    }
    if (event.event_type === "output.delta" && text(p.item_id)) {
      const id = key(event, "item", text(p.item_id)), previous = rows.get(id), old = record(previous?.payload.item);
      rows.set(id, { ...(previous ?? event), event_type: "provider.item", payload: { ...previous?.payload, provider_id: p.provider_id,
        item: { ...old, id: p.item_id, type: old.type || "agentMessage", text: limited(text(old.text) + text(p.text)) } } });
      return true;
    }
    if (event.event_type === "provider.event" && text(p.itemId) && typeof p.delta === "string") {
      const id = key(event, "item", text(p.itemId)), previous = rows.get(id), old = record(previous?.payload.item);
      if (old.type === "reasoning" || text(p.method).startsWith("item/reasoning/")) {
        const index = Number(p.summaryIndex ?? p.textIndex ?? 0);
        if (!Number.isSafeInteger(index) || index < 0 || index >= 100) return false;
        const sections = [...(Array.isArray(old.summary) ? old.summary : [])];
        sections[index] = limited(text(sections[index]) + p.delta);
        rows.set(id, { ...(previous ?? event), event_type: "provider.item", payload: { ...previous?.payload, provider_id: p.provider_id,
          item: { ...old, id: p.itemId, type: "reasoning", status: "running", summary: sections } } });
        return true;
      }
      if (old.type === "commandExecution" || p.method === "item/commandExecution/outputDelta") {
        rows.set(id, { ...(previous ?? event), event_type: "provider.item", payload: { ...previous?.payload, provider_id: p.provider_id,
          item: { ...old, id: p.itemId, type: "commandExecution", aggregatedOutput: limited(text(old.aggregatedOutput) + p.delta) } } });
        return true;
      }
    }
    return false;
  },
  projection() {
    return (event) => {
      if (event.event_type !== "provider.item") return undefined;
      const item = record(event.payload.item), id = text(item.id) || event.id;
      const common = { id, status: text(item.status), duration_ms: duration(item) };
      if (item.type === "agentMessage") {
        return { ...event, event_type: "output.snapshot", payload: { ...common, text: text(item.text) || contentText(item.content) } };
      }
      if (item.type === "reasoning") return { ...event, event_type: "reasoning.snapshot", payload: { ...common,
        text: text(item.text) || contentText(item.summary) || contentText(item.content) || contentText(item.reasoning) } };
      if (item.type === "webSearch") return { ...event, event_type: "provider.tool", payload: { ...common,
        tool: "websearch", input: webSearchInput(item) } };
      if (item.type === "commandExecution") return { ...event, event_type: "provider.tool", payload: { ...common,
        tool: "bash", input: { command: item.command, cwd: item.cwd }, output: item.aggregatedOutput ?? null, exit_code: item.exitCode,
        status: typeof item.exitCode === "number" && item.exitCode !== 0 ? "failed" : common.status } };
      if (item.type === "mcpToolCall") {
        const result = record(item.result), display = typeof result.text === "string" ? result.text : mcpResultText(result.content);
        const output = item.result !== null && item.result !== undefined
          ? { ...(display !== undefined ? { text: display } : {}), ...(result.structuredContent !== undefined && result.structuredContent !== null ? { structuredContent: result.structuredContent } : {}) }
          : common.status === "inProgress" && typeof item.progress === "string" ? { progress: item.progress } : undefined;
        return { ...event, event_type: "provider.tool", payload: { ...common, provider: "mcp", server: item.server,
          tool: [text(item.server), text(item.tool)].filter(Boolean).join("/"), input: item.arguments, output, error: item.error } };
      }
      if (item.type === "fileChange") return { ...event, event_type: "files.changed", payload: { ...common,
        paths: [...(Array.isArray(item.changes) ? item.changes.map((change) => text(record(change).path)) : []),
          ...(Array.isArray(item.paths) ? item.paths.map(text) : []), text(item.path)].filter(Boolean) } };
      return null;
    };
  }
} satisfies ProviderView;
