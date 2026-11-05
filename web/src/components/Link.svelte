<script lang="ts">
  import type { Snippet } from "svelte";
  import { navigate } from "../lib/router.svelte";

  let { to, class: className, children, onClick, ariaLabel, onclick, title }: {
    to: string;
    class?: string;
    children?: Snippet;
    onClick?: () => void;
    onclick?: () => void;
    ariaLabel?: string;
    title?: string;
  } = $props();

  function handleClick(event: MouseEvent) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) {
      return;
    }
    event.preventDefault();
    (onClick ?? onclick)?.();
    navigate(to);
  }
</script>

<a href={to} class={className} aria-label={ariaLabel} title={title} onclick={handleClick}>
  {@render children?.()}
</a>
