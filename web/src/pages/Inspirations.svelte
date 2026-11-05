<script lang="ts">
  import { Button, Input, Select } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import { onDestroy } from "svelte";
  import { Bot, Lightbulb, Plus, Search, Trash2, X } from "lucide-svelte";
  import { api, idempotencyKey } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { location, setQueryParam } from "../lib/router.svelte";
  import type {
    Inspiration,
    InspirationCard,
    InspirationGroup as Group
  } from "../lib/inspirations";
  import InspirationGroup from "../components/InspirationGroup.svelte";
  import InspirationEditor from "../components/InspirationEditor.svelte";
  import WorkAssistant from "../components/WorkAssistant.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import { useConsole } from "./shared.svelte";
  const context = useConsole();
  let active = true;
  onDestroy(() => { active = false; });
  let search = $state(""),
    appliedSearch = $state(""),
    status = $state("all");
  let selected = $state<Inspiration | null>(null),
    busy = $state(false),
    error = $state<unknown>(null);
  let panel = $state(false),
    tab = $state<"edit" | "assistant">("edit"),
    assistantMounted = $state(false),
    editorEpoch = $state(0);
  let deleting = $state<Inspiration | null>(null);
  let editor = $state<{ flush(): Promise<void>; toggleCompleted(): Promise<void> }>();
  const groups = createQuery<{ items: Group[] }>(() => ({
    queryKey: ["inspiration-groups", appliedSearch, status],
    queryFn: ({ queryKey, signal }) =>
      api(`/inspirations/groups?${new URLSearchParams({ search: String(queryKey[1]), status: String(queryKey[2]) })}`, { signal }),
    refetchOnWindowFocus: false,
    refetchInterval: 2_000
  }));
  // Cards are paginated inside group components. Invalidate their existing
  // queries on the page poll rather than remounting them (or the editor).
  $effect(() => {
    if (groups.dataUpdatedAt) void queryClient.invalidateQueries({ queryKey: ["inspirations"] });
  });
  const selectedNote = createQuery<Inspiration>(() => ({
    queryKey: ["inspiration-detail", selected?.id],
    queryFn: ({ queryKey, signal }) => api(`/inspirations/${encodeURIComponent(String(queryKey[1]))}`, { signal }),
    enabled: Boolean(panel && selected),
    refetchInterval: 2_000,
    refetchOnWindowFocus: false
  }));
  $effect(() => {
    const latest = selectedNote.data;
    if (latest && latest.id === selected?.id && latest.version > selected.version) selected = latest;
  });
  function refresh() {
    void queryClient.invalidateQueries({ queryKey: ["inspiration-groups"] });
    void queryClient.invalidateQueries({ queryKey: ["inspirations"] });
    void queryClient.invalidateQueries({ queryKey: ["work-records"] });
  }
  function saved(note: Inspiration) {
    if (!active) return;
    if (selected?.id === note.id && note.version >= selected.version) selected = note;
    refresh();
  }
  async function flush() {
    await editor?.flush();
  }
  async function choose(id: string) {
    if (busy) return;
    busy = true;
    error = null;
    const route = location.search;
    try {
      await flush();
      if (!active || location.search !== route) return;
      const note = await api<Inspiration>(`/inspirations/${encodeURIComponent(id)}`);
      if (!active || location.search !== route) return;
      selected = note;
      panel = true;
      tab = "edit";
      setQueryParam("note", id, true);
    } catch (e) {
      if (active && location.search === route) error = e;
    } finally {
      busy = false;
    }
  }
  async function create(projectID: string) {
    if (busy) return;
    busy = true;
    error = null;
    const route = location.search;
    const projectAtStart = context.project?.id;
    try {
      await flush();
      if (!active || location.search !== route || context.project?.id !== projectAtStart) return;
      const note = await api<Inspiration>("/inspirations", {
        body: { project_id: projectID, markdown: "" },
        headers: { "Idempotency-Key": idempotencyKey("inspiration") }
      });
      refresh();
      if (!active || location.search !== route || context.project?.id !== projectAtStart) return;
      selected = note;
      panel = true;
      tab = "edit";
      setQueryParam("note", note.id, true);
    } catch (e) {
      if (active && location.search === route && context.project?.id === projectAtStart) error = e;
    } finally {
      busy = false;
    }
  }
  async function toggle(note: InspirationCard) {
    if (busy) return;
    busy = true;
    error = null;
    const route = location.search;
    try {
      if (selected?.id === note.id && editor) {
        await editor.toggleCompleted();
      } else {
        const saved = await api<Inspiration>(`/inspirations/${note.id}`, {
          method: "PATCH",
          body: { completed: !note.completed, expected_version: note.version }
        });
        if (active && selected?.id === saved.id && saved.version >= selected.version) selected = saved;
        refresh();
      }
    } catch (e) {
      if (active && location.search === route) error = e;
      refresh();
    } finally {
      busy = false;
    }
  }
  async function showAssistant() {
    const route = location.search;
    try {
      await flush();
      if (!active || location.search !== route) return;
      panel = true;
      tab = "assistant";
      assistantMounted = true;
    } catch (e) {
      if (active && location.search === route) error = e;
    }
  }
  async function close() {
    const route = location.search;
    try {
      await flush();
      if (!active || location.search !== route) return;
      panel = false;
    } catch (e) {
      if (active && location.search === route) error = e;
    }
  }
  async function reload() {
    if (!selected) return;
    const id = selected.id;
    const route = location.search;
    try {
      const latest = await api<Inspiration>(`/inspirations/${encodeURIComponent(id)}`);
      if (!active || selected?.id !== id || location.search !== route) return;
      if (latest.version >= selected.version) selected = latest;
      editorEpoch++;
    } catch (e) {
      if (active && selected?.id === id && location.search === route) error = e;
    }
  }
  async function remove() {
    if (!deleting || busy) return;
    busy = true;
    error = null;
    const route = location.search;
    const target = deleting;
    try {
      await flush();
      if (!active || location.search !== route || deleting?.id !== target.id) return;
      const current = selected?.id === target.id ? selected : target;
      await api(`/inspirations/${current.id}?expected_version=${current.version}`, {
        method: "DELETE"
      });
      if (active && location.search === route && selected?.id === current.id) {
        selected = null;
        panel = false;
        setQueryParam("note", null, true);
      }
      if (active && location.search === route && deleting?.id === target.id) deleting = null;
      refresh();
    } catch (e) {
      if (active && location.search === route) error = e;
    } finally {
      busy = false;
    }
  }
  let appliedLink = $state("");
  $effect(() => {
    const params = new URLSearchParams(location.search);
    const id = params.get("note");
    if (id && id !== selected?.id && id !== appliedLink && !busy) {
      appliedLink = id;
      void choose(id);
    }
    if (params.has("session")) {
      panel = true;
      tab = "assistant";
      assistantMounted = true;
    }
  });
</script>

<div class="flex h-full min-h-0 flex-col" data-inspirations>
  <header
    class="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4"
  >
    <div>
      <h1 class="flex items-center gap-2 text-xl font-semibold">
        <Lightbulb size={22} class="text-accent" />{t("inspirations.title")}
      </h1>
      <p class="mt-1 text-xs text-muted">{t("inspirations.description")}</p>
    </div>
    <div class="flex gap-2">
      <Button
        type="button"
        disabled={busy}
        onclick={showAssistant}><Bot size={15} />{t("inspirations.assistant")}</Button
      ><Button
        type="button" variant="primary"
        disabled={busy}
        onclick={() => create(context.project?.id ?? "")}
        ><Plus size={15} />{t("inspirations.add")}</Button
      >
    </div>
  </header>
  <div class="flex min-h-0 flex-1">
    <section class="flex min-h-0 min-w-0 flex-1 flex-col">
      <form
        class="flex flex-wrap gap-2 border-b border-border p-4"
        onsubmit={(e) => {
          e.preventDefault();
          appliedSearch = search;
        }}
      >
        <Input
          class="min-w-40 flex-1"
          aria-label={t("inspirations.search")}
          placeholder={t("inspirations.search")}
          bind:value={search}
          maxlength={500}
        /><Select class="!w-auto" aria-label={t("work.outcome")} bind:value={status}
          ><option value="all">{t("inspirations.all")}</option><option value="open"
            >{t("inspirations.open")}</option
          ><option value="completed">{t("inspirations.completed")}</option></Select
        ><Button type="submit" icon
          aria-label={t("work.apply")}><Search size={15} /></Button
        >
      </form>
      {#if error || groups.error}<div class="p-3">
          <ErrorNotice error={error ?? groups.error} />
        </div>{/if}
      <div class="min-h-0 flex-1 space-y-5 overflow-y-auto p-5 max-md:p-3">
        {#each groups.data?.items ?? [] as group (group.project_id)}<InspirationGroup
            {group}
            search={appliedSearch}
            {status}
            selected={selected?.id ?? ""}
            {busy}
            onSelect={(id) => {
              void choose(id);
            }}
            onCreate={(id) => {
              void create(id);
            }}
            onToggle={(note) => {
              void toggle(note);
            }}
          />{/each}
      </div>
    </section>
    {#if panel}
      <aside
        class="flex w-[min(48%,620px)] min-w-[360px] shrink-0 flex-col border-l border-border bg-surface max-[1100px]:fixed max-[1100px]:inset-y-0 max-[1100px]:right-0 max-[1100px]:z-50 max-[1100px]:w-[min(100vw,620px)] max-[1100px]:min-w-0 max-[1100px]:shadow-xl"
        aria-label={t("inspirations.title")}
      >
        <header class="flex items-center gap-2 border-b border-border px-3 py-2">
          <div role="tablist" aria-label={t("inspirations.title")} class="flex flex-1 gap-1">
            <button
              role="tab"
              aria-selected={tab === "edit"}
              class="rounded px-3 py-2 text-xs"
              class:bg-surface-subtle={tab === "edit"}
              disabled={!selected}
              onclick={() => (tab = "edit")}>{t("inspirations.edit")}</button
            ><button
              role="tab"
              aria-selected={tab === "assistant"}
              class="rounded px-3 py-2 text-xs"
              class:bg-surface-subtle={tab === "assistant"}
              onclick={showAssistant}>{t("inspirations.assistant")}</button
            >
          </div>
          {#if selected}<Button
              type="button" controlSize="sm" icon variant="danger"
              aria-label={t("inspirations.delete")}
              disabled={busy}
              onclick={() => (deleting = selected)}><Trash2 size={15} /></Button
            >{/if}<Button
            type="button" controlSize="sm" icon variant="ghost"
            aria-label={t("common.close")}
            disabled={busy}
            onclick={close}><X size={16} /></Button
          >
        </header>
        <div class="min-h-0 flex-1" class:hidden={tab !== "edit"}>
          {#if selected}{#key selected.id + ":" + editorEpoch}<InspirationEditor
                bind:this={editor}
                note={selected}
                blocked={busy}
                onSaved={saved}
                onReload={() => {
                  void reload();
                }}
              />{/key}{/if}
        </div>
        <div class="min-h-0 flex-1" class:hidden={tab !== "assistant"}>
          {#if assistantMounted}<WorkAssistant
              scope="inspirations"
              query={{}}
              selected={[]}
              inspirationIDs={selected ? [selected.id] : []}
              inspirationQuery={{ search: appliedSearch, status }}
              contextLabel={selected?.title ?? ""}
              beforeSend={flush}
              onClose={() => {
                if (selected) tab = "edit";
                else void close();
              }}
            />{/if}
        </div>
      </aside>
    {/if}
  </div>
</div>
{#if deleting}<ConfirmDialog
    title={t("inspirations.delete")}
    description={t("inspirations.deleteHint")}
    confirmLabel={t("inspirations.delete")}
    {busy}
    {error}
    onConfirm={() => {
      void remove();
    }}
    onCancel={() => (deleting = null)}
  />{/if}
