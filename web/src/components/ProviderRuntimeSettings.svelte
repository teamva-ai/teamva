<script lang="ts">
  import { t } from "../lib/i18n.svelte";
  import { providerDefinitions } from "../lib/providers";
  import ProviderExecutableEditor from "./ProviderExecutableEditor.svelte";
</script>

<section
  class="overflow-hidden rounded-lg border border-border bg-surface"
  aria-label={t("programmingTools.runtimeTitle")}
>
  <header class="border-b border-border px-5 py-4">
    <h2 class="text-sm font-semibold">{t("programmingTools.runtimeTitle")}</h2>
    <p class="mt-1 max-w-[640px] text-pretty text-xs leading-relaxed text-text-secondary">
      {t("programmingTools.runtimeDesc")}
    </p>
  </header>
  <div class="grid divide-y divide-border">
    {#each providerDefinitions as definition (definition.id)}
      <div class="px-5 py-4">
        <ProviderExecutableEditor provider={definition.id} name={definition.display_name} />
      </div>
    {/each}
  </div>
</section>
