<script lang="ts">
  import { Button, Input } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { onDestroy } from "svelte";
  import {
    AlertTriangle, Files, FolderTree, GitCompareArrows, List, LoaderCircle,
    RefreshCw, Search, SlidersHorizontal, Wifi, WifiOff
  } from "lucide-svelte";
  import RequireProject from "../components/RequireProject.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import GitChangesTree from "../components/GitChangesTree.svelte";
  import GitDiffViewer from "../components/GitDiffViewer.svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { navigate } from "../lib/router.svelte";
  import { subscribeGitRepositoryEvents, type GitEventConnectionState } from "../lib/gitEvents";
  import {
    GIT_CHANGE_AREAS, allRepositoryChanges, buildGitTreeRows, createConcurrencyLimiter,
    emptyGitRepositoryLoadState, fetchGitChanges, fetchGitRepositories,
    GitRequestGate, gitChangeIdentity, gitChangeSelectionIntent, gitRepositoriesQueryKey,
     isGitReadWatchEventSuppressed, isGitResourceAuthorizationError, isRecoverableGitSnapshotError, isRepositoryDirty,
    isWatchFallbackWarning,
    mergeGitChanges, repositoryAreaCount, restoreGitChangeSelection, selectStackedChanges, warningText,
    type GitAreaLoadState, type GitChange, type GitChangeArea, type GitChangesView, type GitChangeSelectionIntent,
    type GitDiffTarget, type GitPrefixLoadState,
    type GitRepositoriesSnapshot, type GitRepositoryLoadState, type GitRepositorySummary,
    type GitTreeRow
  } from "../lib/gitChanges";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const projectId = $derived(context.project?.id ?? "");
  const loadLimited = createConcurrencyLimiter(2);

  let activeProjectId = $state("");
  let observedSnapshotId = $state("");
  let repositoryStates = $state<Record<string, GitRepositoryLoadState | undefined>>({});
  let expandedRepositories = $state<Record<string, boolean | undefined>>({});
  let initializedRepositories = $state<Record<string, boolean | undefined>>({});
  let repositoriesAwaitingRecovery = $state<Record<string, boolean | undefined>>({});
  let collapsedAreas = $state<Record<string, boolean | undefined>>({});
  let expandedDirectories = $state<Record<string, boolean | undefined>>({});
  let search = $state("");
  let view = $state<GitChangesView>("tree");
  let onlyDirty = $state(false);
  let selectedKey = $state("");
  let selectedRepositoryId = $state("");
  let selectionIntent = $state<GitChangeSelectionIntent>();
  let diffMode = $state<"single" | "stacked">("single");
  let mobilePane = $state<"tree" | "diff">("tree");
  let treeWidth = $state(320);
  let streamState = $state<GitEventConnectionState | { kind: "paused" | "authorization" }>({ kind: "connecting" });
  const requestGates = new Map<string, GitRequestGate>();
  const recoveryRequests = new Map<string, Promise<void>>();
  const recoveryControllers = new Map<string, AbortController>();
  const invalidationTimers = new Map<string, ReturnType<typeof setTimeout>>();
  const observedEventGenerations = new Map<string, number>();
  const repositoryPendingRequests = new Map<string, number>();
  let repositoryReconcilePending = $state(false);
  let repositoryReconcileError = $state<unknown>();
  let resourceAuthorizationError = $state<unknown>();
  let authorizationRevision = 0;
  const resourceAuthorizationBlocked = $derived(Boolean(resourceAuthorizationError));
  const resourcesURL = $derived(`/resources?project=${encodeURIComponent(projectId)}`);
  let suppressRefreshEventsUntil = 0;
  let activeOwnRefreshes = 0;
  let resizeCleanup: (() => void) | undefined;

  async function loadRepositorySnapshotForPage(
    requestedProjectId: string,
    refresh: boolean,
    signal?: AbortSignal
  ): Promise<GitRepositoriesSnapshot> {
    const requestedAuthorizationRevision = authorizationRevision;
    const pending = (repositoryPendingRequests.get(requestedProjectId) ?? 0) + 1;
    repositoryPendingRequests.set(requestedProjectId, pending);
    if (projectId === requestedProjectId) repositoryReconcilePending = true;
    if (refresh) beginOwnRefresh();
    try {
      const snapshot = await fetchGitRepositories(requestedProjectId, { refresh, signal });
      if (signal?.aborted || projectId !== requestedProjectId || requestedAuthorizationRevision !== authorizationRevision) {
        throw new DOMException("Repository snapshot superseded", "AbortError");
      }
      repositoryReconcileError = undefined;
      resourceAuthorizationError = undefined;
      return snapshot;
    } catch (error) {
      if (!isAbortError(error) && projectId === requestedProjectId) {
        repositoryReconcileError = error;
        blockResourceAuthorization(error, requestedProjectId);
      }
      throw error;
    } finally {
      if (refresh) finishOwnRefresh();
      const remaining = Math.max(0, (repositoryPendingRequests.get(requestedProjectId) ?? 1) - 1);
      if (remaining) repositoryPendingRequests.set(requestedProjectId, remaining);
      else repositoryPendingRequests.delete(requestedProjectId);
      if (projectId === requestedProjectId) repositoryReconcilePending = remaining > 0;
    }
  }

  const repositories = createQuery<GitRepositoriesSnapshot>(() => {
    const requestedProjectId = projectId;
    return {
      queryKey: gitRepositoriesQueryKey(requestedProjectId),
      queryFn: ({ signal }) => loadRepositorySnapshotForPage(requestedProjectId, false, signal),
      enabled: Boolean(requestedProjectId),
      staleTime: 0,
      retry: (failureCount, error) => !resourceAuthorizationBlocked && !isAbortError(error) &&
        !isGitResourceAuthorizationError(error) && failureCount < 1,
      refetchOnReconnect: () => !resourceAuthorizationBlocked,
      refetchOnWindowFocus: false
    };
  });

  const repositoryItems = $derived(repositories.data?.repositories ?? []);
  const repositorySubscriptionKey = $derived(repositoryItems.map((item) => `${item.id}:${item.resource_ids.join(",")}`).sort().join("|"));
  const treeRows = $derived(buildGitTreeRows({
    repositories: repositoryItems,
    states: repositoryStates,
    expandedRepositories,
    collapsedAreas,
    expandedDirectories,
    view,
    search,
    onlyDirty,
    repositoryListTruncated: Boolean(repositories.data?.truncated)
  }));

  function requestGate(repositoryId: string): GitRequestGate {
    let gate = requestGates.get(repositoryId);
    if (!gate) {
      gate = new GitRequestGate();
      requestGates.set(repositoryId, gate);
    }
    return gate;
  }

  function repositoryById(id: string): GitRepositorySummary | undefined {
    return repositoryItems.find((repository) => repository.id === id);
  }

  function updateRepositoryState(
    repositoryId: string,
    update: (state: GitRepositoryLoadState) => GitRepositoryLoadState
  ) {
    const current = repositoryStates[repositoryId] ?? emptyGitRepositoryLoadState();
    repositoryStates = { ...repositoryStates, [repositoryId]: update(current) };
  }

  function updatePrefixState(
    repositoryId: string,
    prefixToken: string,
    update: (state: GitPrefixLoadState) => GitPrefixLoadState
  ) {
    updateRepositoryState(repositoryId, (current) => {
      const prefix = current.prefixes[prefixToken] ?? {
        items: [], truncated: false, loaded: false, loading: false, loadingMore: false
      };
      return {
        ...current,
        prefixes: { ...current.prefixes, [prefixToken]: update(prefix) }
      };
    });
  }

  function updateAreaState(
    repositoryId: string,
    area: GitChangeArea,
    update: (state: GitAreaLoadState) => GitAreaLoadState
  ) {
    updateRepositoryState(repositoryId, (current) => {
      const areaState = current.areas[area] ?? {
        items: [], truncated: false, loaded: false, loading: false, loadingMore: false
      };
      return {
        ...current,
        areas: { ...current.areas, [area]: update(areaState) }
      };
    });
  }

  function isAbortError(error: unknown): boolean {
    return error instanceof DOMException && error.name === "AbortError";
  }

  async function loadRepositoryArea(
    repository: GitRepositorySummary,
    area: GitChangeArea,
    cursor?: string,
    allowRecovery = true
  ) {
    const snapshotId = repositories.data?.snapshot_id;
    if (!projectId || !snapshotId || resourceAuthorizationBlocked) return;
    const current = repositoryStates[repository.id]?.areas[area];
    if (cursor ? current?.loadingMore : current?.loading || current?.loaded) return;
    updateAreaState(repository.id, area, (state) => ({
      ...state,
      loading: cursor ? state.loading : true,
      loadingMore: cursor ? true : state.loadingMore,
      error: undefined
    }));
    const requestedProjectId = projectId;
    const token = requestGate(repository.id).begin(`area:${area}`);
    try {
      const request = { area, cursor: cursor ?? "", limit: 500, prefix: "", expandUntracked: false };
      const page = await loadLimited(() => fetchGitChanges(
        requestedProjectId,
        repository.id,
        request,
        token.signal
      ));
      if (!token.isCurrent() || projectId !== requestedProjectId) return;
      if (page.snapshot_id !== repositories.data?.snapshot_id ||
        page.repository_generation !== repository.generation ||
        (page.repository_id && page.repository_id !== repository.id)) {
        if (allowRecovery) await recoverRepositorySnapshot(repository.id);
        else updateAreaState(repository.id, area, (state) => ({
          ...state, loading: false, loadingMore: false, loaded: false,
          error: new Error(t("changes.resultsTruncated"))
        }));
        return;
      }
      updateAreaState(repository.id, area, (state) => ({
        ...state,
        items: cursor ? mergeGitChanges(state.items, page.items) : page.items,
        nextCursor: page.next_cursor,
        truncated: page.truncated,
        loaded: true,
        loading: false,
        loadingMore: false,
        warnings: page.warnings,
        error: undefined
      }));
      updateRepositoryState(repository.id, (state) => ({
        ...state,
        items: mergeGitChanges(
          cursor ? state.items : state.items.filter((item) => item.area !== area),
          page.items
        ),
        snapshotId: page.snapshot_id,
        generation: page.repository_generation
      }));
    } catch (error) {
      if (isAbortError(error) || token.signal.aborted) return;
      if (token.isCurrent() && blockResourceAuthorization(error, requestedProjectId)) return;
      if (allowRecovery && token.isCurrent() && isRecoverableGitSnapshotError(error)) {
        await recoverRepositorySnapshot(repository.id);
        return;
      }
      if (!token.isCurrent()) return;
      updateAreaState(repository.id, area, (state) => ({
        ...state, loading: false, loadingMore: false, error
      }));
    } finally {
      token.finish();
    }
  }

  async function loadRepository(repository: GitRepositorySummary, allowRecovery = true) {
    if (resourceAuthorizationBlocked) return;
    const current = repositoryStates[repository.id];
    if (current?.loading || current?.loaded) return;
    const token = requestGate(repository.id).begin("repository");
    updateRepositoryState(repository.id, (state) => ({ ...state, loading: true, error: undefined }));
    const areas = repositories.data?.truncated
      ? [...GIT_CHANGE_AREAS]
      : GIT_CHANGE_AREAS.filter((area) => repositoryAreaCount(repository, area) > 0);
    await Promise.all(areas.map((area) => loadRepositoryArea(repository, area, undefined, allowRecovery)));
    if (!token.isCurrent()) {
      token.finish();
      return;
    }
    updateRepositoryState(repository.id, (state) => ({
      ...state,
      loaded: true,
      loading: false,
      snapshotId: repositories.data?.snapshot_id,
      generation: repository.generation
    }));
    token.finish();
  }

  async function loadPrefix(
    repository: GitRepositorySummary,
    prefixToken: string,
    cursor?: string,
    allowRecovery = true
  ) {
    const snapshotId = repositories.data?.snapshot_id;
    if (!projectId || !snapshotId || !prefixToken || resourceAuthorizationBlocked) return;
    const prefix = repositoryStates[repository.id]?.prefixes[prefixToken];
    if (cursor ? prefix?.loadingMore : prefix?.loading || prefix?.loaded) return;
    updatePrefixState(repository.id, prefixToken, (state) => ({
      ...state,
      loading: cursor ? state.loading : true,
      loadingMore: cursor ? true : state.loadingMore,
      error: undefined
    }));
    const requestedProjectId = projectId;
    const token = requestGate(repository.id).begin(`prefix:${prefixToken}`);
    try {
      const request = {
        area: "untracked" as const,
        cursor: cursor ?? "",
        limit: 500,
        prefix: prefixToken,
        expandUntracked: true
      };
      const page = await loadLimited(() => fetchGitChanges(
        requestedProjectId,
        repository.id,
        request,
        token.signal
      ));
      if (!token.isCurrent() || projectId !== requestedProjectId) return;
      if (page.snapshot_id !== repositories.data?.snapshot_id ||
        page.repository_generation !== repository.generation ||
        (page.repository_id && page.repository_id !== repository.id)) {
        if (allowRecovery) await recoverRepositorySnapshot(repository.id);
        else updatePrefixState(repository.id, prefixToken, (state) => ({
          ...state, loading: false, loadingMore: false, loaded: false,
          error: new Error(t("changes.resultsTruncated"))
        }));
        return;
      }
      updatePrefixState(repository.id, prefixToken, (state) => ({
        ...state,
        items: cursor ? mergeGitChanges(state.items, page.items) : page.items,
        nextCursor: page.next_cursor,
        truncated: page.truncated,
        loaded: true,
        loading: false,
        loadingMore: false,
        warnings: page.warnings,
        error: undefined
      }));
    } catch (error) {
      if (isAbortError(error) || token.signal.aborted) return;
      if (token.isCurrent() && blockResourceAuthorization(error, requestedProjectId)) return;
      if (allowRecovery && token.isCurrent() && isRecoverableGitSnapshotError(error)) {
        await recoverRepositorySnapshot(repository.id);
        return;
      }
      if (!token.isCurrent()) return;
      updatePrefixState(repository.id, prefixToken, (state) => ({
        ...state, loading: false, loadingMore: false, error
      }));
    } finally {
      token.finish();
    }
  }

  function clearRepository(repositoryId: string) {
    requestGate(repositoryId).invalidate();
    const next = { ...repositoryStates };
    delete next[repositoryId];
    repositoryStates = next;
    expandedDirectories = Object.fromEntries(
      Object.entries(expandedDirectories).filter(([key]) => !key.includes(`:${repositoryId}:`))
    );
    collapsedAreas = Object.fromEntries(
      Object.entries(collapsedAreas).filter(([key]) => !key.startsWith(`area:${repositoryId}:`))
    );
    if (selectedRepositoryId === repositoryId) {
      selectedRepositoryId = "";
      selectedKey = "";
    }
    void queryClient.cancelQueries({ queryKey: ["git", "changes", projectId, repositoryId] });
    void queryClient.cancelQueries({ queryKey: ["git", "diff", projectId, repositoryId] });
  }

  function setRepositoryAwaitingRecovery(repositoryId: string, awaiting: boolean) {
    if (awaiting) {
      repositoriesAwaitingRecovery = { ...repositoriesAwaitingRecovery, [repositoryId]: true };
      return;
    }
    const next = { ...repositoriesAwaitingRecovery };
    delete next[repositoryId];
    repositoriesAwaitingRecovery = next;
  }

  function clearAllRepositoryAuthority(preserveSelection = false) {
    for (const gate of requestGates.values()) gate.invalidate();
    repositoryStates = {};
    expandedDirectories = {};
    collapsedAreas = {};
    selectedRepositoryId = "";
    selectedKey = "";
    if (!preserveSelection) {
      selectionIntent = undefined;
      mobilePane = "tree";
    }
  }

  function blockResourceAuthorization(error: unknown, requestedProjectId: string): boolean {
    if (projectId !== requestedProjectId || !isGitResourceAuthorizationError(error)) return false;
    if (resourceAuthorizationError) return true;
    resourceAuthorizationError = error;
    authorizationRevision += 1;
    clearAllRepositoryAuthority();
    // Also cancel a retry that Query may already have scheduled for an older
    // transport failure. Explicit refresh/invalidation can still start a read.
    void queryClient.cancelQueries({ queryKey: gitRepositoriesQueryKey(requestedProjectId), exact: true });
    for (const timer of invalidationTimers.values()) clearTimeout(timer);
    invalidationTimers.clear();
    for (const controller of recoveryControllers.values()) controller.abort();
    repositoriesAwaitingRecovery = {};
    return true;
  }

  // Diff cards/prefetch use the shared QueryClient. Observe actual failed Git
  // reads too, without treating cached errors/invalidation as fresh denials.
  $effect(() => {
    const requestedProjectId = projectId;
    return queryClient.getQueryCache().subscribe((event) => {
      const key = event.query.queryKey;
      if (event.type === "updated" && event.action.type === "error" &&
        key[0] === "git" && key[1] === "diff" && key[2] === requestedProjectId) {
        blockResourceAuthorization(event.action.error, requestedProjectId);
      }
    });
  });

  function beginOwnRefresh() {
    activeOwnRefreshes += 1;
    suppressRefreshEventsUntil = Number.POSITIVE_INFINITY;
  }

  function finishOwnRefresh() {
    activeOwnRefreshes = Math.max(0, activeOwnRefreshes - 1);
    if (activeOwnRefreshes === 0) suppressRefreshEventsUntil = Date.now() + 5_000;
  }

  async function requestGitRefresh(project: string, signal?: AbortSignal): Promise<void> {
    beginOwnRefresh();
    try {
      await api(`/projects/${encodeURIComponent(project)}/git/refresh`, {
        method: "POST",
        signal
      });
    } catch (error) {
      blockResourceAuthorization(error, project);
      throw error;
    } finally {
      finishOwnRefresh();
    }
  }

  async function reconcileRepositories(refresh = false): Promise<GitRepositoriesSnapshot | undefined> {
    const requestedProjectId = projectId;
    if (!requestedProjectId) return undefined;
    try {
      return await loadRepositorySnapshotForPage(requestedProjectId, refresh);
    } catch (error) {
      if (isAbortError(error)) return undefined;
      throw error;
    }
  }

  async function recoverRepositorySnapshot(repositoryId: string): Promise<void> {
    if (!projectId || resourceAuthorizationBlocked) return;
    const existing = recoveryRequests.get(repositoryId);
    if (existing) return existing;
    const requestedProjectId = projectId;
    setRepositoryAwaitingRecovery(repositoryId, true);
    const controller = new AbortController();
    recoveryControllers.set(repositoryId, controller);
    const request = (async () => {
      clearRepository(repositoryId);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["git", "changes", requestedProjectId, repositoryId], refetchType: "none" }),
        queryClient.invalidateQueries({ queryKey: ["git", "diff", requestedProjectId, repositoryId], refetchType: "none" })
      ]);
      await requestGitRefresh(requestedProjectId, controller.signal);
      if (projectId !== requestedProjectId) return;
      const snapshot = await reconcileRepositories(false);
      const repository = snapshot?.repositories.find((item) => item.id === repositoryId);
      if (repository && expandedRepositories[repositoryId]) await loadRepository(repository, false);
    })().catch((error) => {
      if (isAbortError(error) || controller.signal.aborted) return;
      throw error;
    }).finally(() => {
      const currentRecovery = recoveryRequests.get(repositoryId) === request;
      if (currentRecovery) recoveryRequests.delete(repositoryId);
      if (recoveryControllers.get(repositoryId) === controller) recoveryControllers.delete(repositoryId);
      if (currentRecovery && projectId === requestedProjectId) {
        setRepositoryAwaitingRecovery(repositoryId, false);
      }
    });
    recoveryRequests.set(repositoryId, request);
    return request;
  }

  $effect(() => {
    if (activeProjectId === projectId) return;
    repositoryReconcilePending = (repositoryPendingRequests.get(projectId) ?? 0) > 0;
    repositoryReconcileError = undefined;
    resourceAuthorizationError = undefined;
    for (const timer of invalidationTimers.values()) clearTimeout(timer);
    invalidationTimers.clear();
    observedEventGenerations.clear();
    for (const gate of requestGates.values()) gate.invalidate();
    requestGates.clear();
    for (const controller of recoveryControllers.values()) controller.abort();
    recoveryControllers.clear();
    recoveryRequests.clear();
    activeProjectId = projectId;
    observedSnapshotId = "";
    repositoryStates = {};
    expandedRepositories = {};
    initializedRepositories = {};
    repositoriesAwaitingRecovery = {};
    collapsedAreas = {};
    expandedDirectories = {};
    selectedKey = "";
    selectedRepositoryId = "";
    selectionIntent = undefined;
    mobilePane = "tree";
  });

  $effect(() => {
    if (activeProjectId !== projectId) return;
    const snapshot = repositories.data;
    if (!snapshot) return;
    if (observedSnapshotId && observedSnapshotId !== snapshot.snapshot_id) {
      clearAllRepositoryAuthority(true);
      void queryClient.invalidateQueries({ queryKey: ["git", "diff", projectId], refetchType: "none" });
    }
    observedSnapshotId = snapshot.snapshot_id;

    const nextExpanded = { ...expandedRepositories };
    const nextInitialized = { ...initializedRepositories };
    let changed = false;
    for (const repository of snapshot.repositories) {
      if (!nextInitialized[repository.id]) {
        nextInitialized[repository.id] = true;
        nextExpanded[repository.id] = isRepositoryDirty(repository);
        changed = true;
      }
      const loaded = repositoryStates[repository.id];
      if (loaded?.generation !== undefined && loaded.generation !== repository.generation) {
        clearRepository(repository.id);
      }
    }
    if (changed) {
      expandedRepositories = nextExpanded;
      initializedRepositories = nextInitialized;
    }
  });

  $effect(() => {
    if (resourceAuthorizationBlocked || repositoryReconcilePending || streamState.kind === "connecting") return;
    const searching = Boolean(search.trim());
    for (const repository of repositoryItems) {
      const state = repositoryStates[repository.id];
      const shouldLoad = expandedRepositories[repository.id] ||
        (searching && (!onlyDirty || isRepositoryDirty(repository)));
      if (shouldLoad && !repositoriesAwaitingRecovery[repository.id] &&
        !state?.loaded && !state?.loading) {
        void loadRepository(repository);
      }
    }
  });

  const selectedTarget = $derived.by<GitDiffTarget | undefined>(() => {
    const repository = repositoryById(selectedRepositoryId);
    if (!repository || !selectedKey) return undefined;
    const change = allRepositoryChanges(repositoryStates[repository.id])
      .find((item) => gitChangeIdentity(repository.id, item) === selectedKey);
    return change ? { repository, change } : undefined;
  });

  $effect(() => {
    if (selectedTarget || activeProjectId !== projectId || resourceAuthorizationBlocked || repositoryReconcilePending) return;
    const intent = selectionIntent;
    if (intent) {
      const repository = repositoryById(intent.repositoryId);
      if (repository) {
        const state = repositoryStates[repository.id];
        const items = allRepositoryChanges(state);
        const restored = restoreGitChangeSelection(intent, projectId, repository.id, items);
        if (restored) {
          selectedRepositoryId = repository.id;
          selectedKey = gitChangeIdentity(repository.id, restored);
          return;
        }
        // A non-unique/lossy display match is not authoritative absence either;
        // leave it unselected until the user chooses a fresh tree row explicitly.
        if (items.some((item) => item.kind !== "directory" && item.kind !== "tree" && item.area === intent.area &&
          item.path.display === intent.pathDisplay && (item.path.encoding ?? "") === intent.pathEncoding)) return;
        // A missing target during a refresh is not an absent file. Do not let a
        // faster repository/area response replace the user's pending choice.
        const area = state?.areas[intent.area];
        if (repositoriesAwaitingRecovery[repository.id] || !state?.loaded || area?.loading || area?.loadingMore || area?.error) return;
        if (repositoryAreaCount(repository, intent.area) > 0 && !area?.loaded) return;
        if (area?.nextCursor) {
          void loadRepositoryArea(repository, intent.area, area.nextCursor);
          return;
        }
        if (area?.truncated || repositories.data?.truncated) return;
        if (intent.area === "untracked") {
          const directory = items.filter((item) => (item.kind === "directory" || item.kind === "tree") &&
            item.area === "untracked" && (item.path.encoding ?? "") === intent.pathEncoding &&
            intent.pathDisplay.startsWith(`${item.path.display}/`))
            .sort((left, right) => right.path.display.length - left.path.display.length)[0];
          if (directory) {
            const prefix = state.prefixes[directory.path.token];
            if (!prefix?.loaded || prefix.loading || prefix.loadingMore || prefix.error || prefix.nextCursor || prefix.truncated) {
              if (!prefix?.loading && !prefix?.loadingMore && !prefix?.error && !prefix?.truncated) {
                void loadPrefix(repository, directory.path.token, prefix?.nextCursor ?? undefined);
              }
              return;
            }
          }
        }
      } else if (!repositories.data || repositories.data.truncated) return;
      // Only fresh, complete authority may establish that the choice is gone.
      selectionIntent = undefined;
      selectedRepositoryId = "";
      selectedKey = "";
    }
    // Defaults follow the visible/filter-matching tree, not an unrelated file
    // from the first repository. They never race a pending explicit selection.
    const first = treeRows.find((row) => row.type === "change" && row.change);
    if (first?.change) {
      selectionIntent = gitChangeSelectionIntent(projectId, first.repositoryId, first.change);
      selectedRepositoryId = first.repositoryId;
      selectedKey = gitChangeIdentity(first.repositoryId, first.change);
    }
  });

  const selectedRepositoryChanges = $derived.by<GitChange[]>(() => {
    if (!selectedRepositoryId) return [];
    return allRepositoryChanges(repositoryStates[selectedRepositoryId])
      .filter((change) => change.kind !== "directory" && change.kind !== "tree");
  });
  const selectedChangeIndex = $derived(
    selectedTarget
      ? selectedRepositoryChanges.findIndex((change) => gitChangeIdentity(selectedRepositoryId, change) === selectedKey)
      : -1
  );
  const allStackTargets = $derived.by<GitDiffTarget[]>(() => {
    const repository = repositoryById(selectedRepositoryId);
    if (!repository) return [];
    return selectedRepositoryChanges.map((change) => ({ repository, change }));
  });
  const stackTargets = $derived(selectStackedChanges(allStackTargets, selectedChangeIndex));
  const prefetchTargets = $derived.by<GitDiffTarget[]>(() => {
    if (!allStackTargets.length) return [];
    const index = Math.max(0, selectedChangeIndex);
    const start = Math.max(0, Math.min(allStackTargets.length - 25, index - 12));
    return allStackTargets.slice(start, start + 25);
  });

  function toggleRow(row: GitTreeRow) {
    if (row.type === "repository") {
      const expanding = !expandedRepositories[row.repositoryId];
      expandedRepositories = { ...expandedRepositories, [row.repositoryId]: expanding };
      const repository = repositoryById(row.repositoryId);
      if (expanding && repository && !repositoryStates[row.repositoryId]?.loaded) void loadRepository(repository);
      return;
    }
    if (row.type === "area") {
      collapsedAreas = { ...collapsedAreas, [row.key]: Boolean(row.expanded) };
      return;
    }
    if (row.type === "directory") {
      const expanding = !expandedDirectories[row.key];
      expandedDirectories = { ...expandedDirectories, [row.key]: expanding };
      const repository = repositoryById(row.repositoryId);
      if (expanding && repository && row.lazyUntracked && row.directoryToken) {
        void loadPrefix(repository, row.directoryToken);
      }
    }
  }

  function selectChange(repositoryId: string, change: GitChange) {
    selectionIntent = gitChangeSelectionIntent(projectId, repositoryId, change);
    selectedRepositoryId = repositoryId;
    selectedKey = gitChangeIdentity(repositoryId, change);
    mobilePane = "diff";
  }

  function loadMore(row: GitTreeRow) {
    const repository = repositoryById(row.repositoryId);
    if (!repository) return;
    if (row.type === "load-prefix" && row.prefixToken) {
      void loadPrefix(repository, row.prefixToken);
      return;
    }
    if (!row.cursor) return;
    if (row.prefixToken) void loadPrefix(repository, row.prefixToken, row.cursor);
    else if (row.area) void loadRepositoryArea(repository, row.area, row.cursor);
  }

  function retryRow(row: GitTreeRow) {
    const repository = repositoryById(row.repositoryId);
    if (!repository) return;
    if (row.prefixToken) void loadPrefix(repository, row.prefixToken);
    else if (row.area) void loadRepositoryArea(repository, row.area);
    else void loadRepository(repository);
  }

  const refreshRepositories = createMutation<unknown, Error, void>(() => ({
    mutationFn: async () => {
      const requestedProjectId = projectId;
      clearAllRepositoryAuthority(true);
      await requestGitRefresh(requestedProjectId);
      await queryClient.invalidateQueries({ queryKey: ["git", "changes", requestedProjectId], refetchType: "none" });
      await queryClient.invalidateQueries({ queryKey: ["git", "diff", requestedProjectId], refetchType: "none" });
      await reconcileRepositories(false);
    }
  }));

  const pageError = $derived([repositoryReconcileError, repositories.error, refreshRepositories.error]
    .find((error) => error && !isGitResourceAuthorizationError(error)));

  function repositoryEventInvalidation(event: { repository_id: string; generation: number; reason?: string }) {
    if (resourceAuthorizationBlocked) return;
    const repository = repositoryById(event.repository_id);
    const previouslyObserved = Math.max(
      observedEventGenerations.get(event.repository_id) ?? 0,
      repository?.generation ?? 0
    );
    if (event.generation <= previouslyObserved) return;
    observedEventGenerations.set(event.repository_id, event.generation);

    if (event.reason === "refresh" && Date.now() <= suppressRefreshEventsUntil) return;
    if (["worktree_changed", "git_metadata_changed", "status_changed"].includes(event.reason ?? "") &&
      isGitReadWatchEventSuppressed()) return;

    // Path tokens, change IDs and cursors are snapshot authority. Discard them
    // synchronously; the debounced HTTP refresh may only populate a new snapshot.
    setRepositoryAwaitingRecovery(event.repository_id, true);
    clearRepository(event.repository_id);
    const previousTimer = invalidationTimers.get(event.repository_id);
    if (previousTimer) clearTimeout(previousTimer);
    invalidationTimers.set(event.repository_id, setTimeout(() => {
      invalidationTimers.delete(event.repository_id);
      void recoverRepositorySnapshot(event.repository_id).catch((error) => {
        if (projectId) updateRepositoryState(event.repository_id, (state) => ({ ...state, error }));
      });
    }, 150));
  }

  $effect(() => {
    const currentProjectId = projectId;
    void repositorySubscriptionKey;
    if (!currentProjectId) return;
    if (resourceAuthorizationBlocked) {
      streamState = { kind: "authorization" };
      return;
    }
    let stopEvents: (() => void) | undefined;
    let reconcileTimer: ReturnType<typeof setInterval> | undefined;

    const start = () => {
      if (stopEvents) return;
      stopEvents = subscribeGitRepositoryEvents({
        projectId: currentProjectId,
        onEvent: (event) => {
          if (projectId === currentProjectId) repositoryEventInvalidation(event);
        },
        onState: (state) => {
          if (projectId !== currentProjectId) return;
          streamState = state;
          // Refresh on every successful (re)subscription to close the gap
          // between the repository snapshot and the first observed generation.
          if (state.kind === "connected" || state.kind === "retrying") {
            // EventSource hides HTTP status. A normal authenticated read tells
            // a scope denial apart from a transport failure before reconnecting.
            void reconcileRepositories(state.kind === "connected").catch(() => undefined);
          }
        }
      });
      reconcileTimer = setInterval(() => {
        // A full discovery refresh also picks up newly-ready Resources for
        // which no repository-scoped SSE event could have existed yet.
        if (!document.hidden && projectId === currentProjectId) {
          void reconcileRepositories(false).catch(() => undefined);
        }
      }, 15_000);
    };
    const pause = () => {
      stopEvents?.();
      stopEvents = undefined;
      if (reconcileTimer) clearInterval(reconcileTimer);
      reconcileTimer = undefined;
      streamState = { kind: "paused" };
    };
    const visibility = () => {
      if (projectId !== currentProjectId) return;
      if (document.hidden) pause();
      else {
        start();
        void reconcileRepositories(false).catch(() => undefined);
      }
    };
    document.addEventListener("visibilitychange", visibility);
    visibility();
    return () => {
      document.removeEventListener("visibilitychange", visibility);
      stopEvents?.();
      if (reconcileTimer) clearInterval(reconcileTimer);
    };
  });

  function streamLabel(): string {
    if (resourceAuthorizationBlocked || streamState.kind === "authorization") return t("gitAuthorization.title");
    if (streamState.kind === "connected") return t("changes.liveConnected");
    if (streamState.kind === "connecting") return t("changes.liveConnecting");
    if (streamState.kind === "retrying") return t("changes.liveRetrying", { seconds: streamState.delaySeconds });
    if (streamState.kind === "paused") return t("changes.livePaused");
    return t("changes.liveUnavailable");
  }

  function retryRepositoryDiscovery() {
    repositoryReconcileError = undefined;
    void reconcileRepositories(false).catch(() => undefined);
  }

  function beginResize(event: PointerEvent) {
    if (event.button !== 0) return;
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = treeWidth;
    const move = (moveEvent: PointerEvent) => {
      treeWidth = Math.max(260, Math.min(480, startWidth + moveEvent.clientX - startX));
    };
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
      resizeCleanup = undefined;
    };
    resizeCleanup?.();
    resizeCleanup = stop;
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  }

  function resizeWithKeyboard(event: KeyboardEvent) {
    if (event.key === "ArrowLeft") treeWidth = Math.max(260, treeWidth - 16);
    else if (event.key === "ArrowRight") treeWidth = Math.min(480, treeWidth + 16);
    else if (event.key === "Home") treeWidth = 260;
    else if (event.key === "End") treeWidth = 480;
    else return;
    event.preventDefault();
  }

  onDestroy(() => {
    resizeCleanup?.();
    for (const timer of invalidationTimers.values()) clearTimeout(timer);
    for (const gate of requestGates.values()) gate.invalidate();
    for (const controller of recoveryControllers.values()) controller.abort();
  });
</script>

<RequireProject>
  <section class="flex h-full min-h-0 flex-col overflow-hidden bg-surface" data-testid="changes-page">
    <header class="flex min-h-[52px] shrink-0 items-center gap-3 border-b border-border bg-surface px-4 max-md:min-h-[48px] max-md:px-3">
      <GitCompareArrows size={18} class="shrink-0 text-accent" />
      <div class="min-w-0">
        <h1 class="m-0 truncate text-md font-semibold tracking-[-0.01em]">{t("changes.title")}</h1>
        <p class="m-0 truncate text-[10px] text-muted max-md:hidden">{t("changes.description")}</p>
      </div>
      <span class="ml-auto inline-flex min-h-7 shrink-0 items-center gap-1.5 rounded-full border border-border bg-surface-subtle px-2 text-[10px] font-medium {resourceAuthorizationBlocked || streamState.kind === 'retrying' || streamState.kind === 'unsupported' ? 'text-warning' : 'text-muted'}" title={streamLabel()}>
        {#if resourceAuthorizationBlocked}<AlertTriangle size={11} />{:else if streamState.kind === "connected"}<Wifi size={11} class="text-success" />{:else if streamState.kind === "connecting"}<LoaderCircle size={11} class="animate-spin" />{:else}<WifiOff size={11} />{/if}
        <span class="max-md:hidden">{streamLabel()}</span>
      </span>
      <Button
        type="button"
        controlSize="sm" class="shrink-0"
        disabled={refreshRepositories.isPending}
        onclick={() => refreshRepositories.mutate()}
      >
        <RefreshCw size={13} class={refreshRepositories.isPending ? "animate-spin" : ""} />
        <span class="max-md:hidden">{refreshRepositories.isPending ? t("changes.refreshing") : t("changes.refresh")}</span>
      </Button>
    </header>

    {#if repositories.data?.truncated || repositories.data?.warnings.some((warning) => !isWatchFallbackWarning(warning))}
      <div class="flex min-h-8 shrink-0 items-center gap-2 overflow-x-auto border-b border-warning/25 bg-warning-subtle px-3 text-[10px] text-warning">
        <AlertTriangle size={12} class="shrink-0" />
        {#if repositories.data.truncated}<span class="shrink-0">{t("changes.repositoriesTruncated")}</span>{/if}
        {#each repositories.data.warnings.filter((warning) => !isWatchFallbackWarning(warning)) as warning}
          <span class="shrink-0">{warningText(warning)}</span>
        {/each}
      </div>
    {/if}
    {#if resourceAuthorizationBlocked}
      <div role="alert" class="grid shrink-0 gap-1 border-b border-warning/25 bg-warning-subtle px-4 py-3 text-xs text-warning">
        <strong>{t("gitAuthorization.title")}</strong>
        <p class="m-0 leading-relaxed">{t("gitAuthorization.description")}</p>
        <a class="w-fit font-medium underline underline-offset-2" href={resourcesURL} onclick={(event) => { event.preventDefault(); navigate(resourcesURL); }}>{t("gitAuthorization.goToResources")}</a>
      </div>
    {:else if pageError}
      <div class="shrink-0 px-3 pt-3"><ErrorNotice error={pageError} onRetry={retryRepositoryDiscovery} /></div>
    {/if}

    <div class="hidden min-h-10 shrink-0 grid-cols-2 border-b border-border bg-surface-subtle p-1.5 max-md:grid" aria-label={t("changes.mobilePane")}>
      <button type="button" class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {mobilePane === 'tree' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mobilePane === "tree"} onclick={() => (mobilePane = "tree")}><FolderTree size={14} />{t("changes.treePane")}</button>
      <button type="button" class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {mobilePane === 'diff' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mobilePane === "diff"} disabled={!selectedTarget} onclick={() => (mobilePane = "diff")}><Files size={14} />{t("changes.diffPane")}</button>
    </div>

    <div class="changes-workspace grid min-h-0 flex-1 overflow-hidden" style={`--changes-tree-width:${treeWidth}px`}>
      <aside class="changes-tree-pane flex min-h-0 min-w-0 flex-col border-r border-border bg-surface-subtle {mobilePane === 'diff' ? 'max-md:hidden' : ''}" aria-label={t("changes.treePane")}>
        <div class="grid shrink-0 gap-2 border-b border-border bg-surface p-2">
          <label class="relative block">
            <span class="sr-only">{t("changes.searchLabel")}</span>
            <Search size={13} class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input controlSize="sm" bind:value={search} class="!border-border !pl-8 !pr-2 !text-xs" type="search" placeholder={t("changes.searchPlaceholder")} />
          </label>
          <div class="flex items-center gap-1.5">
            <div class="inline-flex min-w-0 flex-1 rounded-sm border border-border bg-surface-subtle p-0.5" aria-label={t("changes.viewMode")}>
              <button type="button" class="inline-flex min-h-7 min-w-0 flex-1 cursor-pointer items-center justify-center gap-1 rounded-[6px] border-0 px-1 text-[10px] font-semibold {view === 'tree' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={view === "tree"} onclick={() => (view = "tree")}><FolderTree size={12} />{t("changes.tree")}</button>
              <button type="button" class="inline-flex min-h-7 min-w-0 flex-1 cursor-pointer items-center justify-center gap-1 rounded-[6px] border-0 px-1 text-[10px] font-semibold {view === 'flat' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={view === "flat"} onclick={() => (view = "flat")}><List size={12} />{t("changes.flat")}</button>
            </div>
            <button type="button" class="inline-flex min-h-8 shrink-0 cursor-pointer items-center gap-1 rounded-sm border border-border px-2 text-[10px] font-semibold {onlyDirty ? 'bg-accent-subtle text-accent' : 'bg-surface text-muted hover:text-text'}" aria-pressed={onlyDirty} onclick={() => (onlyDirty = !onlyDirty)}><SlidersHorizontal size={12} />{t("changes.onlyDirty")}</button>
          </div>
        </div>

        <div class="min-h-0 flex-1">
          {#if resourceAuthorizationBlocked}
            <div class="grid h-full place-content-center gap-2 px-5 text-center"><AlertTriangle size={27} class="mx-auto text-warning" /><p class="m-0 max-w-[280px] text-xs leading-relaxed text-secondary">{t("gitAuthorization.paused")}</p></div>
          {:else if repositories.isLoading && !repositories.data}
            <div class="flex h-full items-center justify-center gap-2 text-xs text-muted"><LoaderCircle size={14} class="animate-spin" />{t("changes.loadingRepositories")}</div>
          {:else if (repositories.error || repositoryReconcileError) && !repositoryItems.length}
            <div class="grid h-full place-content-center gap-2 px-5 text-center"><AlertTriangle size={27} class="mx-auto text-warning" /><h2 class="m-0 text-sm font-semibold">{t("changes.repositoriesLoadFailed")}</h2><p class="m-0 max-w-[280px] text-xs leading-relaxed text-secondary">{t("changes.retryRepository")}</p></div>
          {:else if !repositoryItems.length}
            <div class="grid h-full place-content-center gap-3 px-5 text-center">
              <GitCompareArrows size={27} class="mx-auto text-muted" />
              <h2 class="m-0 text-sm font-semibold">{t("changes.noRepositories")}</h2>
              <p class="m-0 max-w-[280px] text-xs leading-relaxed text-secondary">{t("changes.noRepositoriesDesc")}</p>
              <Button
                class="mx-auto" controlSize="sm" variant="primary"
                type="button"
                onclick={() => navigate("/resources")}
              >{t("changes.goToResources")}</Button>
            </div>
          {:else if !treeRows.length}
            <div class="grid h-full place-content-center px-5 text-center text-xs text-muted">{t("changes.noMatchingChanges")}</div>
          {:else}
            <GitChangesTree rows={treeRows} {selectedKey} onToggle={toggleRow} onSelect={selectChange} onLoadMore={loadMore} onRetry={retryRow} />
          {/if}
        </div>
      </aside>

      <div
        class="changes-resizer cursor-col-resize bg-border transition-colors hover:bg-accent focus:bg-accent max-md:hidden"
        role="slider"
        aria-label={t("changes.resizeTree")}
        aria-orientation="vertical"
        aria-valuemin="260"
        aria-valuemax="480"
        aria-valuenow={treeWidth}
        tabindex="0"
        onpointerdown={beginResize}
        onkeydown={resizeWithKeyboard}
        ondblclick={() => (treeWidth = 320)}
      ></div>

      <div class="min-h-0 min-w-0 {mobilePane === 'tree' ? 'max-md:hidden' : ''}">
        {#if !resourceAuthorizationBlocked}
        <GitDiffViewer
          {projectId}
          snapshotId={repositories.data?.snapshot_id ?? ""}
          target={selectedTarget}
          {stackTargets}
          stackTotal={allStackTargets.length}
          {prefetchTargets}
          bind:mode={diffMode}
          onSnapshotError={(repositoryId) => void recoverRepositorySnapshot(repositoryId)}
        />
        {/if}
      </div>
    </div>
  </section>
</RequireProject>

<style>
  .changes-workspace {
    grid-template-columns: var(--changes-tree-width) 6px minmax(0, 1fr);
  }

  @media (max-width: 767px) {
    .changes-workspace {
      display: block;
    }

    .changes-tree-pane,
    .changes-workspace > :last-child {
      height: 100%;
    }
  }
</style>
