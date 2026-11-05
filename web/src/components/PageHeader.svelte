<script lang="ts">
  import type { Snippet } from "svelte";

  let { title, description, actions }: {
    title: string;
    description: string;
    actions?: Snippet;
  } = $props();
</script>

<header class="flex min-h-[68px] w-full shrink-0 items-center justify-between gap-4 border-b border-border bg-surface px-5 py-3 max-md:px-4">
  <div class="min-w-0">
    <h1 class="truncate text-lg font-semibold leading-tight tracking-[-0.015em]">{title}</h1>
    <p class="mt-1 truncate text-xs leading-normal text-secondary" title={description}>{description}</p>
  </div>
  {#if actions}<div class="flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
</header>
