<script lang="ts">
  import type { HTMLInputAttributes } from "svelte/elements";
  import { currentField } from "./fieldContext";
  let { checked = $bindable(false), ref = $bindable<HTMLInputElement>(), inheritField = true, class: className = "", type: _type, ...rest }:
    Omit<HTMLInputAttributes, "checked"> & { checked?: boolean; ref?: HTMLInputElement; inheritField?: boolean } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
</script>

<input {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} type="checkbox" bind:checked bind:this={ref} class={`am-checkbox ${className}`} />
