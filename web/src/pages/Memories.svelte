
<script lang="ts">
  import { Button, Input, Select, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { Archive, ArchiveRestore, History, Plus, Save } from "lucide-svelte";
  import { v4 as uuid } from "uuid";
  import { onDestroy } from "svelte";
  import { localeInfo, t } from "../lib/i18n.svelte";
  import { deepLinkTranslations } from "../locales/deepLink";
  import { api, APIError } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type { MemoryEntry, Page } from "../lib/types";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Loading from "../components/Loading.svelte";
  import Modal from "../ui/Modal.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import { formatTime } from "../lib/helpers";
  import { location, setQueryParam } from "../lib/router.svelte";
  import { useUnsavedChanges } from "../lib/unsavedChanges.svelte";

  import RequireProject from "../components/RequireProject.svelte";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  let active = true;
  onDestroy(() => { active = false; });
  const project = $derived(context.project);
  const deepLinkText = $derived(deepLinkTranslations[localeInfo.value]);
  let createOpen = $state(false);
  let switchTarget = $state<string | null>(null);
  let archivePending = $state<MemoryEntry | null>(null);
  let archiveNeedsDecision = $state(false);
  let archiveSavePending = $state(false);
  let historyOpen = $state(false);
  let pendingView = $state<boolean | null>(null);
  const showArchived = $derived(new URLSearchParams(location.search).get("archived") === "true");
  let editTitle = $state("");
  let editContent = $state("");
  let editorMemoryId = $state("");
  let editorProjectId = $state("");
  let editorVersion = $state(0);
  let editorTitle = $state("");
  let editorContent = $state("");
  let editorBaseline = $state.raw<MemoryEntry | null>(null);
  let titleEdited = $state(false);
  let titleRevision = 0;
  let editorSession = 0;
  const dirty = $derived(titleEdited || editTitle !== editorTitle || editContent !== editorContent);
  const unsavedChanges = useUnsavedChanges(() => dirty || (createOpen && createDirty), {
    onDiscard: () => {
      editTitle = editorTitle;
      titleEdited = false;
      editContent = editorContent;
      if (createOpen) {
        resetCreateDraft();
        createOpen = false;
      }
    }
  });
  // create modal
  // Generated once per creation draft, kept on failure/retry, never user input.
  let createKey = "";
  let modalKind = $state("fact");
  let modalContent = $state("");
  const createDirty = $derived(Boolean(modalContent || modalKind !== "fact"));

  function resetCreateDraft() {
    createKey = "";
    modalContent = "";
    modalKind = "fact";
  }

  function requestCreate() {
    if (!unsavedChanges.confirm()) return;
    createMemory.reset();
    createKey = `memory-${uuid()}`;
    createOpen = true;
  }

  function closeCreate() {
    if (createMemory.isPending || !unsavedChanges.confirm()) return;
    createOpen = false;
    resetCreateDraft();
  }

  const memories = createQuery<Page<MemoryEntry>>(() => ({
    queryKey: ["memories", project?.id, showArchived],
    queryFn: ({ queryKey, signal }) => api<Page<MemoryEntry>>(`/projects/${encodeURIComponent(String(queryKey[1]))}/memories?archived=${queryKey[2]}`, { signal }),
    enabled: Boolean(project),
    refetchInterval: 2_000
  }));
  const items = $derived((memories.data?.items ?? []).filter((item) => item.project_id === project?.id));
  const requestedMemoryId = $derived(new URLSearchParams(location.search).get("memory"));
  // The URL is the single selection source. An explicit but missing target
  // must never expose a different editable memory through a first-row fallback.
  const selected = $derived(requestedMemoryId !== null ? items.find((item) => item.id === requestedMemoryId) : items[0]);
  const missingMemory = $derived(Boolean(requestedMemoryId !== null && memories.isSuccess && !memories.isFetching && !selected));
  let selectionEpoch = 0;
  let selectionProjectId: string | undefined;
  let selectionMemoryId: string | null | undefined;
  $effect(() => {
    const projectId = project?.id ?? "";
    if (selectionProjectId === projectId && selectionMemoryId === requestedMemoryId) return;
    selectionProjectId = projectId;
    selectionMemoryId = requestedMemoryId;
    selectionEpoch++;
  });

  // Compare drafts with their loaded baseline, not a moving query result. A
  // refresh of the same memory must not close history or replace dirty input.
  $effect(() => {
    const projectId = project?.id ?? "";
    if (editorProjectId !== projectId) {
      editorProjectId = projectId;
      editorSession++;
      editorMemoryId = "";
      titleEdited = false;
      editorBaseline = null;
      archivePending = null;
      switchTarget = null;
      pendingView = null;
      createOpen = false;
      resetCreateDraft();
    }
    if (!selected) {
      // A remote deletion must not silently discard the current dirty draft.
      // Hide its editor, but retain the normal discard/navigation protection.
      if (dirty && editorMemoryId && requestedMemoryId === editorMemoryId) {
        historyOpen = false;
        return;
      }
      if (editorMemoryId) editorSession++;
      editorMemoryId = "";
      editorVersion = 0;
      editorBaseline = null;
      titleEdited = false;
      editTitle = editorTitle = "";
      editContent = editorContent = "";
      historyOpen = false;
    } else if (selected.id !== editorMemoryId || (selected.version > editorVersion && !dirty)) {
      if (selected.id !== editorMemoryId) {
        editorSession++;
        historyOpen = false;
      }
      editorMemoryId = selected.id;
      editorVersion = selected.version;
      editorBaseline = selected;
      titleEdited = false;
      editTitle = editorTitle = selected.title;
      editContent = editorContent = selected.content;
    } else if (selected.version > editorVersion && editorBaseline && sameMemoryBody(editorBaseline, selected)) {
      // An asynchronous title is not a competing content edit. Rebase only
      // this narrow change; keep both a dirty body and explicit title intent.
      editorVersion = selected.version;
      editorBaseline = selected;
      editorTitle = selected.title;
      if (!titleEdited) editTitle = selected.title;
    }
  });

  function sameMemoryBody(left: MemoryEntry, right: MemoryEntry) {
    return left.id === right.id && left.project_id === right.project_id && left.content === right.content &&
      left.key === right.key && left.kind === right.kind && left.source_type === right.source_type && left.source_ref === right.source_ref;
  }
  type MemoryDraft = { projectId: string; id: string; title: string; content: string; version: number; session: number;
    titleEdited: boolean; titleRevision: number; baseline: MemoryEntry };
  function memoryDraft(): MemoryDraft {
    return { projectId: editorProjectId, id: editorMemoryId, title: editTitle, content: editContent, version: editorVersion,
      session: editorSession, titleEdited, titleRevision, baseline: editorBaseline! };
  }
  const update = createMutation<MemoryEntry, Error, MemoryDraft>(() => ({
    mutationFn: async (draft) => {
      const path = `/projects/${encodeURIComponent(draft.projectId)}/memories/${encodeURIComponent(draft.id)}`;
      const write = (version: number) => api<MemoryEntry>(path, {
        method: "PATCH", body: { ...(draft.titleEdited ? { title: draft.title.trim() } : {}),
          content: draft.content, expected_version: version }
      });
      try { return await write(draft.version); } catch (error) {
        if (!(error instanceof APIError) || error.status !== 409) throw error;
        // One bounded retry, and only when a GET proves there was no content
        // or metadata change. A real concurrent body edit must still conflict.
        const latest = await api<MemoryEntry>(path).catch(() => null);
        if (!latest || latest.version <= draft.version || !sameMemoryBody(draft.baseline, latest)) throw error;
        return write(latest.version);
      }
    },
    onSuccess: (saved, draft) => {
      queryClient.setQueryData<Page<MemoryEntry>>(["memories", saved.project_id, false], (current) => current ? {
        ...current, items: current.items.map((item) => item.id === saved.id && item.version <= saved.version ? saved : item)
      } : current);
      if (active && project?.id === draft.projectId && selected?.id === saved.id && editorSession === draft.session) {
        // Advance the baseline even if the user kept typing during this save.
        // Only normalize fields that have not changed since submission.
        const latest = selected.version > saved.version && sameMemoryBody(saved, selected) ? selected : saved;
        editorVersion = latest.version;
        editorBaseline = latest;
        editorTitle = latest.title;
        editorContent = latest.content;
        if (titleRevision === draft.titleRevision) { editTitle = latest.title; titleEdited = false; }
        if (editContent === draft.content) editContent = latest.content;
      }
      queryClient.invalidateQueries({ queryKey: ["memories", saved.project_id] });
      queryClient.invalidateQueries({ queryKey: ["memory-versions", saved.id] });
    }
  }));
  function saveMemory() {
    if (!project || !selected || editorMemoryId !== selected.id || editorProjectId !== project.id ||
      showArchived || archive.isPending || update.isPending || !dirty) return;
    update.mutate(memoryDraft());
  }
  type ArchiveRequest = { item: MemoryEntry; session: number };
  const archive = createMutation<void, Error, ArchiveRequest>(() => ({
    mutationFn: ({ item }) =>
      api<void>(`/projects/${item.project_id}/memories/${item.id}/archive`, {
        body: { expected_version: item.version }
      }),
    onSuccess: (_result, { item, session }) => {
      if (active && project?.id === item.project_id && editorMemoryId === item.id && editorSession === session) {
        archivePending = null;
        // Discard only after the archive succeeded. A failure leaves the draft
        // recoverable, and an old result cannot touch a different editor.
        editTitle = editorTitle;
        titleEdited = false;
        editContent = editorContent;
        unsavedChanges.allowChanges(() => setMemoryLink(null, true));
      }
      queryClient.setQueryData<Page<MemoryEntry>>(["memories", item.project_id, false], (current) => current ? {
        ...current, items: current.items.filter((entry) => entry.id !== item.id)
      } : current);
      queryClient.invalidateQueries({ queryKey: ["memories", item.project_id] });
    }
  }));
  function requestArchive() {
    if (!selected || archive.isPending) return;
    archive.reset();
    archivePending = selected;
    archiveNeedsDecision = dirty;
  }

  async function saveAndArchive() {
    if (!project || !archivePending || archiveSavePending || update.isPending || archive.isPending) return;
    archiveSavePending = true;
    const session = editorSession;
    const projectId = project.id;
    const id = archivePending.id;
    try {
      let saved = archivePending;
      if (dirty) {
        saved = await update.mutateAsync(memoryDraft());
      }
      if (!active || project?.id !== projectId || editorMemoryId !== id || editorSession !== session) return;
      archivePending = saved;
      // onSuccess advanced only the submitted baseline. If typing continued
      // during the request, ask again instead of archiving those newer changes.
      if (dirty) return;
      await archive.mutateAsync({ item: saved, session });
    } catch {
      // Keep both the choice dialog and the draft on a save/archive conflict.
    } finally {
      archiveSavePending = false;
    }
  }
  const restore = createMutation<MemoryEntry, Error, ArchiveRequest>(() => ({
    mutationFn: ({ item }) => api<MemoryEntry>(`/projects/${item.project_id}/memories/${item.id}/restore`, {
      body: { expected_version: item.version }
    }),
    onSuccess: (restored, { item, session }) => {
      queryClient.setQueryData<Page<MemoryEntry>>(["memories", restored.project_id, false], (current) => ({
        ...current, items: [restored, ...(current?.items ?? []).filter((item) => item.id !== restored.id)]
      }));
      queryClient.invalidateQueries({ queryKey: ["memories", restored.project_id] });
      if (!active || project?.id !== item.project_id || editorMemoryId !== item.id || editorSession !== session) return;
      changeView(false);
      setMemoryLink(restored.id, true);
    }
  }));
  type MemoryCreation = { projectId: string; key: string; kind: string; content: string; epoch: number };
  const createMemory = createMutation<MemoryEntry, Error, MemoryCreation>(() => ({
    mutationFn: (draft) =>
      api<MemoryEntry>(`/projects/${draft.projectId}/memories`, {
        body: { key: draft.key.trim(), kind: draft.kind, content: draft.content, source_type: "user" }
      }),
    onSuccess: (created, draft) => {
      queryClient.setQueryData<Page<MemoryEntry>>(["memories", created.project_id, false], (current) => ({
        ...current, items: [created, ...(current?.items ?? []).filter((item) => item.id !== created.id)]
      }));
      queryClient.invalidateQueries({ queryKey: ["memories", created.project_id] });
      if (!active || project?.id !== draft.projectId || selectionEpoch !== draft.epoch) return;
      createOpen = false;
      changeView(false);
      setMemoryLink(created.id);
      resetCreateDraft();
    }
  }));
  const versions = createQuery<Page<{ id: string; version: number; title: string; created_at: string }>>(() => ({
    queryKey: ["memory-versions", selected?.id, project?.id],
    queryFn: ({ queryKey, signal }) => api(`/projects/${encodeURIComponent(String(queryKey[2]))}/memories/${encodeURIComponent(String(queryKey[1]))}/versions`, { signal }),
    enabled: historyOpen && Boolean(selected)
  }));

  function setMemoryLink(id: string | null, replace = false) {
    const params = new URLSearchParams(location.search);
    // A manual project switch may leave an older explicit project parameter.
    // Keep a subsequent user selection scoped to the project already displayed.
    if (project && params.has("project") && params.get("project") !== project.id) setQueryParam("project", project.id, true);
    setQueryParam("memory", id, replace);
  }

  function select(id: string) {
    if (dirty && editorMemoryId && editorMemoryId !== id) {
      switchTarget = id;
      return;
    }
    unsavedChanges.allowChanges(() => setMemoryLink(id));
  }

  function requestView(archived: boolean) {
    if (archived === showArchived) return;
    if (dirty) { pendingView = archived; return; }
    changeView(archived);
  }

  function changeView(archived: boolean) {
    unsavedChanges.allowChanges(() => {
      setMemoryLink(null, true);
      setQueryParam("archived", archived ? "true" : null);
    });
    pendingView = null;
  }

  function kindLabel(kind: string): string {
    const key = `memories.kind${kind.charAt(0).toUpperCase()}${kind.slice(1)}`;
    const label = t(key);
    return label === key ? kind : label;
  }

  function sourceLabel(source: string): string {
    if (source === "user") return t("issues.you");
    if (source === "agent") return t("issues.agent");
    return source;
  }

</script>

<RequireProject>
  <div class="flex h-full min-h-0 flex-col">
  <PageHeader
    title={t("memories.pageTitle")}
    description={t("memories.desc")}
  >
    {#snippet actions()}
      <Button type="button" variant="primary" onclick={requestCreate}><Plus size={14} />{t("memories.newMemory")}</Button>
    {/snippet}
  </PageHeader>
  {#if memories.error}<div class="border-b border-border px-5 py-3"><ErrorNotice error={memories.error} /></div>{/if}
  {#if memories.isLoading}
    <Loading block />
  {:else}
    <section class="grid min-h-0 flex-1 grid-cols-[280px_minmax(0,1fr)] overflow-hidden bg-surface max-md:grid-cols-1 max-md:grid-rows-[auto_minmax(0,1fr)]">
      <aside class="min-h-0 overflow-y-auto border-r border-border bg-surface-subtle max-md:max-h-[180px] max-md:border-b max-md:border-r-0">
        <nav class="sticky top-0 z-10 grid grid-cols-2 gap-1 border-b border-border bg-surface-subtle p-2" aria-label={t("memories.pageTitle")}>
          <button type="button" class="min-h-9 rounded-sm text-xs {showArchived ? 'text-muted' : 'bg-surface font-semibold text-text'}" aria-pressed={!showArchived} onclick={() => requestView(false)}>{t("memories.active")}</button>
          <button type="button" class="min-h-9 rounded-sm text-xs {showArchived ? 'bg-surface font-semibold text-text' : 'text-muted'}" aria-pressed={showArchived} onclick={() => requestView(true)}>{t("status.archived")}</button>
        </nav>
        {#each items as item (item.id)}
          <button data-memory-id={item.id} class={selected?.id === item.id ? "grid w-full cursor-pointer gap-[5px] border-0 border-b border-border bg-surface px-4 py-3 text-left shadow-[inset_3px_0_var(--color-accent)]" : "grid w-full cursor-pointer gap-[5px] border-0 border-b border-border bg-transparent px-4 py-3 text-left"} onclick={() => select(item.id)}>
            <strong>{item.title}</strong>
            <span class="min-w-0 text-[11px] text-muted"><code class="break-all">{item.key}</code> · {kindLabel(item.kind)}</span>
          </button>
        {/each}
        {#if !items.length}<p class="px-4 py-4 text-xs text-muted">{t("memories.noMemories")}</p>{/if}
      </aside>
      <div class="min-h-0 overflow-y-auto p-5">
        {#if missingMemory}
          <div role="alert"><EmptyState title={deepLinkText.memoryUnavailable} description={deepLinkText.memoryUnavailableHint} /></div>
        {:else if requestedMemoryId !== null && !selected && memories.isFetching}
          <Loading block />
        {:else if selected}
          <div class="grid gap-4">
            {#if update.error || archive.error || restore.error}<ErrorNotice error={update.error ?? archive.error ?? restore.error} />{/if}
            <div class="flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
              <span class="min-w-0 [overflow-wrap:anywhere]">{t("memories.versionInfo", { key: selected.key, version: selected.version, source: sourceLabel(selected.source_type) })}</span>
              <div class="flex flex-wrap gap-2">
                {#if dirty}<span class="inline-flex min-h-6 items-center rounded-sm bg-warning-subtle px-[9px] text-xs font-semibold tabular-nums text-warning">{t("common.unsavedChanges")}</span>{/if}
                <Button type="button" onclick={() => (historyOpen = !historyOpen)}>
                  <History size={14} />{t("memories.history")}
                </Button>
                {#if showArchived}
                  <Button type="button" variant="primary" disabled={restore.isPending} onclick={() => restore.mutate({ item: selected!, session: editorSession })}><ArchiveRestore size={14} />{t("memories.restore")}</Button>
                {:else}
                <Button type="button" onclick={requestArchive} disabled={archiveSavePending || update.isPending || archive.isPending}>
                  <Archive size={14} />{t("memories.archive")}
                </Button>
                <Button type="button" variant="primary" onclick={saveMemory} disabled={!editTitle.trim() || update.isPending || !dirty}>
                  <Save size={14} />{t("memories.save")}
                </Button>
                {/if}
              </div>
            </div>
            <Field label={t("memories.title")}><Input bind:value={editTitle} oninput={() => { titleEdited = true; titleRevision++; }} readonly={showArchived || archive.isPending} placeholder={t("memories.titlePlaceholder")} /></Field>
            <Field label={t("memories.content")}><Textarea autoGrow rows={4} bind:value={editContent} readonly={showArchived || archive.isPending} placeholder={t("memories.contentPlaceholder")}></Textarea></Field>
            {#if historyOpen}
              {#if versions.error}<ErrorNotice error={versions.error} />{/if}
              {#if versions.isLoading}<Loading block />{/if}
              <div class="grid border-t border-border">
                {#each versions.data?.items ?? [] as version (version.id)}
                  <div class="flex justify-between py-2 text-[11px]"><strong>v{version.version} · {version.title}</strong><time>{formatTime(version.created_at)}</time></div>
                {/each}
              </div>
            {/if}
          </div>
        {:else}
          <EmptyState title={showArchived ? t("memories.noArchived") : t("memories.firstMemory")} description={showArchived ? t("memories.archivedHint") : t("memories.firstMemoryDesc")} />
        {/if}
      </div>
    </section>
  {/if}

  </div>
  {#if createOpen}
    <Modal title={t("memories.createTitle")} onClose={closeCreate}>
      {#if createMemory.error}<ErrorNotice error={createMemory.error} />{/if}
      <form class="grid gap-4" onsubmit={(event) => {
        event.preventDefault();
        if (project && createKey && !createMemory.isPending) createMemory.mutate({ projectId: project.id, key: createKey, kind: modalKind,
          content: modalContent, epoch: selectionEpoch });
      }}>
          <Field label={t("memories.kind")}>
            <Select bind:value={modalKind}>
              <option value="fact">{t("memories.kindFact")}</option>
              <option value="decision">{t("memories.kindDecision")}</option>
              <option value="constraint">{t("memories.kindConstraint")}</option>
              <option value="progress">{t("memories.kindProgress")}</option>
            </Select>
          </Field>
        <Field label={t("memories.content")}><Textarea rows={8} bind:value={modalContent} placeholder={t("memories.contentPlaceholder")} required></Textarea></Field>
        <div class="flex justify-end gap-2">
          <Button type="button" onclick={closeCreate}>{t("common.cancel")}</Button>
          <Button type="submit" variant="primary" disabled={createMemory.isPending}>{t("memories.create")}</Button>
        </div>
      </form>
    </Modal>
  {/if}

  {#if archivePending}
    {#if archiveNeedsDecision}
      <Modal title={t("memories.archive")} description={t("memories.unsavedWarning")}
        onClose={() => { if (!archiveSavePending && !update.isPending && !archive.isPending) archivePending = null; }}>
        {#if update.error || archive.error}<ErrorNotice error={update.error ?? archive.error} />{/if}
        <div class="flex flex-wrap justify-end gap-2">
          <Button type="button"
            disabled={archiveSavePending || update.isPending || archive.isPending} onclick={() => (archivePending = null)}>{t("common.cancel")}</Button>
          <Button type="button" variant="danger"
            disabled={archiveSavePending || update.isPending || archive.isPending}
            onclick={() => { if (archivePending) archive.mutate({ item: archivePending, session: editorSession }); }}>{t("common.discardChanges")}</Button>
          <Button type="button" variant="primary"
            disabled={!editTitle.trim() || archiveSavePending || update.isPending || archive.isPending} onclick={saveAndArchive}>{t("memories.saveAndArchive")}</Button>
        </div>
      </Modal>
    {:else}
    <ConfirmDialog
      title={t("memories.archive")}
      description={t("memories.archiveConfirm", { title: archivePending.title })}
      confirmLabel={t("memories.archive")}
      danger={false}
      busy={archive.isPending}
      error={archive.error}
      onConfirm={() => { if (archivePending) archive.mutate({ item: archivePending, session: editorSession }); }}
      onCancel={() => (archivePending = null)}
    />
    {/if}
  {/if}

  {#if switchTarget}
    <ConfirmDialog
      title={t("memories.unsavedWarning")}
      description={t("memories.unsavedWarning")}
      confirmLabel={t("common.confirm")}
      danger={false}
      onConfirm={() => {
        if (switchTarget !== null) {
          unsavedChanges.allowChanges(() => setMemoryLink(switchTarget));
        }
        switchTarget = null;
      }}
       onCancel={() => { switchTarget = null; unsavedChanges.allowChanges(() => setMemoryLink(editorMemoryId || selected?.id || null, true)); }}
    />
  {/if}
  {#if pendingView !== null}
    <ConfirmDialog title={t("common.unsavedChanges")} description={t("common.unsavedChanges")} confirmLabel={t("common.confirm")} danger={false}
      onConfirm={() => { if (pendingView !== null) changeView(pendingView); }} onCancel={() => (pendingView = null)} />
  {/if}
</RequireProject>
