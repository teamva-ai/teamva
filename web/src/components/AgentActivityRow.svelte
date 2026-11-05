<script lang="ts">
  import {
    BrainCircuit, ChevronDown, ChevronRight, CircleAlert, FileDiff, GitCompareArrows,
    LoaderCircle, Minimize2, RefreshCw, TerminalSquare
  } from "lucide-svelte";
  import { formatTime } from "../lib/helpers";
  import RichText from "./RichText.svelte";

  let {
    kind, label, text, detail, occurredAt, status = "", durationMs, actionLabel, onAction
  }: {
    kind: "reasoning" | "tool" | "change" | "retry" | "error" | "compaction";
    label: string;
    text: string;
    detail?: string;
    occurredAt: string;
    status?: string;
    durationMs?: number;
    actionLabel?: string;
    onAction?: () => void;
  } = $props();

  let expanded = $state(false);
  let userToggled = $state(false);
  const expandable = $derived(kind === "reasoning" ? Boolean(text.trim()) : Boolean(detail?.trim()));
  const running = $derived(["pending", "running", "inProgress", "in_progress", "started"].includes(status));
  const failed = $derived(["failed", "error", "denied"].includes(status));
  const animateIn = $derived(Date.now() - Date.parse(occurredAt) < 15_000);

  $effect(() => {
    if (kind !== "reasoning" || userToggled) return;
    expanded = running;
  });

  function preview(value: string): string {
    const flattened = value
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/[*_#>`]/g, "")
      .replace(/\s+/g, " ")
      .trim();
    return flattened.length > 86 ? `${flattened.slice(0, 83).trimEnd()}…` : flattened;
  }

  function duration(value: number): string {
    if (value < 1_000) return `${Math.max(0.1, value / 1_000).toFixed(1)}s`;
    const seconds = value / 1_000;
    if (seconds < 60) return `${seconds.toFixed(1)}s`;
    return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
  }

  function toggle() {
    userToggled = true;
    expanded = !expanded;
  }
</script>

<div class="group/activity min-w-0 max-w-full overflow-hidden {animateIn ? 'activity-row-enter' : ''}" data-activity-kind={kind} data-activity-status={status}>
  <div class="flex min-w-0 items-center gap-1">
  <button
    type="button"
    disabled={!expandable}
    class="flex min-h-7 min-w-0 flex-1 items-center gap-1.5 rounded-lg border-0 bg-transparent px-0.5 py-1 text-left text-2xs transition-colors hover:bg-surface-hover/55 disabled:cursor-default disabled:opacity-100 disabled:hover:bg-transparent"
    aria-expanded={expandable ? expanded : undefined}
    onclick={toggle}
  >
    <span class="relative grid size-4 shrink-0 place-items-center text-muted">
      {#if expandable}
        <span class="absolute inset-0 grid place-items-center transition-opacity {expanded ? 'opacity-100' : 'opacity-0 group-hover/activity:opacity-100'}">
          {#if expanded}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
        </span>
        <span class="absolute inset-0 grid place-items-center transition-opacity {expanded ? 'opacity-0' : 'opacity-100 group-hover/activity:opacity-0'}">
          {#if failed}<CircleAlert size={13} class="text-danger" />
          {:else if running}<LoaderCircle size={13} class="animate-spin" />
          {:else if kind === "reasoning"}<BrainCircuit size={13} />
          {:else if kind === "tool"}<TerminalSquare size={13} />
          {:else if kind === "change"}<FileDiff size={13} />
          {:else if kind === "retry"}<RefreshCw size={13} />
          {:else if kind === "compaction"}<Minimize2 size={13} />
          {:else}<CircleAlert size={13} />{/if}
        </span>
      {:else if running}
        <LoaderCircle size={13} class="animate-spin" />
      {:else if failed}<CircleAlert size={13} class="text-danger" />
      {:else if kind === "reasoning"}<BrainCircuit size={13} />
      {:else if kind === "tool"}<TerminalSquare size={13} />
      {:else if kind === "change"}<FileDiff size={13} />
      {:else if kind === "retry"}<RefreshCw size={13} />
      {:else if kind === "compaction"}<Minimize2 size={13} />
      {:else}<CircleAlert size={13} />{/if}
    </span>
    <strong class="shrink-0 font-medium {kind === 'error' || failed ? 'text-danger' : kind === 'retry' ? 'text-secondary' : 'text-text'}">{label}</strong>
    {#if durationMs !== undefined}<span class="shrink-0 tabular-nums text-muted">{duration(durationMs)}</span>{/if}
    {#if text}
      <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-muted" title={text}>{kind === "reasoning" ? preview(text) : text}</span>
    {:else}<span class="min-w-0 flex-1"></span>{/if}
    <time class="shrink-0 tabular-nums text-muted/75 opacity-0 transition-opacity group-hover/activity:opacity-100">{formatTime(occurredAt)}</time>
  </button>
  {#if actionLabel && onAction}
    <button type="button" class="inline-flex min-h-7 shrink-0 cursor-pointer items-center gap-1 rounded-sm border-0 bg-transparent px-1.5 text-[10px] font-semibold text-accent hover:bg-accent-subtle" onclick={onAction}>
      <GitCompareArrows size={11} />{actionLabel}
    </button>
  {/if}
  </div>

  {#if expandable}
    <div
      class="grid transition-[grid-template-rows,opacity] duration-200 ease-out"
      style:grid-template-rows={expanded ? "1fr" : "0fr"}
      style:opacity={expanded ? 1 : 0}
    >
      <div class="min-h-0 overflow-hidden">
        <div class="ml-5 min-w-0 max-w-full overflow-hidden border-l border-border px-3 pb-2 pt-1">
          {#if kind === "reasoning"}
            <RichText {text} compact />
          {:else if expanded}
            <pre class="m-0 max-h-64 max-w-full overflow-x-hidden overflow-y-auto whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-lg bg-surface-subtle px-3 py-2 font-mono text-[11px] leading-[1.6] text-code">{detail}</pre>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>
