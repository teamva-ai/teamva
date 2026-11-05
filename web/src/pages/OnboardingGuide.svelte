<script lang="ts">
  import { createMutation } from "@tanstack/svelte-query";
  import { ArrowLeft, ArrowRight, Bot, CheckCircle2, SquareTerminal } from "lucide-svelte";
  import { onDestroy, tick } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import { navigate } from "../lib/router.svelte";
  import { clearOnboardingRequest } from "../lib/onboardingOverlay.svelte";
  import type { Computer, Onboarding as OnboardingState } from "../lib/types";
  import { Button, Modal } from "../ui";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import ProgrammingToolsPanel from "../components/ProgrammingToolsPanel.svelte";

  let { computer }: { computer: Computer } = $props();
  let step = $state(0), alive = true;
  onDestroy(() => { alive = false; });
  const steps = $derived([t("onboarding.stepCli"), t("onboarding.stepGeneral")]);
  const dismiss = createMutation<OnboardingState, Error, string>(() => ({
    mutationFn: () => api("/system/onboarding", { method: "PUT", body: { dismissed: true } }),
    onSuccess: (value, target) => {
      queryClient.setQueryData(["onboarding"], value);
      clearOnboardingRequest();
      navigate(target, { replace: true });
    }
  }));
  $effect(() => {
    const currentStep = step;
    void tick().then(() => {
      if (alive && currentStep === step) document.querySelector<HTMLElement>("[data-onboarding-dialog] [data-onboarding-step] h2")?.focus({ preventScroll: true });
    });
  });
</script>

<Modal title={t("onboarding.title")} description={t("onboarding.subtitle")}
  dismissible={false} onClose={() => {}} maxWidth={860} layer={90} centered
  initialFocus="[data-onboarding-step] h2" data-onboarding-dialog>
  {#snippet headerExtra()}
    <ol class="mt-4 flex items-center justify-center gap-2 text-xs" aria-label={t("onboarding.title")}>
      {#each steps as label, index (label)}
        <li class="flex items-center gap-2" aria-current={step === index ? "step" : undefined}>
          <span class="grid size-5 place-items-center rounded-full border {step > index ? 'border-success text-success' : step === index ? 'border-accent text-accent' : 'border-border text-muted'}">
            {#if step > index}<CheckCircle2 size={12} />{:else}{index + 1}{/if}
          </span>{label}
          {#if index < steps.length - 1}<span class="h-px w-6 bg-border" aria-hidden="true"></span>{/if}
        </li>
      {/each}
    </ol>
  {/snippet}
  <div data-onboarding-step>
    {#if dismiss.error}<ErrorNotice error={dismiss.error} />{/if}
    {#if step === 0}
      <section class="grid gap-4 py-3">
        <h2 tabindex="-1" class="flex items-center justify-center gap-2 text-md font-semibold"><SquareTerminal size={17} />{t("onboarding.stepCli")}</h2>
        <p class="text-center text-sm text-text-secondary">{t("onboarding.bundledOpenCode")}</p>
        <ProgrammingToolsPanel {computer} autoDetect />
      </section>
    {:else}
      <section class="mx-auto grid max-w-[560px] justify-items-center gap-4 py-4 text-center">
        <Bot size={32} class="text-accent" />
        <h2 tabindex="-1" class="text-md font-semibold">{t("onboarding.meetLumi")}</h2>
        <p class="text-sm leading-relaxed text-text-secondary">{t("onboarding.lumiBuiltin")}</p>
        <p class="text-xs leading-relaxed text-muted">{t("controls.agentConfigurationCentralized")}</p>
      </section>
    {/if}
  </div>
  {#snippet footer()}
    <div class="flex flex-wrap items-center justify-between gap-2">
      <Button disabled={step === 0 || dismiss.isPending} onclick={() => { step = 0; }}><ArrowLeft size={14} />{t("onboarding.back")}</Button>
      <Button variant="ghost" disabled={dismiss.isPending} onclick={() => dismiss.mutate("/workbench")}>{t("onboarding.skipAll")}</Button>
      <Button variant="primary" loading={dismiss.isPending} onclick={() => {
        if (step === 0) step = 1;
        else dismiss.mutate("/issues");
      }}>{t(step === 0 ? "onboarding.next" : "onboarding.finish")}<ArrowRight size={14} /></Button>
    </div>
  {/snippet}
</Modal>
