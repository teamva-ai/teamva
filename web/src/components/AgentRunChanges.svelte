<script lang="ts">
  import { Button, Input, Select } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import { AlertTriangle, Bot, Files, FolderTree, History, LoaderCircle, Search } from "lucide-svelte";
  import { onDestroy, onMount } from "svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import RunGitChangesTree from "./RunGitChangesTree.svelte";
  import RunGitDiffViewer from "./RunGitDiffViewer.svelte";
  import { createConcurrencyLimiter } from "../lib/gitChanges";
  import { formatTime } from "../lib/helpers";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import {
    buildRunGitFileTargets, buildRunGitTreeRows, collectUnverifiedProviderReports,
    fetchRunGitChanges, runChangesBundleQueryKey, runGitChangesQueryKey,
    runGitResultRetainedBytes, runHasGitChanges, type GitRunChangesResult, type RunChangesBundle,
    type RunChangesScopeKind, type RunGitFileTarget, type RunGitSource, type RunGitTreeRow
  } from "../lib/runGitChanges";
  import type { RunChangesInvalidationSignal } from "../lib/activityEvents";
  import type { GitRepositorySummary } from "../lib/gitChanges";
  import type { Run } from "../lib/types";

  let {
    scopeId, scopeKind = "instance", runs, repositories, width,
    defaultScope = "latest", focusRunId = "", liveChangeSignal,
    hasOlderRuns = false, runHistoryLimited = false, loadingOlderRuns = false,
    onLoadOlderRuns = () => {}
  }: {
    scopeId: string;
    scopeKind?: RunChangesScopeKind;
    runs: Run[];
    repositories: GitRepositorySummary[];
    width: number;
    defaultScope?: "latest" | "all";
    focusRunId?: string;
    liveChangeSignal?: RunChangesInvalidationSignal;
    hasOlderRuns?: boolean;
    runHistoryLimited?: boolean;
    loadingOlderRuns?: boolean;
    onLoadOlderRuns?: () => void | Promise<void>;
  } = $props();

  const loadLimited = createConcurrencyLimiter(2);
  const maxBundleBytes = 6 * 1024 * 1024;
  const maxAllScopeRuns = 50;
  const maxLatestScannedRuns = 50;
  let scope = $state<string>("latest");
  let observedFocusRunId = $state("");
  let source = $state<"all" | RunGitSource>("all");
  let search = $state("");
  let collapsed = $state<Record<string, boolean | undefined>>({});
  let selectedId = $state("");
  let mode = $state<"single" | "stacked">("single");
  let mobilePane = $state<"tree" | "diff">("tree");
  let shell = $state<HTMLElement>();
  let measuredWidth = $state(0);
  let invalidationTimer: ReturnType<typeof setTimeout> | undefined;
  let observedScopeIdentity = $state("");

  $effect(() => {
    const identity = `${scopeKind}:${scopeId}`;
    if (observedScopeIdentity === identity) return;
    observedScopeIdentity = identity;
    if (invalidationTimer) clearTimeout(invalidationTimer);
    invalidationTimer = undefined;
    scope = defaultScope;
    observedFocusRunId = "";
    source = "all";
    search = "";
    collapsed = {};
    selectedId = "";
    mode = "single";
    mobilePane = "tree";
  });

  onDestroy(() => {
    if (invalidationTimer) clearTimeout(invalidationTimer);
  });

  const newestRuns = $derived([...runs].sort((left, right) =>
    Date.parse(right.created_at) - Date.parse(left.created_at) || right.id.localeCompare(left.id)
  ));
  const safeFocusRunId = $derived(
    focusRunId && newestRuns.some((run) => run.id === focusRunId) ? focusRunId : ""
  );
  const chronologicalRuns = $derived([...newestRuns].reverse());
  const roundByRun = $derived(new Map(chronologicalRuns.map((run, index) => [run.id, index + 1])));
  const wide = $derived((measuredWidth || width) >= 780);

  onMount(() => {
    if (!shell) return;
    measuredWidth = shell.clientWidth || width;
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => {
      measuredWidth = entry?.contentRect.width || shell?.clientWidth || width;
    });
    observer.observe(shell);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!safeFocusRunId) {
      if (observedFocusRunId) {
        observedFocusRunId = "";
        scope = defaultScope;
      }
      return;
    }
    if (safeFocusRunId !== observedFocusRunId) {
      observedFocusRunId = safeFocusRunId;
      scope = `run:${safeFocusRunId}`;
    }
  });

  function bundleKey() {
    return [
      ...runChangesBundleQueryKey(scopeKind, scopeId),
      scope,
      newestRuns.map((run) => run.id).join(",")
    ] as const;
  }

  async function fetchOne(runId: string, signal?: AbortSignal): Promise<GitRunChangesResult> {
    return loadLimited(() => fetchRunGitChanges(runId, signal));
  }

  async function loadScope(requestedScope: string, requestedRuns: Run[], signal?: AbortSignal): Promise<RunChangesBundle> {
    if (requestedScope.startsWith("run:")) {
      const runId = requestedScope.slice(4);
      if (!requestedRuns.some((run) => run.id === runId)) {
        return { results: [], resolvedRunIds: [] };
      }
      return { results: [await fetchOne(runId, signal)], resolvedRunIds: [runId] };
    }
    if (requestedScope === "all") {
      const candidates = requestedRuns.slice(0, maxAllScopeRuns);
      const results: GitRunChangesResult[] = [];
      const resolvedRunIds: string[] = [];
      let retainedBytes = 0;
      let historyLimited = candidates.length < requestedRuns.length;
      outer: for (let index = 0; index < candidates.length; index += 2) {
        signal?.throwIfAborted();
        const pair = candidates.slice(index, index + 2);
        const loaded = await Promise.all(pair.map((run) => fetchOne(run.id, signal)));
        for (let pairIndex = 0; pairIndex < loaded.length; pairIndex += 1) {
          resolvedRunIds.push(pair[pairIndex].id);
          const weight = runGitResultRetainedBytes(loaded[pairIndex]);
          if (retainedBytes + weight > maxBundleBytes) {
            historyLimited = true;
            break outer;
          }
          retainedBytes += weight;
          results.push(loaded[pairIndex]);
        }
      }
      return { results, resolvedRunIds, historyLimited: historyLimited || undefined };
    }
    const resolvedRunIds: string[] = [];
    const candidates = requestedRuns.slice(0, maxLatestScannedRuns);
    for (let index = 0; index < candidates.length; index += 2) {
      signal?.throwIfAborted();
      const pair = candidates.slice(index, index + 2);
      const results = await Promise.all(pair.map((run) => fetchOne(run.id, signal)));
      resolvedRunIds.push(...pair.map((run) => run.id));
      const changed = results.find((result) => runHasGitChanges(result));
      if (changed) return { results: [changed], resolvedRunIds };
    }
    return {
      results: [], resolvedRunIds,
      historyLimited: candidates.length < requestedRuns.length || undefined
    };
  }

  const changes = createQuery<RunChangesBundle>(() => {
    const requestedScope = scope;
    const requestedRuns = [...newestRuns];
    return {
      queryKey: bundleKey(),
      queryFn: ({ signal }) => loadScope(requestedScope, requestedRuns, signal),
      enabled: requestedRuns.length > 0,
      staleTime: 30_000,
      gcTime: 60_000,
      refetchOnWindowFocus: false
    };
  });

  $effect(() => {
    const signal = liveChangeSignal;
    if (!signal || !newestRuns.some((run) => run.id === signal.runId)) return;
    if (invalidationTimer) clearTimeout(invalidationTimer);
    invalidationTimer = setTimeout(() => {
      invalidationTimer = undefined;
      // SSE is only an invalidation signal. Provider patch/path history is read
      // exclusively through the bounded, cursor-following HTTP endpoint.
      void queryClient.invalidateQueries({ queryKey: runGitChangesQueryKey(signal.runId) });
      void queryClient.invalidateQueries({ queryKey: runChangesBundleQueryKey(scopeKind, scopeId) });
    }, 150);
  });

  const targets = $derived(buildRunGitFileTargets(changes.data?.results ?? [], newestRuns, repositories));
  const unverifiedReports = $derived(collectUnverifiedProviderReports(changes.data?.results ?? [], newestRuns));
  const rows = $derived(buildRunGitTreeRows({ targets, source, search, collapsed }));
  const visibleTargets = $derived(targets.filter((target) =>
    source === "all" || target.source === source
  ));
  const searchedTargets = $derived.by(() => {
    const needle = search.trim().toLocaleLowerCase();
    if (!needle) return visibleTargets;
    return visibleTargets.filter((target) =>
      `${target.path}\n${target.oldPath ?? ""}`.toLocaleLowerCase().includes(needle)
    );
  });
  const selected = $derived(searchedTargets.find((target) => target.id === selectedId));
  const incomplete = $derived((changes.data?.results ?? []).some((result) =>
    result.snapshots.some((snapshot) => snapshot.phase === "start") &&
    !result.snapshots.some((snapshot) => snapshot.phase === "end")
  ));
  const historyTruncated = $derived((changes.data?.results ?? []).some((result) =>
    result.provider_reports_truncated || result.provider_reports.some((report) => report.truncated) ||
    result.snapshots.some((snapshot) => snapshot.truncated || snapshot.areas.some((area) => area.truncated))
  ) || Boolean(changes.data?.historyLimited) || runHistoryLimited || targets.buildLimited);
  const runSetIncomplete = $derived(
    hasOlderRuns || runHistoryLimited || Boolean(changes.data?.historyLimited)
  );
  const allRunsLabel = $derived(scopeKind === "issue"
    ? t(runSetIncomplete ? "runChanges.loadedIssueRuns" : "runChanges.allIssueRuns")
    : t(runSetIncomplete ? "runChanges.loadedRuns" : "runChanges.allSessionRuns")
  );

  $effect(() => {
    if (selected) return;
    selectedId = searchedTargets[0]?.id ?? "";
  });

  function toggle(row: RunGitTreeRow) {
    collapsed = { ...collapsed, [row.key]: Boolean(row.expanded) };
  }

  function select(target: RunGitFileTarget) {
    selectedId = target.id;
    mobilePane = "diff";
  }

  function repositoryLabel(run: Run): string {
    return t("runChanges.projectWorkspace");
  }
</script>

<section bind:this={shell} class="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-surface" data-testid="agent-run-changes">
  <div class="grid shrink-0 gap-2 border-b border-border bg-surface px-3 py-2">
    <div class="flex flex-wrap items-end gap-2">
      <label class="grid min-w-0 flex-[1_1_180px] gap-1 text-[10px] font-medium text-muted">
        <span>{t("runChanges.scope")}</span>
        <Select controlSize="sm" class="min-w-0 text-xs" bind:value={scope} aria-label={t("runChanges.scope")}>
          <option value="latest">{t("runChanges.latestChanged")}</option>
          {#each newestRuns as run (run.id)}
            <option value={`run:${run.id}`}>{t("runChanges.roundOption", { round: roundByRun.get(run.id) ?? 1, time: formatTime(run.created_at), repo: repositoryLabel(run) })}</option>
          {/each}
          <option value="all">{allRunsLabel}</option>
        </Select>
      </label>
      <fieldset class="m-0 flex min-w-0 max-w-full flex-[0_1_auto] flex-wrap items-center gap-1 border-0 p-0" aria-label={t("runChanges.sourceFilter")}>
        {#each [["all", t("runChanges.sourceAll")], ["provider", t("runChanges.agentReported")], ["observation", t("runChanges.runObservation")]] as option}
          <Button type="button" controlSize="sm" class="px-2 text-[10px] font-semibold {source === option[0] ? 'border-accent/40 bg-accent-subtle text-accent' : 'border-border bg-surface text-muted hover:text-text'}" aria-pressed={source === option[0]} onclick={() => (source = option[0] as typeof source)}>
            {#if option[0] === "provider"}<Bot size={11} />{:else if option[0] === "observation"}<History size={11} />{/if}{option[1]}
          </Button>
        {/each}
      </fieldset>
    </div>
    <label class="relative block">
      <span class="sr-only">{t("runChanges.search")}</span>
      <Search size={13} class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
      <Input controlSize="sm" bind:value={search} class="!pl-8 !text-xs" type="search" placeholder={t("runChanges.searchPlaceholder")} />
    </label>
  </div>

  {#if hasOlderRuns}
    <div class="flex shrink-0 items-center justify-between gap-2 border-b border-border bg-surface-subtle px-3 py-2 text-[10px] text-secondary" role="status">
      <span>{t("runChanges.loadedRunsHint")}</span>
      <Button type="button" controlSize="sm" class="shrink-0" disabled={loadingOlderRuns} onclick={() => void onLoadOlderRuns()}>
        {#if loadingOlderRuns}<LoaderCircle size={11} class="mr-1 animate-spin" />{/if}{t("issues.loadOlderTurns")}
      </Button>
    </div>
  {:else if runHistoryLimited}
    <div class="shrink-0 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-[10px] text-warning" role="status">{t("runChanges.loadedRunsLimited")}</div>
  {/if}

  {#if source !== "provider"}
    <div class="shrink-0 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-[10px] leading-relaxed text-warning" role="note">
      {t("runChanges.observationDisclaimer")}
    </div>
  {/if}
  {#if incomplete}
    <div class="shrink-0 border-b border-border bg-surface-subtle px-3 py-2 text-[10px] text-secondary" role="status">{t("runChanges.incompleteSnapshot")}</div>
  {/if}
  {#if historyTruncated}
    <div class="shrink-0 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-[10px] text-warning" role="status">{t("runChanges.truncatedDesc")}</div>
  {/if}
  {#if unverifiedReports.length}
    <section class="shrink-0 border-b border-warning/30 bg-warning-subtle px-3 py-2" aria-labelledby={`unverified-reports-${scopeKind}-${scopeId}`} data-testid="unverified-provider-reports">
      <div class="flex items-start gap-2 text-[10px] leading-relaxed text-warning">
        <AlertTriangle size={13} class="mt-0.5 shrink-0" />
        <div class="min-w-0 flex-1">
          <strong id={`unverified-reports-${scopeKind}-${scopeId}`} class="block text-xs">{t("runChanges.unverifiedReports")}</strong>
          <p class="m-0 mt-0.5">{t("runChanges.unverifiedWarning")}</p>
          <ul class="m-0 mt-1 grid max-h-24 list-none gap-1 overflow-y-auto p-0">
            {#each unverifiedReports as report (`${report.runId}:${report.id}:${report.occurredAt}`)}
              <li class="flex min-w-0 flex-wrap items-center gap-x-2 rounded-sm border border-warning/20 bg-surface/70 px-2 py-1 text-secondary">
                <span class="shrink-0 font-semibold text-warning">{t("runChanges.roundOption", { round: report.round, time: formatTime(report.occurredAt), repo: t("runChanges.unverifiedReports") })}</span>
                <span class="min-w-0 flex-1 truncate" title={report.displayNames.join(", ")}>{report.displayNames.join(", ") || t("runChanges.unverifiedPath")}</span>
                <span class="shrink-0 font-mono text-[9px] text-muted">{report.source}</span>
              </li>
            {/each}
          </ul>
        </div>
      </div>
    </section>
  {/if}
  {#if changes.error}<div class="shrink-0 p-3"><ErrorNotice error={changes.error} onRetry={() => changes.refetch()} /></div>{/if}

  {#if !wide}
    <div class="grid min-h-10 shrink-0 grid-cols-2 border-b border-border bg-surface-subtle p-1.5" aria-label={t("runChanges.mobilePane")}>
      <button type="button" class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {mobilePane === 'tree' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mobilePane === "tree"} onclick={() => (mobilePane = "tree")}><FolderTree size={14} />{t("runChanges.treePane")}</button>
      <button type="button" class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {mobilePane === 'diff' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mobilePane === "diff"} disabled={!selected} onclick={() => (mobilePane = "diff")}><Files size={14} />{t("runChanges.diffPane")}</button>
    </div>
  {/if}

  {#if changes.isLoading}
    <div class="flex min-h-0 flex-1 items-center justify-center gap-2 text-xs text-muted"><LoaderCircle size={14} class="animate-spin" />{t("runChanges.loading")}</div>
  {:else if !targets.length}
    <div class="grid min-h-0 flex-1 place-content-center gap-2 px-6 text-center"><History size={28} class="mx-auto text-muted" /><h3 class="m-0 text-sm font-semibold">{t(runSetIncomplete ? "runChanges.loadedEmpty" : "runChanges.empty")}</h3><p class="m-0 max-w-[430px] text-xs leading-relaxed text-secondary">{t(runSetIncomplete ? "runChanges.loadedEmptyDesc" : "runChanges.emptyDesc")}</p></div>
  {:else}
    <div class="run-changes-workspace grid min-h-0 flex-1 overflow-hidden" class:run-changes-narrow={!wide}>
      <aside class="min-h-0 min-w-0 border-r border-border bg-surface-subtle {mobilePane === 'diff' && !wide ? 'hidden' : ''}" aria-label={t("runChanges.treePane")}>
        {#if rows.length}<RunGitChangesTree {rows} {selectedId} onToggle={toggle} onSelect={select} />{:else}<div class="grid h-full place-content-center text-xs text-muted">{t("runChanges.noSourceMatches")}</div>{/if}
      </aside>
      <div class="min-h-0 min-w-0 {mobilePane === 'tree' && !wide ? 'hidden' : ''}">
        <RunGitDiffViewer target={selected} targets={searchedTargets} bind:mode />
      </div>
    </div>
  {/if}
</section>

<style>
  .run-changes-workspace {
    grid-template-columns: 264px minmax(0, 1fr);
  }
  .run-changes-workspace.run-changes-narrow {
    display: block;
  }
  .run-changes-workspace.run-changes-narrow > * {
    height: 100%;
  }
</style>
