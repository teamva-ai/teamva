<script lang="ts">
  import { AlertCircle } from "lucide-svelte";
  import type { Snippet } from "svelte";

  let { title, description, action }: {
    title: string;
    description: string;
    action?: Snippet;
  } = $props();
</script>

<div class="flex min-h-[180px] flex-col items-center justify-center gap-2 px-8 py-8 text-center text-secondary">
  <AlertCircle size={22} aria-hidden="true" />
  <strong class="text-md text-text">{title}</strong>
  <p class="mb-2 max-w-[480px] text-xs leading-[1.5]">{description}</p>
  {#if action}{@render action()}{/if}
</div>
