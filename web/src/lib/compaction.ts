import { t } from "./i18n.svelte";

export function settledActivityStatus(status: string, active: boolean): string {
  return !active && ["running", "pending", "started", "inProgress", "in_progress"].includes(status) ? "interrupted" : status;
}

export function compactionPresentation(nativeStatus: string, active: boolean) {
  // Do not fabricate a completed event when a Run ends during compaction.
  const normalized = ["", "started", "pending", "inProgress", "in_progress"].includes(nativeStatus) ? "running" : nativeStatus === "ended" ? "completed" : nativeStatus;
  const status = settledActivityStatus(normalized, active);
  const key = status === "running" ? "sessionCompacting" : status === "failed" ? "sessionCompactionFailed"
    : status === "interrupted" || status === "cancelled" ? "sessionCompactionInterrupted" : "sessionCompacted";
  return { status, label: t(`issues.${key}`), text: t(`issues.${key}Desc`) };
}
