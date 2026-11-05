<script lang="ts">
  import { Button, Input, Select, Textarea, FieldGroup } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    ArrowLeftRight,
    Bot,
    ExternalLink,
    GripVertical,
    HardDrive,
    LoaderCircle,
    Plus,
    ShieldCheck,
    Trash2,
    X
  } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { location } from "../lib/router.svelte";
  import { useUnsavedChanges } from "../lib/unsavedChanges.svelte";
  import { queryClient } from "../lib/query";
  import type {
    Agent,
    AgentRevision,
    AgentRevisionSkillPackage,
    ComputerSkillPackage,
    Page,
    SkillPackage
  } from "../lib/types";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Loading from "../components/Loading.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import Link from "../components/Link.svelte";
  import ModelPicker from "../ui/ModelPicker.svelte";
  import Modal from "../ui/Modal.svelte";
  import ModelAPISetup, { type ConfiguredModelProvider } from "../components/ModelAPISetup.svelte";
  import { providerName } from "../lib/helpers";
  import { useConsole } from "./shared.svelte";

  interface AgentRevisionResponse {
    revision: AgentRevision;
    skills: AgentRevisionSkillPackage[];
    skill_packages: Record<string, SkillPackage>;
  }

  type EditorMode = "create" | "edit";
  type SkillDragOrigin = "bound" | "local";

  const context = useConsole();
  const computer = $derived(context.computer);
  const computerAgents = $derived(
    context.services.filter((item) => item.computer_id === computer?.id)
  );

  const agents = createQuery<Page<Agent>>(() => ({
    queryKey: ["agents", computer?.id],
    queryFn: () => api<Page<Agent>>(`/computers/${computer!.id}/agents`),
    enabled: Boolean(computer),
    refetchInterval: 10_000
  }));
  const skillPackages = createQuery<Page<SkillPackage>>(() => ({
    queryKey: ["skill-packages"],
    queryFn: () => api<Page<SkillPackage>>("/skill-packages"),
    enabled: Boolean(computer)
  }));
  const installedSkills = createQuery<Page<ComputerSkillPackage>>(() => ({
    queryKey: ["computer-skill-packages", computer?.id],
    queryFn: () => api<Page<ComputerSkillPackage>>(`/computers/${computer!.id}/skill-packages`),
    enabled: Boolean(computer)
  }));

  const agentItems = $derived(agents.data?.items ?? []);
  const skillItems = $derived(skillPackages.data?.items ?? []);
  const readyServices = $derived(computerAgents.filter((item) => item.status === "ready"));

  function agentNote(item: Agent) {
    return item.note?.trim() ? item.note : t(item.system_role === "general"
      ? "scheduleEditor.builtinAgentNote" : "scheduleEditor.noAgentNote");
  }

  let editorMode = $state<EditorMode>("create");
  let editingId = $state("");
  let formName = $state("");
  let formNote = $state("");
  let formServiceId = $state("");
  let formModel = $state("");
  let formPrompt = $state("");
  let boundSkills = $state<string[]>([]);
  let boundSkillDetails = $state<Record<string, SkillPackage>>({});
  let pendingDelete = $state<Agent | null>(null);
  let revisionError: unknown = $state(null);
  let revisionRequest = 0;
  let revisionLoading = $state(false);
  let formVersion = $state(0);
  let pendingEditor = $state<{ kind: "create" } | { kind: "edit"; id: string } | null>(null);
  let initializedComputerId = $state("");
  let dragSkill = $state<{ id: string; origin: SkillDragOrigin } | null>(null);
  let formBaseline = $state("");
  let apiSetupOpen = $state(false);
  let apiSetupBusy = $state(false);
  let apiSetupTarget = "";

  const editing = $derived(agentItems.find((item) => item.id === editingId));
  // Built-in identity protection does not restrict Provider choices. Specific
  // assistant-session capabilities are validated at that session's entry point.
  const editableServices = $derived(computerAgents);
  let defaultedAgent = "";
  $effect(() => {
    if (!editing || editing.system_role !== "general" || editing.current_revision_id || formServiceId || defaultedAgent === editing.id) return;
    const builtin = editableServices.find((item) => item.provider === "opencode");
    if (builtin) { defaultedAgent = editing.id; formServiceId = builtin.id; }
  });
  const formFingerprint = $derived(
    JSON.stringify([formName, formNote, formServiceId, formModel, formPrompt, boundSkills])
  );
  const formDirty = $derived(formBaseline !== "" && formFingerprint !== formBaseline);
  useUnsavedChanges(() => formDirty);

  function markFormPristine() {
    formBaseline = formFingerprint;
  }
  const modelOptions = $derived(
    (computerAgents.find((item) => item.id === formServiceId)?.capabilities?.models as
      string[] | undefined) ?? []
  );
  const boundPackageIds = $derived(
    new Set(boundSkills.map((id) => boundSkillDetails[id]?.id ?? id))
  );
  const localSkills = $derived(
    (installedSkills.data?.items ?? [])
      .map((installation) => ({
        installation,
        skill: skillItems.find((item) => item.id === installation.skill_package_id)
      }))
      .filter((item) => !boundPackageIds.has(item.installation.skill_package_id))
  );

  function resetForm() {
    formName = "";
    formNote = "";
    formServiceId = readyServices[0]?.id ?? "";
    formModel = "";
    formPrompt = "";
    boundSkills = [];
    boundSkillDetails = {};
    revisionError = null;
    markFormPristine();
  }

  function openCreate() {
    revisionRequest += 1;
    revisionLoading = false;
    saveAgent.reset();
    editorMode = "create";
    editingId = "";
    resetForm();
  }

  async function openEdit(item: Agent) {
    const request = ++revisionRequest;
    saveAgent.reset();
    formVersion = item.version;
    editorMode = "edit";
    editingId = item.id;
    formName = item.name;
    formNote = item.note ?? "";
    formServiceId = item.agent_service_id || (item.system_role === "general"
      ? computerAgents.find((service) => service.provider === "opencode")?.id ?? ""
      : "");
    formModel = item.model ?? "";
    formPrompt = "";
    boundSkills = [];
    boundSkillDetails = {};
    revisionError = null;
    markFormPristine();

    if (!item.current_revision_id) {
      revisionLoading = false;
      revisionError =
        item.system_role === "general" ? null : new Error(t("errors.invalidResponse"));
      return;
    }
    revisionLoading = true;
    try {
      const data = await api<AgentRevisionResponse>(`/agent-revisions/${item.current_revision_id}`);
      if (request !== revisionRequest || editingId !== item.id) return;
      formModel = data.revision.model ?? item.model ?? "";
      formPrompt = data.revision.system_prompt ?? "";
      boundSkills = [...data.skills]
        .sort((left, right) => left.position - right.position)
        .map((entry) => entry.skill_package_version_id);
      boundSkillDetails = data.skill_packages ?? {};
      markFormPristine();
    } catch (error) {
      if (request === revisionRequest) revisionError = error;
    } finally {
      if (request === revisionRequest) revisionLoading = false;
    }
  }

  function requestEditor(next: { kind: "create" } | { kind: "edit"; id: string }) {
    if (saveAgent.isPending) return;
    if (next.kind === "edit" && next.id === editingId) return;
    if (formDirty) {
      pendingEditor = next;
      return;
    }
    changeEditor(next);
  }

  function changeEditor(next: { kind: "create" } | { kind: "edit"; id: string }) {
    if (next.kind === "create") openCreate();
    else {
      const item = agentItems.find((agent) => agent.id === next.id);
      if (item) void openEdit(item);
    }
    pendingEditor = null;
  }

  function cancelEdit() {
    if (editing) {
      void openEdit(editing);
    } else if (agentItems[0]) {
      void openEdit(agentItems[0]);
    } else {
      openCreate();
    }
  }

  let appliedAgentLink = $state("");
  $effect(() => {
    const computerId = computer?.id ?? "";
    const requested = new URLSearchParams(location.search).get("agent") ?? "";
    const target = agentItems.find((item) => item.id === requested);
    if (target && requested !== appliedAgentLink) {
      appliedAgentLink = requested;
      initializedComputerId = computerId;
      requestEditor({ kind: "edit", id: requested });
      return;
    }
    if (!computerId || !agents.data || initializedComputerId === computerId) return;
    if (agentItems[0]) {
      initializedComputerId = computerId;
      void openEdit(agentItems[0]);
    } else if (readyServices.length) {
      initializedComputerId = computerId;
      openCreate();
    }
  });

  const saveAgent = createMutation<Agent, Error, void>(() => ({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        name: formName.trim(),
        note: formNote.trim(),
        model: formModel.trim(),
        system_prompt: formPrompt,
        skill_version_ids: boundSkills
      };
      if (editorMode === "edit" && editing) {
        body.expected_version = formVersion;
        body.agent_service_id = formServiceId;
        return api<Agent>(`/agents/${editing.id}`, { method: "PATCH", body });
      }
      return api<Agent>(`/computers/${computer!.id}/agents`, {
        body: { ...body, agent_service_id: formServiceId }
      });
    },
    onSuccess: async (item) => {
      editorMode = "edit";
      editingId = item.id;
      formVersion = item.version;
      formName = item.name;
      formNote = item.note ?? "";
      formServiceId = item.agent_service_id;
      formModel = item.model ?? "";
      markFormPristine();
      queryClient.setQueryData<Page<Agent>>(["agents", item.computer_id], (current) => {
        const items = current?.items ?? [];
        return {
          ...current,
          items: items.some((agent) => agent.id === item.id)
            ? items.map((agent) => (agent.id === item.id ? item : agent))
            : [...items, item]
        };
      });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agents", item.computer_id] }),
        queryClient.invalidateQueries({ queryKey: ["agents-all"] }),
        queryClient.invalidateQueries({ queryKey: ["work-assistant"] })
      ]);
    }
  }));

  function openModelAPI() {
    apiSetupTarget = `${editorMode}:${editingId}`;
    apiSetupOpen = true;
  }

  async function chooseAPIModel(value: ConfiguredModelProvider) {
    if (!apiSetupOpen || apiSetupTarget !== `${editorMode}:${editingId}`) return;
    const service = computerAgents.find((item) => item.provider === "opencode");
    if (!service) throw new Error(t("onboarding.apiRuntimeUnavailable"));
    formServiceId = service.id;
    formModel = value.model_ref;
    // Choose a draft exactly like the other Agent fields; the single common
    // Save action commits the Agent, revision and Skill bindings together.
    apiSetupBusy = false;
    apiSetupOpen = false;
    void queryClient.invalidateQueries({ queryKey: ["agent-services", computer?.id] });
  }

  const deleteAgent = createMutation<void, Error, void>(() => ({
    mutationFn: () =>
      api<void>(`/agents/${pendingDelete!.id}?expected_version=${pendingDelete!.version}`, {
        method: "DELETE"
      }),
    onSuccess: async () => {
      const deletedId = pendingDelete?.id;
      pendingDelete = null;
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agents", computer?.id] }),
        queryClient.invalidateQueries({ queryKey: ["agents-all"] })
      ]);
      if (editingId === deletedId) {
        const next = (agents.data?.items ?? []).find((item) => item.id !== deletedId);
        if (next) void openEdit(next);
        else openCreate();
      }
    }
  }));

  function bindSkill(versionId: string, targetIndex = boundSkills.length) {
    if (revisionLoading || revisionError || saveAgent.isPending) return;
    const installation = (installedSkills.data?.items ?? []).find(
      (candidate) => candidate.skill_package_version_id === versionId
    );
    const detail = skillItems.find((candidate) => candidate.id === installation?.skill_package_id);
    if (detail) boundSkillDetails = { ...boundSkillDetails, [versionId]: detail };
    const without = boundSkills.filter((id) => id !== versionId);
    const index = Math.max(0, Math.min(targetIndex, without.length));
    without.splice(index, 0, versionId);
    boundSkills = without;
  }

  function unbindSkill(packageId: string) {
    if (revisionLoading || revisionError || saveAgent.isPending) return;
    boundSkills = boundSkills.filter((id) => id !== packageId);
    const { [packageId]: _removed, ...remainingDetails } = boundSkillDetails;
    boundSkillDetails = remainingDetails;
  }

  function skillDetail(packageId: string): SkillPackage | undefined {
    return (
      boundSkillDetails[packageId] ?? skillItems.find((candidate) => candidate.id === packageId)
    );
  }

  function startSkillDrag(event: DragEvent, id: string, origin: SkillDragOrigin) {
    dragSkill = { id, origin };
    event.dataTransfer?.setData("text/plain", id);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
  }

  function allowSkillDrop(event: DragEvent) {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  }

  function dropOnBound(event: DragEvent, targetIndex = boundSkills.length) {
    event.preventDefault();
    event.stopPropagation();
    const id = dragSkill?.id || event.dataTransfer?.getData("text/plain");
    if (!id) return;
    const sourceIndex = boundSkills.indexOf(id);
    const adjustedIndex =
      sourceIndex >= 0 && sourceIndex < targetIndex ? targetIndex - 1 : targetIndex;
    bindSkill(id, adjustedIndex);
    dragSkill = null;
  }

  function dropOnLocal(event: DragEvent) {
    event.preventDefault();
    const id = dragSkill?.id || event.dataTransfer?.getData("text/plain");
    if (id && boundSkills.includes(id)) unbindSkill(id);
    dragSkill = null;
  }
</script>

{#if !computer}
  <div class="grid h-full place-items-center p-4">
    <EmptyState title={t("agents.needComputer")} description={t("agents.needComputerDesc")} />
  </div>
{:else}
  <div class="flex h-full min-h-0 flex-col bg-surface">
    <PageHeader title={t("agents.title")} description={t("agents.desc")}>
      {#snippet actions()}
        <Button
          variant="primary"
          type="button"
          onclick={() => requestEditor({ kind: "create" })}
          disabled={!readyServices.length || agents.isLoading || saveAgent.isPending}
        >
          <Plus size={14} />{t("agents.newAgent")}
        </Button>
      {/snippet}
    </PageHeader>

    <div class="min-h-0 flex-1">
      <section
        class="grid h-full min-h-0 grid-cols-[300px_minmax(0,1fr)] overflow-hidden bg-surface max-lg:grid-cols-[260px_minmax(0,1fr)] max-md:grid-cols-1 max-md:grid-rows-[220px_minmax(0,1fr)]"
      >
        <aside
          class="flex min-h-0 flex-col border-r border-border bg-surface-subtle max-md:border-b max-md:border-r-0"
        >
          <header
            class="flex h-12 shrink-0 items-center justify-between border-b border-border px-4"
          >
            <strong class="text-xs font-semibold">{t("agents.existingAgents")}</strong>
            <span class="rounded-full bg-surface px-2 py-0.5 text-2xs tabular-nums text-muted"
              >{agentItems.length}</span
            >
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto p-2">
            {#if agents.error}
              <ErrorNotice error={agents.error} />
            {:else if agents.isLoading}
              <Loading block />
            {:else if !agentItems.length}
              <div class="px-2 py-6 text-center text-xs leading-relaxed text-muted">
                {t("agents.noAgentsDesc")}
              </div>
            {:else}
              <div class="grid gap-1">
                {#each agentItems as item (item.id)}
                  {@const service = computerAgents.find(
                    (candidate) => candidate.id === item.agent_service_id
                  )}
                  <button
                    class="group grid min-w-0 grid-cols-[34px_minmax(0,1fr)] items-center gap-2 rounded-sm border px-2 py-2 text-left transition-colors {editingId ===
                      item.id && editorMode === 'edit'
                      ? 'border-border-strong bg-surface shadow-[inset_3px_0_0_var(--color-accent)]'
                      : 'border-transparent bg-transparent hover:bg-surface-hover'}"
                    type="button"
                    data-agent-id={item.id}
                    onclick={() => requestEditor({ kind: "edit", id: item.id })}
                    disabled={saveAgent.isPending}
                  >
                    <span
                      class="grid size-[34px] place-items-center rounded-sm bg-surface text-secondary"
                      ><Bot size={16} /></span
                    >
                    <span class="min-w-0">
                      <span class="flex min-w-0 items-center gap-1.5">
                        <strong class="min-w-0 flex-1 truncate text-xs font-semibold">{item.name}</strong>
                        <span class="shrink-0 rounded-sm bg-surface px-1.5 py-0.5 text-2xs text-muted">{service?.provider ? providerName(service.provider) : t("scheduleEditor.unconfiguredAgent")}</span>
                        <span
                          class="size-2 shrink-0 rounded-full {item.status === 'active'
                            ? 'bg-success'
                            : 'bg-border-strong'}"
                          aria-label={item.status === "active"
                            ? t("agents.active")
                            : t("agents.disabled")}
                        ></span>
                      </span>
                      <span class="mt-1 block truncate text-2xs text-muted" title={agentNote(item)}>{agentNote(item)}</span>
                    </span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
          <footer class="shrink-0 border-t border-border p-2">
            <Button
              class="w-full"
              type="button"
              onclick={() => requestEditor({ kind: "create" })}
              disabled={!readyServices.length || agents.isLoading || saveAgent.isPending}
            >
              <Plus size={13} />{t("agents.newAgent")}
            </Button>
          </footer>
        </aside>

        <section class="min-h-0 min-w-0 overflow-hidden">
          {#if !readyServices.length && editorMode === "create"}
            <div class="grid h-full place-items-center p-8">
              <EmptyState
                title={t("agents.noReadyService")}
                description={t("agents.noReadyServiceDesc")}
              />
            </div>
          {:else}
            <form
              class="flex h-full min-h-0 flex-col"
              onsubmit={(event) => {
                event.preventDefault();
                if (!revisionLoading && !revisionError && !saveAgent.isPending) saveAgent.mutate();
              }}
            >
              <header
                class="flex min-h-12 shrink-0 items-center justify-between gap-3 border-b border-border px-4 py-2"
              >
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <strong class="truncate text-sm font-semibold"
                      >{editorMode === "edit"
                        ? t("agents.editAgent")
                        : t("agents.newAgent")}</strong
                    >
                    <span
                      class="inline-flex items-center gap-1 rounded-full bg-surface-subtle px-2 py-0.5 text-2xs text-muted"
                    >
                      <span
                        class="inline-grid size-3 shrink-0 place-items-center"
                        role="status"
                        aria-label={revisionLoading ? t("common.loading") : undefined}
                      >
                        {#if revisionLoading}<LoaderCircle
                            size={11}
                            class="animate-spin"
                          />{:else}<ShieldCheck size={11} />{/if}
                      </span>{t("agents.permissionProvider")}
                    </span>
                    {#if formDirty}
                      <span
                        class="inline-flex items-center gap-1 rounded-full bg-warning-subtle px-2 py-0.5 text-2xs font-medium text-warning"
                        >{t("agents.unsaved")}</span
                      >
                    {/if}
                  </div>
                  {#if editing}<p class="mt-0.5 truncate text-2xs text-muted">
                      {agentNote(editing)}
                    </p>{/if}
                  {#if editing?.system_role === "general"}<p class="text-2xs text-muted">{t("work.protected")}</p>{/if}
                </div>
                <fieldset
                  disabled={agents.isLoading || initializedComputerId !== computer.id || saveAgent.isPending || revisionLoading}
                  class="flex shrink-0 items-center gap-2"
                >
                  {#if editing && editing.system_role !== "general"}
                    <Button
                      controlSize="sm" variant="danger" icon
                      type="button"
                      aria-label={t("common.delete")}
                      onclick={() => {
                        deleteAgent.reset();
                        pendingDelete = editing;
                      }}><Trash2 size={14} /></Button
                    >
                  {/if}
                  <Button controlSize="sm"
                    type="button"
                    onclick={cancelEdit}>{t("common.cancel")}</Button
                  >
                  <Button type="submit" variant="primary" controlSize="sm"
                    disabled={!formName.trim() ||
                      Boolean(revisionError) ||
                      !formServiceId ||
                      saveAgent.isPending ||
                      (editorMode === "edit" && Boolean(editing?.current_revision_id) && !formDirty)}>{t("common.save")}</Button
                  >
                </fieldset>
              </header>

              <div class="min-h-0 flex-1 overflow-y-auto p-5 max-md:p-4">
                {#if saveAgent.error || revisionError}<ErrorNotice
                    error={saveAgent.error ?? revisionError}
                    onRetry={revisionError && editing ? () => openEdit(editing!) : undefined}
                  />{/if}
                <fieldset
                  aria-busy={revisionLoading}
                  disabled={agents.isLoading || initializedComputerId !== computer.id || revisionLoading || Boolean(revisionError) || saveAgent.isPending}
                  class="mx-auto grid min-w-0 max-w-[1180px] gap-5"
                >
                  <FieldGroup collapseAt="lg" data-field-group="agent-configuration">
                    <Field label={t("agents.name")}
                      ><Input bind:value={formName} maxlength={200} required /></Field
                    >
                    <Field label={t("agents.note")} hint={t("agents.noteHint")}
                      ><Input bind:value={formNote} /></Field
                    >
                    <Field label={t("agents.software")}>
                         <Select bind:value={formServiceId} required onchange={(event) => { formServiceId = event.currentTarget.value; formModel = ""; }}>
                          <option value="">—</option>
                          {#if formServiceId && !editableServices.some((item) => item.id === formServiceId)}<option value={formServiceId}>{formServiceId}</option>{/if}
                          {#each editableServices as service (service.id)}
                            <option value={service.id}
                              >{providerName(service.provider)} · {service.provider_version}</option
                            >
                          {/each}
                        </Select>
                    </Field>
                    <Field label={t("agents.model")} hint={t("agents.modelHint")}>
                      <ModelPicker
                        models={modelOptions}
                        bind:value={formModel}
                        placeholder={t("agents.modelPlaceholder")}
                      />
                    </Field>
                  </FieldGroup>
                  {#if computerAgents.some((service) => service.provider === "opencode")}
                    <Button type="button" class="w-fit text-xs text-accent" onclick={openModelAPI}>{t("onboarding.addModelProvider")}</Button>
                  {/if}

                  <p class="flex items-center gap-1.5 text-2xs leading-relaxed text-muted">
                    <ShieldCheck size={13} class="shrink-0" />{t("agents.permissionModeHint")}
                  </p>

                  <Field label={t("agents.systemPrompt")} hint={t("agents.systemPromptHint")}>
                    <Textarea
                      rows={5}
                      bind:value={formPrompt}
                      placeholder={t("agents.systemPromptPlaceholder")}></Textarea>
                  </Field>

                  <section
                    class="overflow-hidden rounded-md border border-border bg-surface-subtle"
                  >
                    <header
                      class="flex items-center justify-between gap-3 border-b border-border px-4 py-3"
                    >
                      <div>
                        <strong class="text-xs font-semibold">{t("mcp.skillsTab")}</strong>
                        <p class="mt-1 text-2xs text-muted">{t("agents.skillsTransferHint")}</p>
                      </div>
                      <div class="flex items-center gap-2">
                        <Link
                          to="/skills"
                          class="inline-flex min-h-7 items-center gap-1 rounded-sm border border-border bg-surface px-2.5 text-2xs text-secondary no-underline hover:bg-surface-hover hover:text-text"
                        >
                          {t("agents.manageLocalSkills")}<ExternalLink size={11} />
                        </Link>
                        <ArrowLeftRight size={16} class="text-muted" />
                      </div>
                    </header>
                    <div class="grid grid-cols-2 gap-3 p-3 max-lg:grid-cols-1">
                      <section
                        class="flex min-h-[250px] min-w-0 flex-col overflow-hidden rounded-sm border border-border bg-surface"
                        aria-label={t("agents.boundSkills")}
                        ondragover={allowSkillDrop}
                        ondrop={(event) => dropOnBound(event)}
                      >
                        <header
                          class="flex h-10 shrink-0 items-center justify-between border-b border-border px-3"
                        >
                          <strong class="text-xs font-semibold">{t("agents.boundSkills")}</strong>
                          <span class="text-2xs tabular-nums text-muted">{boundSkills.length}</span>
                        </header>
                        <div class="min-h-0 flex-1 overflow-y-auto p-2">
                          {#if !boundSkills.length}
                            <div
                              class="grid h-full min-h-40 place-items-center rounded-sm border border-dashed border-border-strong px-5 text-center text-2xs leading-relaxed text-muted"
                            >
                              {t("agents.noBoundSkills")}
                            </div>
                          {:else}
                            <ul class="grid gap-1.5">
                              {#each boundSkills as packageId, index (packageId)}
                                {@const skill = skillDetail(packageId)}
                                <li
                                  class="grid cursor-grab select-none grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-sm border border-border bg-surface px-2.5 py-2 text-xs active:cursor-grabbing"
                                  draggable={true}
                                  ondragstart={(event) => startSkillDrag(event, packageId, "bound")}
                                  ondragend={() => (dragSkill = null)}
                                  ondragover={allowSkillDrop}
                                  ondrop={(event) => dropOnBound(event, index)}
                                >
                                  <GripVertical size={13} class="shrink-0 text-muted" />
                                  <span class="min-w-0">
                                    <strong class="block truncate font-semibold"
                                      >{skill?.name ?? packageId}</strong
                                    >
                                    {#if skill?.description}
                                      <span class="mt-0.5 block truncate text-2xs text-muted"
                                        >{skill.description}</span
                                      >
                                    {/if}
                                  </span>
                                  <button
                                    class="grid size-6 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-muted hover:bg-surface-hover hover:text-text"
                                    type="button"
                                    aria-label={t("agents.removeSkill")}
                                    onclick={() => unbindSkill(packageId)}><X size={12} /></button
                                  >
                                </li>
                              {/each}
                            </ul>
                          {/if}
                        </div>
                      </section>

                      <section
                        class="flex min-h-[250px] min-w-0 flex-col overflow-hidden rounded-sm border border-border bg-surface"
                        aria-label={t("agents.localSkills")}
                        ondragover={allowSkillDrop}
                        ondrop={dropOnLocal}
                      >
                        <header
                          class="flex h-10 shrink-0 items-center justify-between border-b border-border px-3"
                        >
                          <strong class="text-xs font-semibold">{t("agents.localSkills")}</strong>
                          <span class="flex items-center gap-1 text-2xs text-muted"
                            ><HardDrive size={12} />{localSkills.length}</span
                          >
                        </header>
                        <div class="min-h-0 flex-1 overflow-y-auto p-2">
                          {#if skillPackages.error || installedSkills.error}
                            <ErrorNotice error={skillPackages.error ?? installedSkills.error} />
                          {:else if skillPackages.isLoading || installedSkills.isLoading}
                            <Loading block />
                          {:else if !localSkills.length}
                            <div
                              class="grid h-full min-h-40 place-items-center rounded-sm border border-dashed border-border px-5 text-center text-2xs leading-relaxed text-muted"
                            >
                              <span
                                >{t("agents.noLocalSkills")}<Link
                                  to="/skills"
                                  class="mt-2 block text-accent no-underline hover:underline"
                                  >{t("agents.goInstallSkills")}</Link
                                ></span
                              >
                            </div>
                          {:else}
                            <div class="grid gap-1.5" role="list">
                              {#each localSkills as item (item.installation.id)}
                                {@const skill = item.skill}
                                <div
                                  class="grid cursor-grab select-none grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-sm border border-border bg-surface px-2.5 py-2 text-xs active:cursor-grabbing"
                                  role="listitem"
                                  draggable={true}
                                  ondragstart={(event) =>
                                    startSkillDrag(
                                      event,
                                      item.installation.skill_package_version_id,
                                      "local"
                                    )}
                                  ondragend={() => (dragSkill = null)}
                                  ondblclick={() =>
                                    bindSkill(item.installation.skill_package_version_id)}
                                >
                                  <GripVertical size={13} class="shrink-0 text-muted" />
                                  <span class="min-w-0">
                                    <strong class="block truncate font-semibold"
                                      >{skill?.name ?? item.installation.skill_package_id}</strong
                                    >
                                    {#if skill?.description}<span
                                        class="mt-0.5 block truncate text-2xs text-muted"
                                        >{skill.description}</span
                                      >{/if}
                                  </span>
                                  <Button controlSize="sm"
                                    type="button"
                                    onclick={() =>
                                      bindSkill(item.installation.skill_package_version_id)}
                                    ><Plus size={12} />{t("agents.add")}</Button
                                  >
                                </div>
                              {/each}
                            </div>
                          {/if}
                        </div>
                      </section>
                    </div>
                  </section>
                </fieldset>
              </div>
            </form>
          {/if}
        </section>
      </section>
    </div>

    {#if pendingDelete}
      <ConfirmDialog
        title={t("agents.deleteAgent")}
        description={t("agents.deleteConfirm", { name: pendingDelete.name })}
        confirmLabel={t("common.delete")}
        danger
        busy={deleteAgent.isPending}
        error={deleteAgent.error}
        onConfirm={() => deleteAgent.mutate()}
        onCancel={() => (pendingDelete = null)}
      />
    {/if}
    {#if apiSetupOpen}
      <Modal title={t("onboarding.stepAPI")} closeDisabled={apiSetupBusy} onClose={() => { if (!apiSetupBusy) apiSetupOpen = false; }}>
        <ModelAPISetup onSaved={chooseAPIModel} onReadyChange={() => {}} onBusyChange={(value) => { apiSetupBusy = value; }} />
      </Modal>
    {/if}
    {#if pendingEditor}
      <ConfirmDialog
        title={t("common.unsavedChanges")}
        description={t("common.unsavedChanges")}
        confirmLabel={t("common.confirm")}
        danger={false}
        onConfirm={() => {
          if (pendingEditor) changeEditor(pendingEditor);
        }}
        onCancel={() => (pendingEditor = null)}
      />
    {/if}
  </div>
{/if}
