<script lang="ts">
  import type { HTMLTextareaAttributes } from "svelte/elements";
  import { currentField } from "./fieldContext";
  let { value = $bindable<string | undefined>(""), ref = $bindable<HTMLTextAreaElement>(), rows = 3, autoGrow = false, inheritField = true, class: className = "", oninput, ...rest }:
    Omit<HTMLTextareaAttributes, "value"> & { value?: string; ref?: HTMLTextAreaElement; autoGrow?: boolean; inheritField?: boolean } = $props();
  const context = currentField();
  const field = $derived(inheritField ? context : undefined);
  function resize() {
    if (!autoGrow || !ref) return;
    ref.style.height = "auto";
    ref.style.height = `${Math.min(Math.max(ref.scrollHeight, 96), 480)}px`;
  }
  $effect(() => { void value; resize(); });
</script>

<textarea {...rest} id={rest.id ?? field?.controlId} aria-labelledby={rest["aria-labelledby"] ?? (rest["aria-label"] ? undefined : field?.labelId)} aria-describedby={rest["aria-describedby"] ?? field?.descriptionId} {rows} bind:value bind:this={ref} class={`am-textarea ${className}`} oninput={(event) => {
  resize();
  oninput?.(event);
}}></textarea>
