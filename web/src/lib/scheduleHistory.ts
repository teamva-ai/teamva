import type { Run, ScheduledTaskRun } from "./types";

// Server-generated history diagnostics are metadata, never Provider text.
export interface ScheduleHistoryRun extends ScheduledTaskRun {
  dispatch_attempts?: number;
  retry_at?: string;
  last_error?: string;
}

const safeErrorCodes = new Set([
  "schedule_dispatch_failed", "schedule_concurrency_forbidden", "invalid_argument", "not_found",
  "agent_not_ready", "agent_not_configured", "agent_inactive", "agent_service_unavailable",
  "computer_offline", "service_unavailable", "resource_exhausted", "busy", "conflict",
  "run_terminal", "run_interrupted", "dispatcher_restarted", "recovery_not_replayable",
  "schedule_no_next_run", "schedule_issue_unavailable", "schedule_issue_invalid", "issue_paused", "issue_closed"
]);

/** Never render raw Provider/SQL/filesystem diagnostics, even if they look like an error code. */
export function scheduleErrorCode(value?: string): string {
  if (!value) return "";
  return safeErrorCodes.has(value) ? value : "schedule_dispatch_failed";
}

export function scheduleRunTarget(run: Run, projectId: string, expectedRunId: string): string | null {
  if (run.id !== expectedRunId || run.project_id !== projectId || !run.issue_id) return null;
  const params = new URLSearchParams({ issue: run.issue_id });
  if (run.issue_agent_instance_id) params.set("session", run.issue_agent_instance_id);
  params.set("run", run.id);
  return `/issues?${params}`;
}
