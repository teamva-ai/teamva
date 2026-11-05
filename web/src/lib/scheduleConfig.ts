export interface ScheduleConfig {
  kind: "daily" | "weekly" | "once" | "cron";
  expression: string;
  times: string[];
  /** Sunday = 0, Monday = 1, … Saturday = 6. */
  weekdays: number[];
  timezone: string;
}

export function defaultScheduleConfig(): ScheduleConfig {
  return { kind: "daily", expression: "", times: ["09:00"], weekdays: [1], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" };
}

export function validTimezone(timezone: string): boolean {
  if (!timezone || /^[+-]/.test(timezone)) return false;
  try { new Intl.DateTimeFormat("en", { timeZone: timezone }); return true; } catch { return false; }
}

const timePattern = /^(?:[01]\d|2[0-3]):[0-5]\d$/;
const localPattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/;
const isoPattern = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?)(Z|[+-](\d{2}):(\d{2}))$/i;
const pad = (value: number, width = 2) => String(value).padStart(width, "0");

function parseLocal(value: string): number | undefined {
  const match = localPattern.exec(value);
  if (!match) return undefined;
  const [year, month, day, hour, minute] = match.slice(1, 6).map(Number);
  const second = Number(match[6] || 0);
  const ms = Number((match[7] || "").padEnd(3, "0"));
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59) return undefined;
  const date = new Date(0);
  date.setUTCFullYear(year, month - 1, day);
  date.setUTCHours(hour, minute, second, ms);
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return undefined;
  return date.getTime();
}

function validISO(value: string): boolean {
  const match = isoPattern.exec(value);
  if (!match || (match[3] && (+match[3] > 23 || +match[4] > 59))) return false;
  // Validate the actual calendar, not Date.parse's rollover of February 30.
  return parseLocal(match[1].replace(/(\.\d{3})\d+$/, "$1")) !== undefined && Number.isFinite(Date.parse(value));
}

function wallParts(instant: number, timezone: string): string {
  const parts = new Intl.DateTimeFormat("en-CA-u-ca-iso8601-nu-latn", {
    timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  }).formatToParts(instant);
  const part = (name: string) => parts.find((item) => item.type === name)?.value ?? "";
  return `${part("year").padStart(4, "0")}-${part("month")}-${part("day")}T${part("hour")}:${part("minute")}:${part("second")}`;
}

/** Absolute RFC3339 → the selected zone's native datetime-local wall clock.
 * Never depends on the browser timezone. Retains seconds and milliseconds. */
export function toZonedLocal(value: string, timezone: string): string {
  if (!validISO(value) || !validTimezone(timezone)) return "";
  const instant = Date.parse(value);
  const wall = wallParts(instant, timezone);
  const ms = new Date(instant).getUTCMilliseconds();
  return wall + (ms ? `.${pad(ms, 3)}` : "");
}

/** Wall clock → UTC RFC3339. DST gaps are invalid; overlaps select the earlier
 * instant deterministically. The server preview remains authoritative. */
export function fromZonedLocal(value: string, timezone: string): string {
  const wall = parseLocal(value);
  if (wall === undefined || !validTimezone(timezone)) return "";
  const offsets = new Set<number>();
  // Discover both offsets around transitions, including half-hour and date-line
  // changes; verify each candidate so a missing wall clock is never rolled on.
  for (let hour = -48; hour <= 48; hour += 6) {
    const sample = wall + hour * 3_600_000;
    const local = parseLocal(wallParts(sample, timezone));
    if (local !== undefined) offsets.add(local - sample + (sample % 1000 + 1000) % 1000);
  }
  const candidates = [...offsets].map((offset) => wall - offset).filter((instant) => {
    const actual = parseLocal(wallParts(instant, timezone));
    return actual !== undefined && actual + (wall % 1000 + 1000) % 1000 === wall;
  });
  return candidates.length ? new Date(Math.min(...candidates)).toISOString() : "";
}

/** Sort/deduplicate exact HH:mm pairs; never split hours and minutes into cron
 * lists (which would create a Cartesian product). Does not repair invalid data. */
export function normalizeSchedule(config: ScheduleConfig): ScheduleConfig {
  return { ...config, timezone: config.timezone.trim(), expression: config.expression.trim(), times: [...new Set(config.times)].sort(), weekdays: [...new Set(config.weekdays)].sort((a, b) => a - b) };
}

export function scheduleValid(config: ScheduleConfig): boolean {
  if (!validTimezone(config.timezone)) return false;
  if (config.kind === "once") return validISO(config.expression);
  if (config.kind === "cron") return config.expression.trim().length > 0;
  if (config.kind !== "daily" && config.kind !== "weekly") return false;
  if (!config.times.length || config.times.length > 64 || config.times.some((time) => !timePattern.test(time))) return false;
  return config.kind !== "weekly" || (config.weekdays.length > 0 && config.weekdays.every((day) => Number.isInteger(day) && day >= 0 && day <= 6));
}

/** Compact, locale-aware data summary without stores, DOM, or network access. */
export function scheduleSummary(config: ScheduleConfig, locale = "en-US"): string {
  const normalized = normalizeSchedule(config);
  if (config.kind === "once") {
    if (!validISO(config.expression) || !validTimezone(config.timezone)) return config.expression;
    return `${new Intl.DateTimeFormat(locale, { timeZone: config.timezone, dateStyle: "medium", timeStyle: "medium" }).format(new Date(config.expression))} · ${config.timezone}`;
  }
  if (config.kind === "cron") return `${normalized.expression} · ${config.timezone}`;
  const times = normalized.times.join(", ");
  if (config.kind === "daily") return `${times} · ${config.timezone}`;
  const days = normalized.weekdays.map((day) => new Intl.DateTimeFormat(locale, { weekday: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2024, 0, 7 + day))));
  return `${days.join(", ")} · ${times} · ${config.timezone}`;
}

/** Occurrences are absolute instants, but their labels use the task's zone,
 * just like the rule and preview (not the viewing browser's local zone). */
export function scheduleInstantLabel(value: string | undefined, timezone: string, locale = "en-US"): string {
  if (!value || !Number.isFinite(Date.parse(value)) || !validTimezone(timezone)) return "-";
  return new Intl.DateTimeFormat(locale, { timeZone: timezone, month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}
