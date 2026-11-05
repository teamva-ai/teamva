import { t } from "./i18n.svelte";
import { providerDefinition } from "./providers";

export function statusLabel(status: string): string {
  // t() falls back to the raw key for unknown statuses.
  return t(`status.${status}`);
}

export function providerName(provider?: string): string {
  return provider ? providerDefinition(provider)?.display_name ?? provider : t("provider.none");
}

export function resourceKindLabel(kind: string): string {
  const value = t(`resourceKind.${kind}`);
  return value === `resourceKind.${kind}` ? kind : value;
}

export function auditActionLabel(action: string): string {
  const value = t(`audit.${action}`);
  return value === `audit.${action}` ? action : value;
}

const terminal = new Set(["succeeded", "failed", "cancelled", "lost"]);

export function isTerminal(status?: string): boolean {
  return status ? terminal.has(status) : false;
}

export function formatTime(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  // 用各语言下都自然的「月日 时:分」，避免 09/09 16:04:12 这种生硬格式。
  return new Intl.DateTimeFormat(undefined, {
    month: "long",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit"
  }).format(date);
}

export function textContent(blocks?: Array<{ text?: string }>): string {
  return blocks?.map((block) => block.text ?? "").join("") ?? "";
}

export function visibleTextContent(blocks?: Array<{ text?: string; internal?: boolean }>): string {
  return textContent(blocks?.filter((block) => !block.internal));
}

export function statusTone(status: string) {
  if (["ready", "online", "succeeded", "completed", "approved"].includes(status)) return "success";
  if (["failed", "revoked", "invalid", "lost", "denied"].includes(status)) return "danger";
  if (["waiting_approval", "pending", "pending_validation", "validation_pending", "queued"].includes(status)) return "warning";
  if (["running", "starting", "probing", "cancelling"].includes(status)) return "running";
  return "neutral";
}

// triggerKeyLabel localizes scheduled-task trigger keys:
// "manual:xxx" / "schedule:<task>:<key>" become localized labels.
export function triggerKeyLabel(key: string): string {
  if (key.startsWith("manual:")) return t("schedules.manualTrigger");
  if (key.startsWith("schedule:")) return t("schedules.scheduleTrigger");
  return key;
}
