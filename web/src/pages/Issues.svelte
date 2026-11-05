<script lang="ts">
  import { Button, Checkbox, Input, Select, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    ArrowLeft,
    ChevronLeft,
    ChevronRight,
    CirclePlus,
    Search
  } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type {
    Agent, AgentService, Issue, Page, ProjectResource
  } from "../lib/types";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Loading from "../components/Loading.svelte";
  import Modal from "../ui/Modal.svelte";
  import RequireProject from "../components/RequireProject.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import { formatTime } from "../lib/helpers";
  import { location, setQueryParam } from "../lib/router.svelte";
  import { observeWorkLocation } from "../lib/workNavigation";
  import {
    fetchGitRepositories, gitRepositoriesQueryKey, type GitRepositoriesSnapshot
  } from "../lib/gitChanges";
  import { useConsole } from "./shared.svelte";
  import IssuePanel from "./IssuePanel.svelte";

  const context = useConsole();
  const project = $derived(context.project);

  type IssueListPage = Page<Issue> & { total: number };
  let titleSearch = $state("");
  let statusFilter = $state("");
  let listPage = $state(0);
  const listPageSize = 100;
  const issues = createQuery<IssueListPage>(() => ({
    queryKey: ["issues", project?.id, { search: titleSearch.trim(), status: statusFilter, offset: listPage * listPageSize }],
    queryFn: ({ queryKey, signal }) => {
      const filter = queryKey[2] as { search: string; status: string; offset: number };
      const params = new URLSearchParams({ limit: String(listPageSize), offset: String(filter.offset), search: filter.search, status: filter.status });
      return api<IssueListPage>(`/projects/${encodeURIComponent(String(queryKey[1]))}/issues?${params}`, { signal });
    },
    placeholderData: (previous, query) => query?.queryKey[1] === project?.id ? previous : undefined,
    enabled: Boolean(project),
    refetchInterval: 2_000
  }));
  const resources = createQuery<Page<ProjectResource>>(() => ({
    queryKey: ["resources", project?.id],
    queryFn: () => api<Page<ProjectResource>>(`/projects/${project!.id}/resources`),
    enabled: Boolean(project)
  }));
  const agents = createQuery<Page<Agent>>(() => ({
    queryKey: ["agents", context.computer?.id],
    queryFn: () => api<Page<Agent>>(`/computers/${context.computer!.id}/agents`),
    // The error boundary reads .error before an IssuePanel exists. TanStack's
    // tracked-property optimization would then miss an early successful .data
    // update, leaving the later-mounted panel on the old empty result snapshot.
    notifyOnChangeProps: ["data", "error"],
    enabled: Boolean(context.computer)
  }));
  const agentItems = $derived(agents.data?.items ?? []);
  const services = createQuery<Page<AgentService>>(() => ({
    queryKey: ["agent-services", context.computer?.id],
    queryFn: () => api<Page<AgentService>>(`/computers/${context.computer!.id}/agent-services`),
    notifyOnChangeProps: ["data", "error"],
    enabled: Boolean(context.computer)
  }));
  // Repository summaries are only presentation metadata for the session
  // Changes tab. Runs themselves always use the project workspace and never
  // bind to one selected repository.
  const repositories = createQuery<GitRepositoriesSnapshot>(() => ({
    queryKey: gitRepositoriesQueryKey(project?.id ?? ""),
    queryFn: ({ signal }) => fetchGitRepositories(project!.id, { signal }),
    enabled: Boolean(project),
    staleTime: 30_000,
    refetchOnWindowFocus: false
  }));
  const items = $derived((issues.data?.items ?? []).filter((item) => item.project_id === project?.id));
  const total = $derived(issues.data?.total ?? items.length);
  $effect(() => {
    void titleSearch;
    void statusFilter;
    listPage = 0;
  });
  $effect(() => {
    if (!issues.isPlaceholderData && listPage * listPageSize >= total) listPage = 0;
  });
  const hasGitRepo = $derived(
    (resources.data?.items ?? []).some(
      (item) => item.kind === "code_directory" && item.git_detected
    )
  );

  // 显式深链优先；只有没有 issue 参数时才沿用默认列表选择。
  let activeId = $state("");
  let mobileDetail = $state(false);
  let observedProjectId = $state("");
  const requestedIssueId = $derived(new URLSearchParams(location.search).get("issue") ?? "");
  const detailIssueId = $derived(requestedIssueId || activeId);
  const requestedIssue = createQuery<Issue>(() => ({
    queryKey: ["issue-detail", project?.id, detailIssueId],
    queryFn: ({ queryKey, signal }) => api<Issue>(`/issues/${encodeURIComponent(String(queryKey[2]))}`, { signal }),
    enabled: Boolean(project && detailIssueId && issues.isSuccess && !items.some((item) => item.id === detailIssueId)),
    staleTime: 30_000,
    refetchInterval: 2_000
  }));
  $effect(() => {
    const projectId = project?.id ?? "";
    // Project hydration and URL selection must be one transition: a separate
    // project-reset effect can otherwise erase a deep link already applied
    // before the asynchronous ConsoleShell project becomes available.
    if (observedProjectId !== projectId) {
      observedProjectId = projectId;
      activeId = "";
      titleSearch = "";
      statusFilter = "";
      listPage = 0;
    }
    if (!projectId) {
      activeId = "";
    } else if (requestedIssueId) {
      activeId = requestedIssueId;
      mobileDetail = true;
    } else if (!activeId && items.length) {
      activeId = items[0].id;
    }
  });
  const active = $derived(items.find((item) => item.id === activeId) ?? (
    !requestedIssue.isError && requestedIssue.data?.id === activeId && requestedIssue.data.project_id === project?.id ? requestedIssue.data : undefined
  ));
  $effect(() => {
    const selected = items.find((item) => item.id === activeId);
    if (selected) queryClient.setQueryData(["issue-detail", selected.project_id, selected.id], selected);
  });
  $effect(()=>{if(project && issues.isSuccess)observeWorkLocation({project_id:project.id,issue_id:active?.id});});

  // 创建弹窗。
  let createOpen = $state(false);
  // 概览页“新建任务”跳转携带 ?new=1：进入任务页后自动打开创建弹窗，并立即
  // 消费参数（replaceState），刷新或前进/后退不会重复弹出。
  $effect(() => {
    if (new URLSearchParams(location.search).get("new") !== "1") return;
    createOpen = true;
    setQueryParam("new", null, true);
  });
  let formDescription = $state("");
  let formWorktree = $state(false);
  function openCreateIssue() {
    createOpen = true;
  }

  function submitCreateIssue(event: SubmitEvent) {
    event.preventDefault();
    // 标题由服务端默认值和异步命名提供，已有任务仍可随时改名。
    if (createIssue.isPending) return;
    if (!project) return;
    createIssue.mutate({ projectId: project.id, description: formDescription, worktreeHint: formWorktree });
  }

  const createIssue = createMutation<Issue, Error, { projectId: string; description: string; worktreeHint: boolean }>(() => ({
    mutationFn: (input) => api<Issue>(`/projects/${input.projectId}/issues`, {
        body: { description: input.description.trim(), worktree_hint: input.worktreeHint }
      }),
    onSuccess: (item, input) => {
      if (project?.id === input.projectId) {
        if (formDescription === input.description && formWorktree === input.worktreeHint) {
          createOpen = false;
          formDescription = "";
          formWorktree = false;
        }
        activeId = item.id;
        mobileDetail = true;
        setQueryParam("issue", item.id);
      }
      queryClient.setQueryData(["issue-detail", input.projectId, item.id], item);
      queryClient.setQueryData<Page<Issue>>(["issues", input.projectId], (current) => current ? {
        ...current, items: [item, ...current.items.filter((candidate) => candidate.id !== item.id)]
      } : current);
      queryClient.invalidateQueries({ queryKey: ["issues", input.projectId] });
    }
  }));

  function applyIssueUpdate(updated: Issue) {
    queryClient.setQueryData(["issue-detail", updated.project_id, updated.id], updated);
    queryClient.setQueriesData<Page<Issue>>({ queryKey: ["issues", updated.project_id] }, (current) => {
      if (!current) return current;
      return {
        ...current,
        items: current.items.map((item) => item.id === updated.id ? updated : item)
      };
    });
    queryClient.invalidateQueries({ queryKey: ["issues", updated.project_id] });
  }

</script>

<RequireProject>
  <div class="flex h-full min-h-0 flex-col overflow-hidden">
  {#if issues.error || resources.error || agents.error || services.error || repositories.error || requestedIssue.error}
    <div class="shrink-0 px-4 pt-3"><ErrorNotice error={issues.error ?? resources.error ?? agents.error ?? services.error ?? repositories.error ?? requestedIssue.error} onRetry={() => {
      void issues.refetch(); void resources.refetch(); void agents.refetch(); void repositories.refetch();
      void services.refetch();
      if (detailIssueId) void requestedIssue.refetch();
    }} /></div>
  {/if}
  {#if issues.isLoading}
    <Loading block />
  {:else}
    <section class="grid min-h-0 flex-1 grid-cols-[310px_minmax(0,1fr)] overflow-hidden bg-canvas max-md:grid-cols-1" data-testid="issues-master-detail" data-mobile-view={mobileDetail && active ? "detail" : "list"}>
      <aside class="min-h-0 overflow-y-auto overflow-x-hidden border-r border-border bg-surface-subtle max-md:border-r-0 {mobileDetail && active ? 'max-md:hidden' : ''}" aria-label={t("issues.listLabel")}>
        <div class="sticky top-0 z-10 grid gap-3 border-b border-border bg-surface-subtle/95 px-4 py-3 backdrop-blur">
          <div class="flex items-center justify-between gap-3">
            <strong class="text-xs font-semibold">{t("issues.count", { count: total })}</strong>
            <Button controlSize="sm" variant="primary" type="button" onclick={openCreateIssue}>
              <CirclePlus size={14} />{t("issues.newIssue")}
            </Button>
          </div>
          <label class="relative block">
            <Search size={13} class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input controlSize="sm" type="search" class="w-full pl-8 text-xs" bind:value={titleSearch} aria-label={`${t("issues.sessionSearch")} · ${t("issues.titleField")}`} placeholder={`${t("issues.sessionSearch")} · ${t("issues.titleField")}`} />
          </label>
          <Select controlSize="sm" class="w-full text-xs" bind:value={statusFilter} aria-label={t("issues.status")}>
            <option value="">{t("runChanges.sourceAll")}</option>
            <option value="open">{t("issues.statusOpen")}</option>
            <option value="resolved">{t("issues.statusResolved")}</option>
            <option value="closed">{t("issues.statusClosed")}</option>
          </Select>
        </div>
        {#if !items.length && !titleSearch.trim() && !statusFilter}
          <p class="p-5 text-xs leading-relaxed text-muted">{t("issues.noIssues")}</p>
        {:else}
          {#each items as item (item.id)}
            <button
              class="grid w-full cursor-pointer gap-2 border-0 border-b border-border bg-transparent px-4 py-3 text-left hover:bg-surface {item.id === activeId ? 'bg-surface shadow-[inset_3px_0_var(--color-accent)]' : ''}"
              type="button"
              aria-current={item.id === activeId ? "page" : undefined}
              onclick={() => {
                activeId = item.id;
                mobileDetail = true;
                setQueryParam("issue", item.id);
                for (const name of ["focus", "comment", "session", "run", "entry"]) setQueryParam(name, null, true);
              }}
            >
              <span class="grid min-w-0 gap-1.5">
                <strong class="line-clamp-2 break-words text-xs font-semibold leading-[1.45]">{item.title || t("issues.untitled")}</strong>
                <small class="flex items-center justify-between gap-2 text-2xs text-muted">
                  <span>{formatTime(item.updated_at)}</span>
                  <StatusBadge status={item.status} />
                </small>
              </span>
            </button>
          {/each}
          {#if !items.length}<p class="p-5 text-xs text-muted">{t("search.noResults")}</p>{/if}
          {#if total > listPageSize}
            <div class="flex items-center justify-between gap-2 p-3 text-2xs text-muted">
              <Button type="button" controlSize="sm" icon disabled={listPage === 0} aria-label={`${t("issues.listLabel")} −`} onclick={() => listPage--}><ChevronLeft size={14} /></Button>
              <span>{listPage * listPageSize + 1}–{Math.min((listPage + 1) * listPageSize, total)} / {total}</span>
              <Button type="button" controlSize="sm" icon disabled={(listPage + 1) * listPageSize >= total} aria-label={`${t("issues.listLabel")} +`} onclick={() => listPage++}><ChevronRight size={14} /></Button>
            </div>
          {/if}
        {/if}
      </aside>

      <div class="flex min-h-0 min-w-0 flex-col overflow-hidden {!mobileDetail || !active ? 'max-md:hidden' : ''}" data-testid="issue-detail-pane">
        <div class="hidden shrink-0 border-b border-border bg-surface px-3 py-2 max-md:block">
          <Button type="button" onclick={() => (mobileDetail = false)}><ArrowLeft size={14} />{t("issues.listLabel")}</Button>
        </div>
        <div class="min-h-0 flex-1 overflow-hidden">
        {#if active}
		  {#key active.id}
			<IssuePanel
			  issue={active}
			  agents={agentItems}
			  services={services.data?.items ?? []}
			  projectId={project!.id}
			  repositories={repositories.data?.repositories ?? []}
			  onIssueChange={applyIssueUpdate}
			/>
		  {/key}
        {:else}
          <div class="grid h-full place-items-center text-sm text-muted">{t("issues.selectOrCreate")}</div>
        {/if}
        </div>
      </div>
    </section>
  {/if}
  </div>

  {#if createOpen}
    <Modal title={t("issues.createTitle")} description={t("issues.createDesc")} onClose={() => (createOpen = false)}>
      {#if createIssue.error}<ErrorNotice error={createIssue.error} />{/if}
      <form class="grid gap-4" onsubmit={submitCreateIssue}>
        <Field label={t("issues.descriptionField")}>
          <Textarea rows={4} bind:value={formDescription}></Textarea>
        </Field>
        <label
          class="flex cursor-pointer items-start gap-2.5 rounded-sm border border-border bg-surface-subtle p-3 text-xs {!hasGitRepo ? 'opacity-50' : ''}"
          title={hasGitRepo ? t("issues.worktreeHint") : t("issues.worktreeDisabledHint")}
        >
          <Checkbox class="mt-0.5"
            type="checkbox"
            bind:checked={formWorktree}
            disabled={!hasGitRepo}
          />
          <span class="grid gap-1">
            <strong class="font-semibold">{t("issues.createWorktree")}</strong>
            <span class="text-2xs text-muted">{t("issues.worktreeDesc")}</span>
          </span>
        </label>
        <div class="flex justify-end gap-2">
          <Button
            type="button"
            onclick={() => (createOpen = false)}
          >{t("common.cancel")}</Button>
          <Button type="submit" variant="primary"
            disabled={createIssue.isPending}
          >{t("common.create")}</Button>
        </div>
      </form>
    </Modal>
  {/if}

</RequireProject>
