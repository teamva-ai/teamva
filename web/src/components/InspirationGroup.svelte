<script lang="ts">
  import { createInfiniteQuery } from "@tanstack/svelte-query";
  import { Check, Plus } from "lucide-svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import {
    noteColor,
    inspirationPreview,
    type InspirationCard,
    type InspirationGroup,
    type InspirationPage
  } from "../lib/inspirations";
  import ErrorNotice from "./ErrorNotice.svelte";
  let {
    group,
    search,
    status,
    selected,
    busy,
    onSelect,
    onCreate,
    onToggle
  }: {
    group: InspirationGroup;
    search: string;
    status: string;
    selected: string;
    busy: boolean;
    onSelect: (id: string) => void;
    onCreate: (id: string) => void;
    onToggle: (note: InspirationCard) => void;
  } = $props();
  const notes = createInfiniteQuery(() => ({
    queryKey: ["inspirations", group.project_id, search, status],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api<InspirationPage>(
        `/inspirations?${new URLSearchParams({ project_id: group.project_id, search, status, limit: "40", offset: String(pageParam) })}`
      ),
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, page) => n + page.items.length, 0);
      return loaded < last.total ? loaded : undefined;
    },
    refetchOnWindowFocus: false
  }));
  const items = $derived([
    ...new Map(
      (notes.data?.pages.flatMap((page) => page.items) ?? []).map((note) => [note.id, note])
    ).values()
  ]);
</script>

<section
  class="inspiration-group rounded-xl border border-border bg-surface-subtle p-4"
  data-inspiration-group={group.project_id || "ungrouped"}
>
  <header class="mb-4 flex items-center justify-between gap-3">
    <div class="min-w-0">
      <h2 class="truncate text-sm font-semibold">
        {group.project_name || t("inspirations.ungrouped")}{#if group.project_deleted}<span
            class="ml-2 text-2xs font-normal text-muted">{t("inspirations.deletedProject")}</span
          >{/if}
      </h2>
      <p class="mt-1 text-2xs text-muted">
        {group.total} · {t("inspirations.completed")}
        {group.completed}
      </p>
    </div>
    <button
      type="button"
      class="grid size-9 shrink-0 place-items-center rounded-full border border-border bg-surface hover:text-accent"
      disabled={busy || group.project_deleted}
      aria-label={`${t("inspirations.add")} · ${group.project_name || t("inspirations.ungrouped")}`}
      onclick={() => onCreate(group.project_id)}><Plus size={16} /></button
    >
  </header>
  {#if notes.error}<ErrorNotice error={notes.error} />{/if}
  <div class="grid grid-cols-[repeat(auto-fill,minmax(min(100%,210px),1fr))] gap-4">
    {#each items as note (note.id)}
      <article
        class="sticky-note flex min-h-44 min-w-0 flex-col rounded-md border p-4 shadow-sm transition-colors"
        class:done={note.completed}
        class:selected={selected === note.id}
        data-color={noteColor(note.id)}
        data-inspiration={note.id}
        data-completed={note.completed}
      >
        <div class="flex items-start gap-2">
          <button
            type="button"
            class="min-w-0 flex-1 break-words text-left text-sm font-semibold"
            disabled={busy}
            onclick={() => onSelect(note.id)}
            ><span class:line-through={note.completed}>{note.title}</span></button
          ><button
            type="button"
            class="grid size-6 shrink-0 place-items-center rounded-full border border-current opacity-70 hover:opacity-100"
            aria-label={note.completed ? t("inspirations.reopen") : t("inspirations.markDone")}
            aria-pressed={note.completed}
            disabled={busy}
            onclick={() => onToggle(note)}
            >{#if note.completed}<Check size={13} />{/if}</button
          >
        </div>
        <button
          type="button"
          class="mt-3 flex-1 whitespace-pre-wrap break-words text-left text-xs leading-relaxed opacity-80"
          disabled={busy}
          aria-label={`${t("inspirations.edit")} ${note.title}`}
          onclick={() => onSelect(note.id)}
          ><span class="line-clamp-6"
            >{inspirationPreview(note.preview) || t("inspirations.placeholder")}</span
          ></button
        >
        <footer class="mt-4 flex items-center justify-between gap-2 text-2xs opacity-65">
          <time>{new Date(note.created_at).toLocaleDateString()}</time>{#if note.completed}<span
              >{t("inspirations.completed")}</span
            >{/if}
        </footer>
      </article>
    {/each}
  </div>
  {#if !notes.isLoading && !items.length}<p class="py-4 text-xs text-muted">
      {t("inspirations.empty")}
    </p>{/if}
  {#if notes.hasNextPage}<button
      type="button"
      class="mt-4 rounded border border-border bg-surface px-3 py-2 text-xs"
      disabled={notes.isFetchingNextPage}
      onclick={() => notes.fetchNextPage()}>{t("inspirations.more")}</button
    >{/if}
</section>

<style>
  .sticky-note {
    background: #fff6cf;
    border-color: #e4d899;
    color: #383326;
  }
  .sticky-note[data-color="1"] {
    background: #e8f2df;
    border-color: #c8dcbc;
    color: #29382a;
  }
  .sticky-note[data-color="2"] {
    background: #e4eff9;
    border-color: #bed4e9;
    color: #293846;
  }
  .sticky-note[data-color="3"] {
    background: #f8e7e9;
    border-color: #e6c8ce;
    color: #433036;
  }
  .sticky-note.selected {
    outline: 2px solid var(--color-accent);
    outline-offset: 2px;
  }
  .sticky-note.done {
    background: var(--color-surface);
    color: var(--color-muted);
    border-color: var(--color-border);
    box-shadow: none;
  }
  :global([data-theme="dark"]) .sticky-note:not(.done) {
    background: color-mix(in srgb, var(--color-surface) 85%, #ceb653);
    color: var(--color-text);
    border-color: color-mix(in srgb, var(--color-border) 70%, #ceb653);
  }
</style>
