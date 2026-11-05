<script lang="ts">
  import { onMount } from "svelte";
  import {
    Bot, ChevronDown, ChevronRight, CircleDot, File as FileIcon,
    FileQuestion, Folder, FolderOpen, GitBranch, History
  } from "lucide-svelte";
  import { computeVirtualWindow } from "../lib/gitChanges";
  import { formatTime } from "../lib/helpers";
  import { t } from "../lib/i18n.svelte";
  import type { RunGitFileTarget, RunGitTreeRow } from "../lib/runGitChanges";

  let {
    rows, selectedId = "", onToggle, onSelect
  }: {
    rows: RunGitTreeRow[];
    selectedId?: string;
    onToggle: (row: RunGitTreeRow) => void;
    onSelect: (target: RunGitFileTarget) => void;
  } = $props();

  const rowHeight = 34;
  let scroller = $state<HTMLElement>();
  let scrollTop = $state(0);
  let viewportHeight = $state(400);
  let activeKey = $state("");
  const virtualized = $derived(rows.length >= 1_000);
  const windowed = $derived(computeVirtualWindow(rows.length, scrollTop, viewportHeight, rowHeight, 12));
  const visibleRows = $derived(virtualized ? rows.slice(windowed.start, windowed.end) : rows);
  const visibleOffset = $derived(virtualized ? windowed.start : 0);

  $effect(() => {
    const selectedKey = selectedId ? `file:${selectedId}` : "";
    if (selectedKey && rows.some((row) => row.key === selectedKey)) activeKey = selectedKey;
    else if (!rows.some((row) => row.key === activeKey)) activeKey = rows[0]?.key ?? "";
  });

  onMount(() => {
    if (!scroller) return;
    viewportHeight = scroller.clientHeight || 400;
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => {
      viewportHeight = entry?.contentRect.height || scroller?.clientHeight || 400;
    });
    observer.observe(scroller);
    return () => observer.disconnect();
  });

  function areaLabel(area = ""): string {
    if (area === "conflict") return t("runChanges.areaConflict");
    if (area === "staged") return t("runChanges.areaStaged");
    if (area === "unstaged") return t("runChanges.areaUnstaged");
    if (area === "untracked") return t("runChanges.areaUntracked");
    if (area === "reported") return t("runChanges.areaReported");
    return area;
  }

  function sourceLabel(row: RunGitTreeRow): string {
    const source = row.source === "provider"
      ? t("runChanges.agentReported")
      : row.phase === "start"
        ? t("runChanges.startObservation")
        : t("runChanges.endObservation");
    const label = t("runChanges.roundSource", { round: row.round ?? 1, source });
    return row.providerTurnId ? `${label} · ${row.providerTurnId}` : label;
  }

  function invoke(row: RunGitTreeRow) {
    activeKey = row.key;
    if (row.expandable) onToggle(row);
    else if (row.target) onSelect(row.target);
  }

  function focusIndex(index: number) {
    const bounded = Math.max(0, Math.min(rows.length - 1, index));
    const row = rows[bounded];
    if (!row || !scroller) return;
    activeKey = row.key;
    const top = bounded * rowHeight;
    if (top < scroller.scrollTop) scroller.scrollTo({ top });
    else if (top + rowHeight > scroller.scrollTop + scroller.clientHeight) {
      scroller.scrollTo({ top: top - scroller.clientHeight + rowHeight });
    }
    requestAnimationFrame(() => {
      scroller?.querySelector<HTMLElement>(`[data-row-key="${CSS.escape(row.key)}"]`)?.focus();
    });
  }

  function onKeydown(event: KeyboardEvent) {
    if (!rows.length) return;
    const index = Math.max(0, rows.findIndex((row) => row.key === activeKey));
    const row = rows[index];
    if (event.key === "ArrowDown") focusIndex(index + 1);
    else if (event.key === "ArrowUp") focusIndex(index - 1);
    else if (event.key === "Home") focusIndex(0);
    else if (event.key === "End") focusIndex(rows.length - 1);
    else if (event.key === "ArrowRight" && row?.expandable) {
      if (!row.expanded) onToggle(row);
      else focusIndex(index + 1);
    } else if (event.key === "ArrowLeft" && row?.expandable && row.expanded) onToggle(row);
    else if ((event.key === "Enter" || event.key === " ") && row) invoke(row);
    else return;
    event.preventDefault();
  }
</script>

{#snippet treeRow(row: RunGitTreeRow, absoluteIndex: number)}
  <button
    type="button"
    role="treeitem"
    aria-level={row.level}
    aria-expanded={row.expandable ? row.expanded : undefined}
    aria-selected={row.type === "file" ? row.target?.id === selectedId : undefined}
    tabindex={row.key === activeKey ? 0 : -1}
    data-row-key={row.key}
    data-row-index={absoluteIndex}
    class="group flex h-[34px] w-full min-w-0 cursor-pointer items-center gap-1.5 border-0 border-b border-border/45 bg-transparent pr-2 text-left text-xs text-secondary transition-colors hover:bg-surface-hover {row.target?.id === selectedId ? 'bg-accent-subtle text-text shadow-[inset_3px_0_var(--color-accent)]' : ''}"
    style={`padding-left:${8 + Math.max(0, row.level - 1) * 14}px`}
    title={row.target?.path || row.repository?.rootDisplay || row.label}
    onclick={() => invoke(row)}
    onfocus={() => (activeKey = row.key)}
  >
    {#if row.expandable}
      {#if row.expanded}<ChevronDown size={13} class="shrink-0 text-muted" />{:else}<ChevronRight size={13} class="shrink-0 text-muted" />{/if}
    {:else}<span class="w-[13px] shrink-0"></span>{/if}

    {#if row.type === "repository"}
      <GitBranch size={14} class="shrink-0 text-accent" />
      <strong class="min-w-0 flex-1 truncate text-text">{row.label}</strong>
      {#if row.repository?.rootDisplay}<span class="max-w-[78px] truncate text-[9px] text-muted">{row.repository.rootDisplay}</span>{/if}
      <span class="tabular-nums text-[10px] text-muted">{row.count ?? 0}</span>
    {:else if row.type === "source"}
      {#if row.source === "provider"}<Bot size={13} class="shrink-0 text-accent" />{:else}<History size={13} class="shrink-0 text-warning" />{/if}
      <span class="min-w-0 flex-1 truncate font-medium">{sourceLabel(row)}</span>
      {#if row.occurredAt}<time class="text-[9px] text-muted">{formatTime(row.occurredAt)}</time>{/if}
      <span class="tabular-nums text-[9px] text-muted">{row.count ?? 0}</span>
    {:else if row.type === "area"}
      <CircleDot size={12} class="shrink-0 {row.area === 'conflict' ? 'text-danger' : row.area === 'staged' ? 'text-success' : row.area === 'untracked' ? 'text-warning' : 'text-muted'}" />
      <span class="min-w-0 flex-1 truncate">{areaLabel(row.area)}</span>
      <span class="tabular-nums text-[9px] text-muted">{row.count ?? 0}</span>
    {:else if row.type === "directory"}
      {#if row.expanded}<FolderOpen size={14} class="shrink-0 text-warning" />{:else}<Folder size={14} class="shrink-0 text-warning" />{/if}
      <span class="min-w-0 flex-1 truncate">{row.label}</span>
      <span class="tabular-nums text-[9px] text-muted">{row.count ?? 0}</span>
    {:else if row.target}
      {#if row.target.binary || row.target.placeholder !== "none"}<FileQuestion size={13} class="shrink-0 text-muted" />{:else}<FileIcon size={13} class="shrink-0 text-muted" />{/if}
      <span class="inline-grid min-w-5 shrink-0 place-items-center rounded-sm px-1 py-0.5 text-[9px] font-bold {row.target.conflict ? 'bg-danger-subtle text-danger' : row.target.source === 'provider' ? 'bg-accent-subtle text-accent' : row.target.area === 'staged' ? 'bg-success-subtle text-success' : 'bg-surface-subtle text-secondary'}">
        {row.target.source === "provider" ? "A" : row.target.area === "untracked" ? "?" : row.target.area === "staged" ? "S" : row.target.conflict ? "U" : "M"}
      </span>
      <span class="min-w-0 flex-1 truncate">{row.label}</span>
    {/if}
  </button>
{/snippet}

<div
  bind:this={scroller}
  class="h-full min-h-0 overflow-y-auto overflow-x-hidden overscroll-contain bg-surface-subtle"
  role="tree"
  aria-label={t("runChanges.treeLabel")}
  tabindex={rows.length ? -1 : 0}
  onkeydown={onKeydown}
  onscroll={(event) => (scrollTop = event.currentTarget.scrollTop)}
>
  {#if virtualized}
    <div class="relative" style={`height:${windowed.totalSize}px`} data-virtualized="true">
      <div class="absolute inset-x-0" style={`transform:translateY(${windowed.offset}px)`}>
        {#each visibleRows as row, offset (row.key)}{@render treeRow(row, visibleOffset + offset)}{/each}
      </div>
    </div>
  {:else}
    {#each visibleRows as row, index (row.key)}{@render treeRow(row, index)}{/each}
  {/if}
</div>
