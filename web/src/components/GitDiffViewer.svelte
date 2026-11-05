<script lang="ts">
  import { onMount } from "svelte";
  import { Virtualizer } from "@pierre/diffs";
  import type { WorkerPoolManager } from "@pierre/diffs/worker";
  import { Columns2, Files, Rows3, SquareSplitVertical, Wand2 } from "lucide-svelte";
  import GitDiffCard from "./GitDiffCard.svelte";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { acquireGitDiffWorkerPool } from "../lib/gitDiffWorker";
  import {
    fetchGitDiff, gitDiffLifecycleKey, gitDiffQueryKey, gitDiffRetainedBytes,
    nextAutomaticDiffPrefetchBytes,
    type GitDiffLayout, type GitDiffResponse, type GitDiffTarget
  } from "../lib/gitChanges";

  let {
    projectId,
    snapshotId,
    target,
    stackTargets = [],
    stackTotal = 0,
    prefetchTargets = [],
    mode = $bindable<"single" | "stacked">("single"),
    onSnapshotError
  }: {
    projectId: string;
    snapshotId: string;
    target?: GitDiffTarget;
    stackTargets?: GitDiffTarget[];
    stackTotal?: number;
    prefetchTargets?: GitDiffTarget[];
    mode?: "single" | "stacked";
    onSnapshotError?: (repositoryId: string, error: unknown) => void;
  } = $props();

  let shell = $state<HTMLElement>();
  let scroller = $state<HTMLElement>();
  let content = $state<HTMLElement>();
  let width = $state(0);
  let layoutOverride = $state<GitDiffLayout | null>(null);
  let virtualizer = $state<Virtualizer>();
  let workerPool = $state<WorkerPoolManager>();
  let themeType = $state<"light" | "dark">(
    document.documentElement.dataset.theme === "dark" ? "dark" : "light"
  );
  let ready = $state(false);
  let mounted = $state<Record<string, boolean | undefined>>({});
  let lazyObserver: IntersectionObserver | undefined;
  const pendingLazyNodes = new Map<Element, string>();
  let lastPrefetchSignature = "";
  let prefetchController: AbortController | undefined;
  let observedAuthority = "";
  let managedPrefetches: Array<{
    signature: string;
    queryKey: ReturnType<typeof gitDiffQueryKey>;
    bytes: number;
  }> = [];

  const automaticLayout = $derived<GitDiffLayout>(width >= 1_100 ? "split" : "unified");
  const layout = $derived(layoutOverride ?? automaticLayout);

  function targetKey(item: GitDiffTarget): string {
    return gitDiffLifecycleKey(item);
  }

  function pruneManagedPrefetches(removeInactive = false): number {
    const retained: typeof managedPrefetches = [];
    let bytes = 0;
    for (const entry of managedPrefetches) {
      const query = queryClient.getQueryCache().find({ queryKey: entry.queryKey, exact: true });
      if (!query || query.state.data === undefined) continue;
      // Once a card observes the query it owns that visible cache; automatic
      // cleanup must never tear it out from under the renderer.
      if (query.getObserversCount() > 0) continue;
      if (removeInactive) {
        queryClient.removeQueries({ queryKey: entry.queryKey, exact: true });
        continue;
      }
      retained.push(entry);
      bytes += entry.bytes;
    }
    managedPrefetches = retained;
    return bytes;
  }

  async function prefetchWithinBudget(
    candidates: GitDiffTarget[], controller: AbortController,
    requestedProjectId: string, requestedSnapshotId: string
  ) {
    let retainedBytes = pruneManagedPrefetches();
    for (const candidate of candidates) {
      if (controller.signal.aborted || document.hidden) return;
      const queryKey = gitDiffQueryKey({
        projectId: requestedProjectId,
        snapshotId: requestedSnapshotId,
        repositoryId: candidate.repository.id,
        repositoryGeneration: candidate.repository.generation,
        changeId: candidate.change.id,
        area: candidate.change.area,
        pathToken: candidate.change.path.token,
        context: 3,
        allowLarge: false
      });
      const querySignature = JSON.stringify(queryKey);
      if (managedPrefetches.some((entry) => entry.signature === querySignature)) continue;
      const existing = queryClient.getQueryData<GitDiffResponse>(queryKey);
      if (existing) {
        // Manual/visible queries are outside the automatic prefetch cache.
        continue;
      }

      try {
        const prefetched = await queryClient.fetchQuery<GitDiffResponse>({
          queryKey,
          queryFn: ({ signal }) => fetchGitDiff({
            projectId: requestedProjectId,
            repositoryId: candidate.repository.id,
            changeId: candidate.change.id,
            context: 3,
            allowLarge: false,
            signal: typeof AbortSignal.any === "function"
              ? AbortSignal.any([signal, controller.signal])
              : controller.signal
          }),
          staleTime: 30_000,
          gcTime: 30_000,
          retry: false
        });
        if (controller.signal.aborted) return;
        const nextBytes = gitDiffRetainedBytes(prefetched);
        const nextRetainedBytes = nextAutomaticDiffPrefetchBytes(retainedBytes, prefetched);
        if (nextRetainedBytes === undefined) {
          const query = queryClient.getQueryCache().find({ queryKey, exact: true });
          if (!query || query.getObserversCount() === 0) {
            queryClient.removeQueries({ queryKey, exact: true });
          }
          return;
        }
        retainedBytes = nextRetainedBytes;
        managedPrefetches.push({ signature: querySignature, queryKey, bytes: nextBytes });
      } catch (error) {
        if (controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError")) return;
        // Prefetch is opportunistic. The mounted card owns visible errors.
      }
    }
  }

  function mountTarget(key: string) {
    if (mounted[key]) return;
    mounted = { ...mounted, [key]: true };
  }

  function observeLazy(node: Element, key: string) {
    node.setAttribute("data-lazy-diff-key", key);
    pendingLazyNodes.set(node, key);
    if (mounted[key] || typeof IntersectionObserver === "undefined") mountTarget(key);
    else lazyObserver?.observe(node);
    return {
      update(nextKey: string) {
        lazyObserver?.unobserve(node);
        pendingLazyNodes.delete(node);
        key = nextKey;
        node.setAttribute("data-lazy-diff-key", key);
        pendingLazyNodes.set(node, key);
        if (mounted[key] || typeof IntersectionObserver === "undefined") mountTarget(key);
        else lazyObserver?.observe(node);
      },
      destroy() {
        lazyObserver?.unobserve(node);
        pendingLazyNodes.delete(node);
      }
    };
  }

  onMount(() => {
    const cleanup: Array<() => void> = [];
    if (shell) {
      width = shell.clientWidth || window.innerWidth;
      if (typeof ResizeObserver !== "undefined") {
        const resize = new ResizeObserver(([entry]) => {
          width = entry?.contentRect.width || shell?.clientWidth || window.innerWidth;
        });
        resize.observe(shell);
        cleanup.push(() => resize.disconnect());
      }
    }

    const themeObserver = new MutationObserver(() => {
      themeType = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
    });
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    cleanup.push(() => themeObserver.disconnect());

    if (scroller && content && typeof ResizeObserver !== "undefined" && typeof IntersectionObserver !== "undefined") {
      const nextVirtualizer = new Virtualizer({
        overscrollSize: 900,
        intersectionObserverMargin: 1_200,
        resizeDebugging: false
      });
      nextVirtualizer.setup(scroller, content);
      virtualizer = nextVirtualizer;
      cleanup.push(() => nextVirtualizer.cleanUp());

      lazyObserver = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          const key = pendingLazyNodes.get(entry.target);
          if (key) mountTarget(key);
          lazyObserver?.unobserve(entry.target);
        }
      }, {
        root: scroller,
        rootMargin: "300px 0px 300px 0px",
        threshold: 0
      });
      for (const [node, key] of pendingLazyNodes) {
        if (mounted[key]) continue;
        lazyObserver.observe(node);
      }
      cleanup.push(() => {
        lazyObserver?.disconnect();
        lazyObserver = undefined;
      });

      const workerLease = acquireGitDiffWorkerPool();
      workerPool = workerLease.pool;
      cleanup.push(() => workerLease.release());
    }
    ready = true;
    return () => {
      ready = false;
      for (const dispose of cleanup) dispose();
      virtualizer = undefined;
      workerPool = undefined;
      prefetchController?.abort();
      pruneManagedPrefetches(true);
    };
  });

  $effect(() => {
    const authority = `${projectId}\u0000${snapshotId}`;
    if (authority !== observedAuthority) {
      observedAuthority = authority;
      mounted = {};
      lastPrefetchSignature = "";
      prefetchController?.abort();
      pruneManagedPrefetches(true);
    }
    const candidates = prefetchTargets
      .filter(({ change }) =>
        change.diff_available && change.area !== "conflict" && !change.binary && !change.submodule &&
        !change.conflict_code && !change.conflict &&
        (change.additions ?? 0) + (change.deletions ?? 0) <= 500
      )
      .filter((candidate) => !target || targetKey(candidate) !== targetKey(target))
      .slice(0, 25);
    const signature = `${projectId}:${snapshotId}:${candidates.map(targetKey).join("|")}`;
    if (!projectId || !snapshotId || !candidates.length || signature === lastPrefetchSignature || document.hidden) return;
    lastPrefetchSignature = signature;
    prefetchController?.abort();
    const controller = new AbortController();
    prefetchController = controller;
    void prefetchWithinBudget(candidates, controller, projectId, snapshotId);
  });

</script>

<section bind:this={shell} class="flex h-full min-h-0 min-w-0 flex-col bg-surface" aria-label={t("changes.diffPane")}>
  <div class="flex min-h-11 shrink-0 items-center justify-between gap-2 border-b border-border bg-surface px-3">
    <div class="inline-flex items-center rounded-sm border border-border bg-surface-subtle p-0.5" aria-label={t("changes.diffMode")}>
      <button type="button" class="inline-flex min-h-7 cursor-pointer items-center gap-1 rounded-[6px] border-0 px-2 text-[10px] font-semibold {mode === 'single' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}" aria-pressed={mode === "single"} onclick={() => (mode = "single")}><SquareSplitVertical size={12} />{t("changes.single")}</button>
      <button type="button" class="inline-flex min-h-7 cursor-pointer items-center gap-1 rounded-[6px] border-0 px-2 text-[10px] font-semibold {mode === 'stacked' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}" aria-pressed={mode === "stacked"} onclick={() => (mode = "stacked")}><Files size={12} />{t("changes.stacked")}</button>
    </div>
    {#if mode === "stacked" && stackTotal > 0}
      <span class="min-w-0 flex-1 truncate text-center text-[10px] tabular-nums text-muted">{t("changes.stackShowing", { shown: stackTargets.length, total: stackTotal })}</span>
    {:else}
      <span class="min-w-0 flex-1 truncate text-center text-[10px] text-muted">{layoutOverride ? t("changes.manualLayout") : t("changes.autoLayout")}</span>
    {/if}
    <div class="inline-flex items-center rounded-sm border border-border bg-surface-subtle p-0.5" aria-label={t("changes.diffLayout")}>
      <button type="button" class="inline-grid size-7 cursor-pointer place-items-center rounded-[6px] border-0 {layoutOverride === null ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}" title={t("changes.autoLayout")} aria-label={t("changes.autoLayout")} aria-pressed={layoutOverride === null} onclick={() => (layoutOverride = null)}><Wand2 size={13} /></button>
      <button type="button" class="inline-grid size-7 cursor-pointer place-items-center rounded-[6px] border-0 {layout === 'unified' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}" title={t("changes.unified")} aria-label={t("changes.unified")} aria-pressed={layout === "unified"} onclick={() => (layoutOverride = "unified")}><Rows3 size={13} /></button>
      <button type="button" class="inline-grid size-7 cursor-pointer place-items-center rounded-[6px] border-0 {layout === 'split' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}" title={t("changes.split")} aria-label={t("changes.split")} aria-pressed={layout === "split"} onclick={() => (layoutOverride = "split")}><Columns2 size={13} /></button>
    </div>
  </div>

  <div bind:this={scroller} class="min-h-0 flex-1 overflow-auto overscroll-contain bg-canvas">
    <div bind:this={content} class="grid min-w-0 gap-3 p-3">
      {#if !target}
        <section class="grid min-h-[360px] place-content-center gap-2 px-6 text-center">
          <SquareSplitVertical size={30} class="mx-auto text-muted" />
          <h2 class="m-0 text-sm font-semibold">{t("changes.selectChange")}</h2>
          <p class="m-0 max-w-[440px] text-xs leading-relaxed text-secondary">{t("changes.selectChangeDesc")}</p>
        </section>
      {:else if mode === "single"}
        {#if ready}
          {#key targetKey(target)}
            <GitDiffCard {projectId} {snapshotId} {target} {layout} {themeType} {virtualizer} {workerPool} {onSnapshotError} />
          {/key}
        {/if}
      {:else}
        {#each stackTargets as item (targetKey(item))}
          {@const key = targetKey(item)}
          <div class="min-h-[180px] scroll-mt-[300px]" use:observeLazy={key}>
            {#if mounted[key] && ready}
              <GitDiffCard projectId={projectId} snapshotId={snapshotId} target={item} {layout} {themeType} {virtualizer} {workerPool} {onSnapshotError} />
            {:else}
              <div class="grid min-h-[180px] place-content-center rounded-md border border-border bg-surface text-xs text-muted">{t("changes.lazyDiff")}</div>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  </div>
</section>
