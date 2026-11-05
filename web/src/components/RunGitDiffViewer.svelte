<script lang="ts">
  import { Virtualizer } from "@pierre/diffs";
  import type { WorkerPoolManager } from "@pierre/diffs/worker";
  import { Columns2, Files, Rows3, SquareSplitVertical } from "lucide-svelte";
  import { onMount } from "svelte";
  import type { GitDiffLayout } from "../lib/gitChanges";
  import { selectStackedChanges } from "../lib/gitChanges";
  import { acquireGitDiffWorkerPool } from "../lib/gitDiffWorker";
  import { t } from "../lib/i18n.svelte";
  import { runGitDiffLifecycleKey, type RunGitFileTarget } from "../lib/runGitChanges";
  import RunGitDiffCard from "./RunGitDiffCard.svelte";

  let {
    target, targets = [], mode = $bindable<"single" | "stacked">("single")
  }: {
    target?: RunGitFileTarget;
    targets?: RunGitFileTarget[];
    mode?: "single" | "stacked";
  } = $props();

  let shell = $state<HTMLElement>();
  let scroller = $state<HTMLElement>();
  let content = $state<HTMLElement>();
  let width = $state(0);
  let layoutOverride = $state<GitDiffLayout | null>(null);
  let virtualizer = $state<Virtualizer>();
  let workerPool = $state<WorkerPoolManager>();
  let ready = $state(false);
  let mounted = $state<Record<string, boolean | undefined>>({});
  let lazyObserver: IntersectionObserver | undefined;
  const pending = new Map<Element, string>();
  let themeType = $state<"light" | "dark">(
    document.documentElement.dataset.theme === "dark" ? "dark" : "light"
  );
  const layout = $derived<GitDiffLayout>(layoutOverride ?? (width >= 1_100 ? "split" : "unified"));
  const selectedIndex = $derived(target ? targets.findIndex((item) => item.id === target.id) : -1);
  const stackTargets = $derived(selectStackedChanges(targets, selectedIndex));
  let observedTargetsAuthority = "";

  function targetKey(item: RunGitFileTarget): string {
    return runGitDiffLifecycleKey(item);
  }

  $effect(() => {
    const authority = `${targets.length}:${targets[0] ? targetKey(targets[0]) : ""}:${targets.at(-1) ? targetKey(targets.at(-1)!) : ""}`;
    if (authority === observedTargetsAuthority) return;
    const hadAuthority = Boolean(observedTargetsAuthority);
    observedTargetsAuthority = authority;
    // Do not erase fallback mounts performed by the initial `use:lazy` action
    // before this first effect. Subsequent authority changes must still drop
    // every old lifecycle key.
    if (hadAuthority) {
      mounted = {};
      if (typeof IntersectionObserver === "undefined") {
        queueMicrotask(() => {
          if (observedTargetsAuthority !== authority) return;
          for (const id of pending.values()) mountTarget(id);
        });
      }
    }
  });

  function mountTarget(id: string) {
    if (!mounted[id]) mounted = { ...mounted, [id]: true };
  }

  function lazy(node: Element, id: string) {
    pending.set(node, id);
    if (typeof IntersectionObserver === "undefined" || mounted[id]) mountTarget(id);
    else lazyObserver?.observe(node);
    return {
      update(next: string) {
        lazyObserver?.unobserve(node);
        pending.delete(node);
        id = next;
        pending.set(node, id);
        if (typeof IntersectionObserver === "undefined" || mounted[id]) mountTarget(id);
        else lazyObserver?.observe(node);
      },
      destroy() {
        lazyObserver?.unobserve(node);
        pending.delete(node);
      }
    };
  }

  onMount(() => {
    const cleanup: Array<() => void> = [];
    width = shell?.clientWidth || window.innerWidth;
    if (shell && typeof ResizeObserver !== "undefined") {
      const resize = new ResizeObserver(([entry]) => (width = entry?.contentRect.width || shell?.clientWidth || window.innerWidth));
      resize.observe(shell);
      cleanup.push(() => resize.disconnect());
    }
    const theme = new MutationObserver(() => {
      themeType = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
    });
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    cleanup.push(() => theme.disconnect());

    if (scroller && content && typeof ResizeObserver !== "undefined" && typeof IntersectionObserver !== "undefined") {
      const nextVirtualizer = new Virtualizer({ overscrollSize: 900, intersectionObserverMargin: 1_200 });
      nextVirtualizer.setup(scroller, content);
      virtualizer = nextVirtualizer;
      cleanup.push(() => nextVirtualizer.cleanUp());
      lazyObserver = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          const id = pending.get(entry.target);
          if (id) mountTarget(id);
          lazyObserver?.unobserve(entry.target);
        }
      }, { root: scroller, rootMargin: "300px 0px", threshold: 0 });
      for (const [node, id] of pending) if (!mounted[id]) lazyObserver.observe(node);
      cleanup.push(() => lazyObserver?.disconnect());
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
      lazyObserver = undefined;
    };
  });
</script>

<section bind:this={shell} class="flex h-full min-h-0 min-w-0 flex-col bg-surface" aria-label={t("runChanges.diffPane")}>
  <div class="flex min-h-11 shrink-0 items-center justify-between gap-2 border-b border-border bg-surface px-3">
    <div class="inline-flex items-center rounded-sm border border-border bg-surface-subtle p-0.5" aria-label={t("changes.diffMode")}>
      <button type="button" class="inline-flex min-h-7 cursor-pointer items-center gap-1 rounded-[6px] border-0 px-2 text-[10px] font-semibold {mode === 'single' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mode === "single"} onclick={() => (mode = "single")}><SquareSplitVertical size={12} />{t("changes.single")}</button>
      <button type="button" class="inline-flex min-h-7 cursor-pointer items-center gap-1 rounded-[6px] border-0 px-2 text-[10px] font-semibold {mode === 'stacked' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" aria-pressed={mode === "stacked"} onclick={() => (mode = "stacked")}><Files size={12} />{t("changes.stacked")}</button>
    </div>
    {#if mode === "stacked"}<span class="min-w-0 flex-1 truncate text-center text-[10px] text-muted">{t("changes.stackShowing", { shown: stackTargets.length, total: targets.length })}</span>{:else}<span class="flex-1"></span>{/if}
    <div class="inline-flex items-center rounded-sm border border-border bg-surface-subtle p-0.5" aria-label={t("changes.diffLayout")}>
      <button type="button" class="inline-grid size-7 cursor-pointer place-items-center rounded-[6px] border-0 {layout === 'unified' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" title={t("changes.unified")} aria-label={t("changes.unified")} aria-pressed={layout === "unified"} onclick={() => (layoutOverride = "unified")}><Rows3 size={13} /></button>
      <button type="button" class="inline-grid size-7 cursor-pointer place-items-center rounded-[6px] border-0 {layout === 'split' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted'}" title={t("changes.split")} aria-label={t("changes.split")} aria-pressed={layout === "split"} onclick={() => (layoutOverride = "split")}><Columns2 size={13} /></button>
    </div>
  </div>
  <div bind:this={scroller} class="min-h-0 flex-1 overflow-auto overscroll-contain bg-canvas">
    <div bind:this={content} class="grid min-w-0 gap-3 p-3">
      {#if !target}
        <section class="grid min-h-[280px] place-content-center gap-2 px-6 text-center"><SquareSplitVertical size={28} class="mx-auto text-muted" /><h3 class="m-0 text-sm font-semibold">{t("runChanges.selectFile")}</h3><p class="m-0 text-xs text-secondary">{t("runChanges.selectFileDesc")}</p></section>
      {:else if mode === "single"}
        {#if ready}
          {#key targetKey(target)}
            <RunGitDiffCard {target} {layout} {themeType} {virtualizer} {workerPool} />
          {/key}
        {/if}
      {:else}
        {#each stackTargets as item (targetKey(item))}
          {@const key = targetKey(item)}
          <div class="min-h-[180px]" use:lazy={key}>
            {#if mounted[key] && ready}<RunGitDiffCard target={item} {layout} {themeType} {virtualizer} {workerPool} />{:else}<div class="grid min-h-[180px] place-content-center rounded-md border border-border bg-surface text-xs text-muted">{t("changes.lazyDiff")}</div>{/if}
          </div>
        {/each}
      {/if}
    </div>
  </div>
</section>
