<script lang="ts">
  import { Button } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    Download, ExternalLink, FolderOpen, GripVertical, HardDrive, LoaderCircle,
    PackageOpen, RefreshCw, ShoppingBag, Trash2
  } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type { ComputerSkillPackage, Page, SkillPackage } from "../lib/types";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Loading from "../components/Loading.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import { useConsole } from "./shared.svelte";
  import McpServers from "./McpServers.svelte";
  import { location, setQueryParam } from "../lib/router.svelte";

  type DraggedSkill =
    | { origin: "store"; packageId: string }
    | { origin: "installed"; installation: ComputerSkillPackage };

  const context = useConsole();
  const computer = $derived(context.computer);
  const tab = $derived(new URLSearchParams(location.search).get("tab") === "mcp" ? "mcp" : "skills");
  function tabKey(event: KeyboardEvent) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === "Home" ? "skills" : event.key === "End" ? "mcp" : tab === "mcp" ? "skills" : "mcp";
    setQueryParam("tab", next === "mcp" ? "mcp" : null);
    document.getElementById(`skills-tab-${tab}`)?.focus();
  }
  const packages = createQuery<Page<SkillPackage>>(() => ({
    queryKey: ["skill-packages"],
    queryFn: () => api<Page<SkillPackage>>("/skill-packages"),
    enabled: Boolean(computer) && tab === "skills"
  }));
  const installations = createQuery<Page<ComputerSkillPackage>>(() => ({
    queryKey: ["computer-skill-packages", computer?.id],
    queryFn: () => api<Page<ComputerSkillPackage>>(`/computers/${computer!.id}/skill-packages`),
    enabled: Boolean(computer) && tab === "skills"
  }));

  const packageItems = $derived(packages.data?.items ?? []);
  const installedItems = $derived(installations.data?.items ?? []);
  const installedPackageIds = $derived(new Set(installedItems.map((item) => item.skill_package_id)));
  const storeItems = $derived(packageItems.filter((item) => !installedPackageIds.has(item.id)));
  let dragged = $state<DraggedSkill | null>(null);
  let installingPackageId = $state("");
  let openingInstallationId = $state("");

  function packageDetail(packageId: string): SkillPackage | undefined {
    return packageItems.find((item) => item.id === packageId);
  }

  async function refreshSkills() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["computer-skill-packages", computer?.id] }),
      queryClient.invalidateQueries({ queryKey: ["agents", computer?.id] })
    ]);
  }

  const installSkill = createMutation<ComputerSkillPackage, Error, string>(() => ({
    mutationFn: (packageId) => api<ComputerSkillPackage>(`/computers/${computer!.id}/skill-packages`, {
      body: { skill_package_id: packageId }
    }),
    onMutate: (packageId) => (installingPackageId = packageId),
    onSuccess: refreshSkills,
    onSettled: () => (installingPackageId = "")
  }));

  const uninstallSkill = createMutation<void, Error, ComputerSkillPackage>(() => ({
    mutationFn: (installation) => api<void>(
      `/computers/${computer!.id}/skill-packages/${installation.id}?expected_version=${installation.version}`,
      { method: "DELETE" }
    ),
    onSuccess: refreshSkills
  }));

  const openSkill = createMutation<unknown, Error, ComputerSkillPackage>(() => ({
    mutationFn: (installation) => api<unknown>(
      `/computers/${computer!.id}/skill-packages/${installation.id}/open`,
      { method: "POST" }
    ),
    onMutate: (installation) => (openingInstallationId = installation.id),
    onSettled: () => (openingInstallationId = "")
  }));

  function startStoreDrag(event: DragEvent, packageId: string) {
    dragged = { origin: "store", packageId };
    event.dataTransfer?.setData("text/plain", packageId);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
  }

  function startInstalledDrag(event: DragEvent, installation: ComputerSkillPackage) {
    dragged = { origin: "installed", installation };
    event.dataTransfer?.setData("text/plain", installation.id);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
  }

  function allowDrop(event: DragEvent) {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  }

  function dropOnInstalled(event: DragEvent) {
    event.preventDefault();
    if (dragged?.origin === "store") installSkill.mutate(dragged.packageId);
    dragged = null;
  }

  function dropOnStore(event: DragEvent) {
    event.preventDefault();
    if (dragged?.origin === "installed") uninstallSkill.mutate(dragged.installation);
    dragged = null;
  }
</script>

{#if !computer}
  <div class="grid h-full place-items-center p-4">
    <EmptyState title={t("localSkills.needComputer")} description={t("localSkills.needComputerDesc")} />
  </div>
{:else}
  <div class="flex h-full min-h-0 flex-col bg-surface">
    <PageHeader title={t("mcp.title")} description={t("mcp.description")} />
    <div class="flex shrink-0 gap-2 border-b border-border px-5 py-2 max-md:px-3" role="tablist" aria-label={t("mcp.title")}>
      <Button id="skills-tab-skills" role="tab" aria-controls="skills-panel" tabindex={tab === "skills" ? 0 : -1} aria-selected={tab === "skills"} variant={tab === "skills" ? "primary" : "ghost"} onkeydown={tabKey} onclick={() => setQueryParam("tab", null)}>{t("mcp.skillsTab")}</Button>
      <Button id="skills-tab-mcp" role="tab" aria-controls="mcp-panel" tabindex={tab === "mcp" ? 0 : -1} aria-selected={tab === "mcp"} variant={tab === "mcp" ? "primary" : "ghost"} onkeydown={tabKey} onclick={() => setQueryParam("tab", "mcp")}>{t("mcp.tab")}</Button>
    </div>
    {#if tab === "mcp"}
      <div id="mcp-panel" role="tabpanel" aria-labelledby="skills-tab-mcp" class="flex min-h-0 flex-1 flex-col"><McpServers /></div>
    {:else}

    <div id="skills-panel" role="tabpanel" aria-labelledby="skills-tab-skills" class="min-h-0 flex-1 overflow-hidden p-5 max-md:p-3">
      {#if packages.error || installations.error || installSkill.error || uninstallSkill.error || openSkill.error}
        <div class="mb-3"><ErrorNotice error={packages.error ?? installations.error ?? installSkill.error ?? uninstallSkill.error ?? openSkill.error} /></div>
      {/if}
      <section class="grid h-full min-h-0 grid-cols-2 gap-4 max-lg:grid-cols-1 max-lg:grid-rows-2" aria-label={t("localSkills.transferLabel")}>
        <section
          class="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-md border border-border bg-surface"
          aria-label={t("localSkills.installed")}
          ondragover={allowDrop}
          ondrop={dropOnInstalled}
        >
          <header class="flex min-h-14 shrink-0 items-center justify-between gap-3 border-b border-border bg-surface-subtle px-4">
            <div class="flex min-w-0 items-center gap-2.5">
              <span class="grid size-8 shrink-0 place-items-center rounded-sm bg-accent-subtle text-accent"><HardDrive size={16} /></span>
              <span class="min-w-0">
                <strong class="block text-sm font-semibold">{t("localSkills.installed")}</strong>
                <small class="block truncate text-2xs text-muted">{t("localSkills.installedHint")}</small>
              </span>
            </div>
            <span class="rounded-full bg-surface px-2 py-0.5 text-2xs tabular-nums text-muted">{installedItems.length}</span>
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto p-3">
            {#if installations.isLoading}
              <Loading block />
            {:else if !installedItems.length}
              <div class="grid h-full min-h-48 place-items-center rounded-sm border border-dashed border-border-strong p-6 text-center">
                <span><PackageOpen size={24} class="mx-auto mb-2 text-muted" /><strong class="block text-xs">{t("localSkills.noneInstalled")}</strong><small class="mt-1 block max-w-[300px] text-2xs leading-relaxed text-muted">{t("localSkills.noneInstalledHint")}</small></span>
              </div>
            {:else}
              <ul class="grid gap-2">
                {#each installedItems as installation (installation.id)}
                  {@const skill = packageDetail(installation.skill_package_id)}
                  <li
                    class="grid cursor-grab select-none grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-2.5 rounded-sm border border-border bg-surface px-3 py-3 active:cursor-grabbing"
                    draggable={true}
                    ondragstart={(event) => startInstalledDrag(event, installation)}
                    ondragend={() => (dragged = null)}
                  >
                    <GripVertical size={14} class="mt-0.5 shrink-0 text-muted" />
                    <span class="min-w-0">
                      <strong class="block truncate text-xs font-semibold">{skill?.name ?? installation.skill_package_id}</strong>
                      {#if skill?.description}<span class="mt-1 line-clamp-2 block text-2xs leading-relaxed text-muted">{skill.description}</span>{/if}
                      <span class="mt-2 flex flex-wrap items-center gap-2 text-[10px] text-muted">
                        <span>{skill?.license || t("localSkills.licenseUnknown")}</span>
                        {#if skill?.source_url}<a class="inline-flex items-center gap-0.5 text-accent hover:underline" href={skill.source_url} target="_blank" rel="noreferrer">{t("localSkills.source")}<ExternalLink size={10} /></a>{/if}
                      </span>
                    </span>
                    <span class="flex items-center gap-1.5">
					  <Button
						controlSize="sm"
						type="button"
						disabled={installSkill.isPending || !skill}
						onclick={() => skill && installSkill.mutate(skill.id)}
					  >
						{#if skill && installingPackageId === skill.id}<LoaderCircle size={12} class="animate-spin" />{t("localSkills.installing")}{:else}<RefreshCw size={12} />{t("localSkills.reinstall")}{/if}
					  </Button>
                      <Button
                        controlSize="sm"
                        type="button"
                        disabled={openSkill.isPending}
                        onclick={() => openSkill.mutate(installation)}
                      >
                        {#if openingInstallationId === installation.id}<LoaderCircle size={12} class="animate-spin" />{:else}<FolderOpen size={12} />{/if}
                        {t("resources.open")}
                      </Button>
                      <Button
                        variant="danger" controlSize="sm"
                        type="button"
                        disabled={uninstallSkill.isPending}
                        onclick={() => uninstallSkill.mutate(installation)}
                      ><Trash2 size={12} />{t("localSkills.uninstall")}</Button>
                    </span>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        </section>

        <section
          class="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-md border border-border bg-surface"
          aria-label={t("localSkills.store")}
          ondragover={allowDrop}
          ondrop={dropOnStore}
        >
          <header class="flex min-h-14 shrink-0 items-center justify-between gap-3 border-b border-border bg-surface-subtle px-4">
            <div class="flex min-w-0 items-center gap-2.5">
              <span class="grid size-8 shrink-0 place-items-center rounded-sm bg-surface text-secondary"><ShoppingBag size={16} /></span>
              <span class="min-w-0">
                <strong class="block text-sm font-semibold">{t("localSkills.store")}</strong>
                <small class="block truncate text-2xs text-muted">{t("localSkills.storeHint")}</small>
              </span>
            </div>
            <span class="rounded-full bg-surface px-2 py-0.5 text-2xs tabular-nums text-muted">{storeItems.length}</span>
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto p-3">
            {#if packages.isLoading}
              <Loading block />
            {:else if !storeItems.length}
              <div class="grid h-full min-h-48 place-items-center rounded-sm border border-dashed border-border p-6 text-center text-xs text-muted">{t("localSkills.storeEmpty")}</div>
            {:else}
              <ul class="grid gap-2">
                {#each storeItems as skill (skill.id)}
                  <li
                    class="grid cursor-grab select-none grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-2.5 rounded-sm border border-border bg-surface px-3 py-3 active:cursor-grabbing"
                    draggable={true}
                    ondragstart={(event) => startStoreDrag(event, skill.id)}
                    ondragend={() => (dragged = null)}
                  >
                    <GripVertical size={14} class="mt-0.5 shrink-0 text-muted" />
                    <span class="min-w-0">
                      <strong class="block truncate text-xs font-semibold">{skill.name}</strong>
                      <span class="mt-1 line-clamp-2 block text-2xs leading-relaxed text-muted">{skill.description}</span>
                      <span class="mt-2 flex flex-wrap items-center gap-2 text-[10px] text-muted">
                        <span>{skill.license || t("localSkills.licenseUnknown")}</span>
                        {#if skill.source_url}<a class="inline-flex items-center gap-0.5 text-accent hover:underline" href={skill.source_url} target="_blank" rel="noreferrer">{t("localSkills.source")}<ExternalLink size={10} /></a>{/if}
                      </span>
                    </span>
                    <Button
                      controlSize="sm"
                      type="button"
                      disabled={installSkill.isPending}
                      onclick={() => installSkill.mutate(skill.id)}
                    >
                      {#if installingPackageId === skill.id}
                        <LoaderCircle size={12} class="animate-spin" />{t("localSkills.installing")}
                      {:else}
                        <Download size={12} />{t("localSkills.install")}
                      {/if}
                    </Button>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        </section>
      </section>
    </div>
    {/if}
  </div>
{/if}
