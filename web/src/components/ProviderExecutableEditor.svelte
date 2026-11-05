<script lang="ts">
  import { Button, Input } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import { onDestroy } from "svelte";
  import { Check, LoaderCircle, RotateCcw } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type { Page, ProgrammingToolExecutable } from "../lib/types";
  import ErrorNotice from "./ErrorNotice.svelte";

  let {
    provider,
    name,
    compact = false
  }: { provider: string; name: string; compact?: boolean } = $props();

  const executables = createQuery<Page<ProgrammingToolExecutable>>(() => ({
    queryKey: ["programming-tool-executables"],
    queryFn: () => api<Page<ProgrammingToolExecutable>>("/system/programming-tools"),
    staleTime: 30_000
  }));

  const item = $derived(executables.data?.items.find((entry) => entry.provider === provider));

  let draft = $state("");
  let syncedPath = $state<string | null>(null);
  let syncedProvider = $state("");
  let saveError: unknown = $state(null);
  let saving = $state(false);
  let saved = $state(false);
  let editVersion = 0;
  type Submission = { provider: string; path: string; editVersion: number };
  let queued: Submission | null = null;
  let inFlight: Submission | null = null;
  let destroyed = false;
  let savedTimer: ReturnType<typeof setTimeout> | undefined;

  // Keep the input in sync with the server value without stomping on an
  // in-progress edit: only a field that still matches the last synced value is
  // replaced.
  $effect(() => {
    if (!item || saving) return;
    if (syncedProvider !== provider) {
      syncedProvider = provider;
      syncedPath = null;
      editVersion++;
    }
    if (syncedPath === null || draft === syncedPath) draft = item.path;
    syncedPath = item.path;
  });

  onDestroy(() => {
    destroyed = true;
    queued = null;
    clearTimeout(savedTimer);
  });

  function edited() {
    editVersion++;
    saved = false;
    clearTimeout(savedTimer);
  }

  function resetPath() {
    draft = "";
    edited();
    save("");
  }

  // Only Enter/blur/reset enqueue a complete value. One in-flight request and
  // one latest queued value prevent out-of-order writes without locking input.
  function save(value: string) {
    if (!item || destroyed) return;
    if (inFlight?.provider === provider && inFlight.path === value && inFlight.editVersion === editVersion) return;
    queued = { provider, path: value, editVersion };
    if (!saving) void flush();
  }

  async function flush() {
    saving = true;
    saved = false;
    clearTimeout(savedTimer);
    while (queued && !destroyed) {
      const submission = queued;
      queued = null;
      const current = executables.data?.items.find((entry) => entry.provider === submission.provider);
      saveError = null;
      if (current?.path === submission.path) continue;
      inFlight = submission;
      try {
        const value = await api<ProgrammingToolExecutable>(`/system/programming-tools/${submission.provider}`, {
          method: "PUT", body: { path: submission.path }
        });
        queryClient.setQueryData<Page<ProgrammingToolExecutable>>(
          ["programming-tool-executables"],
          (previous) => previous ? {
            ...previous,
            items: previous.items.map((entry) => entry.provider === submission.provider ? value : entry)
          } : previous
        );
        if (!destroyed && provider === submission.provider) {
          if (editVersion === submission.editVersion && draft === submission.path) draft = value.path;
          syncedPath = value.path;
          saved = !queued && draft === value.path;
        }
      } catch (error) {
        if (!destroyed && provider === submission.provider) saveError = error;
      } finally {
        inFlight = null;
      }
    }
    if (destroyed) return;
    saving = false;
    if (saved) savedTimer = setTimeout(() => (saved = false), 2_000);
  }

  function sourceLabel(source: string): string {
    switch (source) {
      case "custom":
        return t("programmingTools.executableSourceCustom");
      case "environment":
        return t("programmingTools.executableSourceEnvironment");
      case "bundled":
        return t("programmingTools.executableSourceBundled");
      case "path":
        return t("programmingTools.executableSourcePath");
      default:
        return source;
    }
  }
</script>

<div class="grid gap-2" data-provider-executable={provider}>
  <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
    <span class="text-xs font-semibold text-secondary">
      {compact ? name : `${name} · ${t("programmingTools.executableLabel")}`}
    </span>
    {#if item}
      <span class="min-w-0 text-2xs text-muted">
        {t("programmingTools.executableCurrent", { source: sourceLabel(item.source) })}
        {#if item.resolved}
          <code class="ml-1 break-all rounded-sm bg-canvas px-1 py-0.5">{item.resolved}</code>
        {/if}
      </span>
    {/if}
  </div>

  {#if executables.isError}
    <p class="text-2xs text-muted">{t("programmingTools.executableUnavailable")}</p>
  {:else}
    <div class="flex flex-wrap items-center gap-2">
      <Input
        class="min-w-[220px] flex-1"
        type="text"
        bind:value={draft}
        spellcheck={false}
        autocapitalize="off"
        autocorrect="off"
        placeholder={t("programmingTools.executablePlaceholder")}
        aria-label={`${name} ${t("programmingTools.executableLabel")}`}
        disabled={executables.isLoading || !item}
        oninput={edited}
        onblur={() => save(draft)}
        onkeydown={(event) => {
          if (event.key === "Enter" && !event.isComposing) {
            event.preventDefault();
            save(draft);
          }
        }}
      />
      {#if saving || saved}
        <span role="status" class="inline-flex items-center gap-1 text-2xs text-muted">
          {#if saving}<LoaderCircle size={14} class="animate-spin" />{t("programmingTools.executableSaving")}
          {:else}<Check size={14} class="text-success" />{t("programmingTools.executableSaved")}{/if}
        </span>
      {/if}
      {#if item?.path}
        <!-- Do not blur/submit the old draft before the reset click runs. -->
        <Button
          class="shrink-0"
          type="button"
          disabled={executables.isLoading || saving}
          onpointerdown={(event) => event.preventDefault()}
          onclick={resetPath}
        >
          <RotateCcw size={13} />{t("programmingTools.executableReset")}
        </Button>
      {/if}
    </div>
    <p class="text-pretty text-2xs leading-relaxed text-muted">
      {t("executableAutosave.hint")}{" "}
      {t("programmingTools.executableHint")}
    </p>
  {/if}

  {#if saveError}
    <ErrorNotice error={saveError} />
    <p class="text-2xs text-muted">{t("executableAutosave.retryHint")}</p>
  {/if}
</div>
