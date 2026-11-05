<script lang="ts">
  import { Button } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import {
    processFile, type FileDiffMetadata, type SupportedLanguages,
    type Virtualizer
  } from "@pierre/diffs";
  import type { WorkerPoolManager } from "@pierre/diffs/worker";
  import {
    AlertTriangle, ArrowRight, FileCode2, FileImage, GitCommitHorizontal,
    LoaderCircle, Maximize2, RefreshCw, ShieldAlert
  } from "lucide-svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import GitFileDiffRenderer from "./GitFileDiffRenderer.svelte";
  import StructuredDiffFallback from "./StructuredDiffFallback.svelte";
  import { t } from "../lib/i18n.svelte";
  import { localizedGitDiffMessage } from "../lib/localizedMessages";
  import {
    diffFallbackReason, diffImageSides, fetchGitDiff, gitDiffQueryKey,
    gitDiffLifecycleKey, isRecoverableGitSnapshotError, requiresLargeDiffConfirmation,
    safeDiffImageSource, safeGitPathDisplay,
    type GitDiffLayout, type GitDiffResponse, type GitDiffTarget
  } from "../lib/gitChanges";

  let {
    projectId,
    snapshotId,
    target,
    layout,
    themeType,
    virtualizer,
    workerPool,
    onSnapshotError
  }: {
    projectId: string;
    snapshotId: string;
    target: GitDiffTarget;
    layout: GitDiffLayout;
    themeType: "light" | "dark";
    virtualizer?: Virtualizer;
    workerPool?: WorkerPoolManager;
    onSnapshotError?: (repositoryId: string, error: unknown) => void;
  } = $props();

  let contextLines = $state(3);
  let allowLarge = $state(false);
  let confirmedLarge = $state(false);
  let pierreError = $state<unknown>();
  let observedTargetKey = "";
  let reportedSnapshotErrorKey = "";
  const targetKey = $derived(gitDiffLifecycleKey(target));

  $effect(() => {
    if (targetKey === observedTargetKey) return;
    observedTargetKey = targetKey;
    contextLines = 3;
    allowLarge = false;
    confirmedLarge = false;
    pierreError = undefined;
    reportedSnapshotErrorKey = "";
  });

  const diffQuery = createQuery<GitDiffResponse>(() => {
    // Cancellation notifies Query observers synchronously, before Svelte has
    // destroyed a card whose parent's selected target was just invalidated.
    // It must be safe to read options in that transient unselected state.
    const requestedTarget = target;
    const requestedProjectId = projectId;
    const requestedContext = contextLines;
    const requestedAllowLarge = allowLarge;
    return {
      queryKey: requestedTarget ? gitDiffQueryKey({
        projectId: requestedProjectId,
        snapshotId,
        repositoryId: requestedTarget.repository.id,
        repositoryGeneration: requestedTarget.repository.generation,
        changeId: requestedTarget.change.id,
        area: requestedTarget.change.area,
        pathToken: requestedTarget.change.path.token,
        context: requestedContext,
        allowLarge: requestedAllowLarge
      }) : ["git", "diff", requestedProjectId, "unselected"],
      enabled: Boolean(requestedTarget),
      queryFn: ({ signal }) => {
        if (!requestedTarget) throw new DOMException("Diff target superseded", "AbortError");
        return fetchGitDiff({
          projectId: requestedProjectId,
          repositoryId: requestedTarget.repository.id,
          changeId: requestedTarget.change.id,
          context: requestedContext,
          allowLarge: requestedAllowLarge,
          signal
        });
      },
      staleTime: 30_000,
      retry: false
    };
  });

  const diff = $derived(diffQuery.data);
  const diffMessage = $derived(diff ? localizedGitDiffMessage(diff) : "");

  $effect(() => {
    const error = diffQuery.error;
    if (!error || !isRecoverableGitSnapshotError(error)) return;
    const key = targetKey;
    if (reportedSnapshotErrorKey === key) return;
    reportedSnapshotErrorKey = key;
    onSnapshotError?.(target.repository.id, error);
  });
  const kind = $derived((diff?.kind || "").toLowerCase().replaceAll("-", "_"));
  const isConflict = $derived(
    target.change.area === "conflict" || Boolean(target.change.conflict_code || target.change.conflict) ||
    kind === "conflict" || diff?.comparison === "conflict"
  );
  const isSubmodule = $derived(Boolean(target.change.submodule) || kind === "submodule");
  const isBinary = $derived(target.change.binary || kind === "binary");
  const isTooLarge = $derived(
    ["too_large", "hard_limit", "oversized", "image_too_large", "response_budget", "patch_too_large"].includes(kind) ||
    Boolean(diff?.truncation?.truncated)
  );
  const imageSides = $derived(diffImageSides(diff?.images));
  const beforeImage = $derived(safeDiffImageSource(imageSides.before));
  const afterImage = $derived(safeDiffImageSource(imageSides.after));
  const imageDiff = $derived(kind === "image" || Boolean(beforeImage || afterImage));
  const needsConfirmation = $derived(Boolean(diff && requiresLargeDiffConfirmation(diff, confirmedLarge)));
  const fallback = $derived(diff?.patch ? diffFallbackReason(diff.patch) : null);

  const pierreMetadata = $derived.by<FileDiffMetadata | undefined>(() => {
    if (!diff?.patch || fallback?.fallback || isConflict || isSubmodule || isBinary || isTooLarge || imageDiff) return undefined;
    try {
      const parsed = processFile(diff.patch, {
        cacheKey: `${snapshotId}:${target.repository.id}:${target.repository.generation}:${target.change.id}:${contextLines}:${allowLarge}`,
        throwOnError: true
      });
      if (!parsed) return undefined;
      parsed.name = safeGitPathDisplay(diff.path?.display || target.change.path.display);
      const previousName = safeGitPathDisplay(diff.old_path?.display || target.change.old_path?.display || "");
      if (previousName && previousName !== parsed.name) parsed.prevName = previousName;
      if (diff.language) parsed.lang = diff.language as SupportedLanguages;
      return parsed;
    } catch {
      return undefined;
    }
  });

  function confirmLargeDiff() {
    confirmedLarge = true;
    allowLarge = true;
  }

  function pathLabel(): string {
    return safeGitPathDisplay(diff?.path?.display || target.change.path.display);
  }

  function oldPathLabel(): string {
    return safeGitPathDisplay(diff?.old_path?.display || target.change.old_path?.display || "");
  }
</script>

<article class="overflow-hidden rounded-md border border-border bg-surface" data-diff-change-id={target.change.id} data-diff-target-key={targetKey}>
  <header class="flex min-h-11 items-center gap-2 border-b border-border bg-surface-subtle px-3">
    <FileCode2 size={14} class="shrink-0 text-muted" />
    <strong class="min-w-0 flex-1 truncate font-mono text-xs" title={pathLabel()}>{pathLabel()}</strong>
    {#if oldPathLabel() && oldPathLabel() !== pathLabel()}
      <span class="hidden min-w-0 items-center gap-1 text-[10px] text-muted lg:flex" title={`${oldPathLabel()} → ${pathLabel()}`}>
        <span class="max-w-[180px] truncate">{oldPathLabel()}</span><ArrowRight size={10} />
      </span>
    {/if}
    {#if typeof target.change.similarity === "number"}
      <span class="shrink-0 text-[10px] tabular-nums text-muted">{target.change.similarity}%</span>
    {/if}
    <span class="shrink-0 tabular-nums text-[10px]"><span class="text-success">+{diff?.stats.additions ?? target.change.additions ?? 0}</span> <span class="text-danger">−{diff?.stats.deletions ?? target.change.deletions ?? 0}</span></span>
    {#if diff?.patch && contextLines < 20 && !needsConfirmation && !isBinary && !isSubmodule && !isConflict}
      <Button
        type="button"
        controlSize="sm" class="shrink-0"
        title={t("changes.expandContextDesc")}
        onclick={() => (contextLines = 20)}
      >
        <Maximize2 size={11} />{t("changes.expandContext")}
      </Button>
    {/if}
  </header>

  {#if diffQuery.isLoading}
    <div class="flex min-h-36 items-center justify-center gap-2 text-xs text-muted"><LoaderCircle size={15} class="animate-spin" />{t("changes.loadingDiff")}</div>
  {:else if diffQuery.error}
    <div class="p-3"><ErrorNotice error={diffQuery.error} onRetry={() => diffQuery.refetch()} /></div>
  {:else if !diff}
    <div class="grid min-h-36 place-content-center text-xs text-muted">{t("changes.diffUnavailable")}</div>
  {:else if needsConfirmation}
    <section class="grid min-h-52 place-content-center gap-3 px-6 py-8 text-center">
      <ShieldAlert size={28} class="mx-auto text-warning" />
      <div>
        <h3 class="m-0 text-sm font-semibold">{t("changes.largeDiffTitle")}</h3>
        <p class="mx-auto mt-1 max-w-[520px] text-xs leading-relaxed text-secondary">{diffMessage || t("changes.largeDiffDesc")}</p>
      </div>
      <Button type="button" variant="primary" class="mx-auto" onclick={confirmLargeDiff}>
        <Maximize2 size={13} />{t("changes.loadLargeDiff")}
      </Button>
    </section>
  {:else if imageDiff}
    {#if diffMessage}<div class="flex items-center gap-2 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-xs text-warning"><AlertTriangle size={13} />{diffMessage}</div>{/if}
    <section class="grid grid-cols-2 gap-px bg-border max-lg:grid-cols-1" aria-label={t("changes.imagePreview")}>
      <figure class="m-0 grid min-h-52 place-content-center gap-2 bg-surface p-4">
        <figcaption class="text-center text-xs font-semibold text-muted">{t("changes.before")}</figcaption>
        {#if beforeImage}<img src={beforeImage} alt={imageSides.before?.alt || t("changes.before")} class="max-h-[55vh] max-w-full rounded-sm border border-border object-contain" />{:else}<span class="text-xs text-muted">{t("changes.imageUnavailable")}</span>{/if}
      </figure>
      <figure class="m-0 grid min-h-52 place-content-center gap-2 bg-surface p-4">
        <figcaption class="text-center text-xs font-semibold text-muted">{t("changes.after")}</figcaption>
        {#if afterImage}<img src={afterImage} alt={imageSides.after?.alt || t("changes.after")} class="max-h-[55vh] max-w-full rounded-sm border border-border object-contain" />{:else}<span class="text-xs text-muted">{t("changes.imageUnavailable")}</span>{/if}
      </figure>
    </section>
  {:else if isConflict}
    <section class="grid min-h-44 place-content-center gap-2 px-6 py-8 text-center"><GitCommitHorizontal size={25} class="mx-auto text-danger" /><h3 class="m-0 text-sm font-semibold">{t("changes.conflictTitle")}</h3><p class="m-0 max-w-[520px] text-xs leading-relaxed text-secondary">{diffMessage || t("changes.conflictDesc")}</p></section>
  {:else if isSubmodule}
    <section class="grid min-h-44 place-content-center gap-2 px-6 py-8 text-center"><GitCommitHorizontal size={25} class="mx-auto text-warning" /><h3 class="m-0 text-sm font-semibold">{t("changes.submoduleTitle")}</h3><p class="m-0 max-w-[520px] text-xs leading-relaxed text-secondary">{diffMessage || t("changes.submoduleDesc")}</p></section>
  {:else if isBinary}
    <section class="grid min-h-44 place-content-center gap-2 px-6 py-8 text-center"><FileImage size={25} class="mx-auto text-muted" /><h3 class="m-0 text-sm font-semibold">{t("changes.binaryTitle")}</h3><p class="m-0 max-w-[520px] text-xs leading-relaxed text-secondary">{diffMessage || t("changes.binaryDesc")}</p></section>
  {:else if isTooLarge || (!diff.patch && diff.truncated)}
    <section class="grid min-h-44 place-content-center gap-2 px-6 py-8 text-center"><AlertTriangle size={25} class="mx-auto text-warning" /><h3 class="m-0 text-sm font-semibold">{t("changes.hardLimitTitle")}</h3><p class="m-0 max-w-[520px] text-xs leading-relaxed text-secondary">{diffMessage || t("changes.hardLimitDesc")}</p><span class="text-[10px] tabular-nums text-muted">+{diff.stats.additions ?? 0} / −{diff.stats.deletions ?? 0}</span></section>
  {:else if !diff.patch}
    <section class="grid min-h-36 place-content-center gap-2 text-center"><AlertTriangle size={20} class="mx-auto text-muted" /><span class="text-xs text-muted">{diffMessage || t("changes.emptyPatch")}</span></section>
  {:else if fallback?.fallback}
    <StructuredDiffFallback patch={diff.patch} reason={fallback.reason} />
  {:else if !pierreMetadata || pierreError || !virtualizer}
    {#if pierreError}<div class="border-b border-warning/25 bg-warning-subtle px-3 py-2 text-xs text-warning">{t("changes.rendererFallback")}</div>{/if}
    <StructuredDiffFallback patch={diff.patch} reason={null} />
  {:else}
    {#if diff.truncated}
      <div class="flex items-center gap-2 border-b border-warning/25 bg-warning-subtle px-3 py-2 text-xs text-warning"><AlertTriangle size={13} />{diffMessage || t("changes.diffTruncated")}</div>
    {/if}
    <GitFileDiffRenderer
      metadata={pierreMetadata}
      {layout}
      {themeType}
      virtualizer={virtualizer}
      {workerPool}
      onFailure={(error) => (pierreError = error)}
    />
  {/if}
</article>
