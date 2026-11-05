<script lang="ts" generics="T extends string | number | null | undefined = string">
  import type { HTMLInputAttributes } from "svelte/elements";
  import { currentField } from "./fieldContext";
  let { value = $bindable<T>(), ref = $bindable<HTMLInputElement>(), type = "text", controlSize = "md", inheritField = true, class: className = "", ...rest }:
    Omit<HTMLInputAttributes, "value" | "size"> & { value?: T; ref?: HTMLInputElement; controlSize?: "sm" | "md" | "lg"; inheritField?: boolean } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
</script>

<input {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} {type} bind:value bind:this={ref} data-size={controlSize} class={`am-input ${className}`} />
