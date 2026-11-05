import { record, text, type ProviderView } from "../view";

const toolExitCode = (metadata: Record<string, unknown>): number | undefined =>
  typeof metadata.exit === "number" && Number.isSafeInteger(metadata.exit) ? metadata.exit : undefined;

const toolStatus = (phase: string, metadata: Record<string, unknown>): string => {
  // V2 tool success means the tool returned, not that its shell exited zero.
  if (phase === "failed" || metadata.status === "failed") return "failed";
  if (metadata.status === "interrupted") return "interrupted";
  const exit = toolExitCode(metadata);
  if (exit !== undefined && exit !== 0) return "failed";
  if (phase === "success" || metadata.status === "completed") {
    // A disclosed but unknown exit must not become a successful command.
    if ("exit" in metadata && exit === undefined) return "unknown";
    return "completed";
  }
  return "running";
};

export default {
  restoreDisplay(source, target, { bounded }) {
    if (source.kind !== "tool" || !Array.isArray(source.content)) return;
    // Only native text blocks are displayable. Never recurse into attachments,
    // nested content, metadata or arbitrary fields on a content block. Bound
    // extraction before joining, then apply the shared ingress scalar budget.
    const limit = 16 * 1024 + 1;
    let output: string | undefined;
    for (const value of source.content.slice(0, 100)) {
      const item = record(value);
      if (item.type !== "text" || typeof item.text !== "string") continue;
      output = (output ?? "") + item.text.slice(0, limit - (output?.length ?? 0));
      if (output.length >= limit) break;
    }
    if (output !== undefined) target.output = bounded(output);
  },
  materialize(event, { rows, key, limited }) {
    const p = event.payload;
    // Raw text deltas belong to the same part as the finalized text event, so
    // both must materialize into one row (otherwise the reply shows twice).
    const isDelta = event.event_type === "output.delta";
    const kind = isDelta ? "text" : text(p.kind);
    if (kind !== "text" && kind !== "reasoning" && kind !== "tool") return false;
    const partID = kind === "tool" ? text(p.call_id) : text(p.part_id);
    if (!partID) return false;
    const id = key(event, kind, partID);
    const previous = rows.get(id);
    const old = record(previous?.payload);
    let merged: Record<string, unknown>;
    if (kind === "tool") {
      merged = { ...old, ...p };
      for (const field of ["input", "text", "output", "metadata", "error"]) {
        if (p[field] === undefined && old[field] !== undefined) merged[field] = old[field];
      }
      if (p.metadata !== undefined) {
        // Progress metadata.output is a cumulative snapshot, not a text delta.
        // A terminal metadata update may omit it while disclosing the exit.
        const metadata = { ...record(p.metadata) };
        if (metadata.output === undefined && record(old.metadata).output !== undefined) {
          metadata.output = record(old.metadata).output;
        }
        merged.metadata = metadata;
      }
    } else {
      // `output.delta` carries one incremental chunk in `text`; a finalized
      // provider text/reasoning event carries the whole text and replaces.
      const deltaText = text(p.delta) || (isDelta ? text(p.text) : "");
      const whole = isDelta ? "" : text(p.text);
      merged = {
        ...old,
        ...p,
        kind,
        part_id: partID,
        text: whole !== "" ? whole : limited(text(old.text) + deltaText)
      };
    }
    rows.set(id, {
      ...event,
      event_type: isDelta ? "provider.event" : event.event_type,
      id: previous?.id ?? event.id,
      seq: previous?.seq ?? event.seq,
      occurred_at: previous?.occurred_at ?? event.occurred_at,
      payload: { ...previous?.payload, ...merged }
    });
    return true;
  },
  projection() {
    return (event) => {
      const p = event.payload;
      const kind = text(p.kind);
      const partID = kind === "tool" ? text(p.call_id) : text(p.part_id);
      if (!partID) return undefined;
      if (kind === "text") {
        return {
          ...event,
          event_type: "output.snapshot",
          payload: { id: partID, status: "completed", text: text(p.text) }
        };
      }
      if (kind === "reasoning") {
        return {
          ...event,
          event_type: "reasoning.snapshot",
          payload: { id: partID, status: "completed", text: text(p.text) }
        };
      }
      if (kind === "tool") {
        const metadata = record(p.metadata);
        return {
          ...event,
          event_type: "provider.tool",
          payload: {
            id: `${text(p.name) || "tool"}:${partID}`,
            tool: text(p.name) || "tool",
            input: p.input ?? p.text,
            output: p.output ?? metadata.output,
            error: p.error,
            status: toolStatus(text(p.phase), metadata),
            exit_code: metadata.exit === null ? null : toolExitCode(metadata),
            metadata: p.metadata
          }
        };
      }
      return null;
    };
  }
} satisfies ProviderView;
