<script lang="ts">
  import { Button, Checkbox, DatePicker, Input, Select, Field, FieldGroup } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { Bot, ChevronDown, ChevronRight, Filter, RefreshCw, Search, Trash2 } from "lucide-svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { location } from "../lib/router.svelte";
  import {
    cleanWorkCondition,
    type WorkQuery,
    type WorkCondition,
    type WorkPage,
    type WorkFilter,
    type WorkRecord
  } from "../lib/workRecords";
  import WorkFilterBuilder from "../components/WorkFilterBuilder.svelte";
  import WorkRecordDetail from "../components/WorkRecordDetail.svelte";
  import WorkAssistant from "../components/WorkAssistant.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import { useConsole } from "./shared.svelte";
  const context = useConsole();
  let search = $state("");
  let from = $state("");
  let until = $state("");
  let projectID = $state("");
  let includeChanges = $state(false);
  let condition = $state<WorkCondition>({ join: "and", conditions: [] });
  let advanced = $state(false);
  let filterName = $state("");
  let query = $state<WorkQuery>({ limit: 50 });
  let expanded = $state<string[]>([]);
  let selected = $state<string[]>([]);
  let showAssistant = $state(
    typeof matchMedia !== "undefined" && matchMedia("(min-width: 1100px)").matches
  );
  let clearQuery = $state<WorkQuery | null>(null);
  const records = createQuery<WorkPage>(() => ({
    queryKey: ["work-records", query],
    queryFn: () => api("/work-records/query", { body: query }),
    refetchInterval: 5000
  }));
  const filters = createQuery<{ items: WorkFilter[] }>(() => ({
    queryKey: ["work-filters"],
    queryFn: () => api("/work-record-filters")
  }));
  const saveFilter = createMutation(() => ({
    mutationFn: () => api("/work-record-filters", { body: { name: filterName, query } }),
    onSuccess: () => {
      filterName = "";
      void queryClient.invalidateQueries({ queryKey: ["work-filters"] });
    }
  }));
  const deleteFilter = createMutation(() => ({
    mutationFn: (id: string) => api(`/work-record-filters/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["work-filters"] })
  }));
  const clear = createMutation(() => ({
    mutationFn: () => api("/work-records/clear", { body: { query: clearQuery, confirm: true } }),
    onSuccess: () => {
      clearQuery = null;
      selected = [];
      expanded = [];
      void queryClient.invalidateQueries({ queryKey: ["work-records"] });
    }
  }));
  function apply() {
    const extra = cleanWorkCondition(condition);
    const conditions: WorkCondition[] = [];
    if (projectID) conditions.push({ field: "project_id", operator: "eq", value: projectID });
    if (extra) conditions.push(extra);
    query = {
      search,
      from: from ? new Date(from).toISOString() : undefined,
      until: until ? new Date(until).toISOString() : undefined,
      include_changes: includeChanges,
      filter: conditions.length ? { join: "and", conditions } : undefined,
      limit: 50,
      offset: 0
    };
    selected = [];
  }
  function reset() {
    search = "";
    from = "";
    until = "";
    projectID = "";
    includeChanges = false;
    condition = { join: "and", conditions: [] };
    apply();
  }
  function load(f: WorkFilter) {
    query = { ...f.query, offset: 0, limit: 50 };
    search = f.query.search ?? "";
    from = localDate(f.query.from);
    until = localDate(f.query.until);
    projectID = "";
    condition = f.query.filter ?? { join: "and", conditions: [] };
    includeChanges = Boolean(f.query.include_changes);
    advanced = true;
    selected = [];
  }
  function localDate(value?: string) {
    if (!value) return "";
    const d = new Date(value);
    return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  }
  function toggle(id: string) {
    expanded = expanded.includes(id) ? expanded.filter((v) => v !== id) : [...expanded, id];
  }
  let requested = $state("");
  $effect(() => {
    const params = new URLSearchParams(location.search);
    const id = params.get("record") ?? "";
    if (id && id !== requested) {
      requested = id;
      query = {
        filter: { field: "id", operator: "eq", value: id },
        include_changes: true,
        limit: 50
      };
      expanded = [id];
    }
    const run = params.get("run");
    if (run && !id)
      query = {
        filter: { field: "run_id", operator: "eq", value: run },
        include_changes: true,
        limit: 50
      };
  });
  const projectName = (record: WorkRecord) =>
    context.projects.find((p) => p.id === record.project_id)?.name ?? record.project_id;
  const actionLabel = (record: WorkRecord) => {
    const verb = record.action.split(".").at(-1) ?? "";
    const key =
      (
        {
          created: "create",
          updated: "update",
          deleted: "delete",
          forked: "fork",
          message: "send"
        } as Record<string, string>
      )[verb] ?? verb;
    const label = t(`work.${key}`);
    const kind =
      (
        {
          issue: t("shell.issues"),
          agent: "Agent",
          project: t("work.project"),
          graph: t("shell.conversations"),
          node: t("shell.conversations"),
          resource: t("shell.resources"),
          memory: t("shell.memories"),
          schedule: t("shell.schedules")
        } as Record<string, string>
      )[record.object_type] ?? record.object_type;
    return label.startsWith("work.") ? record.action : `${label} · ${kind || record.kind}`;
  };
</script>

<div class="flex h-full min-h-0 flex-col" data-work-records>
  <header
    class="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4"
  >
    <div>
      <h1 class="text-xl font-semibold">{t("work.title")}</h1>
      <p class="mt-1 text-xs text-muted">{t("work.description")}</p>
    </div>
    <div class="flex gap-2">
      <Button
        type="button" icon
        aria-label={t("work.refresh")}
        onclick={() => records.refetch()}><RefreshCw size={15} /></Button
      ><Button
        type="button"
        aria-pressed={showAssistant}
        onclick={() => (showAssistant = !showAssistant)}
        ><Bot size={16} />{t("work.assistant")}</Button
      >
    </div>
  </header>
  <div class="flex min-h-0 flex-1">
    <section class="flex min-h-0 min-w-0 flex-1 flex-col">
      <form
        class="grid gap-3 border-b border-border p-4"
        onsubmit={(e) => {
          e.preventDefault();
          apply();
        }}
      >
        <FieldGroup data-field-group="work-search">
          <Field label={t("work.search")}><Input
              bind:value={search}
              placeholder={t("work.search")}
              maxlength={500}
            /></Field>
          <Field label={t("work.project")}><Select bind:value={projectID}
              ><option value="">{t("work.allProjects")}</option>{#each context.projects as p}<option
                  value={p.id}>{p.name}</option
                >{/each}</Select
             ></Field>
          {#snippet actions()}<Button type="submit" variant="primary" class="text-xs"
             ><Search size={14} />{t("work.apply")}</Button
          >{/snippet}
        </FieldGroup>
        <FieldGroup data-field-group="work-dates">
          <Field label={t("work.from")}><DatePicker type="datetime-local" bind:value={from} /></Field>
          <Field label={t("work.until")}><DatePicker type="datetime-local" bind:value={until} /></Field>
        </FieldGroup>
        {#if advanced}<div class="max-h-72 overflow-auto">
            <WorkFilterBuilder bind:value={condition} />
          </div>
          <label class="flex items-center gap-2 text-xs"
            ><Checkbox type="checkbox" bind:checked={includeChanges} />{t(
              "work.changes"
            )}</label
          >{/if}
        <div class="flex flex-wrap gap-2">
          <Button type="button" class="text-xs" aria-expanded={advanced} onclick={() => (advanced = !advanced)}><Filter size={14} />{t("work.advanced")}</Button>
          <Button type="button" class="text-xs" onclick={reset}>{t("work.reset")}</Button>
          <Select
            class="!w-auto max-w-full text-xs"
            aria-label={t("work.saved")}
            onchange={(e) => {
              const f = filters.data?.items.find((f) => f.id === e.currentTarget.value);
              if (f) load(f);
            }}
            ><option value="">{t("work.saved")}</option
            >{#each filters.data?.items ?? [] as f}<option value={f.id}>{f.name}</option
              >{/each}</Select
          ><Input
            class="!w-36"
            aria-label={t("work.filterName")}
            placeholder={t("work.filterName")}
            bind:value={filterName}
            maxlength={100}
          /><Button
            type="button"
            disabled={!filterName.trim() || saveFilter.isPending}
            onclick={() => saveFilter.mutate()}>{t("work.saveFilter")}</Button
          >
          {#if advanced}{#each filters.data?.items ?? [] as f}<Button
                type="button"
                class="text-2xs"
                disabled={deleteFilter.isPending}
                aria-label={`${t("common.delete")} ${f.name}`}
                onclick={() => deleteFilter.mutate(f.id)}>{f.name} ×</Button
              >{/each}{/if}
        </div>
      </form>
      {#if records.error || saveFilter.error || deleteFilter.error}<div class="p-3">
          <ErrorNotice error={records.error ?? saveFilter.error ?? deleteFilter.error} />
        </div>{/if}
      <div class="flex-1 overflow-auto">
        <table class="w-full min-w-[900px] border-collapse text-left text-xs">
          <thead class="sticky top-0 z-10 whitespace-nowrap bg-surface-subtle text-2xs text-muted"
            ><tr
              ><th class="p-3">{t("work.detail")}</th><th class="p-3">{t("work.time")}</th><th
                class="p-3">{t("work.action")}</th
              ><th class="p-3">{t("work.object")}</th><th class="p-3">{t("work.project")}</th><th
                class="p-3">{t("work.outcome")}</th
              ><th class="p-3">{t("work.client")}</th></tr
            ></thead
          ><tbody>
            {#each records.data?.items ?? [] as record (record.id)}
              <tr
                class="border-b border-border hover:bg-surface-subtle"
                data-work-record={record.id}
              >
                <td class="p-3"
                  ><div class="flex items-center gap-2">
                    <Checkbox
                      type="checkbox"
                      aria-label={`${t("work.selected", { count: 1 })} ${record.id}`}
                      checked={selected.includes(record.id)}
                      onchange={(e) =>
                        (selected = e.currentTarget.checked
                          ? [...selected, record.id]
                          : selected.filter((id) => id !== record.id))}
                    /><button
                      class="grid size-7 place-items-center"
                      aria-label={`${t("work.detail")} ${record.id}`}
                      aria-expanded={expanded.includes(record.id)}
                      onclick={() => toggle(record.id)}
                      >{#if expanded.includes(record.id)}<ChevronDown
                          size={14}
                        />{:else}<ChevronRight size={14} />{/if}</button
                    >
                  </div></td
                >
                <td class="whitespace-nowrap p-3"
                  ><time>{new Date(record.occurred_at).toLocaleString()}</time></td
                >
                <td class="p-3"
                  ><strong class="whitespace-nowrap" title={record.action}
                    >{actionLabel(record)}</strong
                  ><span class="mt-1 block text-2xs text-muted"
                    >{record.actor_type === "user"
                      ? t("issues.you")
                      : record.actor_type === "agent"
                        ? t("issues.agent")
                        : t("logs.system")}</span
                  ></td
                >
                <td class="min-w-44 max-w-80 p-3"
                  ><span class="block break-words">{record.title || record.object_type}</span><code
                    class="text-2xs text-muted">{record.object_id}</code
                  ></td
                >
                <td class="p-3">{projectName(record) || "—"}</td><td class="p-3"
                  ><span
                    class={record.outcome === "failed"
                      ? "text-danger"
                      : record.outcome === "pending"
                        ? "text-warning"
                        : "text-muted"}
                    >{t(`status.${record.outcome}`).startsWith("status.")
                      ? record.outcome
                      : t(`status.${record.outcome}`)}</span
                  ></td
                >
                <td class="p-3"
                  ><span>{record.entry_type}</span><code
                    class="mt-1 block text-2xs text-muted"
                    title={`${record.client_id}\n${record.page_id}`}
                    >{record.client_id.slice(0, 8)} / {record.page_id.slice(0, 8)}</code
                  ></td
                >
              </tr>
              {#if expanded.includes(record.id)}<tr
                  ><td colspan="7"><WorkRecordDetail id={record.id} /></td></tr
                >{/if}
            {/each}
            {#if !records.isLoading && !records.data?.items.length}<tr
                ><td colspan="7" class="p-12 text-center text-muted">{t("work.empty")}</td></tr
              >{/if}
          </tbody>
        </table>
      </div>
      <footer
        class="flex flex-wrap items-center justify-between gap-2 border-t border-border p-3 text-xs"
      >
        <div>
          {t("work.total", { count: records.data?.total ?? 0 })} · {t("work.selected", {
            count: selected.length
          })}
        </div>
        <div class="flex gap-2">
          <Button
            type="button" variant="danger"
            disabled={!records.data?.total}
            onclick={() => {
              clear.reset();
              clearQuery = { ...query, until: query.until || new Date().toISOString() };
            }}><Trash2 size={13} />{t("work.clear")}</Button
          ><Button
            type="button"
            disabled={!query.offset}
            onclick={() => (query = { ...query, offset: Math.max(0, (query.offset ?? 0) - 50) })}
            >{t("work.previous")}</Button
          ><Button
            type="button"
            disabled={(query.offset ?? 0) + 50 >= (records.data?.total ?? 0)}
            onclick={() => (query = { ...query, offset: (query.offset ?? 0) + 50 })}
            >{t("work.next")}</Button
          >
        </div>
      </footer>
    </section>
    {#if showAssistant}<div
        class="w-[360px] shrink-0 max-[1100px]:fixed max-[1100px]:inset-y-0 max-[1100px]:right-0 max-[1100px]:z-50 max-[1100px]:w-[min(100vw,420px)] max-[1100px]:shadow-xl"
      >
        <WorkAssistant {query} {selected} onClose={() => (showAssistant = false)} />
      </div>{/if}
  </div>
</div>
{#if clearQuery}<ConfirmDialog
    title={t("work.clear")}
    description={t("work.clearHint")}
    confirmLabel={t("work.clear")}
    busy={clear.isPending}
    error={clear.error}
    onConfirm={() => clear.mutate()}
    onCancel={() => (clearQuery = null)}
  />{/if}
