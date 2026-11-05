/** Provider API v1: optional browser projections for provider-native events.
 * New integrations can omit this module and emit standard semantic events.
 * No application components, stores or DOM access belong in a projection.
 */
export interface ViewEvent {
  id: string; run_id: string; seq: number; event_type: string;
  payload: Record<string, unknown>; occurred_at: string;
}
export interface MaterializationContext {
  rows: Map<string, ViewEvent>;
  key(event: ViewEvent, kind: string, id: string): string;
  limited(value: string): string;
}
export interface ProviderView {
  isFileChange?(event: ViewEvent): boolean;
  restoreDisplay?(source: Record<string, unknown>, target: Record<string, unknown>, context: {
    restoreText(source: Record<string, unknown>, target: Record<string, unknown>): void;
    bounded(value: string): string;
  }): void;
  materialize(event: ViewEvent, context: MaterializationContext): boolean;
  projection(events: readonly ViewEvent[]): (event: ViewEvent) => ViewEvent | null | undefined;
}
export const record = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
export const text = (value: unknown): string => typeof value === "string" ? value : "";
export function contentText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(contentText).join("");
  const item = record(value);
  return text(item.text) || text(item.output_text) || (item.content ? contentText(item.content) : "");
}
export function duration(value: Record<string, unknown>): number | undefined {
  if (typeof value.durationMs === "number") return value.durationMs;
  if (typeof value.duration_ms === "number") return value.duration_ms;
  const time = record(value.time);
  return typeof time.start === "number" && typeof time.end === "number" ? Math.max(0, time.end - time.start) : undefined;
}
