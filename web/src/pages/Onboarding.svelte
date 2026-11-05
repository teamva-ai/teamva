<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { LoaderCircle } from "lucide-svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import type { Computer, Page } from "../lib/types";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import OnboardingGuide from "./OnboardingGuide.svelte";
  import Modal from "../ui/Modal.svelte";

  // The guide only mounts once the Computer is known, so every query inside it
  // keys off a stable id from the first render instead of switching keys.
  const computers = createQuery<Page<Computer>>(() => ({
    queryKey: ["computers"],
    queryFn: () => api<Page<Computer>>("/computers"),
    refetchInterval: 5_000
  }));
  const computer = $derived(computers.data?.items[0]);
</script>

{#if computer}
  <OnboardingGuide {computer} />
{:else}
  <Modal title={t("onboarding.title")} dismissible={false} onClose={() => {}} maxWidth={860} layer={90} centered data-onboarding-dialog>
      {#if computers.error}
        <ErrorNotice error={computers.error} />
      {:else}
        <p class="flex items-center justify-center gap-2 text-sm text-muted">
          <LoaderCircle size={14} class="animate-spin" />{t("common.loading")}
        </p>
      {/if}
  </Modal>
{/if}
