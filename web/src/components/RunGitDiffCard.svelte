<script lang="ts">
  import type { Virtualizer } from "@pierre/diffs";
  import type { WorkerPoolManager } from "@pierre/diffs/worker";
  import {
    AlertTriangle, Bot, FileCode2, FileQuestion, GitCommitHorizontal, History
  } from "lucide-svelte";
  import type { GitDiffLayout } from "../lib/gitChanges";
  import { t } from "../lib/i18n.svelte";
  import {
    acquireRunGitTargetMetadata, runGitDiffLifecycleKey, type RunGitFileTarget
  } from "../lib/runGitChanges";
  import GitFileDiffRenderer from "./GitFileDiffRenderer.svelte";

  let {
    target, layout, themeType, virtualizer, workerPool
  }: {
    target: RunGitFileTarget;
    layout: GitDiffLayout;
    themeType: "light" | "dark";
    virtualizer?: Virtualizer;
    workerPool?: WorkerPoolManager;
  } = $props();

  let rendererError = $state<unknown>();
  let metadata = $state<ReturnType<typeof acquireRunGitTargetMetadata>["metadata"]>();
  let metadataLimited = $state(false);
  let observedTargetKey = "";
  const targetKey = $derived(runGitDiffLifecycleKey(target));

  $effect(() => {
    if (targetKey === observedTargetKey) return;
    observedTargetKey = targetKey;
    rendererError = undefined;
    const lease = acquireRunGitTargetMetadata(target);
    metadata = lease.metadata;
    metadataLimited = lease.limited;
    return () => {
      lease.release();
      metadata = undefined;
      metadataLimited = false;
    };
  });
  const additions = $derived(metadata?.hunks.reduce((total, hunk) => total + hunk.additionLines, 0) ?? 0);
  const deletions = $derived(metadata?.hunks.reduce((total, hunk) => total + hunk.deletionLines, 0) ?? 0);

  function placeholderTitle(): string {
    if (metadataLimited || target.placeholder === "budget") return t("runChanges.derivedLimitTitle");
    if (target.placeholder === "binary") return t("runChanges.binaryTitle");
    if (target.placeholder === "conflict") return t("runChanges.conflictTitle");
    if (target.placeholder === "untracked") return t("runChanges.untrackedTitle");
    if (target.placeholder === "truncated") return t("runChanges.truncatedTitle");
    return t("runChanges.noPatchTitle");
  }

  function placeholderDescription(): string {
    if (metadataLimited || target.placeholder === "budget") return t("runChanges.derivedLimitDesc");
    if (target.placeholder === "binary") return t("runChanges.binaryDesc");
    if (target.placeholder === "conflict") return t("runChanges.conflictDesc");
    if (target.placeholder === "untracked") return t("runChanges.untrackedDesc");
    if (target.placeholder === "truncated") return t("runChanges.truncatedDesc");
    return target.source === "provider" ? t("runChanges.providerNoPatch") : t("runChanges.observationNoPatch");
  }
</script>

<article class="overflow-hidden rounded-md border border-border bg-surface" data-run-diff-id={target.id} data-run-diff-target-key={targetKey} data-metadata-mounted={Boolean(metadata)}>
  <header class="flex min-h-11 items-center gap-2 border-b border-border bg-surface-subtle px-3">
    <FileCode2 size={14} class="shrink-0 text-muted" />
    <strong class="min-w-0 flex-1 truncate font-mono text-xs" title={target.path}>{target.path}</strong>
    <span class="inline-flex shrink-0 items-center gap-1 rounded-sm px-1.5 py-0.5 text-[9px] font-semibold {target.source === 'provider' ? 'bg-accent-subtle text-accent' : 'bg-warning-subtle text-warning'}">
      {#if target.source === "provider"}<Bot size={10} />{t("runChanges.agentReported")}{:else}<History size={10} />{target.phase === "start" ? t("runChanges.startObservation") : t("runChanges.endObservation")}{/if}
    </span>
    {#if metadata}<span class="shrink-0 tabular-nums text-[10px]"><span class="text-success">+{additions}</span> <span class="text-danger">−{deletions}</span></span>{/if}
  </header>

  {#if target.truncated || metadataLimited}
    <div class="flex items-center gap-2 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-[10px] text-warning"><AlertTriangle size={12} />{t("runChanges.truncatedDesc")}</div>
  {/if}
  {#if metadata && virtualizer && !rendererError}
    <GitFileDiffRenderer
      {metadata}
      {layout}
      {themeType}
      virtualizer={virtualizer}
      {workerPool}
      onFailure={(error) => (rendererError = error)}
    />
  {:else}
    <section class="grid min-h-40 place-content-center gap-2 px-6 py-8 text-center">
      {#if target.placeholder === "conflict"}<GitCommitHorizontal size={24} class="mx-auto text-danger" />{:else}<FileQuestion size={24} class="mx-auto text-muted" />{/if}
      <h3 class="m-0 text-sm font-semibold">{rendererError ? t("runChanges.rendererUnavailable") : placeholderTitle()}</h3>
      <p class="m-0 max-w-[520px] text-xs leading-relaxed text-secondary">{rendererError ? t("runChanges.rendererUnavailableDesc") : placeholderDescription()}</p>
      {#if target.sizeBytes !== undefined}<span class="text-[10px] tabular-nums text-muted">{target.sizeBytes} B</span>{/if}
    </section>
  {/if}
</article>
