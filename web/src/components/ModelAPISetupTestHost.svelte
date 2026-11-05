<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import { t } from "../lib/i18n.svelte";
  import { Modal } from "../ui";
  import ModelAPISetup, { type ConfiguredModelProvider } from "./ModelAPISetup.svelte";

  let { onSaved = async () => {}, onReadyChange = () => {}, onBusyChange = () => {} }: {
    onSaved?: (value: ConfiguredModelProvider) => Promise<void>;
    onReadyChange?: (ready: boolean) => void;
    onBusyChange?: (busy: boolean) => void;
  } = $props();
</script>

<QueryClientProvider client={queryClient}>
  <Modal title={t("onboarding.stepAPI")} onClose={() => {}}>
    <ModelAPISetup {onSaved} {onReadyChange} {onBusyChange} />
  </Modal>
</QueryClientProvider>
