<script lang="ts">
  import { Checkbox, Input, Select, Textarea, Field, FieldGroup } from "../ui";
  import { onMount, onDestroy, untrack } from "svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { registerNavigationGuard } from "../lib/router.svelte";
  import {
    InspirationSaveQueue,
    type Inspiration,
    type InspirationDraft
  } from "../lib/inspirations";
  import { useConsole } from "../pages/shared.svelte";
  import MarkdownEditor from "./MarkdownEditor.svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  let {
    note,
    onSaved,
    onReload,
    blocked = false
  }: {
    note: Inspiration;
    onSaved: (note: Inspiration) => void;
    onReload: () => void;
    blocked?: boolean;
  } = $props();
  const initial = untrack(() => note);
  const context = useConsole();
  let title = $state(initial.title),
    markdown = $state(initial.markdown),
    projectID = $state(initial.project_id),
    completed = $state(initial.completed);
  let titleRevision = $state(0);
  let active = true;
  let source = $state(false),
    saving = $state(false),
    error = $state<unknown>(null),
    savedRevision = $state(initial.version);
  let wysiwyg = $state<{ getMarkdown: () => string }>();
  let shell: HTMLElement;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const snapshot = (): InspirationDraft => ({
    title: title.trim(),
    titleRevision,
    markdown: source ? markdown : (wysiwyg?.getMarkdown() ?? markdown),
    project_id: projectID,
    completed
  });
  const queue = new InspirationSaveQueue(
    initial.id,
    initial,
    (id, version, draft) =>
      api(`/inspirations/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: { ...draft, expected_version: version }
      }),
    (saved, submitted) => {
      if (!active) return;
      // A response advances only fields not edited since its snapshot. In
      // particular an automatic name must never erase an in-flight title edit.
      if (titleRevision === submitted.titleRevision) title = saved.title;
      if (markdown === submitted.markdown) markdown = saved.markdown;
      if (projectID === submitted.project_id) projectID = saved.project_id;
      if (completed === submitted.completed) completed = saved.completed;
      savedRevision = saved.version;
      onSaved(saved);
    },
    (id) => api(`/inspirations/${encodeURIComponent(id)}`)
  );
  $effect(() => {
    const latest = note;
    untrack(() => queue.acceptServer(latest));
  });
  const dirty = $derived.by(() => {
    void savedRevision;
    return queue.dirty({
      title: title.trim(),
      titleRevision,
      markdown,
      project_id: projectID,
      completed
    });
  });
  function schedule() {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      void flush().catch(() => {});
    }, 650);
  }
  function changed(value: string) {
    markdown = value;
    schedule();
  }
  export async function flush() {
    if (timer) {
      clearTimeout(timer);
      timer = undefined;
    }
    if (!source && wysiwyg) markdown = wysiwyg.getMarkdown();
    saving = true;
    error = null;
    try {
      await queue.flush(snapshot);
    } catch (e) {
      error = e;
      throw e;
    } finally {
      saving = false;
    }
  }
  export async function toggleCompleted() {
    completed = !completed;
    await flush();
  }
  function toggleSource() {
    if (!source && wysiwyg) markdown = wysiwyg.getMarkdown();
    source = !source;
    if (queue.dirty(snapshot())) schedule();
  }
  async function exportMarkdown() {
    try {
      await flush();
      const blob = new Blob([snapshot().markdown], { type: "text/markdown;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${title.trim().replace(/[\\/:*?"<>|]/g, "-") || "inspiration"}.md`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      /* keep error/draft */
    }
  }
  onMount(() => {
    const remove = registerNavigationGuard(
      () => !queue.dirty(snapshot()) || window.confirm(t("inspirations.discard"))
    );
    const unload = (e: BeforeUnloadEvent) => {
      if (queue.dirty(snapshot())) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", unload);
    const shortcut = (e: KeyboardEvent) => {
      if (
        (e.metaKey || e.ctrlKey) &&
        e.key.toLowerCase() === "s" &&
        shell.contains(document.activeElement)
      ) {
        e.preventDefault();
        void flush().catch(() => {});
      }
    };
    window.addEventListener("keydown", shortcut);
    return () => {
      remove();
      window.removeEventListener("beforeunload", unload);
      window.removeEventListener("keydown", shortcut);
    };
  });
  onDestroy(() => {
    active = false;
    queue.stop();
    if (timer) clearTimeout(timer);
  });
</script>

<section
  bind:this={shell}
  class="flex h-full min-h-0 flex-col bg-surface"
  aria-label={t("inspirations.editor")}
  inert={blocked}
>
  <div class="grid gap-3 border-b border-border p-4">
    <Field label={t("inspirations.heading")}><Input
        aria-label={t("inspirations.heading")}
        bind:value={title}
        oninput={() => { titleRevision++; schedule(); }}
        maxlength={200}
      /></Field>
    <FieldGroup columns={1} collapseAt="sm" data-field-group="inspiration-project">
      <Field label={t("work.project")}><Select
          aria-label={t("work.project")}
          bind:value={projectID}
          onchange={schedule}
          ><option value="">{t("inspirations.ungrouped")}</option
          >{#each context.projects as p}<option value={p.id}>{p.name}</option
            >{/each}{#if projectID && !context.projects.some((p) => p.id === projectID)}<option
              value={projectID}>{t("inspirations.deletedProject")}</option
            >{/if}</Select
        ></Field>
      {#snippet actions()}<label class="flex items-center gap-2 text-xs"
        ><Checkbox inheritField={false} type="checkbox" bind:checked={completed} onchange={schedule} />{t(
          "inspirations.completed"
        )}</label
      >{/snippet}
    </FieldGroup>
    <div class="flex flex-wrap items-center gap-2 text-xs">
      <button
        type="button"
        class="rounded border border-border px-3 py-2"
        aria-pressed={source}
        onclick={toggleSource}
        >{source ? t("inspirations.visual") : t("inspirations.source")}</button
      ><button type="button" class="rounded border border-border px-3 py-2" onclick={exportMarkdown}
        >{t("inspirations.export")}</button
      ><span class="ml-auto text-muted" role="status"
        >{saving
          ? t("inspirations.saving")
          : dirty
            ? t("inspirations.unsaved")
            : t("inspirations.saved")}</span
      ><button
        type="button"
        class="rounded border border-control-border bg-control px-3 py-2 text-control-text hover:bg-control-hover disabled:opacity-50"
        disabled={saving || !dirty}
        onclick={() => {
          void flush().catch(() => {});
        }}>{t("common.save")}</button
      >
    </div>
    {#if error}<ErrorNotice {error} /><button
        type="button"
        class="text-left text-xs text-accent underline"
        onclick={() => {
          if (!queue.dirty(snapshot()) || window.confirm(t("inspirations.discard"))) onReload();
        }}>{t("inspirations.reload")}</button
      >{/if}
  </div>
  <div class="min-h-0 flex-1 overflow-auto" data-inspiration-editor={initial.id}>
    {#if source}<Textarea
        aria-label={t("inspirations.source")}
        class="!h-full !min-h-80 !resize-none !rounded-none !border-0 font-mono text-sm"
        bind:value={markdown}
        oninput={schedule}></Textarea>
    {:else}<MarkdownEditor bind:this={wysiwyg} initial={markdown} onChange={changed} />{/if}
  </div>
  <p class="border-t border-border px-4 py-2 text-2xs text-muted">{t("inspirations.formatHint")}</p>
</section>
