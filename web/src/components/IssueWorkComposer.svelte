<script lang="ts">
  import { Button, Checkbox, Field, FieldGroup, Input, Modal, Select, Textarea } from "../ui";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { canMentionAgent } from "../lib/mentions";
  import { maximumIssueWorkItems, newIssueWorkItem, readIssueWorkPlan, workPlannerAgents } from "../lib/issueWork";
  import type { WorkValidation } from "../lib/issueWork";
  import type { Agent, AgentService, IssueWorkItemSpec } from "../lib/types";
  import ErrorNotice from "./ErrorNotice.svelte";

  let { issueId, input, agents, services, items, plannedInput, validation, submitting, onChange, onCancel, onPlanningChange }:
    { issueId: string; input: string; agents: Agent[]; services: AgentService[]; items: IssueWorkItemSpec[];
      plannedInput: string | null; validation?: WorkValidation; submitting: boolean;
      onChange: (items: IssueWorkItemSpec[], plannedInput: string | null) => void; onCancel: () => void;
      onPlanningChange: (pending: boolean) => void } = $props();

  const executors = $derived(agents.filter(canMentionAgent));
  const planners = $derived(workPlannerAgents(agents, services));
  let plannerId = $state("");
  let plannerName = $state("");
  const plannerAvailable = $derived(planners.some((agent) => agent.id === plannerId));
  let planning = $state(false);
  let replaceScope = $state<string | null>(null);
  let error = $state<unknown>();
  let invalidPlan = $state(false);
  let request: AbortController | undefined;
  const stale = $derived(plannedInput !== null && plannedInput !== input);
  const allowedIds = $derived([...new Set(items.map((item) => item.agent_id))].filter((id) => executors.some((agent) => agent.id === id)));

  $effect(() => {
    if (!plannerId && planners.length) plannerId = planners[0].id;
    const selected = agents.find((agent) => agent.id === plannerId);
    if (selected) plannerName = selected.name;
  });

  function scope() {
    return JSON.stringify([issueId, input, items, plannerId, allowedIds, planners.map((agent) => [agent.id, agent.current_revision_id, agent.agent_service_id])]);
  }
  $effect(() => {
    // Any edit or unmount cancels the old request; a server ignoring abort still cannot apply it.
    scope();
    return () => {
      request?.abort();
      request = undefined;
      planning = false;
      replaceScope = null;
      onPlanningChange(false);
    };
  });

  function change(index: number, patch: Partial<IssueWorkItemSpec>) {
    onChange(items.map((item, i) => i === index ? { ...item, ...patch } : item), plannedInput);
  }
  function remove(index: number) {
    const key = items[index].key;
    if (items.some((item) => item.after.includes(key))) return;
    onChange(items.filter((_, i) => i !== index), plannedInput);
  }

  function requestOrganization() {
    if (planning || submitting || !plannerAvailable || !input.trim() || !allowedIds.length) return;
    if (items.some((item) => item.title.trim() || item.instruction.trim() || item.after.length)) replaceScope = scope();
    else void organize();
  }

  function confirmReplacement() {
    const snapshot = replaceScope;
    replaceScope = null;
    if (snapshot === scope()) void organize();
  }

  async function organize() {
    if (planning || submitting || !plannerAvailable || !input.trim() || !allowedIds.length) return;
    const snapshot = scope();
    const controller = new AbortController();
    request = controller;
    planning = true;
    onPlanningChange(true);
    error = undefined;
    invalidPlan = false;
    try {
      const response = await api<unknown>(`/issues/${encodeURIComponent(issueId)}/work-plan`, {
        body: { agent_id: plannerId, input, agent_ids: allowedIds }, signal: controller.signal
      });
      if (controller.signal.aborted || request !== controller || snapshot !== scope()) return;
      const plan = readIssueWorkPlan(response, allowedIds);
      if (!plan) { invalidPlan = true; return; }
      onChange(plan, input);
    } catch (cause) {
      if (!controller.signal.aborted && request === controller && snapshot === scope()) error = cause;
    } finally {
      if (request === controller) {
        request = undefined;
        planning = false;
        onPlanningChange(false);
      }
    }
  }
</script>

<section class="grid max-h-[45dvh] min-w-0 gap-3 overflow-y-auto border-t border-border pt-3" aria-label={t("issueWork.title")} data-testid="issue-work-composer">
  <div class="flex flex-wrap items-center gap-2">
    <strong class="text-sm">{t("issueWork.title")}</strong>
    <Button controlSize="sm" class="ml-auto" disabled={submitting} onclick={onCancel}>{t("issueWork.cancel")}</Button>
  </div>
  <p class="m-0 text-xs text-muted">{t("issueWork.background")}</p>
  <div class="flex flex-wrap items-center gap-2" data-work-planner-toolbar>
    <Select controlSize="sm" bind:value={plannerId} aria-label={t("issueWork.planner")} disabled={!planners.length || submitting}>
      {#if plannerId && !plannerAvailable}<option value={plannerId} disabled>{t("issueWork.plannerUnavailable", { agent: plannerName || plannerId })}</option>
      {:else if !planners.length}<option value="">—</option>{/if}
      {#each planners as agent (agent.id)}<option value={agent.id}>{agent.name}</option>{/each}
    </Select>
    <Button controlSize="sm" disabled={planning || submitting || !plannerAvailable || !input.trim() || !allowedIds.length} aria-busy={planning} onclick={requestOrganization}>
      {planning ? t("issueWork.organizing") : t("issueWork.organize")}
    </Button>
  </div>
  {#if !planners.length}<p class="m-0 text-xs text-muted">{t("issueWork.noPlanner")}</p>{/if}
  {#if error}<ErrorNotice {error} />{/if}
  {#if invalidPlan}<p class="m-0 text-xs text-danger" role="alert">{t("issueWork.invalidPlan")}</p>{/if}
  {#if stale}
    <div class="grid gap-2 text-xs text-warning" role="alert">
      <p class="m-0">{t("issueWork.stale")}</p>
      <Button controlSize="sm" class="justify-self-start" disabled={submitting} onclick={() => onChange(items, null)}>{t("issueWork.keepManual")}</Button>
    </div>
  {:else if plannedInput !== null}<p class="m-0 text-xs text-muted">{t("issueWork.review")}</p>{/if}
  {#each items as item, index (item.key)}
    {@const referencedBy = items.flatMap((other, otherIndex) => other.after.includes(item.key) ? [other.title || t("issueWork.item", { index: otherIndex + 1 })] : [])}
    <fieldset class="grid min-w-0 gap-2 rounded-md border border-border p-3" data-work-item-key={item.key}>
      <legend class="px-1 text-xs font-semibold">{t("issueWork.item", { index: index + 1 })}</legend>
      <FieldGroup data-field-group="issue-work-owner-title">
        <Field label={t("issueWork.owner")}>
          <Select value={item.agent_id} onchange={(event) => change(index, { agent_id: event.currentTarget.value })}>
            {#if !executors.some((agent) => agent.id === item.agent_id)}<option value={item.agent_id}>{item.agent_id || "—"}</option>{/if}
            {#each executors as agent (agent.id)}<option value={agent.id}>{agent.name}</option>{/each}
          </Select>
        </Field>
        <Field label={t("issueWork.workTitle")}>
          <Input value={item.title} oninput={(event) => change(index, { title: event.currentTarget.value })} aria-invalid={!item.title.trim() || Array.from(item.title).length > 120} />
        </Field>
      </FieldGroup>
      <details>
        <summary class="cursor-pointer text-xs text-secondary">{t("issueWork.instruction")}</summary>
        <Textarea class="mt-2" rows={2} value={item.instruction} aria-label={`${t("issueWork.item", { index: index + 1 })} · ${t("issueWork.instruction")}`} oninput={(event) => change(index, { instruction: event.currentTarget.value })} />
      </details>
      <details>
        <summary class="cursor-pointer text-xs text-secondary">{item.after.length ? t("issueWork.waitForAll", { count: item.after.length }) : t("issueWork.immediate")}</summary>
        <div class="mt-2 grid gap-2">
          {#each items as dependency, otherIndex (dependency.key)}
            {#if otherIndex !== index}
              <label class="flex min-w-0 cursor-pointer items-center gap-2 text-xs">
                <Checkbox checked={item.after.includes(dependency.key)} onchange={(event) => change(index, { after: event.currentTarget.checked ? [...item.after, dependency.key] : item.after.filter((key) => key !== dependency.key) })} />
                <span class="min-w-0 break-words">{dependency.title || t("issueWork.item", { index: otherIndex + 1 })}</span>
              </label>
            {/if}
          {/each}
          {#if items.length < 2}<span class="text-xs text-muted">{t("issueWork.noDependencies")}</span>{/if}
        </div>
      </details>
      {#if referencedBy.length}<p class="m-0 text-xs text-warning">{t("issueWork.referencedBy", { items: referencedBy.join(", ") })}</p>{/if}
      <Button controlSize="sm" class="justify-self-end" aria-label={t("issueWork.remove", { index: index + 1 })} disabled={submitting || Boolean(referencedBy.length)} onclick={() => remove(index)}>{t("common.delete")}</Button>
    </fieldset>
  {/each}
  {#if validation}<p class="m-0 text-xs text-danger" role="alert">{t(`issueWork.${validation === "title" ? "titleError" : validation === "instruction" ? "instructionError" : validation}`)}</p>{/if}
  <Button controlSize="sm" class="justify-self-start" disabled={submitting || items.length >= maximumIssueWorkItems} onclick={() => onChange([...items, newIssueWorkItem(items[0]?.agent_id ?? executors[0]?.id ?? "")], plannedInput)}>{t("issueWork.add")}</Button>
</section>

{#if replaceScope !== null}
  <Modal title={t("issueWork.replace")} onClose={() => replaceScope = null} initialFocus="[data-cancel-work-plan]">
    <p class="m-0 text-sm text-secondary">{t("issueWork.replaceHint")}</p>
    {#snippet footer()}
      <div class="flex flex-wrap justify-end gap-2">
        <Button data-cancel-work-plan onclick={() => replaceScope = null}>{t("common.cancel")}</Button>
        <Button variant="primary" onclick={confirmReplacement}>{t("common.confirm")}</Button>
      </div>
    {/snippet}
  </Modal>
{/if}
