<script lang="ts">
  import { Input } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import {
    ArrowRight, Bot, FileText, LoaderCircle, MessageSquareText, Search, Sparkles, User, X
  } from "lucide-svelte";
  import { onMount, tick } from "svelte";
  import { api } from "../lib/api";
  import { formatTime } from "../lib/helpers";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { navigate } from "../lib/router.svelte";
  import { searchResultTarget } from "../lib/searchNavigation";
  import type { ProjectSearchPage, ProjectSearchResult } from "../lib/types";

  let { projectId, projectName = "" }: { projectId?: string; projectName?: string } = $props();
  let open = $state(false);
  let query = $state("");
  let debouncedQuery = $state("");
  let input = $state<HTMLInputElement>();
  let resultList = $state<HTMLDivElement>();
  let previouslyFocused: HTMLElement | null = null;
  let selectedIndex = $state(0);
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;

  const normalizedQuery = $derived(query.trim());
  type SearchResponse = ProjectSearchPage & { searchQuery: string; projectId: string };
  const queryReady = $derived(Boolean(normalizedQuery) && normalizedQuery === debouncedQuery.trim());
  const search = createQuery<SearchResponse>(() => ({
    queryKey: ["project-search", projectId, normalizedQuery],
    queryFn: async ({ signal, queryKey }) => {
      const targetProject = queryKey[1] as string;
      const targetQuery = queryKey[2] as string;
      const page = await api<ProjectSearchPage>(
        `/projects/${encodeURIComponent(targetProject)}/search?q=${encodeURIComponent(targetQuery)}&limit=30`,
        { signal }
      );
      return { ...page, searchQuery: targetQuery, projectId: targetProject };
    },
    enabled: open && Boolean(projectId) && queryReady,
    staleTime: 10_000,
    refetchOnWindowFocus: false
  }));
  const matchingResults = $derived(open && queryReady && !search.error &&
    search.data?.searchQuery === normalizedQuery && search.data?.projectId === projectId);
  const results = $derived(matchingResults ? search.data?.items ?? [] : []);

  // Cancel immediately on input/project changes, not after the debounce window.
  $effect(() => {
    if (!open || !projectId || !normalizedQuery) return;
    const key = ["project-search", projectId, normalizedQuery];
    return () => { void queryClient.cancelQueries({ queryKey: key, exact: true }); };
  });

  $effect(() => {
    clearTimeout(debounceTimer);
    const value = query;
    debounceTimer = setTimeout(() => {
      debouncedQuery = value;
      selectedIndex = 0;
    }, 180);
  });

  $effect(() => {
    if (!open) return;
    void tick().then(() => input?.focus());
  });

  $effect(() => {
    if (!open || !results.length) return;
    if (selectedIndex >= results.length) selectedIndex = results.length - 1;
  });

  $effect(() => {
    if (!open || !results.length) return;
    const index = selectedIndex;
    void tick().then(() => {
      const target = resultList?.querySelector<HTMLElement>(`[data-search-result-index="${index}"]`);
      if (typeof target?.scrollIntoView === "function") target.scrollIntoView({ block: "nearest" });
    });
  });

  onMount(() => {
    const handleShortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
        event.preventDefault();
        if (projectId) openSearch();
      }
    };
    document.addEventListener("keydown", handleShortcut);
    return () => {
      clearTimeout(debounceTimer);
      document.removeEventListener("keydown", handleShortcut);
    };
  });

  function openSearch() {
    if (!projectId) return;
    previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    open = true;
  }

  function closeSearch() {
    open = false;
    selectedIndex = 0;
    const target = previouslyFocused;
    previouslyFocused = null;
    void tick().then(() => {
      if (target?.isConnected) target.focus();
    });
  }

  function handleDialogKey(event: KeyboardEvent) {
    if (event.key !== "Tab") return;
    const dialog = event.currentTarget as HTMLElement;
    const focusable = dialog.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'
    );
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function choose(item: ProjectSearchResult) {
    if (!matchingResults || !results.some((result) => result.id === item.id)) return;
    closeSearch();
    navigate(searchResultTarget(item));
  }

  function resultLabel(item: ProjectSearchResult): string {
    switch (item.kind) {
      case "memory": return t("search.memory");
      case "issue_description": return t("search.issueDescription");
      case "issue_comment": return t("search.issueComment");
      default: return item.role === "user" ? t("search.userMessage") : t("search.agentMessage");
    }
  }

  function resultIcon(item: ProjectSearchResult) {
    if (item.kind === "memory") return FileText;
    if (item.kind === "session_message") return item.role === "user" ? User : Bot;
    return MessageSquareText;
  }

  function handleInputKey(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      closeSearch();
      return;
    }
    if (event.key === "Enter") event.preventDefault();
    if (!matchingResults || !results.length) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      selectedIndex = (selectedIndex + 1) % results.length;
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      selectedIndex = (selectedIndex - 1 + results.length) % results.length;
    } else if (event.key === "Enter") {
      event.preventDefault();
      choose(results[selectedIndex] ?? results[0]);
    }
  }
</script>

<button
  class="group flex h-8 min-w-0 w-full max-w-[440px] cursor-text items-center gap-2 rounded-sm border border-border bg-surface px-2.5 text-left text-xs text-muted transition-colors hover:border-border-strong hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-55 max-md:size-8 max-md:justify-center max-md:px-0"
  type="button"
  aria-label={t("search.open")}
  title={projectId ? t("search.shortcutHint") : t("search.selectProjectFirst")}
  disabled={!projectId}
  onclick={openSearch}
  style="--wails-draggable: no-drag"
>
  <Search size={14} class="shrink-0" />
  <span class="min-w-0 flex-1 truncate max-md:hidden">{t("search.placeholder")}</span>
  <kbd class="rounded border border-border bg-surface-subtle px-1.5 py-0.5 font-sans text-2xs text-muted max-md:hidden">⌘ K</kbd>
</button>

{#if open}
  <div class="fixed inset-0 z-[80] grid grid-cols-[minmax(0,1fr)] grid-rows-[minmax(0,1fr)] place-items-center bg-backdrop p-4 max-md:p-2" role="presentation">
    <button class="absolute inset-0 cursor-default border-0 bg-transparent p-0" type="button" aria-label={t("common.close")} onclick={closeSearch}></button>
    <div class="relative flex max-h-full w-full min-w-0 max-w-[720px] flex-col overflow-hidden rounded-lg border border-border bg-surface shadow-raised" role="dialog" aria-modal="true" aria-label={t("search.title")} tabindex="-1" onkeydown={handleDialogKey}>
      <header class="flex min-h-14 items-center gap-3 border-b border-border px-4">
        {#if search.isFetching}<LoaderCircle size={17} class="shrink-0 animate-spin text-accent" />{:else}<Search size={17} class="shrink-0 text-muted" />{/if}
        <Input
          bind:ref={input}
          bind:value={query}
          class="!min-h-0 min-w-0 flex-1 !border-0 !bg-transparent !p-0 text-sm !shadow-none focus:!border-transparent"
          type="search"
          maxlength={200}
          autocomplete="off"
          placeholder={t("search.inputPlaceholder")}
          aria-label={t("search.inputLabel")}
          onkeydown={handleInputKey}
        />
        <button class="grid size-8 shrink-0 cursor-pointer place-items-center rounded-sm border-0 bg-transparent p-0 text-muted hover:bg-surface-hover hover:text-text" type="button" aria-label={t("common.close")} onclick={closeSearch}><X size={16} /></button>
      </header>

      <div bind:this={resultList} class="min-h-0 flex-initial overflow-y-auto p-2" role="listbox" aria-label={t("search.results")}>
        {#if !query.trim()}
          <div class="grid place-items-center gap-3 px-5 py-14 text-center">
            <span class="grid size-11 place-items-center rounded-full bg-accent-subtle text-accent"><Sparkles size={20} /></span>
            <div><strong class="text-sm font-semibold">{t("search.startTitle")}</strong><p class="mt-1 text-xs leading-relaxed text-muted">{t("search.startDescription", { project: projectName })}</p></div>
          </div>
        {:else if !queryReady}
          <div class="grid place-items-center py-14 text-xs text-muted"><LoaderCircle size={18} class="mb-3 animate-spin text-accent" />{t("search.searching")}</div>
        {:else if search.error}
          <div class="px-5 py-12 text-center text-xs text-danger">{t("search.failed")}</div>
        {:else if search.isLoading}
          <div class="grid place-items-center py-14 text-xs text-muted"><LoaderCircle size={18} class="mb-3 animate-spin text-accent" />{t("search.searching")}</div>
        {:else if !results.length}
          <div class="px-5 py-14 text-center"><strong class="text-sm font-semibold">{t(search.data?.truncated ? "search.partialNoResults" : "search.noResults")}</strong><p class="mt-1 text-xs text-muted">{t(search.data?.truncated ? "search.truncated" : "search.noResultsDescription")}</p></div>
        {:else}
          {#each results as item, index (item.id)}
            {@const Icon = resultIcon(item)}
            <button
              class="group flex w-full cursor-pointer items-start gap-3 rounded-md border-0 bg-transparent px-3 py-3 text-left transition-colors {selectedIndex === index ? 'bg-surface-hover' : 'hover:bg-surface-hover'}"
              type="button"
              role="option"
              data-search-result-index={index}
              aria-selected={selectedIndex === index}
              onmouseenter={() => (selectedIndex = index)}
              onclick={() => choose(item)}
            >
              <span class="mt-0.5 grid size-8 shrink-0 place-items-center rounded-sm border border-border bg-surface-subtle text-secondary"><Icon size={15} /></span>
              <span class="min-w-0 flex-1">
                <span class="flex min-w-0 items-center gap-2">
                  <strong class="truncate text-xs font-semibold text-text">{item.title || t("search.untitled")}</strong>
                  <span class="shrink-0 rounded-sm bg-accent-subtle px-1.5 py-0.5 text-2xs font-medium text-accent">{resultLabel(item)}</span>
                  <time class="ml-auto shrink-0 text-2xs text-muted">{formatTime(item.occurred_at)}</time>
                </span>
                <span class="mt-1 line-clamp-2 break-words text-xs leading-relaxed text-secondary">{item.excerpt}</span>
              </span>
              <ArrowRight size={14} class="mt-2 shrink-0 text-muted opacity-0 transition-opacity group-hover:opacity-100" />
            </button>
          {/each}
          {#if search.data?.truncated}<p class="border-t border-border px-3 py-2 text-center text-2xs text-muted">{t("search.truncated")}</p>{/if}
        {/if}
      </div>
      <footer class="flex items-center gap-4 border-t border-border bg-surface-subtle px-4 py-2 text-2xs text-muted max-md:hidden"><span>↑↓ {t("search.move")}</span><span>↵ {t("search.openResult")}</span><span>esc {t("common.close")}</span></footer>
    </div>
  </div>
{/if}
