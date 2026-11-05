<script lang="ts">
  import { CircleDashed, HardDrive } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import ProgrammingToolsPanel from "../components/ProgrammingToolsPanel.svelte";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const computer = $derived(context.computer);
</script>

{#if !computer}
  <div class="grid h-full place-items-center p-4">
    <EmptyState
      title={t("programmingTools.needComputer")}
      description={t("programmingTools.needComputerDesc")}
    />
  </div>
{:else}
  <div class="flex h-full min-h-0 flex-col bg-surface">
    <PageHeader title={t("programmingTools.title")} description={t("programmingTools.desc")}>
      {#snippet actions()}
        <span
          class="inline-flex min-h-8 items-center gap-1.5 rounded-full border border-border bg-surface-subtle px-3 text-xs font-medium text-secondary"
        >
          <HardDrive size={13} />{computer.name}
        </span>
      {/snippet}
    </PageHeader>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <ProgrammingToolsPanel {computer} />

      <footer
        class="flex items-start gap-2.5 px-5 py-4 text-xs leading-relaxed text-muted max-md:px-4"
      >
        <CircleDashed size={15} class="mt-0.5 shrink-0" aria-hidden="true" />
        <p>{t("programmingTools.futureHint")}</p>
      </footer>
    </div>
  </div>
{/if}
