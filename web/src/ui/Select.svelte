<script lang="ts" generics="T = string">
  import type { Snippet } from "svelte";
  import type { HTMLSelectAttributes } from "svelte/elements";
  import { ChevronDown } from "lucide-svelte";
  import { currentField } from "./fieldContext";
  let { value = $bindable<T>(), ref = $bindable<HTMLSelectElement>(), controlSize = "md", inheritField = true, class: className = "", children, disabled, ...rest }:
    Omit<HTMLSelectAttributes, "value" | "size" | "multiple" | "children"> & {
      value?: T; ref?: HTMLSelectElement; controlSize?: "sm" | "md" | "lg"; children?: Snippet; inheritField?: boolean;
    } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
</script>

<span class={`am-select ${className}`} data-size={controlSize} data-disabled={disabled || undefined}>
  <select {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} {disabled} bind:value bind:this={ref} class="am-select__control">
    {@render children?.()}
  </select>
  <ChevronDown size={14} class="am-select__icon" aria-hidden="true" />
</span>
