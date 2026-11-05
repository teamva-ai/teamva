import { parsePatchFiles, type FileDiffMetadata } from "@pierre/diffs";
import { api } from "./api";
import { safeGitPathDisplay, safeRepositoryRootDisplay } from "./gitChanges";
import type { Run } from "./types";

export type RunGitSnapshotPhase = "start" | "end";
export type RunGitSource = "provider" | "observation";
export type RunChangesScopeKind = "instance" | "issue";

export const MAX_RUN_GIT_RETAINED_BYTES = 4 * 1024 * 1024;
const MAX_RUN_GIT_HISTORY_PAGES = 100;
export const MAX_RUN_GIT_RETAINED_PATHS = 2_000;
export const MAX_RUN_GIT_SCANNED_LINES = 200_000;
export const MAX_RUN_GIT_DERIVED_BYTES = 2 * 1024 * 1024;
export const MAX_RUN_GIT_DERIVED_LINES = 50_000;

export interface GitRunPatchArea {
  area: string;
  patch?: string;
  paths: string[];
  binary_paths: string[];
  truncated: boolean;
}

export interface GitRunUntrackedEntry {
  path: string;
  kind: string;
  size_bytes?: number;
}

export interface GitRunWorkspaceSnapshot {
  repository_id: string;
  label: string;
  root_display: string;
  phase: RunGitSnapshotPhase;
  captured_at: string;
  areas: GitRunPatchArea[];
  untracked: GitRunUntrackedEntry[];
  warnings: Array<{ code: string; message: string }>;
  truncated: boolean;
  content_hash: string;
}

export interface GitProviderChangeReport {
  id: string;
  /** Optional stable identity used when lifecycle updates span HTTP pages. */
  report_id?: string;
  seq: number;
  occurred_at: string;
  source: "workspace.diff" | "provider.item.fileChange" | string;
  provider_turn_id?: string;
  provider_item_id?: string;
  source_item_id?: string;
  item_id?: string;
  source_item?: string | { id?: string; item_id?: string };
  patch?: string;
  paths: string[];
  paths_verified: boolean;
  scope: "repository" | "unverified" | string;
  status?: string;
  truncated: boolean;
}

export interface GitRunChangesResult {
  run_id: string;
  snapshots: GitRunWorkspaceSnapshot[];
  provider_reports: GitProviderChangeReport[];
  provider_reports_truncated: boolean;
  next_provider_cursor?: string;
}

export interface RunGitRepositoryDisplay {
  id: string;
  label: string;
  rootDisplay: string;
}

export interface RunGitFileTarget {
  id: string;
  repository: RunGitRepositoryDisplay;
  runId: string;
  round: number;
  runCreatedAt: string;
  occurredAt: string;
  source: RunGitSource;
  sourceId: string;
  providerSource?: string;
  providerTurnId?: string;
  phase?: RunGitSnapshotPhase;
  area: string;
  path: string;
  oldPath?: string;
  status?: string;
  kind: string;
  binary: boolean;
  conflict: boolean;
  truncated: boolean;
  /** Shared patch string plus one file section's offsets. No Pierre objects are
   * retained in the tree model. */
  patchSection?: RunGitPatchSection;
  metadata?: FileDiffMetadata;
  placeholder: "none" | "no_patch" | "binary" | "conflict" | "untracked" | "truncated" | "budget";
  sizeBytes?: number;
}

export interface RunGitDerivedBudget {
  readonly maxBytes: number;
  readonly maxLines: number;
  usedBytes: number;
  usedLines: number;
}

export interface RunGitPatchSection {
  patch: string;
  start: number;
  end: number;
  lineCount: number;
  cacheKey: string;
  budget: RunGitDerivedBudget;
}

export interface RunGitFileTargets extends Array<RunGitFileTarget> {
  buildLimited: boolean;
  retainedPathCount: number;
  scannedLineCount: number;
  derivedBudget: RunGitDerivedBudget;
}

export interface RunGitMetadataLease {
  metadata?: FileDiffMetadata;
  limited: boolean;
  release: () => void;
}

export interface RunChangesBundle {
  results: GitRunChangesResult[];
  resolvedRunIds: string[];
  historyLimited?: boolean;
}

export interface RunGitUnverifiedReport {
  id: string;
  runId: string;
  round: number;
  occurredAt: string;
  source: string;
  status?: string;
  displayNames: string[];
  truncated: boolean;
}

export type RunGitTreeRowType = "repository" | "source" | "area" | "directory" | "file";

export interface RunGitTreeRow {
  key: string;
  type: RunGitTreeRowType;
  level: number;
  parentKey?: string;
  label: string;
  count?: number;
  expanded?: boolean;
  expandable?: boolean;
  repository?: RunGitRepositoryDisplay;
  target?: RunGitFileTarget;
  source?: RunGitSource;
  providerTurnId?: string;
  phase?: RunGitSnapshotPhase;
  runId?: string;
  round?: number;
  occurredAt?: string;
  area?: string;
}

export function runGitChangesQueryKey(runId: string) {
  return ["run-git-changes", runId] as const;
}

export function runChangesBundleQueryKey(kind: RunChangesScopeKind, scopeId: string) {
  return [kind === "issue" ? "issue-git-changes" : "instance-git-changes", scopeId] as const;
}

export function fetchRunGitChangesPage(
  runId: string,
  afterProviderSequence = "",
  signal?: AbortSignal
): Promise<GitRunChangesResult> {
  const query = new URLSearchParams({
    after_provider_seq: afterProviderSequence,
    limit: "100"
  });
  return api<GitRunChangesResult>(
    `/runs/${encodeURIComponent(runId)}/git/changes?${query}`,
    { signal }
  );
}

function providerReportSourceItemID(report: GitProviderChangeReport): string {
  if (report.provider_item_id) return report.provider_item_id;
  if (report.source_item_id) return report.source_item_id;
  if (report.item_id) return report.item_id;
  if (typeof report.source_item === "string") return report.source_item;
  return report.source_item?.id || report.source_item?.item_id || "";
}

function providerReportTurnSourceKey(report: GitProviderChangeReport): string {
  if (!report.provider_turn_id) return "";
  return `${report.provider_turn_id}\u0000${report.source}`;
}

function providerReportAliases(report: GitProviderChangeReport): string[] {
  const aliases: string[] = [];
  if (report.report_id) aliases.push(`report\u0000${report.report_id}`);
  const itemID = providerReportSourceItemID(report);
  if (itemID) {
    aliases.push(`item\u0000${report.provider_turn_id ?? ""}\u0000${report.source}\u0000${itemID}`);
  }
  if (report.id) aliases.push(`id\u0000${report.id}`);
  return aliases;
}

function providerReportIdentity(report: GitProviderChangeReport): string {
  return providerReportAliases(report)[0] || `${report.seq}\u0000${report.source}`;
}

function providerReportHasContent(report: GitProviderChangeReport): boolean {
  return Boolean(report.patch || report.paths.length || report.truncated);
}

/** Keep lifecycle metadata from the highest sequence while carrying forward
 * bounded content omitted by a status-only completion update. */
function mergeProviderReports(
  current: GitProviderChangeReport,
  incoming: GitProviderChangeReport
): GitProviderChangeReport {
  const latest = incoming.seq >= current.seq ? incoming : current;
  const previous = latest === incoming ? current : incoming;
  const latestHasPaths = latest.paths.length > 0;
  const keepPreviousPatch = !latest.patch && !latest.truncated;
  return {
    ...previous,
    ...latest,
    id: latest.report_id || latest.id || previous.id,
    paths: latestHasPaths ? latest.paths : previous.paths,
    paths_verified: latestHasPaths ? latest.paths_verified : previous.paths_verified,
    scope: latestHasPaths ? latest.scope : previous.scope,
    patch: latest.patch || (keepPreviousPatch ? previous.patch : undefined),
    status: latest.status || previous.status,
    truncated: latest.truncated || previous.truncated
  };
}

function safeUnverifiedDisplayName(value: string): string {
  const display = value.trim().replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 4 * 1024);
  if (!display || display.startsWith("/") || display.startsWith("\\\\") ||
    /^[A-Za-z]:[\\/]/.test(display) || /^file:\/+/.test(display.toLocaleLowerCase()) ||
    display === ".." || display.startsWith("../") || display.startsWith("..\\")) {
    return "";
  }
  return display;
}

function normalizeProviderReport(report: GitProviderChangeReport): GitProviderChangeReport {
  const verified = report.paths_verified === true && report.scope === "repository";
  return {
    ...report,
    // Unverified reports are display-only. Never retain their patch or feed it
    // to Pierre/path operations, even if a malformed response includes one.
    patch: verified ? report.patch : undefined,
    paths: verified
      ? report.paths.map(safeRunPath)
      : report.paths.map(safeUnverifiedDisplayName).filter(Boolean),
    paths_verified: verified,
    scope: verified ? "repository" : "unverified",
    truncated: Boolean(report.truncated)
  };
}

interface RunGitRetentionBudget {
  remaining: number;
  remainingPaths: number;
  limited: boolean;
}

function retainBudgetedPaths(
  values: string[],
  budget: RunGitRetentionBudget,
  sanitizer: (value: string) => string
): { items: string[]; limited: boolean } {
  const result: string[] = [];
  const seen = new Set<string>();
  let limited = false;
  for (const raw of values) {
    if (budget.remainingPaths <= 0) {
      budget.limited = true;
      limited = true;
      break;
    }
    const value = sanitizer(raw);
    if (!value) {
      limited = true;
      continue;
    }
    if (seen.has(value)) continue;
    const weight = 32 + value.length * 2;
    if (weight > budget.remaining) {
      budget.limited = true;
      limited = true;
      break;
    }
    seen.add(value);
    result.push(value);
    budget.remaining -= weight;
    budget.remainingPaths -= 1;
  }
  return { items: result, limited };
}

function retainProviderReport(
  source: GitProviderChangeReport,
  budget: RunGitRetentionBudget
): GitProviderChangeReport | undefined {
  const report = normalizeProviderReport(source);
  const verified = isVerifiedProviderReport(report);
  const scalarWeight = 512 + [
    report.id, report.occurred_at, report.source, report.provider_turn_id,
    report.scope, report.status
  ].reduce((total, value) => total + (value?.length ?? 0) * 2, 0);
  if (scalarWeight > budget.remaining) {
    budget.limited = true;
    return undefined;
  }
  budget.remaining -= scalarWeight;
  const retainedPaths = retainBudgetedPaths(
    report.paths,
    budget,
    verified ? safeRunPath : safeUnverifiedDisplayName
  );
  let reportLimited = retainedPaths.limited;

  let patch = report.patch;
  if (patch) {
    const patchWeight = patch.length * 2;
    if (patchWeight <= budget.remaining) budget.remaining -= patchWeight;
    else {
      patch = undefined;
      budget.limited = true;
      reportLimited = true;
    }
  }
  return {
    ...report,
    patch,
    paths: retainedPaths.items,
    truncated: report.truncated || reportLimited
  };
}

function retainedProviderReportWeight(report: GitProviderChangeReport): {
  bytes: number;
  paths: number;
} {
  let bytes = 512 + (report.patch?.length ?? 0) * 2 + [
    report.id, report.report_id, report.occurred_at, report.source,
    report.provider_turn_id, report.provider_item_id, report.source_item_id,
    report.item_id, report.scope, report.status
  ].reduce((total, value) => total + (value?.length ?? 0) * 2, 0);
  for (const path of report.paths) bytes += 32 + path.length * 2;
  return { bytes, paths: report.paths.length };
}

function replaceRetainedProviderReport(
  current: GitProviderChangeReport,
  incoming: GitProviderChangeReport,
  budget: RunGitRetentionBudget
): GitProviderChangeReport {
  const currentWeight = retainedProviderReportWeight(current);
  const replacementBudget: RunGitRetentionBudget = {
    remaining: budget.remaining + currentWeight.bytes,
    remainingPaths: budget.remainingPaths + currentWeight.paths,
    limited: budget.limited
  };
  const replacement = retainProviderReport(mergeProviderReports(current, incoming), replacementBudget);
  if (!replacement) {
    budget.limited = true;
    return current;
  }
  budget.remaining = replacementBudget.remaining;
  budget.remainingPaths = replacementBudget.remainingPaths;
  budget.limited = replacementBudget.limited;
  return replacement;
}

function retainWorkspaceSnapshot(
  source: GitRunWorkspaceSnapshot,
  budget: RunGitRetentionBudget
): GitRunWorkspaceSnapshot {
  let snapshotLimited = false;
  const scalarWeight = 512 + [
    source.repository_id, source.label, source.root_display, source.phase,
    source.captured_at, source.content_hash
  ].reduce((total, value) => total + (value?.length ?? 0) * 2, 0);
  if (budget.remaining >= scalarWeight) budget.remaining -= scalarWeight;
  else {
    budget.limited = true;
    snapshotLimited = true;
  }
  const areas = source.areas.map((sourceArea) => {
    const areaWeight = (sourceArea.area?.length ?? 0) * 2;
    if (areaWeight <= budget.remaining) budget.remaining -= areaWeight;
    else {
      budget.limited = true;
      snapshotLimited = true;
    }
    const retainedPaths = retainBudgetedPaths(sourceArea.paths ?? [], budget, safeRunPath);
    const retainedBinaryPaths = retainBudgetedPaths(sourceArea.binary_paths ?? [], budget, safeRunPath);
    let limited = retainedPaths.limited || retainedBinaryPaths.limited;
    let patch = sourceArea.patch;
    if (patch) {
      const patchWeight = patch.length * 2;
      if (patchWeight <= budget.remaining) budget.remaining -= patchWeight;
      else {
        patch = undefined;
        budget.limited = true;
        limited = true;
      }
    }
    snapshotLimited ||= limited;
    return {
      ...sourceArea,
      patch,
      paths: retainedPaths.items,
      binary_paths: retainedBinaryPaths.items,
      truncated: sourceArea.truncated || limited
    };
  });
  const untracked: GitRunUntrackedEntry[] = [];
  for (const entry of source.untracked ?? []) {
    if (budget.remainingPaths <= 0) {
      budget.limited = true;
      snapshotLimited = true;
      break;
    }
    const path = safeRunPath(entry.path);
    const weight = 96 + path.length * 2 + (entry.kind?.length ?? 0) * 2;
    if (weight > budget.remaining) {
      budget.limited = true;
      snapshotLimited = true;
      break;
    }
    budget.remaining -= weight;
    budget.remainingPaths -= 1;
    untracked.push({ ...entry, path });
  }
  const warnings: GitRunWorkspaceSnapshot["warnings"] = [];
  for (const warning of source.warnings ?? []) {
    const weight = 64 + (warning.code?.length ?? 0) * 2 + (warning.message?.length ?? 0) * 2;
    if (weight > budget.remaining) {
      budget.limited = true;
      snapshotLimited = true;
      break;
    }
    budget.remaining -= weight;
    warnings.push(warning);
  }
  return {
    ...source,
    areas,
    untracked,
    warnings,
    truncated: source.truncated || snapshotLimited
  };
}

export function runGitResultRetainedBytes(result: GitRunChangesResult): number {
  let bytes = 1_024;
  for (const snapshot of result.snapshots) {
    bytes += 512 + [
      snapshot.repository_id, snapshot.label, snapshot.root_display, snapshot.phase,
      snapshot.captured_at, snapshot.content_hash
    ].reduce((total, value) => total + (value?.length ?? 0) * 2, 0);
    for (const area of snapshot.areas) {
      bytes += (area.patch?.length ?? 0) * 2 + (area.area?.length ?? 0) * 2;
      for (const path of [...area.paths, ...area.binary_paths]) bytes += 32 + path.length * 2;
    }
    for (const entry of snapshot.untracked) {
      bytes += 96 + entry.path.length * 2 + (entry.kind?.length ?? 0) * 2;
    }
    for (const warning of snapshot.warnings) {
      bytes += 64 + (warning.code?.length ?? 0) * 2 + (warning.message?.length ?? 0) * 2;
    }
  }
  for (const report of result.provider_reports) {
    bytes += retainedProviderReportWeight(report).bytes;
  }
  return bytes;
}

/** Follows the server's scan cursor while the client retention budget permits.
 * Cursor identity, not the last report seq, is authoritative because the
 * server may scan across events that did not produce a report. */
export async function fetchAllRunGitChanges(
  runId: string,
  signal?: AbortSignal
): Promise<GitRunChangesResult> {
  const reports = new Map<string, GitProviderChangeReport>();
  const reportAliasIndex = new Map<string, string>();
  const reportsByTurnSource = new Map<string, Set<string>>();
  const snapshots = new Map<string, GitRunWorkspaceSnapshot>();
  const seenCursors = new Set<string>();
  let cursor = "";
  let unresolvedTruncation = false;
  let nextProviderCursor = "";
  const retention: RunGitRetentionBudget = {
    remaining: MAX_RUN_GIT_RETAINED_BYTES - 1_024,
    remainingPaths: MAX_RUN_GIT_RETAINED_PATHS,
    limited: false
  };

  for (let pageIndex = 0; pageIndex < MAX_RUN_GIT_HISTORY_PAGES; pageIndex += 1) {
    signal?.throwIfAborted();
    const page = await fetchRunGitChangesPage(runId, cursor, signal);
    if (page.run_id !== runId) throw new Error("Run Git changes response does not match the requested Run");
    for (const snapshot of page.snapshots ?? []) {
      const key = `${snapshot.repository_id}\u0000${snapshot.phase}\u0000${snapshot.content_hash}`;
      if (!snapshots.has(key)) snapshots.set(key, retainWorkspaceSnapshot(snapshot, retention));
    }
    for (const source of page.provider_reports ?? []) {
      const normalized = normalizeProviderReport(source);
      const aliases = providerReportAliases(normalized);
      let identity = aliases.map((alias) => reportAliasIndex.get(alias)).find(Boolean);
      const turnSourceKey = providerReportTurnSourceKey(normalized);
      if (!identity && !providerReportHasContent(normalized) && turnSourceKey) {
        const candidates = [...(reportsByTurnSource.get(turnSourceKey) ?? [])]
          .filter((key) => reports.has(key));
        // A status-only update can safely attach without an item ID only when
        // exactly one report exists in that Provider turn/source. Ambiguity is
        // deliberately not resolved by path or event ID, so independent file
        // changes are never collapsed.
        if (candidates.length === 1) identity = candidates[0];
      }

      if (identity && reports.has(identity)) {
        const merged = replaceRetainedProviderReport(reports.get(identity)!, normalized, retention);
        reports.set(identity, merged);
        for (const alias of [...aliases, ...providerReportAliases(merged)]) {
          reportAliasIndex.set(alias, identity);
        }
        continue;
      }
      // A standalone status-only row has no displayable file authority. It is
      // useful only as an update to a uniquely identified existing report.
      if (!providerReportHasContent(normalized)) continue;
      identity = providerReportIdentity(normalized);
      while (reports.has(identity)) identity += "\u0000" + normalized.seq;
      const report = retainProviderReport(normalized, retention);
      if (!report) continue;
      reports.set(identity, report);
      for (const alias of providerReportAliases(report)) reportAliasIndex.set(alias, identity);
      if (turnSourceKey) {
        const candidates = reportsByTurnSource.get(turnSourceKey) ?? new Set<string>();
        candidates.add(identity);
        reportsByTurnSource.set(turnSourceKey, candidates);
      }
    }

    if (!page.provider_reports_truncated) {
      unresolvedTruncation = retention.limited;
      nextProviderCursor = "";
      break;
    }
    const next = String(page.next_provider_cursor ?? "").trim();
    nextProviderCursor = next;
    if (!next || next === cursor || seenCursors.has(next)) {
      unresolvedTruncation = true;
      break;
    }
    if (retention.limited) {
      unresolvedTruncation = true;
      break;
    }
    seenCursors.add(next);
    cursor = next;
    if (pageIndex === MAX_RUN_GIT_HISTORY_PAGES - 1) unresolvedTruncation = true;
  }

  return {
    run_id: runId,
    snapshots: [...snapshots.values()],
    provider_reports: [...reports.values()].sort((left, right) =>
      left.seq - right.seq || left.id.localeCompare(right.id)
    ),
    provider_reports_truncated: unresolvedTruncation || retention.limited,
    next_provider_cursor: unresolvedTruncation && nextProviderCursor ? nextProviderCursor : undefined
  };
}

export function fetchRunGitChanges(runId: string, signal?: AbortSignal): Promise<GitRunChangesResult> {
  return fetchAllRunGitChanges(runId, signal);
}

export function runHasGitChanges(result?: GitRunChangesResult): boolean {
  if (!result) return false;
  if (result.provider_reports.some((report) => report.patch || report.paths.length || report.truncated)) return true;
  return result.snapshots.some((snapshot) =>
    snapshot.untracked.length > 0 || snapshot.areas.some((area) => area.patch || area.paths.length || area.truncated)
  );
}

export function isVerifiedProviderReport(report: GitProviderChangeReport): boolean {
  return report.paths_verified === true && report.scope === "repository";
}

export function safeRunPath(value: string): string {
  const normalized = value.trim().replaceAll("\\", "/");
  let display = safeGitPathDisplay(normalized).replace(/[\u0000-\u001f\u007f]/g, "");
  if (display.startsWith("/") || /^[A-Za-z]:\//.test(display) || display === ".." || display.startsWith("../") || display.startsWith("~/") || /^file:\/+/.test(display.toLocaleLowerCase())) {
    display = display.split("/").filter(Boolean).at(-1) ?? "";
  }
  const fallback = (normalized.split("/").filter(Boolean).at(-1) ?? "").replace(/[\u0000-\u001f\u007f]/g, "");
  const result = display || fallback || "unknown";
  return result.length > 4 * 1024 ? `${result.slice(0, 4 * 1024 - 1)}…` : result;
}

function protocolNoise(value: string): boolean {
  const normalized = value.replaceAll("\\", "/").toLocaleLowerCase();
  return normalized.includes(".teamva/tool-");
}

export function parseRunPatchFiles(patch: string, cacheKey: string): FileDiffMetadata[] {
  if (!patch || patch.length > 2 * 1024 * 1024) return [];
  let fileCount = patch.startsWith("diff --git ") ? 1 : 0;
  for (let offset = patch.indexOf("\ndiff --git "); offset >= 0; offset = patch.indexOf("\ndiff --git ", offset + 1)) {
    fileCount += 1;
    if (fileCount > MAX_RUN_GIT_RETAINED_PATHS) return [];
  }
  try {
    return parsePatchFiles(patch, cacheKey, true)
      .flatMap((parsed) => parsed.files)
      .filter((file) => !protocolNoise(file.name))
      .map((file) => {
        file.name = safeRunPath(file.name);
        if (file.prevName) file.prevName = safeRunPath(file.prevName);
        return file;
      });
  } catch {
    return [];
  }
}

function parseGitHeaderToken(value: string, start: number): { value: string; next: number } | undefined {
  let offset = start;
  while (value[offset] === " ") offset += 1;
  if (offset >= value.length) return undefined;
  if (value[offset] !== '"') {
    const end = value.indexOf(" ", offset);
    return { value: value.slice(offset, end < 0 ? value.length : end), next: end < 0 ? value.length : end };
  }

  offset += 1;
  const parts: string[] = [];
  while (offset < value.length && value[offset] !== '"') {
    if (value[offset] !== "\\") {
      parts.push(value[offset++]);
      continue;
    }
    offset += 1;
    const escaped = value[offset];
    if (escaped && /[0-7]/.test(escaped)) {
      const bytes: number[] = [];
      while (offset + 2 < value.length && /^[0-7]{3}$/.test(value.slice(offset, offset + 3))) {
        bytes.push(Number.parseInt(value.slice(offset, offset + 3), 8));
        offset += 3;
        if (value[offset] !== "\\") break;
        offset += 1;
      }
      parts.push(new TextDecoder().decode(Uint8Array.from(bytes)));
      continue;
    }
    const replacements: Record<string, string> = {
      t: "\t", n: "\n", r: "\r", '"': '"', "\\": "\\"
    };
    parts.push(replacements[escaped] ?? escaped ?? "");
    offset += 1;
  }
  if (value[offset] === '"') offset += 1;
  return { value: parts.join(""), next: offset };
}

interface RunGitTargetBuildBudget {
  remainingPaths: number;
  remainingLines: number;
  limited: boolean;
  scannedLines: number;
  derived: RunGitDerivedBudget;
}

interface ScannedPatchSection {
  start: number;
  end: number;
  lineCount: number;
  path: string;
  oldPath?: string;
  binary: boolean;
  complete: boolean;
}

function firstPatchSectionStart(patch: string): number {
  if (patch.startsWith("diff --git ")) return 0;
  const next = patch.indexOf("\ndiff --git ");
  if (next >= 0) return next + 1;
  const hasUnifiedHeaders = (patch.startsWith("--- ") || patch.includes("\n--- ")) &&
    (patch.startsWith("+++ ") || patch.includes("\n+++ ")) && patch.includes("@@");
  return hasUnifiedHeaders ? 0 : -1;
}

function normalizedPatchToken(token: string): string {
  if (!token || token === "/dev/null") return "";
  return safeRunPath(token.replace(/^[ab]\//, ""));
}

function normalizedPatchHeaderPath(value: string): string {
  return normalizedPatchToken(parseGitHeaderToken(value, 0)?.value ?? "");
}

/**
 * Streams aggregate patch sections without splitting the patch or constructing
 * Pierre metadata. The shared line/path budget can stop scanning in the middle
 * of hostile high-file/high-line payloads.
 */
function scanPatchSections(
  patch: string,
  budget: RunGitTargetBuildBudget,
  visit: (section: ScannedPatchSection) => boolean
): void {
  let sectionStart = firstPatchSectionStart(patch);
  while (sectionStart >= 0) {
    if (budget.remainingPaths <= 0 || budget.remainingLines <= 0) {
      budget.limited = true;
      return;
    }
    const nextHeader = patch.indexOf("\ndiff --git ", sectionStart + 1);
    const sectionEnd = nextHeader < 0 ? patch.length : nextHeader + 1;
    const headerEndAt = patch.indexOf("\n", sectionStart);
    const headerEnd = headerEndAt < 0 || headerEndAt > sectionEnd ? sectionEnd : headerEndAt;
    const hasGitHeader = patch.startsWith("diff --git ", sectionStart);
    const header = hasGitHeader ? patch.slice(sectionStart + "diff --git ".length, headerEnd) : "";
    const first = hasGitHeader ? parseGitHeaderToken(header, 0) : undefined;
    const second = first ? parseGitHeaderToken(header, first.next) : undefined;
    let path = normalizedPatchToken(second?.value || first?.value || "");
    let oldPathValue = normalizedPatchToken(first?.value || "");

    let lineStart = sectionStart;
    let lineCount = 0;
    let binary = false;
    let complete = true;
    while (lineStart < sectionEnd) {
      if (budget.remainingLines <= 0) {
        budget.limited = true;
        complete = false;
        break;
      }
      budget.remainingLines -= 1;
      budget.scannedLines += 1;
      lineCount += 1;
      const newline = patch.indexOf("\n", lineStart);
      const lineEnd = newline < 0 || newline >= sectionEnd ? sectionEnd : newline;
      const lineLength = lineEnd - lineStart;
      if (!hasGitHeader && !oldPathValue && patch.startsWith("--- ", lineStart)) {
        oldPathValue = normalizedPatchHeaderPath(patch.slice(lineStart + 4, Math.min(lineEnd, lineStart + 4_096)));
      } else if (!hasGitHeader && !path && patch.startsWith("+++ ", lineStart)) {
        path = normalizedPatchHeaderPath(patch.slice(lineStart + 4, Math.min(lineEnd, lineStart + 4_096)));
      }
      if ((lineLength >= 13 && patch.startsWith("Binary files ", lineStart)) ||
        (lineLength >= 16 && patch.startsWith("GIT binary patch", lineStart))) {
        binary = true;
      }
      if (newline < 0 || newline >= sectionEnd) break;
      lineStart = newline + 1;
    }

    if (!visit({
      start: sectionStart, end: sectionEnd, lineCount, path,
      oldPath: oldPathValue && oldPathValue !== path ? oldPathValue : undefined,
      binary, complete
    })) {
      budget.limited = true;
      return;
    }
    if (!complete) return;
    sectionStart = nextHeader < 0 ? -1 : nextHeader + 1;
  }
}

export interface ProviderBinaryPathSet extends Set<string> {
  limited: boolean;
  scannedLines: number;
}

/** Bounded streaming binary-path scan; never builds a set for an entire giant patch. */
export function providerBinaryPaths(
  patch: string,
  maxPaths = MAX_RUN_GIT_RETAINED_PATHS,
  maxLines = MAX_RUN_GIT_SCANNED_LINES
): ProviderBinaryPathSet {
  const result = new Set<string>() as ProviderBinaryPathSet;
  Object.defineProperties(result, {
    limited: { value: false, enumerable: false, writable: true },
    scannedLines: { value: 0, enumerable: false, writable: true }
  });
  if (!patch || (!patch.includes("Binary files ") && !patch.includes("GIT binary patch"))) return result;
  const budget: RunGitTargetBuildBudget = {
    remainingPaths: Math.max(1, maxPaths),
    remainingLines: Math.max(1, maxLines),
    limited: false,
    scannedLines: 0,
    derived: { maxBytes: 0, maxLines: 0, usedBytes: 0, usedLines: 0 }
  };
  scanPatchSections(patch, budget, (section) => {
    budget.remainingPaths -= 1;
    if (!section.binary) return true;
    for (const value of [section.oldPath, section.path]) {
      if (!value || result.has(value)) continue;
      if (result.size >= Math.max(1, maxPaths)) return false;
      result.add(value);
    }
    return true;
  });
  result.limited = budget.limited;
  result.scannedLines = budget.scannedLines;
  return result;
}

function estimatedPierreBytes(section: RunGitPatchSection): number {
  // Pierre retains line/hunk objects in addition to source strings. Deliberately
  // overestimate so the lease remains a heap guard rather than a payload-only
  // counter.
  return 8_192 + (section.end - section.start) * 8 + section.lineCount * 256;
}

/** Lazily parses exactly one file section and leases shared derived memory. */
export function acquireRunGitTargetMetadata(target: RunGitFileTarget): RunGitMetadataLease {
  if (target.metadata) return { metadata: target.metadata, limited: false, release: () => {} };
  const section = target.patchSection;
  if (!section || target.binary || target.conflict) {
    return { limited: false, release: () => {} };
  }
  const budget = section.budget;
  const bytes = estimatedPierreBytes(section);
  if (section.lineCount > budget.maxLines - budget.usedLines || bytes > budget.maxBytes - budget.usedBytes) {
    return { limited: true, release: () => {} };
  }
  budget.usedBytes += bytes;
  budget.usedLines += section.lineCount;
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    budget.usedBytes = Math.max(0, budget.usedBytes - bytes);
    budget.usedLines = Math.max(0, budget.usedLines - section.lineCount);
  };
  try {
    const source = section.patch.slice(section.start, section.end);
    const metadata = parsePatchFiles(source, section.cacheKey, true)
      .flatMap((parsed) => parsed.files)
      .find((file) => !protocolNoise(file.name));
    if (!metadata) {
      release();
      return { limited: false, release: () => {} };
    }
    metadata.name = safeRunPath(metadata.name);
    if (metadata.prevName) metadata.prevName = safeRunPath(metadata.prevName);
    return { metadata, limited: false, release };
  } catch {
    release();
    return { limited: false, release: () => {} };
  }
}

function repositoryFor(
  snapshot: GitRunWorkspaceSnapshot | undefined,
  repositories: Array<{ id: string; label: string; root_display: string }>,
  fallbackID: string
): RunGitRepositoryDisplay {
  const id = snapshot?.repository_id || fallbackID;
  const current = repositories.find((item) => item.id === id);
  return {
    id,
    label: snapshot?.label || current?.label || "Repository",
    rootDisplay: safeRepositoryRootDisplay(snapshot?.root_display || current?.root_display || "")
  };
}

function fileTarget(options: Omit<RunGitFileTarget, "id">): RunGitFileTarget {
  return {
    ...options,
    id: [
      options.repository.id, options.runId, options.source, options.sourceId,
      options.phase ?? "", options.area, options.path, options.kind
    ].join("\u0000")
  };
}

export function buildRunGitFileTargets(
  results: GitRunChangesResult[],
  runs: Run[],
  repositories: Array<{ id: string; label: string; root_display: string }> = []
): RunGitFileTargets {
  const chronological = [...runs].sort((left, right) =>
    Date.parse(left.created_at) - Date.parse(right.created_at) || left.id.localeCompare(right.id)
  );
  const roundByRun = new Map(chronological.map((run, index) => [run.id, index + 1]));
  const runById = new Map(runs.map((run) => [run.id, run]));
  const targets: RunGitFileTarget[] = [];
  const budget: RunGitTargetBuildBudget = {
    remainingPaths: MAX_RUN_GIT_RETAINED_PATHS,
    remainingLines: MAX_RUN_GIT_SCANNED_LINES,
    limited: false,
    scannedLines: 0,
    derived: {
      maxBytes: MAX_RUN_GIT_DERIVED_BYTES,
      maxLines: MAX_RUN_GIT_DERIVED_LINES,
      usedBytes: 0,
      usedLines: 0
    }
  };

  const pushExplicitTarget = (options: Omit<RunGitFileTarget, "id">): boolean => {
    if (budget.remainingPaths <= 0) {
      budget.limited = true;
      return false;
    }
    budget.remainingPaths -= 1;
    targets.push(fileTarget(options));
    return true;
  };

  const sectionReference = (
    patch: string,
    section: ScannedPatchSection,
    cacheKey: string
  ): RunGitPatchSection | undefined => section.complete ? {
    patch,
    start: section.start,
    end: section.end,
    lineCount: section.lineCount,
    cacheKey,
    budget: budget.derived
  } : undefined;

  for (const result of results) {
    const run = runById.get(result.run_id);
    if (!run) continue;
    const round = roundByRun.get(run.id) ?? 1;
    for (const snapshot of result.snapshots) {
      const repository = repositoryFor(snapshot, repositories, `unknown:${run.id}`);
      for (const area of snapshot.areas) {
        const binary = new Set(area.binary_paths.map(safeRunPath));
        const renderedPaths = new Set<string>();
        const patch = area.patch ?? "";
        scanPatchSections(patch, budget, (section) => {
          if (!section.path || protocolNoise(section.path)) return true;
          if (budget.remainingPaths <= 0) return false;
          budget.remainingPaths -= 1;
          renderedPaths.add(section.path);
          const isBinary = section.binary || binary.has(section.path) ||
            Boolean(section.oldPath && binary.has(section.oldPath));
          const conflict = area.area === "conflict";
          const sectionLimited = !section.complete;
          targets.push(fileTarget({
            repository, runId: run.id, round, runCreatedAt: run.created_at,
            occurredAt: snapshot.captured_at, source: "observation",
            sourceId: `${snapshot.phase}:${snapshot.content_hash}`, phase: snapshot.phase,
            area: area.area, path: section.path, oldPath: section.oldPath,
            kind: isBinary ? "binary" : "file", binary: isBinary, conflict,
            truncated: area.truncated || snapshot.truncated || sectionLimited,
            patchSection: isBinary || conflict ? undefined : sectionReference(
              patch, section, `${run.id}:${snapshot.phase}:${area.area}:${section.start}`
            ),
            placeholder: conflict ? "conflict" : isBinary ? "binary" : sectionLimited || area.truncated ? "truncated" : "none"
          }));
          return true;
        });
        for (const rawPath of area.paths) {
          const path = safeRunPath(rawPath);
          if (renderedPaths.has(path) || protocolNoise(path)) continue;
          const isBinary = binary.has(path);
          const conflict = area.area === "conflict";
          if (!pushExplicitTarget({
            repository, runId: run.id, round, runCreatedAt: run.created_at,
            occurredAt: snapshot.captured_at, source: "observation",
            sourceId: `${snapshot.phase}:${snapshot.content_hash}`, phase: snapshot.phase,
            area: area.area, path, kind: isBinary ? "binary" : "file", binary: isBinary, conflict,
            truncated: area.truncated || snapshot.truncated,
            placeholder: conflict ? "conflict" : isBinary ? "binary" : area.truncated ? "truncated" : "no_patch"
          })) break;
        }
      }
      for (const entry of snapshot.untracked) {
        const path = safeRunPath(entry.path);
        if (protocolNoise(path)) continue;
        if (!pushExplicitTarget({
          repository, runId: run.id, round, runCreatedAt: run.created_at,
          occurredAt: snapshot.captured_at, source: "observation",
          sourceId: `${snapshot.phase}:${snapshot.content_hash}`, phase: snapshot.phase,
          area: "untracked", path, kind: entry.kind, binary: false, conflict: false,
          truncated: snapshot.truncated, placeholder: "untracked", sizeBytes: entry.size_bytes
        })) break;
      }
    }

    // Provider events currently contain repository-relative paths but no
    // repository identity. A single observed repository is safe to attribute;
    // a multi-repository workspace is not. Keep ambiguous reports in a separate
    // per-Run group instead of pretending they belong to the first snapshot.
    const snapshotRepositoryIDs = [...new Set(result.snapshots.map((item) => item.repository_id))];
    const snapshot = snapshotRepositoryIDs.length === 1
      ? result.snapshots.find((item) => item.repository_id === snapshotRepositoryIDs[0])
      : undefined;
    const repository = repositoryFor(snapshot, repositories, `reported:${run.id}`);
    for (const report of result.provider_reports) {
      if (!isVerifiedProviderReport(report)) continue;
      const reportTargetStart = targets.length;
      const patch = report.patch ?? "";
      const renderedPaths = new Set<string>();
      scanPatchSections(patch, budget, (section) => {
        if (!section.path || protocolNoise(section.path)) return true;
        if (budget.remainingPaths <= 0) return false;
        budget.remainingPaths -= 1;
        renderedPaths.add(section.path);
        const sectionLimited = !section.complete;
        targets.push(fileTarget({
          repository, runId: run.id, round, runCreatedAt: run.created_at,
          occurredAt: report.occurred_at, source: "provider", sourceId: report.id,
          providerSource: report.source, providerTurnId: report.provider_turn_id,
          area: "reported", path: section.path, oldPath: section.oldPath, status: report.status,
          kind: section.binary ? "binary" : "file", binary: section.binary, conflict: false,
          truncated: report.truncated || sectionLimited,
          patchSection: section.binary ? undefined : sectionReference(
            patch, section, `${run.id}:provider:${report.id}:${section.start}`
          ),
          placeholder: section.binary ? "binary" : sectionLimited || report.truncated ? "truncated" : "none"
        }));
        return true;
      });
      for (const rawPath of report.paths) {
        const path = safeRunPath(rawPath);
        if (renderedPaths.has(path) || protocolNoise(path)) continue;
        if (!pushExplicitTarget({
          repository, runId: run.id, round, runCreatedAt: run.created_at,
          occurredAt: report.occurred_at, source: "provider", sourceId: report.id,
          providerSource: report.source, providerTurnId: report.provider_turn_id,
          area: "reported", path, status: report.status, kind: "file", binary: false,
          conflict: false, truncated: report.truncated,
          placeholder: report.truncated ? "truncated" : "no_patch"
        })) break;
      }
      if (targets.length === reportTargetStart && report.paths.length === 0) {
        pushExplicitTarget({
          repository, runId: run.id, round, runCreatedAt: run.created_at,
          occurredAt: report.occurred_at, source: "provider", sourceId: report.id,
          providerSource: report.source, providerTurnId: report.provider_turn_id,
          area: "reported", path: report.source, status: report.status, kind: "report",
          binary: false, conflict: false, truncated: report.truncated || budget.limited,
          placeholder: report.truncated || budget.limited ? "truncated" : "no_patch"
        });
      }
    }
  }

  const sourceOrder = (target: RunGitFileTarget) =>
    target.phase === "start" ? 0 : target.source === "provider" ? 1 : 2;
  const sorted = targets.sort((left, right) =>
    left.repository.label.localeCompare(right.repository.label) ||
    Date.parse(left.runCreatedAt) - Date.parse(right.runCreatedAt) ||
    sourceOrder(left) - sourceOrder(right) ||
    Date.parse(left.occurredAt) - Date.parse(right.occurredAt) ||
    left.area.localeCompare(right.area) || left.path.localeCompare(right.path) || left.id.localeCompare(right.id)
  ) as RunGitFileTargets;
  Object.defineProperties(sorted, {
    buildLimited: { value: budget.limited, enumerable: false, writable: true },
    retainedPathCount: {
      value: MAX_RUN_GIT_RETAINED_PATHS - budget.remainingPaths,
      enumerable: false, writable: true
    },
    scannedLineCount: { value: budget.scannedLines, enumerable: false, writable: true },
    derivedBudget: { value: budget.derived, enumerable: false, writable: true }
  });
  return sorted;
}

export function collectUnverifiedProviderReports(
  results: GitRunChangesResult[],
  runs: Run[]
): RunGitUnverifiedReport[] {
  const chronological = [...runs].sort((left, right) =>
    Date.parse(left.created_at) - Date.parse(right.created_at) || left.id.localeCompare(right.id)
  );
  const roundByRun = new Map(chronological.map((run, index) => [run.id, index + 1]));
  const runIds = new Set(runs.map((run) => run.id));
  const reports: RunGitUnverifiedReport[] = [];
  for (const result of results) {
    if (!runIds.has(result.run_id)) continue;
    for (const report of result.provider_reports) {
      if (isVerifiedProviderReport(report)) continue;
      reports.push({
        id: report.id,
        runId: result.run_id,
        round: roundByRun.get(result.run_id) ?? 1,
        occurredAt: report.occurred_at,
        source: report.source,
        status: report.status,
        displayNames: report.paths.map(safeUnverifiedDisplayName).filter(Boolean),
        truncated: report.truncated
      });
    }
  }
  return reports;
}

export function runGitDiffLifecycleKey(target: RunGitFileTarget): string {
  return [
    target.repository.id,
    target.runId,
    target.source,
    target.sourceId,
    target.area,
    target.path,
    target.id
  ].join("\u0000");
}

interface DirectoryTargetNode {
  name: string;
  display: string;
  children: Map<string, DirectoryTargetNode>;
  files: RunGitFileTarget[];
}

function sourceGroupIdentity(target: RunGitFileTarget): string {
  if (target.source === "provider") {
    return `${target.runId}:provider:${target.providerTurnId || target.runId}`;
  }
  return `${target.runId}:observation:${target.phase ?? "end"}`;
}

function makeDirectoryTargets(targets: RunGitFileTarget[]): DirectoryTargetNode {
  const root: DirectoryTargetNode = { name: "", display: "", children: new Map(), files: [] };
  for (const target of targets) {
    const segments = target.path.split("/").filter(Boolean);
    let node = root;
    const directoryParts = segments.slice(0, -1);
    for (const segment of directoryParts) {
      const display = [...node.display ? node.display.split("/") : [], segment].join("/");
      let child = node.children.get(segment);
      if (!child) {
        child = { name: segment, display, children: new Map(), files: [] };
        node.children.set(segment, child);
      }
      node = child;
    }
    node.files.push(target);
  }
  return root;
}

function appendRunDirectoryRows(
  rows: RunGitTreeRow[],
  node: DirectoryTargetNode,
  parentKey: string,
  level: number,
  prefix: string,
  collapsed: Record<string, boolean | undefined>,
  searching: boolean
) {
  for (const child of [...node.children.values()].sort((left, right) => left.name.localeCompare(right.name))) {
    const key = `${prefix}:directory:${child.display}`;
    const expanded = searching || !collapsed[key];
    rows.push({
      key, type: "directory", level, parentKey, label: child.name,
      count: countDirectoryTargets(child), expanded, expandable: true
    });
    if (expanded) appendRunDirectoryRows(rows, child, key, level + 1, prefix, collapsed, searching);
  }
  for (const target of [...node.files].sort((left, right) => left.path.localeCompare(right.path) || left.id.localeCompare(right.id))) {
    rows.push({
      key: `file:${target.id}`, type: "file", level, parentKey,
      label: target.path.split("/").at(-1) || target.path, target
    });
  }
}

function countDirectoryTargets(node: DirectoryTargetNode): number {
  let count = node.files.length;
  for (const child of node.children.values()) count += countDirectoryTargets(child);
  return count;
}

export function buildRunGitTreeRows(options: {
  targets: RunGitFileTarget[];
  source: "all" | RunGitSource;
  search: string;
  collapsed: Record<string, boolean | undefined>;
}): RunGitTreeRow[] {
  const search = options.search.trim().toLocaleLowerCase();
  const searching = Boolean(search);
  const targets = options.targets.filter((target) =>
    (options.source === "all" || target.source === options.source) &&
    (!searching || `${target.path}\n${target.oldPath ?? ""}`.toLocaleLowerCase().includes(search))
  );
  const rows: RunGitTreeRow[] = [];
  const repositories = new Map<string, RunGitFileTarget[]>();
  for (const target of targets) {
    const existing = repositories.get(target.repository.id) ?? [];
    existing.push(target);
    repositories.set(target.repository.id, existing);
  }
  for (const repositoryTargets of [...repositories.values()].sort((left, right) =>
    left[0].repository.label.localeCompare(right[0].repository.label) ||
    left[0].repository.id.localeCompare(right[0].repository.id)
  )) {
    const repository = repositoryTargets[0].repository;
    const repositoryKey = `run-repository:${repository.id}`;
    const repositoryExpanded = searching || !options.collapsed[repositoryKey];
    rows.push({
      key: repositoryKey, type: "repository", level: 1, label: repository.label,
      count: repositoryTargets.length, repository, expanded: repositoryExpanded, expandable: true
    });
    if (!repositoryExpanded) continue;

    const groups = new Map<string, RunGitFileTarget[]>();
    for (const target of repositoryTargets) {
      const key = sourceGroupIdentity(target);
      const existing = groups.get(key) ?? [];
      existing.push(target);
      groups.set(key, existing);
    }
    const orderedGroups = [...groups.entries()].sort(([, left], [, right]) =>
      Date.parse(left[0].runCreatedAt) - Date.parse(right[0].runCreatedAt) ||
      Date.parse(left[0].occurredAt) - Date.parse(right[0].occurredAt) ||
      sourceGroupIdentity(left[0]).localeCompare(sourceGroupIdentity(right[0]))
    );
    for (const [identity, groupTargets] of orderedGroups) {
      const first = groupTargets[0];
      const groupKey = `run-source:${repository.id}:${identity}`;
      const groupExpanded = searching || !options.collapsed[groupKey];
      rows.push({
        key: groupKey, type: "source", level: 2, parentKey: repositoryKey,
        label: first.source, count: groupTargets.length, source: first.source,
        providerTurnId: first.providerTurnId,
        phase: first.phase, runId: first.runId, round: first.round,
        occurredAt: first.occurredAt, expanded: groupExpanded, expandable: true
      });
      if (!groupExpanded) continue;
      const areas = new Map<string, RunGitFileTarget[]>();
      for (const target of groupTargets) {
        const existing = areas.get(target.area) ?? [];
        existing.push(target);
        areas.set(target.area, existing);
      }
      const areaOrder = ["conflict", "staged", "unstaged", "untracked", "reported"];
      for (const [area, areaTargets] of [...areas.entries()].sort((left, right) =>
        areaOrder.indexOf(left[0]) - areaOrder.indexOf(right[0]) || left[0].localeCompare(right[0])
      )) {
        const areaKey = `${groupKey}:area:${area}`;
        const areaExpanded = searching || !options.collapsed[areaKey];
        rows.push({
          key: areaKey, type: "area", level: 3, parentKey: groupKey,
          label: area, area, count: areaTargets.length, expanded: areaExpanded, expandable: true
        });
        if (!areaExpanded) continue;
        appendRunDirectoryRows(
          rows, makeDirectoryTargets(areaTargets), areaKey, 4,
          `${repository.id}:${identity}:${area}`, options.collapsed, searching
        );
      }
    }
  }
  return rows;
}
