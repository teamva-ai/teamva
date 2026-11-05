
<script lang="ts">
  import { Button, Input, Select, Textarea, SchedulePicker, FieldGroup } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { CalendarClock, Pause, Pencil, Play, Plus, RotateCcw } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api, idempotencyKey } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type { Issue, Page, Run, ScheduledTask, ScheduledTaskRequest } from "../lib/types";
  import { localeInfo } from "../lib/i18n.svelte";
  import { scheduleValid, scheduleSummary, scheduleInstantLabel, type ScheduleConfig } from "../lib/scheduleConfig";
  import { location, navigate } from "../lib/router.svelte";
  import { scheduleErrorCode, scheduleRunTarget, type ScheduleHistoryRun } from "../lib/scheduleHistory";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Loading from "../components/Loading.svelte";
  import Modal from "../ui/Modal.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import { textContent, triggerKeyLabel } from "../lib/helpers";
  import { toastSuccess } from "../lib/toast.svelte";
  import RequireProject from "../components/RequireProject.svelte";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const editorFormId = $props.id();
  const project = $derived(context.project);
  const localAgents = $derived(context.agents ?? []);
  const defaultTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai";
  function defaultSchedule(): ScheduleConfig {
    return { kind: "daily", expression: "", times: ["09:00"], weekdays: [1], timezone: defaultTimezone };
  }
  function taskSchedule(task: ScheduledTask): ScheduleConfig {
    return { kind: task.schedule_kind, expression: task.schedule_expression ?? "", times: task.times?.slice() ?? ["09:00"], weekdays: task.weekdays?.slice() ?? [1], timezone: task.timezone };
  }
  let editorOpen = $state(false);
  let editingTask = $state<ScheduledTask | null>(null);
  let historyTask = $state<ScheduledTask | null>(null);
  // Shared create/edit form state.
  let modalName = $state("");
  let nameEdited = $state(false);
  let modalSchedule = $state<ScheduleConfig>(defaultSchedule());
  let modalIssueMode = $state<"new" | "existing">("new");
  let modalIssueId = $state("");
  let issueSearch = $state("");
  let editorToken = $state(0);
  let modalPrompt = $state("");
  let modalAgentId = $state("");
  let modalPreview = $state<{ times: string[]; dst_note: string } | null>(null);
  let activeProjectId = $state("");
  let debouncedSchedule = $state<ScheduleConfig | null>(null);

  // 表达式输入后短暂防抖再预览，避免每个字符都请求。
  $effect(() => {
    const value = { ...modalSchedule, times: [...modalSchedule.times], weekdays: [...modalSchedule.weekdays] };
    const timer = setTimeout(() => { debouncedSchedule = scheduleValid(value) ? value : null; }, 300);
    return () => clearTimeout(timer);
  });

  function resetEditorFields() {
    modalName = "";
    nameEdited = false;
    modalSchedule = defaultSchedule();
    modalIssueMode = "new";
    modalIssueId = "";
    issueSearch = "";
    modalPrompt = "";
    modalAgentId = "";
    modalPreview = null;
  }

  function openCreateEditor() {
    editorToken++;
    createTask.reset(); updateTask.reset();
    editingTask = null;
    resetEditorFields();
    editorOpen = true;
  }

  function openEditEditor(task: ScheduledTask) {
    editorToken++;
    createTask.reset(); updateTask.reset();
    editingTask = task;
    modalName = task.name;
    nameEdited = false;
    modalSchedule = taskSchedule(task);
    modalIssueMode = task.issue_mode === "existing" ? "existing" : "new";
    modalIssueId = task.issue_id ?? "";
    issueSearch = "";
    modalPrompt = textContent(task.prompt);
    modalAgentId = task.agent_id ?? "";
    modalPreview = null;
    editorOpen = true;
  }

  function closeEditor() {
    editorOpen = false;
    editingTask = null;
    resetEditorFields();
  }

  const tasks = createQuery<Page<ScheduledTask>>(() => ({
    queryKey: ["schedules", project?.id],
    queryFn: ({ signal, queryKey }) => api<Page<ScheduledTask>>(`/projects/${encodeURIComponent(String(queryKey[1]))}/scheduled-tasks`, { signal }),
    enabled: Boolean(project),
    refetchInterval: 2_000
  }));
  $effect(() => {
    if (!editingTask) return;
    const latest = tasks.data?.items.find((task) => task.id === editingTask?.id && task.project_id === project?.id);
    const body = (task: ScheduledTask) => JSON.stringify({ ...task, name: "", version: 0, updated_at: "" });
    if (latest && latest.version > editingTask.version && body(latest) === body(editingTask)) {
      editingTask = latest;
      if (!nameEdited) modalName = latest.name;
    }
  });
  const requestedScheduleId = $derived(new URLSearchParams(location.search).get("schedule") ?? "");
  let appliedScheduleFocus = $state("");
  $effect(() => {
    const id = requestedScheduleId;
    const key = `${project?.id ?? ""}:${id}`;
    if (activeProjectId !== project?.id) return;
    if (!id) { appliedScheduleFocus = ""; return; }
    if (appliedScheduleFocus === key) return;
    const task = tasks.data?.items.find((item) => item.id === id);
    if (!task) return;
    appliedScheduleFocus = key;
    historyTask = task;
  });
  const scheduleMissing = $derived(Boolean(requestedScheduleId && tasks.isSuccess &&
    !tasks.data?.items.some((item) => item.id === requestedScheduleId)));
  function submitSchedule(event: SubmitEvent) {
    event.preventDefault();
    if (!project || !canSave) return;
    const body: ScheduleRequest = {
      ...(editingTask && nameEdited ? { name: modalName.trim() } : {}), ...timingRequest(modalSchedule),
      prompt: [{ type: "text", text: modalPrompt.trim() }], agent_id: modalAgentId,
      issue_mode: modalIssueMode, issue_id: modalIssueMode === "existing" ? modalIssueId : "",
      concurrency_policy: editingTask?.concurrency_policy === "allow" ? "allow" : "forbid",
      misfire_policy: editingTask?.misfire_policy === "run_once" ? "run_once" : "skip",
      ...(editingTask ? { expected_version: editingTask.version } : {})
    };
    const input = { body, projectId: project.id, taskId: editingTask?.id, token: editorToken };
    editingTask ? updateTask.mutate(input) : createTask.mutate(input);
  }

  function timingRequest(value: ScheduleConfig) {
    return {
      schedule_kind: value.kind, schedule_expression: value.kind === "once" || value.kind === "cron" ? value.expression.trim() : "",
      timezone: value.timezone,
      ...(["daily", "weekly"].includes(value.kind) ? { times: [...new Set(value.times)].sort() } : {}),
      ...(value.kind === "weekly" ? { weekdays: [...new Set(value.weekdays)].sort((a, b) => a - b) } : {})
    };
  }

  $effect(() => {
    const nextProjectId = project?.id ?? "";
    if (activeProjectId === nextProjectId) return;
    const previousProjectId = activeProjectId;
    activeProjectId = nextProjectId;
    editorOpen = false;
    editingTask = null;
    historyTask = null;
    resetEditorFields();
    if (previousProjectId) {
      void queryClient.cancelQueries({ queryKey: ["schedules", previousProjectId] });
    }
  });

  const toggle = createMutation<ScheduledTask, Error, ScheduledTask>(() => ({
    mutationFn: (item) =>
      api<ScheduledTask>(`/scheduled-tasks/${item.id}/${item.enabled ? "pause" : "resume"}`, {
        body: { expected_version: item.version }
      }),
    onSuccess: (_updated, input) => {
      queryClient.invalidateQueries({ queryKey: ["schedules", input.project_id] });
    }
  }));
  const runNow = createMutation<void, Error, ScheduledTask>(() => ({
    mutationFn: (item) => api(`/scheduled-tasks/${item.id}/run-now`, {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey("schedule") }
      }),
    onSuccess: () => {
      toastSuccess(t("schedules.triggered"));
    }
  }));
  const previewQuery = createQuery<{ times: string[]; dst_note: string }>(() => ({
    queryKey: ["schedule-preview", debouncedSchedule],
    queryFn: () =>
      api("/scheduled-tasks/preview", {
        body: { ...timingRequest(debouncedSchedule!), count: 5 }
      }),
    enabled: editorOpen && Boolean(debouncedSchedule),
    retry: false
  }));
  const previewCurrent = $derived(Boolean(debouncedSchedule && JSON.stringify(debouncedSchedule) === JSON.stringify(modalSchedule)));
  $effect(() => {
    modalPreview = previewCurrent ? previewQuery.data ?? null : null;
  });
  type ScheduleRequest = Omit<ScheduledTaskRequest, "name"> & { name?: string };
  type SaveInput = { body: ScheduleRequest; projectId: string; taskId?: string; token: number };
  const createTask = createMutation<ScheduledTask, Error, SaveInput>(() => ({
    mutationFn: (input) => api<ScheduledTask>(`/projects/${input.projectId}/scheduled-tasks`, { body: input.body }),
    onSuccess: (_task, input) => {
      if (project?.id === input.projectId && editorToken === input.token) closeEditor();
      toastSuccess(t("schedules.created"));
      queryClient.invalidateQueries({ queryKey: ["schedules", input.projectId] });
    }
  }));
  const updateTask = createMutation<ScheduledTask, Error, SaveInput>(() => ({
    mutationFn: (input) => api<ScheduledTask>(`/scheduled-tasks/${input.taskId}`, { method: "PATCH", body: input.body }),
    onSuccess: (_task, input) => {
      if (project?.id === input.projectId && editorToken === input.token) closeEditor();
      toastSuccess(t("schedules.updated"));
      queryClient.invalidateQueries({ queryKey: ["schedules", input.projectId] });
    }
  }));
  const error = $derived(tasks.error ??
    (toggle.variables?.project_id === project?.id ? toggle.error : null) ??
    (runNow.variables?.project_id === project?.id ? runNow.error : null));
   const selectableComputerAgents = $derived(localAgents.filter((item) => item.status === "active" && item.computer_id === project?.computer_id));
   // Lumi uses the same configured-Agent rule as every other Agent. Built-in
   // identity protection must not prevent scheduled Issue work.
   const readyAgents = $derived(selectableComputerAgents.filter((item) => Boolean(item.current_revision_id)));
   const hasUnconfiguredAgents = $derived(selectableComputerAgents.some((item) => !item.current_revision_id));
  const issues = createQuery<Page<Issue>>(() => ({
    queryKey: ["schedule-issues", project?.id],
    queryFn: ({ signal }) => api<Page<Issue>>(`/projects/${project!.id}/issues`, { signal }),
    enabled: editorOpen && modalIssueMode === "existing" && Boolean(project), retry: false
  }));
  const eligibleIssues = $derived((issues.data?.items ?? []).filter((item) => item.project_id === project?.id && item.status === "open"));
  const visibleIssues = $derived(eligibleIssues.filter((item) => `${item.title} ${item.id}`.toLocaleLowerCase().includes(issueSearch.trim().toLocaleLowerCase())).slice(0, 100));
  const selectedIssue = $derived(eligibleIssues.find((item) => item.id === modalIssueId));
  const saving = $derived(createTask.isPending || updateTask.isPending);
   const canSave = $derived(Boolean((!editingTask || !nameEdited || modalName.trim()) && modalPrompt.trim() && readyAgents.some((item) => item.id === modalAgentId) && scheduleValid(modalSchedule) && !(previewCurrent && previewQuery.error) && !saving && (modalIssueMode === "new" || (selectedIssue && !issues.isLoading && !issues.error))));
  const historyRuns = createQuery<Page<ScheduleHistoryRun>>(() => ({
    queryKey: ["schedule-runs", historyTask?.id],
    queryFn: ({ signal, queryKey }) => api<Page<ScheduleHistoryRun>>(`/scheduled-tasks/${encodeURIComponent(String(queryKey[1]))}/runs`, { signal }),
    enabled: Boolean(historyTask),
    refetchInterval: (query) =>
      (query.state.data as Page<ScheduleHistoryRun> | undefined)?.items.some((item) => !isScheduleTerminal(item.status))
        ? 1_000 : false
  }));
  const openHistoryRun = createMutation<string, Error, { runId: string; projectId: string; taskId: string }>(() => ({
    mutationFn: async ({ runId, projectId }) => {
      const run = await api<Run>(`/runs/${encodeURIComponent(runId)}`);
      const target = scheduleRunTarget(run, projectId, runId);
      if (!target) throw new Error(t("schedules.runUnavailable"));
      return target;
    },
    onSuccess: (target, input) => {
      if (project?.id === input.projectId && historyTask?.id === input.taskId) navigate(target);
    }
  }));

  function isScheduleTerminal(status: string): boolean {
    return ["succeeded", "failed", "cancelled", "lost", "skipped", "skipped_misfire", "skipped_concurrency"].includes(status);
  }
</script>

<RequireProject>
  <div class="flex h-full min-h-0 flex-col">
  <PageHeader
    title={t("schedules.title")}
    description={t("schedules.desc")}
  >
    {#snippet actions()}
      <Button type="button" variant="primary" onclick={openCreateEditor}><Plus size={14} />{t("schedules.newTask")}</Button>
    {/snippet}
  </PageHeader>
  {#if error}<div class="border-b border-border px-5 py-3"><ErrorNotice {error} /></div>{/if}
  {#if scheduleMissing}<p role="status" class="border-b border-border px-5 py-3 text-sm text-warning">{t("schedules.notFound")}</p>{/if}
  {#if tasks.isLoading}
    <Loading block />
  {:else}
    <section class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden bg-surface">
      <div class="flex items-center justify-between gap-4 border-b border-border px-5 py-4">
               <div class="min-w-0">
          <h2 class="m-0 text-md font-semibold tracking-[-0.01em]">{t("schedules.taskList")}</h2>
          <p class="mt-1.5 text-sm leading-[1.5] text-secondary text-pretty">{t("schedules.taskListDesc")}</p>
        </div>
      </div>
      {#if !tasks.data?.items.length}
        <EmptyState title={t("schedules.noTasks")} description={t("schedules.noTasksDesc")}>
          {#snippet action()}
            <Button type="button" variant="primary" onclick={openCreateEditor}><Plus size={14} />{t("schedules.newTask")}</Button>
          {/snippet}
        </EmptyState>
      {:else}
        <div class="grid">
          {#each tasks.data.items as task (task.id)}
            {@const agent = localAgents.find((item) => item.id === task.agent_id)}
            <article class="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 border-b border-border px-5 py-4 last:border-b-0 max-md:grid-cols-[auto_1fr]" class:search-target-flash={requestedScheduleId === task.id} data-schedule-id={task.id}>
              <div class="grid size-[34px] place-items-center rounded-sm bg-accent-subtle text-accent"><CalendarClock size={18} /></div>
              <div>
                <div class="flex flex-wrap items-center gap-2"><strong class="break-words">{task.name}</strong><StatusBadge status={task.enabled ? "enabled" : "disabled"} /></div>
                <p class="my-[5px] line-clamp-2 whitespace-pre-wrap break-words text-xs">{textContent(task.prompt)}</p>
                <small class="text-xs text-muted">{t(task.schedule_kind === "daily" ? "scheduleEditor.daily" : task.schedule_kind === "weekly" ? "scheduleEditor.weekly" : task.schedule_kind === "once" ? "schedules.once" : "schedules.cron")} · {scheduleSummary(taskSchedule(task), localeInfo.value)} · {agent?.name ?? task.agent_id}</small>
                {#if task.issue_mode === "new" || task.issue_mode === "existing"}<p class="mt-1 text-xs text-muted">{task.issue_mode === "existing" ? `${t("scheduleEditor.existingIssueLabel")} · ${task.issue_id}` : t("scheduleEditor.newIssueEachRun")}</p>{/if}
                {#if task.last_dispatch_error}<p role="status" class="mt-1 text-xs text-warning">{t("schedules.lastError")}: <code>{scheduleErrorCode(task.last_dispatch_error)}</code></p>{/if}
              </div>
              <div class="grid justify-items-end gap-2 text-[11px] text-muted max-md:col-span-2 max-md:justify-items-start">
                <span>{t("schedules.nextRun", { time: scheduleInstantLabel(task.next_run_at, task.timezone, localeInfo.value) })}</span>
                <div class="flex flex-wrap justify-end gap-2 max-md:justify-start">
                  <Button type="button" onclick={() => openEditEditor(task)}><Pencil size={13} />{t("schedules.edit")}</Button>
                  <Button type="button" onclick={() => (historyTask = task)}>{t("schedules.history")}</Button>
                  <Button type="button" disabled={!task.enabled || runNow.isPending} loading={runNow.isPending && runNow.variables?.id === task.id} onclick={() => runNow.mutate(task)}><RotateCcw size={13} />{t("schedules.runNow")}</Button>
                  <Button type="button" disabled={toggle.isPending} onclick={() => toggle.mutate(task)}>
                    {#if task.enabled}<Pause size={13} />{:else}<Play size={13} />{/if}
                    {task.enabled ? t("schedules.pause") : t("schedules.resume")}
                  </Button>
                </div>
              </div>
            </article>
          {/each}
        </div>
      {/if}
    </section>
  {/if}

  </div>
  {#if editorOpen}
    <Modal
      title={editingTask ? t("schedules.editTitle") : t("schedules.createTitle")}
      description={editingTask ? t("schedules.editDesc") : t("schedules.createDesc")}
      closeDisabled={saving}
      maxWidth={1080}
      data-schedule-editor-dialog
      onClose={closeEditor}
    >
      {#if (previewCurrent && previewQuery.error) || createTask.error || updateTask.error}<ErrorNotice error={(previewCurrent && previewQuery.error) || createTask.error || updateTask.error} />{/if}
      <form id={editorFormId} class="grid gap-4" aria-label={editingTask ? t("schedules.editTitle") : t("schedules.createTitle")} onsubmit={submitSchedule}>
        <fieldset class="schedule-editor-layout" data-schedule-editor-layout disabled={saving}>
        <div class="schedule-editor-configuration" data-schedule-editor-configuration>
        <p class="rounded-sm border border-border bg-surface-subtle p-3 text-xs leading-relaxed text-secondary">{t("scheduleEditor.scopeHint")}</p>
        {#if editingTask}<Field label={t("schedules.taskName")}><Input bind:value={modalName} oninput={() => (nameEdited = true)} maxlength={200} placeholder={t("schedules.namePlaceholder")} required /></Field>{/if}
        <FieldGroup collapseAt="sm" data-field-group="schedule-target">
        <Field label={t("scheduleEditor.issueMode")}>
          <Select bind:value={modalIssueMode}>
            <option value="new">{t("scheduleEditor.issueNew")}</option>
            <option value="existing">{t("scheduleEditor.issueExisting")}</option>
          </Select>
        </Field>
        <Field label={t("schedules.agent")} hint={t("scheduleEditor.agentHint")}>
          <Select bind:value={modalAgentId} required>
            <option value="">{t("schedules.selectAgent")}</option>
            {#if modalAgentId && !selectableComputerAgents.some((item) => item.id === modalAgentId)}<option value={modalAgentId} disabled>{modalAgentId}</option>{/if}
            {#each selectableComputerAgents as item (item.id)}
              <option value={item.id} disabled={!readyAgents.some((agent) => agent.id === item.id)}>{[item.name, ...(!item.current_revision_id ? [t("scheduleEditor.unconfiguredAgent")] : [])].join(" · ")}</option>
            {/each}
          </Select>
        </Field>
        </FieldGroup>
        <p class="-mt-2 text-xs leading-relaxed text-muted">{t(modalIssueMode === "new" ? "scheduleEditor.issueNewHint" : "scheduleEditor.issueExistingHint")}</p>
        {#if hasUnconfiguredAgents}<p class="text-xs text-warning">{t("scheduleEditor.unconfiguredAgentsHint")} <a href="/agents" class="text-accent underline">{t("scheduleEditor.configureAgent")}</a></p>
        {:else if !selectableComputerAgents.length}<p class="text-xs text-warning">{t("scheduleEditor.noAgents")} <a href="/agents" class="text-accent underline">{t("scheduleEditor.configureAgent")}</a></p>{/if}
        {#if modalIssueMode === "existing"}
          {#if issues.error}<ErrorNotice error={issues.error} onRetry={() => void issues.refetch()} />{/if}
          <div class="grid gap-2 rounded-sm border border-border bg-surface-subtle p-3">
            <Input type="search" aria-label={t("scheduleEditor.issueSearch")} placeholder={t("scheduleEditor.issueSearch")} bind:value={issueSearch} />
            <Field label={t("scheduleEditor.selectIssue")}>
              <Select bind:value={modalIssueId} required disabled={issues.isLoading || Boolean(issues.error)}>
                <option value="">{issues.isLoading ? t("common.loading") : t("scheduleEditor.selectIssue")}</option>
                {#if modalIssueId && !visibleIssues.some((item) => item.id === modalIssueId)}<option value={modalIssueId} disabled={!selectedIssue}>{selectedIssue?.title ?? `${modalIssueId} · ${t("scheduleEditor.issueUnavailable")}`}</option>{/if}
                {#each visibleIssues as item (item.id)}<option value={item.id}>{item.title} · {item.id}</option>{/each}
              </Select>
            </Field>
            {#if issues.isSuccess && !eligibleIssues.length}<p class="text-xs text-muted">{t("scheduleEditor.issueMissing")}</p>{/if}
          </div>
        {/if}
        <div class="border-t border-border pt-4"><SchedulePicker bind:value={modalSchedule} disabled={saving} /></div>
        {#if modalPreview}
          <div class="grid gap-1.5 rounded-sm border border-border bg-surface-subtle p-3 text-[11px]">
            <strong>{t("schedules.futureTriggers")}</strong>
            {#each modalPreview.times as time (time)}
              <time datetime={time}>{new Date(time).toLocaleString(localeInfo.value, { timeZone: modalSchedule.timezone })}</time>
            {/each}
            <small class="text-muted">{modalPreview.dst_note}</small>
          </div>
        {:else if scheduleValid(modalSchedule) && (!previewCurrent || previewQuery.isLoading)}<p role="status" class="text-xs text-muted">{t("scheduleEditor.previewLoading")}</p>
        {:else if !scheduleValid(modalSchedule)}<p class="text-xs text-muted">{t("scheduleEditor.previewInvalid")}</p>
        {/if}
        <p class="text-xs leading-relaxed text-muted">{t("scheduleEditor.serverRunningHint")}</p>
        </div>
        <div class="schedule-editor-prompt" data-schedule-editor-prompt>
          <Field label={t("schedules.prompt")}><Textarea rows={16} bind:value={modalPrompt} maxlength={40000} placeholder={t("schedules.promptPlaceholder")} required /></Field>
        </div>
        </fieldset>
      </form>
      {#snippet footer()}
        <div class="flex flex-wrap justify-end gap-2">
          <span class="flex-1"></span>
          <Button type="button" disabled={saving} onclick={closeEditor}>{t("common.cancel")}</Button>
          <Button type="submit" form={editorFormId} variant="primary" loading={saving} disabled={!canSave}>
            {editingTask ? t("common.save") : t("schedules.create")}
          </Button>
        </div>
      {/snippet}
    </Modal>
  {/if}

  {#if historyTask}
    <Modal title={t("schedules.historyTitle", { name: historyTask.name })} description={t("schedules.historyDesc")} onClose={() => (historyTask = null)}>
      {#if historyRuns.error}<ErrorNotice error={historyRuns.error} />{/if}
      {#if openHistoryRun.variables?.taskId === historyTask.id && openHistoryRun.error}<ErrorNotice error={openHistoryRun.error} />{/if}
      {#if historyRuns.isLoading}
        <Loading block />
      {:else if !historyRuns.data?.items.length}
        <EmptyState title={t("schedules.notTriggered")} description={t("schedules.notTriggeredDesc")} />
      {:else}
        <div class="grid">
          {#each historyRuns.data.items as item (item.id)}
            <article class="flex flex-wrap items-center gap-3 border-b border-border py-4 last:border-b-0" data-schedule-run-id={item.id}>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2"><strong>{scheduleInstantLabel(item.scheduled_for, historyTask.timezone, localeInfo.value)}</strong><StatusBadge status={item.status} /></div>
                <small class="text-[11px] text-muted">{triggerKeyLabel(item.trigger_key)}</small>
                <dl class="mt-2 grid gap-1 text-xs text-muted">
                  <div><dt class="inline">{t("schedules.dispatchAttempts")}: </dt><dd class="inline">{Number.isSafeInteger(item.dispatch_attempts) && item.dispatch_attempts! >= 0 ? item.dispatch_attempts : 0}</dd></div>
                  {#if item.retry_at}<div><dt class="inline">{t("schedules.retryAt")}: </dt><dd class="inline">{scheduleInstantLabel(item.retry_at, historyTask.timezone, localeInfo.value)}</dd></div>{/if}
                  {#if item.last_error}<div><dt class="inline">{t("schedules.lastError")}: </dt><dd class="inline"><code>{scheduleErrorCode(item.last_error)}</code></dd></div>{/if}
                </dl>
              </div>
              {#if item.run_id}<Button type="button" disabled={openHistoryRun.isPending} onclick={() => {
                if (item.run_id && historyTask) openHistoryRun.mutate({ runId: item.run_id, projectId: historyTask.project_id, taskId: historyTask.id });
              }}>{t("schedules.openRun")}</Button>{/if}
            </article>
          {/each}
        </div>
      {/if}
    </Modal>
  {/if}

</RequireProject>

<style>
  .schedule-editor-layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    align-items: stretch;
    gap: 24px;
    min-width: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }
  .schedule-editor-configuration { display: grid; align-content: start; min-width: 0; gap: 16px; }
  .schedule-editor-prompt { display: grid; min-width: 0; padding-left: 24px; border-left: 1px solid var(--color-border); }
  .schedule-editor-prompt :global(.am-field) { grid-template-rows: auto minmax(0, 1fr); align-content: stretch; }
  .schedule-editor-prompt :global(.am-field__control) { align-self: stretch; min-height: 0; }
  .schedule-editor-prompt :global(.am-textarea) { height: 100%; min-height: 360px; }
  @media (max-width: 959px) {
    .schedule-editor-layout { grid-template-columns: minmax(0, 1fr); gap: 20px; }
    .schedule-editor-prompt { padding-left: 0; padding-top: 20px; border-left: 0; border-top: 1px solid var(--color-border); }
    .schedule-editor-prompt :global(.am-textarea) { height: auto; min-height: 240px; }
  }
</style>
