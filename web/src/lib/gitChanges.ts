import { APIError, api, apiBaseURL } from "./api";
import { localizedGitWarning } from "./localizedMessages";
import { queryClient } from "./query";

export const GIT_CHANGE_AREAS = ["conflict", "staged", "unstaged", "untracked"] as const;

export type GitChangeArea = (typeof GIT_CHANGE_AREAS)[number] | (string & {});
export type GitChangesView = "tree" | "flat";
export type GitDiffLayout = "split" | "unified";

export interface GitDisplayPath {
  token: string;
  display: string;
  encoding?: string;
}

export interface GitRepositoryBranch {
  name?: string | null;
  display?: string | null;
  detached?: boolean;
  unborn?: boolean;
  oid?: string | null;
}

export type GitRepositoryWarning = string | {
  code?: string;
  message?: string;
};

export interface GitRepositoryCounts {
  conflict?: number;
  conflicts?: number;
  staged?: number;
  unstaged?: number;
  untracked?: number;
  total?: number;
  dirty?: number;
  [key: string]: number | undefined;
}

export type GitRepositoryScope = "full" | "resource_prefix";

export interface GitRepositorySummary {
  id: string;
  common_id?: string;
  label: string;
  root_display: string;
  resource_ids: string[];
  discovery_origin: string;
  worktree_kind: string;
  worktree?: { linked: boolean; main: boolean };
  scope: GitRepositoryScope;
  branch: string | null | GitRepositoryBranch;
  operation: string | null | { kind?: string; state?: string };
  generation: number;
  state: string | { kind?: string; message?: string };
  counts: GitRepositoryCounts;
  warnings: GitRepositoryWarning[];
}

export interface GitRepositoriesSnapshot {
  snapshot_id: string;
  repositories: GitRepositorySummary[];
  truncated: boolean;
  warnings: GitRepositoryWarning[];
}

type WireGitRepositorySummary = Omit<GitRepositorySummary, "resource_ids" | "warnings"> & {
  resource_ids?: string[] | null;
  warnings?: GitRepositoryWarning[] | null;
};

type WireGitRepositoriesSnapshot = Omit<GitRepositoriesSnapshot, "repositories" | "warnings"> & {
  repositories?: WireGitRepositorySummary[] | null;
  warnings?: GitRepositoryWarning[] | null;
};

export interface GitChange {
  id: string;
  path_id: string;
  area: GitChangeArea;
  status: string;
  kind: string;
  path: GitDisplayPath;
  old_path?: GitDisplayPath | null;
  similarity?: number | null;
  binary: boolean;
  conflict_code?: string | null;
  conflict?: { index_status?: string; worktree_status?: string } | null;
  submodule: boolean | Record<string, unknown> | null;
  diff_available: boolean;
  additions?: number | null;
  deletions?: number | null;
}

export interface GitChangesPage {
  snapshot_id: string;
  repository_id?: string;
  repository_generation: number;
  generation?: number;
  area?: GitChangeArea;
  items: GitChange[];
  next_cursor?: string | null;
  truncated: boolean;
  warnings?: GitRepositoryWarning[];
}

type WireGitChangesPage = Omit<GitChangesPage, "items" | "warnings"> & {
  items?: GitChange[] | null;
  warnings?: GitRepositoryWarning[] | null;
};

export interface GitDiffStats {
  additions?: number;
  deletions?: number;
  lines?: number;
  bytes?: number;
  binary?: boolean;
  [key: string]: number | boolean | undefined;
}

export interface GitDiffImage {
  url?: string;
  src?: string;
  data_url?: string;
  mime_type?: string;
  width?: number;
  height?: number;
  alt?: string;
  size_bytes?: number;
}

export interface GitDiffImages {
  before?: GitDiffImage | string | null;
  after?: GitDiffImage | string | null;
  old?: GitDiffImage | string | null;
  new?: GitDiffImage | string | null;
  previous?: GitDiffImage | string | null;
  current?: GitDiffImage | string | null;
}

export interface GitDiffResponse {
  snapshot_id: string;
  repository_id?: string;
  change_id?: string;
  generation?: number;
  comparison: string | Record<string, unknown>;
  kind: string;
  path: GitDisplayPath;
  old_path?: GitDisplayPath | null;
  language?: string | null;
  stats: GitDiffStats;
  patch?: string | null;
  truncated: boolean;
  truncation?: {
    truncated: boolean;
    reason: string;
    limit_bytes?: number;
  } | null;
  confirm_required: boolean;
  message_code?: string | null;
  message?: string | null;
  images?: GitDiffImages | GitDiffImage[] | null;
}

export interface GitRepositoryEvent {
  repository_id: string;
  generation: number;
  reason: string;
}

export interface GitDiffTarget {
  repository: GitRepositorySummary;
  change: GitChange;
}

export const MAX_AUTOMATIC_DIFF_PREFETCH_BYTES = 4 * 1024 * 1024;
const GIT_READ_WATCH_EVENT_GRACE_MS = 2_500;
let activeGitReadObservations = 0;
let suppressGitReadWatchEventsUntil = 0;

async function observeGitRead<T>(request: () => Promise<T>): Promise<T> {
  activeGitReadObservations += 1;
  try {
    return await request();
  } finally {
    activeGitReadObservations = Math.max(0, activeGitReadObservations - 1);
    if (activeGitReadObservations === 0) {
      suppressGitReadWatchEventsUntil = Date.now() + GIT_READ_WATCH_EVENT_GRACE_MS;
    }
  }
}

/** Local Git implementations may refresh index metadata during a read. The
 * watcher coalesces those filesystem notifications for up to two seconds, so
 * events in this bounded grace window are observations, not external writes. */
export function isGitReadWatchEventSuppressed(now = Date.now()): boolean {
  return activeGitReadObservations > 0 || now <= suppressGitReadWatchEventsUntil;
}

export interface GitPrefixLoadState {
  items: GitChange[];
  nextCursor?: string | null;
  truncated: boolean;
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  error?: unknown;
  warnings?: GitRepositoryWarning[];
}

export type GitAreaLoadState = GitPrefixLoadState;

export interface GitRepositoryLoadState {
  items: GitChange[];
  nextCursor?: string | null;
  truncated: boolean;
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  error?: unknown;
  snapshotId?: string;
  generation?: number;
  prefixes: Record<string, GitPrefixLoadState>;
  areas: Record<string, GitAreaLoadState>;
}

export type GitTreeRowType =
  | "repository"
  | "warning"
  | "area"
  | "directory"
  | "change"
  | "loading"
  | "error"
  | "load-prefix"
  | "empty"
  | "load-more";

export interface GitTreeRow {
  key: string;
  type: GitTreeRowType;
  repositoryId: string;
  level: number;
  parentKey?: string;
  label?: string;
  repository?: GitRepositorySummary;
  area?: GitChangeArea;
  count?: number;
  change?: GitChange;
  expanded?: boolean;
  expandable?: boolean;
  directoryToken?: string;
  directoryDisplay?: string;
  lazyUntracked?: boolean;
  prefixToken?: string;
  cursor?: string;
  warning?: string;
  error?: unknown;
  countsAuthoritative?: boolean;
  partialSearch?: boolean;
}

export interface BuildGitTreeRowsOptions {
  repositories: GitRepositorySummary[];
  states: Record<string, GitRepositoryLoadState | undefined>;
  expandedRepositories: Record<string, boolean | undefined>;
  collapsedAreas: Record<string, boolean | undefined>;
  expandedDirectories: Record<string, boolean | undefined>;
  view: GitChangesView;
  search: string;
  onlyDirty: boolean;
  repositoryListTruncated?: boolean;
}

interface DirectoryNode {
  key: string;
  name: string;
  display: string;
  token?: string;
  directoryChange?: GitChange;
  children: Array<{ type: "directory"; node: DirectoryNode } | { type: "change"; change: GitChange }>;
  childDirectories: Map<string, DirectoryNode>;
}

export function emptyGitRepositoryLoadState(): GitRepositoryLoadState {
  return {
    items: [],
    truncated: false,
    loaded: false,
    loading: false,
    loadingMore: false,
    prefixes: {},
    areas: {}
  };
}

export function gitRepositoriesQueryKey(projectId: string) {
  return ["git", "repositories", projectId] as const;
}

interface GitRepositorySnapshotFlight {
  sequence: number;
  refresh: boolean;
  controller: AbortController;
  promise: Promise<GitRepositoriesSnapshot>;
  replacement?: GitRepositorySnapshotFlight;
}

interface GitRepositoryProjectCoordinator {
  nextSequence: number;
  publishedSequence: number;
  published?: GitRepositoriesSnapshot;
  flight?: GitRepositorySnapshotFlight;
}

const gitRepositoryCoordinators = new Map<string, GitRepositoryProjectCoordinator>();

function repositoryCoordinator(projectId: string): GitRepositoryProjectCoordinator {
  let coordinator = gitRepositoryCoordinators.get(projectId);
  if (!coordinator) {
    coordinator = { nextSequence: 0, publishedSequence: 0 };
    gitRepositoryCoordinators.set(projectId, coordinator);
  }
  return coordinator;
}

function repositoryAbortError(): DOMException {
  return new DOMException("Git repository snapshot superseded", "AbortError");
}

function isRepositoryAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function awaitRepositorySnapshot<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(repositoryAbortError());
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(repositoryAbortError());
    signal.addEventListener("abort", abort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", abort);
        resolve(value);
      },
      (error) => {
        signal.removeEventListener("abort", abort);
        reject(error);
      }
    );
  });
}

async function followRepositorySnapshotFlight(
  coordinator: GitRepositoryProjectCoordinator,
  initialFlight: GitRepositorySnapshotFlight,
  signal?: AbortSignal
): Promise<GitRepositoriesSnapshot> {
  let flight = initialFlight;
  while (true) {
    try {
      return await awaitRepositorySnapshot(flight.promise, signal);
    } catch (error) {
      if (signal?.aborted) throw error;
      const replacement = flight.replacement ??
        (coordinator.flight !== flight ? coordinator.flight : undefined);
      if (isRepositoryAbortError(error) && replacement) {
        flight = replacement;
        continue;
      }
      if (flight.sequence < coordinator.publishedSequence && coordinator.published) {
        return coordinator.published;
      }
      throw error;
    }
  }
}

async function fetchGitRepositoriesRaw(
  projectId: string,
  refresh: boolean,
  signal: AbortSignal
): Promise<GitRepositoriesSnapshot> {
  const query = refresh ? "?refresh=true" : "";
  const snapshot = await api<WireGitRepositoriesSnapshot>(
    `/projects/${encodeURIComponent(projectId)}/git/repositories${query}`,
    { signal }
  );
  return {
    ...snapshot,
    repositories: (snapshot.repositories ?? []).map((repository) => ({
      ...repository,
      resource_ids: repository.resource_ids ?? [],
      warnings: repository.warnings ?? []
    })),
    warnings: snapshot.warnings ?? []
  };
}

function startRepositorySnapshotFlight(
  projectId: string,
  coordinator: GitRepositoryProjectCoordinator,
  refresh: boolean
): GitRepositorySnapshotFlight {
  const previous = coordinator.flight;
  const controller = new AbortController();
  const sequence = ++coordinator.nextSequence;
  let flight!: GitRepositorySnapshotFlight;
  const promise = (async () => {
    try {
      const snapshot = await awaitRepositorySnapshot(
        fetchGitRepositoriesRaw(projectId, refresh, controller.signal),
        controller.signal
      );
      if (controller.signal.aborted) throw repositoryAbortError();
      if (sequence < coordinator.publishedSequence) {
        if (coordinator.published) return coordinator.published;
        throw repositoryAbortError();
      }
      coordinator.publishedSequence = sequence;
      coordinator.published = snapshot;
      queryClient.setQueryData(gitRepositoriesQueryKey(projectId), snapshot);
      return snapshot;
    } catch (error) {
      if (controller.signal.aborted && !isRepositoryAbortError(error)) {
        throw repositoryAbortError();
      }
      throw error;
    }
  })().finally(() => {
    if (coordinator.flight === flight) coordinator.flight = undefined;
  });
  flight = { sequence, refresh, controller, promise };
  coordinator.flight = flight;
  if (previous) {
    previous.replacement = flight;
    previous.controller.abort();
  }
  return flight;
}

/** Test-only lifecycle hook. Production callers share coordinator state for the
 * full module lifetime so route transitions cannot reopen a stale authority. */
export function resetGitRepositoryCoordinator(projectId?: string) {
  if (projectId) {
    gitRepositoryCoordinators.get(projectId)?.flight?.controller.abort();
    gitRepositoryCoordinators.delete(projectId);
    return;
  }
  for (const coordinator of gitRepositoryCoordinators.values()) {
    coordinator.flight?.controller.abort();
  }
  gitRepositoryCoordinators.clear();
}

export interface GitChangesRequest {
  area: GitChangeArea;
  cursor?: string | null;
  limit?: number;
  prefix?: string;
  expandUntracked?: boolean;
}

export function gitDiffQueryKey(options: {
  projectId: string;
  snapshotId: string;
  repositoryId: string;
  repositoryGeneration: number;
  changeId: string;
  area?: GitChangeArea;
  pathToken?: string;
  context: number;
  allowLarge: boolean;
}) {
  return [
    "git", "diff", options.projectId, options.repositoryId, options.changeId,
    options.area ?? "", options.pathToken ?? "", options.snapshotId,
    options.repositoryGeneration, options.context, options.allowLarge
  ] as const;
}

export async function fetchGitRepositories(
  projectId: string,
  options: { refresh?: boolean; signal?: AbortSignal } = {}
): Promise<GitRepositoriesSnapshot> {
  if (options.signal?.aborted) throw repositoryAbortError();
  const coordinator = repositoryCoordinator(projectId);
  let flight = coordinator.flight;
  if (!flight || (Boolean(options.refresh) && !flight.refresh)) {
    flight = startRepositorySnapshotFlight(projectId, coordinator, Boolean(options.refresh));
  }
  return followRepositorySnapshotFlight(coordinator, flight, options.signal);
}

export async function fetchGitChanges(
  projectId: string,
  repositoryId: string,
  request: GitChangesRequest,
  signal?: AbortSignal
): Promise<GitChangesPage> {
  const query = new URLSearchParams({
    area: request.area,
    cursor: request.cursor ?? "",
    limit: String(request.limit ?? 500),
    prefix: request.prefix ?? "",
    expand_untracked: String(request.expandUntracked ?? false)
  });
  return observeGitRead(async () => {
    const page = await api<WireGitChangesPage>(
      `/projects/${encodeURIComponent(projectId)}/git/repositories/${encodeURIComponent(repositoryId)}/changes?${query}`,
      { signal }
    );
    return {
      ...page,
      items: page.items ?? [],
      warnings: page.warnings ?? []
    };
  });
}

export function fetchGitDiff(options: {
  projectId: string;
  repositoryId: string;
  changeId: string;
  context?: number;
  allowLarge?: boolean;
  signal?: AbortSignal;
}): Promise<GitDiffResponse> {
  const query = new URLSearchParams({
    context: String(options.context ?? 3),
    allow_large: String(options.allowLarge ?? false)
  });
  return observeGitRead(() => api<GitDiffResponse>(
    `/projects/${encodeURIComponent(options.projectId)}/git/repositories/${encodeURIComponent(options.repositoryId)}/changes/${encodeURIComponent(options.changeId)}/diff?${query}`,
    { signal: options.signal }
  ));
}

export function gitChangeIdentity(repositoryId: string, change: GitChange): string {
  return `${repositoryId}\u0000${change.area}\u0000${change.id}\u0000${change.path.token}`;
}

/** UI intent only: IDs/path tokens are snapshot authority and must never survive
 * invalidation. Exact display+encoding may rebind a choice to a fresh, unique
 * server-issued change, but are never sent as read authority themselves. */
export interface GitChangeSelectionIntent {
  projectId: string;
  repositoryId: string;
  area: GitChangeArea;
  pathDisplay: string;
  pathEncoding: string;
}

export function gitChangeSelectionIntent(projectId: string, repositoryId: string, change: GitChange): GitChangeSelectionIntent {
  return { projectId, repositoryId, area: change.area, pathDisplay: change.path.display, pathEncoding: change.path.encoding ?? "" };
}

export function restoreGitChangeSelection(
  intent: GitChangeSelectionIntent,
  projectId: string,
  repositoryId: string,
  changes: readonly GitChange[]
): GitChange | undefined {
  if (intent.projectId !== projectId || intent.repositoryId !== repositoryId) return undefined;
  const matches = changes.filter((change) => change.kind !== "directory" && change.kind !== "tree" &&
    change.area === intent.area && change.path.display === intent.pathDisplay &&
    (change.path.encoding ?? "") === intent.pathEncoding);
  // Lossy display names are not enough to disambiguate multiple fresh paths.
  return matches.length === 1 ? matches[0] : undefined;
}

/** Component/query identity for every piece of server-issued diff authority. */
export function gitDiffLifecycleKey(target: GitDiffTarget): string {
  return [
    target.repository.id,
    target.change.id,
    target.change.area,
    target.change.path.token
  ].join("\u0000");
}

/** Conservative retained-heap estimate used only for automatic prefetch. */
export function gitDiffRetainedBytes(diff: GitDiffResponse): number {
  const stringBytes = (value?: string | null) => (value?.length ?? 0) * 2;
  const reportedPatchBytes = Number(diff.stats.bytes ?? 0);
  let bytes = 1_024 + Math.max(
    stringBytes(diff.patch),
    Number.isFinite(reportedPatchBytes) && reportedPatchBytes > 0 ? reportedPatchBytes : 0
  );
  bytes += stringBytes(diff.message) + stringBytes(diff.language);
  bytes += stringBytes(diff.path?.token) + stringBytes(diff.path?.display);
  bytes += stringBytes(diff.old_path?.token) + stringBytes(diff.old_path?.display);

  const seenImages = new Set<GitDiffImage | string>();
  const images = Array.isArray(diff.images) ? diff.images : diff.images ? Object.values(diff.images) : [];
  for (const image of images) {
    if (!image || seenImages.has(image)) continue;
    seenImages.add(image);
    if (typeof image === "string") {
      bytes += stringBytes(image);
      continue;
    }
    const reportedImageBytes = Number(image.size_bytes ?? 0);
    bytes += Math.max(
      Number.isFinite(reportedImageBytes) && reportedImageBytes > 0 ? reportedImageBytes : 0,
      stringBytes(image.url) + stringBytes(image.src) + stringBytes(image.data_url)
    );
    bytes += stringBytes(image.alt) + stringBytes(image.mime_type) + 256;
  }
  return Math.max(0, Math.ceil(bytes));
}

export function nextAutomaticDiffPrefetchBytes(
  retainedBytes: number,
  diff: GitDiffResponse,
  maximumBytes = MAX_AUTOMATIC_DIFF_PREFETCH_BYTES
): number | undefined {
  const next = Math.max(0, retainedBytes) + gitDiffRetainedBytes(diff);
  return next <= Math.max(0, maximumBytes) ? next : undefined;
}

export const RECOVERABLE_GIT_SNAPSHOT_CODES = new Set([
  "change_not_found",
  "invalid_cursor",
  "snapshot_expired"
]);

/** A scope denial requires an explicit resource authorization check, not a new
 * snapshot or a network reconnect. Session/CSRF recovery stays owned by api(). */
export function isGitResourceAuthorizationError(error: unknown): boolean {
  return error instanceof APIError && error.status !== 401 && error.code !== "csrf_failed" &&
    (error.status === 403 || error.code === "path_out_of_scope" || error.code === "invalid_scope");
}

export function isRecoverableGitSnapshotError(error: unknown): boolean {
  return error instanceof APIError && !isGitResourceAuthorizationError(error) && RECOVERABLE_GIT_SNAPSHOT_CODES.has(error.code);
}

/** Retry a snapshot-bound operation exactly once after the caller has thrown
 * away all path/change/cursor authority and refreshed repository discovery. */
export async function retryGitSnapshotRequest<T>(
  request: () => Promise<T>,
  refresh: () => Promise<void>
): Promise<T> {
  try {
    return await request();
  } catch (error) {
    if (!isRecoverableGitSnapshotError(error)) throw error;
    await refresh();
    return request();
  }
}

export interface GitRequestToken {
  readonly signal: AbortSignal;
  readonly epoch: number;
  readonly sequence: number;
  isCurrent(): boolean;
  finish(): void;
}

/** Per-repository request gate. Requests in different areas can run together,
 * while a newer request for the same scope supersedes the old one. invalidate
 * aborts every in-flight path/cursor request in one operation. */
export class GitRequestGate {
  #epoch = 0;
  #sequences = new Map<string, number>();
  #controllers = new Set<AbortController>();
  #controllerByScope = new Map<string, AbortController>();

  begin(scope: string): GitRequestToken {
    this.#controllerByScope.get(scope)?.abort();
    const epoch = this.#epoch;
    const sequence = (this.#sequences.get(scope) ?? 0) + 1;
    this.#sequences.set(scope, sequence);
    const controller = new AbortController();
    this.#controllers.add(controller);
    this.#controllerByScope.set(scope, controller);
    let finished = false;
    return {
      signal: controller.signal,
      epoch,
      sequence,
      isCurrent: () => !finished && !controller.signal.aborted &&
        epoch === this.#epoch && this.#sequences.get(scope) === sequence,
      finish: () => {
        if (finished) return;
        finished = true;
        this.#controllers.delete(controller);
        if (this.#controllerByScope.get(scope) === controller) {
          this.#controllerByScope.delete(scope);
        }
      }
    };
  }

  invalidate(): void {
    this.#epoch += 1;
    this.#sequences.clear();
    for (const controller of this.#controllers) controller.abort();
    this.#controllers.clear();
    this.#controllerByScope.clear();
  }

  get epoch(): number {
    return this.#epoch;
  }
}

export function mergeGitChanges(current: GitChange[], incoming: GitChange[]): GitChange[] {
  const seen = new Set<string>();
  const result: GitChange[] = [];
  for (const item of [...current, ...incoming]) {
    // A path can legitimately be present in both index and worktree. Area is
    // therefore part of identity; path_id alone must never collapse dual-area rows.
    const key = `${item.area}\u0000${item.id}\u0000${item.path.token}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(item);
  }
  return result;
}

export function allRepositoryChanges(state?: GitRepositoryLoadState): GitChange[] {
  if (!state) return [];
  let items = [...state.items];
  for (const prefix of Object.values(state.prefixes)) {
    items = mergeGitChanges(items, prefix.items);
  }
  return items;
}

export function warningText(warning: GitRepositoryWarning): string {
  return localizedGitWarning(warning);
}

export function isWatchFallbackWarning(warning: GitRepositoryWarning): boolean {
  return typeof warning === "object" && warning?.code === "watch_limit_exceeded";
}

export function safeRepositoryRootDisplay(value: string): string {
  const trimmed = value.trim().replace(/[\\/]+$/, "");
  if (!trimmed) return "";
  const absolute = trimmed.startsWith("/") || trimmed.startsWith("\\\\") || /^[A-Za-z]:[\\/]/.test(trimmed);
  if (!absolute) return trimmed;
  return trimmed.split(/[\\/]/).filter(Boolean).at(-1) ?? "";
}

export function safeGitPathDisplay(value: string): string {
  const trimmed = value.trim().replaceAll("\\", "/").replace(/^\.\//, "");
  if (!trimmed) return "";
  if (trimmed.startsWith("/") || /^[A-Za-z]:\//.test(trimmed)) {
    return trimmed.split("/").filter(Boolean).at(-1) ?? "";
  }
  return trimmed;
}

export function repositoryAreaCount(repository: GitRepositorySummary, area: GitChangeArea): number {
  if (area === "conflict") {
    return repository.counts.conflict ?? repository.counts.conflicts ?? 0;
  }
  return repository.counts[area] ?? 0;
}

export function repositoryDirtyCount(repository: GitRepositorySummary): number {
  const explicit = repository.counts.dirty ?? repository.counts.total;
  if (typeof explicit === "number") return Math.max(0, explicit);
  return GIT_CHANGE_AREAS.reduce((total, area) => total + repositoryAreaCount(repository, area), 0);
}

export function isRepositoryDirty(repository: GitRepositorySummary): boolean {
  const state = typeof repository.state === "string" ? repository.state : repository.state.kind ?? "";
  return repositoryDirtyCount(repository) > 0 || ["dirty", "conflicted", "partial"].includes(state);
}

export function repositoryBranch(repository: GitRepositorySummary): {
  name: string;
  detached: boolean;
  unborn: boolean;
} {
  if (typeof repository.branch === "string") {
    return { name: repository.branch, detached: false, unborn: false };
  }
  if (!repository.branch) return { name: "", detached: false, unborn: false };
  return {
    name: repository.branch.display || repository.branch.name || "",
    detached: Boolean(repository.branch.detached),
    unborn: Boolean(repository.branch.unborn)
  };
}

export function repositoryOperation(repository: GitRepositorySummary): string {
  if (typeof repository.operation === "string") return repository.operation;
  return repository.operation?.kind || repository.operation?.state || "";
}

export function isPartialRepository(repository: GitRepositorySummary): boolean {
  return repository.scope === "resource_prefix";
}

function areaSortIndex(area: GitChangeArea): number {
  const index = GIT_CHANGE_AREAS.indexOf(area as (typeof GIT_CHANGE_AREAS)[number]);
  return index < 0 ? GIT_CHANGE_AREAS.length : index;
}

/** Group by the fixed UI area order while preserving the API's stable order inside each area. */
export function stableAreaOrder(items: GitChange[]): GitChange[] {
  return items
    .map((item, index) => ({ item, index }))
    .sort((left, right) => areaSortIndex(left.item.area) - areaSortIndex(right.item.area) || left.index - right.index)
    .map(({ item }) => item);
}

function directoryKey(repositoryId: string, area: GitChangeArea, display: string, token?: string): string {
  return `directory:${repositoryId}:${area}:${token || display}`;
}

function areaKey(repositoryId: string, area: GitChangeArea): string {
  return `area:${repositoryId}:${area}`;
}

function matchesSearch(change: GitChange, search: string): boolean {
  if (!search) return true;
  const haystack = `${change.path.display}\n${change.old_path?.display ?? ""}`.toLocaleLowerCase();
  return haystack.includes(search);
}

function lazyPrefixSearchCanContinue(prefix?: GitPrefixLoadState): boolean {
  return !prefix || !prefix.loaded || Boolean(
    prefix.loading || prefix.error || prefix.nextCursor || prefix.truncated
  );
}

function createDirectoryTree(repositoryId: string, area: GitChangeArea, items: GitChange[]): DirectoryNode {
  const root: DirectoryNode = {
    key: `directory-root:${repositoryId}:${area}`,
    name: "",
    display: "",
    children: [],
    childDirectories: new Map()
  };

  for (const change of items) {
    const display = safeGitPathDisplay(change.path.display);
    const segments = display.split("/").filter(Boolean);
    if (!segments.length) continue;
    const isDirectory = change.kind === "directory" || change.kind === "tree";
    const directorySegments = isDirectory ? segments : segments.slice(0, -1);
    let parent = root;
    const path: string[] = [];
    for (const segment of directorySegments) {
      path.push(segment);
      const childDisplay = path.join("/");
      let child = parent.childDirectories.get(segment);
      if (!child) {
        child = {
          key: directoryKey(repositoryId, area, childDisplay),
          name: segment,
          display: childDisplay,
          children: [],
          childDirectories: new Map()
        };
        parent.childDirectories.set(segment, child);
        parent.children.push({ type: "directory", node: child });
      }
      parent = child;
    }
    if (isDirectory) {
      parent.directoryChange = change;
      parent.token = change.path.token;
      parent.key = directoryKey(repositoryId, area, display, change.path.token);
    } else {
      parent.children.push({ type: "change", change });
    }
  }
  return root;
}

function appendDirectoryRows(
  rows: GitTreeRow[],
  node: DirectoryNode,
  options: BuildGitTreeRowsOptions,
  repository: GitRepositorySummary,
  area: GitChangeArea,
  level: number,
  parentKey: string,
  searching: boolean
) {
  const state = options.states[repository.id];
  for (const entry of node.children) {
    if (entry.type === "change") {
      rows.push({
        key: gitChangeIdentity(repository.id, entry.change),
        type: "change",
        repositoryId: repository.id,
        level,
        parentKey,
        area,
        change: entry.change
      });
      continue;
    }

    const directory = entry.node;
    const expanded = searching || Boolean(options.expandedDirectories[directory.key]);
    const prefixState = directory.token ? state?.prefixes[directory.token] : undefined;
    const lazyUntracked = area === "untracked" && Boolean(directory.directoryChange && directory.token);
    rows.push({
      key: directory.key,
      type: "directory",
      repositoryId: repository.id,
      level,
      parentKey,
      label: directory.name,
      area,
      change: directory.directoryChange,
      expanded,
      expandable: true,
      directoryToken: directory.token,
      directoryDisplay: directory.display,
      lazyUntracked
    });
    if (!expanded) continue;

    if (lazyUntracked && prefixState?.loading && directory.children.length === 0) {
      rows.push({
        key: `loading-prefix:${repository.id}:${directory.token}`,
        type: "loading",
        repositoryId: repository.id,
        level: level + 1,
        parentKey: directory.key,
        prefixToken: directory.token
      });
    }
    if (lazyUntracked && prefixState?.error) {
      rows.push({
        key: `error-prefix:${repository.id}:${directory.token}`,
        type: "error",
        repositoryId: repository.id,
        level: level + 1,
        parentKey: directory.key,
        prefixToken: directory.token,
        error: prefixState.error
      });
    }
    if (lazyUntracked) {
      for (const warning of prefixState?.warnings ?? []) {
        const message = warningText(warning);
        if (!message) continue;
        rows.push({
          key: `warning-prefix:${repository.id}:${directory.token}:${rows.length}`,
          type: "warning",
          repositoryId: repository.id,
          level: level + 1,
          parentKey: directory.key,
          prefixToken: directory.token,
          warning: message
        });
      }
    }
    if (searching && lazyUntracked && !prefixState?.loading && !prefixState?.error &&
      (!prefixState || (!prefixState.loaded && !prefixState.nextCursor))) {
      rows.push({
        key: `load-prefix:${repository.id}:${directory.token}`,
        type: "load-prefix",
        repositoryId: repository.id,
        level: level + 1,
        parentKey: directory.key,
        prefixToken: directory.token
      });
    }
    appendDirectoryRows(
      rows, directory, options, repository, area, level + 1, directory.key, searching
    );
    if (lazyUntracked && prefixState?.nextCursor) {
      rows.push({
        key: `load-more-prefix:${repository.id}:${directory.token}:${prefixState.nextCursor}`,
        type: "load-more",
        repositoryId: repository.id,
        level: level + 1,
        parentKey: directory.key,
        prefixToken: directory.token,
        cursor: prefixState.nextCursor
      });
    }
  }
}

export function buildGitTreeRows(options: BuildGitTreeRowsOptions): GitTreeRow[] {
  const rows: GitTreeRow[] = [];
  const search = options.search.trim().toLocaleLowerCase();
  const searching = Boolean(search);

  for (const repository of options.repositories) {
    if (options.onlyDirty && !options.repositoryListTruncated && !isRepositoryDirty(repository)) continue;
    const expanded = searching || Boolean(options.expandedRepositories[repository.id]);
    const repositoryRowKey = `repository:${repository.id}`;
    rows.push({
      key: repositoryRowKey,
      type: "repository",
      repositoryId: repository.id,
      level: 1,
      repository,
      expanded,
      expandable: true,
      countsAuthoritative: !options.repositoryListTruncated &&
        !stateHasTruncatedResults(options.states[repository.id])
    });
    if (!expanded) continue;

    for (const warning of repository.warnings ?? []) {
      if (isWatchFallbackWarning(warning)) continue;
      const message = warningText(warning);
      if (!message) continue;
      rows.push({
        key: `warning:${repository.id}:${rows.length}`,
        type: "warning",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey,
        warning: message
      });
    }

    const state = options.states[repository.id];
    if (!state || (state.loading && !state.loaded)) {
      rows.push({
        key: `loading:${repository.id}`,
        type: "loading",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey
      });
      continue;
    }
    if (state.error && !state.loaded) {
      rows.push({
        key: `error:${repository.id}`,
        type: "error",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey,
        error: state.error
      });
      continue;
    }

    const allItems = stableAreaOrder(allRepositoryChanges(state));
    const filteredItems = searching ? allItems.filter((item) => matchesSearch(item, search)) : allItems;
    const areas = [...GIT_CHANGE_AREAS] as GitChangeArea[];
    for (const item of allItems) {
      if (!areas.includes(item.area)) areas.push(item.area);
    }
    for (const area of Object.keys(state.areas) as GitChangeArea[]) {
      if (!areas.includes(area)) areas.push(area);
    }

    let visibleChanges = 0;
    for (const area of areas) {
      const allAreaItems = allItems.filter((item) => item.area === area);
      const items = filteredItems.filter((item) => item.area === area);
      const areaState = state.areas[area];
      // A lazy untracked directory is cursor authority. Keep it while searching
      // even when this loaded page has no matching child, otherwise the user can
      // never reach a match on a later prefix page.
      const structuralDirectories = searching ? allAreaItems.filter((item) => {
        if (area !== "untracked") return false;
        if (item.kind !== "directory" && item.kind !== "tree") return false;
        const prefix = state.prefixes[item.path.token];
        return lazyPrefixSearchCanContinue(prefix);
      }) : [];
      const displayItems = searching ? mergeGitChanges(items, structuralDirectories) : items;
      const areaHasPendingAuthority = Boolean(
        areaState?.loading || areaState?.error || areaState?.nextCursor || areaState?.truncated ||
        structuralDirectories.length
      );
      const countsAuthoritative = !options.repositoryListTruncated && !stateHasTruncatedResults(state);
      const count = searching || !countsAuthoritative
        ? items.length
        : Math.max(items.length, repositoryAreaCount(repository, area));
      if (!count && !areaHasPendingAuthority) continue;
      const key = areaKey(repository.id, area);
      const areaExpanded = searching || !options.collapsedAreas[key];
      rows.push({
        key,
        type: "area",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey,
        area,
        count,
        expanded: areaExpanded,
        expandable: true
      });
      if (!areaExpanded) continue;
      if (areaState?.loading && !areaState.loaded) {
        rows.push({
          key: `loading-area:${repository.id}:${area}`,
          type: "loading",
          repositoryId: repository.id,
          level: 3,
          parentKey: key,
          area
        });
      }
      if (areaState?.error) {
        rows.push({
          key: `error-area:${repository.id}:${area}`,
          type: "error",
          repositoryId: repository.id,
          level: 3,
          parentKey: key,
          area,
          error: areaState.error
        });
      }
      for (const warning of areaState?.warnings ?? []) {
        const message = warningText(warning);
        if (!message) continue;
        rows.push({
          key: `warning-area:${repository.id}:${area}:${rows.length}`,
          type: "warning",
          repositoryId: repository.id,
          level: 3,
          parentKey: key,
          area,
          warning: message
        });
      }
      visibleChanges += items.filter((item) => item.kind !== "directory" && item.kind !== "tree").length;

      if (options.view === "flat") {
        for (const change of displayItems) {
          const changeIsDirectory = change.kind === "directory" || change.kind === "tree";
          const flatDirectoryKey = directoryKey(repository.id, area, change.path.display, change.path.token);
          const lazyUntracked = area === "untracked" && changeIsDirectory;
          const flatExpanded = changeIsDirectory && (searching || Boolean(options.expandedDirectories[flatDirectoryKey]));
          rows.push({
            key: changeIsDirectory ? flatDirectoryKey : gitChangeIdentity(repository.id, change),
            type: changeIsDirectory ? "directory" : "change",
            repositoryId: repository.id,
            level: 3,
            parentKey: key,
            label: safeGitPathDisplay(change.path.display),
            area,
            change,
            expanded: flatExpanded,
            expandable: changeIsDirectory,
            directoryToken: changeIsDirectory ? change.path.token : undefined,
            directoryDisplay: change.path.display,
            lazyUntracked
          });
          if (!lazyUntracked || !flatExpanded) continue;
          const prefixState = state.prefixes[change.path.token];
          if (prefixState?.loading) {
            rows.push({
              key: `loading-prefix:${repository.id}:${change.path.token}`,
              type: "loading", repositoryId: repository.id, level: 4,
              parentKey: flatDirectoryKey, prefixToken: change.path.token
            });
          }
          if (prefixState?.error) {
            rows.push({
              key: `error-prefix:${repository.id}:${change.path.token}`,
              type: "error", repositoryId: repository.id, level: 4,
              parentKey: flatDirectoryKey, prefixToken: change.path.token,
              error: prefixState.error
            });
          }
          for (const warning of prefixState?.warnings ?? []) {
            const message = warningText(warning);
            if (!message) continue;
            rows.push({
              key: `warning-prefix:${repository.id}:${change.path.token}:${rows.length}`,
              type: "warning", repositoryId: repository.id, level: 4,
              parentKey: flatDirectoryKey, prefixToken: change.path.token,
              warning: message
            });
          }
          if (searching && !prefixState?.loading && !prefixState?.error &&
            (!prefixState || (!prefixState.loaded && !prefixState.nextCursor))) {
            rows.push({
              key: `load-prefix:${repository.id}:${change.path.token}`,
              type: "load-prefix", repositoryId: repository.id, level: 4,
              parentKey: flatDirectoryKey, prefixToken: change.path.token
            });
          }
          if (prefixState?.nextCursor) {
            rows.push({
              key: `load-more-prefix:${repository.id}:${change.path.token}:${prefixState.nextCursor}`,
              type: "load-more", repositoryId: repository.id, level: 4,
              parentKey: flatDirectoryKey, prefixToken: change.path.token,
              cursor: prefixState.nextCursor
            });
          }
        }
      } else {
        const root = createDirectoryTree(repository.id, area, displayItems);
        appendDirectoryRows(rows, root, options, repository, area, 3, key, searching);
      }
      if (areaState?.nextCursor) {
        rows.push({
          key: `load-more-area:${repository.id}:${area}:${areaState.nextCursor}`,
          type: "load-more",
          repositoryId: repository.id,
          level: 3,
          parentKey: key,
          area,
          cursor: areaState.nextCursor
        });
      }
    }

    const searchCanContinue = Object.values(state.areas).some((areaState) =>
      Boolean(areaState.loading || areaState.error || areaState.nextCursor || areaState.truncated)
    ) || Object.values(state.prefixes).some((prefixState) =>
      Boolean(prefixState.loading || prefixState.error || prefixState.nextCursor || prefixState.truncated)
    ) || allItems.some((item) =>
      item.area === "untracked" && (item.kind === "directory" || item.kind === "tree") &&
      lazyPrefixSearchCanContinue(state.prefixes[item.path.token])
    );
    const areaRequestPendingOrFailed = Object.values(state.areas).some((areaState) =>
      Boolean(areaState.loading || areaState.error)
    );
    if ((searching && visibleChanges === 0) ||
      (!searching && !filteredItems.length && !areaRequestPendingOrFailed)) {
      rows.push({
        key: `empty:${repository.id}:${search}`,
        type: "empty",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey,
        partialSearch: searching && searchCanContinue
      });
    }
    if (state.truncated && !state.nextCursor) {
      rows.push({
        key: `warning-truncated:${repository.id}`,
        type: "warning",
        repositoryId: repository.id,
        level: 2,
        parentKey: repositoryRowKey
      });
    }
  }
  return rows;
}

function stateHasTruncatedResults(state?: GitRepositoryLoadState): boolean {
  if (!state) return false;
  return state.truncated || Object.values(state.areas).some((area) => area.truncated) ||
    Object.values(state.prefixes).some((prefix) => prefix.truncated);
}

export function stackedRenderLimit(total: number): number {
  if (total <= 6) return Math.max(0, total);
  if (total <= 12) return 6;
  if (total <= 25) return 4;
  return 2;
}

export function selectStackedChanges<T>(items: T[], selectedIndex: number): T[] {
  const limit = stackedRenderLimit(items.length);
  if (limit >= items.length) return items;
  const safeIndex = Math.max(0, Math.min(items.length - 1, selectedIndex));
  const start = Math.max(0, Math.min(items.length - limit, safeIndex - Math.floor((limit - 1) / 2)));
  return items.slice(start, start + limit);
}

export interface VirtualWindow {
  start: number;
  end: number;
  offset: number;
  totalSize: number;
}

export function computeVirtualWindow(
  total: number,
  scrollTop: number,
  viewportHeight: number,
  rowHeight = 34,
  overscan = 12
): VirtualWindow {
  if (total <= 0) return { start: 0, end: 0, offset: 0, totalSize: 0 };
  const safeHeight = Math.max(rowHeight, viewportHeight);
  const start = Math.max(0, Math.floor(Math.max(0, scrollTop) / rowHeight) - overscan);
  const end = Math.min(total, Math.ceil((Math.max(0, scrollTop) + safeHeight) / rowHeight) + overscan);
  return { start, end, offset: start * rowHeight, totalSize: total * rowHeight };
}

interface PatchLineState {
  oldNumber: number;
  newNumber: number;
  oldRemaining: number;
  newRemaining: number;
  inHunk: boolean;
}

interface ClassifiedPatchLine {
  kind: "addition" | "deletion" | "context" | "hunk" | "metadata";
  oldNumber?: number;
  newNumber?: number;
}

function parsePatchHunkHeader(patch: string, start: number, end: number): {
  oldNumber: number;
  newNumber: number;
  oldCount: number;
  newCount: number;
} | undefined {
  if (patch.charCodeAt(start) !== 64 || patch.charCodeAt(start + 1) !== 64) return undefined;
  const header = patch.slice(start, Math.min(end, start + 256));
  const hunk = header.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
  if (!hunk) return undefined;
  return {
    oldNumber: Number(hunk[1]),
    oldCount: hunk[2] === undefined ? 1 : Number(hunk[2]),
    newNumber: Number(hunk[3]),
    newCount: hunk[4] === undefined ? 1 : Number(hunk[4])
  };
}

function finishPatchHunkLine(state: PatchLineState): void {
  if (state.oldRemaining <= 0 && state.newRemaining <= 0) state.inHunk = false;
}

function classifyPatchLine(
  patch: string,
  start: number,
  end: number,
  state: PatchLineState
): ClassifiedPatchLine {
  const hunk = parsePatchHunkHeader(patch, start, end);
  if (hunk) {
    state.oldNumber = hunk.oldNumber;
    state.newNumber = hunk.newNumber;
    state.oldRemaining = Math.max(0, hunk.oldCount);
    state.newRemaining = Math.max(0, hunk.newCount);
    state.inHunk = true;
    finishPatchHunkLine(state);
    return { kind: "hunk" };
  }
  if (!state.inHunk) return { kind: "metadata" };

  const first = patch.charCodeAt(start);
  if (first === 43) {
    const newNumber = state.newNumber++;
    state.newRemaining = Math.max(0, state.newRemaining - 1);
    finishPatchHunkLine(state);
    return { kind: "addition", newNumber };
  }
  if (first === 45) {
    const oldNumber = state.oldNumber++;
    state.oldRemaining = Math.max(0, state.oldRemaining - 1);
    finishPatchHunkLine(state);
    return { kind: "deletion", oldNumber };
  }
  if (first === 32) {
    const oldNumber = state.oldNumber++;
    const newNumber = state.newNumber++;
    state.oldRemaining = Math.max(0, state.oldRemaining - 1);
    state.newRemaining = Math.max(0, state.newRemaining - 1);
    finishPatchHunkLine(state);
    return { kind: "context", oldNumber, newNumber };
  }
  // "\\ No newline at end of file" and malformed metadata consume no hunk
  // line numbers. A following valid @@ header can still recover the state.
  return { kind: "metadata" };
}

export function patchChangedLineCount(patch?: string | null): number {
  if (!patch) return 0;
  const state: PatchLineState = {
    oldNumber: 0, newNumber: 0, oldRemaining: 0, newRemaining: 0, inHunk: false
  };
  let count = 0;
  let start = 0;
  while (start <= patch.length) {
    const newline = patch.indexOf("\n", start);
    const end = newline < 0 ? patch.length : newline;
    const classified = classifyPatchLine(patch, start, end, state);
    if (classified.kind === "addition" || classified.kind === "deletion") count += 1;
    if (newline < 0) break;
    start = newline + 1;
  }
  return count;
}

export function diffChangedLineCount(diff: Pick<GitDiffResponse, "stats" | "patch">): number {
  const statsCount = Math.max(
    diff.stats.lines ?? 0,
    (diff.stats.additions ?? 0) + (diff.stats.deletions ?? 0)
  );
  return Math.max(statsCount, patchChangedLineCount(diff.patch));
}

export function requiresLargeDiffConfirmation(
  diff: Pick<GitDiffResponse, "confirm_required" | "stats" | "patch">,
  confirmed: boolean,
  threshold = 500
): boolean {
  return !confirmed && (diff.confirm_required || diffChangedLineCount(diff) > threshold);
}

export interface DiffFallbackReason {
  fallback: boolean;
  reason: "bytes" | "line" | null;
  bytes: number;
  longestLine: number;
}

export function diffFallbackReason(
  patch: string,
  maxBytes = 500 * 1024,
  maxLineLength = 10_000
): DiffFallbackReason {
  const bytes = new TextEncoder().encode(patch).byteLength;
  let longestLine = 0;
  let lineStart = 0;
  while (lineStart <= patch.length) {
    const newline = patch.indexOf("\n", lineStart);
    const lineEnd = newline < 0 ? patch.length : newline;
    longestLine = Math.max(longestLine, lineEnd - lineStart);
    if (newline < 0) break;
    lineStart = newline + 1;
  }
  const reason = bytes > maxBytes ? "bytes" : longestLine > maxLineLength ? "line" : null;
  return { fallback: reason !== null, reason, bytes, longestLine };
}

export function createConcurrencyLimiter(limit: number) {
  const max = Math.max(1, Math.floor(limit));
  let active = 0;
  const queue: Array<() => void> = [];

  const release = () => {
    active -= 1;
    queue.shift()?.();
  };

  return function run<T>(task: () => Promise<T>): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const start = () => {
        active += 1;
        void Promise.resolve().then(task).then(resolve, reject).finally(release);
      };
      if (active < max) start();
      else queue.push(start);
    });
  };
}

export interface StructuredPatchLine {
  text: string;
  kind: "addition" | "deletion" | "context" | "hunk" | "metadata";
  oldNumber?: number;
  newNumber?: number;
}

/**
 * A compact random-access index for the lightweight renderer. Keeping one
 * Uint32 offset per source line costs about 2.7 MiB for a 700k-line patch,
 * instead of retaining hundreds of thousands of strings and row objects.
 * Line-number checkpoints bound work after an arbitrary virtual scroll jump.
 */
export interface StructuredPatchIndex {
  lineStarts: Uint32Array;
  checkpointInterval: number;
  checkpointOldNumbers: Uint32Array;
  checkpointNewNumbers: Uint32Array;
  checkpointOldRemaining: Uint32Array;
  checkpointNewRemaining: Uint32Array;
  checkpointInHunk: Uint8Array;
}

function patchLineEnd(patch: string, index: StructuredPatchIndex, line: number): number {
  const start = index.lineStarts[line];
  const next = index.lineStarts[line + 1];
  return next > start && patch.charCodeAt(next - 1) === 10 ? next - 1 : next;
}

function structuredPatchLineAt(
  patch: string,
  index: StructuredPatchIndex,
  line: number,
  state: PatchLineState,
  materialize: boolean
): StructuredPatchLine | undefined {
  const start = index.lineStarts[line];
  const end = patchLineEnd(patch, index, line);
  const classified = classifyPatchLine(patch, start, end, state);
  return materialize ? { text: patch.slice(start, end), ...classified } : undefined;
}

export function createStructuredPatchIndex(
  patch: string,
  checkpointInterval = 1_024
): StructuredPatchIndex {
  const interval = Math.max(1, Math.floor(checkpointInterval));
  let lineCount = 1;
  for (let offset = patch.indexOf("\n"); offset >= 0; offset = patch.indexOf("\n", offset + 1)) {
    lineCount += 1;
  }

  const lineStarts = new Uint32Array(lineCount + 1);
  let line = 1;
  for (let offset = patch.indexOf("\n"); offset >= 0; offset = patch.indexOf("\n", offset + 1)) {
    lineStarts[line++] = offset + 1;
  }
  lineStarts[lineCount] = patch.length;

  const checkpointCount = Math.ceil(lineCount / interval);
  const checkpointOldNumbers = new Uint32Array(checkpointCount);
  const checkpointNewNumbers = new Uint32Array(checkpointCount);
  const checkpointOldRemaining = new Uint32Array(checkpointCount);
  const checkpointNewRemaining = new Uint32Array(checkpointCount);
  const checkpointInHunk = new Uint8Array(checkpointCount);
  const index: StructuredPatchIndex = {
    lineStarts, checkpointInterval: interval,
    checkpointOldNumbers, checkpointNewNumbers,
    checkpointOldRemaining, checkpointNewRemaining, checkpointInHunk
  };
  const state: PatchLineState = {
    oldNumber: 0, newNumber: 0, oldRemaining: 0, newRemaining: 0, inHunk: false
  };
  for (let lineIndex = 0; lineIndex < lineCount; lineIndex += 1) {
    if (lineIndex % interval === 0) {
      const checkpoint = Math.floor(lineIndex / interval);
      checkpointOldNumbers[checkpoint] = state.oldNumber;
      checkpointNewNumbers[checkpoint] = state.newNumber;
      checkpointOldRemaining[checkpoint] = state.oldRemaining;
      checkpointNewRemaining[checkpoint] = state.newRemaining;
      checkpointInHunk[checkpoint] = state.inHunk ? 1 : 0;
    }
    structuredPatchLineAt(patch, index, lineIndex, state, false);
  }
  return index;
}

export function structuredPatchLineCount(index: StructuredPatchIndex): number {
  return Math.max(0, index.lineStarts.length - 1);
}

/** Parse only one virtual window, plus at most one checkpoint interval. */
export function structurePatchWindow(
  patch: string,
  index: StructuredPatchIndex,
  start: number,
  end: number
): StructuredPatchLine[] {
  const total = structuredPatchLineCount(index);
  const safeStart = Math.max(0, Math.min(total, Math.floor(start)));
  const safeEnd = Math.max(safeStart, Math.min(total, Math.ceil(end)));
  if (safeStart === safeEnd) return [];

  const checkpoint = Math.floor(safeStart / index.checkpointInterval);
  const checkpointLine = checkpoint * index.checkpointInterval;
  const state: PatchLineState = {
    oldNumber: index.checkpointOldNumbers[checkpoint] ?? 0,
    newNumber: index.checkpointNewNumbers[checkpoint] ?? 0,
    oldRemaining: index.checkpointOldRemaining[checkpoint] ?? 0,
    newRemaining: index.checkpointNewRemaining[checkpoint] ?? 0,
    inHunk: index.checkpointInHunk[checkpoint] === 1
  };
  for (let line = checkpointLine; line < safeStart; line += 1) {
    structuredPatchLineAt(patch, index, line, state, false);
  }
  const result: StructuredPatchLine[] = [];
  for (let line = safeStart; line < safeEnd; line += 1) {
    const structured = structuredPatchLineAt(patch, index, line, state, true);
    if (structured) result.push(structured);
  }
  return result;
}

export function structurePatchLines(patch: string): StructuredPatchLine[] {
  const index = createStructuredPatchIndex(patch);
  return structurePatchWindow(patch, index, 0, structuredPatchLineCount(index));
}

function imageValue(value: GitDiffImage | string | null | undefined): GitDiffImage | undefined {
  if (!value) return undefined;
  return typeof value === "string" ? { url: value } : value;
}

export function diffImageSides(images?: GitDiffResponse["images"]): {
  before?: GitDiffImage;
  after?: GitDiffImage;
} {
  if (!images) return {};
  if (Array.isArray(images)) return { before: images[0], after: images[1] };
  return {
    before: imageValue(images.before ?? images.old ?? images.previous),
    after: imageValue(images.after ?? images.new ?? images.current)
  };
}

export function safeDiffImageSource(image?: GitDiffImage): string | undefined {
  const source = image?.url || image?.src || image?.data_url;
  if (!source) return undefined;
  if (/^data:image\/(?:png|jpeg|webp);base64,/i.test(source) || source.startsWith("blob:")) return source;
  try {
    const parsed = new URL(source, window.location.origin);
    const apiURL = new URL(apiBaseURL, window.location.origin);
    if (parsed.origin !== apiURL.origin) return undefined;
    const apiPath = apiURL.pathname.replace(/\/$/, "");
    if (!parsed.pathname.startsWith(`${apiPath}/`)) return undefined;
    return parsed.href;
  } catch {
    return undefined;
  }
}
