<script lang="ts">
  import { Input, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    BookOpenText, CalendarClock, ChevronRight, FolderCheck, MessageSquareText,
    Pencil, Plus
  } from "lucide-svelte";
  import { tick } from "svelte";
  import Link from "../components/Link.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Loading from "../components/Loading.svelte";
  import RequireProject from "../components/RequireProject.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import type {
    Issue, MemoryEntry, Page, Project, ProjectResource, ScheduledTask
  } from "../lib/types";
  import { formatTime } from "../lib/helpers";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const project = $derived(context.project);
  const computerId = $derived(context.computer?.id ?? "");

  type OverviewIssues = Page<Issue> & { recent: Issue[]; total?: number; open_total?: number };
  const issues = createQuery<OverviewIssues>(() => ({
    queryKey: ["issues", project?.id, "overview"],
    queryFn: async ({ queryKey, signal }) => {
      const path = `/projects/${encodeURIComponent(String(queryKey[1]))}/issues?limit=8`;
      const [recent, open] = await Promise.all([
        api<Page<Issue> & { total?: number }>(path, { signal }),
        api<Page<Issue> & { total?: number }>(`${path}&status=open`, { signal })
      ]);
      return {
        items: [...new Map([...open.items, ...recent.items].map((item) => [item.id, item])).values()],
        recent: recent.items, total: recent.total, open_total: open.total
      };
    },
    enabled: Boolean(project),
    refetchInterval: 2_000
  }));
  const schedules = createQuery<Page<ScheduledTask>>(() => ({
    queryKey: ["schedules", project?.id],
    queryFn: () => api<Page<ScheduledTask>>(`/projects/${project!.id}/scheduled-tasks`),
    enabled: Boolean(project)
  }));
  const resources = createQuery<Page<ProjectResource>>(() => ({
    queryKey: ["resources", project?.id],
    queryFn: () => api<Page<ProjectResource>>(`/projects/${project!.id}/resources`),
    enabled: Boolean(project)
  }));
  const memories = createQuery<Page<MemoryEntry>>(() => ({
    queryKey: ["memories", project?.id],
    queryFn: () => api<Page<MemoryEntry>>(`/projects/${project!.id}/memories`),
    enabled: Boolean(project)
  }));

  function timestamp(value?: string) {
    const parsed = value ? Date.parse(value) : NaN;
    return Number.isFinite(parsed) ? parsed : 0;
  }
  function recentFirst(a: { updated_at: string; id: string }, b: { updated_at: string; id: string }) {
    return timestamp(b.updated_at) - timestamp(a.updated_at) || a.id.localeCompare(b.id);
  }
  function objectTarget(kind: "issue" | "memory" | "resource" | "schedule", id: string) {
    const paths = { issue: "issues", memory: "memories", resource: "resources", schedule: "schedules" };
    return `/${paths[kind]}?${kind}=${encodeURIComponent(id)}`;
  }
  const issueItems = $derived([...(issues.data?.items ?? [])].sort((a, b) =>
    Number(b.status === "open") - Number(a.status === "open") || recentFirst(a, b)));
  const scheduleItems = $derived([...(schedules.data?.items ?? [])].sort((a, b) =>
    Number(b.enabled) - Number(a.enabled) ||
    (timestamp(a.next_run_at) || Infinity) - (timestamp(b.next_run_at) || Infinity) || recentFirst(a, b)));
  const resourceItems = $derived([...(resources.data?.items ?? [])].sort(recentFirst));
  const memoryItems = $derived([...(memories.data?.items ?? [])].sort(recentFirst));
  const recentActivity = $derived([
    ...(issues.data?.recent ?? []).map((item) => ({ ...item, target: objectTarget("issue", item.id), label: t("shell.issues"), title: item.title?.trim() || t("issues.untitled") })),
    ...scheduleItems.map((item) => ({ ...item, target: objectTarget("schedule", item.id), label: t("shell.schedules"), title: item.name?.trim() || t("search.untitled") })),
    ...resourceItems.map((item) => ({ ...item, target: objectTarget("resource", item.id), label: t("shell.resources"), title: item.name?.trim() || t("search.untitled") })),
    ...memoryItems.map((item) => ({ ...item, target: objectTarget("memory", item.id), label: t("shell.memories"), title: item.title?.trim() || t("search.untitled") }))
  ].sort(recentFirst).slice(0, 5));
  const openIssueCount = $derived(issues.data?.open_total ?? issueItems.filter((item) => item.status === "open").length);
  const enabledScheduleCount = $derived(scheduleItems.filter((item) => item.enabled).length);
  // Read every flag eagerly: untracked observers may otherwise retain their
  // optimistic loading value when all four requests settle in the same tick.
  const loading = $derived(
    [issues.isLoading, schedules.isLoading, resources.isLoading, memories.isLoading].some(Boolean)
  );

  let nameEditing = $state(false);
  let descriptionEditing = $state(false);
  let nameDraft = $state("");
  let descriptionDraft = $state("");
  let nameInput = $state<HTMLInputElement>();
  let descriptionInput = $state<HTMLTextAreaElement>();
  let editingProjectId = $state("");

  $effect(() => {
    if (!project || editingProjectId === project.id) return;
    editingProjectId = project.id;
    nameEditing = false;
    descriptionEditing = false;
    nameDraft = project.name;
    descriptionDraft = project.description ?? "";
  });

  function applyProjectUpdate(updated: Project) {
    queryClient.setQueryData<Page<Project>>(["projects", computerId], (current) => {
      if (!current) return current;
      return {
        ...current,
        items: current.items.map((item) => item.id === updated.id ? updated : item)
      };
    });
    queryClient.invalidateQueries({ queryKey: ["projects", computerId] });
  }

  const updateProject = createMutation<Project, Error, { name?: string; description?: string }>(() => ({
    mutationFn: (patch) => api<Project>(`/projects/${project!.id}`, {
      method: "PATCH",
      body: { expected_version: project!.version, ...patch }
    }),
    onSuccess: (updated) => {
      nameDraft = updated.name;
      descriptionDraft = updated.description ?? "";
      nameEditing = false;
      descriptionEditing = false;
      applyProjectUpdate(updated);
    }
  }));

  const error = $derived(
    issues.error ?? schedules.error ?? resources.error ?? memories.error ?? updateProject.error
  );

  function startNameEdit() {
    if (!project || project.is_default || updateProject.isPending) return;
    nameDraft = project.name;
    nameEditing = true;
    void tick().then(() => {
      nameInput?.focus();
      const length = nameInput?.value.length ?? 0;
      nameInput?.setSelectionRange(length, length);
    });
  }

  function commitName() {
    if (!project || project.is_default || !nameEditing || updateProject.isPending) return;
    const value = nameDraft.trim();
    if (!value || value === project.name) {
      nameDraft = project.name;
      nameEditing = false;
      return;
    }
    updateProject.mutate({ name: value });
  }

  function startDescriptionEdit() {
    if (!project || updateProject.isPending) return;
    descriptionDraft = project.description ?? "";
    descriptionEditing = true;
    void tick().then(() => {
      descriptionInput?.focus();
      const length = descriptionInput?.value.length ?? 0;
      descriptionInput?.setSelectionRange(length, length);
    });
  }

  function commitDescription() {
    if (!project || !descriptionEditing || updateProject.isPending) return;
    const value = descriptionDraft.trim();
    if (value === (project.description ?? "").trim()) {
      descriptionEditing = false;
      return;
    }
    updateProject.mutate({ description: value });
  }
</script>

<RequireProject>
  {#if error}
    <div class="p-5"><ErrorNotice {error} /></div>
  {:else if loading}
    <Loading block />
  {:else}
    <div class="min-h-full bg-surface">
      <header class="border-b border-border px-7 pb-6 pt-5 max-md:px-4">
        <div class="flex items-start justify-between gap-5">
          <div class="min-w-0 flex-1">
            <span class="text-xs font-semibold text-muted">{t("workbench.title")}</span>
            {#if project?.is_default}
              <div class="mt-1 flex flex-wrap items-center gap-2">
                <h1 class="m-0 min-w-0 break-words text-[30px] font-semibold leading-tight tracking-[-0.025em] text-text">{project.name}</h1>
                <span class="rounded-full border border-border px-2 py-0.5 text-xs text-muted">{t("defaultProject.badge")}</span>
              </div>
            {:else if nameEditing}
              <Input
                class="mt-1 !min-h-0 !border-0 !bg-transparent !p-0 text-[30px] font-semibold leading-tight tracking-[-0.025em] !shadow-none focus:!border-transparent"
                bind:ref={nameInput}
                bind:value={nameDraft}
                aria-label={t("settings.projectName")}
                disabled={updateProject.isPending}
                onblur={commitName}
                onkeydown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    (event.currentTarget as HTMLInputElement).blur();
                  } else if (event.key === "Escape") {
                    nameDraft = project?.name ?? "";
                    nameEditing = false;
                  }
                }}
              />
            {:else}
              <button class="group/name mt-1 flex max-w-full cursor-text items-start gap-2 border-0 bg-transparent p-0 text-left" type="button" onclick={startNameEdit}>
                <h1 class="m-0 min-w-0 break-words text-[30px] font-semibold leading-tight tracking-[-0.025em] text-text">{project?.name?.trim() || t("search.untitled")}</h1>
                <Pencil size={14} class="mt-2 shrink-0 text-muted opacity-0 transition-opacity group-hover/name:opacity-100" />
              </button>
            {/if}

            {#if descriptionEditing}
              <Textarea
                class="mt-2 !min-h-0 resize-none !border-0 !bg-transparent !p-0 text-sm leading-[1.65] text-secondary !shadow-none focus:!border-transparent"
                rows={2}
                bind:ref={descriptionInput}
                bind:value={descriptionDraft}
                aria-label={t("settings.description")}
                disabled={updateProject.isPending}
                onblur={commitDescription}
                onkeydown={(event) => {
                  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                    event.preventDefault();
                    (event.currentTarget as HTMLTextAreaElement).blur();
                  } else if (event.key === "Escape") {
                    descriptionDraft = project?.description ?? "";
                    descriptionEditing = false;
                  }
                }}
              ></Textarea>
            {:else}
              <button class="group/note mt-2 flex max-w-[860px] cursor-text items-start gap-2 border-0 bg-transparent p-0 text-left" type="button" onclick={startDescriptionEdit}>
                <p class="m-0 whitespace-pre-wrap text-sm leading-[1.65] {project?.description?.trim() ? 'text-secondary' : 'text-muted'}">
                  {project?.description?.trim() || t("workbench.noProjectNote")}
                </p>
                <Pencil size={13} class="mt-1 shrink-0 text-muted opacity-0 transition-opacity group-hover/note:opacity-100" />
              </button>
            {/if}
          </div>
          <Link class="inline-flex min-h-9 shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-sm border border-control-border bg-control px-3.5 text-xs font-semibold text-control-text hover:bg-control-hover" to="/issues?new=1">
            <Plus size={14} />{t("workbench.newIssue")}
          </Link>
        </div>
      </header>

      <nav class="grid grid-cols-4 border-b border-border max-md:grid-cols-2" aria-label={t("workbench.title")}>
        <Link class="flex min-h-[74px] items-center gap-3 border-r border-border px-6 hover:bg-surface-subtle max-md:border-b max-md:px-4" to="/issues">
          <MessageSquareText size={17} class="text-muted" />
          <span><strong class="block text-lg tabular-nums">{openIssueCount}</strong><small class="text-2xs text-muted">{t("shell.issues")}</small></span>
        </Link>
        <Link class="flex min-h-[74px] items-center gap-3 border-r border-border px-6 hover:bg-surface-subtle max-md:border-b max-md:border-r-0 max-md:px-4" to="/schedules">
          <CalendarClock size={17} class="text-muted" />
          <span><strong class="block text-lg tabular-nums">{enabledScheduleCount}</strong><small class="text-2xs text-muted">{t("shell.schedules")}</small></span>
        </Link>
        <Link class="flex min-h-[74px] items-center gap-3 border-r border-border px-6 hover:bg-surface-subtle max-md:px-4" to="/resources">
          <FolderCheck size={17} class="text-muted" />
          <span><strong class="block text-lg tabular-nums">{resourceItems.length}</strong><small class="text-2xs text-muted">{t("shell.resources")}</small></span>
        </Link>
        <Link class="flex min-h-[74px] items-center gap-3 px-6 hover:bg-surface-subtle max-md:px-4" to="/memories">
          <BookOpenText size={17} class="text-muted" />
          <span><strong class="block text-lg tabular-nums">{memoryItems.length}</strong><small class="text-2xs text-muted">{t("shell.memories")}</small></span>
        </Link>
      </nav>

      <div class="grid grid-cols-[minmax(0,1.45fr)_minmax(320px,0.75fr)] max-lg:grid-cols-1">
        <section class="min-w-0 border-r border-border max-lg:border-b max-lg:border-r-0">
          <Link class="flex min-h-14 items-center gap-3 border-b border-border px-6 hover:bg-surface-subtle max-md:px-4" to="/issues">
            <h2 class="min-w-0 flex-1 text-sm font-semibold">{t("shell.issues")}</h2>
            <span class="text-2xs text-muted">{issues.data?.total ?? issueItems.length}</span>
            <ChevronRight size={15} class="text-muted" />
          </Link>
          {#if issueItems.length}
            {#each issueItems.slice(0, 8) as issue (issue.id)}
              <Link class="flex min-h-[50px] min-w-0 items-center gap-3 border-b border-border px-6 py-2.5 last:border-b-0 hover:bg-surface-subtle max-md:px-4" to={objectTarget("issue", issue.id)}>
                <span class="min-w-0 flex-1"><strong class="block truncate text-sm font-medium">{issue.title?.trim() || t("issues.untitled")}</strong><time class="text-2xs text-muted">{formatTime(issue.updated_at)}</time></span>
                <StatusBadge status={issue.status} />
              </Link>
            {/each}
          {:else}
            <Link class="flex min-h-24 items-center px-6 text-sm text-muted hover:bg-surface-subtle max-md:px-4" to="/issues">{t("issues.noIssues")}</Link>
          {/if}
        </section>

        <aside class="min-w-0">
          <section>
            <Link class="flex min-h-12 items-center gap-3 border-b border-border px-5 hover:bg-surface-subtle" to="/schedules">
              <CalendarClock size={15} class="text-muted" /><h2 class="min-w-0 flex-1 text-xs font-semibold">{t("shell.schedules")}</h2><ChevronRight size={14} class="text-muted" />
            </Link>
            {#each scheduleItems.slice(0, 3) as task (task.id)}
              <Link class="flex min-h-11 items-center gap-2 border-b border-border px-5 py-2 text-xs hover:bg-surface-subtle" to={objectTarget("schedule", task.id)}>
                <span class="min-w-0 flex-1"><span class="block truncate">{task.name?.trim() || t("search.untitled")}</span><small class="text-2xs text-muted">{t("schedules.nextRun", { time: formatTime(task.next_run_at) })}</small></span><StatusBadge status={task.enabled ? "enabled" : "disabled"} />
              </Link>
            {:else}
              <Link class="flex min-h-14 items-center border-b border-border px-5 text-xs text-muted hover:bg-surface-subtle" to="/schedules">{t("schedules.noTasks")}</Link>
            {/each}
          </section>
          <section>
            <Link class="flex min-h-12 items-center gap-3 border-b border-border px-5 hover:bg-surface-subtle" to="/resources">
              <FolderCheck size={15} class="text-muted" /><h2 class="min-w-0 flex-1 text-xs font-semibold">{t("shell.resources")}</h2><ChevronRight size={14} class="text-muted" />
            </Link>
            {#each resourceItems.slice(0, 3) as resource (resource.id)}
              <Link class="flex min-h-11 items-center border-b border-border px-5 text-xs hover:bg-surface-subtle" to={objectTarget("resource", resource.id)}><span class="truncate">{resource.name?.trim() || t("search.untitled")}</span></Link>
            {:else}
              <Link class="flex min-h-14 items-center border-b border-border px-5 text-xs text-muted hover:bg-surface-subtle" to="/resources">{t("resources.noResources")}</Link>
            {/each}
          </section>
          <section>
            <Link class="flex min-h-12 items-center gap-3 border-b border-border px-5 hover:bg-surface-subtle" to="/memories">
              <BookOpenText size={15} class="text-muted" /><h2 class="min-w-0 flex-1 text-xs font-semibold">{t("shell.memories")}</h2><ChevronRight size={14} class="text-muted" />
            </Link>
            {#each memoryItems.slice(0, 3) as memory (memory.id)}
              <Link class="flex min-h-11 items-center border-b border-border px-5 text-xs last:border-b-0 hover:bg-surface-subtle" to={objectTarget("memory", memory.id)}><span class="truncate">{memory.title?.trim() || t("search.untitled")}</span></Link>
            {:else}
              <Link class="flex min-h-14 items-center px-5 text-xs text-muted hover:bg-surface-subtle" to="/memories">{t("memories.noMemories")}</Link>
            {/each}
          </section>
          <section aria-label={t("workbench.recentActivity")}>
            <h2 class="border-b border-border px-5 py-3 text-xs font-semibold">{t("workbench.recentActivity")}</h2>
            {#each recentActivity as item (item.target)}
              <Link class="flex min-h-12 items-center gap-2 border-b border-border px-5 py-2 text-xs hover:bg-surface-subtle" to={item.target}>
                <span class="min-w-0 flex-1"><strong class="block truncate font-medium">{item.title}</strong><small class="text-2xs text-muted">{item.label}</small></span>
                <time class="shrink-0 text-2xs text-muted">{formatTime(item.updated_at)}</time>
              </Link>
            {:else}
              <p class="px-5 py-4 text-xs text-muted">{t("common.noData")}</p>
            {/each}
          </section>
        </aside>
      </div>
    </div>
  {/if}
</RequireProject>
