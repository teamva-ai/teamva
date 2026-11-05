<script lang="ts">
  import { Button } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import {
    AlertTriangle,
    CheckCircle2,
    Clock3,
    LoaderCircle,
    MinusCircle,
    RefreshCw,
    SquareTerminal,
    Stethoscope,
    Unplug,
    XCircle
  } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import { formatTime } from "../lib/helpers";
  import { providerDefinitions } from "../lib/providers";
  import type {
    AgentService,
    Computer,
    Page,
    ProgrammingToolDiagnostic,
    ProgrammingToolDiagnosticStep
  } from "../lib/types";
  import ErrorNotice from "./ErrorNotice.svelte";
  import Modal from "../ui/Modal.svelte";
  import ProviderExecutableEditor from "./ProviderExecutableEditor.svelte";

  interface ToolDefinition {
    provider: string;
    name: string;
    command: string;
    descriptionKey?: string;
    description: string;
    installHint?: string;
    loginHint?: string;
    docsUrl?: string;
    steps: Array<{ id: string; label: string }>;
  }

  const tools: ToolDefinition[] = providerDefinitions.map((definition) => ({
    provider: definition.id,
    name: definition.display_name,
    command: definition.command,
    descriptionKey: definition.description_key,
    description: definition.description,
    installHint: definition.install_hint,
    loginHint: definition.login_hint,
    docsUrl: definition.docs_url,
    steps: definition.diagnostic_steps ?? []
  }));

  // Shared by the dedicated page, Settings and the first-run guide. The caller
  // owns layout; this component only detects, renders and reports results.
  let { computer, autoDetect = false }: { computer: Computer; autoDetect?: boolean } = $props();

  const services = createQuery<Page<AgentService>>(() => ({
    queryKey: ["agent-services", computer.id],
    queryFn: () => api<Page<AgentService>>(`/computers/${computer.id}/agent-services`),
    refetchInterval: 10_000
  }));

  let diagnostics = $state<Record<string, ProgrammingToolDiagnostic | undefined>>({});
  let running = $state<Record<string, boolean | undefined>>({});
  let failures = $state<Record<string, unknown | undefined>>({});
  let activeProvider = $state<string | null>(null);
  const activeTool = $derived(tools.find((tool) => tool.provider === activeProvider) ?? null);

  function serviceFor(provider: string): AgentService | undefined {
    return services.data?.items.find((item) => item.provider === provider);
  }

  function connectionState(
    service?: AgentService
  ): "connected" | "needs_auth" | "unavailable" | "unknown" {
    if (!service) return "unknown";
    if (service.status === "ready" && service.login_status === "authenticated") return "connected";
    if (service.login_status === "required" || service.status === "probing") return "needs_auth";
    return "unavailable";
  }

  function stateLabel(state: ReturnType<typeof connectionState>): string {
    return t(`programmingTools.state.${state}`);
  }

  function stateDescription(state: ReturnType<typeof connectionState>): string {
    return t(`programmingTools.state.${state}Desc`);
  }

  async function run(tool: ToolDefinition) {
    running[tool.provider] = true;
    failures[tool.provider] = undefined;
    try {
      diagnostics[tool.provider] = await api<ProgrammingToolDiagnostic>(
        `/computers/${computer.id}/programming-tools/${tool.provider}/diagnose`,
        { method: "POST" }
      );
      await queryClient.invalidateQueries({ queryKey: ["agent-services"] });
    } catch (error) {
      failures[tool.provider] = error;
    } finally {
      running[tool.provider] = false;
    }
  }

  // Auto-detection only fills gaps: tools that already report ready and
  // authenticated keep their stored result, so stepping back and forth or
  // reopening the guide never re-runs a full diagnosis. Unknown or not-ready
  // tools (for example a CLI the user just signed in to elsewhere) are
  // re-checked once the stored services have loaded.
  function needsDetection(provider: string): boolean {
    const service = serviceFor(provider);
    return !(service && service.status === "ready" && service.login_status === "authenticated");
  }

  let autoStarted = false;
  $effect(() => {
    if (!autoDetect || autoStarted || !services.isSuccess) return;
    autoStarted = true;
    for (const tool of tools) {
      if (needsDetection(tool.provider)) void run(tool);
    }
  });

  function openDiagnostic(tool: ToolDefinition) {
    activeProvider = tool.provider;
    if (!diagnostics[tool.provider] && !running[tool.provider]) void run(tool);
  }

  function closeDiagnostic() {
    activeProvider = null;
  }

  const activePendingSteps = $derived.by((): ProgrammingToolDiagnosticStep[] => {
    const tool = activeTool;
    if (!tool) return [];
    return tool.steps.map(({ id, label }, index) => ({
      id,
      label:
        t(`programmingTools.steps.${tool.provider}.${id}`) ===
        `programmingTools.steps.${tool.provider}.${id}`
          ? label
          : t(`programmingTools.steps.${tool.provider}.${id}`),
      status: index === 0 ? "running" : "pending",
      message: index === 0 ? t("programmingTools.runningStep") : t("programmingTools.waitingStep")
    }));
  });

  function stepClass(status: string): string {
    if (status === "success") return "text-success";
    if (status === "failed") return "text-danger";
    if (status === "warning") return "text-warning";
    return "text-muted";
  }

  function diagnosticSummary(item: ProgrammingToolDiagnostic): string {
    if (!item.available) return t("programmingTools.resultUnavailable");
    if (!item.authenticated) return t("programmingTools.resultNeedsAuth");
    return t("programmingTools.resultConnected");
  }
</script>

<div class="programming-tools-panel min-w-0">
  <div
    class="programming-tools-heading programming-tools-grid min-h-10 items-center border-b border-border bg-surface-subtle px-5 text-2xs font-semibold uppercase tracking-[0.04em] text-muted"
  >
    <span>{t("programmingTools.columns.tool")}</span>
    <span>{t("programmingTools.columns.status")}</span>
    <span>{t("programmingTools.columns.version")}</span>
    <span class="programming-tools-last-checked">{t("programmingTools.columns.lastChecked")}</span>
    <span class="text-right">{t("programmingTools.columns.action")}</span>
  </div>

  <ul aria-label={t("programmingTools.supportedList")}>
    {#each tools as tool (tool.provider)}
      {@const service = serviceFor(tool.provider)}
      {@const state = connectionState(service)}
      <li
        class="programming-tools-row programming-tools-grid min-h-[92px] items-center border-b border-border px-5 py-3"
        data-programming-tool={tool.provider}
        data-tool-state={state}
      >
        <div class="flex min-w-0 items-center gap-3">
          <span
            class="grid size-10 shrink-0 place-items-center rounded-md border border-border bg-surface-subtle text-text"
          >
            <SquareTerminal size={20} aria-hidden="true" />
          </span>
          <span class="min-w-0">
            <strong class="block truncate text-sm font-semibold">{tool.name}</strong>
            <small class="mt-1 block truncate text-xs text-muted"
              >{tool.descriptionKey ? t(tool.descriptionKey) : tool.description}</small
            >
            <code class="programming-tools-command mt-1 hidden break-all text-2xs text-muted">{tool.command}</code>
          </span>
        </div>

        <div class="programming-tools-status min-w-0">
          {#if running[tool.provider]}
            <span class="mb-1 flex items-center gap-1.5 text-xs font-semibold text-muted">
              <LoaderCircle size={11} class="animate-spin" aria-hidden="true" />
              {t("programmingTools.diagnosing")}
            </span>
          {/if}
          <span
            class="inline-flex items-center gap-1.5 text-xs font-semibold {state === 'connected'
              ? 'text-success'
              : state === 'unavailable'
                ? 'text-danger'
                : state === 'needs_auth'
                  ? 'text-warning'
                  : 'text-muted'}"
          >
            <span
              class="size-2 shrink-0 rounded-full {state === 'connected'
                ? 'bg-success'
                : state === 'unavailable'
                  ? 'bg-danger'
                  : state === 'needs_auth'
                    ? 'bg-warning'
                    : 'bg-muted'}"
            ></span>
            {stateLabel(state)}
          </span>
          {#if state !== "connected"}
            <small class="mt-1 block truncate text-2xs text-muted" title={stateDescription(state)}
              >{stateDescription(state)}</small
            >
            {#if tool.installHint || tool.loginHint}
              <span class="mt-1.5 grid gap-0.5">
                {#if (state === "unavailable" || state === "unknown") && tool.installHint}
                  <small class="block break-all text-2xs text-secondary"
                    >{t("programmingTools.installLabel")}
                    <code class="rounded-sm bg-canvas px-1 py-0.5">{tool.installHint}</code></small
                  >
                {/if}
                {#if (state === "needs_auth" || state === "unknown") && tool.loginHint}
                  <small class="block break-all text-2xs text-secondary"
                    >{t("programmingTools.loginLabel")}
                    <code class="rounded-sm bg-canvas px-1 py-0.5">{tool.loginHint}</code></small
                  >
                {/if}
                <small class="block text-2xs text-muted"
                  >{t("programmingTools.setupHint")}{#if tool.docsUrl}
                    · <a
                      class="text-accent underline underline-offset-2"
                      href={tool.docsUrl}
                      target="_blank"
                      rel="noopener noreferrer">{t("programmingTools.docsLabel")}</a
                    >{/if}</small
                >
              </span>
            {/if}
          {/if}
        </div>

        <span
          class="programming-tools-version min-w-0 truncate text-xs font-medium tabular-nums text-secondary"
        >
          {service?.provider_version || "—"}
        </span>
        <span class="programming-tools-last-checked min-w-0 break-words text-xs tabular-nums text-muted">{formatTime(service?.last_probed_at)}</span>
        <Button
          class="programming-tools-action justify-self-end"
          type="button"
          onclick={() => openDiagnostic(tool)}
        >
          {#if running[tool.provider]}<LoaderCircle
              size={14}
              class="animate-spin"
              aria-hidden="true"
            />{:else}<Stethoscope size={14} aria-hidden="true" />{/if}{t(
            "programmingTools.diagnose"
          )}
        </Button>
      </li>
    {/each}
  </ul>
</div>

{#if activeTool}
  <Modal
    title={t("programmingTools.diagnosticTitle", { tool: activeTool.name })}
    description={t("programmingTools.diagnosticDesc")}
    onClose={closeDiagnostic}
  >
    <div class="grid gap-4">
      {#if failures[activeTool.provider]}
        <ErrorNotice error={failures[activeTool.provider]} />
      {/if}

      {#if diagnostics[activeTool.provider]}
        {@const diagnostic = diagnostics[activeTool.provider]!}
        <section
          class="flex items-start gap-3 rounded-md border px-3.5 py-3 {diagnostic.available &&
          diagnostic.authenticated
            ? 'border-success/30 bg-success-subtle'
            : diagnostic.available
              ? 'border-warning/30 bg-warning-subtle'
              : 'border-danger/30 bg-danger-subtle'}"
        >
          {#if diagnostic.available && diagnostic.authenticated}
            <CheckCircle2 size={19} class="mt-0.5 shrink-0 text-success" />
          {:else if diagnostic.available}
            <Unplug size={19} class="mt-0.5 shrink-0 text-warning" />
          {:else}
            <XCircle size={19} class="mt-0.5 shrink-0 text-danger" />
          {/if}
          <span class="min-w-0">
            <strong class="block text-sm">{diagnosticSummary(diagnostic)}</strong>
            <small class="mt-1 block text-xs text-secondary">
              {#if diagnostic.provider_version}{t("programmingTools.detectedVersion", {
                  version: diagnostic.provider_version
                })}{:else}{t("programmingTools.noVersionDetected")}{/if}
              {#if diagnostic.models?.length}
                · {t("programmingTools.modelCount", { count: diagnostic.models.length })}{/if}
            </small>
            {#if !diagnostic.available && activeTool.installHint}
              <small class="mt-1.5 block break-all text-2xs text-secondary"
                >{t("programmingTools.installLabel")}
                <code class="rounded-sm bg-canvas px-1 py-0.5">{activeTool.installHint}</code
                ></small
              >
            {/if}
            {#if diagnostic.available && !diagnostic.authenticated && activeTool.loginHint}
              <small class="mt-1.5 block break-all text-2xs text-secondary"
                >{t("programmingTools.loginLabel")}
                <code class="rounded-sm bg-canvas px-1 py-0.5">{activeTool.loginHint}</code></small
              >
            {/if}
            {#if (!diagnostic.available || !diagnostic.authenticated) && activeTool.docsUrl}
              <small class="mt-1 block text-2xs text-muted"
                >{t("programmingTools.setupHint")} ·
                <a
                  class="text-accent underline underline-offset-2"
                  href={activeTool.docsUrl}
                  target="_blank"
                  rel="noopener noreferrer">{t("programmingTools.docsLabel")}</a
                ></small
              >
            {/if}
          </span>
        </section>
      {/if}

      <section
        class="overflow-hidden rounded-md border border-border bg-surface-subtle"
        aria-live="polite"
      >
        <header class="flex min-h-10 items-center justify-between border-b border-border px-3.5">
          <span class="text-xs font-semibold">{t("programmingTools.diagnosticLog")}</span>
          {#if running[activeTool.provider]}
            <span class="inline-flex items-center gap-1.5 text-2xs text-muted"
              ><LoaderCircle size={12} class="animate-spin" />{t(
                "programmingTools.diagnosing"
              )}</span
            >
          {:else if diagnostics[activeTool.provider]}
            <span class="inline-flex items-center gap-1.5 text-2xs text-muted"
              ><Clock3 size={12} />{formatTime(diagnostics[activeTool.provider]!.finished_at)}</span
            >
          {/if}
        </header>
        <ol class="divide-y divide-border font-mono">
          {#each diagnostics[activeTool.provider]?.steps ?? activePendingSteps as step, index (step.id)}
            <li
              class="grid grid-cols-[22px_minmax(0,1fr)_auto] items-start gap-2 px-3.5 py-3 text-xs"
            >
              <span class="pt-px {stepClass(step.status)}">
                {#if step.status === "success"}<CheckCircle2 size={15} />
                {:else if step.status === "failed"}<XCircle size={15} />
                {:else if step.status === "warning"}<AlertTriangle size={15} />
                {:else if step.status === "running"}<LoaderCircle size={15} class="animate-spin" />
                {:else}<MinusCircle size={15} />{/if}
              </span>
              <span class="min-w-0">
                <strong class="block truncate font-sans text-xs font-semibold text-text"
                  >{index + 1}. {step.label}</strong
                >
                <span
                  class="mt-1 block break-words font-sans text-2xs leading-relaxed text-secondary"
                  >{step.message}</span
                >
                {#if step.detail}<code
                    class="mt-1.5 block break-all rounded-sm bg-canvas px-2 py-1.5 text-[10px] leading-relaxed text-muted"
                    >{step.detail}</code
                  >{/if}
              </span>
              {#if step.duration_ms !== undefined && step.status !== "skipped"}
                <span class="pt-px text-[10px] tabular-nums text-muted">{step.duration_ms}ms</span>
              {/if}
            </li>
          {/each}
        </ol>
      </section>

      <section class="rounded-md border border-border bg-surface-subtle px-3.5 py-3">
        <ProviderExecutableEditor
          provider={activeTool.provider}
          name={activeTool.name}
          compact
        />
      </section>

    </div>
    {#snippet footer()}
      <div class="flex flex-wrap justify-between gap-2">
        <Button
          type="button"
          disabled={running[activeTool.provider]}
          onclick={() => void run(activeTool!)}
          ><RefreshCw size={14} class={running[activeTool.provider] ? "animate-spin" : ""} />{t(
            "programmingTools.runAgain"
          )}</Button
        >
        <Button
          variant="primary"
          type="button"
          onclick={closeDiagnostic}>{t("common.close")}</Button
        >
      </div>
    {/snippet}
  </Modal>
{/if}

<style>
  .programming-tools-panel {
    container-type: inline-size;
  }

  .programming-tools-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 180px) minmax(0, 140px) minmax(0, 170px) 128px;
    column-gap: 16px;
  }

  /* The same panel lives in the settings page, a modal and a narrow main pane.
     Its actual available width, not the viewport, decides which columns fit. */
  @container (max-width: 960px) {
    .programming-tools-grid {
      grid-template-columns: minmax(0, 1fr) minmax(0, 170px) minmax(0, 120px) 128px;
    }
    .programming-tools-last-checked {
      display: none;
    }
  }

  @container (max-width: 680px) {
    .programming-tools-heading {
      display: none;
    }
    .programming-tools-row {
      grid-template-columns: minmax(0, 1fr) auto;
      column-gap: 12px;
      padding-inline: 16px;
    }
    .programming-tools-status {
      grid-column: 1;
      margin-top: 12px;
    }
    .programming-tools-version {
      grid-column: 1;
      margin-top: 4px;
    }
    .programming-tools-command {
      display: block;
    }
    :global(.programming-tools-action) {
      grid-column: 2;
      grid-row: 1 / span 3;
    }
  }
</style>
