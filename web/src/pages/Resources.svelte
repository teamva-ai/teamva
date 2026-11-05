<script lang="ts">
  import { Button, Input } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    ChevronDown, Code2, FileText, FolderCheck, FolderOpen, Globe2, LoaderCircle, Plus,
    Pencil, RefreshCw, SquareTerminal, Trash2
  } from "lucide-svelte";
  import { tick } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import { location } from "../lib/router.svelte";
  import type { Page, ProjectResource, ResourceOpener, ResourceOpeners } from "../lib/types";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import DirectoryPicker from "../components/DirectoryPicker.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Loading from "../components/Loading.svelte";
  import Modal from "../ui/Modal.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import { formatTime, resourceKindLabel } from "../lib/helpers";
  import { gitRepositoriesQueryKey } from "../lib/gitChanges";
  import RequireProject from "../components/RequireProject.svelte";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const project = $derived(context.project);
  const computerId = $derived(context.computer?.id ?? "");
  let pendingDelete = $state<ProjectResource | null>(null);
  let modalKind = $state<ProjectResource["kind"]>("code_directory");
  let modalName = $state("");
  let modalPath = $state("");
  let modalURL = $state("");
  let modalOpen = $state(false);
  let modalSession = $state(0);
  let createPending = $state(false);
  let createError = $state<unknown>(null);
  let activeCreateRequest = 0;
  let requestSequence = 0;
  let editingResource = $state<ProjectResource | null>(null);
  let renameName = $state("");
  let renameSession = $state(0);
  let renamePending = $state(false);
  let renameError = $state<unknown>(null);
  let activeProjectId = $state("");
  let openMenuResource = $state<ProjectResource | null>(null);
  let openMenuPosition = $state<{ left: number; top: number } | null>(null);

  // 资源类型卡片：图标固定，标题/说明按当前语言在模板里取。
  const resourceTypeOptions = [
    { kind: "code_directory", icon: Code2 },
    { kind: "document_directory", icon: FileText },
    { kind: "url", icon: Globe2 }
  ] as const;

  interface CommandResult {
    status: string;
    result?: unknown;
  }

  async function pollCommand(commandId: string, targetComputerId: string): Promise<unknown> {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const data = await api<CommandResult>(`/computers/${targetComputerId}/directory-list/${commandId}`);
      if (data.status === "completed" && data.result !== undefined) return data.result;
      if (data.status === "expired" || data.status === "failed") {
        throw new Error(t("resources.pickerExpired"));
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(t("resources.pickerTimeout"));
  }

  function resetModal() {
    modalKind = "code_directory";
    modalName = "";
    modalPath = "";
    modalURL = "";
  }

  function openModal() {
    modalSession += 1;
    createPending = false;
    createError = null;
    resetModal();
    modalOpen = true;
  }

  function closeModal() {
    modalSession += 1;
    modalOpen = false;
    createPending = false;
    createError = null;
    resetModal();
  }

  function openRename(item: ProjectResource) {
    renameSession += 1;
    editingResource = item;
    renameName = item.name;
    renamePending = false;
    renameError = null;
  }

  function closeRename() {
    renameSession += 1;
    editingResource = null;
    renameName = "";
    renamePending = false;
    renameError = null;
  }

  $effect(() => {
    const id = project?.id ?? "";
    if (id === activeProjectId) return;
    activeProjectId = id;
    closeModal();
    closeRename();
    pendingDelete = null;
    closeOpenMenu();
  });

  function choosePath(path: string) {
    modalPath = path;
    if (!modalName.trim()) {
      modalName = path.replace(/\/+$/, "").split("/").pop() ?? "";
    }
  }

  // 切换资源类型时清空另一类型的输入，避免把 URL/路径带到错误的表单里。
  function chooseKind(kind: ProjectResource["kind"]) {
    if (modalKind === kind) return;
    modalKind = kind;
    modalPath = "";
    modalURL = "";
  }

  function isAbsoluteDirectoryPath(path: string) {
    return path.startsWith("/") || path.startsWith("\\\\") || /^[A-Za-z]:[\\/]/.test(path);
  }

  function displayLocation(item: ProjectResource) {
    return item.url || item.path || "—";
  }

  const resources = createQuery<Page<ProjectResource>>(() => ({
    queryKey: ["resources", project?.id],
    queryFn: () => api<Page<ProjectResource>>(`/projects/${project!.id}/resources`),
    enabled: Boolean(project),
    refetchInterval: (query) =>
      (query.state.data as Page<ProjectResource> | undefined)?.items.some((item) => item.status === "validation_pending")
        ? 1_000 : false
  }));
  const requestedResourceId = $derived(new URLSearchParams(location.search).get("resource") ?? "");
  let appliedResourceFocus = $state("");
  $effect(() => {
    const id = requestedResourceId;
    const key = `${project?.id ?? ""}:${id}`;
    if (!id) { appliedResourceFocus = ""; return; }
    if (appliedResourceFocus === key || !resources.data?.items.some((item) => item.id === id)) return;
    appliedResourceFocus = key;
    void tick().then(() => {
      if (key !== `${project?.id ?? ""}:${requestedResourceId}`) return;
      const candidates = [...document.querySelectorAll<HTMLElement>("[data-resource-id]")]
        .filter((element) => element.dataset.resourceId === id);
      const target = candidates.find((element) => element.getClientRects().length > 0) ?? candidates[0];
      target?.scrollIntoView?.({ block: "center" });
      target?.focus({ preventScroll: true });
    });
  });
  const resourceMissing = $derived(Boolean(requestedResourceId && resources.isSuccess &&
    !resources.data?.items.some((item) => item.id === requestedResourceId)));
  let observedGitResourceSignature = $state("");
  $effect(() => {
    const signature = (resources.data?.items ?? [])
      .map((item) => `${item.id}:${item.status}:${Boolean(item.git_detected)}`)
      .join("|");
    if (observedGitResourceSignature && signature !== observedGitResourceSignature && project?.id) {
      void queryClient.invalidateQueries({ queryKey: gitRepositoriesQueryKey(project.id) });
    }
    observedGitResourceSignature = signature;
  });

  const hasCodeDirectories = $derived(
    resources.data?.items.some((item) =>
      item.kind === "code_directory" && Boolean(item.path) && isAbsoluteDirectoryPath(item.path ?? "")
    ) ?? false
  );
  const resourceOpeners = createQuery<ResourceOpeners>(() => ({
    queryKey: ["resource-openers", computerId],
    queryFn: () => api<ResourceOpeners>(`/computers/${computerId}/resource-openers`),
    enabled: Boolean(computerId && hasCodeDirectories),
    staleTime: 30_000
  }));

  const validate = createMutation<ProjectResource, Error, ProjectResource>(() => ({
    mutationFn: (item) =>
      api<ProjectResource>(`/projects/${item.project_id}/resources/${item.id}/validate`, {
        body: { expected_version: item.version }
      }),
    onSuccess: (item) => {
      void queryClient.invalidateQueries({ queryKey: ["resources", item.project_id] });
      void queryClient.invalidateQueries({ queryKey: gitRepositoriesQueryKey(item.project_id) });
    }
  }));

  const remove = createMutation<void, Error, ProjectResource>(() => ({
    mutationFn: (item) =>
      api<void>(`/projects/${item.project_id}/resources/${item.id}?expected_version=${item.version}`, {
        method: "DELETE"
      }),
    onSuccess: (_result, item) => {
      pendingDelete = null;
      queryClient.invalidateQueries({ queryKey: ["resources", item.project_id] });
      queryClient.invalidateQueries({ queryKey: gitRepositoriesQueryKey(item.project_id) });
    }
  }));

  type CreateResourceInput = {
    projectId: string; computerId: string; kind: ProjectResource["kind"];
    name: string; path: string; url: string; session: number; request: number;
  };
  const createResource = createMutation<ProjectResource, Error, CreateResourceInput>(() => ({
    mutationFn: async (input) => {
      // The picker may take seconds. Switching projects while it resolves must
      // never register the directory under a different project's authorization.
      const { projectId: targetProjectId, computerId: targetComputerId, kind, name, path, url } = input;
      if (kind === "url") {
        return api<ProjectResource>(`/projects/${targetProjectId}/resources`, {
          body: { kind, name, url }
        });
      }

      const command = await api<{ command_id: string }>(`/computers/${targetComputerId}/resource-register`, {
        body: { path }
      });
      const result = await pollCommand(command.command_id, targetComputerId) as { path?: string };
      if (!result.path) throw new Error(t("resources.pickerRegisterFailed"));
      return api<ProjectResource>(`/projects/${targetProjectId}/resources`, {
        body: { kind, name, path: result.path }
      });
    },
    onSuccess: (created, input) => {
      // A reset only resets the observer; it does not cancel the old request.
      if (modalOpen && modalSession === input.session && activeCreateRequest === input.request) closeModal();
      queryClient.invalidateQueries({ queryKey: ["resources", input.projectId] });
      queryClient.invalidateQueries({ queryKey: gitRepositoriesQueryKey(input.projectId) });
      // 新资源立即做一次验证，避免用户还要手动点一次才能识别 Git 仓库。
      if (created.path) validate.mutate(created);
    },
    onError: (error, input) => {
      if (modalOpen && modalSession === input.session && activeCreateRequest === input.request) createError = error;
    },
    onSettled: (_value, _error, input) => {
      if (modalSession === input.session && activeCreateRequest === input.request) createPending = false;
    }
  }));

  const canCreateResource = $derived(Boolean(
    (modalKind === "url" || modalName.trim()) && (modalKind === "url" ? modalURL.trim() : modalPath.trim())
  ));

  function submitResource(event: SubmitEvent) {
    event.preventDefault();
    if (!project || createPending || !canCreateResource) return;
    activeCreateRequest = ++requestSequence;
    createPending = true;
    createError = null;
    createResource.mutate({
      projectId: project.id, computerId, kind: modalKind, name: modalName.trim(),
      path: modalPath.trim(), url: modalURL.trim(), session: modalSession, request: activeCreateRequest
    });
  }

  const rename = createMutation<ProjectResource, Error, { item: ProjectResource; name: string; session: number }>(() => ({
    mutationFn: ({ item, name }) => api(`/projects/${item.project_id}/resources/${encodeURIComponent(item.id)}`, {
      method: "PATCH", body: { name, expected_version: item.version }
    }),
    onSuccess: (_updated, input) => {
      void queryClient.invalidateQueries({ queryKey: ["resources", input.item.project_id] });
      if (renameSession === input.session && editingResource?.id === input.item.id) closeRename();
    },
    onError: (error, input) => {
      if (renameSession === input.session && editingResource?.id === input.item.id) renameError = error;
    },
    onSettled: (_value, _error, input) => {
      if (renameSession === input.session) renamePending = false;
    }
  }));

  const openResource = createMutation<unknown, Error, { resource: ProjectResource; opener: ResourceOpener }>(() => ({
    mutationFn: ({ resource, opener }) => api<unknown>(
      `/projects/${resource.project_id}/resources/${resource.id}/open`,
      { body: { opener_id: opener.id } }
    ),
    onSuccess: () => closeOpenMenu()
  }));

  function toggleOpenMenu(event: MouseEvent, item: ProjectResource) {
    if (openMenuResource?.id === item.id) {
      closeOpenMenu();
      return;
    }
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    const width = 240;
    openMenuPosition = {
      left: Math.max(12, Math.min(window.innerWidth - width - 12, rect.right - width)),
      top: rect.bottom + 6
    };
    openMenuResource = item;
  }

  function closeOpenMenu() {
    openMenuResource = null;
    openMenuPosition = null;
  }

  function openerIcon(kind: string) {
    if (kind === "file_manager") return FolderOpen;
    if (kind === "terminal") return SquareTerminal;
    return Code2;
  }

  const error = $derived(
    resources.error ?? validate.error ?? remove.error ??
    resourceOpeners.error ?? openResource.error
  );
</script>

<RequireProject>
  <div class="flex h-full min-h-0 flex-col">
  <PageHeader
    title={t("resources.title")}
    description={t("resources.desc", { computer: context.computer?.name ?? "" })}
  >
    {#snippet actions()}
      <Button type="button" variant="primary" onclick={openModal}>
        <Plus size={14} />{t("resources.addResource")}
      </Button>
    {/snippet}
  </PageHeader>
  {#if error}<div class="border-b border-border px-5 py-3"><ErrorNotice {error} /></div>{/if}
  {#if resourceMissing}<p role="status" class="border-b border-border px-5 py-3 text-sm text-warning">{t("resources.notFound")}</p>{/if}
  {#if resources.isLoading}
    <Loading block />
  {:else}
    <section class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden bg-surface">
      <div class="flex items-center justify-between gap-4 border-b border-border px-5 py-4">
        <div>
          <h2 class="m-0 text-md font-semibold tracking-[-0.01em]">{t("resources.resourceList")}</h2>
          <p class="mt-1.5 text-sm leading-[1.5] text-secondary text-pretty">{t("resources.resourceListDesc")}</p>
        </div>
      </div>
      {#if !resources.data?.items.length}
        <EmptyState title={t("resources.noResources")} description={t("resources.noResourcesDesc")} />
      {:else}
        <div class="hidden divide-y divide-border max-md:block">
          {#each resources.data.items as item (item.id)}
            <article class="grid gap-3 px-4 py-4" class:search-target-flash={requestedResourceId === item.id} data-resource-id={item.id} tabindex="-1">
              <div class="flex min-w-0 items-center gap-2">
                {#if item.kind === "url"}<Globe2 size={16} class="shrink-0" />{:else}<FolderCheck size={16} class="shrink-0" />{/if}
                <strong class="min-w-0 flex-1 break-words text-sm">{item.name}</strong>
                <StatusBadge status={item.status} />
              </div>
              <code class="block break-all text-xs text-secondary">{displayLocation(item)}</code>
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="text-xs text-muted">{resourceKindLabel(item.kind)}</span>
                <div class="flex items-center gap-2">
                  {#if item.kind === "code_directory" && item.path}
                    <Button type="button" onclick={(event) => toggleOpenMenu(event, item)}>{t("resources.openWith")}</Button>
                  {/if}
                  {#if item.path}<Button type="button" disabled={validate.isPending} onclick={() => validate.mutate(item)}>{t("resources.revalidate")}</Button>{/if}
                  <Button type="button" onclick={() => openRename(item)}>{t("common.edit")}</Button>
                  <Button type="button" variant="danger" icon aria-label={t("resources.deleteResource")} onclick={() => { remove.reset(); pendingDelete = item; }}><Trash2 size={15} /></Button>
                </div>
              </div>
            </article>
          {/each}
        </div>
        <div class="w-full overflow-x-auto max-md:hidden">
          <table class="w-full border-collapse text-xs">
            <thead>
              <tr>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted">{t("resources.name")}</th>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted">{t("resources.kind")}</th>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted">{t("resources.path")}</th>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted">{t("resources.status")}</th>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted">{t("resources.updatedAt")}</th>
                <th class="bg-surface-subtle text-[11px] font-semibold uppercase tracking-[0.02em] text-muted" aria-label="operations"></th>
              </tr>
            </thead>
            <tbody>
              {#each resources.data.items as item (item.id)}
                <tr class:search-target-flash={requestedResourceId === item.id} data-resource-id={item.id} tabindex="-1">
                  <td class="border-b border-border px-4 py-3 text-left">
                    <span class="flex items-center gap-2">
                      {#if item.kind === "url"}<Globe2 size={15} />{:else}<FolderCheck size={15} />{/if}
                      <strong>{item.name}</strong>
                    </span>
                  </td>
                  <td class="border-b border-border px-4 py-3 text-left">{resourceKindLabel(item.kind)}</td>
                  <td class="border-b border-border px-4 py-3 text-left"><code class="block max-w-[360px] overflow-hidden text-ellipsis whitespace-nowrap text-code" title={displayLocation(item)}>{displayLocation(item)}</code></td>
                  <td class="border-b border-border px-4 py-3 text-left"><StatusBadge status={item.status} /></td>
                  <td class="border-b border-border px-4 py-3 text-left">{formatTime(item.updated_at)}</td>
                  <td class="border-b border-border px-4 py-3 text-left">
                    <div class="flex items-center justify-end gap-2">
                      {#if item.kind === "code_directory" && item.path && isAbsoluteDirectoryPath(item.path)}
                        <Button
                          controlSize="sm"
                          type="button"
                          title={t("resources.openCodeDirectory")}
                          aria-haspopup="menu"
                          aria-expanded={openMenuResource?.id === item.id}
                          disabled={resourceOpeners.isLoading || !resourceOpeners.data}
                          onclick={(event) => toggleOpenMenu(event, item)}
                        >
                          {#if resourceOpeners.isLoading}<LoaderCircle size={14} class="animate-spin" />{:else}<FolderOpen size={14} />{/if}
                          {t("resources.open")}
                          <ChevronDown size={12} />
                        </Button>
                      {/if}
                      {#if item.path && isAbsoluteDirectoryPath(item.path)}
                        <Button type="button" controlSize="sm" icon title={t("resources.revalidate")} aria-label={t("resources.revalidate")} onclick={() => validate.mutate(item)} disabled={validate.isPending}>
                          <RefreshCw size={15} />
                        </Button>
                      {/if}
                      <Button type="button" controlSize="sm" icon title={t("common.edit")} aria-label={t("common.edit")} onclick={() => openRename(item)}><Pencil size={15} /></Button>
                      <Button type="button" controlSize="sm" variant="danger" icon title={t("resources.deleteResource")} aria-label={t("resources.deleteResource")} onclick={() => (pendingDelete = item)} disabled={remove.isPending}>
                        <Trash2 size={15} />
                      </Button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {/if}

  </div>
  {#if modalOpen}
    <Modal title={t("resources.addTitle")} description={t("resources.addDesc")} onClose={closeModal}>
      {#if createError}<ErrorNotice error={createError} />{/if}
      <form class="grid gap-4" onsubmit={submitResource}>
        <div class="grid gap-2">
          <span class="text-sm font-semibold text-secondary">{t("resources.resourceType")}</span>
          <div
            class="grid grid-cols-3 gap-2 max-sm:grid-cols-1"
            role="radiogroup"
            aria-label={t("resources.resourceType")}
          >
            {#each resourceTypeOptions as option (option.kind)}
              {@const Icon = option.icon}
              <button
                class="grid cursor-pointer content-start gap-1.5 rounded-sm border p-3 text-left transition-colors {modalKind ===
                option.kind
                  ? 'border-accent bg-accent-subtle text-text'
                  : 'border-border bg-surface text-secondary hover:border-border-strong hover:bg-surface-hover'}"
                type="button"
                role="radio"
                aria-checked={modalKind === option.kind}
                data-kind={option.kind}
                onclick={() => chooseKind(option.kind)}
              >
                <span class="inline-flex items-center gap-2">
                  <Icon
                    size={15}
                    class={modalKind === option.kind ? "text-accent" : "text-muted"}
                  />
                  <strong class="text-xs font-semibold">{t(`resourceKind.${option.kind}`)}</strong>
                </span>
                <small class="text-2xs font-normal leading-[1.45] text-muted"
                  >{t(`resources.typeDesc.${option.kind}`)}</small
                >
              </button>
            {/each}
          </div>
        </div>
        <Field label={t("resources.displayName")} hint={modalKind === "url" ? t("resources.urlNameHint") : undefined}>
          <Input bind:value={modalName} required={modalKind !== "url"} maxlength={200}
            placeholder={modalKind === "url" ? t("resources.urlNamePlaceholder") : t("resources.displayName")} />
        </Field>

        {#if modalKind === "url"}
          <Field label={t("resources.urlLabel")} hint={t("resources.urlHint")}>
            <Input type="url" bind:value={modalURL} required placeholder={t("resources.urlPlaceholder")} />
          </Field>
        {:else}
          <Field label={t("resources.pathLabel")} hint={t("resources.pathHint", { computer: context.computer?.name ?? "" })}>
            <DirectoryPicker {computerId} selectedPath={modalPath} onSelect={choosePath} />
          </Field>
        {/if}

        <div class="flex justify-end gap-2">
          <Button type="button" onclick={closeModal}>{t("common.cancel")}</Button>
          <Button type="submit" variant="primary" disabled={!canCreateResource || createPending}>
            {#if createPending}<LoaderCircle size={14} class="animate-spin" aria-hidden="true" />{/if}{t("resources.add")}
          </Button>
        </div>
      </form>
    </Modal>
  {/if}

  {#if editingResource}
    <Modal title={t("common.edit")} description={editingResource.name} onClose={closeRename}>
      {#if renameError}<ErrorNotice error={renameError} />{/if}
      <form class="grid gap-4" onsubmit={(event) => {
        event.preventDefault();
        if (!editingResource || !renameName.trim() || renamePending) return;
        renamePending = true;
        renameError = null;
        rename.mutate({ item: editingResource, name: renameName.trim(), session: renameSession });
      }}>
        <Field label={t("resources.name")}><Input bind:value={renameName} required disabled={renamePending} /></Field>
        <div class="flex justify-end gap-2">
          <Button type="button" onclick={closeRename}>{t("common.cancel")}</Button>
          <Button type="submit" variant="primary" disabled={!renameName.trim() || renamePending}>{t("common.save")}</Button>
        </div>
      </form>
    </Modal>
  {/if}

  {#if pendingDelete}
    <ConfirmDialog
      title={t("resources.deleteResource")}
      description={t("resources.deleteConfirm", { name: pendingDelete.name })}
      confirmLabel={t("common.delete")}
      busy={remove.isPending}
      error={remove.error}
      onConfirm={() => { if (pendingDelete) remove.mutate(pendingDelete); }}
      onCancel={() => (pendingDelete = null)}
    />
  {/if}

  {#if openMenuResource && openMenuPosition && resourceOpeners.data}
    <button
      class="fixed inset-0 z-40 cursor-default border-0 bg-transparent p-0"
      type="button"
      aria-hidden="true"
      tabindex="-1"
      onclick={closeOpenMenu}
    ></button>
    <div
      class="fixed z-50 w-[240px] overflow-hidden rounded-md border border-border bg-surface p-1.5 shadow-raised"
      style={`left:${openMenuPosition.left}px;top:${openMenuPosition.top}px`}
      role="menu"
      aria-label={t("resources.openWith")}
    >
      <div class="border-b border-border px-2.5 pb-2 pt-1.5">
        <strong class="block truncate text-xs font-semibold">{t("resources.openWith")}</strong>
        <small class="mt-0.5 block truncate text-2xs text-muted" title={openMenuResource.path}>{openMenuResource.name}</small>
      </div>
      <div class="pt-1">
        {#each resourceOpeners.data.items as opener (opener.id)}
          {@const Icon = openerIcon(opener.kind)}
          <button
            class="flex min-h-10 w-full items-center gap-2.5 rounded-sm px-2.5 text-left text-xs transition-colors {opener.available ? 'cursor-pointer text-text hover:bg-surface-hover' : 'cursor-not-allowed text-muted opacity-55'}"
            type="button"
            role="menuitem"
            disabled={!opener.available || openResource.isPending}
            onclick={() => openResource.mutate({ resource: openMenuResource!, opener })}
          >
            {#if openResource.isPending && openResource.variables?.opener.id === opener.id}
              <LoaderCircle size={15} class="shrink-0 animate-spin" />
            {:else}
              <Icon size={15} class="shrink-0" />
            {/if}
            <span class="min-w-0 flex-1 truncate">{opener.name}</span>
            {#if !opener.available}<small class="shrink-0 text-[10px]">{t("resources.notInstalled")}</small>{/if}
          </button>
        {/each}
      </div>
      <p class="border-t border-border px-2.5 pb-1 pt-2 text-[10px] leading-relaxed text-muted">
        {t("resources.terminalCwdHint")}
      </p>
    </div>
  {/if}
</RequireProject>
