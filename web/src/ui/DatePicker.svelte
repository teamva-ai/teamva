<script lang="ts">
  import type { ComponentProps } from "svelte";
  import Input from "./Input.svelte";
  import { toLocalDateTime, fromLocalDateTime } from "./dateValue";
  let { value = $bindable(""), type = "date", iso = false, ref = $bindable<HTMLInputElement>(), oninput, ...rest }:
    Omit<ComponentProps<typeof Input<string>>, "type"> & { type?: "date" | "datetime-local" | "time"; value?: string; iso?: boolean } = $props();
</script>

{#if iso && type === "datetime-local"}
  <Input {...rest} {type} step={rest.step ?? "any"} defaultValue={rest.defaultValue == null ? undefined : toLocalDateTime(String(rest.defaultValue))}
    bind:value={() => toLocalDateTime(value), (next: string) => { value = fromLocalDateTime(next); }} bind:ref {oninput} />
{:else}
  <Input {...rest} {type} bind:value bind:ref {oninput} />
{/if}
