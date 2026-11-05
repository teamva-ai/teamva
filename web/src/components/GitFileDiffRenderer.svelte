<script lang="ts">
  import type { FileDiffMetadata, Virtualizer } from "@pierre/diffs";
  import type { WorkerPoolManager } from "@pierre/diffs/worker";
  import type { GitDiffLayout } from "../lib/gitChanges";
  import { mountGitFileDiff } from "../lib/gitDiffRenderer";

  let {
    metadata, layout, themeType, virtualizer, workerPool, onFailure
  }: {
    metadata: FileDiffMetadata;
    layout: GitDiffLayout;
    themeType: "light" | "dark";
    virtualizer: Virtualizer;
    workerPool?: WorkerPoolManager;
    onFailure?: (error: unknown) => void;
  } = $props();

  let host = $state<HTMLElement>();

  $effect(() => {
    if (!host || !metadata || !virtualizer) return;
    try {
      return mountGitFileDiff({ host, metadata, layout, themeType, virtualizer, workerPool });
    } catch (error) {
      host.replaceChildren();
      onFailure?.(error);
    }
  });
</script>

<div bind:this={host} class="git-pierre-diff min-w-0 overflow-hidden" data-layout={layout}></div>
