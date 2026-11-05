<script lang="ts">
  import type { HTMLInputAttributes } from "svelte/elements";
  import { currentField } from "./fieldContext";
  let { ref = $bindable<HTMLInputElement>(), inheritField = true, type: _type, ...rest }: HTMLInputAttributes & { ref?: HTMLInputElement; inheritField?: boolean } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
</script>

<input {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} type="file" bind:this={ref} />
