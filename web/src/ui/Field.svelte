<script lang="ts">
  // Shared label, control and hint layout; business pages own only field data.
  import { setContext, type Snippet } from "svelte";
  import { fieldContextKey, type FieldContext } from "./fieldContext";

  let { label, hint, id, children }: { label: string; hint?: string; id?: string; children: Snippet } = $props();
  const generated = $props.id();
  const field: FieldContext = { get controlId() { return id || `${generated}-control`; }, labelId: `${generated}-label`, get descriptionId() { return hint ? `${generated}-hint` : undefined; } };
  setContext(fieldContextKey, field);
</script>

<div class="am-field text-xs font-medium text-secondary">
  <label class="am-field__label" id={field.labelId} for={field.controlId}>{label}</label>
  <div class="am-field__control">{@render children()}</div>
  {#if hint}<small id={field.descriptionId} class="am-field__hint text-xs font-normal leading-[1.4] text-muted">{hint}</small>{/if}
</div>
