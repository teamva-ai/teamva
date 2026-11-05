<script lang="ts">
  import brandIcon from "../../../assets/brand/icon.png";
  import { Button, Input, Select, Textarea } from "../ui";
  import { createQuery, createMutation } from "@tanstack/svelte-query";
  import {
    Activity as ActivityIcon,
    Bot,
    CalendarClock,
    Check,
    ChevronsUpDown,
    FileText,
    Folder,
    FolderKanban,
    GitCompareArrows,
    GitFork,
    LayoutDashboard,
    Menu,
    MessageSquareText,
    PackageOpen,
    Plus,
    Server,
    Settings as SettingsIcon,
    Settings2
  } from "lucide-svelte";
  import { applyUserLocalePreference, changeLocale, localeInfo, t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import {
    hydratePreferences,
    selection,
    preferences,
    setAllowWrite,
    setAppearance,
    setTheme,
    setComputer as storeSetComputer,
    setProject as storeSetProject
  } from "../lib/stores.svelte";
  import { applyTheme, subscribeTheme } from "../lib/theme";
  import { preferredProjectId } from "../lib/projectSelection";
  import { queryClient } from "../lib/query";
  import { location } from "../lib/router.svelte";
  import { confirmProjectChange } from "../lib/unsavedChanges.svelte";
  import { onboardingOverlay, requestOnboarding } from "../lib/onboardingOverlay.svelte";
  import { observeWorkProject, flushWorkNavigation } from "../lib/workNavigation";
  import NavLink from "../components/NavLink.svelte";
  import Link from "../components/Link.svelte";
  import Route from "../components/Route.svelte";
  import Modal from "../ui/Modal.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import Field from "../ui/Field.svelte";
  import type {
    Agent,
    AgentService,
    Computer,
    Onboarding,
    Page,
    Project,
    User,
    UserPreferences,
    UserPreferencesPatch
  } from "../lib/types";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import Loading from "../components/Loading.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import GlobalSearch from "../components/GlobalSearch.svelte";
  import PendingApprovals from "../components/PendingApprovals.svelte";
  import FeatureTours from "../components/FeatureTours.svelte";
  import { provideConsoleContext, type ConsoleContext } from "./shared.svelte";
  import Workbench from "./Workbench.svelte";
  import Issues from "./Issues.svelte";
  import Conversations from "./Conversations.svelte";
  import Agents from "./Agents.svelte";
  import LocalSkills from "./LocalSkills.svelte";
  import ProgrammingTools from "./ProgrammingTools.svelte";
  import Resources from "./Resources.svelte";
  import Memories from "./Memories.svelte";
  import Schedules from "./Schedules.svelte";
  import Settings from "./Settings.svelte";
  import WorkRecords from "./WorkRecords.svelte";
  import Inspirations from "./Inspirations.svelte";

  let ChangesComponent = $state<typeof import("./Changes.svelte").default | null>(null);
  let changesError = $state<unknown>(null);

  async function loadChanges() {
    changesError = null;
    try {
      ChangesComponent = (await import("./Changes.svelte")).default;
    } catch (error) {
      changesError = error;
    }
  }

  $effect(() => {
    if (location.path === "/changes" && !ChangesComponent) {
      void loadChanges();
    }
  });

  const me = createQuery<User>(() => ({ queryKey: ["me"], queryFn: () => api<User>("/me") }));
  const userPreferences = createQuery<UserPreferences>(() => ({
    queryKey: ["user-preferences"],
    queryFn: () => api<UserPreferences>("/me/preferences")
  }));
  // A slim banner keeps direct links to subpages usable while the first-run
  // guide is still outstanding; the default entry point redirects instead.
  const onboardingStatus = createQuery<Onboarding>(() => ({
    queryKey: ["onboarding"],
    queryFn: () => api<Onboarding>("/system/onboarding"),
    staleTime: 5_000,
    refetchInterval: 10_000
  }));
  const computers = createQuery<Page<Computer>>(() => ({
    queryKey: ["computers"],
    queryFn: () => api<Page<Computer>>("/computers"),
    refetchInterval: 10_000
  }));
  const computerItems = $derived(computers.data?.items ?? []);
  const selectedComputerId = $derived(computerItems[0]?.id ?? "");
  const projects = createQuery<Page<Project>>(() => ({
    queryKey: ["projects", selectedComputerId],
    queryFn: () =>
      api<Page<Project>>(`/projects?computer_id=${encodeURIComponent(selectedComputerId)}`),
    enabled: Boolean(selectedComputerId)
  }));
  const projectItems = $derived(projects.data?.items ?? []);
  const selectedProjectId = $derived(preferredProjectId(projectItems, selection.projectId));
  const project = $derived(projectItems.find((item) => item.id === selectedProjectId));
  let appliedProjectLink = $state("");
  $effect(() => {
    const id = new URLSearchParams(location.search).get("project") ?? "";
    const link = location.path + location.search;
    if (id && link !== appliedProjectLink && projectItems.some((p) => p.id === id)) {
      appliedProjectLink = link;
      if (id === selection.projectId || confirmProjectChange(id)) storeSetProject(id);
    }
  });
  $effect(() => {
    if (!projects.isSuccess || !selectedProjectId) return;
    // Detail pages report their effective object, not a transient URL alone.
    if (location.path !== "/issues" && location.path !== "/conversations")
      observeWorkProject(selectedProjectId);
    void flushWorkNavigation();
  });

  const computerIds = $derived(computers.data?.items.map((item) => item.id).join(",") ?? "");
  // Same key and Page shape as ProgrammingToolsPanel/Onboarding: one cache
  // entry must never hold two different response shapes.
  const services = createQuery<Page<AgentService>>(() => ({
    queryKey: ["agent-services", computerIds],
    queryFn: async () => {
      const items = computers.data?.items ?? [];
      const pages = await Promise.all(
        items.map((item) => api<Page<AgentService>>(`/computers/${item.id}/agent-services`))
      );
      return { items: pages.flatMap((page) => page.items) };
    },
    enabled: Boolean(computers.data?.items.length),
    refetchInterval: 10_000
  }));
  const agents = createQuery<Agent[]>(() => ({
    queryKey: ["agents-all", computerIds],
    queryFn: async () => {
      const items = computers.data?.items ?? [];
      const pages = await Promise.all(
        items.map((item) => api<Page<Agent>>(`/computers/${item.id}/agents`))
      );
      return pages.flatMap((page) => page.items);
    },
    enabled: Boolean(computers.data?.items.length),
    refetchInterval: 10_000
  }));

  // The server is authoritative after login. Local state only provides a
  // startup value while this query is loading.
  $effect(() => {
    const stop = subscribeTheme();
    return stop;
  });
  $effect(() => {
    void localeInfo.value;
    void preferences.theme;
    void preferences.appearance;
    applyTheme(preferences.theme, preferences.appearance);
  });
  let hydratedPreferencesVersion = $state(0);
  let effectivePreferences = $state<UserPreferences>({
    user_id: "",
    locale: localeInfo.value,
    locale_explicit: false,
    theme: "system",
    allow_write: false,
    version: 1
  });

  function applyServerPreferences(value: UserPreferences) {
    const effectiveLocale = applyUserLocalePreference(value);
    effectivePreferences = { ...value, locale: effectiveLocale };
    hydratePreferences(value, effectiveLocale);
    applyTheme(preferences.theme, preferences.appearance);
  }

  $effect(() => {
    const value = userPreferences.data;
    if (!value || value.version === hydratedPreferencesVersion) return;
    hydratedPreferencesVersion = value.version;
    applyServerPreferences(value);
  });
  $effect(() => {
    if (computerItems.length === 1 && selection.computerId !== computerItems[0].id) {
      storeSetComputer(computerItems[0].id);
    }
  });
  $effect(() => {
    if (projects.isSuccess && selectedProjectId && selection.projectId !== selectedProjectId) {
      if (confirmProjectChange(selectedProjectId)) storeSetProject(selectedProjectId);
    }
  });

  const savePreferences = createMutation<UserPreferences, Error, UserPreferencesPatch>(() => ({
    mutationFn: (patch) => {
      const current = userPreferences.data;
      if (!current) throw new Error(t("settings.preferencesUnavailable"));
      return api<UserPreferences>("/me/preferences", {
        method: "PATCH",
        body: { expected_version: current.version, ...patch }
      });
    },
    onSuccess: (value) => {
      queryClient.setQueryData(["user-preferences"], value);
      hydratedPreferencesVersion = value.version;
      applyServerPreferences(value);
    }
  }));

  async function updatePreferences(patch: UserPreferencesPatch): Promise<UserPreferences> {
    const previous = userPreferences.data;
    if (!previous) throw new Error(t("settings.preferencesUnavailable"));
    if (patch.locale) changeLocale(patch.locale);
    if (patch.appearance) setAppearance(patch.appearance);
    if (patch.theme) {
      setTheme(patch.theme);
      applyTheme(preferences.theme, preferences.appearance);
    }
    if (patch.allow_write !== undefined) setAllowWrite(patch.allow_write);
    try {
      return await savePreferences.mutateAsync(patch);
    } catch (preferenceError) {
      hydratedPreferencesVersion = previous.version;
      applyServerPreferences(previous);
      throw preferenceError;
    }
  }

  const computer = $derived(computerItems.find((item) => item.id === selectedComputerId));

  // Wails v2 does not guarantee that its runtime bridge is available during
  // the first Svelte render. The custom wails: protocol is stable and lets the
  // title bar reserve the native macOS traffic-light safe area immediately.
  const isDesktopShell = typeof window !== "undefined" && window.location.protocol === "wails:";

  function refresh() {
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: ["computers"] }),
      queryClient.invalidateQueries({ queryKey: ["projects"] }),
      queryClient.invalidateQueries({ queryKey: ["agent-services"] })
    ]);
  }

  const context = $state<ConsoleContext>({
    user: me.data!,
    computers: [],
    projects: [],
    services: [],
    agents: [],
    computer: undefined,
    project: undefined,
    preferences: {
      user_id: "",
      locale: localeInfo.value,
      locale_explicit: false,
      theme: "system",
      allow_write: false,
      version: 1
    },
    preferencesSaving: false,
    updatePreferences,
    openProjectCreator: openPjCreate,
    refresh
  });
  provideConsoleContext(context);
  $effect(() => {
    if (me.data) context.user = me.data;
    context.computers = computers.data?.items ?? [];
    context.projects = projectItems;
    context.services = services.data?.items ?? [];
    context.agents = agents.data ?? [];
    context.computer = computer;
    context.project = project;
    if (userPreferences.data) context.preferences = effectivePreferences;
    context.preferencesSaving = savePreferences.isPending;
  });

  const projectNav = $derived([
    { to: "/workbench", icon: LayoutDashboard, label: t("shell.workbench") },
    { to: "/issues", icon: MessageSquareText, label: t("shell.issues") },
    { to: "/conversations", icon: GitFork, label: t("shell.conversations") },
    { to: "/resources", icon: FolderKanban, label: t("shell.resources") },
    { to: "/memories", icon: FileText, label: t("shell.memories") },
    { to: "/schedules", icon: CalendarClock, label: t("shell.schedules") },
    { to: "/changes", icon: GitCompareArrows, label: t("shell.changes") }
  ]);
  const localNav = $derived([
    { to: "/agents", icon: Bot, label: t("shell.agents") },
    { to: "/skills", icon: PackageOpen, label: t("shell.skills") }
  ]);
  const globalNav = $derived([
    { to: "/settings", icon: SettingsIcon, label: t("shell.settings") },
    { to: "/work-records", icon: ActivityIcon, label: t("work.title") },
    { to: "/inspirations", icon: FileText, label: t("inspirations.title") }
  ]);

  // 上下文选择面板（fixed 定位在按钮下方，避免被 aside 的 overflow 裁剪）。
  let projectOpen = $state(false);
  let moreOpen = $state(false);
  let popup = $state<{ left: number; top: number; width: number } | null>(null);

  // 移动端「更多」抽屉在路由变化后自动关闭。
  $effect(() => {
    location.path;
    moreOpen = false;
  });

  function openProjectPanel(event: MouseEvent) {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    popup = { left: rect.left, top: rect.bottom + 6, width: rect.width };
    projectOpen = true;
  }

  function closePanels() {
    projectOpen = false;
    popup = null;
  }

  // ---- 项目弹窗（配置在项目入口，不进全局设置页） ----
  let pjCreateOpen = $state(false);
  let pjManageOpen = $state(false);
  let pjDeleteConfirm = $state(false);
  let pjManageId = $state<string | null>(null);
  let pjFormName = $state("");
  let pjFormDescription = $state("");

  const managedProject = $derived(projectItems.find((item) => item.id === pjManageId));

  function openPjCreate() {
    createProject.reset();
    moreOpen = false;
    pjFormName = "";
    pjFormDescription = "";
    pjCreateOpen = true;
    closePanels();
  }

  function openPjManage(id: string) {
    updateProject.reset();
    deleteProject.reset();
    moreOpen = false;
    const item = projectItems.find((p) => p.id === id);
    pjManageId = id;
    pjFormName = item?.name ?? "";
    pjFormDescription = item?.description ?? "";
    pjManageOpen = true;
    closePanels();
  }

  function closeProjectManager() {
    pjManageOpen = false;
  }

  function confirmProjectDeletion() {
    if (!managedProject || managedProject.is_default) return;
    if (managedProject?.id === selectedProjectId && !confirmProjectChange(projectItems.find((item) => item.is_default)?.id ?? "")) return;
    pjDeleteConfirm = true;
  }

  const createProject = createMutation<Project, Error, void>(() => ({
    mutationFn: () =>
      api<Project>("/projects", {
        body: {
          computer_id: selectedComputerId,
          name: pjFormName.trim(),
          description: pjFormDescription.trim()
        }
      }),
    onSuccess: async (item) => {
      queryClient.setQueryData<Page<Project>>(["projects", selectedComputerId], (current) =>
        current
          ? {
              ...current,
              items: [...current.items.filter((projectItem) => projectItem.id !== item.id), item]
            }
          : current
      );
      changeProject(item.id);
      pjCreateOpen = false;
      await queryClient.invalidateQueries({ queryKey: ["projects", selectedComputerId] });
    }
  }));

  const updateProject = createMutation<Project, Error, void>(() => ({
    mutationFn: () =>
      api<Project>(`/projects/${managedProject!.id}`, {
        method: "PATCH",
        body: {
          expected_version: managedProject!.version,
          name: pjFormName.trim() || managedProject!.name,
          description: pjFormDescription.trim()
        }
      }),
    onSuccess: async (item) => {
      queryClient.setQueryData<Page<Project>>(["projects", selectedComputerId], (current) =>
        current
          ? {
              ...current,
              items: current.items.map((projectItem) =>
                projectItem.id === item.id ? item : projectItem
              )
            }
          : current
      );
      pjManageOpen = false;
      await queryClient.invalidateQueries({ queryKey: ["projects", selectedComputerId] });
    }
  }));

  const deleteProject = createMutation<void, Error, void>(() => ({
    mutationFn: () => {
      if (!managedProject || managedProject.is_default) throw new Error(t("defaultProject.hint"));
      return api<void>(`/projects/${managedProject.id}?expected_version=${managedProject.version}`, {
        method: "DELETE"
      });
    },
    onSuccess: async () => {
      pjManageOpen = false;
      pjDeleteConfirm = false;
      if (selection.projectId === managedProject!.id) storeSetProject(projectItems.find((item) => item.is_default)?.id ?? "");
      await queryClient.invalidateQueries({ queryKey: ["projects", selectedComputerId] });
    }
  }));

  const topError = $derived(me.error ?? userPreferences.error ?? computers.error ?? projects.error);
  function changeProject(id: string): boolean {
    if (id === selectedProjectId) return true;
    if (!confirmProjectChange(id)) return false;
    storeSetProject(id);
    return true;
  }
  const knownRoute = $derived(
    location.path === "/programming-tools" ||
    [...projectNav, ...localNav, ...globalNav].some((item) => item.to === location.path)
  );
</script>

<svelte:window
  onresize={closePanels}
  onkeydown={(event) => {
    if (event.key === "Escape") {
      closePanels();
      moreOpen = false;
    }
  }}
/>

{#if me.isLoading || userPreferences.isLoading || computers.isLoading || projects.isLoading}
  <Loading label={t("shell.loadingConsole")} />
{:else if topError || !me.data}
  <ErrorNotice
    error={topError ?? new Error(t("shell.consoleError"))}
    onRetry={() => window.location.reload()}
  />
{:else}
  <div
    class="grid h-dvh overflow-hidden bg-canvas [grid-template-areas:'topbar_topbar'_'sidebar_workspace'] [grid-template-rows:var(--topbar-height)_minmax(0,1fr)] [grid-template-columns:var(--sidebar-width)_minmax(0,1fr)] max-md:[grid-template-areas:'topbar'_'workspace'_'mobile-nav'] max-md:[grid-template-rows:var(--topbar-height)_minmax(0,1fr)_58px] max-md:[grid-template-columns:1fr]"
  >
    <header
      class="grid cursor-default select-none grid-cols-[minmax(216px,1fr)_minmax(200px,440px)_minmax(216px,1fr)] items-center gap-4 border-b border-border bg-canvas pr-3 max-md:grid-cols-[minmax(0,1fr)_auto_auto] max-md:gap-2 max-md:px-3 [grid-area:topbar]"
      style="--wails-draggable: drag"
    >
      <div
        class="flex h-full items-center gap-2 max-md:min-w-0 {isDesktopShell
          ? 'min-w-[286px]'
          : 'min-w-[var(--sidebar-width)]'}"
      >
        <Link
          to="/workbench"
          class="flex h-full min-w-0 cursor-pointer items-center gap-2.5 border-0 bg-transparent text-md font-semibold [--wails-draggable:no-drag] max-md:px-0 {isDesktopShell
            ? 'pl-[100px] pr-4'
            : 'px-4'}"
        >
          <img src={brandIcon} alt="" class="size-7 shrink-0" />
          <span class="truncate">Teamva</span>
        </Link>
        <div class="flex shrink-0 items-center gap-1" style="--wails-draggable: no-drag">
          <PendingApprovals />
          <FeatureTours userId={me.data.id} path={location.path}
            enabled={Boolean(project) && onboardingStatus.isSuccess && !onboardingOverlay.requested && !projectOpen && !moreOpen &&
              !(onboardingStatus.data?.needed && (location.path === "/workbench" || location.path === "/"))} />
        </div>
      </div>
      <div class="flex min-w-0 items-center justify-center" style="--wails-draggable: no-drag">
        <GlobalSearch projectId={project?.id} projectName={project?.name ?? ""} />
      </div>
      <div class="flex min-w-0 items-center gap-1.5 max-md:ml-0 md:hidden">
        <Select controlSize="sm"
          value={selectedProjectId}
          aria-label={t("shell.currentProject")}
          disabled={!selectedComputerId}
          onchange={(event) => {
            const select = event.target as HTMLSelectElement;
            if (!changeProject(select.value)) select.value = selectedProjectId;
          }}
          class="max-w-[130px] border-transparent bg-transparent text-xs text-muted"
        >
          <option value="">{t("shell.selectProject")}</option>
          {#each projectItems as item (item.id)}
            <option value={item.id}>{item.name}{item.is_default ? ` · ${t("defaultProject.badge")}` : ""}</option>
          {/each}
        </Select>
      </div>
    </header>

    <aside
      class="flex min-w-0 flex-col justify-between overflow-hidden border-r border-border bg-surface-subtle px-3 py-4 max-md:hidden [grid-area:sidebar]"
    >
      <div class="min-h-0 overflow-y-auto overflow-x-hidden">
        <div class="mb-5 grid gap-1">
          <div
            class="flex min-h-9 w-full items-center gap-2 px-2.5 text-sm font-semibold text-text"
          >
            <Server size={15} class="shrink-0 text-muted" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate text-left"
              >{computer?.name ?? t("shell.localComputer")}</span
            >
          </div>
          <div class="mx-2 h-px bg-border" aria-hidden="true"></div>
          <button
            class="flex min-h-9 w-full cursor-pointer items-center gap-2 rounded-sm border-0 bg-transparent pl-2.5 pr-3.5 text-sm text-text-secondary transition-colors hover:bg-surface-hover hover:text-text disabled:pointer-events-none disabled:opacity-50 disabled:hover:bg-transparent"
            type="button"
            title={t("shell.projectHint")}
            aria-label={t("shell.selectProject")}
            aria-haspopup="dialog"
            aria-expanded={projectOpen}
            disabled={!selectedComputerId}
            onclick={openProjectPanel}
          >
            <Folder size={15} class="shrink-0 text-muted" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate text-left"
              >{project?.name ?? t("shell.selectProject")}</span
            >
            <ChevronsUpDown size={13} class="mr-0.5 shrink-0 text-muted" aria-hidden="true" />
          </button>
        </div>
        <nav aria-label={t("shell.projectNav")} class="grid gap-1">
          <span class="mb-2 block text-xs font-semibold text-muted">{t("shell.projectNav")}</span>
          {#each projectNav as item (item.to)}
            <NavLink
              to={item.to}
              class="flex min-h-9 w-full cursor-pointer items-center gap-2.5 rounded-sm border border-transparent px-3 text-left text-sm text-secondary hover:bg-surface-hover hover:text-text"
              activeClass="border-border bg-surface font-semibold !text-text shadow-[inset_3px_0_0_0_var(--color-accent),0_1px_2px_var(--color-shadow)]"
            >
              {#snippet children(_state)}
                <item.icon size={17} aria-hidden="true" />
                <span class="min-w-0 truncate">{item.label}</span>
              {/snippet}
            </NavLink>
          {/each}
        </nav>
        <nav class="mt-5 grid gap-1" aria-label={t("shell.local")}>
          <span class="mb-2 block text-xs font-semibold text-muted">{t("shell.local")}</span>
          {#each localNav as item (item.to)}
            <NavLink
              to={item.to}
              class="flex min-h-9 w-full cursor-pointer items-center gap-2.5 rounded-sm border border-transparent px-3 text-left text-sm text-secondary hover:bg-surface-hover hover:text-text"
              activeClass="border-border bg-surface font-semibold !text-text shadow-[inset_3px_0_0_0_var(--color-accent),0_1px_2px_var(--color-shadow)]"
            >
              {#snippet children(_state)}
                <item.icon size={17} aria-hidden="true" />
                <span class="min-w-0 truncate">{item.label}</span>
              {/snippet}
            </NavLink>
          {/each}
        </nav>
      </div>
      <nav class="grid shrink-0 gap-1 border-t border-border pt-3" aria-label={t("shell.system")}>
        {#each globalNav as item (item.to)}
          <NavLink
            to={item.to}
            class="flex min-h-9 w-full cursor-pointer items-center gap-2.5 rounded-sm border border-transparent px-3 text-left text-sm text-secondary hover:bg-surface-hover hover:text-text"
            activeClass="border-border bg-surface font-semibold !text-text shadow-[inset_3px_0_0_0_var(--color-accent),0_1px_2px_var(--color-shadow)]"
          >
            {#snippet children(_state)}
              <item.icon size={17} aria-hidden="true" />
              <span class="min-w-0 truncate">{item.label}</span>
            {/snippet}
          </NavLink>
        {/each}
      </nav>
    </aside>
    <main
      class="flex min-h-0 min-w-0 flex-col bg-surface p-0 [grid-area:workspace] overflow-hidden"
    >
      {#if onboardingStatus.data?.needed && !onboardingOverlay.requested && location.path !== "/workbench" && location.path !== "/"}
        <div
          class="flex shrink-0 items-center gap-3 border-b border-warning/40 bg-warning-subtle px-5 py-2 text-xs text-warning max-md:px-4"
          role="status"
        >
          <span class="min-w-0 flex-1">{t("onboarding.banner")}</span>
          <Button
            class="shrink-0"
            type="button"
            onclick={requestOnboarding}>{t("onboarding.bannerAction")}</Button
          >
        </div>
      {/if}
      <div
        class="min-h-0 flex-1 {location.path === '/inspirations' ||
        location.path === '/work-records' ||
        location.path === '/issues' ||
        location.path === '/conversations' ||
        location.path === '/changes' ||
        location.path === '/agents' ||
        location.path === '/skills' ||
        location.path === '/programming-tools' ||
        location.path === '/resources' ||
        location.path === '/memories' ||
        location.path === '/schedules'
          ? 'overflow-hidden'
          : 'overflow-y-auto'}"
      >
        <Route path="/workbench"><Workbench /></Route>
        <Route path="/changes">
          {#if changesError}<ErrorNotice
              error={changesError}
              onRetry={loadChanges}
            />{:else if ChangesComponent}<ChangesComponent />{:else}<Loading
              label={t("common.loading")}
            />{/if}
        </Route>
        <Route path="/issues"
          >{#key selectedProjectId}<Issues />{/key}</Route
        >
        <Route path="/conversations"
          >{#key selectedProjectId}<Conversations />{/key}</Route
        >
        <Route path="/agents"><Agents /></Route>
        <Route path="/skills"><LocalSkills /></Route>
        <Route path="/programming-tools"><ProgrammingTools /></Route>
        <Route path="/resources"
          >{#key selectedProjectId}<Resources />{/key}</Route
        >
        <Route path="/memories"
          >{#key selectedProjectId}<Memories />{/key}</Route
        >
        <Route path="/schedules"
          >{#key selectedProjectId}<Schedules />{/key}</Route
        >
        <Route path="/settings"><Settings /></Route>
        <Route path="/work-records"><WorkRecords /></Route>
        <Route path="/inspirations"><Inspirations /></Route>
        {#if !knownRoute}
          <EmptyState title={t("shell.pageNotFound")} description={t("shell.pageNotFoundDesc")}>
            {#snippet action()}<Link to="/workbench" class="text-sm text-accent underline"
                >{t("shell.workbench")}</Link
              >{/snippet}
          </EmptyState>
        {/if}
      </div>
    </main>
    <nav
      class="hidden grid-cols-6 border-t border-border bg-surface max-md:grid [grid-area:mobile-nav]"
      aria-label={t("shell.projectNav")}
    >
      {#each projectNav.slice(0, 5) as item (item.to)}
        <NavLink
          to={item.to}
          ariaLabel={item.label}
          class="grid min-w-0 content-center place-items-center gap-[3px] text-[10px] text-muted"
          activeClass="font-semibold !text-accent"
        >
          {#snippet children(_state)}
            <item.icon size={18} />
            <span class="max-w-full truncate px-0.5">{item.label}</span>
          {/snippet}
        </NavLink>
      {/each}
      <button
        class="grid min-w-0 cursor-pointer content-center place-items-center gap-[3px] border-0 bg-transparent text-[10px] {moreOpen
          ? 'font-semibold text-accent'
          : 'text-muted'}"
        type="button"
        aria-expanded={moreOpen}
        onclick={() => (moreOpen = !moreOpen)}
      >
        <Menu size={18} />
        <span class="max-w-full truncate px-0.5">{t("shell.more")}</span>
      </button>
    </nav>

    {#if moreOpen}
      <button
        class="fixed inset-0 z-40 hidden cursor-default border-0 bg-black/20 p-0 max-md:block"
        type="button"
        aria-label={t("common.close")}
        onclick={() => (moreOpen = false)}
      ></button>
      <div
        class="fixed inset-x-0 bottom-[58px] z-50 hidden border-t border-border bg-surface p-2 shadow-raised max-md:block"
      >
        <div class="mb-2 flex gap-2 border-b border-border pb-2">
          <Button
            class="flex-1"
            type="button"
            onclick={openPjCreate}>{t("shell.newProject")}</Button
          >
          {#if selectedProjectId}<Button
              class="flex-1"
              type="button"
              onclick={() => openPjManage(selectedProjectId)}>{t("shell.manageProject")}</Button
            >{/if}
        </div>
        <div class="grid grid-cols-3 gap-1">
          {#each [...localNav, ...projectNav.slice(5), ...globalNav] as item (item.to)}
            <NavLink
              to={item.to}
              ariaLabel={item.label}
              class="flex min-h-11 items-center gap-2 rounded-sm px-2.5 text-xs text-secondary hover:bg-surface-hover hover:text-text"
              activeClass="bg-surface-hover font-semibold !text-accent"
            >
              {#snippet children(_state)}
                <item.icon size={16} />
                <span class="min-w-0 truncate">{item.label}</span>
              {/snippet}
            </NavLink>
          {/each}
        </div>
      </div>
    {/if}
  </div>

  {#if projectOpen && popup}
    <button
      class="fixed inset-0 z-40 cursor-default border-0 bg-transparent p-0"
      type="button"
      aria-hidden="true"
      tabindex="-1"
      onclick={closePanels}
    ></button>
    <div
      class="fixed z-50 overflow-y-auto rounded-md border border-border bg-surface p-1.5 shadow-raised"
      style={`left:${popup.left}px;top:${popup.top}px;width:${popup.width}px;max-height:calc(100dvh - ${popup.top + 16}px)`}
      role="dialog"
      aria-label={t("shell.selectProject")}
    >
      {#if !selectedComputerId}
        <p class="px-2.5 py-2 text-xs text-muted">{t("shell.selectComputerFirst")}</p>
      {:else if !projectItems.length}
        <p class="px-2.5 py-2 text-xs text-muted">{t("shell.noProjects")}</p>
      {/if}
      {#each projectItems as item (item.id)}
        <div class="flex items-center gap-1" data-project-id={item.id} data-default-project={item.is_default || undefined}>
          <button
            class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-sm px-2.5 py-2 text-left text-sm text-text hover:bg-surface-hover"
            type="button"
            aria-pressed={item.id === selectedProjectId}
            onclick={() => {
              if (changeProject(item.id)) closePanels();
            }}
          >
            <span class="min-w-0 flex-1 truncate">{item.name}</span>
            {#if item.is_default}<span class="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-2xs text-muted">{t("defaultProject.badge")}</span>{/if}
            {#if item.id === selectedProjectId}<Check
                size={14}
                class="shrink-0 text-accent"
                aria-hidden="true"
              />{/if}
          </button>
          <Button
            controlSize="sm" icon class="shrink-0"
            type="button"
            aria-label={t("shell.manageProject")}
            onclick={() => openPjManage(item.id)}
          >
            <Settings2 size={14} aria-hidden="true" />
          </Button>
        </div>
      {/each}
      <div class="mt-1 border-t border-border pt-1">
        <Button
          variant="ghost" class="w-full !justify-start"
          type="button"
          disabled={!selectedComputerId}
          onclick={openPjCreate}
        >
          <Plus size={13} aria-hidden="true" />{t("shell.newProject")}
        </Button>
      </div>
    </div>
  {/if}

  {#if pjCreateOpen}
    <Modal title={t("shell.newProject")} onClose={() => (pjCreateOpen = false)}>
      {#if createProject.error}<ErrorNotice error={createProject.error} />{/if}
      <form
        class="grid gap-4"
        onsubmit={(event) => {
          event.preventDefault();
          createProject.mutate();
        }}
      >
        <Field label={t("settings.projectName")}>
          <Input
            bind:value={pjFormName}
            placeholder={t("settings.projectNamePlaceholder")}
            required
          />
        </Field>
        <Field label={t("settings.description")}>
          <Textarea
            rows={3}
            bind:value={pjFormDescription}
            placeholder={t("settings.projectDescPlaceholder")}></Textarea>
        </Field>
        <div class="flex justify-end gap-2">
          <Button
            type="button"
            onclick={() => (pjCreateOpen = false)}>{t("common.cancel")}</Button
          >
          <Button type="submit" variant="primary"
            disabled={!pjFormName.trim() || createProject.isPending}>{t("settings.create")}</Button
          >
        </div>
      </form>
    </Modal>
  {/if}

  {#if pjManageOpen && managedProject}
    <Modal
      title={t("shell.manageProject")}
      description={managedProject.name}
      onClose={closeProjectManager}
    >
      {#if updateProject.error || deleteProject.error}<ErrorNotice
          error={updateProject.error ?? deleteProject.error}
        />{/if}
      <form
        class="grid gap-4"
        onsubmit={(event) => {
          event.preventDefault();
          updateProject.mutate();
        }}
      >
        <Field label={t("settings.projectName")} hint={managedProject.is_default ? t("defaultProject.hint") : undefined}>
          <Input
            bind:value={pjFormName}
            placeholder={t("settings.projectNamePlaceholder")}
            readonly={managedProject.is_default}
            required
          />
        </Field>
        <Field label={t("settings.description")}>
          <Textarea
            rows={3}
            bind:value={pjFormDescription}
            placeholder={t("settings.projectDescPlaceholder")}></Textarea>
        </Field>
        <div class="flex items-center justify-between gap-2">
          {#if !managedProject.is_default}<Button variant="danger"
            type="button"
            onclick={confirmProjectDeletion}>{t("settings.deleteProject")}</Button
          >{:else}<span></span>{/if}
          <div class="flex gap-2">
            <Button
              type="button"
              onclick={closeProjectManager}>{t("common.cancel")}</Button
            >
            <Button type="submit" variant="primary"
              disabled={updateProject.isPending}>{t("common.save")}</Button
            >
          </div>
        </div>
      </form>
    </Modal>
    {#if pjDeleteConfirm && !managedProject.is_default}
      <ConfirmDialog
        title={t("settings.deleteProject")}
        description={t("settings.deleteProjectConfirm", { name: managedProject.name })}
        confirmLabel={t("settings.deleteProject")}
        danger={true}
        busy={deleteProject.isPending}
        error={deleteProject.error}
        onConfirm={() => deleteProject.mutate()}
        onCancel={() => (pjDeleteConfirm = false)}
      />
    {/if}
  {/if}
{/if}
