<script lang="ts">
  import type { HTMLButtonAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { LoaderCircle } from "lucide-svelte";
  let { ref = $bindable<HTMLButtonElement>(), type = "button", variant = "secondary", controlSize = "md", icon = false, loading = false, disabled = false, class: className = "", children, ...rest }:
    Omit<HTMLButtonAttributes, "children"> & { ref?: HTMLButtonElement; variant?: "primary" | "secondary" | "ghost" | "danger"; controlSize?: "sm" | "md" | "lg"; icon?: boolean; loading?: boolean; children?: Snippet } = $props();
</script>

<button {...rest} {type} disabled={disabled || loading} aria-busy={loading || undefined} bind:this={ref} class={`am-button ${className}`} data-variant={variant} data-size={controlSize} data-icon={icon || undefined}>
  {#if loading}<LoaderCircle size={14} class="animate-spin" aria-hidden="true" />{/if}
  {@render children?.()}
</button>
