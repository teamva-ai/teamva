<script lang="ts">
  import { LoaderCircle } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";

  let { label, block = false }: { label?: string; block?: boolean } = $props();
  const text = $derived(label ?? (block ? t("common.loadingBlock") : t("common.loading")));
</script>

{#if block}
  <!-- 内容区加载：骨架屏（匹配页面真实结构），非居中 spinner -->
  <div class="mx-auto max-w-[1120px] py-6" role="status" aria-label={text}>
    <span class="sr-only">{text}</span>
    <div>
      <span class="block h-6 w-[38%] animate-pulse rounded-sm bg-surface-hover"></span>
      <span class="mt-2.5 block h-3.5 w-[26%] animate-pulse rounded-sm bg-surface-hover"></span>
    </div>
    <div class="mt-6 grid grid-cols-12 gap-3">
      <span class="col-span-6 h-24 animate-pulse rounded-md bg-surface shadow-raised max-sm:col-span-12"></span>
      <span class="col-span-3 h-24 animate-pulse rounded-md bg-surface shadow-raised max-sm:col-span-12"></span>
      <span class="col-span-3 h-24 animate-pulse rounded-md bg-surface shadow-raised max-sm:col-span-12"></span>
      <span class="col-span-6 h-24 animate-pulse rounded-md bg-surface shadow-raised max-sm:col-span-12"></span>
      <span class="col-span-6 h-24 animate-pulse rounded-md bg-surface shadow-raised max-sm:col-span-12"></span>
    </div>
    <div class="mt-3 h-[220px] animate-pulse rounded-md bg-surface p-5 shadow-raised">
      <span class="block h-3.5 w-[46%] rounded-sm bg-surface-hover"></span>
    </div>
  </div>
{:else}
  <div class="flex min-h-dvh flex-row items-center justify-center gap-2 py-8 text-sm text-secondary" role="status">
    <LoaderCircle class="animate-spin" size={18} aria-hidden="true" />
    {text}
  </div>
{/if}
