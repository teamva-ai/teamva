<script lang="ts">
  import type { HTMLInputAttributes } from "svelte/elements";
  import { currentField } from "./fieldContext";
  let { checked = $bindable(false), ref = $bindable<HTMLInputElement>(), inheritField = true, disabled, class: className = "", type: _type, role: _role, ...rest }:
    Omit<HTMLInputAttributes, "checked"> & { checked?: boolean; ref?: HTMLInputElement; inheritField?: boolean } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
</script>

<span class={`am-switch ${className}`} data-disabled={disabled || undefined}>
  <input {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} {disabled} type="checkbox" role="switch" bind:checked bind:this={ref} class="am-switch__input" />
  <span class="am-switch__track" aria-hidden="true"></span>
</span>
