
<script lang="ts">
  import { Button } from "../ui";
  import type { Snippet } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import { useConsole } from "../pages/shared.svelte";

  let { children }: { children: Snippet } = $props();
  const context = useConsole();

  const hasComputer = $derived(Boolean(context.computer));
  const hasProject = $derived(Boolean(context.project));
</script>

{#if !hasComputer}
  <section class="mx-auto my-12 max-w-[560px] px-8 py-8 text-center">
    <h2 class="my-2 text-lg font-semibold">{t("shell.consoleError")}</h2>
    <p class="mx-auto max-w-[460px] text-sm leading-[1.6] text-secondary">{t("shell.localComputerUnavailable")}</p>
  </section>
{:else if !hasProject}
  <section class="mx-auto my-12 max-w-[560px] px-8 py-8 text-center">
    <span class="inline-flex items-center whitespace-nowrap rounded-sm border border-border bg-surface-subtle px-3 py-1 text-xs font-medium tabular-nums text-secondary">{context.computer?.name}</span>
    <h2 class="my-2 text-lg font-semibold">{t("settings.noProjectTitle")}</h2>
    <p class="mx-auto mb-5 max-w-[460px] text-sm leading-[1.6] text-secondary">{t("settings.noProjectDesc")}</p>
    <div class="flex justify-center gap-3">
      <Button variant="primary" type="button" onclick={context.openProjectCreator}>
        {t("settings.createProjectAction")}
      </Button>
    </div>
  </section>
{:else}
  {@render children()}
{/if}
