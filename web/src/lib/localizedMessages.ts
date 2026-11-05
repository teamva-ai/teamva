import { t } from "./i18n.svelte";

type GitWarningLike = string | { code?: string; message?: string };

interface GitDiffMessageLike {
  kind?: string | null;
  message?: string | null;
  message_code?: string | null;
  confirm_required?: boolean;
  stats?: { additions?: number; deletions?: number; lines?: number };
  truncation?: { truncated?: boolean; reason?: string | null } | null;
}

export const GIT_WARNING_CODES = [
  "changes_truncated",
  "discovery_limit_exceeded",
  "patch_truncated",
  "path_limit_exceeded",
  "snapshot_capture_failed",
  "snapshot_incomplete",
  "snapshot_payload_truncated",
  "stats_unavailable",
  "status_output_truncated",
  "status_truncated",
  "status_unavailable",
  "watch_limit_exceeded"
] as const;

const gitWarningKeys: Record<string, string> = Object.fromEntries(
  GIT_WARNING_CODES.map((code) => [code, `gitMessages.warnings.${code}`])
);

const gitDiffMessageKeys: Record<string, string> = {
  binary: "gitMessages.diff.binary",
  binary_diff_unavailable: "gitMessages.diff.binary",
  binary_summary: "gitMessages.diff.binary",
  conflict: "gitMessages.diff.conflict",
  conflict_summary: "gitMessages.diff.conflict",
  conflict_diff_unavailable: "gitMessages.diff.conflict",
  diff_confirmation_required: "gitMessages.diff.confirmSize",
  diff_line_confirmation_required: "gitMessages.diff.confirmLines",
  diff_line_limit: "gitMessages.diff.confirmLines",
  diff_lines_confirmation_required: "gitMessages.diff.confirmLines",
  diff_too_many_lines_confirmation: "gitMessages.diff.confirmLines",
  diff_patch_limit: "gitMessages.diff.patchLimit",
  diff_limit_exceeded: "gitMessages.diff.patchLimit",
  diff_truncated: "gitMessages.diff.patchLimit",
  diff_size_confirmation: "gitMessages.diff.confirmSize",
  diff_timeout: "gitMessages.diff.timeout",
  image_confirmation_required: "gitMessages.diff.confirmImageSize",
  image_size_confirmation: "gitMessages.diff.confirmImageSize",
  image_too_large_confirmation: "gitMessages.diff.confirmImageSize",
  large_diff_confirmation: "gitMessages.diff.confirmSize",
  large_diff_lines_confirmation: "gitMessages.diff.confirmLines",
  large_image_confirmation: "gitMessages.diff.confirmImageSize",
  patch_limit: "gitMessages.diff.patchLimit",
  patch_truncated: "gitMessages.diff.patchLimit",
  read_timeout: "gitMessages.diff.timeout",
  response_budget: "gitMessages.diff.responseBudget",
  response_budget_exceeded: "gitMessages.diff.responseBudget",
  diff_response_budget: "gitMessages.diff.responseBudget",
  diff_response_budget_exceeded: "gitMessages.diff.responseBudget",
  response_too_large: "gitMessages.diff.responseBudget",
  submodule: "gitMessages.diff.submodule",
  submodule_summary: "gitMessages.diff.submodule",
  submodule_diff_unavailable: "gitMessages.diff.submodule",
  timeout: "gitMessages.diff.timeout",
  untracked_directory: "gitMessages.diff.untrackedDirectory",
  untracked_directory_expand: "gitMessages.diff.untrackedDirectory",
  untracked_directory_expand_required: "gitMessages.diff.untrackedDirectory",
  untracked_directory_requires_expansion: "gitMessages.diff.untrackedDirectory",
  untracked_symlink: "gitMessages.diff.untrackedSymlink",
  symlink_summary: "gitMessages.diff.untrackedSymlink",
  untracked_symlink_summary: "gitMessages.diff.untrackedSymlink"
};

const gitDiffTruncationKeys: Record<string, string> = {
  patch_limit: "gitMessages.diff.patchLimit",
  response_budget: "gitMessages.diff.responseBudget",
  timeout: "gitMessages.diff.timeout"
};

const apiErrorKeys: Record<string, string> = {
  agent_in_use: "errors.agentInUse",
  attachment_invalid: "errors.codes.attachment_invalid",
  attachment_too_large: "errors.codes.attachment_too_large",
  attachments_too_large: "errors.codes.attachments_too_large",
  attachments_too_many: "errors.codes.attachments_too_many",
  capability_unsupported: "errors.codes.capability_unsupported",
  change_not_found: "errors.codes.change_not_found",
  change_unavailable: "errors.codes.change_unavailable",
  git_discovery_incomplete: "errors.codes.git_discovery_incomplete",
  git_metadata_too_large: "errors.codes.git_metadata_too_large",
  git_resource_invalid: "errors.codes.git_resource_invalid",
  git_resources_too_large: "errors.codes.git_resources_too_large",
  git_timeout: "errors.codes.git_timeout",
  git_unavailable: "errors.codes.git_unavailable",
  git_unsupported: "errors.codes.git_unsupported",
  internal: "errors.codes.internal",
  invalid_argument: "errors.codes.invalid_argument",
  invalid_cursor: "errors.codes.invalid_cursor",
  invalid_response: "errors.invalidResponse",
  invalid_session: "errors.codes.invalid_session",
  node_offline: "errors.codes.node_offline",
  not_found: "errors.codes.not_found",
  path_out_of_scope: "errors.codes.path_out_of_scope",
  permission_denied: "errors.codes.permission_denied",
  rate_limited: "errors.codes.rate_limited",
  repository_not_found: "errors.codes.repository_not_found",
  request_too_large: "errors.codes.request_too_large",
  response_too_large: "errors.codes.response_too_large",
  runtime_closed: "errors.codes.runtime_closed",
  schedule_no_next_run: "errors.codes.schedule_no_next_run",
  skill_in_use: "errors.codes.skill_in_use",
  skill_uninstall_failed: "errors.codes.skill_uninstall_failed",
  skill_uninstall_incomplete: "errors.codes.skill_uninstall_incomplete",
  snapshot_expired: "errors.codes.snapshot_expired",
  stats_too_large: "errors.codes.stats_too_large",
  stats_unavailable: "errors.codes.stats_unavailable",
  unauthenticated: "errors.codes.unauthenticated",
  update_conflict: "errors.codes.update_conflict",
  version_conflict: "errors.codes.version_conflict"
};

function normalizedCode(value?: string | null): string {
  return String(value ?? "").trim().toLocaleLowerCase().replaceAll("-", "_");
}

function backendEnglishFallback(value?: string | null): string | undefined {
  const message = String(value ?? "").trim();
  if (!message) return undefined;
  // Stable codes own localization. A wire message is only an escape hatch for
  // a future unknown code, and that public fallback is required to be English.
  // In particular, never leak today's Chinese runtime prose into another UI locale.
  if (/\p{Script=Han}/u.test(message)) return undefined;
  return message;
}

export function localizedGitWarning(warning: GitWarningLike): string {
  if (typeof warning === "string") {
    return backendEnglishFallback(warning) ?? t("gitMessages.unknownWarning");
  }
  const code = normalizedCode(warning.code);
  const key = gitWarningKeys[code];
  if (key) return t(key);
  return backendEnglishFallback(warning.message) || code || t("gitMessages.unknownWarning");
}

export function localizedGitDiffMessage(diff: GitDiffMessageLike): string {
  const messageCode = normalizedCode(diff.message_code);
  if (messageCode) {
    const key = gitDiffMessageKeys[messageCode];
    if (key) return t(key);
    return backendEnglishFallback(diff.message) ?? messageCode;
  }

  const reason = normalizedCode(diff.truncation?.reason);
  if (reason) {
    const key = gitDiffTruncationKeys[reason];
    if (key) return t(key);
    return backendEnglishFallback(diff.message) ?? reason;
  }

  // The current wire contract does not yet require message_code on every
  // confirmation. Infer only from stable structured fields, never prose.
  if (diff.confirm_required) {
    const kind = normalizedCode(diff.kind);
    if (kind === "image" || kind === "image_too_large") return t("gitMessages.diff.confirmImageSize");
    const changedLines = Number(diff.stats?.lines ?? 0) ||
      Number(diff.stats?.additions ?? 0) + Number(diff.stats?.deletions ?? 0);
    if (changedLines > 500) return t("gitMessages.diff.confirmLines");
    return t("gitMessages.diff.confirmSize");
  }

  const kind = normalizedCode(diff.kind);
  if (kind === "directory") return t("gitMessages.diff.untrackedDirectory");
  if (kind === "symlink") return t("gitMessages.diff.untrackedSymlink");

  return backendEnglishFallback(diff.message) ?? "";
}

export function localizedAPIErrorMessage(code?: string, status?: number): string | undefined {
  const normalized = normalizedCode(code);
  const key = apiErrorKeys[normalized];
  if (key) return t(key, { status: status ?? 0 });
  return undefined;
}

export function unknownAPIErrorMessage(message: string | undefined, status: number): string {
  return backendEnglishFallback(message) ?? t("errors.requestFailed", { status });
}

/** Test support for proving every shipped locale resolves every stable key. */
export function localizedMessageContractKeys(): string[] {
  return [
    ...Object.values(gitWarningKeys),
    ...new Set(Object.values(gitDiffMessageKeys)),
    ...new Set(Object.values(gitDiffTruncationKeys)),
    ...Object.values(apiErrorKeys)
  ];
}
