<script lang="ts">
  import Input from "./Input.svelte";
  import Button from "./Button.svelte";
  import { ChevronDown } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  // ModelPicker renders every model surface the same way: the provider's
  // supported models are one click away (datalist suggestions plus optional
  // chips) while the field stays a free-text input for custom names.
  let {
    models = [],
    value = $bindable(""),
    label = "",
    placeholder = "",
    chips = false,
    oncommit
  }: {
    models?: string[];
    value?: string;
    label?: string;
    placeholder?: string;
    chips?: boolean;
    oncommit?: (value: string) => void;
  } = $props();

  const listID = `model-options-${Math.random().toString(36).slice(2, 10)}`;
  let expanded = $state(false);
  let input = $state<HTMLInputElement>();
  const visibleModels = $derived(expanded ? models :
    (models.indexOf(value) >= 8 ? [value, ...models.filter((option) => option !== value)].slice(0, 8) : models.slice(0, 8)));
</script>

<div class="am-model-picker relative min-w-0 w-full" data-has-models={models.length > 0 || undefined}>
<Input
  type="text"
  {placeholder}
  list={listID}
  bind:value
  bind:ref={input}
  maxlength={500}
  autocomplete="off"
  spellcheck={false}
  aria-label={label || undefined}
  onchange={(event) => oncommit?.(event.currentTarget.value)}
  onkeydown={(event) => {
    if (event.key === "Enter" && oncommit) {
      event.preventDefault();
      oncommit(event.currentTarget.value);
    }
  }}
/>
{#if models.length}<Button type="button" icon controlSize="sm" variant="ghost" class="am-model-picker__button am-model-picker__trigger text-muted" aria-label={t("controls.chooseModel")} aria-controls={listID} onclick={() => {
  input?.focus({ preventScroll: true });
  try { input?.showPicker?.(); } catch { /* Older WebViews keep keyboard/type-ahead datalist selection. */ }
}}><ChevronDown size={14} aria-hidden="true" /></Button>{/if}
</div>
{#if models.length}
  <datalist id={listID}>
    {#each models as option (option)}<option value={option}></option>{/each}
  </datalist>
  {#if chips}
    <div class="mt-1.5 flex max-h-60 flex-wrap gap-1.5 overflow-y-auto" aria-label={label || undefined}>
      {#each visibleModels as option (option)}
        <Button controlSize="sm" variant="ghost"
          class="am-model-picker__button cursor-pointer rounded-full border px-2 py-0.5 text-2xs font-normal transition-colors {value ===
          option
            ? 'border-accent bg-accent-subtle text-accent'
            : 'border-border text-muted hover:border-border-strong hover:text-text'}"
          type="button"
          onclick={() => { value = option; oncommit?.(option); }}>{option}</Button
        >
      {/each}
    </div>
    {#if models.length > 8}<Button variant="ghost" controlSize="sm" class="am-model-picker__button mt-1 w-fit text-xs text-muted" aria-expanded={expanded} onclick={() => { expanded = !expanded; }}>{t(expanded ? "controls.modelShowLess" : "controls.modelShowMore", { count: models.length })}</Button>{/if}
  {/if}
{/if}
