<script lang="ts">
  import { onMount } from "svelte";
  import {
    AlertTriangle, ChevronDown, ChevronRight, CircleDot, File as FileIcon,
    FileQuestion, Folder, FolderOpen, GitBranch, GitCommitHorizontal, GitMerge,
    LoaderCircle, MoreHorizontal, RefreshCw
  } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import {
    computeVirtualWindow, isPartialRepository, isRepositoryDirty, repositoryAreaCount,
    repositoryBranch, repositoryDirtyCount, repositoryOperation, safeGitPathDisplay,
    safeRepositoryRootDisplay, type GitChange, type GitChangeArea, type GitTreeRow
  } from "../lib/gitChanges";

  let {
    rows,
    selectedKey = "",
    onToggle,
    onSelect,
    onLoadMore,
    onRetry
  }: {
    rows: GitTreeRow[];
    selectedKey?: string;
    onToggle: (row: GitTreeRow) => void;
    onSelect: (repositoryId: string, change: GitChange) => void;
    onLoadMore: (row: GitTreeRow) => void;
    onRetry: (row: GitTreeRow) => void;
  } = $props();

  const rowHeight = 34;
  const overscan = 12;
  let scroller = $state<HTMLElement>();
  let scrollTop = $state(0);
  let viewportHeight = $state(400);
  let activeKey = $state("");
  const virtualized = $derived(rows.length >= 1_000);
  const virtualWindow = $derived(computeVirtualWindow(rows.length, scrollTop, viewportHeight, rowHeight, overscan));
  const visibleRows = $derived(virtualized ? rows.slice(virtualWindow.start, virtualWindow.end) : rows);
  const visibleOffset = $derived(virtualized ? virtualWindow.start : 0);

  $effect(() => {
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

  function areaLabel(area?: GitChangeArea): string {
    if (area === "conflict") return t("changes.areaConflict");
    if (area === "staged") return t("changes.areaStaged");
    if (area === "unstaged") return t("changes.areaUnstaged");
    if (area === "untracked") return t("changes.areaUntracked");
    return area || t("changes.areaOther");
  }

  function statusLabel(status: string): string {
    const normalized = status.toLowerCase();
    if (["m", "modified", "modify", "type_changed"].includes(normalized)) return t("changes.statusModified");
    if (["a", "added", "add"].includes(normalized)) return t("changes.statusAdded");
    if (["d", "deleted", "delete"].includes(normalized)) return t("changes.statusDeleted");
    if (["r", "renamed", "rename"].includes(normalized)) return t("changes.statusRenamed");
    if (["c", "copied", "copy"].includes(normalized)) return t("changes.statusCopied");
    if (["u", "unmerged", "conflict", "conflicted"].includes(normalized)) return t("changes.statusConflicted");
    if (["?", "untracked"].includes(normalized)) return t("changes.statusUntracked");
    return status || t("changes.statusUnknown");
  }

  function statusCode(status: string, area?: GitChangeArea): string {
    if (area === "untracked") return "?";
    const normalized = status.toLowerCase();
    if (normalized === "type_changed") return "T";
    if (["modified", "modify"].includes(normalized)) return "M";
    if (["added", "add"].includes(normalized)) return "A";
    if (["deleted", "delete"].includes(normalized)) return "D";
    if (["renamed", "rename"].includes(normalized)) return "R";
    if (["copied", "copy"].includes(normalized)) return "C";
    if (["unmerged", "conflict", "conflicted"].includes(normalized)) return "U";
    if (["untracked"].includes(normalized)) return "?";
    return status.slice(0, 2).toUpperCase() || "?";
  }

  function changeLabel(row: GitTreeRow): string {
    if (row.label) return row.label;
    const display = safeGitPathDisplay(row.change?.path.display ?? "");
    return display.split("/").at(-1) || display;
  }

  function changeTitle(change: GitChange): string {
    const current = safeGitPathDisplay(change.path.display);
    const old = safeGitPathDisplay(change.old_path?.display ?? "");
    const rename = old && old !== current ? `${old} → ${current}` : current;
    const stats = `${change.additions ?? 0}+ / ${change.deletions ?? 0}-`;
    const status = change.area === "untracked" ? t("changes.statusUntracked") : statusLabel(change.status);
    return `${rename} · ${status} · ${stats}`;
  }

  function repositoryTitle(row: GitTreeRow): string {
    const repository = row.repository;
    if (!repository) return "";
    const branch = repositoryBranch(repository);
    const root = safeRepositoryRootDisplay(repository.root_display);
    const operation = repositoryOperation(repository);
    return [repository.label, root, repositoryStateLabel(row), branch.name, operation, worktreeLabel(effectiveWorktreeKind(row))]
      .filter(Boolean).join(" · ");
  }

  function stateKind(row: GitTreeRow): string {
    const state = row.repository?.state;
    return typeof state === "string" ? state : state?.kind ?? "";
  }

  function repositoryStateLabel(row: GitTreeRow): string {
    const state = stateKind(row);
    if (state === "clean") return t("changes.repoClean");
    if (state === "dirty") return t("changes.repoDirty");
    if (state === "conflicted") return t("changes.repoConflicted");
    if (state === "unavailable") return t("changes.repoUnavailable");
    return state;
  }

  function worktreeLabel(kind: string): string {
    if (["main", "primary"].includes(kind)) return t("changes.worktreeMain");
    if (["linked", "worktree"].includes(kind)) return t("changes.worktreeLinked");
    if (kind === "bare") return t("changes.worktreeBare");
    return kind;
  }

  function effectiveWorktreeKind(row: GitTreeRow): string {
    const repository = row.repository;
    if (!repository) return "";
    if (repository.worktree_kind) return repository.worktree_kind;
    if (repository.worktree?.linked) return "linked";
    if (repository.worktree?.main) return "main";
    return "";
  }

  function invoke(row: GitTreeRow) {
    activeKey = row.key;
    if (row.type === "repository" || row.type === "area" || row.type === "directory") {
      onToggle(row);
    } else if (row.type === "change" && row.change) {
      onSelect(row.repositoryId, row.change);
    } else if (row.type === "load-more" || row.type === "load-prefix") {
      onLoadMore(row);
    } else if (row.type === "error") {
      onRetry(row);
    }
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

  function handleKeydown(event: KeyboardEvent) {
    if (!rows.length) return;
    const index = Math.max(0, rows.findIndex((row) => row.key === activeKey));
    const row = rows[index];
    if (event.key === "ArrowDown") {
      event.preventDefault();
      focusIndex(index + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      focusIndex(index - 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      focusIndex(0);
    } else if (event.key === "End") {
      event.preventDefault();
      focusIndex(rows.length - 1);
    } else if (event.key === "ArrowRight" && row?.expandable) {
      event.preventDefault();
      if (!row.expanded) onToggle(row);
      else focusIndex(index + 1);
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      if (row?.expandable && row.expanded) onToggle(row);
      else if (row?.parentKey) {
        const parentIndex = rows.findIndex((candidate) => candidate.key === row.parentKey);
        if (parentIndex >= 0) focusIndex(parentIndex);
      }
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (row) invoke(row);
    }
  }
</script>

{#snippet treeRow(row: GitTreeRow, absoluteIndex: number)}
  <button
    type="button"
    role="treeitem"
    aria-level={row.level}
    aria-expanded={row.expandable ? row.expanded : undefined}
    aria-selected={row.type === "change" ? row.key === selectedKey : undefined}
    tabindex={row.key === activeKey ? 0 : -1}
    data-row-key={row.key}
    data-row-index={absoluteIndex}
    class="group flex h-[34px] w-full min-w-0 shrink-0 cursor-pointer items-center gap-1.5 border-0 border-b border-border/45 pr-2 text-left text-xs transition-colors hover:bg-surface-hover {row.key === selectedKey ? 'bg-accent-subtle text-text shadow-[inset_3px_0_var(--color-accent)]' : 'bg-transparent text-secondary'} {row.type === 'warning' || row.type === 'error' ? 'text-warning' : ''}"
    style={`padding-left:${8 + Math.max(0, row.level - 1) * 14}px`}
    title={row.type === "repository" ? repositoryTitle(row) : row.change ? changeTitle(row.change) : row.warning}
    onclick={() => invoke(row)}
    onfocus={() => (activeKey = row.key)}
  >
    {#if row.expandable}
      {#if row.expanded}<ChevronDown size={13} class="shrink-0 text-muted" />{:else}<ChevronRight size={13} class="shrink-0 text-muted" />{/if}
    {:else}
      <span class="w-[13px] shrink-0"></span>
    {/if}

    {#if row.type === "repository" && row.repository}
      {@const branch = repositoryBranch(row.repository)}
      {@const operation = repositoryOperation(row.repository)}
      <GitBranch size={14} class="shrink-0 {isRepositoryDirty(row.repository) ? 'text-accent' : 'text-muted'}" />
      <strong class="min-w-0 flex-1 truncate text-text">{row.repository.label}</strong>
      {#if safeRepositoryRootDisplay(row.repository.root_display) && safeRepositoryRootDisplay(row.repository.root_display) !== row.repository.label}
        <span class="max-w-[72px] truncate text-[10px] text-muted">{safeRepositoryRootDisplay(row.repository.root_display)}</span>
      {/if}
      {#if repositoryStateLabel(row)}
        <span class="max-w-[58px] truncate rounded-sm px-1 py-0.5 text-[9px] font-semibold {stateKind(row) === 'clean' ? 'bg-success-subtle text-success' : stateKind(row) === 'conflicted' || stateKind(row) === 'unavailable' ? 'bg-danger-subtle text-danger' : 'bg-accent-subtle text-accent'}">{repositoryStateLabel(row)}</span>
      {/if}
      {#if branch.unborn}
        <span class="rounded-sm bg-warning-subtle px-1.5 py-0.5 text-[9px] font-semibold text-warning">{t("changes.unborn")}</span>
      {:else if branch.detached}
        <span class="inline-flex items-center gap-0.5 text-[10px] text-warning"><GitCommitHorizontal size={11} />{t("changes.detached")}{#if typeof row.repository.branch === "object" && row.repository.branch?.oid}<span class="font-mono">@{row.repository.branch.oid.slice(0, 7)}</span>{/if}</span>
      {:else if branch.name}
        <span class="max-w-[72px] truncate font-mono text-[10px] text-muted">{branch.name}</span>
      {/if}
      {#if isPartialRepository(row.repository)}
        <span class="rounded-sm border border-accent/30 px-1 py-0.5 text-[9px] font-semibold text-accent" title={t("changes.partialExactTitle")}>{t("changes.partialExact")}</span>
      {/if}
      {#if effectiveWorktreeKind(row) && effectiveWorktreeKind(row) !== "main" && effectiveWorktreeKind(row) !== "primary"}
        <span class="max-w-[62px] truncate rounded-sm border border-border px-1 py-0.5 text-[9px] text-muted">{worktreeLabel(effectiveWorktreeKind(row))}</span>
      {/if}
      {#if operation}
        <span class="inline-flex items-center gap-0.5 rounded-sm bg-warning-subtle px-1 py-0.5 text-[9px] text-warning"><GitMerge size={10} />{operation}</span>
      {/if}
       {#if row.countsAuthoritative === false}
         <span class="inline-flex shrink-0 items-center gap-0.5 tabular-nums text-[10px] font-semibold text-warning" title={t("changes.resultsTruncated")}><AlertTriangle size={10} />~{repositoryDirtyCount(row.repository)}</span>
       {:else}
         <span class="shrink-0 tabular-nums text-[10px] font-semibold {isRepositoryDirty(row.repository) ? 'text-accent' : 'text-muted'}">{repositoryDirtyCount(row.repository)}</span>
       {/if}
      <span class="sr-only">{worktreeLabel(effectiveWorktreeKind(row))} · {stateKind(row)}{#if isPartialRepository(row.repository)} · {t("changes.partialExactTitle")}{/if}</span>
    {:else if row.type === "area"}
      <CircleDot size={12} class="shrink-0 {row.area === 'conflict' ? 'text-danger' : row.area === 'staged' ? 'text-success' : row.area === 'untracked' ? 'text-warning' : 'text-muted'}" />
      <span class="min-w-0 flex-1 truncate font-medium">{areaLabel(row.area)}</span>
      <span class="tabular-nums text-[10px] text-muted">{row.count ?? 0}</span>
    {:else if row.type === "directory"}
      {#if row.expanded}<FolderOpen size={14} class="shrink-0 text-warning" />{:else}<Folder size={14} class="shrink-0 text-warning" />{/if}
      <span class="min-w-0 flex-1 truncate">{changeLabel(row)}</span>
      {#if row.change}<span class="text-[9px] text-muted">{statusCode(row.change.status, row.change.area)}</span>{/if}
    {:else if row.type === "change" && row.change}
      {#if row.change.submodule}
        <GitCommitHorizontal size={13} class="shrink-0 text-warning" />
      {:else if row.change.binary}
        <FileQuestion size={13} class="shrink-0 text-muted" />
      {:else}
        <FileIcon size={13} class="shrink-0 text-muted" />
      {/if}
      <span class="inline-grid min-w-5 shrink-0 place-items-center rounded-sm px-1 py-0.5 text-[9px] font-bold {row.change.conflict_code || row.change.conflict || row.change.area === 'conflict' ? 'bg-danger-subtle text-danger' : row.change.area === 'staged' ? 'bg-success-subtle text-success' : 'bg-surface-subtle text-secondary'}">{statusCode(row.change.status, row.change.area)}</span>
      <span class="min-w-0 flex-1 truncate">{changeLabel(row)}</span>
      {#if row.change.old_path && safeGitPathDisplay(row.change.old_path.display) !== safeGitPathDisplay(row.change.path.display)}
        <span class="shrink-0 text-[9px] text-muted" title={safeGitPathDisplay(row.change.old_path.display)}>R{row.change.similarity ?? ""}</span>
      {/if}
      {#if typeof row.change.additions === "number" || typeof row.change.deletions === "number"}
        <span class="shrink-0 tabular-nums text-[9px]"><span class="text-success">+{row.change.additions ?? 0}</span> <span class="text-danger">−{row.change.deletions ?? 0}</span></span>
      {/if}
    {:else if row.type === "warning"}
      <AlertTriangle size={13} class="shrink-0" />
      <span class="min-w-0 flex-1 truncate">{row.warning || t("changes.resultsTruncated")}</span>
    {:else if row.type === "loading"}
      <LoaderCircle size={13} class="shrink-0 animate-spin text-muted" />
      <span class="min-w-0 flex-1 truncate text-muted">{t("changes.loadingChanges")}</span>
    {:else if row.type === "error"}
      <RefreshCw size={13} class="shrink-0" />
      <span class="min-w-0 flex-1 truncate">{t("changes.retryRepository")}</span>
    {:else if row.type === "load-prefix"}
      <MoreHorizontal size={13} class="shrink-0 text-accent" />
      <span class="min-w-0 flex-1 truncate font-semibold text-accent">{t("changes.loadToSearch")}</span>
    {:else if row.type === "load-more"}
      <MoreHorizontal size={13} class="shrink-0 text-accent" />
      <span class="min-w-0 flex-1 truncate font-semibold text-accent">{t("changes.loadMore")}</span>
    {:else if row.type === "empty"}
      <span class="min-w-0 flex-1 truncate text-muted">{t(row.partialSearch ? "changes.noMatchingLoadedChanges" : "changes.noMatchingChanges")}</span>
    {/if}
  </button>
{/snippet}

<div
  bind:this={scroller}
  class="h-full min-h-0 overflow-y-auto overflow-x-hidden overscroll-contain bg-surface-subtle"
  role="tree"
  aria-label={t("changes.treeLabel")}
  aria-multiselectable="false"
  tabindex={rows.length ? -1 : 0}
  onkeydown={handleKeydown}
  onscroll={(event) => (scrollTop = event.currentTarget.scrollTop)}
>
  {#if virtualized}
    <div class="relative" style={`height:${virtualWindow.totalSize}px`} data-virtualized="true">
      <div class="absolute inset-x-0" style={`transform:translateY(${virtualWindow.offset}px)`}>
        {#each visibleRows as row, offset (row.key)}
          {@render treeRow(row, visibleOffset + offset)}
        {/each}
      </div>
    </div>
  {:else}
    {#each visibleRows as row, index (row.key)}
      {@render treeRow(row, index)}
    {/each}
  {/if}
</div>
