import type { RunEvent } from "./types";

const redacted = "[REDACTED]";
const secretKey = (key: string) => /(?:authorization|cookie|apikey|accesskey|password|passwd|secret|accesstoken|refreshtoken|idtoken|connectionstring|connectionurl|databaseurl|datasourceurl|credentials)$/.test(key.toLowerCase().replace(/[-_ .]/g, "")) || /^(token|pwd|dsn)$/i.test(key);

/** Defense-in-depth for old/provider error envelopes, before the Activity cache.
 * The authoritative credential boundary is the runtime's pre-SQLite projection.
 */
export function sanitizeDiagnosticText(text: string): string {
  return text
    .replace(/[a-z][a-z0-9+.-]*:\/\/[^\s<>"'`]+/gi, (raw) => {
      try {
        const address = raw.replace(/[.,;:)\]}]+$/, ""), suffix = raw.slice(address.length);
        const url = new URL(address);
        if (url.username || url.password) { url.username = redacted; url.password = ""; }
        for (const key of [...url.searchParams.keys()]) if (secretKey(key) || ["key", "auth", "signature", "sig"].includes(key)) url.searchParams.set(key, redacted);
        return url.toString() + suffix;
      } catch { return "[connection URL omitted]"; }
    })
    .replace(/\b((?:proxy[-_ ]?)?authorization["']?\s*[=:]\s*["']?)(?:(?:Bearer|Basic)\s+)?[^\s,;"'\\]+/gi, `$1${redacted}`)
    .replace(/\b((?:set[-_ ]?)?cookie["']?\s*[=:]\s*["']?)[^\r\n"']+/gim, `$1${redacted}`)
    .replace(/(["']?(?:[a-z0-9]+[-_])*(?:api[-_ ]?key(?:\s+(?:provided|used|value))?|access[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|id[-_ ]?token|client[-_ ]?secret|secret|password|passwd|pwd|token|connection[-_ ]?string|database[-_ ]?url|dsn)["']?\s*[=:]\s*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}&"'\\]+)/gi, `$1${redacted}`)
    .replace(/\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{12,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b/g, redacted);
}

export function sanitizeDiagnosticPayload(payload: Record<string, unknown>): Record<string, unknown> {
  const secrets: string[] = [];
  const collect = (value: unknown, depth = 0) => {
    if (!value || typeof value !== "object" || depth > 16) return;
    const source = value as Record<string, unknown>;
    for (const field of ["name", "key"]) if (typeof source[field] === "string" && secretKey(source[field] as string) && typeof source.value === "string" && source.value.length >= 4) secrets.push(source.value);
    for (const [key, item] of Object.entries(value)) {
      if (secretKey(key) && typeof item === "string" && item.length >= 4) secrets.push(item);
      collect(item, depth + 1);
    }
  };
  collect(payload);
  const clean = (value: unknown, depth = 0): unknown => {
    if (depth > 16) return "[diagnostic nesting omitted]";
    if (typeof value === "string") {
      for (const secret of secrets) value = (value as string).split(secret).join(redacted);
      return sanitizeDiagnosticText(value as string);
    }
    if (Array.isArray(value)) return value.slice(0, 100).map((item) => clean(item, depth + 1));
    if (!value || typeof value !== "object") return value;
    const result: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value).slice(0, 64)) result[key] = secretKey(key) ? redacted : clean(item, depth + 1);
    for (const field of ["name", "key"]) if (typeof result[field] === "string" && secretKey(result[field] as string) && "value" in result) result.value = redacted;
    return result;
  };
  return clean(payload) as Record<string, unknown>;
}

export function diagnosticText(payload: Record<string, unknown>, fallback = ""): string {
  const parts: string[] = [];
  let status: number | undefined;
  const add = (value: unknown) => {
    if (typeof value !== "string") return;
    for (let line of value.split("\n").map((line) => line.trim()).filter(Boolean)) {
      line = line.replace(/\s+\(HTTP ([1-5]\d\d)\)$/, (_, code) => { status = Number(code); return ""; });
      if (!parts.includes(line)) parts.push(line);
    }
  };
  const visit = (value: unknown, depth = 0) => {
    if (depth > 8) return;
    if (typeof value === "string") { add(value); return; }
    if (Array.isArray(value)) { for (const item of value.slice(0, 100)) visit(item, depth + 1); return; }
    if (!value || typeof value !== "object") return;
    const item = value as Record<string, unknown>;
    for (const key of ["message", "detail", "details", "additionalDetails", "responseBody"]) add(item[key]);
    for (const key of ["error", "data", "cause", "turn", "defect"]) visit(item[key], depth + 1);
    if (item.response && typeof item.response === "object") add((item.response as Record<string, unknown>).body);
    const code = item.statusCode ?? item.status;
    if (typeof code === "number" && Number.isInteger(code) && code >= 100 && code <= 599) status = code;
  };
  const safe = sanitizeDiagnosticPayload(payload);
  visit(safe);
  if (!parts.length) add(safe.reason ?? safe.code ?? fallback);
  return (parts.join("\n") + (status === undefined ? "" : ` (HTTP ${status})`)).slice(0, 16 * 1024);
}

export function isDiagnosticEvent(type: string, payload: Record<string, unknown>): boolean {
  return ["run.error", "run.finished", "run.completed", "context.compaction"].includes(type) || payload.willRetry === true || payload.kind === "error";
}

export function isRunTerminalEvent(event: RunEvent): boolean {
  return ["run.completed", "run.finished", "run.error"].includes(event.event_type) && event.payload.willRetry !== true;
}

export function isFailureEvent(event: RunEvent): boolean {
  if (event.payload.willRetry === true) return false;
  const turn = event.payload.turn as Record<string, unknown> | undefined;
  return event.event_type === "run.error" || event.event_type.includes("failed") ||
    (["run.finished", "run.completed"].includes(event.event_type) && ["failed", "error"].includes(String(event.payload.status ?? turn?.status)));
}
