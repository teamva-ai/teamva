<script lang="ts">
  import { onMount } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import {
    computeVirtualWindow, createStructuredPatchIndex, structuredPatchLineCount,
    structurePatchWindow
  } from "../lib/gitChanges";

  let { patch, reason }: { patch: string; reason: "bytes" | "line" | null } = $props();

  const rowHeight = 20;
  const overscan = 24;
  const lineIndex = $derived(createStructuredPatchIndex(patch));
  const lineCount = $derived(structuredPatchLineCount(lineIndex));
  let scroller = $state<HTMLElement>();
  let scrollTop = $state(0);
  let viewportHeight = $state(480);
  const windowRange = $derived(computeVirtualWindow(lineCount, scrollTop, viewportHeight, rowHeight, overscan));
  const visibleLines = $derived(structurePatchWindow(patch, lineIndex, windowRange.start, windowRange.end));

  onMount(() => {
    if (!scroller) return;
    viewportHeight = scroller.clientHeight || 480;
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => {
      viewportHeight = entry?.contentRect.height || scroller?.clientHeight || 480;
    });
    observer.observe(scroller);
    return () => observer.disconnect();
  });
</script>

<section class="overflow-hidden border-t border-border bg-surface" aria-label={t("changes.structuredFallbackTitle")}>
  <div class="flex items-start gap-2 border-b border-border bg-warning-subtle px-3 py-2 text-xs text-warning">
    <strong class="shrink-0">{t("changes.structuredFallbackTitle")}</strong>
    <span>{reason === "line" ? t("changes.longLineFallback") : t("changes.structuredFallbackDesc")}</span>
  </div>
  <div
    bind:this={scroller}
    class="h-[min(68vh,720px)] overflow-auto overscroll-contain bg-surface font-mono text-[12px] leading-5"
    role="region"
    aria-label={t("changes.structuredFallbackTitle")}
    tabindex="-1"
    onscroll={(event) => (scrollTop = event.currentTarget.scrollTop)}
  >
    <div class="relative min-w-max" style={`height:${windowRange.totalSize}px`}>
      <div class="absolute inset-x-0" style={`transform:translateY(${windowRange.offset}px)`}>
        {#each visibleLines as line, offset (`${windowRange.start + offset}:${line.text}`)}
          <div
            class="grid h-5 grid-cols-[48px_48px_minmax(max-content,1fr)] border-b border-border/30 {line.kind === 'addition' ? 'bg-success-subtle/55' : line.kind === 'deletion' ? 'bg-danger-subtle/55' : line.kind === 'hunk' ? 'bg-surface-subtle text-accent' : ''}"
            data-line-index={windowRange.start + offset}
          >
            <span class="select-none border-r border-border/50 px-2 text-right tabular-nums text-muted">{line.oldNumber ?? ""}</span>
            <span class="select-none border-r border-border/50 px-2 text-right tabular-nums text-muted">{line.newNumber ?? ""}</span>
            <code class="block whitespace-pre px-2 text-code">{line.text || " "}</code>
          </div>
        {/each}
      </div>
    </div>
  </div>
</section>
