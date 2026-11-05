<script lang="ts">
  import { Button, Input, Select } from "../ui";
  import WorkFilterBuilder from "./WorkFilterBuilder.svelte";
  import { t } from "../lib/i18n.svelte";
  import { workFields, type WorkCondition } from "../lib/workRecords";
  let { value = $bindable(), depth = 0 }: { value: WorkCondition; depth?: number } = $props();
</script>

<fieldset class="min-w-0 rounded-md border border-border p-3" data-work-filter-group>
  <legend class="px-1 text-xs">{t("work.advanced")} · {depth + 1}</legend>
  <div class="mb-2 flex flex-wrap gap-2">
    <Select aria-label="AND / OR" class="!w-auto" bind:value={value.join}
      ><option value="and">AND</option><option value="or">OR</option></Select
    >
    <button
      type="button"
      class="rounded border border-border px-2 text-xs"
      onclick={() =>
        (value.conditions ??= []).push({ field: "action", operator: "contains", value: "" })}
      >{t("work.condition")}</button
    >
    {#if depth < 4}<button
        type="button"
        class="rounded border border-border px-2 text-xs"
        onclick={() => (value.conditions ??= []).push({ join: "or", conditions: [] })}
        >{t("work.group")}</button
      >{/if}
  </div>
  <div class="grid gap-2">
    {#each value.conditions ?? [] as condition, index}
      <div class="flex min-w-0 items-start gap-2">
        <div class="min-w-0 flex-1">
          {#if condition.join}<WorkFilterBuilder
              bind:value={value.conditions![index]}
              depth={depth + 1}
            />
          {:else}<div class="grid grid-cols-[1fr_1fr_1.3fr] gap-2 max-sm:grid-cols-1">
              <Select aria-label={t("work.object")} bind:value={condition.field}
                >{#each workFields as field}<option value={field}>{field}</option>{/each}</Select
              >
              <Select aria-label={t("work.condition")} bind:value={condition.operator}
                >{#each ["eq", "ne", "contains", "not_contains"] as operator}<option
                    value={operator}>{t(`work.${operator}`)}</option
                  >{/each}</Select
              >
              <Input
                aria-label={t("work.value")}
                placeholder={t("work.value")}
                bind:value={condition.value}
                maxlength={500}
              />
            </div>{/if}
        </div>
        <Button
          type="button"
          icon
          class="text-muted"
          aria-label={t("common.delete")}
          onclick={() => value.conditions?.splice(index, 1)}>×</Button
        >
      </div>
    {/each}
  </div>
</fieldset>
