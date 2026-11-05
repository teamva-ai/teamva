/** Native datetime-local represents the browser's local wall clock. API values
 * remain absolute RFC3339 instants; do not drop their offset by slicing strings. */
export function toLocalDateTime(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  const base = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
  const seconds = date.getSeconds() || date.getMilliseconds() ? `:${pad(date.getSeconds())}` : "";
  const milliseconds = date.getMilliseconds() ? `.${String(date.getMilliseconds()).padStart(3, "0")}` : "";
  return base + seconds + milliseconds;
}
export function fromLocalDateTime(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toISOString() : "";
}
