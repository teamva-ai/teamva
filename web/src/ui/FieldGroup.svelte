<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLAttributes } from "svelte/elements";
  let { children, actions, columns = 2, collapseAt = "md", class: className = "", ...rest }: Omit<HTMLAttributes<HTMLDivElement>, "children"> & {
    children: Snippet; actions?: Snippet; columns?: 1 | 2 | 3; collapseAt?: "sm" | "md" | "lg";
  } = $props();
</script>

<!-- Keep Field children direct: each row shares label/control/hint tracks, so
     wrapped labels and asymmetric hints cannot move a neighboring control. -->
<div {...rest} class={`am-field-group am-field-group--${collapseAt} ${className}`} data-has-actions={actions ? "true" : undefined} style:--am-field-columns={columns}>
  {@render children()}
  {#if actions}<div class="am-field-group__actions"><div class="am-field-group__action-controls">{@render actions()}</div></div>{/if}
</div>
